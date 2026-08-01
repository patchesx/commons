package youtube

import (
	"context"
	"errors"
	"fmt"
	"io"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/option"
	ytapi "google.golang.org/api/youtube/v3"

	"github.com/jackc/pgx/v5/pgxpool"

	gauth "commons/integrations/google"
	"commons/platform"
	"commons/store"
	"commons/util"
)

// autoCreateResource inserts a "Meeting Recording" resource into resource_library
// after a successful YouTube upload. Errors are non-fatal — the job is already complete.
func autoCreateResource(ctx context.Context, pool *pgxpool.Pool, encKey []byte, rec *platform.RecordingMeta, title, videoID string) error {
	localDate := util.LocalizeMeetingDate(ctx, pool, encKey, rec.MeetingDate)
	var desc string
	if rec.DurationSecs != nil {
		desc = fmt.Sprintf("%s · %d min", localDate.Format("January 2, 2006"), *rec.DurationSecs/60)
	} else {
		desc = localDate.Format("January 2, 2006")
	}
	resource := &store.Resource{
		Title:       title,
		URL:         fmt.Sprintf("https://youtu.be/%s", videoID),
		Category:    "Meeting Recording",
		Description: &desc,
	}
	return store.CreateResource(ctx, pool, resource)
}

// newYouTubeService builds an authenticated YouTube API service using credentials
// stored in config_store.
func newYouTubeService(ctx context.Context, pool *pgxpool.Pool, encKey []byte) (*ytapi.Service, error) {
	clientID, err := store.GetServiceConfig(ctx, pool, "google", "client_id", encKey)
	if errors.Is(err, store.ErrNotFound) {
		return nil, fmt.Errorf("google/client_id not configured in config_store")
	}
	if err != nil {
		return nil, fmt.Errorf("read client_id: %w", err)
	}

	clientSecret, err := store.GetServiceConfig(ctx, pool, "google", "client_secret", encKey)
	if errors.Is(err, store.ErrNotFound) {
		return nil, fmt.Errorf("google/client_secret not configured in config_store")
	}
	if err != nil {
		return nil, fmt.Errorf("read client_secret: %w", err)
	}

	refreshToken, err := store.GetServiceConfig(ctx, pool, "google", "refresh_token", encKey)
	if errors.Is(err, store.ErrNotFound) {
		return nil, fmt.Errorf("google/refresh_token not configured — connect via web UI")
	}
	if err != nil {
		return nil, fmt.Errorf("read refresh_token: %w", err)
	}

	oauthCfg := &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Endpoint:     google.Endpoint,
		Scopes:       gauth.Scopes,
	}

	tokenSource := oauthCfg.TokenSource(ctx, &oauth2.Token{RefreshToken: refreshToken})
	httpClient := oauth2.NewClient(ctx, tokenSource)

	svc, err := ytapi.NewService(ctx, option.WithHTTPClient(httpClient))
	if err != nil {
		return nil, fmt.Errorf("create YouTube service: %w", err)
	}
	return svc, nil
}

// insertCaptions attaches a VTT caption track to a YouTube video from any io.Reader.
func insertCaptions(ctx context.Context, svc *ytapi.Service, videoID string, reader io.Reader) error {
	caption := &ytapi.Caption{
		Snippet: &ytapi.CaptionSnippet{
			VideoId:   videoID,
			Language:  "en",
			TrackKind: "standard",
		},
	}

	call := svc.Captions.Insert([]string{"snippet"}, caption)
	call.Media(reader)

	if _, err := call.Do(); err != nil {
		return fmt.Errorf("insert captions: %w", err)
	}
	return nil
}

// uploadCaptions streams a VTT transcript and attaches it to a YouTube video.
// Errors are non-fatal — the video is already uploaded.
func uploadCaptions(ctx context.Context, streamer platform.RecordingStreamer, svc *ytapi.Service, videoID, transcriptURL, downloadToken string) error {
	reader, err := streamer.Stream(ctx, transcriptURL, downloadToken)
	if err != nil {
		return fmt.Errorf("stream transcript: %w", err)
	}
	defer reader.Close()
	return insertCaptions(ctx, svc, videoID, reader)
}

// InsertCaptions builds a YouTube service and attaches a VTT caption track from an io.Reader.
// Use this for manual uploads where the transcript file is already available locally.
func InsertCaptions(ctx context.Context, pool *pgxpool.Pool, encKey []byte, videoID string, reader io.Reader) error {
	svc, err := newYouTubeService(ctx, pool, encKey)
	if err != nil {
		return fmt.Errorf("build YouTube client: %w", err)
	}
	return insertCaptions(ctx, svc, videoID, reader)
}

// UploadVideo uploads a video stream to YouTube as unlisted and returns the video ID.
// It does not create job records or resource library entries — callers handle those.
func UploadVideo(ctx context.Context, pool *pgxpool.Pool, encKey []byte, reader io.Reader, title, description string) (string, error) {
	svc, err := newYouTubeService(ctx, pool, encKey)
	if err != nil {
		return "", fmt.Errorf("build YouTube client: %w", err)
	}

	video := &ytapi.Video{
		Snippet: &ytapi.VideoSnippet{
			Title:       title,
			Description: description,
		},
		Status: &ytapi.VideoStatus{
			PrivacyStatus: "unlisted",
		},
	}

	call := svc.Videos.Insert([]string{"snippet", "status"}, video)
	call.Media(reader)

	result, err := call.Do()
	if err != nil {
		return "", fmt.Errorf("YouTube upload: %w", err)
	}
	return result.Id, nil
}

// buildTitle returns the YouTube video title in the format "Topic — January 2, 2006".
func buildTitle(ctx context.Context, pool *pgxpool.Pool, encKey []byte, rec platform.RecordingMeta) string {
	localDate := util.LocalizeMeetingDate(ctx, pool, encKey, rec.MeetingDate)
	return fmt.Sprintf("%s \u2014 %s", rec.MeetingTopic, localDate.Format("January 2, 2006"))
}

// buildDescription returns the YouTube video description.
func buildDescription(ctx context.Context, pool *pgxpool.Pool, encKey []byte, rec platform.RecordingMeta) string {
	localDate := util.LocalizeMeetingDate(ctx, pool, encKey, rec.MeetingDate)
	desc := fmt.Sprintf("Meeting: %s\nDate: %s",
		rec.MeetingTopic,
		localDate.Format("January 2, 2006"),
	)
	if rec.DurationSecs != nil {
		desc += fmt.Sprintf("\nDuration: %d minutes", *rec.DurationSecs/60)
	}
	if rec.HostEmail != nil {
		desc += fmt.Sprintf("\nHost: %s", *rec.HostEmail)
	}
	return desc
}
