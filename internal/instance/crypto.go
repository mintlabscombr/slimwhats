package instance

import (
	"encoding/base64"

	"golang.org/x/crypto/bcrypt"
)

// BcryptAPIKey returns the bcrypt hash of a plaintext API key. Cost
// 12 — same as the PRD spec. Exported for the API-key rotate flow
// (handlers/api_key.go) and for tests.
func BcryptAPIKey(plaintext string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(plaintext), 12)
	if err != nil {
		return "", err
	}
	return string(h), nil
}

// VerifyAPIKey returns nil if plaintext matches the stored bcrypt hash.
// Uses constant-time comparison internally (bcrypt package handles it).
func VerifyAPIKey(hash, plaintext string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plaintext)) == nil
}

// bcryptAPIKey is the unexported alias kept for internal callers that
// don't need the export.
func bcryptAPIKey(plaintext string) (string, error) { return BcryptAPIKey(plaintext) }

// base64URLNoPad returns the base64url-encoded representation of b
// without padding. Used internally for API-key generation.
func base64URLNoPad(b []byte) string {
	return base64.RawURLEncoding.EncodeToString(b)
}
