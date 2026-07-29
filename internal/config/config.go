// Package config loads the service configuration from `config.yaml` plus
// `APP_*` environment variable overrides.
package config

import (
	"encoding/base64"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config holds the runtime configuration for whatsmeow-api. Non-secret
// defaults can be set in `config.yaml`; the required secret material
// (`APP_MANAGER_PASSWORD`) is read from env only and never persisted.
//
// `APP_ENCRYPTION_KEY` was an earlier v1 requirement (AES-256-GCM at-rest
// encryption of webhook secrets) and is now ignored. Webhook secrets are
// stored as plaintext. If `APP_ENCRYPTION_KEY` is set, we still parse +
// validate it for backward compatibility but it's not used anywhere —
// the field is here as a no-op so old `.env` files don't fail to load.
type Config struct {
	HTTPAddr        string
	DBDriver        string
	DBDSN           string
	ManagerUsername string
	TrustedProxies  []string

	// Required, env-only.
	ManagerPassword string

	// Legacy: accepted but no longer used. Kept so old .env files
	// don't error out. The hotfix is recorded in PROGRESS.md.
	EncryptionKey []byte
}

// yamlConfig is the subset of Config that can be loaded from `config.yaml`.
// Secret material is deliberately excluded so it can never end up on disk.
type yamlConfig struct {
	HTTPAddr        string   `yaml:"http_addr"`
	DBDriver        string   `yaml:"db_driver"`
	DBDSN           string   `yaml:"db_dsn"`
	ManagerUsername string   `yaml:"manager_username"`
	TrustedProxies  []string `yaml:"trusted_proxies"`
}

// Load reads `config.yaml` from the current working directory (if present)
// for defaults, then layers `APP_*` env vars on top. It returns a fully
// validated *Config or an error describing what's missing or malformed.
func Load() (*Config, error) {
	yc := yamlConfig{
		HTTPAddr:        ":8080",
		DBDriver:        "sqlite3",
		DBDSN:           "file:data/whatsmeow-api.db?_pragma=foreign_keys(1)",
		ManagerUsername: "admin",
	}
	if data, err := os.ReadFile("config.yaml"); err == nil {
		if err := yaml.Unmarshal(data, &yc); err != nil {
			return nil, fmt.Errorf("invalid config.yaml: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read config.yaml: %w", err)
	}

	c := &Config{
		HTTPAddr:        normalizeHTTPAddr(envOr("APP_HTTP_ADDR", yc.HTTPAddr)),
		DBDriver:        envOr("APP_DB_DRIVER", yc.DBDriver),
		DBDSN:           envOr("APP_DB_DSN", yc.DBDSN),
		ManagerUsername: envOr("APP_MANAGER_USERNAME", yc.ManagerUsername),
		TrustedProxies:  yc.TrustedProxies, // env override deferred to a later US
	}

	// Required: manager password.
	pw := os.Getenv("APP_MANAGER_PASSWORD")
	if pw == "" {
		return nil, fmt.Errorf("APP_MANAGER_PASSWORD is required")
	}
	c.ManagerPassword = pw

	// Legacy: APP_ENCRYPTION_KEY is no longer required. If set, we
	// validate it for backward compatibility but otherwise ignore it.
	// Webhook secrets are stored as plaintext in the DB (hotfix —
	// see PROGRESS.md). A one-time warning is logged when the key is
	// missing (helps catch .env files that haven't been updated).
	keyB64 := os.Getenv("APP_ENCRYPTION_KEY")
	if keyB64 == "" {
		slog.Warn("APP_ENCRYPTION_KEY is not set; webhook secrets are stored as plaintext in the DB")
	} else {
		key, err := base64.StdEncoding.DecodeString(keyB64)
		if err != nil {
			return nil, fmt.Errorf("APP_ENCRYPTION_KEY is not valid base64: %w", err)
		}
		if len(key) != 32 {
			return nil, fmt.Errorf("APP_ENCRYPTION_KEY must decode to 32 bytes (got %d)", len(key))
		}
		c.EncryptionKey = key
	}

	return c, nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// normalizeHTTPAddr accepts the address in three forms and returns the
// canonical `host:port` form that http.Server expects:
//   - "8080"           -> ":8080"           (bare port → all interfaces)
//   - ":8080"          -> ":8080"           (already canonical)
//   - "127.0.0.1:8080" -> "127.0.0.1:8080"  (host:port, unchanged)
//   - "[::1]:8080"     -> "[::1]:8080"      (IPv6, unchanged — contains ':')
//
// The bare-port form is the common shorthand; net/http refuses to
// listen on "8080" (it interprets it as a hostname and fails to bind).
func normalizeHTTPAddr(addr string) string {
	if addr == "" {
		return ":8080"
	}
	if !strings.Contains(addr, ":") {
		return ":" + addr
	}
	return addr
}
