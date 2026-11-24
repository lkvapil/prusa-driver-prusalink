package prusalink

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// maxReplyBytes bounds one answer from PrusaLink. The largest one this driver
// reads is a file listing.
const maxReplyBytes = 1 << 20

// client speaks HTTP to one PrusaLink. It signs a request with an API key when
// the endpoint names one. Otherwise it answers a Digest challenge with the user
// name and password the endpoint names.
//
// The last challenge is kept, so a request after the first is signed straight
// away and is not refused first. PrusaLink keeps a nonce for a while, and a
// stale one is answered with a new challenge, which is then answered once.
type client struct {
	http    *http.Client
	base    string
	timeout time.Duration

	apiKey   string
	user     string
	password string

	mu        sync.Mutex
	challenge *challenge
	count     uint32
}

// errStatus is an answer with a status this driver did not expect.
type errStatus struct {
	method string
	path   string
	status int
}

func (e errStatus) Error() string {
	return fmt.Sprintf("%s %s answered %d %s", e.method, e.path, e.status, http.StatusText(e.status))
}

// baseURL turns a bare host, a host and port, or a URL into the address of the
// PrusaLink web server. PrusaLink serves on port 80, and a URL keeps any path,
// for a printer behind a proxy.
func baseURL(address string) (string, error) {
	trimmed := strings.TrimSpace(address)
	if trimmed == "" {
		return "", fmt.Errorf("the endpoint names no address")
	}

	if !strings.Contains(trimmed, "://") {
		trimmed = "http://" + trimmed
	}

	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("%q is not an address: %w", address, err)
	}

	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("%q names the scheme %q, and PrusaLink speaks http and https", address, parsed.Scheme)
	}

	if parsed.Host == "" {
		return "", fmt.Errorf("%q names no host", address)
	}

	return strings.TrimSuffix(parsed.String(), "/"), nil
}

// get reads one JSON answer into out. A 204 leaves out as it is, which is how
// PrusaLink says that there is no job.
func (c *client) get(ctx context.Context, path string, out any) (int, error) {
	return c.do(ctx, http.MethodGet, path, out)
}

// do sends one request without a body and reads a JSON answer into out when out
// is not nil. A status of 400 or more is an error that carries the status.
func (c *client) do(ctx context.Context, method, path string, out any) (int, error) {
	callCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	resp, err := c.send(callCtx, method, path)
	if err != nil {
		return 0, err
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= http.StatusBadRequest {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxReplyBytes))

		return resp.StatusCode, errStatus{method: method, path: path, status: resp.StatusCode}
	}

	if out == nil || resp.StatusCode == http.StatusNoContent {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxReplyBytes))

		return resp.StatusCode, nil
	}

	if err = json.NewDecoder(io.LimitReader(resp.Body, maxReplyBytes)).Decode(out); err != nil {
		return resp.StatusCode, fmt.Errorf("the answer to %s %s is not readable: %w", method, path, err)
	}

	return resp.StatusCode, nil
}

// request sends one request with optional headers and body and discards the
// answer. length is the body's length, or -1 for no body. timeout bounds the
// request; zero leaves it to ctx, which is what an upload needs.
//
// A request with a body is not sent twice: the body is read once. A Digest
// printer has to have challenged this client before, which Send makes sure of.
func (c *client) request(
	ctx context.Context, method, path string, headers http.Header, body io.Reader, length int64, timeout time.Duration,
) (int, error) {
	if timeout > 0 {
		var cancel context.CancelFunc

		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	var resp *http.Response

	var err error

	if body == nil {
		resp, err = c.sendWith(ctx, method, path, headers)
	} else {
		resp, err = c.attemptWith(ctx, method, path, headers, body, length)
	}

	if err != nil {
		return 0, err
	}

	defer func() { _ = resp.Body.Close() }()

	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxReplyBytes))

	if resp.StatusCode >= http.StatusBadRequest {
		return resp.StatusCode, errStatus{method: method, path: path, status: resp.StatusCode}
	}

	return resp.StatusCode, nil
}

// send makes the request, and answers a Digest challenge once when the printer
// asks for one.
func (c *client) send(ctx context.Context, method, path string) (*http.Response, error) {
	return c.sendWith(ctx, method, path, nil)
}

// sendWith is send with extra headers.
func (c *client) sendWith(ctx context.Context, method, path string, headers http.Header) (*http.Response, error) {
	resp, err := c.attemptWith(ctx, method, path, headers, nil, -1)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusUnauthorized || c.user == "" {
		return resp, nil
	}

	offered, ok := parseChallenge(resp.Header.Get("WWW-Authenticate"))

	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxReplyBytes))
	_ = resp.Body.Close()

	if !ok {
		return nil, errStatus{method: method, path: path, status: http.StatusUnauthorized}
	}

	c.mu.Lock()
	c.challenge, c.count = &offered, 0
	c.mu.Unlock()

	return c.attemptWith(ctx, method, path, headers, nil, -1)
}

// attemptWith makes the request once, signed with what this client knows now.
func (c *client) attemptWith(
	ctx context.Context, method, path string, headers http.Header, body io.Reader, length int64,
) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return nil, fmt.Errorf("build %s %s: %w", method, path, err)
	}

	if length >= 0 && body != nil {
		req.ContentLength = length
	}

	for key, values := range headers {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}

	req.Header.Set("Accept", "application/json")

	if c.apiKey != "" {
		req.Header.Set("X-Api-Key", c.apiKey)
	}

	if header := c.sign(method, req.URL.RequestURI()); header != "" {
		req.Header.Set("Authorization", header)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", method, path, err)
	}

	return resp, nil
}

// sign is the Authorization header for one request, or empty before the first
// challenge and for a client that has no user name.
func (c *client) sign(method, uri string) string {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.challenge == nil || c.user == "" {
		return ""
	}

	c.count++

	return c.challenge.authorization(method, uri, c.user, c.password, c.count, "")
}
