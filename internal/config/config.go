// Package config loads the service configuration from `config.yaml` plus
// `APP_*` environment variable overrides.
package config

import (
	"encoding/base64"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Config holds the runtime configuration for whatsmeow-api. Non-secret
// defaults can be set in `config.yaml`; the required secret material
// (`APP_MANAGER_PASSWORD`, `APP_ENCRYPTION_KEY`) is read from env only and
// never persisted.
type Config struct {
	HTTPAddr        string
	DBDriver        string
	DBDSN           string
	ManagerUsername string
	TrustedProxies  []string

	// Required, env-only.
	ManagerPassword string
	EncryptionKey   []byte
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
		DBDSN:           "file:data/whatsmeow-api.db?_foreign_keys=on",
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
		HTTPAddr:        envOr("APP_HTTP_ADDR", yc.HTTPAddr),
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

	// Required: encryption key for webhook secret at-rest encryption.
	// Must be 32 raw bytes, base64-encoded (a 44-char string from
	// `openssl rand -base64 32`).
	keyB64 := os.Getenv("APP_ENCRYPTION_KEY")
	if keyB64 == "" {
		return nil, fmt.Errorf("APP_ENCRYPTION_KEY is required (generate with: openssl rand -base64 32)")
	}
	key, err := base64.StdEncoding.DecodeString(keyB64)
	if err != nil {
		return nil, fmt.Errorf("APP_ENCRYPTION_KEY is not valid base64: %w", err)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("APP_ENCRYPTION_KEY must decode to 32 bytes (got %d); generate with: openssl rand -base64 32", len(key))
	}
	c.EncryptionKey = key

	return c, nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
