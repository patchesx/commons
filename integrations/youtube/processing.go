package youtube

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	ytapi "google.golang.org/api/youtube/v3"

	"commons/platform"
	"commons/plugin"
	"commons/store"
)

const (
	processingPollInterval = 60 * time.Second
	processingMaxWait      = 2 * time.Hour
)

// ResumeProcessingJobs queries for jobs stuck in "youtube_processing" phase and resumes
// polling for each. Called once at startup to recover from a server restart during transcoding.
func ResumeProcessingJobs(ctx context.Context, pool *pgxpool.Pool, encKey []byte) {
	jobs, err := store.ListJobsByPhase(ctx, pool, "youtube_processing")
	if err != nil {
		log.Printf("youtube: startup recovery — list youtube_processing jobs: %v", err)
		return
	}
	if len(jobs) == 0 {
		return
	}
	log.Printf("youtube: startup recovery — resuming %d job(s) stuck in youtube_processing", len(jobs))
	for i := range jobs {
		job := &jobs[i]
		uploadData, err := GetUploadData(ctx, pool, job.ID)
		if err != nil || uploadData == nil || uploadData.VideoID == nil || *uploadData.VideoID == "" {
			log.Printf("youtube: startup recovery — job %s has no video_id, failing", job.ID)
			rec := recordingMetaFromUploadData(uploadData)
			markProcessingFailed(ctx, pool, job, rec, fmt.Errorf("resumed from youtube_processing state but no video_id recorded"))
			continue
		}
		rec := recordingMetaFromUploadData(uploadData)
		videoID := *uploadData.VideoID
		go waitForProcessing(ctx, pool, encKey, job, rec, videoID)
	}
}

// recordingMetaFromUploadData builds a platform.RecordingMeta from stored upload data.
// Returns an empty struct (not nil) if uploadData is nil — safe for notifications.
func recordingMetaFromUploadData(d *UploadData) *platform.RecordingMeta {
	if d == nil {
		return &platform.RecordingMeta{}
	}
	return &platform.RecordingMeta{
		MeetingTopic: d.MeetingTopic,
		MeetingDate:  d.MeetingDate,
		DurationSecs: d.DurationSecs,
	}
}

// waitForProcessing polls the YouTube Data API until the video finishes transcoding,
// the 2-hour timeout elapses, or ctx is cancelled. Runs synchronously — callers should
// already be in a background goroutine.
func waitForProcessing(ctx context.Context, pool *pgxpool.Pool, encKey []byte, job *store.Job, rec *platform.RecordingMeta, videoID string) {
	svc, err := newYouTubeService(ctx, pool, encKey)
	if err != nil {
		log.Printf("youtube/processing: job %s — build YouTube client: %v", job.ID, err)
		markProcessingFailed(ctx, pool, job, rec, fmt.Errorf("build YouTube client for poll: %w", err))
		return
	}

	deadline := time.Now().Add(processingMaxWait)
	ticker := time.NewTicker(processingPollInterval)
	defer ticker.Stop()

	log.Printf("youtube/processing: job %s — polling video %s", job.ID, videoID)

	for {
		select {
		case <-ctx.Done():
			log.Printf("youtube/processing: job %s — context cancelled during poll", job.ID)
			return

		case <-ticker.C:
			status, err := getProcessingStatus(ctx, svc, videoID)
			if err != nil {
				log.Printf("youtube/processing: job %s — poll error (will retry): %v", job.ID, err)
				if time.Now().After(deadline) {
					log.Printf("youtube/processing: job %s — timed out with last poll error: %v", job.ID, err)
					onProcessingTimeout(ctx, pool, encKey, job, rec, videoID)
					return
				}
				continue
			}

			log.Printf("youtube/processing: job %s — video %s status: %s", job.ID, videoID, status)

			switch status {
			case "succeeded":
				onProcessingSucceeded(ctx, pool, encKey, job, rec, videoID)
				return

			case "failed", "terminated":
				markProcessingFailed(ctx, pool, job, rec, fmt.Errorf("YouTube processing status: %s", status))
				return

			default: // "processing" or unknown — keep waiting
				if time.Now().After(deadline) {
					log.Printf("youtube/processing: job %s — 2-hour timeout, falling back to complete", job.ID)
					onProcessingTimeout(ctx, pool, encKey, job, rec, videoID)
					return
				}
			}
		}
	}
}

// getProcessingStatus returns the YouTube processingStatus for the given video ID.
// Returns "succeeded" if processingDetails is absent — the video is accessible.
func getProcessingStatus(ctx context.Context, svc *ytapi.Service, videoID string) (string, error) {
	resp, err := svc.Videos.List([]string{"processingDetails"}).Id(videoID).Context(ctx).Do()
	if err != nil {
		return "", fmt.Errorf("videos.list: %w", err)
	}
	if len(resp.Items) == 0 {
		return "", fmt.Errorf("video %s not found in API response", videoID)
	}
	if resp.Items[0].ProcessingDetails == nil {
		return "succeeded", nil
	}
	return resp.Items[0].ProcessingDetails.ProcessingStatus, nil
}

func onProcessingSucceeded(ctx context.Context, pool *pgxpool.Pool, encKey []byte, job *store.Job, rec *platform.RecordingMeta, videoID string) {
	if err := store.CompleteJob(ctx, pool, job.ID); err != nil {
		log.Printf("youtube/processing: job %s — CompleteJob: %v", job.ID, err)
	}
	if err := store.ClearJobPhase(ctx, pool, job.ID); err != nil {
		log.Printf("youtube/processing: job %s — ClearJobPhase: %v", job.ID, err)
	}
	log.Printf("youtube/processing: job %s — complete (video %s)", job.ID, videoID)

	title := buildTitle(ctx, pool, encKey, *rec)
	if err := autoCreateResource(ctx, pool, encKey, rec, title, videoID); err != nil {
		log.Printf("youtube/processing: job %s — autoCreateResource: %v", job.ID, err)
	}

	data := map[string]any{
		"title":        title,
		"youtube_url":  "https://youtu.be/" + videoID,
		"video_id":     videoID,
		"meeting_date": rec.MeetingDate.Format("January 2, 2006"),
	}
	if rec.DurationSecs != nil {
		data["duration_mins"] = fmt.Sprintf("%d", *rec.DurationSecs/60)
	}
	if err := plugin.Fire(ctx, "youtube.upload_complete", job.ID, data); err != nil {
		log.Printf("youtube/processing: job %s — fire upload_complete: %v", job.ID, err)
	}
}

func onProcessingTimeout(ctx context.Context, pool *pgxpool.Pool, encKey []byte, job *store.Job, rec *platform.RecordingMeta, videoID string) {
	if err := store.CompleteJob(ctx, pool, job.ID); err != nil {
		log.Printf("youtube/processing: job %s — CompleteJob (timeout): %v", job.ID, err)
	}
	if err := store.ClearJobPhase(ctx, pool, job.ID); err != nil {
		log.Printf("youtube/processing: job %s — ClearJobPhase (timeout): %v", job.ID, err)
	}

	title := buildTitle(ctx, pool, encKey, *rec)
	if err := autoCreateResource(ctx, pool, encKey, rec, title, videoID); err != nil {
		log.Printf("youtube/processing: job %s — autoCreateResource (timeout): %v", job.ID, err)
	}
}

func markProcessingFailed(ctx context.Context, pool *pgxpool.Pool, job *store.Job, rec *platform.RecordingMeta, err error) {
	log.Printf("youtube/processing: job %s failed: %v", job.ID, err)
	if dbErr := store.FailJob(ctx, pool, job.ID, err.Error()); dbErr != nil {
		log.Printf("youtube/processing: job %s — FailJob: %v", job.ID, dbErr)
	}
	if phaseErr := store.ClearJobPhase(ctx, pool, job.ID); phaseErr != nil {
		log.Printf("youtube/processing: job %s — ClearJobPhase on fail: %v", job.ID, phaseErr)
	}
	errMsg := err.Error()

	if fireErr := plugin.Fire(ctx, "youtube.upload_failed", job.ID, map[string]any{
		"title":         rec.MeetingTopic,
		"error_message": errMsg,
		"meeting_date":  rec.MeetingDate.Format("January 2, 2006"),
	}); fireErr != nil {
		log.Printf("youtube/processing: job %s — fire upload_failed: %v", job.ID, fireErr)
	}
}
