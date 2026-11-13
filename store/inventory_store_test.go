package store

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"commons/internal/testhelpers"
)

func invUser(t *testing.T, pool *pgxpool.Pool, name string) string {
	t.Helper()
	var id string
	require.NoError(t, pool.QueryRow(context.Background(),
		`INSERT INTO users (display_name) VALUES ($1) RETURNING id`, name).Scan(&id))
	return id
}

func invItem(t *testing.T, pool *pgxpool.Pool, code string) *InventoryItem {
	t.Helper()
	days := 7
	it, err := CreateInventoryItem(context.Background(), pool,
		InventoryItemInput{Name: "Item " + code, VerifyEveryDays: &days}, code)
	require.NoError(t, err)
	return it
}

func invClaim(t *testing.T, pool *pgxpool.Pool, itemID, userID string) *CustodyEvent {
	t.Helper()
	ev, err := ApplyCustodyChange(context.Background(), pool, CustodyChange{ItemID: itemID, UserID: userID, Source: "slack"})
	require.NoError(t, err)
	return ev
}

func invExec(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	_, err := pool.Exec(context.Background(), sql, args...)
	require.NoError(t, err)
}

func invIDs(items []InventoryItem) []string {
	ids := make([]string, 0, len(items))
	for _, it := range items {
		ids = append(ids, it.ID)
	}
	return ids
}

func TestInventoryItemLifecycle(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	desc := "Epson"
	it, err := CreateInventoryItem(ctx, pool, InventoryItemInput{Name: "Projector", Description: &desc}, "ABC234")
	require.NoError(t, err)
	assert.Nil(t, it.VerifyEveryDays, "nil cadence means no reminders")
	assert.Empty(t, it.Metadata)

	_, err = CreateInventoryItem(ctx, pool, InventoryItemInput{Name: "Dup"}, "ABC234")
	assert.ErrorIs(t, err, ErrInventoryCodeTaken)

	got, err := GetInventoryItemByCode(ctx, pool, "  abc234 ")
	require.NoError(t, err)
	assert.Equal(t, it.ID, got.ID)

	_, err = GetInventoryItemByCode(ctx, pool, "NOPE22")
	assert.ErrorIs(t, err, ErrNotFound)

	days := 14
	loc := "Closet"
	require.NoError(t, UpdateInventoryItem(ctx, pool, it.ID, InventoryItemInput{Name: "Projector 2", Location: &loc, VerifyEveryDays: &days}))
	grace := 3
	require.NoError(t, SetInventoryItemGrace(ctx, pool, it.ID, &grace))
	got, err = GetInventoryItem(ctx, pool, it.ID)
	require.NoError(t, err)
	assert.Equal(t, "Projector 2", got.Name)
	assert.Nil(t, got.Description, "update replaces the editable fields")
	assert.Equal(t, 14, *got.VerifyEveryDays)
	assert.Equal(t, 3, *got.VerifyGraceDays)

	assert.ErrorIs(t, UpdateInventoryItem(ctx, pool, "00000000-0000-0000-0000-000000000000", InventoryItemInput{Name: "x"}), ErrNotFound)

	// Rotation
	other := invItem(t, pool, "OTHER2")
	assert.ErrorIs(t, RotateInventoryItemCode(ctx, pool, it.ID, other.Code), ErrInventoryCodeTaken)
	require.NoError(t, RotateInventoryItemCode(ctx, pool, it.ID, "ROT234"))
	_, err = GetInventoryItemByCode(ctx, pool, "ABC234")
	assert.ErrorIs(t, err, ErrNotFound, "old code stops working")
	_, err = GetInventoryItemByCode(ctx, pool, "ROT234")
	assert.NoError(t, err)

	// Soft delete and restore
	require.NoError(t, ArchiveInventoryItem(ctx, pool, it.ID))
	_, err = GetInventoryItemByCode(ctx, pool, "ROT234")
	assert.ErrorIs(t, err, ErrNotFound, "archived items are not claimable by code")
	active, err := ListInventoryItems(ctx, pool, false)
	require.NoError(t, err)
	assert.NotContains(t, invIDs(active), it.ID)
	all, err := ListInventoryItems(ctx, pool, true)
	require.NoError(t, err)
	assert.Contains(t, invIDs(all), it.ID)
	got, err = GetInventoryItem(ctx, pool, it.ID)
	require.NoError(t, err)
	assert.NotNil(t, got.ArchivedAt)

	_, err = CreateInventoryItem(ctx, pool, InventoryItemInput{Name: "Reuse"}, "ROT234")
	assert.ErrorIs(t, err, ErrInventoryCodeTaken, "archived items keep their code")

	require.NoError(t, RestoreInventoryItem(ctx, pool, it.ID))
	_, err = GetInventoryItemByCode(ctx, pool, "ROT234")
	assert.NoError(t, err)
	assert.ErrorIs(t, RestoreInventoryItem(ctx, pool, it.ID), ErrNotFound, "restoring an active item matches nothing")
}

func TestInventoryFieldDefinitionLifecycle(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	serial, err := CreateInventoryFieldDefinition(ctx, pool, "serial", "Serial Number")
	require.NoError(t, err)
	color, err := CreateInventoryFieldDefinition(ctx, pool, "color", "Color")
	require.NoError(t, err)
	assert.Less(t, serial.SortOrder, color.SortOrder)

	_, err = CreateInventoryFieldDefinition(ctx, pool, "serial", "Again")
	assert.ErrorIs(t, err, ErrInventoryFieldKeyTaken)

	defs, err := ListInventoryFieldDefinitions(ctx, pool, false)
	require.NoError(t, err)
	require.Len(t, defs, 2)
	assert.Equal(t, "serial", defs[0].Key)

	require.NoError(t, ReorderInventoryFieldDefinitions(ctx, pool, []string{color.ID, serial.ID}))
	defs, err = ListInventoryFieldDefinitions(ctx, pool, false)
	require.NoError(t, err)
	assert.Equal(t, "color", defs[0].Key)

	require.NoError(t, UpdateInventoryFieldDefinition(ctx, pool, serial.ID, "S/N", false))
	active, err := ListInventoryFieldDefinitions(ctx, pool, true)
	require.NoError(t, err)
	require.Len(t, active, 1)
	assert.Equal(t, "color", active[0].Key)
	defs, err = ListInventoryFieldDefinitions(ctx, pool, false)
	require.NoError(t, err)
	assert.Len(t, defs, 2)

	require.NoError(t, DeleteInventoryFieldDefinition(ctx, pool, color.ID))
	assert.ErrorIs(t, DeleteInventoryFieldDefinition(ctx, pool, color.ID), ErrNotFound)
}

func TestInventoryMetadata(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	it := invItem(t, pool, "META22")
	serial, err := CreateInventoryFieldDefinition(ctx, pool, "serial", "Serial")
	require.NoError(t, err)
	_, err = CreateInventoryFieldDefinition(ctx, pool, "color", "Color")
	require.NoError(t, err)

	require.NoError(t, SetInventoryItemMetadata(ctx, pool, it.ID, map[string]string{"serial": "X1", "color": "red"}))
	got, err := GetInventoryItem(ctx, pool, it.ID)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"serial": "X1", "color": "red"}, got.Metadata)

	// Empty value removes the key; other keys are untouched.
	require.NoError(t, SetInventoryItemMetadata(ctx, pool, it.ID, map[string]string{"color": ""}))
	got, _ = GetInventoryItem(ctx, pool, it.ID)
	assert.Equal(t, map[string]string{"serial": "X1"}, got.Metadata)

	// An unknown key rejects the whole write.
	err = SetInventoryItemMetadata(ctx, pool, it.ID, map[string]string{"serial": "CHANGED", "bogus": "z"})
	assert.ErrorIs(t, err, ErrInventoryUnknownField)
	got, _ = GetInventoryItem(ctx, pool, it.ID)
	assert.Equal(t, "X1", got.Metadata["serial"])

	// Deactivating a field hides it from writes but keeps the stored value.
	require.NoError(t, UpdateInventoryFieldDefinition(ctx, pool, serial.ID, "Serial", false))
	assert.ErrorIs(t, SetInventoryItemMetadata(ctx, pool, it.ID, map[string]string{"serial": "NEW"}), ErrInventoryUnknownField)
	require.NoError(t, SetInventoryItemMetadata(ctx, pool, it.ID, map[string]string{"color": "blue"}))
	got, _ = GetInventoryItem(ctx, pool, it.ID)
	assert.Equal(t, map[string]string{"serial": "X1", "color": "blue"}, got.Metadata)

	assert.ErrorIs(t, SetInventoryItemMetadata(ctx, pool, "00000000-0000-0000-0000-000000000000", nil), ErrNotFound)
}

func TestInventoryApplyCustodyChange(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	alice := invUser(t, pool, "Alice")
	bob := invUser(t, pool, "Bob")
	it := invItem(t, pool, "CUST22")

	ev := invClaim(t, pool, it.ID, alice)
	assert.Equal(t, "applied", ev.Status)
	assert.Nil(t, ev.PreviousHolderUserID)
	assert.NotNil(t, ev.AppliedAt)

	got, _ := GetInventoryItem(ctx, pool, it.ID)
	assert.Equal(t, alice, *got.CurrentHolderUserID)
	assert.NotNil(t, got.LastVerifiedAt, "a claim counts as verification")

	ev = invClaim(t, pool, it.ID, bob)
	assert.Equal(t, alice, *ev.PreviousHolderUserID)

	// Already the holder: no-op, nothing written.
	_, err := ApplyCustodyChange(ctx, pool, CustodyChange{ItemID: it.ID, UserID: bob, Source: "slack"})
	assert.ErrorIs(t, err, ErrInventoryAlreadyHolder)
	events, err := ListInventoryCustodyEvents(ctx, pool, it.ID)
	require.NoError(t, err)
	assert.Len(t, events, 2)

	// A new holder restarts the verification cycle.
	invExec(t, pool, `UPDATE inventory.items SET last_reminded_at = NOW(), last_escalated_at = NOW() WHERE id = $1`, it.ID)
	invClaim(t, pool, it.ID, alice)
	got, _ = GetInventoryItem(ctx, pool, it.ID)
	assert.Nil(t, got.LastRemindedAt)
	assert.Nil(t, got.LastEscalatedAt)

	held, err := ListInventoryItemsHeldBy(ctx, pool, alice)
	require.NoError(t, err)
	assert.Equal(t, []string{it.ID}, invIDs(held))

	require.NoError(t, ArchiveInventoryItem(ctx, pool, it.ID))
	_, err = ApplyCustodyChange(ctx, pool, CustodyChange{ItemID: it.ID, UserID: bob, Source: "slack"})
	assert.ErrorIs(t, err, ErrInventoryItemArchived)

	_, err = ApplyCustodyChange(ctx, pool, CustodyChange{ItemID: "00000000-0000-0000-0000-000000000000", UserID: bob, Source: "slack"})
	assert.ErrorIs(t, err, ErrNotFound)

	held, err = ListInventoryItemsHeldBy(ctx, pool, alice)
	require.NoError(t, err)
	assert.Empty(t, held, "archived items are not listed as held")
}

func TestInventoryHandOffGuard(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	alice := invUser(t, pool, "Alice")
	bob := invUser(t, pool, "Bob")
	carol := invUser(t, pool, "Carol")
	it := invItem(t, pool, "HAND22")
	invClaim(t, pool, it.ID, alice)

	// Bob is not the holder, so he cannot hand the item to Carol.
	_, err := ApplyCustodyChange(ctx, pool, CustodyChange{ItemID: it.ID, UserID: carol, FromUserID: bob, Source: "slack"})
	assert.ErrorIs(t, err, ErrInventoryNotHolder)
	got, _ := GetInventoryItem(ctx, pool, it.ID)
	assert.Equal(t, alice, *got.CurrentHolderUserID)
	events, _ := ListInventoryCustodyEvents(ctx, pool, it.ID)
	assert.Len(t, events, 1, "a rejected hand-off writes nothing")

	// An unheld item cannot be handed off either.
	loose := invItem(t, pool, "LOOS22")
	_, err = ApplyCustodyChange(ctx, pool, CustodyChange{ItemID: loose.ID, UserID: carol, FromUserID: alice, Source: "slack"})
	assert.ErrorIs(t, err, ErrInventoryNotHolder)

	// The holder can.
	note := "Handed off by Alice"
	ev, err := ApplyCustodyChange(ctx, pool, CustodyChange{ItemID: it.ID, UserID: bob, FromUserID: alice, Source: "slack", Note: &note})
	require.NoError(t, err)
	assert.Equal(t, alice, *ev.PreviousHolderUserID)
	assert.Equal(t, "Handed off by Alice", *ev.Note)
	got, _ = GetInventoryItem(ctx, pool, it.ID)
	assert.Equal(t, bob, *got.CurrentHolderUserID)

	// Alice no longer holds it, so a stale second hand-off is rejected.
	_, err = ApplyCustodyChange(ctx, pool, CustodyChange{ItemID: it.ID, UserID: carol, FromUserID: alice, Source: "slack"})
	assert.ErrorIs(t, err, ErrInventoryNotHolder)
}

func TestInventoryPendingClaimsAndLinking(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	alice := invUser(t, pool, "Alice")
	bob := invUser(t, pool, "Bob")
	carol := invUser(t, pool, "Carol")
	it := invItem(t, pool, "LINK22")
	invClaim(t, pool, it.ID, alice)

	ip, ua := "203.0.113.5", "TestAgent"
	ev, err := RecordPendingClaim(ctx, pool, it.ID, "Robert", "bob@elsewhere.example", &ip, &ua)
	require.NoError(t, err)
	assert.Equal(t, "pending_unmatched", ev.Status)
	assert.Nil(t, ev.UserID)
	assert.Nil(t, ev.AppliedAt)
	assert.Equal(t, alice, *ev.PreviousHolderUserID)

	got, _ := GetInventoryItem(ctx, pool, it.ID)
	assert.Equal(t, alice, *got.CurrentHolderUserID, "holder is unchanged until an admin links the claim")

	pending, err := ListPendingUnmatched(ctx, pool)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	assert.Equal(t, "Item LINK22", pending[0].ItemName)
	assert.Equal(t, "LINK22", pending[0].ItemCode)
	assert.Equal(t, "Alice", *pending[0].PreviousHolderName)
	assert.Equal(t, "bob@elsewhere.example", *pending[0].RawEmail)

	// The holder changes while the claim waits; linking records who was actually replaced.
	invClaim(t, pool, it.ID, carol)
	linked, err := LinkClaimToUser(ctx, pool, ev.ID, bob)
	require.NoError(t, err)
	assert.Equal(t, "linked", linked.Status)
	assert.Equal(t, bob, *linked.UserID)
	assert.Equal(t, carol, *linked.PreviousHolderUserID)
	assert.NotNil(t, linked.AppliedAt)

	got, _ = GetInventoryItem(ctx, pool, it.ID)
	assert.Equal(t, bob, *got.CurrentHolderUserID)

	pending, err = ListPendingUnmatched(ctx, pool)
	require.NoError(t, err)
	assert.Empty(t, pending)

	_, err = LinkClaimToUser(ctx, pool, ev.ID, bob)
	assert.ErrorIs(t, err, ErrInventoryClaimNotPending)
	_, err = LinkClaimToUser(ctx, pool, "00000000-0000-0000-0000-000000000000", bob)
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestInventoryRevertCustody(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	alice := invUser(t, pool, "Alice")
	bob := invUser(t, pool, "Bob")
	it := invItem(t, pool, "REVT22")

	_, err := RevertCustody(ctx, pool, it.ID, nil)
	assert.ErrorIs(t, err, ErrInventoryNothingToRevert)

	invClaim(t, pool, it.ID, alice)
	invClaim(t, pool, it.ID, bob)
	_, err = RecordVerification(ctx, pool, it.ID, nil) // not a holder change; revert must skip it
	require.NoError(t, err)

	note := "wrong person"
	ev, err := RevertCustody(ctx, pool, it.ID, &note)
	require.NoError(t, err)
	assert.Equal(t, "revert", ev.Source)
	assert.Equal(t, alice, *ev.UserID)
	assert.Equal(t, bob, *ev.PreviousHolderUserID)
	got, _ := GetInventoryItem(ctx, pool, it.ID)
	assert.Equal(t, alice, *got.CurrentHolderUserID)

	// Reverting the revert puts the original holder back.
	_, err = RevertCustody(ctx, pool, it.ID, nil)
	require.NoError(t, err)
	got, _ = GetInventoryItem(ctx, pool, it.ID)
	assert.Equal(t, bob, *got.CurrentHolderUserID)

	// Reverting the first claim leaves the item unheld.
	solo := invItem(t, pool, "SOLO22")
	invClaim(t, pool, solo.ID, alice)
	_, err = RevertCustody(ctx, pool, solo.ID, nil)
	require.NoError(t, err)
	got, _ = GetInventoryItem(ctx, pool, solo.ID)
	assert.Nil(t, got.CurrentHolderUserID)
}

func TestInventoryRecordVerification(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	alice := invUser(t, pool, "Alice")
	it := invItem(t, pool, "VERF22")

	_, err := RecordVerification(ctx, pool, it.ID, nil)
	assert.ErrorIs(t, err, ErrInventoryNoHolder)

	invClaim(t, pool, it.ID, alice)
	invExec(t, pool, `UPDATE inventory.items SET last_verified_at = NOW() - INTERVAL '30 days',
		last_reminded_at = NOW(), last_escalated_at = NOW() WHERE id = $1`, it.ID)

	ev, err := RecordVerification(ctx, pool, it.ID, nil)
	require.NoError(t, err)
	assert.Equal(t, "verify", ev.Source)
	assert.Equal(t, alice, *ev.UserID)

	got, _ := GetInventoryItem(ctx, pool, it.ID)
	assert.Equal(t, alice, *got.CurrentHolderUserID)
	assert.Nil(t, got.LastRemindedAt)
	assert.Nil(t, got.LastEscalatedAt)
	due, err := ListInventoryItemsDueForVerification(ctx, pool)
	require.NoError(t, err)
	assert.NotContains(t, invIDs(due), it.ID, "verification starts a new cycle")
}

func TestInventoryDispute(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	alice := invUser(t, pool, "Alice")
	it := invItem(t, pool, "DISP22")

	_, err := RecordInventoryDispute(ctx, pool, it.ID, nil)
	assert.ErrorIs(t, err, ErrInventoryNoHolder)

	invClaim(t, pool, it.ID, alice)
	invExec(t, pool, `UPDATE inventory.items SET last_verified_at = NOW() - INTERVAL '10 days',
		last_reminded_at = NOW() - INTERVAL '6 days' WHERE id = $1`, it.ID)
	pastGrace, err := ListInventoryItemsPastGrace(ctx, pool)
	require.NoError(t, err)
	require.Equal(t, []string{it.ID}, invIDs(pastGrace), "it would be escalated by the grace period")

	note := "Reported via verification reminder"
	ev, err := RecordInventoryDispute(ctx, pool, it.ID, &note)
	require.NoError(t, err)
	assert.Equal(t, "disputed", ev.Status)
	assert.Equal(t, "slack", ev.Source)
	assert.Equal(t, alice, *ev.UserID)
	assert.Nil(t, ev.AppliedAt, "a dispute is not a holder change")

	got, _ := GetInventoryItem(ctx, pool, it.ID)
	assert.Equal(t, alice, *got.CurrentHolderUserID, "the holder does not change")
	assert.NotNil(t, got.LastEscalatedAt)
	pastGrace, err = ListInventoryItemsPastGrace(ctx, pool)
	require.NoError(t, err)
	assert.Empty(t, pastGrace, "the grace-period alert does not fire again after a dispute")

	// A dispute is not a custody change, so revert still undoes the claim, not the dispute.
	_, err = RevertCustody(ctx, pool, it.ID, nil)
	require.NoError(t, err)
	got, _ = GetInventoryItem(ctx, pool, it.ID)
	assert.Nil(t, got.CurrentHolderUserID)

	require.NoError(t, ArchiveInventoryItem(ctx, pool, it.ID))
	_, err = RecordInventoryDispute(ctx, pool, it.ID, nil)
	assert.ErrorIs(t, err, ErrInventoryItemArchived)
}

func TestInventoryVerificationReminders(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	alice := invUser(t, pool, "Alice")
	age := func(id string, days int) {
		invExec(t, pool, `UPDATE inventory.items SET last_verified_at = NOW() - make_interval(days => $2) WHERE id = $1`, id, days)
	}

	due := invItem(t, pool, "DUE222")
	recent := invItem(t, pool, "RCNT22")
	archived := invItem(t, pool, "ARCH22")
	unheld := invItem(t, pool, "UNHD22")
	noCadence := invItem(t, pool, "NOCD22")
	for _, it := range []*InventoryItem{due, recent, archived, noCadence} {
		invClaim(t, pool, it.ID, alice)
	}
	age(due.ID, 8)
	age(recent.ID, 3)
	age(archived.ID, 10)
	age(noCadence.ID, 10)
	require.NoError(t, ArchiveInventoryItem(ctx, pool, archived.ID))
	invExec(t, pool, `UPDATE inventory.items SET verify_every_days = NULL WHERE id = $1`, noCadence.ID)
	invExec(t, pool, `UPDATE inventory.items SET created_at = NOW() - INTERVAL '30 days' WHERE id = $1`, unheld.ID)

	items, err := ListInventoryItemsDueForVerification(ctx, pool)
	require.NoError(t, err)
	assert.Equal(t, []string{due.ID}, invIDs(items))

	// One reminder per cycle.
	require.NoError(t, MarkInventoryItemReminded(ctx, pool, due.ID))
	items, err = ListInventoryItemsDueForVerification(ctx, pool)
	require.NoError(t, err)
	assert.Empty(t, items)

	pastGrace := func() []string {
		items, err := ListInventoryItemsPastGrace(ctx, pool)
		require.NoError(t, err)
		return invIDs(items)
	}
	remindedAgo := func(days int) {
		invExec(t, pool, `UPDATE inventory.items SET last_reminded_at = NOW() - make_interval(days => $2) WHERE id = $1`, due.ID, days)
	}

	assert.Empty(t, pastGrace(), "just reminded")
	remindedAgo(4)
	assert.Empty(t, pastGrace(), "inside the default 5 day grace")
	remindedAgo(6)
	assert.Equal(t, []string{due.ID}, pastGrace(), "past the default grace")

	// Per-item grace overrides the default.
	ten := 10
	require.NoError(t, SetInventoryItemGrace(ctx, pool, due.ID, &ten))
	assert.Empty(t, pastGrace())
	three := 3
	require.NoError(t, SetInventoryItemGrace(ctx, pool, due.ID, &three))
	remindedAgo(4)
	assert.Equal(t, []string{due.ID}, pastGrace())

	// The config default applies when the item has no grace of its own.
	require.NoError(t, SetInventoryItemGrace(ctx, pool, due.ID, nil))
	invExec(t, pool, `UPDATE config_store SET value = '2' WHERE service = 'inventory' AND key = 'default_grace_days'`)
	remindedAgo(3)
	assert.Equal(t, []string{due.ID}, pastGrace())
	invExec(t, pool, `UPDATE config_store SET value = '30' WHERE service = 'inventory' AND key = 'default_grace_days'`)
	assert.Empty(t, pastGrace())
	invExec(t, pool, `UPDATE config_store SET value = 'not a number' WHERE service = 'inventory' AND key = 'default_grace_days'`)
	remindedAgo(6)
	assert.Equal(t, []string{due.ID}, pastGrace(), "a bad config value falls back to 5 days")

	// Admins are alerted once per cycle.
	require.NoError(t, MarkInventoryItemEscalated(ctx, pool, due.ID))
	assert.Empty(t, pastGrace())
}

func TestFindUserForClaimEmail(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	primary := invUser(t, pool, "Primary")
	invExec(t, pool, `UPDATE users SET email = 'primary@example.com' WHERE id = $1`, primary)
	viaSlack := invUser(t, pool, "Via Slack")
	invExec(t, pool, `INSERT INTO user_identities (user_id, provider, external_id, external_email)
		VALUES ($1, 'slack', 'U1', 'Slack.Only@Example.com')`, viaSlack)

	u, err := FindUserForClaimEmail(ctx, pool, "  PRIMARY@Example.com ")
	require.NoError(t, err)
	assert.Equal(t, primary, u.ID, "users.email matches case-insensitively")

	u, err = FindUserForClaimEmail(ctx, pool, "slack.only@example.com")
	require.NoError(t, err)
	assert.Equal(t, viaSlack, u.ID, "falls back to a single external email")

	_, err = FindUserForClaimEmail(ctx, pool, "nobody@example.com")
	assert.ErrorIs(t, err, ErrNotFound)
	_, err = FindUserForClaimEmail(ctx, pool, "   ")
	assert.ErrorIs(t, err, ErrNotFound)

	// An external email shared by two members is ambiguous, so it is not a match.
	other := invUser(t, pool, "Other")
	invExec(t, pool, `INSERT INTO user_identities (user_id, provider, external_id, external_email)
		VALUES ($1, 'slack', 'U2', 'slack.only@example.com')`, other)
	_, err = FindUserForClaimEmail(ctx, pool, "slack.only@example.com")
	assert.ErrorIs(t, err, ErrNotFound)

	// The primary email wins over an external one.
	invExec(t, pool, `INSERT INTO user_identities (user_id, provider, external_id, external_email)
		VALUES ($1, 'slack', 'U3', 'primary@example.com')`, other)
	u, err = FindUserForClaimEmail(ctx, pool, "primary@example.com")
	require.NoError(t, err)
	assert.Equal(t, primary, u.ID)
}

func TestCustodyEventLookupAndPendingCount(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	alice := invUser(t, pool, "Alice")
	it := invItem(t, pool, "CNT222")

	n, err := CountPendingUnmatched(ctx, pool)
	require.NoError(t, err)
	assert.Zero(t, n)

	ev, err := RecordPendingClaim(ctx, pool, it.ID, "Robert", "bob@elsewhere.example", nil, nil)
	require.NoError(t, err)
	n, _ = CountPendingUnmatched(ctx, pool)
	assert.Equal(t, 1, n)

	got, err := GetCustodyEvent(ctx, pool, ev.ID)
	require.NoError(t, err)
	assert.Equal(t, "pending_unmatched", got.Status)
	assert.Equal(t, "Robert", *got.RawName)
	_, err = GetCustodyEvent(ctx, pool, "00000000-0000-0000-0000-000000000000")
	assert.ErrorIs(t, err, ErrNotFound)

	_, err = LinkClaimToUser(ctx, pool, ev.ID, alice)
	require.NoError(t, err)
	n, _ = CountPendingUnmatched(ctx, pool)
	assert.Zero(t, n, "a linked claim is no longer pending")
}

func TestGetUserByUniqueExternalEmail(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	pat := invUser(t, pool, "Pat")
	sam := invUser(t, pool, "Sam")
	addIdentity := func(userID, provider, externalID, email string) {
		invExec(t, pool, `INSERT INTO user_identities (user_id, provider, external_id, external_email)
			VALUES ($1, $2, $3, $4)`, userID, provider, externalID, email)
	}

	_, err := GetUserByUniqueExternalEmail(ctx, pool, "nobody@example.com")
	assert.ErrorIs(t, err, ErrNotFound)
	_, err = GetUserByUniqueExternalEmail(ctx, pool, "  ")
	assert.ErrorIs(t, err, ErrNotFound)

	addIdentity(pat, "slack", "U1", "Pat@Example.com")
	u, err := GetUserByUniqueExternalEmail(ctx, pool, "pat@example.COM")
	require.NoError(t, err)
	assert.Equal(t, pat, u.ID)

	// The same user on two providers is still one match.
	addIdentity(pat, "discord", "D1", "pat@example.com")
	u, err = GetUserByUniqueExternalEmail(ctx, pool, "pat@example.com")
	require.NoError(t, err)
	assert.Equal(t, pat, u.ID)

	// A second user with the same address makes it ambiguous.
	addIdentity(sam, "slack", "U2", "pat@example.com")
	_, err = GetUserByUniqueExternalEmail(ctx, pool, "pat@example.com")
	assert.ErrorIs(t, err, ErrNotFound)
}
