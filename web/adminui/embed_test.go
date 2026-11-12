package adminui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"commons/internal/testhelpers"
	"commons/store"
)

func TestEmbedCalendar(t *testing.T) {
	ctx := t.Context()
	deps := Deps{Pool: testhelpers.SetupTestDB(t), EncKey: testhelpers.EncKey()}

	cal := &store.Calendar{Name: "Embed Cal", Slug: "embed-cal", Timezone: "America/Chicago"}
	require.NoError(t, store.CreateCalendar(ctx, deps.Pool, cal))
	evt := &store.CalendarEvent{
		CalendarID: cal.ID,
		Title:      "Embeddable Event",
		StartTime:  time.Now().Add(48 * time.Hour),
	}
	require.NoError(t, store.CreateCalendarEvent(ctx, deps.Pool, evt))

	h := deps.EmbedCalendar()

	t.Run("unknown slug is 404", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/embed/nope", nil)
		req.SetPathValue("slug", "nope")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		assert.Equal(t, http.StatusNotFound, w.Code)
	})

	t.Run("agenda view renders events and cache header", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/embed/embed-cal", nil)
		req.SetPathValue("slug", "embed-cal")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)

		require.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, "public, max-age=60", w.Header().Get("Cache-Control"))
		body := w.Body.String()
		assert.Contains(t, body, "Embeddable Event")
		assert.Contains(t, body, "Embed Cal")
		assert.Contains(t, strings.ToLower(body), "<!doctype html>", "standalone page")
		assert.NotContains(t, body, "/admin/fragments", "no admin chrome or HTMX targets")
	})

	t.Run("month view renders grid with paging links", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/embed/embed-cal?view=month", nil)
		req.SetPathValue("slug", "embed-cal")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)

		require.Equal(t, http.StatusOK, w.Code)
		body := w.Body.String()
		assert.Contains(t, body, "Embeddable Event")
		assert.Contains(t, body, "/embed/embed-cal?view=month", "paging links stay on the embed route")
	})

	t.Run("composite calendars embed with merged events", func(t *testing.T) {
		comp := &store.Calendar{Name: "Embed Comp", Slug: "embed-comp", Timezone: "UTC", Kind: store.CalendarKindComposite}
		require.NoError(t, store.CreateCalendar(ctx, deps.Pool, comp))
		require.NoError(t, store.AddCalendarMembers(ctx, deps.Pool, comp.ID, []string{cal.ID}))

		req := httptest.NewRequest(http.MethodGet, "/embed/embed-comp", nil)
		req.SetPathValue("slug", "embed-comp")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)

		require.Equal(t, http.StatusOK, w.Code)
		assert.Contains(t, w.Body.String(), "Embeddable Event", "member events surface in the composite embed")
	})
}
