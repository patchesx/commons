package zoom

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"commons/store"
)

// RecurrencePattern mirrors Zoom's recurrence object.
// Only the fields relevant to the chosen type need to be populated.
type RecurrencePattern struct {
	Type           int    `json:"type"` // 1=Daily 2=Weekly 3=Monthly
	RepeatInterval int    `json:"repeat_interval"`
	WeeklyDays     string `json:"weekly_days,omitempty"`      // comma-separated: 1=Sun 2=Mon 3=Tue 4=Wed 5=Thu 6=Fri 7=Sat
	MonthlyWeek    int    `json:"monthly_week,omitempty"`     // 1=first 2=second 3=third 4=fourth -1=last
	MonthlyWeekDay int    `json:"monthly_week_day,omitempty"` // same encoding as WeeklyDays
	MonthlyDay     int    `json:"monthly_day,omitempty"`      // 1–31 (monthly-by-date only)
	EndTimes       int    `json:"end_times"`                  // number of occurrences; Zoom max = 60
}

type MeetingParams struct {
	Topic      string
	Agenda     string
	StartTime  time.Time
	Duration   int // minutes
	Timezone   string
	Password   string
	Recurrence *RecurrencePattern // nil = one-off (Zoom type=2); non-nil = recurring (Zoom type=8)
	Settings   MeetingSettings
}

type MeetingSettings struct {
	JoinBeforeHost bool `json:"join_before_host"`
	PrivateMeeting bool `json:"private_meeting"`
}

type MeetingOccurrence struct {
	OccurrenceID string
	StartTime    time.Time
	Duration     int
	Status       string
}

type ZoomMeeting struct {
	ID          int64
	UUID        string
	HostEmail   string
	Topic       string
	Agenda      string
	StartTime   time.Time // first (or only) occurrence start time
	Duration    int
	Timezone    string
	JoinURL     string
	StartURL    string
	Password    string
	IsRecurring bool
	Occurrences []MeetingOccurrence // for recurring: from Zoom response; for one-off: single synthesized entry
	Settings    MeetingSettings
}

// ScheduleMeeting creates a Zoom meeting (one-off or recurring) using S2S OAuth credentials
// stored in config_store. Returns a populated ZoomMeeting or an error.
func ScheduleMeeting(ctx context.Context, pool *pgxpool.Pool, encKey []byte, params MeetingParams) (*ZoomMeeting, error) {
	accountID, err := store.GetServiceConfig(ctx, pool, "zoom", "account_id", encKey)
	if errors.Is(err, store.ErrNotFound) {
		return nil, fmt.Errorf("zoom/account_id is not configured — set it on the Config page")
	}
	if err != nil {
		return nil, fmt.Errorf("read zoom/account_id: %w", err)
	}

	clientID, err := store.GetServiceConfig(ctx, pool, "zoom", "api_client_id", encKey)
	if errors.Is(err, store.ErrNotFound) {
		return nil, fmt.Errorf("zoom/api_client_id is not configured — set it on the Config page")
	}
	if err != nil {
		return nil, fmt.Errorf("read zoom/api_client_id: %w", err)
	}

	clientSecret, err := store.GetServiceConfig(ctx, pool, "zoom", "api_client_secret", encKey)
	if errors.Is(err, store.ErrNotFound) {
		return nil, fmt.Errorf("zoom/api_client_secret is not configured — set it on the Config page")
	}
	if err != nil {
		return nil, fmt.Errorf("read zoom/api_client_secret: %w", err)
	}

	accessToken, err := fetchS2SToken(ctx, accountID, clientID, clientSecret)
	if err != nil {
		return nil, fmt.Errorf("fetch S2S token: %w", err)
	}

	// Build request body
	reqBody := map[string]any{
		"topic":      params.Topic,
		"agenda":     params.Agenda,
		"start_time": params.StartTime.UTC().Format(time.RFC3339),
		"duration":   params.Duration,
		"timezone":   params.Timezone,
		"password":   params.Password,
		"settings": map[string]bool{
			"join_before_host": params.Settings.JoinBeforeHost,
		},
	}
	if params.Recurrence == nil {
		reqBody["type"] = 2 // one-off
	} else {
		reqBody["type"] = 8 // recurring
		reqBody["recurrence"] = params.Recurrence
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.zoom.us/v2/users/me/meetings", bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("API request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusCreated {
		log.Printf("zoom/schedule: API returned %d: %s", resp.StatusCode, string(body))
		return nil, fmt.Errorf("zoom schedule returned %d: %s", resp.StatusCode, string(body))
	}

	var zoomResp struct {
		ID         int64  `json:"id"`
		UUID       string `json:"uuid"`
		HostEmail  string `json:"host_email"`
		Topic      string `json:"topic"`
		Agenda     string `json:"agenda"`
		StartTime  string `json:"start_time"`
		Duration   int    `json:"duration"`
		Timezone   string `json:"timezone"`
		JoinURL    string `json:"join_url"`
		StartURL   string `json:"start_url"`
		Password   string `json:"password"`
		Recurrence *struct {
			Type           int    `json:"type"`
			RepeatInterval int    `json:"repeat_interval"`
			WeeklyDays     string `json:"weekly_days"`
			MonthlyWeek    int    `json:"monthly_week"`
			MonthlyWeekDay int    `json:"monthly_week_day"`
			MonthlyDay     int    `json:"monthly_day"`
			EndTimes       int    `json:"end_times"`
		} `json:"recurrence"`
		Occurrences []struct {
			OccurrenceID string `json:"occurrence_id"`
			StartTime    string `json:"start_time"`
			Duration     int    `json:"duration"`
			Status       string `json:"status"`
		} `json:"occurrences"`
		Settings struct {
			JoinBeforeHost bool `json:"join_before_host"`
		} `json:"settings"`
	}
	if err := json.Unmarshal(body, &zoomResp); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	// Parse start time
	startTime, err := time.Parse(time.RFC3339, zoomResp.StartTime)
	if err != nil {
		return nil, fmt.Errorf("parse start_time: %w", err)
	}

	// Build occurrences
	var occurrences []MeetingOccurrence
	if len(zoomResp.Occurrences) > 0 {
		for _, occ := range zoomResp.Occurrences {
			occStart, err := time.Parse(time.RFC3339, occ.StartTime)
			if err != nil {
				return nil, fmt.Errorf("parse occurrence start_time: %w", err)
			}
			occurrences = append(occurrences, MeetingOccurrence{
				OccurrenceID: occ.OccurrenceID,
				StartTime:    occStart,
				Duration:     occ.Duration,
				Status:       occ.Status,
			})
		}
	} else {
		// One-off meeting: synthesize single occurrence
		occurrences = []MeetingOccurrence{{
			OccurrenceID: "",
			StartTime:    startTime,
			Duration:     zoomResp.Duration,
			Status:       "available",
		}}
	}

	return &ZoomMeeting{
		ID:          zoomResp.ID,
		UUID:        zoomResp.UUID,
		HostEmail:   zoomResp.HostEmail,
		Topic:       zoomResp.Topic,
		Agenda:      zoomResp.Agenda,
		StartTime:   startTime,
		Duration:    zoomResp.Duration,
		Timezone:    zoomResp.Timezone,
		JoinURL:     zoomResp.JoinURL,
		StartURL:    zoomResp.StartURL,
		Password:    zoomResp.Password,
		IsRecurring: zoomResp.Recurrence != nil,
		Occurrences: occurrences,
		Settings: MeetingSettings{
			JoinBeforeHost: zoomResp.Settings.JoinBeforeHost,
		},
	}, nil
}

// OccurrenceParams holds update fields for a single meeting occurrence.
type OccurrenceParams struct {
	StartTime       time.Time
	DurationMinutes int
}

// UpdateMeeting updates an existing Zoom meeting using S2S OAuth credentials.
// It PATCHes the meeting then GETs the updated record to retrieve computed occurrences.
func UpdateMeeting(ctx context.Context, pool *pgxpool.Pool, encKey []byte, meetingID int64, params MeetingParams) (*ZoomMeeting, error) {
	accessToken, err := AccessToken(ctx, pool, encKey)
	if err != nil {
		return nil, fmt.Errorf("fetch S2S token: %w", err)
	}

	reqBody := map[string]any{
		"topic":      params.Topic,
		"agenda":     params.Agenda,
		"start_time": params.StartTime.UTC().Format(time.RFC3339),
		"duration":   params.Duration,
		"timezone":   params.Timezone,
		"password":   params.Password,
		"settings": map[string]bool{
			"join_before_host": params.Settings.JoinBeforeHost,
		},
	}
	if params.Recurrence == nil {
		reqBody["type"] = 2
	} else {
		reqBody["type"] = 8
		reqBody["recurrence"] = params.Recurrence
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal update body: %w", err)
	}

	patchReq, err := http.NewRequestWithContext(ctx, http.MethodPatch,
		fmt.Sprintf("https://api.zoom.us/v2/meetings/%d", meetingID), bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("build patch request: %w", err)
	}
	patchReq.Header.Set("Authorization", "Bearer "+accessToken)
	patchReq.Header.Set("Content-Type", "application/json")

	patchResp, err := http.DefaultClient.Do(patchReq)
	if err != nil {
		return nil, fmt.Errorf("patch request failed: %w", err)
	}
	patchResp.Body.Close()
	if patchResp.StatusCode != http.StatusNoContent && patchResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("zoom update returned %d", patchResp.StatusCode)
	}

	// GET updated meeting to retrieve occurrences with their IDs.
	getReq, err := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("https://api.zoom.us/v2/meetings/%d", meetingID), nil)
	if err != nil {
		return nil, fmt.Errorf("build get request: %w", err)
	}
	getReq.Header.Set("Authorization", "Bearer "+accessToken)

	getResp, err := http.DefaultClient.Do(getReq)
	if err != nil {
		return nil, fmt.Errorf("get request failed: %w", err)
	}
	defer getResp.Body.Close()

	body, err := io.ReadAll(getResp.Body)
	if err != nil {
		return nil, fmt.Errorf("read get response: %w", err)
	}
	if getResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("zoom get returned %d: %s", getResp.StatusCode, string(body))
	}

	var zoomResp struct {
		ID         int64  `json:"id"`
		UUID       string `json:"uuid"`
		HostEmail  string `json:"host_email"`
		Topic      string `json:"topic"`
		Agenda     string `json:"agenda"`
		StartTime  string `json:"start_time"`
		Duration   int    `json:"duration"`
		Timezone   string `json:"timezone"`
		JoinURL    string `json:"join_url"`
		StartURL   string `json:"start_url"`
		Password   string `json:"password"`
		Recurrence *struct {
			Type int `json:"type"`
		} `json:"recurrence"`
		Occurrences []struct {
			OccurrenceID string `json:"occurrence_id"`
			StartTime    string `json:"start_time"`
			Duration     int    `json:"duration"`
			Status       string `json:"status"`
		} `json:"occurrences"`
		Settings struct {
			JoinBeforeHost bool `json:"join_before_host"`
		} `json:"settings"`
	}
	if err := json.Unmarshal(body, &zoomResp); err != nil {
		return nil, fmt.Errorf("parse get response: %w", err)
	}

	startTime, err := time.Parse(time.RFC3339, zoomResp.StartTime)
	if err != nil {
		return nil, fmt.Errorf("parse start_time: %w", err)
	}

	var occurrences []MeetingOccurrence
	if len(zoomResp.Occurrences) > 0 {
		for _, occ := range zoomResp.Occurrences {
			occStart, err := time.Parse(time.RFC3339, occ.StartTime)
			if err != nil {
				return nil, fmt.Errorf("parse occurrence start_time: %w", err)
			}
			occurrences = append(occurrences, MeetingOccurrence{
				OccurrenceID: occ.OccurrenceID,
				StartTime:    occStart,
				Duration:     occ.Duration,
				Status:       occ.Status,
			})
		}
	} else {
		occurrences = []MeetingOccurrence{{
			OccurrenceID: "",
			StartTime:    startTime,
			Duration:     zoomResp.Duration,
			Status:       "available",
		}}
	}

	return &ZoomMeeting{
		ID:          zoomResp.ID,
		UUID:        zoomResp.UUID,
		HostEmail:   zoomResp.HostEmail,
		Topic:       zoomResp.Topic,
		Agenda:      zoomResp.Agenda,
		StartTime:   startTime,
		Duration:    zoomResp.Duration,
		Timezone:    zoomResp.Timezone,
		JoinURL:     zoomResp.JoinURL,
		StartURL:    zoomResp.StartURL,
		Password:    zoomResp.Password,
		IsRecurring: zoomResp.Recurrence != nil,
		Occurrences: occurrences,
		Settings: MeetingSettings{
			JoinBeforeHost: zoomResp.Settings.JoinBeforeHost,
		},
	}, nil
}

// UpdateOccurrence updates a single occurrence's start time and duration.
func UpdateOccurrence(ctx context.Context, pool *pgxpool.Pool, encKey []byte, meetingID int64, occurrenceID string, params OccurrenceParams) error {
	accessToken, err := AccessToken(ctx, pool, encKey)
	if err != nil {
		return fmt.Errorf("fetch S2S token: %w", err)
	}

	reqBody := map[string]any{
		"start_time": params.StartTime.UTC().Format(time.RFC3339),
		"duration":   params.DurationMinutes,
	}
	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("marshal occurrence update body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPatch,
		fmt.Sprintf("https://api.zoom.us/v2/meetings/%d?occurrence_id=%s", meetingID, occurrenceID),
		bytes.NewReader(jsonBody))
	if err != nil {
		return fmt.Errorf("build patch occurrence request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("patch occurrence request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusOK {
		return nil
	}
	body, _ := io.ReadAll(resp.Body)
	return fmt.Errorf("zoom update occurrence returned %d: %s", resp.StatusCode, string(body))
}

// DeleteOccurrence cancels a single occurrence of a recurring Zoom meeting.
func DeleteOccurrence(ctx context.Context, pool *pgxpool.Pool, encKey []byte, meetingID int64, occurrenceID string) error {
	accessToken, err := AccessToken(ctx, pool, encKey)
	if err != nil {
		return fmt.Errorf("fetch S2S token: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodDelete,
		fmt.Sprintf("https://api.zoom.us/v2/meetings/%d?occurrence_id=%s", meetingID, occurrenceID), nil)
	if err != nil {
		return fmt.Errorf("build delete occurrence request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("delete occurrence request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusOK {
		log.Printf("zoom/delete: occurrence: meeting %d occurrence %s cancelled (status %d)", meetingID, occurrenceID, resp.StatusCode)
		return nil
	}
	body, _ := io.ReadAll(resp.Body)
	return fmt.Errorf("zoom delete occurrence returned %d: %s", resp.StatusCode, string(body))
}

// DeleteMeeting removes a Zoom meeting (cancels the entire series if recurring).
// Used to roll back a meeting if the post-creation overlap check fails.
// Returns nil on success (204 or 404); logs non-fatal errors.
func DeleteMeeting(ctx context.Context, pool *pgxpool.Pool, encKey []byte, meetingID int64) error {
	accountID, err := store.GetServiceConfig(ctx, pool, "zoom", "account_id", encKey)
	if errors.Is(err, store.ErrNotFound) {
		return nil // missing credentials → silent no-op
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

	accessToken, err := fetchS2SToken(ctx, accountID, clientID, clientSecret)
	if err != nil {
		return fmt.Errorf("fetch S2S token: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, fmt.Sprintf("https://api.zoom.us/v2/meetings/%d", meetingID), nil)
	if err != nil {
		return fmt.Errorf("build delete request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("delete request failed: %w", err)
	}
	defer resp.Body.Close()

	// 204 = success, 404 = already gone
	if resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusNotFound {
		log.Printf("zoom/delete: meeting %d cancelled (status %d)", meetingID, resp.StatusCode)
		return nil
	}

	body, _ := io.ReadAll(resp.Body)
	log.Printf("zoom/delete: API returned %d for meeting %d: %s", resp.StatusCode, meetingID, string(body))
	return fmt.Errorf("zoom delete returned %d: %s", resp.StatusCode, string(body))
}
