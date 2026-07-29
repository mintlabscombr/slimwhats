package config

import (
	"encoding/base64"
	"os"
	"strings"
	"testing"
)

func TestLoad_MissingPassword(t *testing.T) {
	os.Unsetenv("APP_MANAGER_PASSWORD")
	os.Unsetenv("APP_ENCRYPTION_KEY")
	_, err := Load()
	if err == nil {
		t.Fatal("expected error when APP_MANAGER_PASSWORD is missing")
	}
	if !strings.Contains(err.Error(), "APP_MANAGER_PASSWORD is required") {
		t.Errorf("error = %q, want it to mention APP_MANAGER_PASSWORD", err)
	}
}

// TestLoad_NoEncryptionKeyIsOK documents the hotfix: APP_ENCRYPTION_KEY
// is no longer required. Webhook secrets are stored as plaintext.
func TestLoad_NoEncryptionKeyIsOK(t *testing.T) {
	os.Setenv("APP_MANAGER_PASSWORD", "x")
	os.Unsetenv("APP_ENCRYPTION_KEY")
	c, err := Load()
	if err != nil {
		t.Fatalf("expected OK with no encryption key, got: %v", err)
	}
	if c.EncryptionKey != nil {
		t.Errorf("EncryptionKey should be nil when APP_ENCRYPTION_KEY is unset, got %v bytes", len(c.EncryptionKey))
	}
}

// TestLoad_EncryptionKeyStillValidates keeps the existing length check
// for the case where the operator has the env var in their .env (so
// old deployments don't silently break).
func TestLoad_EncryptionKeyWrongLength(t *testing.T) {
	os.Setenv("APP_MANAGER_PASSWORD", "x")
	os.Setenv("APP_ENCRYPTION_KEY", base64.StdEncoding.EncodeToString([]byte("shortkey")))
	_, err := Load()
	if err == nil {
		t.Fatal("expected error when APP_ENCRYPTION_KEY decodes to != 32 bytes")
	}
	if !strings.Contains(err.Error(), "must decode to 32 bytes") {
		t.Errorf("error = %q, want length complaint", err)
	}
}

func TestLoad_HappyPath(t *testing.T) {
	os.Setenv("APP_MANAGER_PASSWORD", "secret-pw")
	os.Setenv("APP_MANAGER_USERNAME", "ops")
	os.Setenv("APP_ENCRYPTION_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	os.Setenv("APP_HTTP_ADDR", ":9000")
	c, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.ManagerPassword != "secret-pw" {
		t.Errorf("ManagerPassword = %q, want %q", c.ManagerPassword, "secret-pw")
	}
	if c.ManagerUsername != "ops" {
		t.Errorf("ManagerUsername = %q, want %q", c.ManagerUsername, "ops")
	}
	if len(c.EncryptionKey) != 32 {
		t.Errorf("EncryptionKey length = %d, want 32", len(c.EncryptionKey))
	}
	if c.HTTPAddr != ":9000" {
		t.Errorf("HTTPAddr = %q, want :9000 (env override)", c.HTTPAddr)
	}
}

// TestLoad_HappyPathNoKey exercises the new "no APP_ENCRYPTION_KEY" path
// end-to-end: service must boot successfully without it.
func TestLoad_HappyPathNoKey(t *testing.T) {
	os.Setenv("APP_MANAGER_PASSWORD", "secret-pw")
	os.Unsetenv("APP_ENCRYPTION_KEY")
	c, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.ManagerPassword != "secret-pw" {
		t.Errorf("ManagerPassword = %q, want %q", c.ManagerPassword, "secret-pw")
	}
	if c.EncryptionKey != nil {
		t.Errorf("EncryptionKey should be nil, got %v bytes", len(c.EncryptionKey))
	}
}

// TestNormalizeHTTPAddr is a small table test for the APP_HTTP_ADDR
// shorthand: operators can pass just "8080" instead of ":8080".
func TestNormalizeHTTPAddr(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", ":8080"},                        // empty -> default
		{"8080", ":8080"},                    // bare port -> prepend ':'
		{":8080", ":8080"},                   // already canonical
		{"127.0.0.1:8080", "127.0.0.1:8080"}, // host:port, unchanged
		{"0.0.0.0:9090", "0.0.0.0:9090"},     // explicit all interfaces
		{"[::1]:8080", "[::1]:8080"},         // IPv6, has ':' so unchanged
		{"localhost:3000", "localhost:3000"}, // hostname, has ':' so unchanged
	}
	for _, tc := range cases {
		got := normalizeHTTPAddr(tc.in)
		if got != tc.want {
			t.Errorf("normalizeHTTPAddr(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestLoad_HTTPAddrShorthand verifies that APP_HTTP_ADDR=8080 (no
// leading colon) works end-to-end: the loaded Config.HTTPAddr must be
// ":8080" so net/http can bind.
func TestLoad_HTTPAddrShorthand(t *testing.T) {
	os.Setenv("APP_MANAGER_PASSWORD", "x")
	os.Setenv("APP_HTTP_ADDR", "8080")
	c, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.HTTPAddr != ":8080" {
		t.Errorf("HTTPAddr = %q, want %q (operator passed bare '8080' shorthand)", c.HTTPAddr, ":8080")
	}
}
