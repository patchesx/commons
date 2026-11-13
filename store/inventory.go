package store

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrInventoryCodeTaken is returned when an item code is already in use (archived items keep theirs).
var ErrInventoryCodeTaken = errors.New("inventory code already in use")

// ErrInventoryAlreadyHolder is returned by ApplyCustodyChange when the user already holds the item.
// No event is written.
var ErrInventoryAlreadyHolder = errors.New("user already holds this item")

// ErrInventoryNotHolder is returned by ApplyCustodyChange when FromUserID is set and that user no longer
// holds the item.
var ErrInventoryNotHolder = errors.New("user does not hold this item")

// ErrInventoryItemArchived is returned when a custody change targets an archived item.
var ErrInventoryItemArchived = errors.New("inventory item is archived")

// ErrInventoryNoHolder is returned when an item has no current holder but the operation needs one.
var ErrInventoryNoHolder = errors.New("inventory item has no holder")

// ErrInventoryClaimNotPending is returned when linking a claim that is not pending_unmatched.
var ErrInventoryClaimNotPending = errors.New("claim is not pending")

// ErrInventoryNothingToRevert is returned when an item has no holder-changing event to undo.
var ErrInventoryNothingToRevert = errors.New("no custody change to revert")

// ErrInventoryUnknownField is returned when metadata names a field that is not an active definition.
var ErrInventoryUnknownField = errors.New("unknown or inactive inventory field")

// ErrInventoryFieldKeyTaken is returned when a custom field key already exists.
var ErrInventoryFieldKeyTaken = errors.New("inventory field key already in use")

// InventoryItem is a tracked item. Metadata holds custom field values keyed by field key.
type InventoryItem struct {
	ID                  string
	Name                string
	Description         *string
	Location            *string
	Code                string
	Metadata            map[string]string
	VerifyEveryDays     *int // nil = no verification reminders
	VerifyGraceDays     *int // nil = use the inventory.default_grace_days config value
	LastVerifiedAt      *time.Time
	LastRemindedAt      *time.Time
	LastEscalatedAt     *time.Time
	CurrentHolderUserID *string
	ArchivedAt          *time.Time
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

// InventoryItemInput holds the admin-editable item fields. A nil VerifyEveryDays means no reminders.
type InventoryItemInput struct {
	Name            string
	Description     *string
	Location        *string
	VerifyEveryDays *int
}

// InventoryFieldDefinition names and orders a custom field. Values are always optional strings.
type InventoryFieldDefinition struct {
	ID        string
	Key       string
	Label     string
	Kind      string   // InventoryFieldText or InventoryFieldSelect
	Options   []string // the allowed values of a select field, in display order
	SortOrder int
	Active    bool
	CreatedAt time.Time
}

// CustodyEvent is one row of an item's possession history.
type CustodyEvent struct {
	ID                   string
	ItemID               string
	UserID               *string
	PreviousHolderUserID *string
	RawName              *string
	RawEmail             *string
	Source               string // slack, web_form, admin, verify, revert
	Status               string // applied, pending_unmatched, linked, disputed
	IPAddress            *string
	UserAgent            *string
	Note                 *string
	AppliedAt            *time.Time
	CreatedAt            time.Time
}

// CustodyChange describes a holder change to apply immediately.
type CustodyChange struct {
	ItemID     string
	UserID     string
	FromUserID string // when set (hand-offs), the change applies only while this user is the holder
	Source     string
	RawName    *string
	RawEmail   *string
	IPAddress  *string
	UserAgent  *string
	Note       *string
}

// PendingClaim is an unmatched web-form claim awaiting an admin, with the item and previous holder joined in.
type PendingClaim struct {
	CustodyEvent
	ItemName           string
	ItemCode           string
	PreviousHolderName *string
}

const inventoryItemCols = `id, name, description, location, code, metadata, verify_every_days, verify_grace_days,
	last_verified_at, last_reminded_at, last_escalated_at, current_holder_user_id, archived_at, created_at, updated_at`

const custodyEventCols = `id, item_id, user_id, previous_holder_user_id, raw_name, raw_email, source, status,
	ip_address, user_agent, note, applied_at, created_at`

func scanInventoryItem(row pgx.Row) (*InventoryItem, error) {
	it := &InventoryItem{}
	err := row.Scan(&it.ID, &it.Name, &it.Description, &it.Location, &it.Code, &it.Metadata,
		&it.VerifyEveryDays, &it.VerifyGraceDays, &it.LastVerifiedAt, &it.LastRemindedAt, &it.LastEscalatedAt,
		&it.CurrentHolderUserID, &it.ArchivedAt, &it.CreatedAt, &it.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if it.Metadata == nil {
		it.Metadata = map[string]string{}
	}
	return it, nil
}

func scanInventoryItems(rows pgx.Rows) ([]InventoryItem, error) {
	defer rows.Close()
	items := make([]InventoryItem, 0)
	for rows.Next() {
		it, err := scanInventoryItem(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, *it)
	}
	return items, rows.Err()
}

func scanCustodyEvent(row pgx.Row) (*CustodyEvent, error) {
	e := &CustodyEvent{}
	err := row.Scan(&e.ID, &e.ItemID, &e.UserID, &e.PreviousHolderUserID, &e.RawName, &e.RawEmail,
		&e.Source, &e.Status, &e.IPAddress, &e.UserAgent, &e.Note, &e.AppliedAt, &e.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return e, nil
}

// ---- Items ----

// CreateInventoryItem inserts an item with the given code. Code generation lives with the caller;
// returns ErrInventoryCodeTaken on a collision so the caller can retry with a new code.
func CreateInventoryItem(ctx context.Context, pool *pgxpool.Pool, in InventoryItemInput, code string) (*InventoryItem, error) {
	it, err := scanInventoryItem(pool.QueryRow(ctx, `
		INSERT INTO inventory.items (name, description, location, code, verify_every_days)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING `+inventoryItemCols,
		in.Name, in.Description, in.Location, code, in.VerifyEveryDays))
	if isUniqueViolation(err) {
		return nil, ErrInventoryCodeTaken
	}
	return it, err
}

// GetInventoryItem returns an item by ID, including archived items. Returns ErrNotFound if absent.
func GetInventoryItem(ctx context.Context, pool *pgxpool.Pool, id string) (*InventoryItem, error) {
	return scanInventoryItem(pool.QueryRow(ctx,
		`SELECT `+inventoryItemCols+` FROM inventory.items WHERE id = $1`, id))
}

// GetInventoryItemByCode returns an active item by its code, accepting any case and with or without the
// dash (see NormalizeInventoryCode). Archived items are treated as absent. Returns ErrNotFound if absent.
func GetInventoryItemByCode(ctx context.Context, pool *pgxpool.Pool, code string) (*InventoryItem, error) {
	return scanInventoryItem(pool.QueryRow(ctx,
		`SELECT `+inventoryItemCols+` FROM inventory.items WHERE code = $1 AND archived_at IS NULL`,
		NormalizeInventoryCode(code)))
}

// ListInventoryItems returns items ordered by name. Archived items are included only when requested.
func ListInventoryItems(ctx context.Context, pool *pgxpool.Pool, includeArchived bool) ([]InventoryItem, error) {
	rows, err := pool.Query(ctx, `
		SELECT `+inventoryItemCols+` FROM inventory.items
		WHERE $1 OR archived_at IS NULL
		ORDER BY name, code`, includeArchived)
	if err != nil {
		return nil, err
	}
	return scanInventoryItems(rows)
}

// ListInventoryItemsHeldBy returns the active items a user currently holds.
func ListInventoryItemsHeldBy(ctx context.Context, pool *pgxpool.Pool, userID string) ([]InventoryItem, error) {
	rows, err := pool.Query(ctx, `
		SELECT `+inventoryItemCols+` FROM inventory.items
		WHERE current_holder_user_id = $1 AND archived_at IS NULL
		ORDER BY name, code`, userID)
	if err != nil {
		return nil, err
	}
	return scanInventoryItems(rows)
}

// UpdateInventoryItem updates the admin-editable fields. Returns ErrNotFound if absent.
func UpdateInventoryItem(ctx context.Context, pool *pgxpool.Pool, id string, in InventoryItemInput) error {
	tag, err := pool.Exec(ctx, `
		UPDATE inventory.items
		SET name = $2, description = $3, location = $4, verify_every_days = $5, updated_at = NOW()
		WHERE id = $1`, id, in.Name, in.Description, in.Location, in.VerifyEveryDays)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetInventoryItemGrace sets the per-item grace period. A nil value falls back to the config default.
// Kept separate from UpdateInventoryItem because only inventory admins may change it.
func SetInventoryItemGrace(ctx context.Context, pool *pgxpool.Pool, id string, days *int) error {
	tag, err := pool.Exec(ctx,
		`UPDATE inventory.items SET verify_grace_days = $2, updated_at = NOW() WHERE id = $1`, id, days)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// RotateInventoryItemCode replaces an item's code. Returns ErrInventoryCodeTaken on a collision.
func RotateInventoryItemCode(ctx context.Context, pool *pgxpool.Pool, id, newCode string) error {
	tag, err := pool.Exec(ctx,
		`UPDATE inventory.items SET code = $2, updated_at = NOW() WHERE id = $1`, id, newCode)
	if isUniqueViolation(err) {
		return ErrInventoryCodeTaken
	}
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ArchiveInventoryItem soft-deletes an item. History and the current holder are kept.
func ArchiveInventoryItem(ctx context.Context, pool *pgxpool.Pool, id string) error {
	tag, err := pool.Exec(ctx,
		`UPDATE inventory.items SET archived_at = NOW(), updated_at = NOW() WHERE id = $1 AND archived_at IS NULL`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// RestoreInventoryItem un-archives an item.
func RestoreInventoryItem(ctx context.Context, pool *pgxpool.Pool, id string) error {
	tag, err := pool.Exec(ctx,
		`UPDATE inventory.items SET archived_at = NULL, updated_at = NOW() WHERE id = $1 AND archived_at IS NOT NULL`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ---- Custom fields ----

// ListInventoryFieldDefinitions returns field definitions in display order.
func ListInventoryFieldDefinitions(ctx context.Context, pool *pgxpool.Pool, activeOnly bool) ([]InventoryFieldDefinition, error) {
	rows, err := pool.Query(ctx, `
		SELECT id, key, label, kind, options, sort_order, active, created_at
		FROM inventory.field_definitions
		WHERE NOT $1 OR active
		ORDER BY sort_order, created_at`, activeOnly)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	defs := make([]InventoryFieldDefinition, 0)
	for rows.Next() {
		var d InventoryFieldDefinition
		if err := rows.Scan(&d.ID, &d.Key, &d.Label, &d.Kind, &d.Options, &d.SortOrder, &d.Active, &d.CreatedAt); err != nil {
			return nil, err
		}
		defs = append(defs, d)
	}
	return defs, rows.Err()
}

// CreateInventoryFieldDefinition adds a field at the end of the order.
// Returns ErrInventoryFieldKeyTaken if the key exists.
func CreateInventoryFieldDefinition(ctx context.Context, pool *pgxpool.Pool, key, label string) (*InventoryFieldDefinition, error) {
	d := &InventoryFieldDefinition{}
	err := pool.QueryRow(ctx, `
		INSERT INTO inventory.field_definitions (key, label, sort_order)
		SELECT $1, $2, COALESCE(MAX(sort_order), 0) + 1 FROM inventory.field_definitions
		RETURNING id, key, label, kind, options, sort_order, active, created_at`, key, label,
	).Scan(&d.ID, &d.Key, &d.Label, &d.Kind, &d.Options, &d.SortOrder, &d.Active, &d.CreatedAt)
	if isUniqueViolation(err) {
		return nil, ErrInventoryFieldKeyTaken
	}
	if err != nil {
		return nil, err
	}
	return d, nil
}

// UpdateInventoryFieldDefinition changes a field's label and active flag. The key is immutable
// because item metadata is keyed by it. Deactivating hides the field but keeps stored values.
func UpdateInventoryFieldDefinition(ctx context.Context, pool *pgxpool.Pool, id, label string, active bool) error {
	tag, err := pool.Exec(ctx,
		`UPDATE inventory.field_definitions SET label = $2, active = $3 WHERE id = $1`, id, label, active)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ReorderInventoryFieldDefinitions sets sort_order from the position of each ID in the slice.
func ReorderInventoryFieldDefinitions(ctx context.Context, pool *pgxpool.Pool, ids []string) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	for i, id := range ids {
		if _, err := tx.Exec(ctx,
			`UPDATE inventory.field_definitions SET sort_order = $2 WHERE id = $1`, id, i+1); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// DeleteInventoryFieldDefinition removes a definition. Values already stored in item metadata are left in place
// (hidden, like a deactivated field) and would reappear if a field with the same key were created again.
func DeleteInventoryFieldDefinition(ctx context.Context, pool *pgxpool.Pool, id string) error {
	tag, err := pool.Exec(ctx, `DELETE FROM inventory.field_definitions WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetInventoryItemMetadata merges custom field values into an item. Every key must be an active field
// definition (else ErrInventoryUnknownField and nothing is written). An empty value removes the key.
// Existing values for other keys, including deactivated fields, are kept.
func SetInventoryItemMetadata(ctx context.Context, pool *pgxpool.Pool, itemID string, values map[string]string) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var current map[string]string
	err = tx.QueryRow(ctx, `SELECT metadata FROM inventory.items WHERE id = $1 FOR UPDATE`, itemID).Scan(&current)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if current == nil {
		current = map[string]string{}
	}

	rows, err := tx.Query(ctx, `SELECT key, kind, options FROM inventory.field_definitions WHERE active`)
	if err != nil {
		return err
	}
	active := map[string]InventoryFieldDefinition{}
	for rows.Next() {
		var def InventoryFieldDefinition
		if err := rows.Scan(&def.Key, &def.Kind, &def.Options); err != nil {
			rows.Close()
			return err
		}
		active[def.Key] = def
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	for k, v := range values {
		def, ok := active[k]
		if !ok {
			return ErrInventoryUnknownField
		}
		if v != "" && v != current[k] {
			canonical, err := def.CheckValue(v)
			if err != nil {
				return err
			}
			v = canonical
		}
		if v == "" {
			delete(current, k)
		} else {
			current[k] = v
		}
	}

	if _, err := tx.Exec(ctx,
		`UPDATE inventory.items SET metadata = $2, updated_at = NOW() WHERE id = $1`, itemID, current); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ---- Custody ----

// lockInventoryItem row-locks an item and returns its holder and archived state.
func lockInventoryItem(ctx context.Context, tx pgx.Tx, itemID string) (holder *string, archived bool, err error) {
	var archivedAt *time.Time
	err = tx.QueryRow(ctx,
		`SELECT current_holder_user_id, archived_at FROM inventory.items WHERE id = $1 FOR UPDATE`, itemID,
	).Scan(&holder, &archivedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, ErrNotFound
	}
	return holder, archivedAt != nil, err
}

// setInventoryHolder changes the holder and restarts the verification cycle: a new holder counts as
// freshly verified, and reminder/escalation stamps are cleared.
func setInventoryHolder(ctx context.Context, tx pgx.Tx, itemID string, holder *string) error {
	_, err := tx.Exec(ctx, `
		UPDATE inventory.items
		SET current_holder_user_id = $2, last_verified_at = NOW(),
		    last_reminded_at = NULL, last_escalated_at = NULL, updated_at = NOW()
		WHERE id = $1`, itemID, holder)
	return err
}

// ApplyCustodyChange makes the user the item's holder and records an applied event with the previous holder.
// Returns ErrInventoryAlreadyHolder (writing nothing) if they already hold it, and ErrInventoryItemArchived
// for archived items.
func ApplyCustodyChange(ctx context.Context, pool *pgxpool.Pool, c CustodyChange) (*CustodyEvent, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	holder, archived, err := lockInventoryItem(ctx, tx, c.ItemID)
	if err != nil {
		return nil, err
	}
	if archived {
		return nil, ErrInventoryItemArchived
	}
	if c.FromUserID != "" && (holder == nil || *holder != c.FromUserID) {
		return nil, ErrInventoryNotHolder
	}
	if holder != nil && *holder == c.UserID {
		return nil, ErrInventoryAlreadyHolder
	}

	ev, err := scanCustodyEvent(tx.QueryRow(ctx, `
		INSERT INTO inventory.custody_events
		    (item_id, user_id, previous_holder_user_id, raw_name, raw_email, source, status,
		     ip_address, user_agent, note, applied_at)
		VALUES ($1, $2, $3, $4, $5, $6, 'applied', $7, $8, $9, NOW())
		RETURNING `+custodyEventCols,
		c.ItemID, c.UserID, holder, c.RawName, c.RawEmail, c.Source, c.IPAddress, c.UserAgent, c.Note))
	if err != nil {
		return nil, err
	}
	if err := setInventoryHolder(ctx, tx, c.ItemID, &c.UserID); err != nil {
		return nil, err
	}
	return ev, tx.Commit(ctx)
}

// RecordPendingClaim stores a web-form claim whose email matched no member. The item's holder is unchanged;
// the current holder is captured as previous_holder_user_id for the admin alert.
func RecordPendingClaim(ctx context.Context, pool *pgxpool.Pool, itemID, rawName, rawEmail string, ip, userAgent *string) (*CustodyEvent, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	holder, archived, err := lockInventoryItem(ctx, tx, itemID)
	if err != nil {
		return nil, err
	}
	if archived {
		return nil, ErrInventoryItemArchived
	}

	ev, err := scanCustodyEvent(tx.QueryRow(ctx, `
		INSERT INTO inventory.custody_events
		    (item_id, previous_holder_user_id, raw_name, raw_email, source, status, ip_address, user_agent)
		VALUES ($1, $2, $3, $4, 'web_form', 'pending_unmatched', $5, $6)
		RETURNING `+custodyEventCols,
		itemID, holder, rawName, rawEmail, ip, userAgent))
	if err != nil {
		return nil, err
	}
	return ev, tx.Commit(ctx)
}

// GetCustodyEvent returns one custody event. Returns ErrNotFound if absent.
func GetCustodyEvent(ctx context.Context, pool *pgxpool.Pool, id string) (*CustodyEvent, error) {
	return scanCustodyEvent(pool.QueryRow(ctx,
		`SELECT `+custodyEventCols+` FROM inventory.custody_events WHERE id = $1`, id))
}

// CountPendingUnmatched returns how many web-form claims are waiting for an admin to link them.
func CountPendingUnmatched(ctx context.Context, pool *pgxpool.Pool) (int, error) {
	var n int
	err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM inventory.custody_events WHERE status = 'pending_unmatched'`).Scan(&n)
	return n, err
}

// ListPendingUnmatched returns unresolved web-form claims, oldest first.
func ListPendingUnmatched(ctx context.Context, pool *pgxpool.Pool) ([]PendingClaim, error) {
	rows, err := pool.Query(ctx, `
		SELECT e.id, e.item_id, e.user_id, e.previous_holder_user_id, e.raw_name, e.raw_email, e.source, e.status,
		       e.ip_address, e.user_agent, e.note, e.applied_at, e.created_at,
		       i.name, i.code, COALESCE(ph.display_name, ph.email)
		FROM inventory.custody_events e
		JOIN inventory.items i ON i.id = e.item_id
		LEFT JOIN users ph ON ph.id = e.previous_holder_user_id
		WHERE e.status = 'pending_unmatched'
		ORDER BY e.created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	claims := make([]PendingClaim, 0)
	for rows.Next() {
		var c PendingClaim
		if err := rows.Scan(&c.ID, &c.ItemID, &c.UserID, &c.PreviousHolderUserID, &c.RawName, &c.RawEmail,
			&c.Source, &c.Status, &c.IPAddress, &c.UserAgent, &c.Note, &c.AppliedAt, &c.CreatedAt,
			&c.ItemName, &c.ItemCode, &c.PreviousHolderName); err != nil {
			return nil, err
		}
		claims = append(claims, c)
	}
	return claims, rows.Err()
}

// LinkClaimToUser resolves a pending claim: the user becomes the item's holder and the event becomes
// linked. previous_holder_user_id is refreshed to whoever actually held the item at link time, so a later
// revert restores the right person. Returns ErrInventoryClaimNotPending if the claim was already resolved.
func LinkClaimToUser(ctx context.Context, pool *pgxpool.Pool, eventID, userID string) (*CustodyEvent, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var itemID, status string
	err = tx.QueryRow(ctx,
		`SELECT item_id, status FROM inventory.custody_events WHERE id = $1 FOR UPDATE`, eventID,
	).Scan(&itemID, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if status != "pending_unmatched" {
		return nil, ErrInventoryClaimNotPending
	}

	holder, archived, err := lockInventoryItem(ctx, tx, itemID)
	if err != nil {
		return nil, err
	}
	if archived {
		return nil, ErrInventoryItemArchived
	}

	ev, err := scanCustodyEvent(tx.QueryRow(ctx, `
		UPDATE inventory.custody_events
		SET user_id = $2, previous_holder_user_id = $3, status = 'linked', applied_at = NOW()
		WHERE id = $1
		RETURNING `+custodyEventCols, eventID, userID, holder))
	if err != nil {
		return nil, err
	}
	if err := setInventoryHolder(ctx, tx, itemID, &userID); err != nil {
		return nil, err
	}
	return ev, tx.Commit(ctx)
}

// RevertCustody undoes the item's most recent holder change by restoring that event's previous holder
// (possibly nobody) and recording a revert event. Verification and pending events are not holder changes.
// Reverting twice restores the original holder. Returns ErrInventoryNothingToRevert if there is no change.
func RevertCustody(ctx context.Context, pool *pgxpool.Pool, itemID string, note *string) (*CustodyEvent, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	holder, archived, err := lockInventoryItem(ctx, tx, itemID)
	if err != nil {
		return nil, err
	}
	if archived {
		return nil, ErrInventoryItemArchived
	}

	var target *string
	err = tx.QueryRow(ctx, `
		SELECT previous_holder_user_id FROM inventory.custody_events
		WHERE item_id = $1 AND source <> 'verify' AND status IN ('applied', 'linked') AND applied_at IS NOT NULL
		ORDER BY applied_at DESC, created_at DESC
		LIMIT 1`, itemID).Scan(&target)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrInventoryNothingToRevert
	}
	if err != nil {
		return nil, err
	}

	ev, err := scanCustodyEvent(tx.QueryRow(ctx, `
		INSERT INTO inventory.custody_events
		    (item_id, user_id, previous_holder_user_id, source, status, note, applied_at)
		VALUES ($1, $2, $3, 'revert', 'applied', $4, NOW())
		RETURNING `+custodyEventCols, itemID, target, holder, note))
	if err != nil {
		return nil, err
	}
	if err := setInventoryHolder(ctx, tx, itemID, target); err != nil {
		return nil, err
	}
	return ev, tx.Commit(ctx)
}

// RecordVerification confirms the current holder still has the item: stamps last_verified_at, clears the
// reminder and escalation stamps, and records a verify event. Returns ErrInventoryNoHolder if nobody holds it.
func RecordVerification(ctx context.Context, pool *pgxpool.Pool, itemID string, note *string) (*CustodyEvent, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	holder, archived, err := lockInventoryItem(ctx, tx, itemID)
	if err != nil {
		return nil, err
	}
	if archived {
		return nil, ErrInventoryItemArchived
	}
	if holder == nil {
		return nil, ErrInventoryNoHolder
	}

	ev, err := scanCustodyEvent(tx.QueryRow(ctx, `
		INSERT INTO inventory.custody_events (item_id, user_id, source, status, note, applied_at)
		VALUES ($1, $2, 'verify', 'applied', $3, NOW())
		RETURNING `+custodyEventCols, itemID, holder, note))
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE inventory.items
		SET last_verified_at = NOW(), last_reminded_at = NULL, last_escalated_at = NULL, updated_at = NOW()
		WHERE id = $1`, itemID); err != nil {
		return nil, err
	}
	return ev, tx.Commit(ctx)
}

// RecordInventoryDispute records that the holder says they no longer have the item (a disputed event
// from a verification reminder). The holder does not change. The item is also stamped as escalated, since
// the caller alerts inventory admins right away; that keeps the grace-period alert from firing a second
// time. Returns ErrInventoryNoHolder if nobody holds the item.
func RecordInventoryDispute(ctx context.Context, pool *pgxpool.Pool, itemID string, note *string) (*CustodyEvent, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	holder, archived, err := lockInventoryItem(ctx, tx, itemID)
	if err != nil {
		return nil, err
	}
	if archived {
		return nil, ErrInventoryItemArchived
	}
	if holder == nil {
		return nil, ErrInventoryNoHolder
	}

	ev, err := scanCustodyEvent(tx.QueryRow(ctx, `
		INSERT INTO inventory.custody_events (item_id, user_id, source, status, note)
		VALUES ($1, $2, 'slack', 'disputed', $3)
		RETURNING `+custodyEventCols, itemID, holder, note))
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE inventory.items SET last_escalated_at = NOW(), updated_at = NOW() WHERE id = $1`, itemID); err != nil {
		return nil, err
	}
	return ev, tx.Commit(ctx)
}

// ListInventoryCustodyEvents returns an item's history, newest first.
func ListInventoryCustodyEvents(ctx context.Context, pool *pgxpool.Pool, itemID string) ([]CustodyEvent, error) {
	rows, err := pool.Query(ctx, `
		SELECT `+custodyEventCols+` FROM inventory.custody_events
		WHERE item_id = $1 ORDER BY created_at DESC`, itemID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	events := make([]CustodyEvent, 0)
	for rows.Next() {
		ev, err := scanCustodyEvent(rows)
		if err != nil {
			return nil, err
		}
		events = append(events, *ev)
	}
	return events, rows.Err()
}

// ---- Verification reminders ----

// ListInventoryItemsDueForVerification returns held, active items with a cadence whose verification cycle
// has elapsed and that have not been reminded this cycle. A verification or custody change starts a new
// cycle by clearing last_reminded_at.
func ListInventoryItemsDueForVerification(ctx context.Context, pool *pgxpool.Pool) ([]InventoryItem, error) {
	rows, err := pool.Query(ctx, `
		SELECT `+inventoryItemCols+` FROM inventory.items
		WHERE archived_at IS NULL
		  AND current_holder_user_id IS NOT NULL
		  AND verify_every_days IS NOT NULL
		  AND last_reminded_at IS NULL
		  AND COALESCE(last_verified_at, created_at) + make_interval(days => verify_every_days) <= NOW()
		ORDER BY COALESCE(last_verified_at, created_at)`)
	if err != nil {
		return nil, err
	}
	return scanInventoryItems(rows)
}

// MarkInventoryItemReminded stamps last_reminded_at = NOW().
func MarkInventoryItemReminded(ctx context.Context, pool *pgxpool.Pool, id string) error {
	_, err := pool.Exec(ctx, `UPDATE inventory.items SET last_reminded_at = NOW() WHERE id = $1`, id)
	return err
}

// ListInventoryItemsPastGrace returns reminded items whose grace period has passed with no verification and
// whose admins have not yet been alerted. The grace is the item's verify_grace_days, else the
// inventory.default_grace_days config value, else 5.
func ListInventoryItemsPastGrace(ctx context.Context, pool *pgxpool.Pool) ([]InventoryItem, error) {
	rows, err := pool.Query(ctx, `
		SELECT `+inventoryItemCols+` FROM inventory.items
		WHERE archived_at IS NULL
		  AND current_holder_user_id IS NOT NULL
		  AND verify_every_days IS NOT NULL
		  AND last_reminded_at IS NOT NULL
		  AND last_escalated_at IS NULL
		  AND last_reminded_at + make_interval(days => COALESCE(
		        verify_grace_days,
		        (SELECT CASE WHEN value ~ '^[0-9]+$' THEN value::int END
		           FROM config_store WHERE service = 'inventory' AND key = 'default_grace_days'),
		        5)) <= NOW()
		ORDER BY last_reminded_at`)
	if err != nil {
		return nil, err
	}
	return scanInventoryItems(rows)
}

// MarkInventoryItemEscalated stamps last_escalated_at = NOW() so admins are alerted once per cycle.
func MarkInventoryItemEscalated(ctx context.Context, pool *pgxpool.Pool, id string) error {
	_, err := pool.Exec(ctx, `UPDATE inventory.items SET last_escalated_at = NOW() WHERE id = $1`, id)
	return err
}

// ---- Email fallback ----

// FindUserForClaimEmail matches the email typed on the public claim form to a member: users.email first
// (stored lowercase, looked up case-insensitively), then a single user_identities.external_email match.
// Returns ErrNotFound when nothing matches or the match is ambiguous.
func FindUserForClaimEmail(ctx context.Context, pool *pgxpool.Pool, email string) (*User, error) {
	email = strings.TrimSpace(email)
	if email == "" {
		return nil, ErrNotFound
	}
	u, err := GetUserByEmail(ctx, pool, email)
	if err == nil {
		return u, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	return GetUserByUniqueExternalEmail(ctx, pool, email)
}

// GetUserByUniqueExternalEmail finds a user through user_identities.external_email (case-insensitive).
// It matches only when exactly one distinct user has that address; zero or several return ErrNotFound.
func GetUserByUniqueExternalEmail(ctx context.Context, pool *pgxpool.Pool, email string) (*User, error) {
	email = strings.TrimSpace(email)
	if email == "" {
		return nil, ErrNotFound
	}
	rows, err := pool.Query(ctx, `
		SELECT DISTINCT u.id, u.display_name, u.email, u.bot, u.created_at, u.updated_at
		FROM user_identities ui
		JOIN users u ON u.id = ui.user_id
		WHERE lower(ui.external_email) = lower($1)
		LIMIT 2`, email)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var found []User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.DisplayName, &u.Email, &u.Bot, &u.CreatedAt, &u.UpdatedAt); err != nil {
			return nil, err
		}
		found = append(found, u)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(found) != 1 {
		return nil, ErrNotFound
	}
	return &found[0], nil
}
