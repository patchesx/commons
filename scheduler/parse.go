// Package scheduler implements the scheduled trigger runner. It polls for
// due scheduled triggers and fires their pipelines.
package scheduler

import (
	"fmt"
	"time"

	"github.com/robfig/cron/v3"
)

// ScheduleType is either "interval" or "cron".
type ScheduleType int

const (
	ScheduleTypeInterval ScheduleType = iota
	ScheduleTypeCron
)

// ParsedSchedule is a parsed schedule, either an interval or a cron expression.
type ParsedSchedule struct {
	Type     ScheduleType
	Interval time.Duration   // valid when Type == ScheduleTypeInterval
	Cron     cron.Schedule   // valid when Type == ScheduleTypeCron
	Raw      string          // the original schedule string
}

// ParseSchedule parses a schedule string into either an interval or a cron schedule.
// Interval format: Go duration strings ("1m", "5m", "1h", "30s").
// Cron format: standard cron expressions ("0 9 * * MON", "*/5 * * * *").
func ParseSchedule(schedule string) (*ParsedSchedule, error) {
	if d, err := time.ParseDuration(schedule); err == nil {
		if d <= 0 {
			return nil, fmt.Errorf("scheduler: interval must be positive, got %q", schedule)
		}
		return &ParsedSchedule{Type: ScheduleTypeInterval, Interval: d, Raw: schedule}, nil
	}

	cs, err := cron.ParseStandard(schedule)
	if err != nil {
		return nil, fmt.Errorf("scheduler: invalid schedule %q (expected interval like \"5m\" or cron like \"0 9 * * MON\"): %w", schedule, err)
	}
	return &ParsedSchedule{Type: ScheduleTypeCron, Cron: cs, Raw: schedule}, nil
}

// ValidateSchedule returns nil if the schedule string is a valid interval or cron expression.
func ValidateSchedule(schedule string) error {
	_, err := ParseSchedule(schedule)
	return err
}

// IsDue returns true if the schedule should fire at now, given the last fired time.
// If lastFired is nil (never fired), returns true (fire on first tick).
// Missed schedules fire once and reset — no catch-up.
func (p *ParsedSchedule) IsDue(lastFired *time.Time, now time.Time, loc *time.Location) bool {
	if lastFired == nil {
		return true // never fired — fire on next tick
	}

	switch p.Type {
	case ScheduleTypeInterval:
		return now.Sub(*lastFired) >= p.Interval

	case ScheduleTypeCron:
		// Compute the next activation after the last fire, in the configured timezone.
		next := p.Cron.Next(lastFired.In(loc))
		return !next.After(now.In(loc))
	}

	return false
}

// NextFire returns the time when the schedule should next fire, given the last
// fired time. If lastFired is nil, returns now (fire immediately).
func (p *ParsedSchedule) NextFire(lastFired *time.Time, now time.Time, loc *time.Location) time.Time {
	if lastFired == nil {
		return now
	}

	switch p.Type {
	case ScheduleTypeInterval:
		next := lastFired.Add(p.Interval)
		if next.Before(now) {
			return now
		}
		return next

	case ScheduleTypeCron:
		return p.Cron.Next(lastFired.In(loc))
	}

	return now
}

// LoadLocation loads a timezone by name, falling back to UTC on error.
func LoadLocation(timezone string) *time.Location {
	loc, err := time.LoadLocation(timezone)
	if err != nil {
		return time.UTC
	}
	return loc
}
