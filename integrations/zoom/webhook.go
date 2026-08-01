package zoom

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"commons/platform"
	"commons/store"
)

// RecordingJob carries everything the upload pipeline needs for a new recording.
type RecordingJob struct {
	Job           *store.Job
	Recording     RecordingData // Zoom meeting metadata
	DownloadURL   string        // best MP4, for YouTube
	DownloadToken string
	FileSize      int64                    // best MP4 size, for YouTube
	TranscriptURL string                   // empty if Zoom did not produce a transcript
	AllFiles      []platform.RecordingFile // all completed files, for Drive backup
}

// Processor is invoked in a background goroutine for each new, validated recording.
type Processor func(ctx context.Context, pool *pgxpool.Pool, rj RecordingJob)

// Zoom event types.
const (
	eventURLValidation      = "endpoint.url_validation"
	eventRecordingCompleted = "recording.completed"
)

// Zoom webhook JSON structures.
type zoomEvent struct {
	Event         string      `json:"event"`
	DownloadToken string      `json:"download_token"`
	Payload       zoomPayload `json:"payload"`
}

type zoomPayload struct {
	Object zoomMeeting `json:"object"`
}

type zoomMeeting struct {
	UUID           string              `json:"uuid"`
	ID             int64               `json:"id"`            // numeric Zoom meeting ID; stable across occurrences
	OccurrenceID   string              `json:"occurrence_id"` // set for recurring instances; empty for one-offs
	Topic          string              `json:"topic"`
	StartTime      time.Time           `json:"start_time"`
	Duration       int                 `json:"duration"` // minutes
	HostEmail      string              `json:"host_email"`
	RecordingFiles []zoomRecordingFile `json:"recording_files"`
}

type zoomRecordingFile struct {
	RecordingType string `json:"recording_type"`
	FileType      string `json:"file_type"`
	FileName      string `json:"file_name"`
	DownloadURL   string `json:"download_url"`
	FileSize      int64  `json:"file_size"`
	Status        string `json:"status"`
}

// Handler returns an http.HandlerFunc that handles Zoom webhook events.
// integrationID is the integrations.id for this Zoom account instance.
// processor is called in a background goroutine for each valid new recording.
func Handler(pool *pgxpool.Pool, encKey []byte, integrationID string, processor Processor) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		// Read body once — needed for both HMAC verification and JSON decode.
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "failed to read body", http.StatusInternalServerError)
			return
		}

		// Decode just enough to check the event type before signature verification,
		// since URL validation challenges don't carry a signature.
		var evt zoomEvent
		if err := json.Unmarshal(body, &evt); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}

		// Fetch the webhook secret from config_store.
		secret, err := store.GetServiceConfig(ctx, pool, "zoom", "webhook_secret", encKey)
		if errors.Is(err, store.ErrNotFound) {
			log.Printf("zoom/webhook: webhook_secret not configured in config_store")
			http.Error(w, "server misconfigured", http.StatusInternalServerError)
			return
		}
		if err != nil {
			log.Printf("zoom/webhook: failed to read webhook_secret: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		// URL validation challenge — no signature on these.
		if evt.Event == eventURLValidation {
			handleURLChallenge(w, evt.Payload.Object.UUID, secret, body)
			return
		}

		// Reject requests with a stale timestamp — prevents replay attacks.
		// Zoom recommends a 5-minute tolerance window.
		timestamp := r.Header.Get("x-zm-request-timestamp")
		ts, tsErr := strconv.ParseInt(timestamp, 10, 64)
		if tsErr != nil || time.Now().Unix()-ts > 300 {
			log.Printf("zoom/webhook: stale or missing timestamp")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		// Verify HMAC-SHA256 signature on all other requests.
		sigHeader := r.Header.Get("x-zm-signature")
		if !verifySignature(secret, timestamp, string(body), sigHeader) {
			log.Printf("zoom/webhook: invalid signature")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		// Only process recording completions.
		if evt.Event != eventRecordingCompleted {
			w.WriteHeader(http.StatusOK)
			return
		}

		meeting := evt.Payload.Object

		// Dedup — if we've already seen this meeting UUID, acknowledge and stop.
		existing, err := GetByMeetingUUID(ctx, pool, meeting.UUID)
		if err != nil {
			log.Printf("zoom/webhook: dedup check failed: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if existing != nil {
			log.Printf("zoom/webhook: duplicate meeting UUID %s — skipping", meeting.UUID)
			w.WriteHeader(http.StatusOK)
			return
		}

		// Find the best available MP4.
		file := selectRecordingFile(meeting.RecordingFiles)
		if file == nil {
			log.Printf("zoom/webhook: no completed MP4 found for meeting %s", meeting.UUID)
			w.WriteHeader(http.StatusOK)
			return
		}

		// Try to link this recording to a locally-scheduled occurrence.
		occurrenceID, err := FindOccurrenceForRecording(ctx, pool, integrationID, meeting.ID, meeting.OccurrenceID)
		if err != nil {
			log.Printf("zoom/webhook: occurrence lookup for meeting %d: %v", meeting.ID, err)
			// Non-fatal — proceed without the link.
		}

		// Check if the scheduled meeting has upload disabled.
		if skip, err := GetSkipUploadForMeeting(ctx, pool, integrationID, meeting.ID); err != nil {
			log.Printf("zoom/webhook: skip_upload check for meeting %d: %v", meeting.ID, err)
		} else if skip {
			log.Printf("zoom/webhook: skip_upload set for meeting %d — ignoring recording", meeting.ID)
			w.WriteHeader(http.StatusOK)
			return
		}

		// Create the generic job record.
		job := &store.Job{
			Type:    store.JobTypeRecordingUpload,
			Feature: store.JobFeatureRecordingPipeline,
			Trigger: store.JobTriggerWebhook,
			Status:  store.JobStatusPending,
		}
		if err := store.CreateJob(ctx, pool, job); err != nil {
			log.Printf("zoom/webhook: failed to create job: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		// Create the Zoom extension row.
		durationSecs := meeting.Duration * 60
		hostEmail := meeting.HostEmail
		rec := RecordingData{
			JobID:                 job.ID,
			IntegrationID:         integrationID,
			MeetingUUID:           meeting.UUID,
			MeetingTopic:          meeting.Topic,
			MeetingDate:           meeting.StartTime,
			DurationSecs:          &durationSecs,
			HostEmail:             &hostEmail,
			ScheduledOccurrenceID: occurrenceID,
		}
		if err := CreateRecordingData(ctx, pool, &rec); err != nil {
			log.Printf("zoom/webhook: failed to create recording data: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		// Respond immediately — Zoom expects a fast acknowledgement.
		w.WriteHeader(http.StatusOK)

		// Find a transcript (VTT) file if Zoom produced one.
		var transcriptURL string
		if t := selectTranscriptFile(meeting.RecordingFiles); t != nil {
			transcriptURL = t.DownloadURL
		}

		// Collect all completed files for Drive backup.
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

		// Process in the background with a fresh context independent of the request.
		go processor(context.Background(), pool, RecordingJob{
			Job:           job,
			Recording:     rec,
			DownloadURL:   file.DownloadURL,
			DownloadToken: evt.DownloadToken,
			FileSize:      file.FileSize,
			TranscriptURL: transcriptURL,
			AllFiles:      allFiles,
		})
	}
}

// handleURLChallenge responds to Zoom's endpoint.url_validation challenge.
// The plain token is in the UUID field of the challenge payload — we re-parse
// the raw body to extract it safely.
func handleURLChallenge(w http.ResponseWriter, _ string, secret string, body []byte) {
	// The plainToken lives at payload.plainToken in the challenge body.
	var challenge struct {
		Payload struct {
			PlainToken string `json:"plainToken"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(body, &challenge); err != nil {
		http.Error(w, "invalid challenge", http.StatusBadRequest)
		return
	}

	plainToken := challenge.Payload.PlainToken
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(plainToken))
	encrypted := hex.EncodeToString(mac.Sum(nil))

	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"plainToken":%q,"encryptedToken":%q}`, plainToken, encrypted)
}

// verifySignature validates the Zoom HMAC-SHA256 request signature.
// message format: "v0:{timestamp}:{body}"
// expected header format: "v0={hex-hmac}"
func verifySignature(secret, timestamp, body, sigHeader string) bool {
	if timestamp == "" || sigHeader == "" {
		return false
	}
	message := fmt.Sprintf("v0:%s:%s", timestamp, body)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(message))
	expected := "v0=" + hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(sigHeader))
}
