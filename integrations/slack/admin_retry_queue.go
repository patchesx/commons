package slack

import (
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	admintempl "commons/web/templ"
)

const retryQueuePageLimit = 100

// HandleRetryQueuePage renders the admin page listing all queued Slack messages.
func HandleRetryQueuePage(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		msgs, err := ListAllRetryMessages(ctx, pool, retryQueuePageLimit)
		if err != nil {
			http.Error(w, "failed to load retry queue", http.StatusInternalServerError)
			return
		}
		admintempl.SlackRetryQueuePage(toRetryQueueViews(msgs)).Render(ctx, w)
	}
}

// HandleRetryQueueRetry resets a message for immediate retry and returns the
// refreshed table fragment.
func HandleRetryQueueRetry(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		if err := ResetRetryMessage(ctx, pool, r.PathValue("id")); err != nil {
			http.Error(w, "failed to reset message", http.StatusInternalServerError)
			return
		}
		msgs, err := ListAllRetryMessages(ctx, pool, retryQueuePageLimit)
		if err != nil {
			http.Error(w, "failed to load retry queue", http.StatusInternalServerError)
			return
		}
		admintempl.SlackRetryQueueTable(toRetryQueueViews(msgs)).Render(ctx, w)
	}
}

// HandleRetryQueueDelete deletes a message and returns the refreshed table fragment.
func HandleRetryQueueDelete(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		if err := DeleteRetryMessage(ctx, pool, r.PathValue("id")); err != nil {
			http.Error(w, "failed to delete message", http.StatusInternalServerError)
			return
		}
		msgs, err := ListAllRetryMessages(ctx, pool, retryQueuePageLimit)
		if err != nil {
			http.Error(w, "failed to load retry queue", http.StatusInternalServerError)
			return
		}
		admintempl.SlackRetryQueueTable(toRetryQueueViews(msgs)).Render(ctx, w)
	}
}

func toRetryQueueViews(msgs []RetryMessage) []admintempl.RetryQueueView {
	views := make([]admintempl.RetryQueueView, len(msgs))
	for i, m := range msgs {
		views[i] = admintempl.RetryQueueView{
			ID:            m.ID,
			Destination:   m.Destination,
			IsDM:          m.IsDM,
			Text:          m.Text,
			HasBlocks:     len(m.Blocks) > 0,
			AttemptCount:  m.AttemptCount,
			MaxAttempts:   m.MaxAttempts,
			NextAttemptAt: m.NextAttemptAt,
			LastError:     m.LastError,
			CreatedAt:     m.CreatedAt,
			Exhausted:     m.AttemptCount >= m.MaxAttempts,
		}
	}
	return views
}
