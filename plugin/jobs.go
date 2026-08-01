package plugin

import (
	"context"
	"sync"

	"github.com/jackc/pgx/v5/pgxpool"
)

// JobDetailContributor returns a (key, value) pair to be merged into the job
// detail envelope. Multiple contributors can register for the same job type.
// Returning an empty key or nil value is a no-op.
type JobDetailContributor func(ctx context.Context, pool *pgxpool.Pool, jobID string) (key string, value any)

// JobSummaryProvider returns a one-line human-readable description for a job.
// Only one provider should be registered per job type.
type JobSummaryProvider func(ctx context.Context, pool *pgxpool.Pool, jobID string) string

var (
	detailContributors   = map[string][]JobDetailContributor{}
	detailContributorsMu sync.RWMutex

	summaryProviders   = map[string]JobSummaryProvider{}
	summaryProvidersMu sync.RWMutex
)

// RegisterJobDetailContributor registers fn as a contributor for jobType.
func RegisterJobDetailContributor(jobType string, fn JobDetailContributor) {
	detailContributorsMu.Lock()
	detailContributors[jobType] = append(detailContributors[jobType], fn)
	detailContributorsMu.Unlock()
}

// RegisterJobSummaryProvider registers fn as the summary provider for jobType.
func RegisterJobSummaryProvider(jobType string, fn JobSummaryProvider) {
	summaryProvidersMu.Lock()
	summaryProviders[jobType] = fn
	summaryProvidersMu.Unlock()
}

// GetJobDetailContributors returns all contributors registered for jobType.
func GetJobDetailContributors(jobType string) []JobDetailContributor {
	detailContributorsMu.RLock()
	defer detailContributorsMu.RUnlock()
	return detailContributors[jobType]
}

// GetJobSummaryProvider returns the summary provider for jobType, or nil.
func GetJobSummaryProvider(jobType string) JobSummaryProvider {
	summaryProvidersMu.RLock()
	defer summaryProvidersMu.RUnlock()
	return summaryProviders[jobType]
}
