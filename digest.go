package prusalink

import (
	"crypto/md5" //nolint:gosec // HTTP Digest in RFC 7616 with MD5 is what PrusaLink asks for. It protects nothing here.
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"strings"
)

// challenge is what a WWW-Authenticate header with the Digest scheme asks for.
// PrusaLink on a Raspberry Pi signs in a user this way. The default user name
// is "maker", and the password is on the printer's screen.
//
// PrusaLink 0.8.1 serves through PoorWSGI 2.5, which asks for the algorithm
// MD5-sess with qop auth and an opaque value, and refuses an answer whose
// algorithm is not exactly the one it asked for (poorwsgi/digest.py,
// check_credentials). Its nonce lasts 60 seconds, and an expired one is
// answered with a new challenge marked stale.
type challenge struct {
	realm     string
	nonce     string
	opaque    string
	algorithm string
	qop       bool
}

// parseChallenge reads a WWW-Authenticate header. ok is false for a header that
// is not Digest, or that names no realm or no nonce.
func parseChallenge(header string) (challenge, bool) {
	rest, found := strings.CutPrefix(strings.TrimSpace(header), "Digest ")
	if !found {
		return challenge{}, false
	}

	var c challenge

	hasRealm := false

	for _, part := range splitParams(rest) {
		key, value, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}

		value = strings.Trim(strings.TrimSpace(value), `"`)

		switch strings.ToLower(strings.TrimSpace(key)) {
		case "realm":
			c.realm, hasRealm = value, true
		case "nonce":
			c.nonce = value
		case "opaque":
			c.opaque = value
		case "algorithm":
			c.algorithm = value
		case "qop":
			for _, offered := range strings.Split(value, ",") {
				if strings.TrimSpace(offered) == "auth" {
					c.qop = true
				}
			}
		}
	}

	if c.algorithm == "" {
		c.algorithm = "MD5"
	}

	_, supported := hashOf(c.algorithm)

	return c, hasRealm && c.nonce != "" && supported
}

// hashOf is the hash an algorithm names, and false for one this driver does
// not speak. A -sess algorithm uses the same hash.
func hashOf(algorithm string) (func() hash.Hash, bool) {
	switch strings.ToUpper(strings.TrimSuffix(strings.ToLower(algorithm), "-sess")) {
	case "MD5":
		return md5.New, true
	case "SHA-256":
		return sha256.New, true
	default:
		return nil, false
	}
}

// splitParams splits the parameters of a challenge at the commas that are not
// inside a quoted value. A qop value such as "auth,auth-int" holds a comma.
func splitParams(s string) []string {
	var (
		out    []string
		quoted bool
		start  int
	)

	for i, r := range s {
		switch {
		case r == '"':
			quoted = !quoted
		case r == ',' && !quoted:
			out = append(out, s[start:i])
			start = i + 1
		}
	}

	return append(out, s[start:])
}

// authorization is the Authorization header for one request. count is the
// nonce count, which has to grow with each request on one nonce. cnonce is
// random when empty, and a test passes a fixed one.
func (c challenge) authorization(method, uri, user, password string, count uint32, cnonce string) string {
	if cnonce == "" {
		cnonce = randomHex()
	}

	newHash, _ := hashOf(c.algorithm)
	sum := func(text string) string { return hexOf(newHash, text) }

	ha1 := sum(user + ":" + c.realm + ":" + password)
	if strings.HasSuffix(strings.ToLower(c.algorithm), "-sess") {
		ha1 = sum(ha1 + ":" + c.nonce + ":" + cnonce)
	}

	ha2 := sum(method + ":" + uri)
	nc := fmt.Sprintf("%08x", count)

	response := sum(ha1 + ":" + c.nonce + ":" + ha2)
	if c.qop {
		response = sum(ha1 + ":" + c.nonce + ":" + nc + ":" + cnonce + ":auth:" + ha2)
	}

	parts := []string{
		`username="` + user + `"`,
		`realm="` + c.realm + `"`,
		`nonce="` + c.nonce + `"`,
		`uri="` + uri + `"`,
		`response="` + response + `"`,
		"algorithm=" + c.algorithm,
	}

	if c.opaque != "" {
		parts = append(parts, `opaque="`+c.opaque+`"`)
	}

	if c.qop {
		parts = append(parts, "qop=auth", "nc="+nc, `cnonce="`+cnonce+`"`)
	}

	return "Digest " + strings.Join(parts, ", ")
}

func hexOf(newHash func() hash.Hash, text string) string {
	h := newHash()
	_, _ = h.Write([]byte(text))

	return hex.EncodeToString(h.Sum(nil))
}

// randomHex is a client nonce. A failure of the system's random source leaves
// a fixed value, which the server still accepts. The nonce protects against
// replay, and the connection is on the local network.
func randomHex() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "0000000000000000"
	}

	return hex.EncodeToString(b[:])
}
