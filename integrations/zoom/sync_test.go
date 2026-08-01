package zoom

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"commons/internal/testhelpers"
	"commons/store"
)

// ---- syncMeetingDiffers ----

func baseExisting() *store.ScheduledMeeting {
	return &store.ScheduledMeeting{
		Topic:           "Weekly Sync",
		Agenda:          "Stand-up",
		DurationMinutes: 30,
		Timezone:        "America/Chicago",
		JoinURL:         "https://zoom.us/j/111",
		IsRecurring:     false,
	}
}

func baseZoom() *ZoomMeeting {
	return &ZoomMeeting{
		Topic:       "Weekly Sync",
		Agenda:      "Stand-up",
		Duration:    30,
		Timezone:    "America/Chicago",
		JoinURL:     "https://zoom.us/j/111",
		IsRecurring: false,
	}
}

func TestSyncMeetingDiffersIdentical(t *testing.T) {
	assert.False(t, syncMeetingDiffers(baseExisting(), baseZoom()))
}

func TestSyncMeetingDiffersTopic(t *testing.T) {
	zm := baseZoom()
	zm.Topic = "Changed Topic"
	assert.True(t, syncMeetingDiffers(baseExisting(), zm))
}

func TestSyncMeetingDiffersAgenda(t *testing.T) {
	zm := baseZoom()
	zm.Agenda = "New agenda"
	assert.True(t, syncMeetingDiffers(baseExisting(), zm))
}

func TestSyncMeetingDiffersDuration(t *testing.T) {
	zm := baseZoom()
	zm.Duration = 60
	assert.True(t, syncMeetingDiffers(baseExisting(), zm))
}

func TestSyncMeetingDiffersTimezone(t *testing.T) {
	zm := baseZoom()
	zm.Timezone = "UTC"
	assert.True(t, syncMeetingDiffers(baseExisting(), zm))
}

func TestSyncMeetingDiffersJoinURL(t *testing.T) {
	zm := baseZoom()
	zm.JoinURL = "https://zoom.us/j/999"
	assert.True(t, syncMeetingDiffers(baseExisting(), zm))
}

func TestSyncMeetingDiffersIsRecurring(t *testing.T) {
	zm := baseZoom()
	zm.IsRecurring = true
	assert.True(t, syncMeetingDiffers(baseExisting(), zm))
}

func TestSyncMeetingDiffersPasswordExcluded(t *testing.T) {
	// Password changes are intentionally excluded from the diff check.
	existing := baseExisting()
	existing.Password = "oldpass"
	zm := baseZoom()
	zm.Password = "newpass"
	assert.False(t, syncMeetingDiffers(existing, zm))
}

// ---- toStoreOccurrences ----

func TestToStoreOccurrencesEmpty(t *testing.T) {
	assert.Empty(t, toStoreOccurrences(nil))
	assert.Empty(t, toStoreOccurrences([]MeetingOccurrence{}))
}

func TestToStoreOccurrencesConverts(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	in := []MeetingOccurrence{
		{OccurrenceID: "occ-1", StartTime: now, Duration: 45, Status: "available"},
		{OccurrenceID: "occ-2", StartTime: now.Add(time.Hour), Duration: 30, Status: "deleted"},
	}
	out := toStoreOccurrences(in)
	require.Len(t, out, 2)
	assert.Equal(t, "occ-1", out[0].ZoomOccurrenceID)
	assert.Equal(t, now, out[0].StartTime)
	assert.Equal(t, 45, out[0].DurationMinutes)
	assert.Equal(t, "available", out[0].Status)
	assert.Equal(t, "occ-2", out[1].ZoomOccurrenceID)
}

// ---- marshalMeetingSettings ----

func TestMarshalMeetingSettings(t *testing.T) {
	b := marshalMeetingSettings(MeetingSettings{JoinBeforeHost: true})
	require.NotNil(t, b)
	var out map[string]any
	require.NoError(t, json.Unmarshal(b, &out))
	assert.Equal(t, true, out["join_before_host"])
}

func TestMarshalMeetingSettingsDefaults(t *testing.T) {
	b := marshalMeetingSettings(MeetingSettings{})
	require.NotNil(t, b)
	var out map[string]any
	require.NoError(t, json.Unmarshal(b, &out))
	assert.Equal(t, false, out["join_before_host"])
}

// ---- fetchMeetingDetail ----

func TestFetchMeetingDetailSuccess(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	payload := map[string]any{
		"id":         int64(99999),
		"uuid":       "uuid-abc",
		"host_email": "host@example.com",
		"topic":      "Team Call",
		"agenda":     "Weekly check-in",
		"duration":   60,
		"timezone":   "America/Chicago",
		"join_url":   "https://zoom.us/j/99999",
		"start_url":  "https://zoom.us/s/99999",
		"password":   "secret",
		"recurrence": map[string]any{"type": 2},
		"occurrences": []map[string]any{
			{"occurrence_id": "O-1", "start_time": now.Format(time.RFC3339), "duration": 60, "status": "available"},
		},
		"settings": map[string]any{"join_before_host": true},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(payload)
	}))
	defer srv.Close()

	orig := http.DefaultClient
	http.DefaultClient = &http.Client{Transport: &testRedirectTransport{base: srv.URL}}
	defer func() { http.DefaultClient = orig }()

	zm, err := fetchMeetingDetail(context.Background(), "tok", 99999)
	require.NoError(t, err)
	assert.Equal(t, int64(99999), zm.ID)
	assert.Equal(t, "Team Call", zm.Topic)
	assert.True(t, zm.IsRecurring)
	require.Len(t, zm.Occurrences, 1)
	assert.Equal(t, "O-1", zm.Occurrences[0].OccurrenceID)
	assert.Equal(t, now, zm.Occurrences[0].StartTime)
	assert.True(t, zm.Settings.JoinBeforeHost)
}

func TestFetchMeetingDetailNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer srv.Close()

	orig := http.DefaultClient
	http.DefaultClient = &http.Client{Transport: &testRedirectTransport{base: srv.URL}}
	defer func() { http.DefaultClient = orig }()

	_, err := fetchMeetingDetail(context.Background(), "tok", 12345)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "404")
}

// ---- runMeetingSync helpers ----

// zoomFakeServer builds a test server that mocks the Zoom S2S token endpoint,
// the list-meetings endpoint, and the meeting-detail endpoint.
// listResp is returned from GET /v2/users/me/meetings.
// detailFn, if non-nil, is called for GET /v2/meetings/{id} requests.
func zoomFakeServer(t *testing.T, listResp listMeetingsResponse, detailFn func(id string) any) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	// S2S token endpoint.
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"access_token": "test-tok"})
	})

	// List meetings.
	mux.HandleFunc("/v2/users/me/meetings", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(listResp)
	})

	// Meeting detail — the sync fetches detail for every meeting. detailFn
	// overrides the response; by default a one-off detail response is
	// synthesized from the matching list summary (top-level start_time, no
	// occurrences array — fetchMeetingDetail synthesizes the single occurrence).
	mux.HandleFunc("/v2/meetings/", func(w http.ResponseWriter, r *http.Request) {
		// Path is /v2/meetings/{id}?show_previous_occurrences=false
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/v2/meetings/"), "/")
		id := parts[0]
		if detailFn != nil {
			json.NewEncoder(w).Encode(detailFn(id))
			return
		}
		for _, m := range listResp.Meetings {
			if strconv.FormatInt(m.ID, 10) == id {
				json.NewEncoder(w).Encode(map[string]any{
					"id":         m.ID,
					"uuid":       "uuid-" + id,
					"topic":      m.Topic,
					"start_time": m.StartTime,
					"duration":   m.Duration,
					"timezone":   m.Timezone,
					"settings":   map[string]any{"private_meeting": false},
				})
				return
			}
		}
		http.Error(w, "not found", http.StatusNotFound)
	})

	return httptest.NewServer(mux)
}

// seedZoomSyncPrereqs inserts a zoom integration row and seeds the three required
// config keys so AccessToken can complete without real credentials.
func seedZoomSyncPrereqs(t *testing.T, ctx context.Context, pool *pgxpool.Pool, encKey []byte) string {
	t.Helper()
	var integrationID string
	err := pool.QueryRow(ctx,
		`INSERT INTO integrations (type, name) VALUES ('zoom', 'Test Zoom') RETURNING id`,
	).Scan(&integrationID)
	require.NoError(t, err)
	return integrationID
}

func TestRunMeetingSyncImportsOneOff(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)
	encKey := testhelpers.EncKey()

	integrationID := seedZoomSyncPrereqs(t, ctx, pool, encKey)

	require.NoError(t, store.SetServiceConfig(ctx, pool, "zoom", "account_id", "acct-1", false, nil, encKey))
	require.NoError(t, store.SetServiceConfig(ctx, pool, "zoom", "api_client_id", "cid-1", false, nil, encKey))
	require.NoError(t, store.SetServiceConfig(ctx, pool, "zoom", "api_client_secret", "csec-1", true, nil, encKey))

	startTime := time.Now().UTC().Truncate(time.Second).Add(2 * time.Hour)
	listResp := listMeetingsResponse{
		Meetings: []zoomMeetingSummary{
			{ID: 11111, Topic: "New Meeting", Type: 2, StartTime: startTime.Format(time.RFC3339), Duration: 60, Timezone: "America/Chicago"},
		},
	}

	srv := zoomFakeServer(t, listResp, nil)
	defer srv.Close()

	orig := http.DefaultClient
	http.DefaultClient = &http.Client{Transport: &testRedirectTransport{base: srv.URL}}
	defer func() { http.DefaultClient = orig }()

	imported, updated, deleted, occs, err := runMeetingSync(ctx, pool, encKey, integrationID)
	require.NoError(t, err)
	assert.Equal(t, 1, imported)
	assert.Equal(t, 0, updated)
	assert.Equal(t, 0, deleted)
	assert.Equal(t, 1, occs)

	m, err := store.GetScheduledMeetingByZoomID(ctx, pool, 11111)
	require.NoError(t, err)
	require.NotNil(t, m)
	assert.Equal(t, "New Meeting", m.Topic)
	assert.False(t, m.IsRecurring)
}

func TestRunMeetingSyncImportsRecurring(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)
	encKey := testhelpers.EncKey()

	integrationID := seedZoomSyncPrereqs(t, ctx, pool, encKey)
	require.NoError(t, store.SetServiceConfig(ctx, pool, "zoom", "account_id", "acct-1", false, nil, encKey))
	require.NoError(t, store.SetServiceConfig(ctx, pool, "zoom", "api_client_id", "cid-1", false, nil, encKey))
	require.NoError(t, store.SetServiceConfig(ctx, pool, "zoom", "api_client_secret", "csec-1", true, nil, encKey))

	occ1 := time.Now().UTC().Truncate(time.Second).Add(24 * time.Hour)
	occ2 := occ1.Add(7 * 24 * time.Hour)

	listResp := listMeetingsResponse{
		Meetings: []zoomMeetingSummary{
			{ID: 22222, Topic: "Weekly Standup", Type: 8, Duration: 30, Timezone: "America/Chicago"},
		},
	}

	detailFn := func(_ string) any {
		return map[string]any{
			"id": int64(22222), "topic": "Weekly Standup", "duration": 30,
			"timezone": "America/Chicago", "join_url": "https://zoom.us/j/22222",
			"recurrence": map[string]any{"type": 1},
			"occurrences": []map[string]any{
				{"occurrence_id": "O-A", "start_time": occ1.Format(time.RFC3339), "duration": 30, "status": "available"},
				{"occurrence_id": "O-B", "start_time": occ2.Format(time.RFC3339), "duration": 30, "status": "available"},
			},
			"settings": map[string]any{"join_before_host": false},
		}
	}

	srv := zoomFakeServer(t, listResp, detailFn)
	defer srv.Close()

	orig := http.DefaultClient
	http.DefaultClient = &http.Client{Transport: &testRedirectTransport{base: srv.URL}}
	defer func() { http.DefaultClient = orig }()

	imported, updated, deleted, occs, err := runMeetingSync(ctx, pool, encKey, integrationID)
	require.NoError(t, err)
	assert.Equal(t, 1, imported)
	assert.Equal(t, 0, updated)
	assert.Equal(t, 0, deleted)
	assert.Equal(t, 2, occs)

	m, err := store.GetScheduledMeetingByZoomID(ctx, pool, 22222)
	require.NoError(t, err)
	require.NotNil(t, m)
	assert.True(t, m.IsRecurring)
}

func TestRunMeetingSyncUpdatesExisting(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)
	encKey := testhelpers.EncKey()

	integrationID := seedZoomSyncPrereqs(t, ctx, pool, encKey)
	require.NoError(t, store.SetServiceConfig(ctx, pool, "zoom", "account_id", "acct-1", false, nil, encKey))
	require.NoError(t, store.SetServiceConfig(ctx, pool, "zoom", "api_client_id", "cid-1", false, nil, encKey))
	require.NoError(t, store.SetServiceConfig(ctx, pool, "zoom", "api_client_secret", "csec-1", true, nil, encKey))

	// Pre-seed the meeting locally with the old topic.
	sentinelID, err := store.GetZoomSentinelUserID(ctx, pool)
	require.NoError(t, err)
	existing := &store.ScheduledMeeting{
		IntegrationID: integrationID, ZoomMeetingID: 33333, Topic: "Old Topic",
		DurationMinutes: 30, Timezone: "UTC", IsRecurring: false, CreatedBy: sentinelID,
	}
	startTime := time.Now().UTC().Truncate(time.Second).Add(time.Hour)
	require.NoError(t, store.CreateScheduledMeeting(ctx, pool, encKey, existing,
		[]store.MeetingOccurrence{{StartTime: startTime, DurationMinutes: 30, Status: "available"}}))

	// Zoom now reports a different topic.
	listResp := listMeetingsResponse{
		Meetings: []zoomMeetingSummary{
			{ID: 33333, Topic: "Updated Topic", Type: 2, StartTime: startTime.Format(time.RFC3339), Duration: 30, Timezone: "UTC"},
		},
	}

	srv := zoomFakeServer(t, listResp, nil)
	defer srv.Close()

	orig := http.DefaultClient
	http.DefaultClient = &http.Client{Transport: &testRedirectTransport{base: srv.URL}}
	defer func() { http.DefaultClient = orig }()

	imported, updated, deleted, _, err := runMeetingSync(ctx, pool, encKey, integrationID)
	require.NoError(t, err)
	assert.Equal(t, 0, imported)
	assert.Equal(t, 1, updated)
	assert.Equal(t, 0, deleted)

	m, err := store.GetScheduledMeetingByZoomID(ctx, pool, 33333)
	require.NoError(t, err)
	assert.Equal(t, "Updated Topic", m.Topic)
}

func TestRunMeetingSyncDeletesStale(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)
	encKey := testhelpers.EncKey()

	integrationID := seedZoomSyncPrereqs(t, ctx, pool, encKey)
	require.NoError(t, store.SetServiceConfig(ctx, pool, "zoom", "account_id", "acct-1", false, nil, encKey))
	require.NoError(t, store.SetServiceConfig(ctx, pool, "zoom", "api_client_id", "cid-1", false, nil, encKey))
	require.NoError(t, store.SetServiceConfig(ctx, pool, "zoom", "api_client_secret", "csec-1", true, nil, encKey))

	// Pre-seed a local meeting that Zoom no longer knows about.
	sentinelID, err := store.GetZoomSentinelUserID(ctx, pool)
	require.NoError(t, err)
	stale := &store.ScheduledMeeting{
		IntegrationID: integrationID, ZoomMeetingID: 44444, Topic: "Gone Meeting",
		DurationMinutes: 60, Timezone: "UTC", IsRecurring: false, CreatedBy: sentinelID,
	}
	require.NoError(t, store.CreateScheduledMeeting(ctx, pool, encKey, stale,
		[]store.MeetingOccurrence{{StartTime: time.Now().Add(time.Hour), DurationMinutes: 60, Status: "available"}}))

	// Zoom returns an empty list.
	srv := zoomFakeServer(t, listMeetingsResponse{}, nil)
	defer srv.Close()

	orig := http.DefaultClient
	http.DefaultClient = &http.Client{Transport: &testRedirectTransport{base: srv.URL}}
	defer func() { http.DefaultClient = orig }()

	imported, updated, deleted, _, err := runMeetingSync(ctx, pool, encKey, integrationID)
	require.NoError(t, err)
	assert.Equal(t, 0, imported)
	assert.Equal(t, 0, updated)
	assert.Equal(t, 1, deleted)

	m, err := store.GetScheduledMeetingByZoomID(ctx, pool, 44444)
	assert.ErrorIs(t, err, store.ErrNotFound, "stale meeting should be deleted")
	assert.Nil(t, m)
}

// testRedirectTransport redirects all requests to a test server.
type testRedirectTransport struct {
	base string
}

func (t *testRedirectTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	newURL := t.base + req.URL.Path + "?" + req.URL.RawQuery
	newReq, err := http.NewRequestWithContext(req.Context(), req.Method, newURL, req.Body)
	if err != nil {
		return nil, err
	}
	for k, vs := range req.Header {
		newReq.Header[k] = vs
	}
	return http.DefaultTransport.RoundTrip(newReq)
}
