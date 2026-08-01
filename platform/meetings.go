package platform

import (
	"context"
	"time"
)

// Recurrence type constants — platform-neutral.
const (
	RecurrenceDaily   = 1
	RecurrenceWeekly  = 2
	RecurrenceMonthly = 3
)

type RecurrencePattern struct {
	Type           int    `json:"type"`
	RepeatInterval int    `json:"repeat_interval"`
	WeeklyDays     string `json:"weekly_days,omitempty"`
	MonthlyWeek    int    `json:"monthly_week,omitempty"`
	MonthlyWeekDay int    `json:"monthly_week_day,omitempty"`
	MonthlyDay     int    `json:"monthly_day,omitempty"`
	EndTimes       int    `json:"end_times"`
}

type MeetingSettings struct {
	JoinBeforeHost bool
	PrivateMeeting bool
}

type MeetingParams struct {
	Topic      string
	Agenda     string
	StartTime  time.Time
	Duration   int // minutes
	Timezone   string
	Password   string
	Recurrence *RecurrencePattern // nil = one-off
	Settings   MeetingSettings
}

type OccurrenceParams struct {
	StartTime       time.Time
	DurationMinutes int
}

type MeetingOccurrence struct {
	OccurrenceID string
	StartTime    time.Time
	Duration     int
	Status       string
}

type ScheduledMeeting struct {
	ID          int64
	UUID        string
	HostEmail   string
	Topic       string
	Agenda      string
	StartTime   time.Time
	Duration    int
	Timezone    string
	JoinURL     string
	StartURL    string
	Password    string
	IsRecurring bool
	Occurrences []MeetingOccurrence
	Settings    MeetingSettings
}

// MeetingScheduler is the scheduling capability interface. Implementations
// capture pool and encKey at construction time; callers only provide
// request-scoped context and domain parameters.
type MeetingScheduler interface {
	ScheduleMeeting(ctx context.Context, params MeetingParams) (*ScheduledMeeting, error)
	UpdateMeeting(ctx context.Context, meetingID int64, params MeetingParams) (*ScheduledMeeting, error)
	UpdateOccurrence(ctx context.Context, meetingID int64, occurrenceID string, params OccurrenceParams) error
	DeleteMeeting(ctx context.Context, meetingID int64) error
	DeleteOccurrence(ctx context.Context, meetingID int64, occurrenceID string) error
}
