package zoom

import (
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

// listMeetingsResponse is the shape of GET /v2/users/me/meetings.
type listMeetingsResponse struct {
	PageSize      int                  `json:"page_size"`
	TotalRecords  int                  `json:"total_records"`
	NextPageToken string               `json:"next_page_token"`
	Meetings      []zoomMeetingSummary `json:"meetings"`
}

// zoomMeetingSummary is a single entry from the list meetings endpoint.
type zoomMeetingSummary struct {
	ID        int64  `json:"id"`
	Topic     string `json:"topic"`
	Type      int    `json:"type"` // 2=scheduled, 3=recurring no fixed time, 8=recurring fixed time
	StartTime string `json:"start_time"`
	Duration  int    `json:"duration"`
	Timezone  string `json:"timezone"`
}

// SyncMeetings pulls upcoming meetings from Zoom and upserts them into the local DB.
// Meetings absent from the Zoom response are deleted from local tables.
// This is the entry point for the scheduled sync job.
func SyncMeetings(ctx context.Context, pool *pgxpool.Pool, encKey []byte) {
	integration, err := store.GetIntegrationByType(ctx, pool, "zoom")
	if errors.Is(err, store.ErrNotFound) {
		log.Printf("zoom/sync: no enabled zoom integration — skipping")
		return
	}
	if err != nil {
		log.Printf("zoom/sync: fetch integration: %v", err)
		return
	}

	job := &store.Job{
		Type:    store.JobTypeMeetingSync,
		Feature: store.JobFeatureMeetingScheduling,
		Status:  store.JobStatusPending,
		Trigger: store.JobTriggerScheduled,
	}
	if err := store.CreateJob(ctx, pool, job); err != nil {
		log.Printf("zoom/sync: create job: %v", err)
		return
	}

	if err := CreateMeetingSyncData(ctx, pool, &MeetingSyncData{
		JobID:         job.ID,
		IntegrationID: integration.ID,
	}); err != nil {
		log.Printf("zoom/sync: create sync data: %v", err)
		_ = store.FailJob(ctx, pool, job.ID, fmt.Sprintf("create sync data: %v", err))
		return
	}

	imported, updated, deleted, occurrences, syncErr := runMeetingSync(ctx, pool, encKey, integration.ID)
	if syncErr != nil {
		log.Printf("zoom/sync: %v", syncErr)
		_ = store.FailJob(ctx, pool, job.ID, syncErr.Error())
		return
	}

	_ = UpdateMeetingSyncCounts(ctx, pool, job.ID, imported, updated, deleted, occurrences)
	if err := store.CompleteJob(ctx, pool, job.ID); err != nil {
		log.Printf("zoom/sync: complete job: %v", err)
	}
	log.Printf("zoom/sync: done — imported=%d updated=%d deleted=%d occurrences=%d",
		imported, updated, deleted, occurrences)
}

func runMeetingSync(ctx context.Context, pool *pgxpool.Pool, encKey []byte, integrationID string) (imported, updated, deleted, occurrencesSynced int, err error) {
	token, err := AccessToken(ctx, pool, encKey)
	if err != nil {
		return 0, 0, 0, 0, fmt.Errorf("get access token: %w", err)
	}

	sentinelID, err := store.GetZoomSentinelUserID(ctx, pool)
	if errors.Is(err, store.ErrNotFound) {
		return 0, 0, 0, 0, fmt.Errorf("zoom sentinel user not found — has the zoom integration migration been applied?")
	}
	if err != nil {
		return 0, 0, 0, 0, fmt.Errorf("get sentinel user ID: %w", err)
	}

	// List upcoming meetings from Zoom.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://api.zoom.us/v2/users/me/meetings?type=upcoming&page_size=300", nil)
	if err != nil {
		return 0, 0, 0, 0, fmt.Errorf("build list request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, 0, 0, 0, fmt.Errorf("list meetings: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, 0, 0, 0, fmt.Errorf("read list response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return 0, 0, 0, 0, fmt.Errorf("zoom list meetings returned %d: %s", resp.StatusCode, string(body))
	}

	var listResp listMeetingsResponse
	if err := json.Unmarshal(body, &listResp); err != nil {
		return 0, 0, 0, 0, fmt.Errorf("parse list response: %w", err)
	}

	apiZoomIDs := make(map[int64]struct{}, len(listResp.Meetings))

	for _, summary := range listResp.Meetings {
		apiZoomIDs[summary.ID] = struct{}{}

		// Fetch full detail for all meeting types — needed to capture settings
		// (including private_meeting) and, for recurring meetings, occurrence IDs.
		zm, err := fetchMeetingDetail(ctx, token, summary.ID)
		if err != nil {
			log.Printf("zoom/sync: get detail for meeting %d: %v", summary.ID, err)
			continue
		}

		existing, lookupErr := store.GetScheduledMeetingByZoomID(ctx, pool, summary.ID)
		if lookupErr != nil && !errors.Is(lookupErr, store.ErrNotFound) {
			log.Printf("zoom/sync: lookup meeting %d: %v", summary.ID, lookupErr)
			continue
		}

		occs := toStoreOccurrences(zm.Occurrences)

		if existing == nil {
			m := &store.ScheduledMeeting{
				IntegrationID:   integrationID,
				ZoomMeetingID:   zm.ID,
				HostEmail:       zm.HostEmail,
				Topic:           zm.Topic,
				Agenda:          zm.Agenda,
				DurationMinutes: zm.Duration,
				Timezone:        zm.Timezone,
				Password:        zm.Password,
				JoinURL:         zm.JoinURL,
				StartURL:        zm.StartURL,
				IsRecurring:     zm.IsRecurring,
				IsPrivate:       zm.Settings.PrivateMeeting,
				Settings:        marshalMeetingSettings(zm.Settings),
				CreatedBy:       sentinelID,
			}
			if createErr := store.CreateScheduledMeeting(ctx, pool, encKey, m, occs); createErr != nil {
				log.Printf("zoom/sync: create meeting %d: %v", summary.ID, createErr)
				continue
			}
			imported++
			occurrencesSynced += len(occs)
		} else {
			if syncMeetingDiffers(existing, zm) {
				existing.HostEmail = zm.HostEmail
				existing.Topic = zm.Topic
				existing.Agenda = zm.Agenda
				existing.DurationMinutes = zm.Duration
				existing.Timezone = zm.Timezone
				existing.Password = zm.Password
				existing.JoinURL = zm.JoinURL
				existing.StartURL = zm.StartURL
				existing.IsRecurring = zm.IsRecurring
				existing.IsPrivate = zm.Settings.PrivateMeeting
				existing.Settings = marshalMeetingSettings(zm.Settings)
				if updateErr := store.UpdateScheduledMeeting(ctx, pool, encKey, existing, occs); updateErr != nil {
					log.Printf("zoom/sync: update meeting %d: %v", summary.ID, updateErr)
					continue
				}
				updated++
			} else {
				if syncErr := store.SyncMeetingOccurrences(ctx, pool, existing.ID, occs); syncErr != nil {
					log.Printf("zoom/sync: sync occurrences for meeting %d: %v", summary.ID, syncErr)
					continue
				}
			}
			occurrencesSynced += len(occs)
		}
	}

	// Delete local meetings no longer present in Zoom.
	localMeetings, listErr := store.ListScheduledMeetingsByIntegration(ctx, pool, integrationID)
	if listErr != nil {
		return imported, updated, deleted, occurrencesSynced, fmt.Errorf("list local meetings: %w", listErr)
	}
	for _, lm := range localMeetings {
		if _, inAPI := apiZoomIDs[lm.ZoomMeetingID]; !inAPI {
			if deleteErr := store.DeleteScheduledMeeting(ctx, pool, lm.ID); deleteErr != nil {
				log.Printf("zoom/sync: delete stale meeting %s (zoom_id=%d): %v", lm.ID, lm.ZoomMeetingID, deleteErr)
				continue
			}
			deleted++
		}
	}

	return imported, updated, deleted, occurrencesSynced, nil
}

// fetchMeetingDetail fetches full meeting detail from Zoom for recurring meetings,
// returning occurrences with their Zoom occurrence IDs.
func fetchMeetingDetail(ctx context.Context, token string, meetingID int64) (*ZoomMeeting, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("https://api.zoom.us/v2/meetings/%d?show_previous_occurrences=false", meetingID), nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("returned %d: %s", resp.StatusCode, string(body))
	}

	var d struct {
		ID         int64  `json:"id"`
		UUID       string `json:"uuid"`
		HostEmail  string `json:"host_email"`
		Topic      string `json:"topic"`
		Agenda     string `json:"agenda"`
		Duration   int    `json:"duration"`
		Timezone   string `json:"timezone"`
		StartTime  string `json:"start_time"` // top-level; present for non-recurring meetings
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
			PrivateMeeting bool `json:"private_meeting"`
		} `json:"settings"`
	}
	if err := json.Unmarshal(body, &d); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	var occs []MeetingOccurrence
	for _, o := range d.Occurrences {
		occStart, parseErr := time.Parse(time.RFC3339, o.StartTime)
		if parseErr != nil {
			log.Printf("zoom/sync: parse occurrence start_time for meeting %d: %v", meetingID, parseErr)
			continue
		}
		occs = append(occs, MeetingOccurrence{
			OccurrenceID: o.OccurrenceID,
			StartTime:    occStart,
			Duration:     o.Duration,
			Status:       o.Status,
		})
	}

	// Non-recurring meetings return start_time at the top level instead of an
	// occurrences array. Synthesize a single occurrence so callers can treat
	// all meeting types uniformly.
	if len(occs) == 0 && d.StartTime != "" {
		if t, parseErr := time.Parse(time.RFC3339, d.StartTime); parseErr == nil {
			occs = []MeetingOccurrence{{
				StartTime: t,
				Duration:  d.Duration,
				Status:    "available",
			}}
		} else {
			log.Printf("zoom/sync: parse start_time for meeting %d: %v", meetingID, parseErr)
		}
	}

	return &ZoomMeeting{
		ID:          d.ID,
		UUID:        d.UUID,
		HostEmail:   d.HostEmail,
		Topic:       d.Topic,
		Agenda:      d.Agenda,
		Duration:    d.Duration,
		Timezone:    d.Timezone,
		JoinURL:     d.JoinURL,
		StartURL:    d.StartURL,
		Password:    d.Password,
		IsRecurring: d.Recurrence != nil,
		Occurrences: occs,
		Settings: MeetingSettings{
			JoinBeforeHost: d.Settings.JoinBeforeHost,
			PrivateMeeting: d.Settings.PrivateMeeting,
		},
	}, nil
}

// toStoreOccurrences converts zoom package occurrences to store layer occurrences.
func toStoreOccurrences(occs []MeetingOccurrence) []store.MeetingOccurrence {
	result := make([]store.MeetingOccurrence, 0, len(occs))
	for _, o := range occs {
		result = append(result, store.MeetingOccurrence{
			ZoomOccurrenceID: o.OccurrenceID,
			StartTime:        o.StartTime,
			DurationMinutes:  o.Duration,
			Status:           o.Status,
		})
	}
	return result
}

// marshalMeetingSettings encodes MeetingSettings as JSON bytes for storage.
// Returns nil on marshal error (treated as empty/default settings).
func marshalMeetingSettings(s MeetingSettings) []byte {
	b, err := json.Marshal(s)
	if err != nil {
		return nil
	}
	return b
}

// syncMeetingDiffers reports whether an API meeting differs from the local record
// on fields that should trigger a DB update. Password is excluded to avoid
// decryption overhead — Zoom password changes are rare and can be updated manually.
func syncMeetingDiffers(existing *store.ScheduledMeeting, zm *ZoomMeeting) bool {
	return existing.Topic != zm.Topic ||
		existing.Agenda != zm.Agenda ||
		existing.DurationMinutes != zm.Duration ||
		existing.Timezone != zm.Timezone ||
		existing.JoinURL != zm.JoinURL ||
		existing.IsRecurring != zm.IsRecurring ||
		existing.IsPrivate != zm.Settings.PrivateMeeting
}
