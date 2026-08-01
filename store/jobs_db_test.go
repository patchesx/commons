package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"commons/internal/testhelpers"
)

func TestCreateJobPopulatesIDAndStartedAt(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	j := &Job{
		Type:    JobTypeRecordingUpload,
		Feature: JobFeatureRecordingPipeline,
		Trigger: JobTriggerWebhook,
		Status:  JobStatusPending,
	}
	require.NoError(t, CreateJob(ctx, pool, j))
	assert.NotEmpty(t, j.ID)
	assert.False(t, j.StartedAt.IsZero())
}

func TestUpdateJobStatus(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	j := &Job{Type: JobTypeRecordingUpload, Feature: JobFeatureRecordingPipeline, Trigger: JobTriggerWebhook, Status: JobStatusPending}
	require.NoError(t, CreateJob(ctx, pool, j))

	require.NoError(t, UpdateJobStatus(ctx, pool, j.ID, JobStatusRunning, nil))

	got, err := GetJobByID(ctx, pool, j.ID)
	require.NoError(t, err)
	assert.Equal(t, JobStatusRunning, got.Status)
	assert.Nil(t, got.ErrorMessage)
}

func TestFailJob(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	j := &Job{Type: JobTypeRecordingUpload, Feature: JobFeatureRecordingPipeline, Trigger: JobTriggerWebhook, Status: JobStatusRunning}
	require.NoError(t, CreateJob(ctx, pool, j))

	require.NoError(t, FailJob(ctx, pool, j.ID, "something went wrong"))

	got, err := GetJobByID(ctx, pool, j.ID)
	require.NoError(t, err)
	assert.Equal(t, JobStatusFailed, got.Status)
	require.NotNil(t, got.ErrorMessage)
	assert.Equal(t, "something went wrong", *got.ErrorMessage)
	assert.NotNil(t, got.CompletedAt)
}

func TestCompleteJob(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	j := &Job{Type: JobTypeRecordingUpload, Feature: JobFeatureRecordingPipeline, Trigger: JobTriggerWebhook, Status: JobStatusRunning}
	require.NoError(t, CreateJob(ctx, pool, j))

	require.NoError(t, CompleteJob(ctx, pool, j.ID))

	got, err := GetJobByID(ctx, pool, j.ID)
	require.NoError(t, err)
	assert.Equal(t, JobStatusComplete, got.Status)
	assert.Nil(t, got.ErrorMessage)
	assert.NotNil(t, got.CompletedAt)
}

func TestCancelJobPending(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	j := &Job{Type: JobTypeRecordingUpload, Feature: JobFeatureRecordingPipeline, Trigger: JobTriggerWebhook, Status: JobStatusPending}
	require.NoError(t, CreateJob(ctx, pool, j))

	ok, err := CancelJob(ctx, pool, j.ID)
	require.NoError(t, err)
	assert.True(t, ok)

	got, err := GetJobByID(ctx, pool, j.ID)
	require.NoError(t, err)
	assert.Equal(t, JobStatusCancelled, got.Status)
	assert.NotNil(t, got.CompletedAt)
}

func TestCancelJobRunning(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	j := &Job{Type: JobTypeRecordingUpload, Feature: JobFeatureRecordingPipeline, Trigger: JobTriggerWebhook, Status: JobStatusRunning}
	require.NoError(t, CreateJob(ctx, pool, j))

	ok, err := CancelJob(ctx, pool, j.ID)
	require.NoError(t, err)
	assert.True(t, ok)

	got, err := GetJobByID(ctx, pool, j.ID)
	require.NoError(t, err)
	assert.Equal(t, JobStatusCancelled, got.Status)
}

func TestCancelJobDoesNotOverwriteTerminal(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	j := &Job{Type: JobTypeRecordingUpload, Feature: JobFeatureRecordingPipeline, Trigger: JobTriggerWebhook, Status: JobStatusRunning}
	require.NoError(t, CreateJob(ctx, pool, j))
	require.NoError(t, CompleteJob(ctx, pool, j.ID))

	ok, err := CancelJob(ctx, pool, j.ID)
	require.NoError(t, err)
	assert.False(t, ok)

	got, err := GetJobByID(ctx, pool, j.ID)
	require.NoError(t, err)
	assert.Equal(t, JobStatusComplete, got.Status)
}

func TestFailJobDoesNotOverwriteCancelled(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	j := &Job{Type: JobTypeRecordingUpload, Feature: JobFeatureRecordingPipeline, Trigger: JobTriggerWebhook, Status: JobStatusRunning}
	require.NoError(t, CreateJob(ctx, pool, j))

	ok, err := CancelJob(ctx, pool, j.ID)
	require.NoError(t, err)
	require.True(t, ok)

	// Simulate the in-flight goroutine calling FailJob after cancel.
	require.NoError(t, FailJob(ctx, pool, j.ID, "context canceled"))

	got, err := GetJobByID(ctx, pool, j.ID)
	require.NoError(t, err)
	assert.Equal(t, JobStatusCancelled, got.Status, "FailJob must not overwrite cancelled status")
}

func TestFailJobDoesNotOverwriteComplete(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	j := &Job{Type: JobTypeRecordingUpload, Feature: JobFeatureRecordingPipeline, Trigger: JobTriggerWebhook, Status: JobStatusRunning}
	require.NoError(t, CreateJob(ctx, pool, j))
	require.NoError(t, CompleteJob(ctx, pool, j.ID))

	require.NoError(t, FailJob(ctx, pool, j.ID, "late error"))

	got, err := GetJobByID(ctx, pool, j.ID)
	require.NoError(t, err)
	assert.Equal(t, JobStatusComplete, got.Status, "FailJob must not overwrite complete status")
}
