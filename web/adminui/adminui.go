package adminui

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"commons/platform"
	"commons/plugin"
	"commons/web"
	admintempl "commons/web/templ"
)

type Deps struct {
	Pool          *pgxpool.Pool
	EncKey        []byte
	SessionSecret string
	Notifier      platform.Notifier
	Pctx          plugin.PluginContext

	// Imports makes an inventory import preview confirmable only once and keeps its result page reloadable.
	// Nil skips both, and the result is shown straight from the confirm request.
	Imports *ImportState
	// ClaimLimiter rate-limits the public inventory claim form. Nil disables limiting.
	ClaimLimiter *web.RateLimiter
	// NotifyUnmatchedClaim tells inventory admins about a claim whose email matched no member. Nil skips it.
	NotifyUnmatchedClaim func(ctx context.Context, eventID string)
	// NotifyClaimApplied tells a member that a web-form check-in with their email gave them an item. Nil skips it.
	NotifyClaimApplied func(ctx context.Context, itemID, userID string)
	// NotifyClaimLinked tells a member that an admin linked a web-form claim to them. Nil skips it.
	NotifyClaimLinked func(ctx context.Context, itemID, userID string)
}

func FragmentError(w http.ResponseWriter, r *http.Request, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	admintempl.FragmentError(msg).Render(r.Context(), w)
}

// inlineHelp renders a one-line Bulma help message into an HTMX form-feedback
// target. kind is a Bulma color modifier: "success", "warning", or "danger".
func inlineHelp(w http.ResponseWriter, r *http.Request, kind, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	admintempl.InlineHelp(kind, msg).Render(r.Context(), w)
}
