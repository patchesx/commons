package config

import (
	"encoding/hex"
	"fmt"
	"os"
	"strings"
)

// Config holds all bootstrap environment variables required at startup.
// Dynamic service credentials (Zoom, YouTube, etc.) live in the database
// and are accessed via store.GetServiceConfig.
type Config struct {
	DatabaseURL       string
	SessionSecret     string
	EncryptionKey     []byte // 32-byte AES-256 key decoded from ENCRYPTION_KEY hex string
	AdminUsername     string
	AdminPasswordHash string
	SecureCookies     bool // false for local HTTP development; true (default) for production HTTPS
	Port              string
}

// Load reads bootstrap configuration from environment variables.
// Returns an error listing all missing required variables at once.
func Load() (*Config, error) {
	var missing []string

	get := func(key string) string {
		v := os.Getenv(key)
		if v == "" {
			missing = append(missing, key)
		}
		return v
	}

	cfg := &Config{
		DatabaseURL:   get("DATABASE_URL"),
		SessionSecret: get("SESSION_SECRET"),
	}

	// ENCRYPTION_KEY must be a 64-character hex string (32 bytes).
	// Generate with: openssl rand -hex 32
	encKeyHex := get("ENCRYPTION_KEY")

	// Bootstrap credentials are optional — only used to seed the first admin on a fresh deployment.
	cfg.AdminUsername = os.Getenv("ADMIN_USERNAME")
	cfg.AdminPasswordHash = os.Getenv("ADMIN_PASSWORD_HASH")

	if len(missing) > 0 {
		return nil, fmt.Errorf("missing required environment variables: %s", strings.Join(missing, ", "))
	}

	key, err := hex.DecodeString(encKeyHex)
	if err != nil || len(key) != 32 {
		return nil, fmt.Errorf("ENCRYPTION_KEY must be a 64-character hex string (32 bytes); generate with: openssl rand -hex 32")
	}
	cfg.EncryptionKey = key

	// SECURE_COOKIES defaults to true (production). Set to "false" for local HTTP development.
	cfg.SecureCookies = os.Getenv("SECURE_COOKIES") != "false"

	cfg.Port = os.Getenv("PORT")
	if cfg.Port == "" {
		cfg.Port = "8080"
	}

	return cfg, nil
}

// EncryptionKeyHex returns the 64-character hex-encoded form of EncryptionKey.
func (c *Config) EncryptionKeyHex() string {
	return fmt.Sprintf("%x", c.EncryptionKey)
}
