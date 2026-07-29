package instance

import (
	"crypto/subtle"
	"encoding/base64"
)

// CompareConstantTime returns true iff a == b. Uses
// crypto/subtle.ConstantTimeCompare so a timing-attack can't reveal
// the key prefix.
//
// Post 2026-07-29 (drop-bcrypt), this is the comparison used for
// bearer auth on /api/v1/*. The bcrypt helpers (BcryptAPIKey,
// VerifyAPIKey) are removed — the API key is stored as plaintext and
// authenticated by direct comparison against the submitted value.
func CompareConstantTime(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// base64URLNoPad returns the base64url-encoded representation of b
// without padding. Used internally for API-key generation.
func base64URLNoPad(b []byte) string {
	return base64.RawURLEncoding.EncodeToString(b)
}
