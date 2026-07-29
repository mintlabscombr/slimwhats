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
