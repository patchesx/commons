package zoom

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"commons/plugin"
	"commons/store"
	"commons/web"
)

// pkgIntegrationID is the integrations.id for the Zoom integration row.
// Set during Init; read by Handler via IntegrationID().
var pkgIntegrationID string

// IntegrationID returns the Zoom integration's database ID.
// Available after plugin.InitAll has run.
func IntegrationID() string {
	return pkgIntegrationID
}

// ZoomPlugin registers the Zoom integration with the plugin system.
type ZoomPlugin struct{}

func init() {
	plugin.Register(&ZoomPlugin{})
}

func (p *ZoomPlugin) Name() string    { return "zoom" }
func (p *ZoomPlugin) Label() string   { return "Zoom" }
func (p *ZoomPlugin) Version() string { return "1.0.0" }

func (p *ZoomPlugin) Migrations() []plugin.Migration { return nil }
func (p *ZoomPlugin) Provides() []string {
	return []string{"zoom.recordings", "zoom.scheduling"}
}

// Init looks up the Zoom integration ID, registers the webhook processor,
// registers job detail/summary providers, HTTP routes, and the meeting sync job.
func (p *ZoomPlugin) Init(pctx plugin.PluginContext) error {
	pool := pctx.DB()
	encKey := pctx.EncKey()

	// Register the recording streamer so video hosts and storage backends can fetch
	// recording files without knowing anything about Zoom's CDN or auth scheme.
	pctx.SetRecordingStreamer(&ZoomStreamer{})

	// Register the meeting scheduler so chat plugins can schedule via platform interface.
	pctx.SetMeetingScheduler(NewZoomScheduler(pool, encKey))

	integ, err := store.GetIntegrationByType(context.Background(), pool, "zoom")
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		log.Printf("zoom/plugin: failed to look up integration row: %v", err)
	} else if integ != nil {
		pkgIntegrationID = integ.ID
	}

	// Register the Zoom webhook processor and seed its type into the DB.
	proc := &ZoomWebhookProcessor{pool: pool, encKey: encKey}
	plugin.RegisterProcessor(proc)
	if err := store.SeedWebhookProcessorType(context.Background(), pool, proc.Type(), proc.Label()); err != nil {
		log.Printf("zoom/plugin: failed to seed processor type: %v", err)
	}

	// Register action types.
	plugin.RegisterActionType(&DeleteRecordingAction{pool: pool, encKey: encKey})

	// Register job detail contributor: zoom recording metadata.
	plugin.RegisterJobDetailContributor(store.JobTypeRecordingUpload, func(ctx context.Context, p *pgxpool.Pool, jobID string) (string, any) {
		rec, err := GetRecordingData(ctx, p, jobID)
		if err != nil || rec == nil {
			return "", nil
		}
		return "zoom", rec
	})

	// Register job summary provider: use meeting topic for recording uploads.
	plugin.RegisterJobSummaryProvider(store.JobTypeRecordingUpload, func(ctx context.Context, p *pgxpool.Pool, jobID string) string {
		rec, err := GetRecordingData(ctx, p, jobID)
		if err != nil || rec == nil {
			return "Recording upload"
		}
		return rec.MeetingTopic
	})

	// Register job summary provider: zoom meeting sync.
	plugin.RegisterJobSummaryProvider(store.JobTypeMeetingSync, func(_ context.Context, _ *pgxpool.Pool, _ string) string {
		return "Zoom meeting sync"
	})

	getAdminID := func(ctx context.Context) string {
		return web.UserIDFromContext(ctx)
	}

	syncFn := func() {
		SyncMeetings(context.Background(), pool, encKey)
	}

	// Register HTTP routes.
	pctx.RegisterAuthRoute("GET", "/api/zoom/s2s-configured", HandleS2SConfigured(pool, encKey))
	pctx.RegisterAuthRoute("POST", "/api/jobs/{id}/delete-zoom-recording", HandleDeleteRecording(pool, encKey))
	pctx.RegisterAuthRoute("GET", "/api/meetings", HandleListMeetings(pool))
	pctx.RegisterAuthRoute("POST", "/api/meetings", HandleScheduleMeeting(pool, encKey, getAdminID))
	pctx.RegisterAuthRoute("DELETE", "/api/meetings/{id}", HandleCancelMeeting(pool, encKey, getAdminID))
	pctx.RegisterAuthRoute("POST", "/api/zoom/meetings/sync", HandleTriggerMeetingSync(syncFn))

	// Register scheduled job.
	pctx.RegisterScheduledJob(
		"zoom_meeting_sync_enabled", "zoom_meeting_sync_interval_minutes",
		1*time.Minute, syncFn,
	)

	return nil
}
