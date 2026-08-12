package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ScheduledTrigger is the joined view of a trigger_sources row (type = 'scheduled')
// and its scheduled_trigger_config row.
type ScheduledTrigger struct {
	ID          string
	Name        string
	Enabled     bool
	ManagedBy   *string
	Schedule    string
	Timezone    string
	LastFiredAt *time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

const scheduledTriggerSelectCols = `
	ts.id, ts.name, ts.enabled, ts.managed_by,
	stc.schedule, stc.timezone, stc.last_fired_at,
	ts.created_at, ts.updated_at`

const scheduledTriggerFromJoin = `
	FROM trigger_sources ts
	JOIN scheduled_trigger_config stc ON stc.trigger_id = ts.id
	WHERE ts.type = 'scheduled'`

func scanScheduledTrigger(row interface {
	Scan(...any) error
}) (ScheduledTrigger, error) {
	var st ScheduledTrigger
	if err := row.Scan(
		&st.ID, &st.Name, &st.Enabled, &st.ManagedBy,
		&st.Schedule, &st.Timezone, &st.LastFiredAt,
		&st.CreatedAt, &st.UpdatedAt,
	); err != nil {
		return ScheduledTrigger{}, err
	}
	return st, nil
}

// ListScheduledTriggers returns all scheduled triggers, ordered by name.
func ListScheduledTriggers(ctx context.Context, pool *pgxpool.Pool) ([]ScheduledTrigger, error) {
	rows, err := pool.Query(ctx,
		`SELECT`+scheduledTriggerSelectCols+scheduledTriggerFromJoin+` ORDER BY ts.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ScheduledTrigger
	for rows.Next() {
		st, err := scanScheduledTrigger(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

// ListEnabledScheduledTriggers returns all enabled scheduled triggers.
// Used by the schedule runner to find candidates for firing.
func ListEnabledScheduledTriggers(ctx context.Context, pool *pgxpool.Pool) ([]ScheduledTrigger, error) {
	rows, err := pool.Query(ctx,
		`SELECT`+scheduledTriggerSelectCols+scheduledTriggerFromJoin+` AND ts.enabled = TRUE ORDER BY ts.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ScheduledTrigger
	for rows.Next() {
		st, err := scanScheduledTrigger(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

// GetScheduledTriggerByID returns the scheduled trigger with the given trigger ID.
func GetScheduledTriggerByID(ctx context.Context, pool *pgxpool.Pool, id string) (*ScheduledTrigger, error) {
	st, err := scanScheduledTrigger(pool.QueryRow(ctx,
		`SELECT`+scheduledTriggerSelectCols+scheduledTriggerFromJoin+` AND ts.id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &st, nil
}

// CreateScheduledTriggerParams holds the parameters for creating a scheduled trigger.
type CreateScheduledTriggerParams struct {
	Name     string
	Schedule string
	Timezone string
	Enabled  bool
}

// CreateScheduledTrigger creates a new scheduled trigger (trigger_sources + scheduled_trigger_config).
func CreateScheduledTrigger(ctx context.Context, pool *pgxpool.Pool, p CreateScheduledTriggerParams) (*ScheduledTrigger, error) {
	if p.Timezone == "" {
		p.Timezone = "UTC"
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	var id string
	if err := tx.QueryRow(ctx, `
		INSERT INTO trigger_sources (type, name, enabled)
		VALUES ('scheduled', $1, $2)
		RETURNING id`,
		p.Name, p.Enabled).Scan(&id); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO scheduled_trigger_config (trigger_id, schedule, timezone)
		VALUES ($1, $2, $3)`,
		id, p.Schedule, p.Timezone); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return GetScheduledTriggerByID(ctx, pool, id)
}

// UpdateScheduledTriggerParams holds the parameters for updating a scheduled trigger.
type UpdateScheduledTriggerParams struct {
	Name     string
	Schedule string
	Timezone string
	Enabled  bool
}

// UpdateScheduledTrigger updates a scheduled trigger's name, schedule, timezone, and enabled flag.
func UpdateScheduledTrigger(ctx context.Context, pool *pgxpool.Pool, id string, p UpdateScheduledTriggerParams) (*ScheduledTrigger, error) {
	if p.Timezone == "" {
		p.Timezone = "UTC"
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx,
		`UPDATE trigger_sources SET name=$2, enabled=$3, updated_at=NOW() WHERE id=$1`,
		id, p.Name, p.Enabled); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE scheduled_trigger_config SET schedule=$2, timezone=$3, updated_at=NOW() WHERE trigger_id=$1`,
		id, p.Schedule, p.Timezone); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return GetScheduledTriggerByID(ctx, pool, id)
}

// DeleteScheduledTrigger deletes a scheduled trigger. Returns an error if the trigger
// is managed by a plugin (managed_by is set).
func DeleteScheduledTrigger(ctx context.Context, pool *pgxpool.Pool, id string) error {
	var managedBy *string
	if err := pool.QueryRow(ctx,
		`SELECT managed_by FROM trigger_sources WHERE id = $1`, id).Scan(&managedBy); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if managedBy != nil && *managedBy != "" {
		return errors.New("cannot delete plugin-managed scheduled trigger")
	}
	_, err := pool.Exec(ctx, `DELETE FROM trigger_sources WHERE id = $1`, id)
	return err
}

// MarkScheduleFired updates last_fired_at to the given time.
func MarkScheduleFired(ctx context.Context, pool *pgxpool.Pool, id string, firedAt time.Time) error {
	_, err := pool.Exec(ctx,
		`UPDATE scheduled_trigger_config SET last_fired_at = $2, updated_at = NOW() WHERE trigger_id = $1`,
		id, firedAt)
	return err
}

// UpsertManagedScheduledTriggerParams holds parameters for creating or updating a
// plugin-managed scheduled trigger.
type UpsertManagedScheduledTriggerParams struct {
	Name      string
	Schedule  string
	Timezone  string
	ManagedBy string
	Enabled   bool
}

// UpsertManagedScheduledTrigger creates or updates a plugin-managed scheduled trigger.
// On conflict (a managed trigger with the same ManagedBy already exists), updates only
// the schedule and timezone — leaving name, enabled, and admin-customized actions unchanged.
// Returns an error if a scheduled trigger with the same ManagedBy exists but was created
// by a different owner.
func UpsertManagedScheduledTrigger(ctx context.Context, pool *pgxpool.Pool, p UpsertManagedScheduledTriggerParams) (*ScheduledTrigger, error) {
	if p.Timezone == "" {
		p.Timezone = "UTC"
	}

	// Check for existing managed trigger with the same ManagedBy.
	var existingID string
	var existingManagedBy *string
	err := pool.QueryRow(ctx, `
		SELECT ts.id, ts.managed_by
		FROM trigger_sources ts
		JOIN scheduled_trigger_config stc ON stc.trigger_id = ts.id
		WHERE ts.type = 'scheduled' AND ts.managed_by = $1`,
		p.ManagedBy).Scan(&existingID, &existingManagedBy)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if err == nil {
		// Existing trigger — update schedule and timezone only.
		if _, err := pool.Exec(ctx,
			`UPDATE scheduled_trigger_config SET schedule=$2, timezone=$3, updated_at=NOW() WHERE trigger_id=$1`,
			existingID, p.Schedule, p.Timezone); err != nil {
			return nil, err
		}
		return GetScheduledTriggerByID(ctx, pool, existingID)
	}

	// No existing trigger — insert new.
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	var id string
	if err := tx.QueryRow(ctx, `
		INSERT INTO trigger_sources (type, name, enabled, managed_by)
		VALUES ('scheduled', $1, $2, $3)
		RETURNING id`,
		p.Name, p.Enabled, p.ManagedBy).Scan(&id); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO scheduled_trigger_config (trigger_id, schedule, timezone)
		VALUES ($1, $2, $3)`,
		id, p.Schedule, p.Timezone); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return GetScheduledTriggerByID(ctx, pool, id)
}
