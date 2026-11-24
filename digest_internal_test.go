package prusalink

import "testing"

// TestDigestAnswersAsPoorWSGIChecks holds the header to the challenge PoorWSGI
// 2.5 sends for PrusaLink 0.8.1, with a fixed client nonce, and to RFC 7616's
// plain MD5. The expected responses were worked out by the formula PoorWSGI
// uses (poorwsgi/digest.py, check_response).
func TestDigestAnswersAsPoorWSGIChecks(t *testing.T) {
	sess, ok := parseChallenge(`Digest realm="Administrator",qop="auth",algorithm="MD5-sess",nonce="abc",opaque="xyz"`)
	if !ok || sess.algorithm != "MD5-sess" || sess.opaque != "xyz" || !sess.qop {
		t.Fatalf("the PoorWSGI challenge read as %+v, %v", sess, ok)
	}

	// hash1 = md5(md5("maker:Administrator:secret"):abc:c0ffee)
	// response = md5(hash1:abc:00000001:c0ffee:auth:md5("GET:/api/v1/status"))
	got := sess.authorization("GET", "/api/v1/status", "maker", "secret", 1, "c0ffee")
	want := `Digest username="maker", realm="Administrator", nonce="abc", uri="/api/v1/status", ` +
		`response="` + expectedSess + `", algorithm=MD5-sess, opaque="xyz", qop=auth, nc=00000001, cnonce="c0ffee"`

	if got != want {
		t.Fatalf("the header is\n%s\nwant\n%s", got, want)
	}

	plain, ok := parseChallenge(`Digest realm="r", nonce="n"`)
	if !ok || plain.algorithm != "MD5" || plain.qop {
		t.Fatalf("a challenge with no algorithm read as %+v, %v", plain, ok)
	}

	if _, ok = parseChallenge(`Digest realm="r", nonce="n", algorithm="SHA-512-256"`); ok {
		t.Fatal("an algorithm this driver cannot answer was accepted")
	}
}

// expectedSess is the response worked out in Python with hashlib, outside this code.
const expectedSess = "63fd64a75aabb83f20ff54691d160e2f"
