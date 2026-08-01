package store

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"commons/internal/testhelpers"
)

func TestSetGetServiceConfigNonSensitive(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)
	encKey := testhelpers.EncKey()

	require.NoError(t, SetServiceConfig(ctx, pool, "test-svc", "my-key", "my-value", false, nil, encKey))

	got, err := GetServiceConfig(ctx, pool, "test-svc", "my-key", encKey)
	require.NoError(t, err)
	assert.Equal(t, "my-value", got)
}

func TestSetGetServiceConfigSensitive(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)
	encKey := testhelpers.EncKey()

	require.NoError(t, SetServiceConfig(ctx, pool, "test-svc", "secret-key", "supersecret", true, nil, encKey))

	got, err := GetServiceConfig(ctx, pool, "test-svc", "secret-key", encKey)
	require.NoError(t, err)
	assert.Equal(t, "supersecret", got)
}

func TestGetServiceConfigMissingKey(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)
	encKey := testhelpers.EncKey()

	_, err := GetServiceConfig(ctx, pool, "test-svc", "nonexistent", encKey)
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestListServiceConfigsRawEncPrefix(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)
	encKey := testhelpers.EncKey()

	require.NoError(t, SetServiceConfig(ctx, pool, "test-svc", "plain", "hello", false, nil, encKey))
	require.NoError(t, SetServiceConfig(ctx, pool, "test-svc", "secret", "world", true, nil, encKey))

	entries, err := ListServiceConfigs(ctx, pool, "test-svc")
	require.NoError(t, err)
	require.Len(t, entries, 2)

	byKey := make(map[string]ConfigEntry)
	for _, e := range entries {
		byKey[e.Key] = e
	}

	assert.Equal(t, "hello", byKey["plain"].Value)
	assert.True(t, strings.HasPrefix(byKey["secret"].Value, "enc:v1:"), "sensitive value should have enc:v1: prefix in raw listing")
}

func TestSetServiceConfigUpsert(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)
	encKey := testhelpers.EncKey()

	require.NoError(t, SetServiceConfig(ctx, pool, "test-svc", "key", "first", false, nil, encKey))
	require.NoError(t, SetServiceConfig(ctx, pool, "test-svc", "key", "second", false, nil, encKey))

	got, err := GetServiceConfig(ctx, pool, "test-svc", "key", encKey)
	require.NoError(t, err)
	assert.Equal(t, "second", got)
}

func TestDeleteServiceConfig(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)
	encKey := testhelpers.EncKey()

	require.NoError(t, SetServiceConfig(ctx, pool, "test-svc", "key", "value", false, nil, encKey))
	require.NoError(t, DeleteServiceConfig(ctx, pool, "test-svc", "key"))

	_, err := GetServiceConfig(ctx, pool, "test-svc", "key", encKey)
	assert.ErrorIs(t, err, ErrNotFound)
}
