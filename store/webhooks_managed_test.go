// store/webhooks_managed_test.go
package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"commons/internal/testhelpers"
)

func TestUpsertManagedWebhook_FreshInsert(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	require.NoError(t, SeedWebhookProcessorType(ctx, pool, "slack_events", "Slack Events"))

	wh, err := UpsertManagedWebhook(ctx, pool, UpsertManagedWebhookParams{
		Slug:          "slack/events",
		Name:          "Slack Events",
		ProcessorType: "slack_events",
		ManagedBy:     "slack",
	})
	require.NoError(t, err)
	require.NotNil(t, wh)
	assert.Equal(t, "slack/events", wh.Slug)
	assert.Equal(t, "Slack Events", wh.Name)
	require.NotNil(t, wh.ManagedBy)
	assert.Equal(t, "slack", *wh.ManagedBy)
	require.NotNil(t, wh.ProcessorType)
	assert.Equal(t, "slack_events", *wh.ProcessorType)
}

func TestUpsertManagedWebhook_IdempotentUpdate(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	require.NoError(t, SeedWebhookProcessorType(ctx, pool, "slack_events", "Slack Events"))

	// Insert once.
	wh1, err := UpsertManagedWebhook(ctx, pool, UpsertManagedWebhookParams{
		Slug:          "slack/events",
		Name:          "Slack Events",
		ProcessorType: "slack_events",
		ManagedBy:     "slack",
	})
	require.NoError(t, err)
	require.NotNil(t, wh1)

	// Upsert again — should succeed and update processor_type.
	wh2, err := UpsertManagedWebhook(ctx, pool, UpsertManagedWebhookParams{
		Slug:          "slack/events",
		Name:          "Slack Events Updated", // should NOT be updated
		ProcessorType: "slack_events",
		ManagedBy:     "slack",
	})
	require.NoError(t, err)
	require.NotNil(t, wh2)
	// Name is not updated on conflict.
	assert.Equal(t, "Slack Events", wh2.Name)
	assert.Equal(t, wh1.ID, wh2.ID)
}

func TestUpsertManagedWebhook_ConflictWithUserWebhook(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	// Create a user-owned webhook with the target slug.
	var tsID string
	err := pool.QueryRow(ctx,
		`INSERT INTO trigger_sources (type, name, enabled) VALUES ('http.webhook', 'My Webhook', true) RETURNING id`,
	).Scan(&tsID)
	require.NoError(t, err)
	_, err = pool.Exec(ctx,
		`INSERT INTO http_trigger_config (trigger_id, slug, verification_mode) VALUES ($1, 'slack/events', 'none')`, tsID)
	require.NoError(t, err)

	// UpsertManagedWebhook must refuse to take over the slug.
	_, err = UpsertManagedWebhook(ctx, pool, UpsertManagedWebhookParams{
		Slug:          "slack/events",
		Name:          "Slack Events",
		ProcessorType: "slack_events",
		ManagedBy:     "slack",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "slack/events")
}

func TestEnsureWebhookAction_CreatesOnce(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	// Create a webhook to attach an action to.
	var tsID2 string
	err := pool.QueryRow(ctx,
		`INSERT INTO trigger_sources (type, name, enabled) VALUES ('http.webhook', 'Test', true) RETURNING id`,
	).Scan(&tsID2)
	require.NoError(t, err)
	_, err = pool.Exec(ctx,
		`INSERT INTO http_trigger_config (trigger_id, slug, verification_mode) VALUES ($1, 'test-wh', 'none')`, tsID2)
	require.NoError(t, err)

	// First call creates the action.
	err = EnsureWebhookAction(ctx, pool, tsID2, "slack.handle_events")
	require.NoError(t, err)

	var count int
	err = pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM pipeline_actions WHERE trigger_id=$1 AND type=$2`,
		tsID2, "slack.handle_events").Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 1, count)

	// Second call must not create a duplicate.
	err = EnsureWebhookAction(ctx, pool, tsID2, "slack.handle_events")
	require.NoError(t, err)

	err = pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM pipeline_actions WHERE trigger_id=$1 AND type=$2`,
		tsID2, "slack.handle_events").Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 1, count, "EnsureWebhookAction must be idempotent")
}
