package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Integration is a configured third-party API instance.
// Multiple instances of the same type are supported (e.g., two Zoom accounts).
type Integration struct {
	ID        string
	Type      string
	Name      string
	Enabled   bool
	CreatedAt time.Time
}

// GetIntegrationByType returns the first enabled integration of the given type.
// Most deployments have exactly one per type; use ListIntegrationsByType for multi-instance.
// Returns ErrNotFound if no enabled integration of this type exists.
func GetIntegrationByType(ctx context.Context, pool *pgxpool.Pool, intType string) (*Integration, error) {
	i := &Integration{}
	err := pool.QueryRow(ctx, `
		SELECT id, type, name, enabled, created_at
		FROM integrations WHERE type = $1 AND enabled = TRUE
		ORDER BY created_at LIMIT 1
	`, intType).Scan(&i.ID, &i.Type, &i.Name, &i.Enabled, &i.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return i, nil
}

// GetIntegrationByTypeAny returns the integration row for the given type
// regardless of whether it is enabled. Returns ErrNotFound if absent.
func GetIntegrationByTypeAny(ctx context.Context, pool *pgxpool.Pool, intType string) (*Integration, error) {
	i := &Integration{}
	err := pool.QueryRow(ctx, `
		SELECT id, type, name, enabled, created_at
		FROM integrations WHERE type = $1
		ORDER BY created_at LIMIT 1
	`, intType).Scan(&i.ID, &i.Type, &i.Name, &i.Enabled, &i.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return i, nil
}

// ListIntegrationsByType returns all integrations of the given type, oldest first.
func ListIntegrationsByType(ctx context.Context, pool *pgxpool.Pool, intType string) ([]Integration, error) {
	rows, err := pool.Query(ctx, `
		SELECT id, type, name, enabled, created_at
		FROM integrations WHERE type = $1 ORDER BY created_at
	`, intType)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	integrations := make([]Integration, 0)
	for rows.Next() {
		var i Integration
		if err := rows.Scan(&i.ID, &i.Type, &i.Name, &i.Enabled, &i.CreatedAt); err != nil {
			return nil, err
		}
		integrations = append(integrations, i)
	}
	return integrations, rows.Err()
}

// ListIntegrations returns all integration instances, ordered by type then creation.
func ListIntegrations(ctx context.Context, pool *pgxpool.Pool) ([]Integration, error) {
	rows, err := pool.Query(ctx, `
		SELECT id, type, name, enabled, created_at
		FROM integrations ORDER BY type, created_at
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	integrations := make([]Integration, 0)
	for rows.Next() {
		var i Integration
		if err := rows.Scan(&i.ID, &i.Type, &i.Name, &i.Enabled, &i.CreatedAt); err != nil {
			return nil, err
		}
		integrations = append(integrations, i)
	}
	return integrations, rows.Err()
}

// SetIntegrationEnabled sets the enabled flag for an integration row.
func SetIntegrationEnabled(ctx context.Context, pool *pgxpool.Pool, id string, enabled bool) error {
	_, err := pool.Exec(ctx, `UPDATE integrations SET enabled = $2 WHERE id = $1`, id, enabled)
	return err
}

// GetIntegrationConfig reads a single config value for an integration instance.
// Decrypts if the value carries the enc:v1: prefix. Returns ErrNotFound if not set.
func GetIntegrationConfig(ctx context.Context, pool *pgxpool.Pool, integrationID, key string, encKey []byte) (string, error) {
	var value string
	err := pool.QueryRow(ctx, `
		SELECT value FROM integration_config WHERE integration_id = $1 AND key = $2
	`, integrationID, key).Scan(&value)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	return Decrypt(encKey, value)
}

// SetIntegrationConfig upserts a config value for an integration instance.
// Encrypts the value when sensitive=true.
func SetIntegrationConfig(ctx context.Context, pool *pgxpool.Pool, integrationID, key, value string, sensitive bool, updatedByID *string, encKey []byte) error {
	stored := value
	if sensitive {
		var err error
		stored, err = Encrypt(encKey, value)
		if err != nil {
			return err
		}
	}
	_, err := pool.Exec(ctx, `
		INSERT INTO integration_config (integration_id, key, value, sensitive, updated_by, updated_at)
		VALUES ($1, $2, $3, $4, $5, NOW())
		ON CONFLICT (integration_id, key) DO UPDATE
		    SET value      = EXCLUDED.value,
		        sensitive  = EXCLUDED.sensitive,
		        updated_by = EXCLUDED.updated_by,
		        updated_at = NOW()
	`, integrationID, key, stored, sensitive, updatedByID)
	return err
}

// ListIntegrationConfigs returns all config entries for an integration instance.
// Sensitive values are decrypted before being returned.
// ConfigEntry.ID is set to the integration_id; ConfigEntry.Service is set to the integration type.
func ListIntegrationConfigs(ctx context.Context, pool *pgxpool.Pool, integrationID string, encKey []byte) ([]ConfigEntry, error) {
	rows, err := pool.Query(ctx, `
		SELECT ic.integration_id, i.type, ic.key, ic.value, ic.sensitive, ic.updated_by, ic.updated_at
		FROM integration_config ic
		JOIN integrations i ON i.id = ic.integration_id
		WHERE ic.integration_id = $1 ORDER BY ic.key
	`, integrationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []ConfigEntry
	for rows.Next() {
		var e ConfigEntry
		if err := rows.Scan(&e.ID, &e.Service, &e.Key, &e.Value, &e.Sensitive, &e.UpdatedBy, &e.UpdatedAt); err != nil {
			return nil, err
		}
		if e.Sensitive {
			e.Value, err = Decrypt(encKey, e.Value)
			if err != nil {
				return nil, err
			}
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}
