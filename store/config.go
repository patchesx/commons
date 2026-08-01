package store

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type ConfigEntry struct {
	ID        string
	Service   string
	Key       string
	Value     string
	Sensitive bool
	UpdatedBy *string
	UpdatedAt time.Time
}

type ConfigSchemaEntry struct {
	Service     string
	Key         string
	Label       string
	Description *string
	Sensitive   bool
	Required    bool
	Filterable  bool
}

// ListConfigSchema returns all known keys for a service from config_schema, ordered by key.
func ListConfigSchema(ctx context.Context, pool *pgxpool.Pool, service string) ([]ConfigSchemaEntry, error) {
	rows, err := pool.Query(ctx, `
		SELECT service, key, label, description, sensitive, required, filterable
		FROM config_schema WHERE service = $1 ORDER BY key
	`, service)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []ConfigSchemaEntry
	for rows.Next() {
		var e ConfigSchemaEntry
		if err := rows.Scan(&e.Service, &e.Key, &e.Label, &e.Description, &e.Sensitive, &e.Required, &e.Filterable); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// ListAllConfigSchemaEntries returns all filterable config_schema entries across all services,
// ordered by service then key. Used to populate config_ref dropdowns in webhook filter UI.
func ListAllConfigSchemaEntries(ctx context.Context, pool *pgxpool.Pool) ([]ConfigSchemaEntry, error) {
	rows, err := pool.Query(ctx, `
		SELECT service, key, label, description, sensitive, required, filterable
		FROM config_schema WHERE filterable = true ORDER BY service, key
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []ConfigSchemaEntry
	for rows.Next() {
		var e ConfigSchemaEntry
		if err := rows.Scan(&e.Service, &e.Key, &e.Label, &e.Description, &e.Sensitive, &e.Required, &e.Filterable); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// GetConfigSchemaEntry returns the schema definition for a single service/key pair.
// Returns ErrNotFound if the key is not in the schema.
func GetConfigSchemaEntry(ctx context.Context, pool *pgxpool.Pool, service, key string) (*ConfigSchemaEntry, error) {
	var e ConfigSchemaEntry
	err := pool.QueryRow(ctx, `
		SELECT service, key, label, description, sensitive, required, filterable
		FROM config_schema WHERE service = $1 AND key = $2
	`, service, key).Scan(&e.Service, &e.Key, &e.Label, &e.Description, &e.Sensitive, &e.Required, &e.Filterable)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &e, nil
}

// GetServiceConfig reads a single config value from the database and decrypts it
// if it carries the "enc:v1:" prefix. Returns ErrNotFound if the key does not exist.
func GetServiceConfig(ctx context.Context, pool *pgxpool.Pool, service, key string, encKey []byte) (string, error) {
	var value string
	err := pool.QueryRow(ctx, `
		SELECT value FROM config_store WHERE service = $1 AND key = $2
	`, service, key).Scan(&value)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	return Decrypt(encKey, value)
}

// SetServiceConfig upserts a config value, encrypting it when sensitive=true.
// updatedByID may be nil for system-initiated writes.
func SetServiceConfig(ctx context.Context, pool *pgxpool.Pool, service, key, value string, sensitive bool, updatedByID *string, encKey []byte) error {
	stored := value
	if sensitive {
		var err error
		stored, err = Encrypt(encKey, value)
		if err != nil {
			return fmt.Errorf("encrypt value: %w", err)
		}
	}
	_, err := pool.Exec(ctx, `
		INSERT INTO config_store (service, key, value, sensitive, updated_by, updated_at)
		VALUES ($1, $2, $3, $4, $5, NOW())
		ON CONFLICT (service, key) DO UPDATE
		    SET value      = EXCLUDED.value,
		        sensitive  = EXCLUDED.sensitive,
		        updated_by = EXCLUDED.updated_by,
		        updated_at = NOW()
	`, service, key, stored, sensitive, updatedByID)
	return err
}

// DeleteServiceConfig removes a config entry entirely (reverts to unset/default).
func DeleteServiceConfig(ctx context.Context, pool *pgxpool.Pool, service, key string) error {
	_, err := pool.Exec(ctx, `DELETE FROM config_store WHERE service = $1 AND key = $2`, service, key)
	return err
}

// ListServiceConfigs returns all config entries for a service, ordered by key.
// Values are returned raw (enc:v1: prefix intact for sensitive rows).
// Callers that need plaintext should call store.Decrypt on sensitive entries.
func ListServiceConfigs(ctx context.Context, pool *pgxpool.Pool, service string) ([]ConfigEntry, error) {
	rows, err := pool.Query(ctx, `
		SELECT id, service, key, value, sensitive, updated_by, updated_at
		FROM config_store WHERE service = $1 ORDER BY key
	`, service)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	entries := make([]ConfigEntry, 0)
	for rows.Next() {
		var e ConfigEntry
		if err := rows.Scan(&e.ID, &e.Service, &e.Key, &e.Value, &e.Sensitive, &e.UpdatedBy, &e.UpdatedAt); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// BaseURL reads bot/admin_url from config_store and returns the scheme+host with no path.
// Used by OAuth handlers that need to construct an absolute redirect URI.
func BaseURL(ctx context.Context, pool *pgxpool.Pool, encKey []byte) (string, error) {
	adminURL, err := GetServiceConfig(ctx, pool, "bot", "admin_url", encKey)
	if errors.Is(err, ErrNotFound) || adminURL == "" {
		return "", fmt.Errorf("bot/admin_url not configured — set it in the web UI before connecting Google")
	}
	if err != nil {
		return "", fmt.Errorf("read admin_url: %w", err)
	}
	u, err := url.Parse(adminURL)
	if err != nil {
		return "", fmt.Errorf("parse admin_url %q: %w", adminURL, err)
	}
	u.Path = ""
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}
