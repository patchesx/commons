package vimeo

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5/pgxpool"

	"commons/platform"
	"commons/store"
	"commons/util"
)

// UploadVideo streams r to the Vimeo upload link obtained from initiateUpload.
// Returns the Vimeo video ID and URL on success.
func UploadVideo(ctx context.Context, pool *pgxpool.Pool, encKey []byte,
	r io.Reader, sizeBytes int64, title, description string, privacyView string,
) (videoID string, videoURL string, err error) {
	token, err := getAccessToken(ctx, pool, encKey)
	if err != nil {
		return "", "", fmt.Errorf("get access token: %w", err)
	}

	res, err := initiateUpload(ctx, token, title, description, sizeBytes, privacyView)
	if err != nil {
		return "", "", fmt.Errorf("initiate upload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "PUT", res.uploadLink, r)
	if err != nil {
		return "", "", fmt.Errorf("create PUT request: %w", err)
	}
	req.Header.Set("Content-Type", "video/mp4")
	req.ContentLength = sizeBytes
	req.Header.Set("Content-Length", strconv.FormatInt(sizeBytes, 10))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("PUT upload: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", "", fmt.Errorf("PUT upload: status %d", resp.StatusCode)
	}

	return res.videoID, "https://vimeo.com/" + res.videoID, nil
}

// buildTitle returns the video title in the format "Topic — January 2, 2006".
func buildTitle(ctx context.Context, pool *pgxpool.Pool, encKey []byte, rec platform.RecordingMeta) string {
	localDate := util.LocalizeMeetingDate(ctx, pool, encKey, rec.MeetingDate)
	return fmt.Sprintf("%s \u2014 %s", rec.MeetingTopic, localDate.Format("January 2, 2006"))
}

// buildDescription returns the video description.
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

// autoCreateResource inserts a "Meeting Recording" resource into resource_library
// after a successful Vimeo upload. Errors are non-fatal.
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
		URL:         fmt.Sprintf("https://vimeo.com/%s", videoID),
		Category:    "Meeting Recording",
		Description: &desc,
	}
	return store.CreateResource(ctx, pool, resource)
}
