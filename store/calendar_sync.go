package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// FeedOccurrence is one materialized feed occurrence to reconcile into a
// syndicated calendar's stored events. UID+StartTime address the occurrence.
type FeedOccurrence struct {
	UID         string
	Title       string
	Description *string
	Location    *string
	URL         *string
	StartTime   time.Time
	EndTime     *time.Time
	AllDay      bool
}

// SyncResult summarizes one calendar's reconciliation.
type SyncResult struct {
	Inserted int
	Updated  int
	Deleted  int
}

// ListFeedCalendars returns all calendars with a live iCal feed URL.
func ListFeedCalendars(ctx context.Context, pool *pgxpool.Pool) ([]Calendar, error) {
	rows, err := pool.Query(ctx, `
		SELECT id, name, COALESCE(slug, ''), description, ical_url, timezone, display_order, kind, color, created_by, created_at, updated_at
		FROM calendars
		WHERE ical_url IS NOT NULL AND ical_url <> ''
		ORDER BY display_order, name
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanCalendars(rows)
}

// GetCalendarLastSynced returns the calendar's last successful feed sync time.
// ok is false when it has never synced (or the column is NULL).
func GetCalendarLastSynced(ctx context.Context, pool *pgxpool.Pool, calendarID string) (time.Time, bool) {
	var ts *time.Time
	err := pool.QueryRow(ctx, `SELECT last_synced_at FROM calendars WHERE id = $1`, calendarID).Scan(&ts)
	if err != nil || ts == nil {
		return time.Time{}, false
	}
	return *ts, true
}

// MarkCalendarSynced stamps the calendar's last_synced_at to now.
func MarkCalendarSynced(ctx context.Context, pool *pgxpool.Pool, calendarID string) error {
	_, err := pool.Exec(ctx, `UPDATE calendars SET last_synced_at = NOW() WHERE id = $1`, calendarID)
	return err
}

// storedFeedRow mirrors a stored calendar_events row during reconciliation.
type storedFeedRow struct {
	id          string
	uid         string
	title       string
	description *string
	location    *string
	url         *string
	startTime   time.Time
	endTime     *time.Time
	allDay      bool
}

// ReconcileFeedEvents brings a feed calendar's stored events in line with the
// fetched occurrences, keyed on (uid, start_time):
//
//   - new occurrences are inserted
//   - stored occurrences that vanished upstream are deleted
//   - stored occurrences whose fields changed are updated
//
// Rows without a uid (legacy imports predating uid-aware sync) are stale on a
// feed calendar — the calendar is read-only, so every row came from a feed —
// and are deleted and re-inserted with uids. Occurrences without a uid from
// the feed are insert-only (never matched, never deleted).
//
// The whole reconciliation is atomic: callers can retry freely on failure.
// It only runs on feed calendars (ical_url set) — reconcile deletes stored rows
// missing from the feed, which would destroy manual events otherwise.
func ReconcileFeedEvents(ctx context.Context, pool *pgxpool.Pool, calendarID string, occurrences []FeedOccurrence) (SyncResult, error) {
	var feedURL string
	err := pool.QueryRow(ctx, `SELECT COALESCE(ical_url, '') FROM calendars WHERE id = $1`, calendarID).Scan(&feedURL)
	if err != nil {
		return SyncResult{}, err
	}
	if feedURL == "" {
		return SyncResult{}, ErrNotFeedCalendar
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return SyncResult{}, err
	}
	defer tx.Rollback(ctx)

	rows, err := tx.Query(ctx, `
		SELECT id, COALESCE(uid, ''), title, description, location, url, start_time, end_time, all_day
		FROM calendar_events
		WHERE calendar_id = $1
	`, calendarID)
	if err != nil {
		return SyncResult{}, err
	}
	var stale []storedFeedRow           // uid-less rows, resolved by natural-key matching
	byKey := map[string]storedFeedRow{} // uid|start → row
	for rows.Next() {
		var sr storedFeedRow
		if err := rows.Scan(&sr.id, &sr.uid, &sr.title, &sr.description, &sr.location, &sr.url, &sr.startTime, &sr.endTime, &sr.allDay); err != nil {
			rows.Close()
			return SyncResult{}, err
		}
		if sr.uid == "" {
			stale = append(stale, sr)
			continue
		}
		byKey[feedKey(sr.uid, sr.startTime)] = sr
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return SyncResult{}, err
	}

	var res SyncResult
	// Index incoming occurrences two ways: by (uid, start) for uid-bearing rows,
	// and by the natural key (title, start) for NULL-uid rows — which have no
	// uid identity and live or die by natural-key presence in the feed.
	// Starts are normalized to UTC strings: pgx returns stored times in the
	// session timezone, and raw time.Time values would never compare equal.
	type naturalKey struct {
		title string
		start string
	}
	normKey := func(title string, start time.Time) naturalKey {
		return naturalKey{title, start.UTC().Truncate(time.Second).Format(time.RFC3339)}
	}
	// Two occurrences with the same title and start can't both be stored (calendar_events is unique on
	// (calendar_id, title, start_time)), so the first wins and the rest are dropped here. Left in, a
	// uid-bearing duplicate would count as stored and then collide when its row is renamed.
	occurrences = dedupeFeedOccurrences(occurrences)
	fetchedKeys := make(map[string]bool, len(occurrences))
	incomingNatural := make(map[naturalKey]string, len(occurrences))
	for _, occ := range occurrences {
		if occ.UID != "" {
			fetchedKeys[feedKey(occ.UID, occ.StartTime)] = true
		}
		incomingNatural[normKey(occ.Title, occ.StartTime)] = occ.UID
	}

	// Deletions: uid-bearing rows vanished from the feed, and NULL-uid rows that
	// either vanished entirely or are being upgraded by a uid-bearing occurrence
	// of the same event (the insert loop then recreates the row with its uid).
	for key, sr := range byKey {
		if !fetchedKeys[key] {
			if _, err := tx.Exec(ctx, `DELETE FROM calendar_events WHERE id = $1`, sr.id); err != nil {
				return SyncResult{}, err
			}
			res.Deleted++
		}
	}
	for _, sr := range stale {
		uid, matched := incomingNatural[normKey(sr.title, sr.startTime)]
		if matched && uid == "" {
			// Same event, still uid-less upstream: keep; the insert dedupes
			// via the (calendar_id, title, start_time) unique constraint.
			continue
		}
		if _, err := tx.Exec(ctx, `DELETE FROM calendar_events WHERE id = $1`, sr.id); err != nil {
			return SyncResult{}, err
		}
		res.Deleted++
	}

	// Work out what to insert and what to update, then apply updates before inserts: an update can vacate a
	// (title, start) that a new occurrence is about to take.
	type pendingUpdate struct {
		row storedFeedRow
		occ FeedOccurrence
	}
	var toInsert []FeedOccurrence
	var toUpdate []pendingUpdate
	for _, occ := range occurrences {
		if occ.UID == "" {
			// Insert-only; the (calendar_id, title, start_time) unique net dedupes.
			toInsert = append(toInsert, occ)
			continue
		}
		sr, exists := byKey[feedKey(occ.UID, occ.StartTime)]
		if !exists {
			// Not stored under this uid — but a legacy row with the same
			// (title, start) may still exist; it was in `stale` and is gone now.
			toInsert = append(toInsert, occ)
			continue
		}
		if feedRowChanged(sr, occ) {
			toUpdate = append(toUpdate, pendingUpdate{sr, occ})
		}
	}

	// Rows being renamed are parked under a title nothing else can have before any is rewritten, so renames
	// that swap or chain titles (A takes B's title while B takes another) don't hit the unique constraint
	// part-way through. Every parked row is rewritten just below, in the same transaction.
	var renamed []string
	for _, u := range toUpdate {
		if u.row.title != u.occ.Title {
			renamed = append(renamed, u.row.id)
		}
	}
	if len(renamed) > 0 {
		if _, err := tx.Exec(ctx,
			`UPDATE calendar_events SET title = 'sync-parked:' || id::text WHERE id = ANY($1::uuid[])`, renamed); err != nil {
			return SyncResult{}, err
		}
	}
	for _, u := range toUpdate {
		if _, err := tx.Exec(ctx, `
			UPDATE calendar_events
			SET title = $1, description = $2, location = $3, url = $4,
			    end_time = $5, all_day = $6, uid = $7, updated_at = NOW()
			WHERE id = $8
		`, u.occ.Title, u.occ.Description, u.occ.Location, u.occ.URL, u.occ.EndTime, u.occ.AllDay, u.occ.UID, u.row.id); err != nil {
			return SyncResult{}, err
		}
		res.Updated++
	}
	for _, occ := range toInsert {
		inserted, err := execFeedInsert(ctx, tx, calendarID, occ)
		if err != nil {
			return SyncResult{}, err
		}
		res.Inserted += inserted
	}

	if err := tx.Commit(ctx); err != nil {
		return SyncResult{}, err
	}
	return res, nil
}

// ImportFeedOccurrences inserts occurrences into a calendar without reconciling
// — existing rows are never deleted or updated. Used for one-time imports into
// arbitrary calendars (POST /api/calendars/{id}/import). Returns the number of
// rows actually inserted (duplicates on (title, start_time) are skipped).
func ImportFeedOccurrences(ctx context.Context, pool *pgxpool.Pool, calendarID string, occurrences []FeedOccurrence) (int, error) {
	inserted := 0
	for _, occ := range occurrences {
		n, err := execFeedInsert(ctx, pool, calendarID, occ)
		if err != nil {
			return inserted, err
		}
		inserted += n
	}
	return inserted, nil
}

// execer abstracts pgx.Tx and pgxpool.Pool for shared insert helpers.
type execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

func feedKey(uid string, start time.Time) string {
	return uid + "|" + start.UTC().Truncate(time.Second).Format(time.RFC3339)
}

func execFeedInsert(ctx context.Context, tx execer, calendarID string, occ FeedOccurrence) (int, error) {
	tag, err := tx.Exec(ctx, `
		INSERT INTO calendar_events (calendar_id, title, description, location, url, start_time, end_time, all_day, uid)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, NULLIF($9, ''))
		ON CONFLICT (calendar_id, title, start_time) DO NOTHING
	`, calendarID, occ.Title, occ.Description, occ.Location, occ.URL, occ.StartTime, occ.EndTime, occ.AllDay, occ.UID)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

// feedRowChanged compares a stored row with a fetched occurrence (start times
// match by construction — they're the key).
func feedRowChanged(sr storedFeedRow, occ FeedOccurrence) bool {
	return sr.title != occ.Title ||
		!strPtrEqual(sr.description, occ.Description) ||
		!strPtrEqual(sr.location, occ.Location) ||
		!strPtrEqual(sr.url, occ.URL) ||
		!timePtrEqual(sr.endTime, occ.EndTime) ||
		sr.allDay != occ.AllDay
}

func strPtrEqual(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func timePtrEqual(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.UTC().Equal(b.UTC())
}

// dedupeFeedOccurrences drops occurrences that repeat an earlier one's title and start time, keeping the
// first. The order of the rest is unchanged.
func dedupeFeedOccurrences(in []FeedOccurrence) []FeedOccurrence {
	type key struct {
		title string
		start int64
	}
	seen := make(map[key]bool, len(in))
	out := make([]FeedOccurrence, 0, len(in))
	for _, occ := range in {
		k := key{occ.Title, occ.StartTime.UTC().UnixMicro()}
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, occ)
	}
	return out
}
