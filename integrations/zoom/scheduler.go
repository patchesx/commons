package zoom

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"commons/platform"
)

// ZoomScheduler implements platform.MeetingScheduler using the Zoom API.
// pool and encKey are captured at construction time; callers only provide context.
type ZoomScheduler struct {
	pool   *pgxpool.Pool
	encKey []byte
}

func NewZoomScheduler(pool *pgxpool.Pool, encKey []byte) *ZoomScheduler {
	return &ZoomScheduler{pool: pool, encKey: encKey}
}

func (s *ZoomScheduler) ScheduleMeeting(ctx context.Context, params platform.MeetingParams) (*platform.ScheduledMeeting, error) {
	m, err := ScheduleMeeting(ctx, s.pool, s.encKey, toZoomParams(params))
	if err != nil {
		return nil, err
	}
	return fromZoomMeeting(m), nil
}

func (s *ZoomScheduler) UpdateMeeting(ctx context.Context, meetingID int64, params platform.MeetingParams) (*platform.ScheduledMeeting, error) {
	m, err := UpdateMeeting(ctx, s.pool, s.encKey, meetingID, toZoomParams(params))
	if err != nil {
		return nil, err
	}
	return fromZoomMeeting(m), nil
}

func (s *ZoomScheduler) UpdateOccurrence(ctx context.Context, meetingID int64, occurrenceID string, params platform.OccurrenceParams) error {
	return UpdateOccurrence(ctx, s.pool, s.encKey, meetingID, occurrenceID,
		OccurrenceParams{StartTime: params.StartTime, DurationMinutes: params.DurationMinutes})
}

func (s *ZoomScheduler) DeleteMeeting(ctx context.Context, meetingID int64) error {
	return DeleteMeeting(ctx, s.pool, s.encKey, meetingID)
}

func (s *ZoomScheduler) DeleteOccurrence(ctx context.Context, meetingID int64, occurrenceID string) error {
	return DeleteOccurrence(ctx, s.pool, s.encKey, meetingID, occurrenceID)
}

func toZoomParams(p platform.MeetingParams) MeetingParams {
	params := MeetingParams{
		Topic:     p.Topic,
		Agenda:    p.Agenda,
		StartTime: p.StartTime,
		Duration:  p.Duration,
		Timezone:  p.Timezone,
		Password:  p.Password,
		Settings: MeetingSettings{
			JoinBeforeHost: p.Settings.JoinBeforeHost,
			PrivateMeeting: p.Settings.PrivateMeeting,
		},
	}
	if p.Recurrence != nil {
		r := p.Recurrence
		params.Recurrence = &RecurrencePattern{
			Type:           r.Type,
			RepeatInterval: r.RepeatInterval,
			WeeklyDays:     r.WeeklyDays,
			MonthlyWeek:    r.MonthlyWeek,
			MonthlyWeekDay: r.MonthlyWeekDay,
			MonthlyDay:     r.MonthlyDay,
			EndTimes:       r.EndTimes,
		}
	}
	return params
}

func fromZoomMeeting(m *ZoomMeeting) *platform.ScheduledMeeting {
	occs := make([]platform.MeetingOccurrence, len(m.Occurrences))
	for i, o := range m.Occurrences {
		occs[i] = platform.MeetingOccurrence{
			OccurrenceID: o.OccurrenceID,
			StartTime:    o.StartTime,
			Duration:     o.Duration,
			Status:       o.Status,
		}
	}
	return &platform.ScheduledMeeting{
		ID:          m.ID,
		UUID:        m.UUID,
		HostEmail:   m.HostEmail,
		Topic:       m.Topic,
		Agenda:      m.Agenda,
		StartTime:   m.StartTime,
		Duration:    m.Duration,
		Timezone:    m.Timezone,
		JoinURL:     m.JoinURL,
		StartURL:    m.StartURL,
		Password:    m.Password,
		IsRecurring: m.IsRecurring,
		Occurrences: occs,
		Settings: platform.MeetingSettings{
			JoinBeforeHost: m.Settings.JoinBeforeHost,
			PrivateMeeting: m.Settings.PrivateMeeting,
		},
	}
}
