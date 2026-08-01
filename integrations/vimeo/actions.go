package vimeo

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"commons/platform"
	"commons/plugin"
	"commons/store"
)

// UploadVideoAction implements plugin.ActionType for "vimeo.upload_video".
type UploadVideoAction struct {
	pool   *pgxpool.Pool
	encKey []byte
	pctx   plugin.PluginContext
}

func (a *UploadVideoAction) ID() string    { return "vimeo.upload_video" }
func (a *UploadVideoAction) Label() string { return "Upload to Vimeo" }
func (a *UploadVideoAction) RequiredCapabilities() []string {
	return []string{"vimeo.uploads"}
}
func (a *UploadVideoAction) OutputSchema() []plugin.DataFieldDef {
	return []plugin.DataFieldDef{
		{Key: "vimeo_url", Label: "Vimeo Video URL", Type: "string",
			Description: "Full URL to the uploaded Vimeo video."},
		{Key: "video_id", Label: "Vimeo Video ID", Type: "string",
			Description: "Vimeo numeric video ID."},
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
		{Key: "meeting_topic", Label: "Meeting Topic", Type: "text", Dynamic: true,
			Description: "Used to build the video title. Use {{meeting_topic}}."},
		{Key: "privacy_status", Label: "Privacy", Type: "select", Default: "unlisted",
			Options: []plugin.SelectOption{
				{Value: "unlisted", Label: "Unlisted"},
				{Value: "anybody", Label: "Public"},
				{Value: "nobody", Label: "Private"},
			},
		},
	}
}

func (a *UploadVideoAction) Execute(ctx context.Context, params map[string]any, ac plugin.ActionContext) (map[string]any, error) {
	downloadURL, _ := params["download_url"].(string)
	downloadToken, _ := params["download_token"].(string)
	meetingTopic, _ := params["meeting_topic"].(string)
	hostEmail, _ := params["host_email"].(string)
	jobID, _ := params["job_id"].(string)

	privacyStatus, _ := params["privacy_status"].(string)
	if privacyStatus == "" {
		privacyStatus = "unlisted"
	}

	// Parse file_size — may arrive as string (template-resolved) or numeric.
	var fileSize int64
	switch v := params["file_size"].(type) {
	case int64:
		fileSize = v
	case int:
		fileSize = int64(v)
	case string:
		if v != "" {
			fileSize, _ = strconv.ParseInt(v, 10, 64)
		}
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
		return nil, fmt.Errorf("vimeo.upload_video: download_url is required")
	}
	if downloadToken == "" {
		return nil, fmt.Errorf("vimeo.upload_video: download_token is required")
	}

	setPhase := func(phase string) {
		if err := ac.SetPhase(ctx, phase); err != nil {
			log.Printf("vimeo.upload_video: set phase %q: %v", phase, err)
		}
	}

	rec := platform.RecordingMeta{
		MeetingTopic: meetingTopic,
		MeetingDate:  meetingDate,
		DurationSecs: &durationSecs,
		HostEmail:    &hostEmail,
	}

	title := buildTitle(ctx, a.pool, a.encKey, rec)
	description := buildDescription(ctx, a.pool, a.encKey, rec)

	if jobID != "" {
		vimeoInteg, _ := store.GetIntegrationByType(ctx, a.pool, "vimeo")
		vimeoIntegID := ""
		if vimeoInteg != nil {
			vimeoIntegID = vimeoInteg.ID
		}
		if err := CreateUploadData(ctx, a.pool, &UploadData{
			JobID:         jobID,
			IntegrationID: vimeoIntegID,
			Title:         title,
			MeetingTopic:  meetingTopic,
			MeetingDate:   meetingDate,
			DurationSecs:  &durationSecs,
		}); err != nil {
			log.Printf("vimeo.upload_video: create upload data: %v", err)
		}
	}

	streamer := a.pctx.RecordingStreamer()
	if streamer == nil {
		return nil, fmt.Errorf("vimeo.upload_video: recording streamer not configured")
	}

	setPhase("uploading")

	reader, err := streamer.Stream(ctx, downloadURL, downloadToken)
	if err != nil {
		return nil, fmt.Errorf("vimeo.upload_video: stream from recording source: %w", err)
	}
	defer reader.Close()

	videoID, videoURL, err := UploadVideo(ctx, a.pool, a.encKey, reader, fileSize, title, description, privacyStatus)
	if err != nil {
		return nil, fmt.Errorf("vimeo.upload_video: %w", err)
	}

	if jobID != "" {
		if err := SetVideoID(ctx, a.pool, jobID, videoID); err != nil {
			log.Printf("vimeo.upload_video: set video ID: %v", err)
		}
	}

	if err := ac.ClearPhase(ctx); err != nil {
		log.Printf("vimeo.upload_video: clear phase: %v", err)
	}

	if err := autoCreateResource(ctx, a.pool, a.encKey, &platform.RecordingMeta{
		MeetingTopic: meetingTopic,
		MeetingDate:  meetingDate,
		DurationSecs: &durationSecs,
	}, title, videoID); err != nil {
		log.Printf("vimeo.upload_video: autoCreateResource (non-fatal): %v", err)
	}

	return map[string]any{
		"vimeo_url": videoURL,
		"video_id":  videoID,
	}, nil
}
