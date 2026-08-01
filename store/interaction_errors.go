package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type InteractionError struct {
	ID           string
	CreatedAt    time.Time
	Source       string
	RequestType  string
	SlackUserID  string
	ErrorMessage string
}

func CreateInteractionError(ctx context.Context, pool *pgxpool.Pool, e *InteractionError) error {
	return pool.QueryRow(ctx, `
		INSERT INTO interaction_errors (source, request_type, slack_user_id, error_message)
		VALUES ($1, $2, $3, $4)
		RETURNING id, created_at`,
		e.Source, e.RequestType, e.SlackUserID, e.ErrorMessage,
	).Scan(&e.ID, &e.CreatedAt)
}
