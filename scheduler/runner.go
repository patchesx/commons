package scheduler

import (
	"context"
	"log"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"commons/pipeline"
	"commons/store"
)

// Run starts the schedule poller. It checks every 30 seconds for due scheduled
// triggers and fires their pipelines via the unified pipeline.RunPipeline.
// The goroutine runs until ctx is cancelled.
func Run(ctx context.Context, pool *pgxpool.Pool, encKey []byte, reg pipeline.CancelRegistry) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	// Fire once on startup so schedules due during downtime fire immediately.
	fireDueSchedules(ctx, pool, encKey, reg)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			fireDueSchedules(ctx, pool, encKey, reg)
		}
	}
}

// fireDueSchedules queries all enabled scheduled triggers, checks which are due,
// and fires their pipelines. Each fire runs in its own goroutine.
func fireDueSchedules(ctx context.Context, pool *pgxpool.Pool, encKey []byte, reg pipeline.CancelRegistry) {
	triggers, err := store.ListEnabledScheduledTriggers(ctx, pool)
	if err != nil {
		log.Printf("scheduler: list enabled triggers: %v", err)
		return
	}

	now := time.Now()
	for _, st := range triggers {
		st := st
		parsed, err := ParseSchedule(st.Schedule)
		if err != nil {
			log.Printf("scheduler: trigger %s has invalid schedule %q: %v", st.ID, st.Schedule, err)
			continue
		}

		loc := LoadLocation(st.Timezone)
		if !parsed.IsDue(st.LastFiredAt, now, loc) {
			continue
		}

		// Mark as fired before launching the goroutine to prevent double-fire
		// within the same tick.
		if err := store.MarkScheduleFired(ctx, pool, st.ID, now); err != nil {
			log.Printf("scheduler: trigger %s mark fired: %v", st.ID, err)
			continue
		}

		go func() {
			bgCtx := context.Background()
			source := store.TriggerSource{
				ID:        st.ID,
				Type:      "scheduled",
				Name:      st.Name,
				Enabled:   st.Enabled,
				ManagedBy: st.ManagedBy,
				CreatedAt: st.CreatedAt,
				UpdatedAt: st.UpdatedAt,
			}
			data := map[string]any{
				"trigger_id":   st.ID,
				"trigger_name": st.Name,
				"schedule":     st.Schedule,
			}
			// Scheduled triggers: createJob=true (appear in Jobs page), reg for cancellation.
			pipeline.RunPipeline(bgCtx, pool, encKey, source, data, reg, true)
		}()
	}
}
