package vimeo

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"commons/store"
)

func getAccessToken(ctx context.Context, pool *pgxpool.Pool, encKey []byte) (string, error) {
	return store.GetServiceConfig(ctx, pool, "vimeo", "access_token", encKey)
}

type initiateResult struct {
	uploadLink string
	videoID    string
}

// initiateUpload creates the Vimeo video record and returns the one-time upload link.
func initiateUpload(ctx context.Context, token, title, description string, sizeBytes int64, privacyView string) (*initiateResult, error) {
	body := map[string]any{
		"name":        title,
		"description": description,
		"privacy":     map[string]string{"view": privacyView},
		"upload": map[string]any{
			"approach": "streaming",
			"size":     sizeBytes,
		},
	}

	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.vimeo.com/me/videos", bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/vnd.vimeo.*+json;version=3.4")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("initiate upload: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("initiate upload: status %d", resp.StatusCode)
	}

	var result struct {
		Link   string `json:"link"`
		Upload struct {
			UploadLink string `json:"upload_link"`
		} `json:"upload"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	// Parse video ID from link (e.g. "/videos/123456789" or "https://vimeo.com/123456789").
	videoID := result.Link
	if idx := strings.LastIndex(videoID, "/"); idx >= 0 {
		videoID = videoID[idx+1:]
	}

	return &initiateResult{
		uploadLink: result.Upload.UploadLink,
		videoID:    videoID,
	}, nil
}
