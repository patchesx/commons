package adminui

import (
	"errors"
	"net/http"

	"commons/store"
	admintempl "commons/web/templ"
)

func (d Deps) MeetingsPage() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		zoomInteg, err := store.GetIntegrationByType(ctx, d.Pool, "zoom")
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			http.Error(w, "failed to load integration", http.StatusInternalServerError)
			return
		}
		var meetings []store.MeetingSummary
		if zoomInteg != nil {
			meetings, err = store.ListUpcomingMeetingSummaries(ctx, d.Pool, zoomInteg.ID)
			if err != nil {
				http.Error(w, "failed to load meetings", http.StatusInternalServerError)
				return
			}
		}
		admintempl.MeetingsPage(meetings).Render(ctx, w)
	}
}

func (d Deps) MeetingStartURL() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		id := r.PathValue("id")
		m, err := store.GetScheduledMeetingByID(ctx, d.Pool, d.EncKey, id)
		if err != nil || m == nil {
			w.Header().Set("Content-Type", "text/html")
			admintempl.StartURLErrorFragment(id).Render(ctx, w)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		admintempl.StartURLFragment(id, m.StartURL).Render(ctx, w)
	}
}
