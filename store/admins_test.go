package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"commons/internal/testhelpers"
)

func TestHasAnyAdminCredentials_EmptyDB(t *testing.T) {
	pool := testhelpers.SetupTestDB(t)
	has, err := HasAnyAdminCredentials(context.Background(), pool)
	require.NoError(t, err)
	assert.False(t, has)
}

func TestHasAnyAdminCredentials_AfterBootstrap(t *testing.T) {
	pool := testhelpers.SetupTestDB(t)
	hash, _ := bcrypt.GenerateFromPassword([]byte("password"), 4)
	_, err := UpsertBootstrapUser(context.Background(), pool, "admin@example.com", string(hash))
	require.NoError(t, err)

	has, err := HasAnyAdminCredentials(context.Background(), pool)
	require.NoError(t, err)
	assert.True(t, has)
}
