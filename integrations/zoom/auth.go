package zoom

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"commons/store"
)

// AccessToken reads S2S credentials from config_store and returns a Bearer token.
func AccessToken(ctx context.Context, pool *pgxpool.Pool, encKey []byte) (string, error) {
	accountID, err := store.GetServiceConfig(ctx, pool, "zoom", "account_id", encKey)
	if errors.Is(err, store.ErrNotFound) {
		return "", fmt.Errorf("zoom/account_id is not configured — set it on the Config page")
	}
	if err != nil {
		return "", fmt.Errorf("read zoom/account_id: %w", err)
	}
	clientID, err := store.GetServiceConfig(ctx, pool, "zoom", "api_client_id", encKey)
	if errors.Is(err, store.ErrNotFound) {
		return "", fmt.Errorf("zoom/api_client_id is not configured — set it on the Config page")
	}
	if err != nil {
		return "", fmt.Errorf("read zoom/api_client_id: %w", err)
	}
	clientSecret, err := store.GetServiceConfig(ctx, pool, "zoom", "api_client_secret", encKey)
	if errors.Is(err, store.ErrNotFound) {
		return "", fmt.Errorf("zoom/api_client_secret is not configured — set it on the Config page")
	}
	if err != nil {
		return "", fmt.Errorf("read zoom/api_client_secret: %w", err)
	}
	return fetchS2SToken(ctx, accountID, clientID, clientSecret)
}
