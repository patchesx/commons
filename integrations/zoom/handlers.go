package zoom

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"commons/internal/httpx"
	"commons/permissions"
	"commons/store"
)

// HandleDeleteRecording handles POST /api/jobs/{id}/delete-zoom-recording.
func HandleDeleteRecording(pool *pgxpool.Pool, encKey []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		jobID := r.PathValue("id")
		log.Printf("zoom/api: delete-zoom-recording requested for job %s", jobID)

		job, err := store.GetJobByID(ctx, pool, jobID)
		if err != nil || job == nil {
			httpx.WriteError(w, http.StatusNotFound, "job not found")
			return
		}
		rec, recErr := GetRecordingData(ctx, pool, jobID)
		if recErr != nil || rec == nil {
			httpx.WriteError(w, http.StatusNotFound, "recording data not found")
			return
		}
		if rec.RecordingDeletedAt != nil {
			httpx.WriteError(w, http.StatusConflict, "recording already deleted")
			return
		}
		if err := CheckZoomS2SConfig(ctx, pool, encKey); err != nil {
			httpx.WriteError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		if err := DeleteRecording(ctx, pool, rec.MeetingUUID, encKey); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if err := MarkDeleted(ctx, pool, jobID); err != nil {
			log.Printf("zoom/api: failed to mark recording deleted for job %s: %v", jobID, err)
		}
		log.Printf("zoom/api: zoom recording deleted for job %s", jobID)
		httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

// HandleS2SConfigured handles GET /api/zoom/s2s-configured.
func HandleS2SConfigured(pool *pgxpool.Pool, encKey []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		err := CheckZoomS2SConfig(r.Context(), pool, encKey)
		httpx.WriteJSON(w, http.StatusOK, map[string]bool{"configured": err == nil})
	}
}

// HandleTriggerMeetingSync handles POST /api/zoom/meetings/sync.
func HandleTriggerMeetingSync(syncFn func()) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		go syncFn()
		httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "sync started"})
	}
}

// ---- meeting response types ----

type occurrenceSummaryResponse struct {
	ID               string `json:"id"`
	ZoomOccurrenceID string `json:"zoomOccurrenceId,omitempty"`
	StartTime        string `json:"startTime"`
	DurationMinutes  int    `json:"durationMinutes"`
}

type meetingSummaryResponse struct {
	ID              string                      `json:"id"`
	ZoomMeetingID   int64                       `json:"zoomMeetingId"`
	Topic           string                      `json:"topic"`
	IsRecurring     bool                        `json:"isRecurring"`
	DurationMinutes int                         `json:"durationMinutes"`
	Timezone        string                      `json:"timezone"`
	NextStart       string                      `json:"nextStart,omitempty"`
	Occurrences     []occurrenceSummaryResponse `json:"occurrences,omitempty"`
}

func toMeetingSummaryResponse(m store.MeetingSummary) meetingSummaryResponse {
	resp := meetingSummaryResponse{
		ID:              m.ID,
		ZoomMeetingID:   m.ZoomMeetingID,
		Topic:           m.Topic,
		IsRecurring:     m.IsRecurring,
		DurationMinutes: m.DurationMinutes,
		Timezone:        m.Timezone,
	}
	if !m.NextStart.IsZero() {
		resp.NextStart = m.NextStart.Format(time.RFC3339)
	}
	if len(m.Occurrences) > 0 {
		resp.Occurrences = make([]occurrenceSummaryResponse, len(m.Occurrences))
		for i, o := range m.Occurrences {
			resp.Occurrences[i] = occurrenceSummaryResponse{
				ID:               o.ID,
				ZoomOccurrenceID: o.ZoomOccurrenceID,
				StartTime:        o.StartTime.Format(time.RFC3339),
				DurationMinutes:  o.DurationMinutes,
			}
		}
	}
	return resp
}

// HandleListMeetings handles GET /api/meetings.
func HandleListMeetings(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		zoomInteg, err := store.GetIntegrationByType(ctx, pool, "zoom")
		if errors.Is(err, store.ErrNotFound) {
			httpx.WriteJSON(w, http.StatusOK, []meetingSummaryResponse{})
			return
		}
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to look up Zoom integration")
			return
		}

		summaries, err := store.ListUpcomingMeetingSummaries(ctx, pool, zoomInteg.ID)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to list meetings")
			return
		}

		out := make([]meetingSummaryResponse, len(summaries))
		for i, s := range summaries {
			out[i] = toMeetingSummaryResponse(s)
		}
		httpx.WriteJSON(w, http.StatusOK, out)
	}
}

type scheduleMeetingBody struct {
	Topic        string          `json:"topic"`
	Agenda       string          `json:"agenda"`
	StartTime    string          `json:"startTime"`
	Duration     int             `json:"duration"`
	Timezone     string          `json:"timezone"`
	Password     string          `json:"password"`
	Recurrence   *recurrenceBody `json:"recurrence"`
	SendReminder bool            `json:"sendReminder"`
	SkipUpload   bool            `json:"skipUpload"`
}

type recurrenceBody struct {
	Type           string `json:"type"`
	Interval       int    `json:"interval"`
	EndTimes       int    `json:"endTimes"`
	WeeklyDays     string `json:"weeklyDays"`
	MonthlyDay     int    `json:"monthlyDay"`
	MonthlyWeek    int    `json:"monthlyWeek"`
	MonthlyWeekday int    `json:"monthlyWeekday"`
}

// HandleScheduleMeeting handles POST /api/meetings.
// getAdminID extracts the authenticated user's ID from the request context;
// injected to avoid an import cycle with the web package.
func HandleScheduleMeeting(pool *pgxpool.Pool, encKey []byte, getAdminID func(context.Context) string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		userID := getAdminID(ctx)

		var body scheduleMeetingBody
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "invalid request body")
			return
		}

		if body.Topic == "" {
			httpx.WriteError(w, http.StatusBadRequest, "topic is required")
			return
		}

		startTime, err := time.Parse(time.RFC3339, body.StartTime)
		if err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "startTime must be RFC3339 format")
			return
		}
		minAdvStr, _ := store.GetServiceConfig(ctx, pool, "meetings", "min_advance_minutes", encKey)
		minAdv := 15
		if n, err := strconv.Atoi(minAdvStr); err == nil && n > 0 {
			minAdv = n
		}
		if startTime.Before(time.Now().Add(time.Duration(minAdv) * time.Minute)) {
			httpx.WriteError(w, http.StatusBadRequest, fmt.Sprintf("start time must be at least %d minutes from now", minAdv))
			return
		}

		if body.Duration < 1 {
			httpx.WriteError(w, http.StatusBadRequest, "duration must be at least 1 minute")
			return
		}
		maxDurStr, _ := store.GetServiceConfig(ctx, pool, "meetings", "max_duration_minutes", encKey)
		maxDur := 1440
		if n, err := strconv.Atoi(maxDurStr); err == nil && n > 0 {
			maxDur = n
		}
		if body.Duration > maxDur {
			httpx.WriteError(w, http.StatusBadRequest, fmt.Sprintf("duration exceeds maximum of %d minutes", maxDur))
			return
		}

		var recurrence *RecurrencePattern
		if body.Recurrence != nil && body.Recurrence.Type != "" && body.Recurrence.Type != "none" {
			rec := body.Recurrence
			if rec.EndTimes < 1 || rec.EndTimes > 60 {
				httpx.WriteError(w, http.StatusBadRequest, "endTimes must be between 1 and 60")
				return
			}
			interval := rec.Interval
			if interval < 1 {
				interval = 1
			}
			zoomRec := &RecurrencePattern{RepeatInterval: interval, EndTimes: rec.EndTimes}
			switch rec.Type {
			case "weekly":
				if rec.WeeklyDays == "" {
					httpx.WriteError(w, http.StatusBadRequest, "weeklyDays is required for weekly recurrence")
					return
				}
				zoomRec.Type = 2
				zoomRec.WeeklyDays = rec.WeeklyDays
			case "monthly_date":
				if rec.MonthlyDay < 1 || rec.MonthlyDay > 28 {
					httpx.WriteError(w, http.StatusBadRequest, "monthlyDay must be between 1 and 28")
					return
				}
				zoomRec.Type = 3
				zoomRec.MonthlyDay = rec.MonthlyDay
			case "monthly_weekday":
				zoomRec.Type = 3
				zoomRec.MonthlyWeek = rec.MonthlyWeek
				zoomRec.MonthlyWeekDay = rec.MonthlyWeekday
			default:
				httpx.WriteError(w, http.StatusBadRequest, "recurrence type must be weekly, monthly_date, or monthly_weekday")
				return
			}
			recurrence = zoomRec
		}

		zoomInteg, err := store.GetIntegrationByType(ctx, pool, "zoom")
		if err != nil || zoomInteg == nil {
			httpx.WriteError(w, http.StatusBadRequest, "Zoom integration is not configured")
			return
		}

		maxSchedStr, _ := store.GetServiceConfig(ctx, pool, "meetings", "max_scheduled", encKey)
		if maxSchedStr != "" {
			limit, _ := strconv.Atoi(maxSchedStr)
			existing, _ := store.CountUpcomingOccurrences(ctx, pool, zoomInteg.ID)
			newCount := 1
			if recurrence != nil {
				newCount = recurrence.EndTimes
			}
			if existing+newCount > limit {
				httpx.WriteError(w, http.StatusBadRequest, fmt.Sprintf("scheduling %d more would exceed the limit of %d", newCount, limit))
				return
			}
		}

		allowOverlap, _ := store.GetServiceConfig(ctx, pool, "meetings", "allow_overlap", encKey)
		maxOverlap := 1
		if maxOverlapStr, _ := store.GetServiceConfig(ctx, pool, "meetings", "max_overlap", encKey); maxOverlapStr != "" {
			if n, err := strconv.Atoi(maxOverlapStr); err == nil {
				maxOverlap = n
			}
		}
		existingOccs, _ := store.ListUpcomingOccurrences(ctx, pool, zoomInteg.ID, "", "")
		if t := store.CheckOverlapConflict(startTime, body.Duration, existingOccs, allowOverlap == "true", maxOverlap); t != nil {
			httpx.WriteError(w, http.StatusConflict, fmt.Sprintf(
				"conflicts with an existing meeting at %s (max additional concurrent: %d)",
				t.UTC().Format("Jan 2, 2006 3:04 PM MST"), maxOverlap,
			))
			return
		}

		timezone := body.Timezone
		if timezone == "" {
			timezone = "UTC"
		}
		meeting, err := ScheduleMeeting(ctx, pool, encKey, MeetingParams{
			Topic:      body.Topic,
			Agenda:     body.Agenda,
			StartTime:  startTime,
			Duration:   body.Duration,
			Timezone:   timezone,
			Password:   body.Password,
			Recurrence: recurrence,
		})
		if err != nil {
			log.Printf("zoom/api: schedule meeting: zoom API error: %v", err)
			httpx.WriteError(w, http.StatusBadGateway, "failed to create meeting in Zoom — check credentials")
			return
		}

		if recurrence != nil {
			for _, proposed := range meeting.Occurrences {
				if t := store.CheckOverlapConflict(proposed.StartTime, proposed.Duration, existingOccs, allowOverlap == "true", maxOverlap); t != nil {
					if rollbackErr := DeleteMeeting(ctx, pool, encKey, meeting.ID); rollbackErr != nil {
						log.Printf("zoom/api: schedule meeting: rollback failed for zoom meeting %d: %v", meeting.ID, rollbackErr)
					}
					httpx.WriteError(w, http.StatusConflict, fmt.Sprintf(
						"conflicts with an existing meeting at %s (max additional concurrent: %d)",
						proposed.StartTime.UTC().Format("Jan 2, 2006 3:04 PM MST"), maxOverlap,
					))
					return
				}
			}
		}

		occurrences := make([]store.MeetingOccurrence, len(meeting.Occurrences))
		for i, o := range meeting.Occurrences {
			occurrences[i] = store.MeetingOccurrence{
				ZoomOccurrenceID: o.OccurrenceID,
				StartTime:        o.StartTime,
				DurationMinutes:  o.Duration,
				Status:           o.Status,
			}
		}
		if err := store.CreateScheduledMeeting(ctx, pool, encKey, &store.ScheduledMeeting{
			IntegrationID:   zoomInteg.ID,
			ZoomMeetingID:   meeting.ID,
			HostEmail:       meeting.HostEmail,
			Topic:           body.Topic,
			Agenda:          body.Agenda,
			DurationMinutes: body.Duration,
			Timezone:        timezone,
			Password:        body.Password,
			JoinURL:         meeting.JoinURL,
			StartURL:        meeting.StartURL,
			IsRecurring:     recurrence != nil,
			CreatedBy:       userID,
			SendReminder:    body.SendReminder,
			SkipUpload:      body.SkipUpload,
		}, occurrences); err != nil {
			log.Printf("zoom/api: schedule meeting: failed to persist (zoom meeting %d exists): %v", meeting.ID, err)
		}

		loc, _ := time.LoadLocation(timezone)
		if loc == nil {
			loc = time.UTC
		}
		firstOcc := meeting.Occurrences[0]
		httpx.WriteJSON(w, http.StatusCreated, map[string]any{
			"zoomMeetingId":     meeting.ID,
			"joinUrl":           meeting.JoinURL,
			"startUrl":          meeting.StartURL,
			"firstStart":        firstOcc.StartTime.In(loc).Format(time.RFC3339),
			"isRecurring":       recurrence != nil,
			"occurrences":       len(meeting.Occurrences),
			"recurrenceSummary": recurrenceSummaryText(recurrence),
		})
	}
}

// HandleCancelMeeting handles DELETE /api/meetings/{id}.
func HandleCancelMeeting(pool *pgxpool.Pool, encKey []byte, getAdminID func(context.Context) string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		id := r.PathValue("id")
		userID := getAdminID(ctx)

		m, err := store.GetScheduledMeetingByID(ctx, pool, encKey, id)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to look up meeting")
			return
		}
		if m == nil {
			httpx.WriteError(w, http.StatusNotFound, "meeting not found")
			return
		}

		if m.CreatedBy != userID {
			perms, err := store.GetUserPermissions(ctx, pool, userID)
			if err != nil {
				httpx.WriteError(w, http.StatusInternalServerError, "failed to check permissions")
				return
			}
			hasManage := false
			for _, p := range perms {
				if p == permissions.MeetingsManage {
					hasManage = true
					break
				}
			}
			if !hasManage {
				httpx.WriteError(w, http.StatusForbidden, "you can only cancel meetings you created")
				return
			}
		}

		if err := DeleteMeeting(ctx, pool, encKey, m.ZoomMeetingID); err != nil {
			log.Printf("zoom/api: cancel meeting: zoom delete %d: %v", m.ZoomMeetingID, err)
		}
		if err := store.DeleteScheduledMeeting(ctx, pool, m.ID); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to delete meeting")
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

func recurrenceSummaryText(rec *RecurrencePattern) string {
	if rec == nil {
		return "One-time"
	}
	switch rec.Type {
	case 1:
		if rec.RepeatInterval == 1 {
			return fmt.Sprintf("Daily, %d times", rec.EndTimes)
		}
		return fmt.Sprintf("Every %d days, %d times", rec.RepeatInterval, rec.EndTimes)
	case 2:
		days := parseDayNames(rec.WeeklyDays)
		if rec.RepeatInterval == 1 {
			return fmt.Sprintf("Weekly on %s, %d times", strings.Join(days, "/"), rec.EndTimes)
		}
		return fmt.Sprintf("Every %d weeks on %s, %d times", rec.RepeatInterval, strings.Join(days, "/"), rec.EndTimes)
	case 3:
		return fmt.Sprintf("Monthly, %d times", rec.EndTimes)
	}
	return "Recurring"
}

var dayNames = map[string]string{
	"1": "Sun", "2": "Mon", "3": "Tue", "4": "Wed",
	"5": "Thu", "6": "Fri", "7": "Sat",
}

func parseDayNames(weeklyDays string) []string {
	var names []string
	for _, d := range strings.Split(weeklyDays, ",") {
		if name, ok := dayNames[strings.TrimSpace(d)]; ok {
			names = append(names, name)
		}
	}
	return names
}
