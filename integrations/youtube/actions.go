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

// UploadVideoAction implements plugin.ActionType for "youtube.upload_video".
type UploadVideoAction struct {
	pool   *pgxpool.Pool
	encKey []byte
	pctx   plugin.PluginContext
}

func (a *UploadVideoAction) ID() string    { return "youtube.upload_video" }
func (a *UploadVideoAction) Label() string { return "Upload to YouTube" }
func (a *UploadVideoAction) RequiredCapabilities() []string {
	return []string{"youtube.uploads"}
}
func (a *UploadVideoAction) OutputSchema() []plugin.DataFieldDef {
	return []plugin.DataFieldDef{
		{Key: "youtube_url", Label: "YouTube Video URL", Type: "string",
			Description: "Full URL to the uploaded YouTube video."},
		{Key: "video_id", Label: "YouTube Video ID", Type: "string",
			Description: "YouTube video ID (the part after youtu.be/)."},
	}
}
func (a *UploadVideoAction) ParamSchema() []plugin.ParamDef {
	return []plugin.ParamDef{
		{Key: "download_url", Label: "Download URL", Type: "text", Required: true, Dynamic: true,
			Description: "Zoom recording download URL. Use {{download_url}}."},
		{Key: "download_token", Label: "Download Token", Type: "text", Required: true, Dynamic: true,
			Description: "Zoom download authentication token. Use {{download_token}}."},
		{Key: "file_size", Label: "File Size", Type: "text", Dynamic: true,
			Description: "Recording file size in bytes. Use {{file_size}}."},
		{Key: "transcript_url", Label: "Transcript URL", Type: "text", Dynamic: true,
			Description: "Zoom VTT transcript URL. Use {{transcript_url}}."},
		{Key: "meeting_topic", Label: "Meeting Topic", Type: "text", Dynamic: true,
			Description: "Used to build the video title. Use {{meeting_topic}}."},
		{Key: "privacy_status", Label: "Privacy", Type: "select", Default: "unlisted",
			Options: []plugin.SelectOption{
				{Value: "unlisted", Label: "Unlisted"},
				{Value: "public", Label: "Public"},
				{Value: "private", Label: "Private"},
			},
		},
		{Key: "made_for_kids", Label: "Made for Kids", Type: "boolean", Default: false},
	}
}

func (a *UploadVideoAction) Execute(ctx context.Context, params map[string]any, ac plugin.ActionContext) (map[string]any, error) {
	downloadURL, _ := params["download_url"].(string)
	downloadToken, _ := params["download_token"].(string)
	transcriptURL, _ := params["transcript_url"].(string)
	meetingTopic, _ := params["meeting_topic"].(string)
	hostEmail, _ := params["host_email"].(string)
	jobID, _ := params["job_id"].(string)

	privacyStatus, _ := params["privacy_status"].(string)
	if privacyStatus == "" {
		privacyStatus = "unlisted"
	}

	var meetingDate time.Time
	if t, ok := params["meeting_date"].(time.Time); ok {
		meetingDate = t
	}

	var durationSecs int
	switch v := params["duration_secs"].(type) {
	case int:
		durationSecs = v
	case int64:
		durationSecs = int(v)
	}

	if downloadURL == "" {
		return nil, fmt.Errorf("youtube.upload_video: download_url is required")
	}
	if downloadToken == "" {
		return nil, fmt.Errorf("youtube.upload_video: download_token is required")
	}

	setPhase := func(phase string) {
		if err := ac.SetPhase(ctx, phase); err != nil {
			log.Printf("youtube.upload_video: set phase %q: %v", phase, err)
		}
	}

	// Create UploadData row before the upload (stores title, job→video link, meeting metadata).
	title := buildTitle(ctx, a.pool, a.encKey, platform.RecordingMeta{
		MeetingTopic: meetingTopic,
		MeetingDate:  meetingDate,
	})
	description := buildDescription(ctx, a.pool, a.encKey, platform.RecordingMeta{
		MeetingTopic: meetingTopic,
		MeetingDate:  meetingDate,
		DurationSecs: &durationSecs,
		HostEmail:    &hostEmail,
	})

	if jobID != "" {
		ytInteg, _ := store.GetIntegrationByType(ctx, a.pool, "youtube")
		ytIntegID := ""
		if ytInteg != nil {
			ytIntegID = ytInteg.ID
		}
		if err := CreateUploadData(ctx, a.pool, &UploadData{
			JobID:         jobID,
			IntegrationID: ytIntegID,
			Title:         title,
			MeetingTopic:  meetingTopic,
			MeetingDate:   meetingDate,
			DurationSecs:  &durationSecs,
		}); err != nil {
			log.Printf("youtube.upload_video: create upload data: %v", err)
		}
	}

	streamer := a.pctx.RecordingStreamer()
	if streamer == nil {
		return nil, fmt.Errorf("youtube.upload_video: recording streamer not configured")
	}

	setPhase("downloading")

	// Build the YouTube service.
	svc, err := newYouTubeService(ctx, a.pool, a.encKey)
	if err != nil {
		return nil, fmt.Errorf("youtube.upload_video: build YouTube client: %w", err)
	}

	// Stream from recording source.
	reader, err := streamer.Stream(ctx, downloadURL, downloadToken)
	if err != nil {
		return nil, fmt.Errorf("youtube.upload_video: stream from recording source: %w", err)
	}
	defer reader.Close()

	setPhase("uploading")

	// Upload to YouTube.
	video := &ytapi.Video{
		Snippet: &ytapi.VideoSnippet{
			Title:       title,
			Description: description,
		},
		Status: &ytapi.VideoStatus{
			PrivacyStatus: privacyStatus,
		},
	}
	call := svc.Videos.Insert([]string{"snippet", "status"}, video)
	call.Media(reader)
	result, err := call.Do()
	if err != nil {
		return nil, fmt.Errorf("youtube.upload_video: YouTube upload: %w", err)
	}
	videoID := result.Id

	// Persist video ID.
	if jobID != "" {
		if err := SetVideoID(ctx, a.pool, jobID, videoID); err != nil {
			log.Printf("youtube.upload_video: set video ID: %v", err)
		}
	}

	// Upload captions if available (non-fatal).
	if transcriptURL != "" {
		if err := uploadCaptions(ctx, streamer, svc, videoID, transcriptURL, downloadToken); err != nil {
			log.Printf("youtube.upload_video: caption upload (non-fatal): %v", err)
		}
	}

	setPhase("youtube_processing")
	log.Printf("youtube.upload_video: upload complete, polling video %s", videoID)

	// Poll until YouTube finishes transcoding (blocks).
	if err := pollProcessing(ctx, svc, videoID); err != nil {
		return nil, fmt.Errorf("youtube.upload_video: processing failed: %w", err)
	}

	if err := ac.ClearPhase(ctx); err != nil {
		log.Printf("youtube.upload_video: clear phase: %v", err)
	}

	// Create resource library entry (non-fatal).
	if err := autoCreateResource(ctx, a.pool, a.encKey, &platform.RecordingMeta{
		MeetingTopic: meetingTopic,
		MeetingDate:  meetingDate,
		DurationSecs: &durationSecs,
	}, title, videoID); err != nil {
		log.Printf("youtube.upload_video: autoCreateResource (non-fatal): %v", err)
	}

	// Fire event so Triggers pipelines (Slack channel, Discord DM, etc.) can react.
	youtubeURL := "https://youtu.be/" + videoID
	fireData := map[string]any{
		"title":        title,
		"youtube_url":  youtubeURL,
		"video_id":     videoID,
		"meeting_date": meetingDate.Format("January 2, 2006"),
	}
	if durationSecs > 0 {
		fireData["duration_mins"] = fmt.Sprintf("%d", durationSecs/60)
	}
	if err := plugin.Fire(ctx, "youtube.upload_complete", jobID, fireData); err != nil {
		log.Printf("youtube.upload_video: fire upload_complete: %v", err)
	}

	return map[string]any{
		"youtube_url": youtubeURL,
		"video_id":    videoID,
	}, nil
}

// pollProcessing polls YouTube until the video finishes processing or the
// 2-hour timeout elapses. Returns nil on success or timeout; returns error
// on failure status or context cancellation. Does not touch job state.
func pollProcessing(ctx context.Context, svc *ytapi.Service, videoID string) error {
	deadline := time.Now().Add(processingMaxWait)
	ticker := time.NewTicker(processingPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			status, err := getProcessingStatus(ctx, svc, videoID)
			if err != nil {
				if time.Now().After(deadline) {
					return nil // treat timeout as accessible
				}
				continue
			}
			switch status {
			case "succeeded":
				return nil
			case "failed", "terminated":
				return fmt.Errorf("YouTube processing status: %s", status)
			default:
				if time.Now().After(deadline) {
					return nil // timeout — treat as accessible
				}
			}
		}
	}
}
