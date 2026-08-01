package zoom

import (
	"context"
	"fmt"
	"io"
	"net/http"
)

// selectRecordingFile picks the best available completed MP4 from a recording's file list.
// Preference order: shared_screen_with_speaker_view → shared_screen → first completed MP4.
func selectRecordingFile(files []zoomRecordingFile) *zoomRecordingFile {
	var fallback *zoomRecordingFile

	for i := range files {
		f := &files[i]
		if f.FileType != "MP4" || f.Status != "completed" {
			continue
		}
		switch f.RecordingType {
		case "shared_screen_with_speaker_view":
			return f // best option — return immediately
		case "shared_screen":
			fallback = f // good option — keep looking for the best
		default:
			if fallback == nil {
				fallback = f // any MP4 is better than nothing
			}
		}
	}

	return fallback
}

// selectTranscriptFile returns the completed TRANSCRIPT file from a recording's file list, if any.
func selectTranscriptFile(files []zoomRecordingFile) *zoomRecordingFile {
	for i := range files {
		f := &files[i]
		if f.FileType == "TRANSCRIPT" && f.Status == "completed" {
			return f
		}
	}
	return nil
}

// ZoomStreamer implements platform.RecordingStreamer for Zoom CDN downloads.
type ZoomStreamer struct{}

func (s *ZoomStreamer) Stream(ctx context.Context, downloadURL, downloadToken string) (io.ReadCloser, error) {
	return Stream(ctx, downloadURL, downloadToken)
}

// Stream opens a streaming HTTP connection to the Zoom CDN and returns the response body.
// The caller is responsible for closing the returned ReadCloser.
// The download token is sent as a Bearer token per Zoom's API requirements.
// No data is buffered to disk.
func Stream(ctx context.Context, downloadURL, downloadToken string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build download request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+downloadToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("zoom download request failed: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("zoom CDN returned status %d for %s", resp.StatusCode, downloadURL)
	}

	return resp.Body, nil
}
