package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"commons/internal/testhelpers"
	"commons/plugin"
	"commons/store"
)

func TestBuildSummaryRecordingUploadFallback(t *testing.T) {
	j := store.Job{Type: store.JobTypeRecordingUpload}
	assert.Equal(t, "Recording upload", BuildSummary(context.Background(), nil, j))
}

func TestBuildSummaryWithRegisteredProvider(t *testing.T) {
	const jobType = "test_type_with_provider"
	plugin.RegisterJobSummaryProvider(jobType, func(_ context.Context, _ *pgxpool.Pool, _ string) string {
		return "custom summary"
	})
	j := store.Job{Type: jobType}
	assert.Equal(t, "custom summary", BuildSummary(context.Background(), nil, j))
}

func TestBuildSummaryKnownTypes(t *testing.T) {
	cases := []struct {
		jobType string
		want    string
	}{
		{store.JobTypeMemberSync, "Member sync"},
		{store.JobTypeOpenStatesSync, "OpenStates sync"},
		{store.JobTypeLegistarSync, "Legistar sync"},
		{store.JobTypeMeetingSync, "Zoom meeting sync"},
	}
	for _, tc := range cases {
		t.Run(tc.jobType, func(t *testing.T) {
			assert.Equal(t, tc.want, BuildSummary(context.Background(), nil, store.Job{Type: tc.jobType}))
		})
	}
}

func TestBuildSummaryUnknownTypeFallsBackToTypeString(t *testing.T) {
	j := store.Job{Type: "some_future_job_type"}
	assert.Equal(t, "some_future_job_type", BuildSummary(context.Background(), nil, j))
}

func TestCancelJobHandlerNotFound(t *testing.T) {
	pool := testhelpers.SetupTestDB(t)

	var called atomic.Bool
	h := CancelJob(pool, func(string) bool { called.Store(true); return false })
	req := httptest.NewRequest(http.MethodPost, "/api/jobs/00000000-0000-0000-0000-000000000000/cancel", nil)
	req.SetPathValue("id", "00000000-0000-0000-0000-000000000000")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
	assert.False(t, called.Load())
}

func TestCancelJobHandlerAlreadyTerminal(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	j := &store.Job{
		Type:    store.JobTypeRecordingUpload,
		Feature: store.JobFeatureRecordingPipeline,
		Trigger: store.JobTriggerWebhook,
		Status:  store.JobStatusRunning,
	}
	require.NoError(t, store.CreateJob(ctx, pool, j))
	require.NoError(t, store.CompleteJob(ctx, pool, j.ID))

	var called atomic.Bool
	h := CancelJob(pool, func(string) bool { called.Store(true); return false })
	req := httptest.NewRequest(http.MethodPost, "/api/jobs/"+j.ID+"/cancel", nil)
	req.SetPathValue("id", j.ID)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	assert.Equal(t, http.StatusConflict, w.Code)
	assert.False(t, called.Load())
}

func TestCancelJobHandlerSuccess(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	j := &store.Job{
		Type:    store.JobTypeRecordingUpload,
		Feature: store.JobFeatureRecordingPipeline,
		Trigger: store.JobTriggerWebhook,
		Status:  store.JobStatusRunning,
	}
	require.NoError(t, store.CreateJob(ctx, pool, j))

	var cancelledID string
	h := CancelJob(pool, func(id string) bool { cancelledID = id; return true })
	req := httptest.NewRequest(http.MethodPost, "/api/jobs/"+j.ID+"/cancel", nil)
	req.SetPathValue("id", j.ID)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, j.ID, cancelledID)

	got, err := store.GetJobByID(ctx, pool, j.ID)
	require.NoError(t, err)
	assert.Equal(t, store.JobStatusCancelled, got.Status)
}

func TestCancelJobHandlerContextNotRegistered(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	j := &store.Job{
		Type:    store.JobTypeMemberSync,
		Feature: store.JobFeatureMemberPortal,
		Trigger: store.JobTriggerScheduled,
		Status:  store.JobStatusRunning,
	}
	require.NoError(t, store.CreateJob(ctx, pool, j))

	// cancelFn returns false — job not in registry (e.g. a sync job)
	h := CancelJob(pool, func(string) bool { return false })
	req := httptest.NewRequest(http.MethodPost, "/api/jobs/"+j.ID+"/cancel", nil)
	req.SetPathValue("id", j.ID)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	// Still succeeds — DB update is the source of truth
	require.Equal(t, http.StatusOK, w.Code)

	got, err := store.GetJobByID(ctx, pool, j.ID)
	require.NoError(t, err)
	assert.Equal(t, store.JobStatusCancelled, got.Status)
}
