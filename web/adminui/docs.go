package adminui

import (
	"net/http"

	admintempl "commons/web/templ"
)

func (d Deps) DocsPage() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		admintempl.DocsPage().Render(r.Context(), w)
	}
}
