package prusalink

import (
	"crypto/md5" //nolint:gosec // HTTP Digest in RFC 7616 with MD5 is what PrusaLink asks for. It is not used to protect anything here.
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
)

// challenge is what a WWW-Authenticate header with the Digest scheme asks for.
// PrusaLink on a Raspberry Pi signs in a user this way. The default user name
// is "maker", and the password is on the printer's screen.
type challenge struct {
	realm  string
	nonce  string
	opaque string
	qop    bool
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
		case "qop":
			for _, offered := range strings.Split(value, ",") {
				if strings.TrimSpace(offered) == "auth" {
					c.qop = true
				}
			}
		}
	}

	return c, hasRealm && c.nonce != ""
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

	ha1 := md5Hex(user + ":" + c.realm + ":" + password)
	ha2 := md5Hex(method + ":" + uri)
	nc := fmt.Sprintf("%08x", count)

	response := md5Hex(ha1 + ":" + c.nonce + ":" + ha2)
	if c.qop {
		response = md5Hex(ha1 + ":" + c.nonce + ":" + nc + ":" + cnonce + ":auth:" + ha2)
	}

	parts := []string{
		`username="` + user + `"`,
		`realm="` + c.realm + `"`,
		`nonce="` + c.nonce + `"`,
		`uri="` + uri + `"`,
		`response="` + response + `"`,
		"algorithm=MD5",
	}

	if c.opaque != "" {
		parts = append(parts, `opaque="`+c.opaque+`"`)
	}

	if c.qop {
		parts = append(parts, "qop=auth", "nc="+nc, `cnonce="`+cnonce+`"`)
	}

	return "Digest " + strings.Join(parts, ", ")
}

func md5Hex(s string) string {
	sum := md5.Sum([]byte(s)) //nolint:gosec // see the import.

	return hex.EncodeToString(sum[:])
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
