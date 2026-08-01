package adminui

import (
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"commons/platform"
	"commons/plugin"
	admintempl "commons/web/templ"
)

type Deps struct {
	Pool          *pgxpool.Pool
	EncKey        []byte
	SessionSecret string
	Notifier      platform.Notifier
	Pctx          plugin.PluginContext
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
