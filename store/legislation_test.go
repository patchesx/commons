package store

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"commons/internal/testhelpers"
)

func TestUpsertBillInsert(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	body := LegislativeBody{Name: "Test Council", Level: "local", State: "MO", DataSource: "legistar", Active: true}
	require.NoError(t, CreateLegislativeBody(ctx, pool, &body))

	extID := "OS-001"
	b := &Bill{
		BodyID:       body.ID,
		ExternalID:   &extID,
		Identifier:   "HB 1",
		Title:        "Test Bill",
		AutoImported: true,
		Following:    true,
	}
	inserted, err := UpsertBill(ctx, pool, b)
	require.NoError(t, err)
	assert.True(t, inserted)
	assert.NotEmpty(t, b.ID)
}

func TestUpsertBillUpdateFollowingTrue(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	body := LegislativeBody{Name: "Test Council", Level: "local", State: "MO", DataSource: "legistar", Active: true}
	require.NoError(t, CreateLegislativeBody(ctx, pool, &body))

	extID := "OS-002"
	b := &Bill{
		BodyID:       body.ID,
		ExternalID:   &extID,
		Identifier:   "HB 2",
		Title:        "Original Title",
		AutoImported: true,
		Following:    true,
	}
	_, err := UpsertBill(ctx, pool, b)
	require.NoError(t, err)

	// Upsert again with updated title.
	b2 := &Bill{
		BodyID:       body.ID,
		ExternalID:   &extID,
		Identifier:   "HB 2",
		Title:        "Updated Title",
		AutoImported: true,
	}
	_, err = UpsertBill(ctx, pool, b2)
	require.NoError(t, err)

	got, err := GetBill(ctx, pool, b2.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "Updated Title", got.Title)
}

func TestUpsertBillDismissedStaysDismissed(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	body := LegislativeBody{Name: "Test Council", Level: "local", State: "MO", DataSource: "legistar", Active: true}
	require.NoError(t, CreateLegislativeBody(ctx, pool, &body))

	extID := "OS-003"
	// Create a bill with following=false (dismissed).
	dismissed := &Bill{
		BodyID:       body.ID,
		ExternalID:   &extID,
		Identifier:   "HB 3",
		Title:        "Dismissed Bill",
		AutoImported: true,
		Following:    false,
	}
	require.NoError(t, CreateBill(ctx, pool, dismissed))

	// UpsertBill should return pgx.ErrNoRows for a dismissed bill.
	upsert := &Bill{
		BodyID:       body.ID,
		ExternalID:   &extID,
		Identifier:   "HB 3",
		Title:        "Should Not Update",
		AutoImported: true,
	}
	_, err := UpsertBill(ctx, pool, upsert)
	assert.ErrorIs(t, err, pgx.ErrNoRows, "upsert on dismissed bill should return ErrNoRows")

	// Confirm the title was NOT updated.
	// GetBill filters on following=true, so query the raw row directly.
	var title string
	require.NoError(t, pool.QueryRow(ctx, `SELECT title FROM bills WHERE id = $1`, dismissed.ID).Scan(&title))
	assert.Equal(t, "Dismissed Bill", title, "dismissed bill title must not be overwritten")
}

func TestCreateLegislativeBody(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	b := LegislativeBody{
		Name:       "KC City Council",
		Level:      "local",
		State:      "MO",
		DataSource: "legistar",
		Active:     true,
	}
	require.NoError(t, CreateLegislativeBody(ctx, pool, &b))
	assert.NotEmpty(t, b.ID)
	assert.False(t, b.CreatedAt.IsZero())
}
