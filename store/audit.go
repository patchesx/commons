package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type AuditEntry struct {
	ID        string
	ActorID   *string
	Action    string
	Target    *string
	Detail    json.RawMessage
	CreatedAt time.Time
}

// LogAction appends a record to the audit log.
// actorID may be nil for system-initiated actions.
// detail is marshalled to JSONB; pass nil to omit.
func LogAction(ctx context.Context, pool *pgxpool.Pool, actorID *string, action, target string, detail any) error {
	var detailJSON []byte
	if detail != nil {
		b, err := json.Marshal(detail)
		if err != nil {
			return fmt.Errorf("marshal audit detail: %w", err)
		}
		detailJSON = b
	}

	var targetPtr *string
	if target != "" {
		targetPtr = &target
	}

	_, err := pool.Exec(ctx, `
		INSERT INTO audit_log (actor_id, action, target, detail)
		VALUES ($1, $2, $3, $4)
	`, actorID, action, targetPtr, detailJSON)
	return err
}

// CountAuditLog returns the total number of audit entries.
func CountAuditLog(ctx context.Context, pool *pgxpool.Pool) (int, error) {
	var count int
	err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM audit_log`).Scan(&count)
	return count, err
}

// ListAuditLog returns audit entries newest first with pagination.
func ListAuditLog(ctx context.Context, pool *pgxpool.Pool, limit, offset int) ([]AuditEntry, error) {
	rows, err := pool.Query(ctx, `
		SELECT id, actor_id, action, target, detail, created_at
		FROM audit_log ORDER BY created_at DESC LIMIT $1 OFFSET $2
	`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []AuditEntry
	for rows.Next() {
		var e AuditEntry
		if err := rows.Scan(&e.ID, &e.ActorID, &e.Action, &e.Target, &e.Detail, &e.CreatedAt); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}
