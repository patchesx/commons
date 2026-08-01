package store

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestBuildFilterEmpty(t *testing.T) {
	where, args := buildFilter(JobFilter{})
	assert.Equal(t, "", where)
	assert.Empty(t, args)
}

func TestBuildFilterTypeOnly(t *testing.T) {
	where, args := buildFilter(JobFilter{Type: "recording_upload"})
	assert.Equal(t, " WHERE type = $1", where)
	assert.Equal(t, []any{"recording_upload"}, args)
}

func TestBuildFilterFeatureOnly(t *testing.T) {
	where, args := buildFilter(JobFilter{Feature: "recording_pipeline"})
	assert.Equal(t, " WHERE feature = $1", where)
	assert.Equal(t, []any{"recording_pipeline"}, args)
}

func TestBuildFilterStatusOnly(t *testing.T) {
	where, args := buildFilter(JobFilter{Status: "running"})
	assert.Equal(t, " WHERE status = $1", where)
	assert.Equal(t, []any{"running"}, args)
}

func TestBuildFilterAllThree(t *testing.T) {
	f := JobFilter{Type: "recording_upload", Feature: "recording_pipeline", Status: "complete"}
	where, args := buildFilter(f)
	assert.Equal(t, " WHERE type = $1 AND feature = $2 AND status = $3", where)
	assert.Equal(t, []any{"recording_upload", "recording_pipeline", "complete"}, args)
}

func TestBuildFilterTypeAndStatus(t *testing.T) {
	// Feature is empty; status must use $2, not $3.
	f := JobFilter{Type: "member_sync", Status: "failed"}
	where, args := buildFilter(f)
	assert.Equal(t, " WHERE type = $1 AND status = $2", where)
	assert.Equal(t, []any{"member_sync", "failed"}, args)
}

func TestBuildFilterFeatureAndStatus(t *testing.T) {
	f := JobFilter{Feature: "legislation_sync", Status: "running"}
	where, args := buildFilter(f)
	assert.Equal(t, " WHERE feature = $1 AND status = $2", where)
	assert.Equal(t, []any{"legislation_sync", "running"}, args)
}

func TestCountArgs(t *testing.T) {
	assert.Equal(t, 0, countArgs(JobFilter{}))
	assert.Equal(t, 1, countArgs(JobFilter{Type: "x"}))
	assert.Equal(t, 1, countArgs(JobFilter{Feature: "x"}))
	assert.Equal(t, 1, countArgs(JobFilter{Status: "x"}))
	assert.Equal(t, 2, countArgs(JobFilter{Type: "x", Status: "x"}))
	assert.Equal(t, 3, countArgs(JobFilter{Type: "x", Feature: "x", Status: "x"}))
}

func TestArgN(t *testing.T) {
	// No filters: LIMIT=$1, OFFSET=$2
	assert.Equal(t, "1", argN(JobFilter{}, 1))
	assert.Equal(t, "2", argN(JobFilter{}, 2))

	// One filter: LIMIT=$2, OFFSET=$3
	assert.Equal(t, "2", argN(JobFilter{Type: "x"}, 1))
	assert.Equal(t, "3", argN(JobFilter{Type: "x"}, 2))

	// Three filters: LIMIT=$4, OFFSET=$5
	f := JobFilter{Type: "x", Feature: "x", Status: "x"}
	assert.Equal(t, "4", argN(f, 1))
	assert.Equal(t, "5", argN(f, 2))
}
