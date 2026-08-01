package store

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ConfigGetter provides read access to config_store values.
// Abstracts pool and encryption key so plugins don't need to carry them directly.
type ConfigGetter interface {
	GetServiceConfig(ctx context.Context, service, key string) (string, error)
}

type configGetter struct {
	pool   *pgxpool.Pool
	encKey []byte
}

// NewConfigGetter returns a ConfigGetter backed by pool and encKey.
func NewConfigGetter(pool *pgxpool.Pool, encKey []byte) ConfigGetter {
	return &configGetter{pool: pool, encKey: encKey}
}

func (g *configGetter) GetServiceConfig(ctx context.Context, service, key string) (string, error) {
	return GetServiceConfig(ctx, g.pool, service, key, g.encKey)
}
