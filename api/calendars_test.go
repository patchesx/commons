package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"commons/internal/testhelpers"
	"commons/store"
)

func seedCalendarForAPI(t *testing.T, pool *pgxpool.Pool, name, slug string) *store.Calendar {
	t.Helper()
	cal := &store.Calendar{Name: name, Slug: slug, Timezone: "UTC"}
	require.NoError(t, store.CreateCalendar(t.Context(), pool, cal))
	return cal
}

func createCompositeForAPI(t *testing.T, pool *pgxpool.Pool, name, slug string, memberIDs []string) *store.Calendar {
	t.Helper()
	var sb strings.Builder
	sb.WriteString(`{"name":"` + name + `","slug":"` + slug + `","timezone":"UTC","kind":"composite","member_ids":[`)
	for i, id := range memberIDs {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString(`"` + id + `"`)
	}
	sb.WriteString(`]}`)

	req := httptest.NewRequest(http.MethodPost, "/api/calendars", strings.NewReader(sb.String()))
	w := httptest.NewRecorder()
	CreateCalendar(pool, testhelpers.EncKey()).ServeHTTP(w, req)
	require.Equal(t, http.StatusCreated, w.Code, "composite create should succeed: %s", w.Body.String())
	var created store.Calendar
	require.NoError(t, json.NewDecoder(w.Body).Decode(&created))
	return &created
}

func TestCreateCompositeCalendar(t *testing.T) {
	pool := testhelpers.SetupTestDB(t)
	a := seedCalendarForAPI(t, pool, "Member A", "api-member-a")
	b := seedCalendarForAPI(t, pool, "Member B", "api-member-b")

	created := createCompositeForAPI(t, pool, "All", "api-all", []string{a.ID, b.ID})
	assert.Equal(t, store.CalendarKindComposite, created.Kind)

	members, err := store.ListCalendarMembers(t.Context(), pool, created.ID)
	require.NoError(t, err)
	assert.Len(t, members, 2)
}

func TestCreateCalendarValidation(t *testing.T) {
	pool := testhelpers.SetupTestDB(t)
	a := seedCalendarForAPI(t, pool, "Solo", "api-solo")
	h := CreateCalendar(pool, testhelpers.EncKey())

	cases := []struct {
		name string
		body string
	}{
		{"composite without members", `{"name":"C","slug":"api-c1","kind":"composite"}`},
		{"composite with feed url", `{"name":"C","slug":"api-c2","kind":"composite","member_ids":["` + a.ID + `"],"IcalURL":"https://example.com/x.ics"}`},
		{"personal kind rejected", `{"name":"P","slug":"api-p","kind":"personal"}`},
		{"unknown kind rejected", `{"name":"X","slug":"api-x","kind":"weird"}`},
		{"member_ids on standard", `{"name":"S","slug":"api-s","member_ids":["` + a.ID + `"]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/calendars", strings.NewReader(tc.body))
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			assert.Equal(t, http.StatusBadRequest, w.Code)
		})
	}
}

func TestUpdateCalendarKindImmutable(t *testing.T) {
	pool := testhelpers.SetupTestDB(t)
	a := seedCalendarForAPI(t, pool, "Std", "api-std")
	comp := createCompositeForAPI(t, pool, "Comp", "api-comp", []string{a.ID})

	// Kind change must be rejected.
	req := httptest.NewRequest(http.MethodPut, "/api/calendars/"+comp.ID,
		strings.NewReader(`{"name":"Comp","slug":"api-comp","kind":"standard"}`))
	req.SetPathValue("id", comp.ID)
	w := httptest.NewRecorder()
	UpdateCalendar(pool).ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code, "kind change must be rejected")

	// Same-kind update with full membership succeeds (PUT replaces membership).
	req = httptest.NewRequest(http.MethodPut, "/api/calendars/"+comp.ID,
		strings.NewReader(`{"name":"Comp","slug":"api-comp","member_ids":["`+a.ID+`"]}`))
	req.SetPathValue("id", comp.ID)
	w = httptest.NewRecorder()
	UpdateCalendar(pool).ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	// A composite update without member_ids is rejected: PUT replaces the full
	// membership, and composites cannot have zero members.
	req = httptest.NewRequest(http.MethodPut, "/api/calendars/"+comp.ID,
		strings.NewReader(`{"name":"Comp","slug":"api-comp"}`))
	req.SetPathValue("id", comp.ID)
	w = httptest.NewRecorder()
	UpdateCalendar(pool).ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestCreateCompositeCycleRejected(t *testing.T) {
	pool := testhelpers.SetupTestDB(t)
	std := seedCalendarForAPI(t, pool, "Std", "api-cycle-std")
	inner := createCompositeForAPI(t, pool, "Inner", "api-cycle-inner", []string{std.ID})
	outer := createCompositeForAPI(t, pool, "Outer", "api-cycle-outer", []string{inner.ID})

	// outer contains inner; adding outer to inner would close the cycle.
	req := httptest.NewRequest(http.MethodPut, "/api/calendars/"+inner.ID,
		strings.NewReader(`{"name":"Inner","slug":"api-cycle-inner","member_ids":["`+std.ID+`","`+outer.ID+`"]}`))
	req.SetPathValue("id", inner.ID)
	w := httptest.NewRecorder()
	UpdateCalendar(pool).ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code, "cycle must be rejected with 400")
}

func TestServeICSCompositeMergesMembers(t *testing.T) {
	pool := testhelpers.SetupTestDB(t)
	a := seedCalendarForAPI(t, pool, "Feed A", "api-feed-a")
	b := seedCalendarForAPI(t, pool, "Feed B", "api-feed-b")

	sharedStart := time.Date(2030, 6, 1, 15, 0, 0, 0, time.UTC)
	for _, cal := range []*store.Calendar{a, b} {
		evt := &store.CalendarEvent{CalendarID: cal.ID, Title: "Shared Event", StartTime: sharedStart}
		require.NoError(t, store.CreateCalendarEvent(t.Context(), pool, evt))
	}
	uniq := &store.CalendarEvent{
		CalendarID: b.ID,
		Title:      "Only B",
		StartTime:  time.Date(2030, 6, 2, 15, 0, 0, 0, time.UTC),
	}
	require.NoError(t, store.CreateCalendarEvent(t.Context(), pool, uniq))

	createCompositeForAPI(t, pool, "Merged", "api-merged", []string{a.ID, b.ID})

	req := httptest.NewRequest(http.MethodGet, "/cal/api-merged.ics", nil)
	req.SetPathValue("slug", "api-merged")
	w := httptest.NewRecorder()
	ServeICS(pool).ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	ics := w.Body.String()
	assert.Equal(t, 1, strings.Count(ics, "SUMMARY:Shared Event"), "identical member events must be collapsed")
	assert.Equal(t, 1, strings.Count(ics, "SUMMARY:Only B"))
}
