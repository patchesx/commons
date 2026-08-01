package slack

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"commons/internal/testhelpers"
	"commons/store"
)

func TestSendBotScheduledReminderSuccess(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)
	encKey := testhelpers.EncKey()
	pkgEncKey = encKey

	srv, fake := newFakeSlackServer(t)
	client := newTestSlackClient(srv.URL)

	u, err := store.GetOrCreateUserByIdentity(ctx, pool, "slack", "U_REMINDER", "reminder", "Reminder User")
	require.NoError(t, err)

	occ := store.OccurrenceReminder{
		OccurrenceID: "occ-1",
		MeetingID:    "mtg-1",
		Topic:        "Weekly Sync",
		StartTime:    time.Now().Add(time.Hour),
		Timezone:     "America/Chicago",
		StartURL:     "https://zoom.us/s/start",
		JoinURL:      "https://zoom.us/j/join",
		CreatedByID:  u.ID,
		IsSyncImport: false,
	}

	sendBotScheduledReminder(ctx, pool, encKey, client, occ)

	fake.mu.Lock()
	defer fake.mu.Unlock()
	require.Len(t, fake.dmOpens, 1, "should open a DM with the meeting creator")
	assert.Equal(t, "U_REMINDER", fake.dmOpens[0])
	require.Len(t, fake.posts, 1)
	assert.Contains(t, fake.posts[0].Text, "Weekly Sync")
	assert.Contains(t, fake.posts[0].Text, "https://zoom.us/s/start")
}

func TestSendBotScheduledReminderNoSlackIdentity(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)
	encKey := testhelpers.EncKey()
	pkgEncKey = encKey

	srv, fake := newFakeSlackServer(t)
	client := newTestSlackClient(srv.URL)

	// User exists but has no Slack identity.
	var userID string
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO users (display_name) VALUES ('No Slack') RETURNING id`,
	).Scan(&userID))

	occ := store.OccurrenceReminder{
		OccurrenceID: "occ-2",
		CreatedByID:  userID,
		Topic:        "Meeting",
		StartTime:    time.Now().Add(time.Hour),
		Timezone:     "UTC",
	}

	sendBotScheduledReminder(ctx, pool, encKey, client, occ)

	fake.mu.Lock()
	defer fake.mu.Unlock()
	assert.Empty(t, fake.dmOpens, "no DM should be sent when user has no Slack identity")
}

func TestSendSyncImportReminderDisabled(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)
	encKey := testhelpers.EncKey()
	pkgEncKey = encKey

	srv, fake := newFakeSlackServer(t)
	client := newTestSlackClient(srv.URL)

	// sync_reminder_enabled not set — defaults to disabled.
	occ := store.OccurrenceReminder{
		OccurrenceID: "occ-3",
		Topic:        "City Council",
		StartTime:    time.Now().Add(time.Hour),
		Timezone:     "UTC",
		JoinURL:      "https://zoom.us/j/join",
		IsSyncImport: true,
	}

	sendSyncImportReminder(ctx, pool, encKey, client, occ)

	fake.mu.Lock()
	defer fake.mu.Unlock()
	assert.Empty(t, fake.posts, "no post when sync reminders are disabled")
}

func TestSendSyncImportReminderEnabledNoChannel(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)
	encKey := testhelpers.EncKey()
	pkgEncKey = encKey

	srv, fake := newFakeSlackServer(t)
	client := newTestSlackClient(srv.URL)

	require.NoError(t, store.SetServiceConfig(ctx, pool, "meetings", "sync_reminder_enabled", "true", false, nil, encKey))
	// sync_reminder_channel intentionally not set.

	occ := store.OccurrenceReminder{
		OccurrenceID: "occ-4",
		Topic:        "City Council",
		StartTime:    time.Now().Add(time.Hour),
		Timezone:     "UTC",
		JoinURL:      "https://zoom.us/j/join",
		IsSyncImport: true,
	}

	sendSyncImportReminder(ctx, pool, encKey, client, occ)

	fake.mu.Lock()
	defer fake.mu.Unlock()
	assert.Empty(t, fake.posts, "no post when channel is not configured")
}

func TestSendSyncImportReminderSuccess(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)
	encKey := testhelpers.EncKey()
	pkgEncKey = encKey

	srv, fake := newFakeSlackServer(t)
	client := newTestSlackClient(srv.URL)

	require.NoError(t, store.SetServiceConfig(ctx, pool, "meetings", "sync_reminder_enabled", "true", false, nil, encKey))
	require.NoError(t, store.SetServiceConfig(ctx, pool, "meetings", "sync_reminder_channel", "C_MEETINGS", false, nil, encKey))

	occ := store.OccurrenceReminder{
		OccurrenceID: "occ-5",
		Topic:        "City Council",
		StartTime:    time.Now().Add(time.Hour),
		Timezone:     "UTC",
		JoinURL:      "https://zoom.us/j/council",
		IsSyncImport: true,
	}

	sendSyncImportReminder(ctx, pool, encKey, client, occ)

	fake.mu.Lock()
	defer fake.mu.Unlock()
	require.Len(t, fake.posts, 1)
	assert.Equal(t, "C_MEETINGS", fake.posts[0].Channel)
	assert.Contains(t, fake.posts[0].Text, "City Council")
	assert.Contains(t, fake.posts[0].Text, "https://zoom.us/j/council")
}
