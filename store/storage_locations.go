package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// StorageLocation is a named storage destination owned by a specific integration instance.
type StorageLocation struct {
	ID              string
	IntegrationID   string
	IntegrationType string // populated via JOIN on integrations.type
	IntegrationName string // populated via JOIN on integrations.name
	Name            string
	Path            string
	CreatedAt       time.Time
}

// CreateStorageLocation inserts a new storage location and populates s.ID and s.CreatedAt.
func CreateStorageLocation(ctx context.Context, pool *pgxpool.Pool, integrationID, name, path string) (*StorageLocation, error) {
	s := &StorageLocation{}
	err := pool.QueryRow(ctx, `
		INSERT INTO storage_locations (integration_id, name, path)
		VALUES ($1, $2, $3)
		RETURNING id, integration_id, name, path, created_at
	`, integrationID, name, path).Scan(&s.ID, &s.IntegrationID, &s.Name, &s.Path, &s.CreatedAt)
	if err != nil {
		return nil, err
	}
	// Populate integration type/name via separate query
	err = pool.QueryRow(ctx, `
		SELECT type, name FROM integrations WHERE id = $1
	`, integrationID).Scan(&s.IntegrationType, &s.IntegrationName)
	if err != nil {
		return nil, err
	}
	return s, nil
}

// GetStorageLocation returns a single storage location by ID, with integration details.
// Returns ErrNotFound if absent.
func GetStorageLocation(ctx context.Context, pool *pgxpool.Pool, id string) (*StorageLocation, error) {
	s := &StorageLocation{}
	err := pool.QueryRow(ctx, `
		SELECT sl.id, sl.integration_id, i.type, i.name, sl.name, sl.path, sl.created_at
		FROM storage_locations sl
		JOIN integrations i ON i.id = sl.integration_id
		WHERE sl.id = $1
	`, id).Scan(&s.ID, &s.IntegrationID, &s.IntegrationType, &s.IntegrationName, &s.Name, &s.Path, &s.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return s, nil
}

// ListStorageLocations returns all storage locations with integration details, ordered by integration type then name.
func ListStorageLocations(ctx context.Context, pool *pgxpool.Pool) ([]StorageLocation, error) {
	rows, err := pool.Query(ctx, `
		SELECT sl.id, sl.integration_id, i.type, i.name, sl.name, sl.path, sl.created_at
		FROM storage_locations sl
		JOIN integrations i ON i.id = sl.integration_id
		ORDER BY i.type, sl.name
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanStorageLocations(rows)
}

// ListStorageLocationsByIntegration returns storage locations for a specific integration instance.
func ListStorageLocationsByIntegration(ctx context.Context, pool *pgxpool.Pool, integrationID string) ([]StorageLocation, error) {
	rows, err := pool.Query(ctx, `
		SELECT sl.id, sl.integration_id, i.type, i.name, sl.name, sl.path, sl.created_at
		FROM storage_locations sl
		JOIN integrations i ON i.id = sl.integration_id
		WHERE sl.integration_id = $1
		ORDER BY sl.name
	`, integrationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanStorageLocations(rows)
}

// ListStorageLocationsByType returns storage locations for all integrations of a given type.
func ListStorageLocationsByType(ctx context.Context, pool *pgxpool.Pool, integrationType string) ([]StorageLocation, error) {
	rows, err := pool.Query(ctx, `
		SELECT sl.id, sl.integration_id, i.type, i.name, sl.name, sl.path, sl.created_at
		FROM storage_locations sl
		JOIN integrations i ON i.id = sl.integration_id
		WHERE i.type = $1
		ORDER BY sl.name
	`, integrationType)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanStorageLocations(rows)
}

// DeleteStorageLocation removes a storage location by ID.
func DeleteStorageLocation(ctx context.Context, pool *pgxpool.Pool, id string) error {
	_, err := pool.Exec(ctx, `DELETE FROM storage_locations WHERE id = $1`, id)
	return err
}

func scanStorageLocations(rows pgx.Rows) ([]StorageLocation, error) {
	locations := make([]StorageLocation, 0)
	for rows.Next() {
		var s StorageLocation
		if err := rows.Scan(&s.ID, &s.IntegrationID, &s.IntegrationType, &s.IntegrationName, &s.Name, &s.Path, &s.CreatedAt); err != nil {
			return nil, err
		}
		locations = append(locations, s)
	}
	return locations, rows.Err()
}
