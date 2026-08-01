package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// --- Structs ---

type LegislativeBody struct {
	ID                     string
	Name                   string
	Level                  string
	State                  string
	DataSource             string
	OpenStatesJurisdiction *string
	OpenStatesChamber      *string
	LegistarClient         *string
	LegistarBodyID         *int
	Active                 bool
	CreatedAt              time.Time
}

type Bill struct {
	ID               string
	BodyID           string
	BodyName         string // populated via JOIN
	ExternalID       *string
	Identifier       string
	Title            string
	Type             *string
	Chamber          *string
	Session          *string
	Status           *string
	LatestAction     *string
	LatestActionDate *time.Time
	IntroducedAt     *time.Time
	Link             *string
	ChapterPosition  *string
	Notes            *string
	Subjects         []string // stored from OpenStates API; empty for Legistar/manual bills
	AutoImported     bool
	Following        bool
	SyncedAt         *time.Time
	CreatedAt        time.Time
	UpdatedAt        time.Time
	Tags             []string // populated by ListBillTags or enriched query
}

type Tag struct {
	ID        string
	Name      string
	CreatedAt time.Time
}

type BillUpdate struct {
	ID        string
	BillID    string
	Field     string
	OldValue  *string
	NewValue  *string
	Source    string
	CreatedAt time.Time
}

type ImportFilter struct {
	ID         string
	BodyID     string
	FilterType string
	Value      string
	Active     bool
	CreatedAt  time.Time
}

// --- Legislative Bodies ---

// CreateLegislativeBody inserts a new legislative body. Populates b.ID and b.CreatedAt.
func CreateLegislativeBody(ctx context.Context, pool *pgxpool.Pool, b *LegislativeBody) error {
	return pool.QueryRow(ctx, `
		INSERT INTO legislative_bodies (
			name, level, state, data_source,
			openstates_jurisdiction, openstates_chamber,
			legistar_client, legistar_body_id, active
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING id, created_at
	`,
		b.Name, b.Level, b.State, b.DataSource,
		b.OpenStatesJurisdiction, b.OpenStatesChamber,
		b.LegistarClient, b.LegistarBodyID, b.Active,
	).Scan(&b.ID, &b.CreatedAt)
}

// UpdateLegislativeBody updates all configurable fields on an existing body.
func UpdateLegislativeBody(ctx context.Context, pool *pgxpool.Pool, b *LegislativeBody) error {
	_, err := pool.Exec(ctx, `
		UPDATE legislative_bodies
		SET name                    = $1,
		    level                   = $2,
		    state                   = $3,
		    data_source             = $4,
		    openstates_jurisdiction = $5,
		    openstates_chamber      = $6,
		    legistar_client         = $7,
		    legistar_body_id        = $8,
		    active                  = $9
		WHERE id = $10
	`,
		b.Name, b.Level, b.State, b.DataSource,
		b.OpenStatesJurisdiction, b.OpenStatesChamber,
		b.LegistarClient, b.LegistarBodyID, b.Active,
		b.ID,
	)
	return err
}

// ListLegislativeBodies returns all bodies ordered by state then name.
func ListLegislativeBodies(ctx context.Context, pool *pgxpool.Pool) ([]LegislativeBody, error) {
	rows, err := pool.Query(ctx, `
		SELECT id, name, level, state, data_source,
		       openstates_jurisdiction, openstates_chamber,
		       legistar_client, legistar_body_id,
		       active, created_at
		FROM legislative_bodies
		ORDER BY state, name
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanBodies(rows)
}

// GetLegislativeBody returns a single legislative body by ID. Returns ErrNotFound if absent.
func GetLegislativeBody(ctx context.Context, pool *pgxpool.Pool, id string) (*LegislativeBody, error) {
	rows, err := pool.Query(ctx, `
		SELECT id, name, level, state, data_source,
		       openstates_jurisdiction, openstates_chamber,
		       legistar_client, legistar_body_id,
		       active, created_at
		FROM legislative_bodies
		WHERE id = $1
	`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	bodies, err := scanBodies(rows)
	if err != nil {
		return nil, err
	}
	if len(bodies) == 0 {
		return nil, ErrNotFound
	}
	return &bodies[0], nil
}

// ListActiveBodiesBySource returns active bodies filtered by data_source.
// Used by the sync engine to load bodies for a specific provider.
func ListActiveBodiesBySource(ctx context.Context, pool *pgxpool.Pool, dataSource string) ([]LegislativeBody, error) {
	rows, err := pool.Query(ctx, `
		SELECT id, name, level, state, data_source,
		       openstates_jurisdiction, openstates_chamber,
		       legistar_client, legistar_body_id,
		       active, created_at
		FROM legislative_bodies
		WHERE data_source = $1 AND active = TRUE
		ORDER BY state, name
	`, dataSource)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanBodies(rows)
}

func scanBodies(rows pgx.Rows) ([]LegislativeBody, error) {
	var bodies []LegislativeBody
	for rows.Next() {
		var b LegislativeBody
		if err := rows.Scan(
			&b.ID, &b.Name, &b.Level, &b.State, &b.DataSource,
			&b.OpenStatesJurisdiction, &b.OpenStatesChamber,
			&b.LegistarClient, &b.LegistarBodyID,
			&b.Active, &b.CreatedAt,
		); err != nil {
			return nil, err
		}
		bodies = append(bodies, b)
	}
	return bodies, rows.Err()
}

// --- Bills ---

const billSelectSQL = `
	SELECT b.id, b.body_id, lb.name,
	       b.external_id, b.identifier, b.title, b.type, b.chamber, b.session,
	       b.status, b.latest_action, b.latest_action_date, b.introduced_at,
	       b.link, b.chapter_position, b.notes,
	       b.subjects, b.auto_imported, b.following, b.synced_at,
	       b.created_at, b.updated_at
	FROM bills b
	JOIN legislative_bodies lb ON lb.id = b.body_id`

func scanBill(row pgx.Row) (*Bill, error) {
	b := &Bill{}
	err := row.Scan(
		&b.ID, &b.BodyID, &b.BodyName,
		&b.ExternalID, &b.Identifier, &b.Title, &b.Type, &b.Chamber, &b.Session,
		&b.Status, &b.LatestAction, &b.LatestActionDate, &b.IntroducedAt,
		&b.Link, &b.ChapterPosition, &b.Notes,
		&b.Subjects, &b.AutoImported, &b.Following, &b.SyncedAt,
		&b.CreatedAt, &b.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return b, nil
}

func scanBills(rows pgx.Rows) ([]Bill, error) {
	var bills []Bill
	for rows.Next() {
		b := &Bill{}
		if err := rows.Scan(
			&b.ID, &b.BodyID, &b.BodyName,
			&b.ExternalID, &b.Identifier, &b.Title, &b.Type, &b.Chamber, &b.Session,
			&b.Status, &b.LatestAction, &b.LatestActionDate, &b.IntroducedAt,
			&b.Link, &b.ChapterPosition, &b.Notes,
			&b.Subjects, &b.AutoImported, &b.Following, &b.SyncedAt,
			&b.CreatedAt, &b.UpdatedAt,
		); err != nil {
			return nil, err
		}
		bills = append(bills, *b)
	}
	return bills, rows.Err()
}

// GetBill returns a single bill by internal ID. Returns ErrNotFound if absent.
func GetBill(ctx context.Context, pool *pgxpool.Pool, id string) (*Bill, error) {
	return scanBill(pool.QueryRow(ctx, billSelectSQL+` WHERE b.id = $1`, id))
}

// GetBillByExternalID returns a bill by body + external ID. Returns ErrNotFound if absent.
func GetBillByExternalID(ctx context.Context, pool *pgxpool.Pool, bodyID, externalID string) (*Bill, error) {
	return scanBill(pool.QueryRow(ctx, billSelectSQL+` WHERE b.body_id = $1 AND b.external_id = $2`, bodyID, externalID))
}

// ListBills returns all following=true bills ordered by body name then identifier.
func ListBills(ctx context.Context, pool *pgxpool.Pool) ([]Bill, error) {
	rows, err := pool.Query(ctx, billSelectSQL+`
		WHERE b.following = TRUE
		ORDER BY lb.name, b.identifier
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanBills(rows)
}

// ListBillsByBody returns following=true bills for a specific legislative body.
func ListBillsByBody(ctx context.Context, pool *pgxpool.Pool, bodyID string) ([]Bill, error) {
	rows, err := pool.Query(ctx, billSelectSQL+`
		WHERE b.body_id = $1 AND b.following = TRUE
		ORDER BY b.identifier
	`, bodyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanBills(rows)
}

// CreateBill inserts a new bill record. Populates b.ID, b.CreatedAt, b.UpdatedAt.
func CreateBill(ctx context.Context, pool *pgxpool.Pool, b *Bill) error {
	return pool.QueryRow(ctx, `
		INSERT INTO bills (
			body_id, external_id, identifier, title, type, chamber, session,
			status, latest_action, latest_action_date, introduced_at,
			link, chapter_position, notes, auto_imported, following
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7,
			$8, $9, $10, $11,
			$12, $13, $14, $15, $16
		)
		RETURNING id, created_at, updated_at
	`,
		b.BodyID, b.ExternalID, b.Identifier, b.Title, b.Type, b.Chamber, b.Session,
		b.Status, b.LatestAction, b.LatestActionDate, b.IntroducedAt,
		b.Link, b.ChapterPosition, b.Notes, b.AutoImported, b.Following,
	).Scan(&b.ID, &b.CreatedAt, &b.UpdatedAt)
}

// UpsertBill inserts or updates a bill by (body_id, external_id).
// If the existing bill has following=false, the row is NOT updated (dismissed bills stay dismissed).
// Returns whether the bill was newly inserted (true) or already existed (false).
func UpsertBill(ctx context.Context, pool *pgxpool.Pool, b *Bill) (inserted bool, err error) {
	if b.Subjects == nil {
		b.Subjects = []string{}
	}
	var id string
	err = pool.QueryRow(ctx, `
		INSERT INTO bills (
			body_id, external_id, identifier, title, type, chamber, session,
			status, latest_action, latest_action_date, introduced_at,
			link, subjects, auto_imported, synced_at
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7,
			$8, $9, $10, $11,
			$12, $13, $14, NOW()
		)
		ON CONFLICT (body_id, external_id) DO UPDATE
			SET identifier          = EXCLUDED.identifier,
			    title               = EXCLUDED.title,
			    type                = EXCLUDED.type,
			    chamber             = EXCLUDED.chamber,
			    session             = EXCLUDED.session,
			    status              = EXCLUDED.status,
			    latest_action       = EXCLUDED.latest_action,
			    latest_action_date  = EXCLUDED.latest_action_date,
			    introduced_at       = EXCLUDED.introduced_at,
			    link                = EXCLUDED.link,
			    subjects            = EXCLUDED.subjects,
			    synced_at           = NOW(),
			    updated_at          = NOW()
			WHERE bills.following = TRUE
		RETURNING id
	`,
		b.BodyID, b.ExternalID, b.Identifier, b.Title, b.Type, b.Chamber, b.Session,
		b.Status, b.LatestAction, b.LatestActionDate, b.IntroducedAt,
		b.Link, b.Subjects, b.AutoImported,
	).Scan(&id)
	if err != nil {
		return false, err
	}
	inserted = (b.ID == "")
	b.ID = id
	return inserted, nil
}

// UpdateBill updates the admin-managed fields on a bill (position, notes, link).
func UpdateBill(ctx context.Context, pool *pgxpool.Pool, b *Bill) error {
	_, err := pool.Exec(ctx, `
		UPDATE bills
		SET identifier       = $1,
		    title            = $2,
		    chapter_position = $3,
		    notes            = $4,
		    link             = $5,
		    updated_at       = NOW()
		WHERE id = $6
	`, b.Identifier, b.Title, b.ChapterPosition, b.Notes, b.Link, b.ID)
	return err
}

// SetBillFollowing sets following=true/false. Used to dismiss auto-imported bills
// or re-follow a previously dismissed one.
func SetBillFollowing(ctx context.Context, pool *pgxpool.Pool, id string, following bool) error {
	_, err := pool.Exec(ctx, `
		UPDATE bills SET following = $1, updated_at = NOW() WHERE id = $2
	`, following, id)
	return err
}

// --- Tags ---

// ListTags returns all tags ordered alphabetically.
func ListTags(ctx context.Context, pool *pgxpool.Pool) ([]Tag, error) {
	rows, err := pool.Query(ctx, `SELECT id, name, created_at FROM tags ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tags []Tag
	for rows.Next() {
		var t Tag
		if err := rows.Scan(&t.ID, &t.Name, &t.CreatedAt); err != nil {
			return nil, err
		}
		tags = append(tags, t)
	}
	return tags, rows.Err()
}

// CreateTag inserts a new tag. Returns an error if the name already exists.
func CreateTag(ctx context.Context, pool *pgxpool.Pool, name string) (*Tag, error) {
	t := &Tag{}
	err := pool.QueryRow(ctx,
		`INSERT INTO tags (name) VALUES ($1) RETURNING id, name, created_at`, name,
	).Scan(&t.ID, &t.Name, &t.CreatedAt)
	if err != nil {
		return nil, err
	}
	return t, nil
}

// DeleteTag removes a tag by ID.
func DeleteTag(ctx context.Context, pool *pgxpool.Pool, id string) error {
	_, err := pool.Exec(ctx, `DELETE FROM tags WHERE id = $1`, id)
	return err
}

// ListBillTags returns the tag names attached to a bill.
func ListBillTags(ctx context.Context, pool *pgxpool.Pool, billID string) ([]string, error) {
	rows, err := pool.Query(ctx, `
		SELECT t.name FROM tags t
		JOIN bill_tags bt ON bt.tag_id = t.id
		WHERE bt.bill_id = $1
		ORDER BY t.name
	`, billID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		names = append(names, n)
	}
	return names, rows.Err()
}

// SetBillTags replaces all tags on a bill with the provided tag IDs.
// Runs in a transaction: delete existing, insert new.
func SetBillTags(ctx context.Context, pool *pgxpool.Pool, billID string, tagIDs []string) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `DELETE FROM bill_tags WHERE bill_id = $1`, billID); err != nil {
		return err
	}
	for _, tagID := range tagIDs {
		if _, err := tx.Exec(ctx,
			`INSERT INTO bill_tags (bill_id, tag_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
			billID, tagID,
		); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// --- Subscriptions ---

// SubscribeUserToBill adds a subscription. No-op if already subscribed.
func SubscribeUserToBill(ctx context.Context, pool *pgxpool.Pool, userID, billID string) error {
	_, err := pool.Exec(ctx,
		`INSERT INTO bill_subscriptions (user_id, bill_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
		userID, billID,
	)
	return err
}

// UnsubscribeUserFromBill removes a subscription.
func UnsubscribeUserFromBill(ctx context.Context, pool *pgxpool.Pool, userID, billID string) error {
	_, err := pool.Exec(ctx,
		`DELETE FROM bill_subscriptions WHERE user_id = $1 AND bill_id = $2`,
		userID, billID,
	)
	return err
}

// IsUserSubscribed returns whether a user is subscribed to a specific bill.
func IsUserSubscribed(ctx context.Context, pool *pgxpool.Pool, userID, billID string) (bool, error) {
	var exists bool
	err := pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM bill_subscriptions WHERE user_id = $1 AND bill_id = $2)`,
		userID, billID,
	).Scan(&exists)
	return exists, err
}

// GetBillSubscribers returns all users subscribed to a bill. Used to send notifications.
func GetBillSubscribers(ctx context.Context, pool *pgxpool.Pool, billID string) ([]User, error) {
	rows, err := pool.Query(ctx, `
		SELECT u.id, u.display_name, u.email, u.bot, u.created_at, u.updated_at
		FROM users u
		JOIN bill_subscriptions bs ON bs.user_id = u.id
		WHERE bs.bill_id = $1
	`, billID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.DisplayName, &u.Email, &u.Bot, &u.CreatedAt, &u.UpdatedAt); err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

// GetUserSubscriptions returns all bills a user is subscribed to.
func GetUserSubscriptions(ctx context.Context, pool *pgxpool.Pool, userID string) ([]Bill, error) {
	rows, err := pool.Query(ctx, billSelectSQL+`
		JOIN bill_subscriptions bs ON bs.bill_id = b.id
		WHERE bs.user_id = $1 AND b.following = TRUE
		ORDER BY lb.name, b.identifier
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanBills(rows)
}

// --- Bill Updates (change log) ---

// RecordBillUpdate appends a change log entry. Populates u.ID and u.CreatedAt.
func RecordBillUpdate(ctx context.Context, pool *pgxpool.Pool, u *BillUpdate) error {
	return pool.QueryRow(ctx, `
		INSERT INTO bill_updates (bill_id, field, old_value, new_value, source)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, created_at
	`, u.BillID, u.Field, u.OldValue, u.NewValue, u.Source,
	).Scan(&u.ID, &u.CreatedAt)
}

// ListBillUpdates returns the change history for a bill, newest first.
func ListBillUpdates(ctx context.Context, pool *pgxpool.Pool, billID string) ([]BillUpdate, error) {
	rows, err := pool.Query(ctx, `
		SELECT id, bill_id, field, old_value, new_value, source, created_at
		FROM bill_updates
		WHERE bill_id = $1
		ORDER BY created_at DESC
	`, billID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var updates []BillUpdate
	for rows.Next() {
		var u BillUpdate
		if err := rows.Scan(&u.ID, &u.BillID, &u.Field, &u.OldValue, &u.NewValue, &u.Source, &u.CreatedAt); err != nil {
			return nil, err
		}
		updates = append(updates, u)
	}
	return updates, rows.Err()
}

// --- Import Filters ---

// ListImportFilters returns active import filters for a body.
func ListImportFilters(ctx context.Context, pool *pgxpool.Pool, bodyID string) ([]ImportFilter, error) {
	rows, err := pool.Query(ctx, `
		SELECT id, body_id, filter_type, value, active, created_at
		FROM bill_import_filters
		WHERE body_id = $1 AND active = TRUE
		ORDER BY filter_type, value
	`, bodyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var filters []ImportFilter
	for rows.Next() {
		var f ImportFilter
		if err := rows.Scan(&f.ID, &f.BodyID, &f.FilterType, &f.Value, &f.Active, &f.CreatedAt); err != nil {
			return nil, err
		}
		filters = append(filters, f)
	}
	return filters, rows.Err()
}

// CreateImportFilter adds a new auto-import filter for a body.
func CreateImportFilter(ctx context.Context, pool *pgxpool.Pool, f *ImportFilter) error {
	return pool.QueryRow(ctx, `
		INSERT INTO bill_import_filters (body_id, filter_type, value)
		VALUES ($1, $2, $3)
		RETURNING id, created_at
	`, f.BodyID, f.FilterType, f.Value,
	).Scan(&f.ID, &f.CreatedAt)
}

// DeleteImportFilter removes an import filter by ID.
func DeleteImportFilter(ctx context.Context, pool *pgxpool.Pool, id string) error {
	_, err := pool.Exec(ctx, `DELETE FROM bill_import_filters WHERE id = $1`, id)
	return err
}

// ListBodySubjects returns a sorted, deduplicated list of all subjects stored
// on bills for the given legislative body. Used to populate the filter datalist.
func ListBodySubjects(ctx context.Context, pool *pgxpool.Pool, bodyID string) ([]string, error) {
	rows, err := pool.Query(ctx, `
		SELECT DISTINCT unnest(subjects) AS subject
		FROM bills
		WHERE body_id = $1 AND subjects <> '{}'
		ORDER BY subject
	`, bodyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var subjects []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		subjects = append(subjects, s)
	}
	return subjects, rows.Err()
}

// CountTrackedBills returns the number of bills currently being followed.
func CountTrackedBills(ctx context.Context, pool *pgxpool.Pool) (int, error) {
	var count int
	err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM bills WHERE following = TRUE`).Scan(&count)
	return count, err
}

// CountRecentUpdates returns the number of bill update entries recorded in the last N days.
func CountRecentUpdates(ctx context.Context, pool *pgxpool.Pool, sinceDays int) (int, error) {
	var count int
	err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM bill_updates
		WHERE created_at >= NOW() - $1 * INTERVAL '1 day'
	`, sinceDays).Scan(&count)
	return count, err
}

// HasTrackedBills returns true if at least one bill with following = TRUE exists.
func HasTrackedBills(ctx context.Context, pool *pgxpool.Pool) (bool, error) {
	var exists bool
	err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM bills WHERE following = TRUE LIMIT 1)`).Scan(&exists)
	return exists, err
}
