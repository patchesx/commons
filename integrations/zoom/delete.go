package zoom

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"commons/store"
)

// CheckZoomS2SConfig returns an error if any of the three S2S OAuth credentials are
// missing from config_store. Use this before calling DeleteRecording in contexts where
// missing credentials should surface as an error (e.g. manual delete from the UI)
// rather than a silent no-op (the auto-delete path).
func CheckZoomS2SConfig(ctx context.Context, pool *pgxpool.Pool, encKey []byte) error {
	for _, key := range []string{"account_id", "api_client_id", "api_client_secret"} {
		_, err := store.GetServiceConfig(ctx, pool, "zoom", key, encKey)
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("zoom/%s is not configured — set it on the Config page", key)
		}
		if err != nil {
			return fmt.Errorf("read zoom/%s: %w", key, err)
		}
	}
	return nil
}

// fetchS2SToken exchanges Zoom Server-to-Server OAuth credentials for a short-lived
// access token. We fetch a fresh token per call — no caching needed given how
// infrequently this fires (once per recording upload).
func fetchS2SToken(ctx context.Context, accountID, clientID, clientSecret string) (string, error) {
	endpoint := fmt.Sprintf(
		"https://zoom.us/oauth/token?grant_type=account_credentials&account_id=%s",
		url.QueryEscape(accountID),
	)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
	if err != nil {
		return "", fmt.Errorf("build token request: %w", err)
	}
	req.SetBasicAuth(clientID, clientSecret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("token request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read token response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("zoom token exchange returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var result struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("parse token response: %w", err)
	}
	if result.AccessToken == "" {
		return "", fmt.Errorf("zoom token exchange returned empty access_token")
	}
	return result.AccessToken, nil
}

// DeleteRecording trashes the Zoom cloud recording for meetingUUID using
// Server-to-Server OAuth credentials from config_store.
//
// Returns nil (silent no-op) if any of the three credential keys are missing.
// Returns nil on 204 No Content (success) or 404 Not Found (already gone).
// Does NOT check the delete_after_upload toggle — callers decide when to invoke this.
// Callers must treat non-nil errors as non-fatal.
func DeleteRecording(ctx context.Context, pool *pgxpool.Pool, meetingUUID string, encKey []byte) error {
	accountID, err := store.GetServiceConfig(ctx, pool, "zoom", "account_id", encKey)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read zoom/account_id: %w", err)
	}

	clientID, err := store.GetServiceConfig(ctx, pool, "zoom", "api_client_id", encKey)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read zoom/api_client_id: %w", err)
	}

	clientSecret, err := store.GetServiceConfig(ctx, pool, "zoom", "api_client_secret", encKey)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read zoom/api_client_secret: %w", err)
	}

	log.Printf("zoom/delete: attempting to trash recording for meeting %s", meetingUUID)

	accessToken, err := fetchS2SToken(ctx, accountID, clientID, clientSecret)
	if err != nil {
		return fmt.Errorf("fetch S2S token: %w", err)
	}
	log.Printf("zoom/delete: token exchange succeeded")

	// Zoom API requires double-encoding only when the UUID starts with '/' or contains '//'.
	// All other UUIDs (including those with a single '/' or '+' in the middle) need single encoding.
	encodedUUID := url.PathEscape(meetingUUID)
	if strings.HasPrefix(meetingUUID, "/") || strings.Contains(meetingUUID, "//") {
		encodedUUID = url.PathEscape(encodedUUID)
	}
	deleteURL := fmt.Sprintf("https://api.zoom.us/v2/meetings/%s/recordings?action=trash", encodedUUID)

	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, deleteURL, nil)
	if err != nil {
		return fmt.Errorf("build delete request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("delete request failed: %w", err)
	}
	defer resp.Body.Close()

	// 204 = success. 404 = already gone (manual delete or retention policy) — treat as success.
	if resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusNotFound {
		log.Printf("zoom/delete: recording %s trashed (status %d)", meetingUUID, resp.StatusCode)
		return nil
	}

	body, _ := io.ReadAll(resp.Body)
	log.Printf("zoom/delete: API returned %d for meeting %s: %s", resp.StatusCode, meetingUUID, strings.TrimSpace(string(body)))
	return fmt.Errorf("zoom delete returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
}
