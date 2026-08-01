package zoom

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"commons/platform"
	"commons/plugin"
	"commons/store"
)

// ZoomWebhookProcessor implements plugin.WebhookProcessor for Zoom recording events.
type ZoomWebhookProcessor struct {
	pool   *pgxpool.Pool
	encKey []byte
}

func (p *ZoomWebhookProcessor) Type() string               { return "zoom" }
func (p *ZoomWebhookProcessor) Label() string              { return "Zoom Recording Webhook" }
func (p *ZoomWebhookProcessor) VerificationMethod() string { return "none" }

func (p *ZoomWebhookProcessor) DataSchema() []plugin.DataFieldDef {
	return []plugin.DataFieldDef{
		{Key: "job_id", Label: "Job ID", Type: "string"},
		{Key: "meeting_uuid", Label: "Meeting UUID", Type: "string"},
		{Key: "meeting_id", Label: "Zoom Meeting ID", Type: "number"},
		{Key: "meeting_topic", Label: "Meeting Topic", Type: "string"},
		{Key: "meeting_date", Label: "Meeting Date", Type: "string"},
		{Key: "host_email", Label: "Host Email", Type: "string"},
		{Key: "duration_minutes", Label: "Duration (minutes)", Type: "number"},
		{Key: "duration_secs", Label: "Duration (seconds)", Type: "number"},
		{Key: "occurrence_id", Label: "Occurrence ID", Type: "string"},
		{Key: "integration_id", Label: "Integration ID", Type: "string"},
		{Key: "download_url", Label: "Download URL", Type: "string"},
		{Key: "file_size", Label: "File Size (bytes)", Type: "number"},
		{Key: "transcript_url", Label: "Transcript URL", Type: "string"},
		{Key: "is_private", Label: "Private Meeting", Type: "boolean",
			Description: "Whether the meeting was marked private in Zoom settings."},
	}
}

// Extract performs Zoom-specific HMAC verification, deduplication, and payload parsing.
// It writes the HTTP response in all cases.
// Returns (dataMap, nil) when the pipeline should run.
// Returns (nil, nil) when Extract handled everything and the pipeline should not run.
// Returns (nil, err) on internal error.
func (p *ZoomWebhookProcessor) Extract(ctx context.Context, w http.ResponseWriter, r *http.Request, payload []byte, _ store.Webhook) (map[string]any, error) {
	var evt zoomEvent
	if err := json.Unmarshal(payload, &evt); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return nil, fmt.Errorf("unmarshal: %w", err)
	}

	secret, err := store.GetServiceConfig(ctx, p.pool, "zoom", "webhook_secret", p.encKey)
	if errors.Is(err, store.ErrNotFound) {
		log.Printf("zoom/webhook: webhook_secret not configured")
		http.Error(w, "server misconfigured", http.StatusInternalServerError)
		return nil, fmt.Errorf("webhook_secret not configured")
	}
	if err != nil {
		log.Printf("zoom/webhook: failed to read webhook_secret: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return nil, fmt.Errorf("read webhook_secret: %w", err)
	}

	// URL validation challenge — no signature on these.
	if evt.Event == eventURLValidation {
		handleURLChallenge(w, evt.Payload.Object.UUID, secret, payload)
		return nil, nil
	}

	// Reject stale timestamps — prevents replay attacks.
	timestamp := r.Header.Get("x-zm-request-timestamp")
	ts, tsErr := strconv.ParseInt(timestamp, 10, 64)
	if tsErr != nil || time.Now().Unix()-ts > 300 {
		log.Printf("zoom/webhook: stale or missing timestamp")
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return nil, nil
	}

	// Verify HMAC-SHA256 signature.
	sigHeader := r.Header.Get("x-zm-signature")
	if !verifySignature(secret, timestamp, string(payload), sigHeader) {
		log.Printf("zoom/webhook: invalid signature")
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return nil, nil
	}

	// Only process recording completions.
	if evt.Event != eventRecordingCompleted {
		w.WriteHeader(http.StatusOK)
		return nil, nil
	}

	meeting := evt.Payload.Object

	// Dedup check.
	existing, err := GetByMeetingUUID(ctx, p.pool, meeting.UUID)
	if err != nil {
		log.Printf("zoom/webhook: dedup check failed: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return nil, fmt.Errorf("dedup check: %w", err)
	}
	if existing != nil {
		log.Printf("zoom/webhook: duplicate meeting UUID %s — skipping", meeting.UUID)
		w.WriteHeader(http.StatusOK)
		return nil, nil
	}

	// Find the best available MP4.
	file := selectRecordingFile(meeting.RecordingFiles)
	if file == nil {
		log.Printf("zoom/webhook: no completed MP4 found for meeting %s", meeting.UUID)
		w.WriteHeader(http.StatusOK)
		return nil, nil
	}

	// Try to link to a locally-scheduled occurrence.
	occurrenceID, err := FindOccurrenceForRecording(ctx, p.pool, pkgIntegrationID, meeting.ID, meeting.OccurrenceID)
	if err != nil {
		log.Printf("zoom/webhook: occurrence lookup for meeting %d: %v", meeting.ID, err)
	}

	// Check if skip_upload is set for this meeting.
	if skip, err := GetSkipUploadForMeeting(ctx, p.pool, pkgIntegrationID, meeting.ID); err != nil {
		log.Printf("zoom/webhook: skip_upload check for meeting %d: %v", meeting.ID, err)
	} else if skip {
		log.Printf("zoom/webhook: skip_upload set for meeting %d — ignoring recording", meeting.ID)
		w.WriteHeader(http.StatusOK)
		return nil, nil
	}

	// Create the job record first — recording_data.job_id is the PK and NOT NULL.
	job := &store.Job{
		Type:    store.JobTypeRecordingUpload,
		Feature: store.JobFeatureRecordingPipeline,
		Trigger: store.JobTriggerWebhook,
		Status:  store.JobStatusPending,
	}
	if err := store.CreateJob(ctx, p.pool, job); err != nil {
		log.Printf("zoom/webhook: failed to create job: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return nil, fmt.Errorf("create job: %w", err)
	}

	// Create the zoom.recording_data row (dedup guard; must exist before pipeline runs).
	durationSecs := meeting.Duration * 60
	hostEmail := meeting.HostEmail
	rec := RecordingData{
		JobID:                 job.ID,
		IntegrationID:         pkgIntegrationID,
		MeetingUUID:           meeting.UUID,
		MeetingTopic:          meeting.Topic,
		MeetingDate:           meeting.StartTime,
		DurationSecs:          &durationSecs,
		HostEmail:             &hostEmail,
		ScheduledOccurrenceID: occurrenceID,
	}
	if err := CreateRecordingData(ctx, p.pool, &rec); err != nil {
		log.Printf("zoom/webhook: failed to create recording data: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return nil, fmt.Errorf("create recording data: %w", err)
	}

	// Respond immediately — Zoom requires a fast ACK.
	w.WriteHeader(http.StatusOK)

	// Fetch full meeting detail from Zoom API to get is_private.
	// Done after ACK so the API call doesn't delay Zoom's response.
	// Fail open: if the call fails, default to not private so recordings aren't silently dropped.
	isPrivate := false
	if token, err := AccessToken(ctx, p.pool, p.encKey); err != nil {
		log.Printf("zoom/webhook: get access token for meeting %d: %v — assuming not private", meeting.ID, err)
	} else if detail, err := fetchMeetingDetail(ctx, token, meeting.ID); err != nil {
		log.Printf("zoom/webhook: fetch meeting detail for %d: %v — assuming not private", meeting.ID, err)
	} else {
		isPrivate = detail.Settings.PrivateMeeting
	}

	// Collect transcript URL and all files for downstream actions.
	var transcriptURL string
	if t := selectTranscriptFile(meeting.RecordingFiles); t != nil {
		transcriptURL = t.DownloadURL
	}

	var allFiles []platform.RecordingFile
	for _, f := range meeting.RecordingFiles {
		if f.Status == "completed" && f.FileSize > 0 {
			allFiles = append(allFiles, platform.RecordingFile{
				RecordingType: f.RecordingType,
				FileType:      f.FileType,
				FileName:      f.FileName,
				DownloadURL:   f.DownloadURL,
				FileSize:      f.FileSize,
			})
		}
	}

	return map[string]any{
		"job_id":           job.ID,
		"meeting_uuid":     meeting.UUID,
		"meeting_id":       meeting.ID,
		"meeting_topic":    meeting.Topic,
		"meeting_date":     meeting.StartTime,
		"host_email":       meeting.HostEmail,
		"duration_minutes": meeting.Duration,
		"duration_secs":    durationSecs,
		"occurrence_id":    meeting.OccurrenceID,
		"integration_id":   pkgIntegrationID,
		"download_url":     file.DownloadURL,
		"download_token":   evt.DownloadToken,
		"file_size":        file.FileSize,
		"transcript_url":   transcriptURL,
		"all_files":        allFiles,
		"is_private":       isPrivate,
	}, nil
}
