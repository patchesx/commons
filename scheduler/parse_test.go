package scheduler

import (
	"testing"
	"time"
)

func TestParseSchedule_Interval(t *testing.T) {
	cases := []struct {
		input    string
		duration time.Duration
	}{
		{"1m", time.Minute},
		{"5m", 5 * time.Minute},
		{"1h", time.Hour},
		{"30s", 30 * time.Second},
		{"2h", 2 * time.Hour},
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			p, err := ParseSchedule(tc.input)
			if err != nil {
				t.Fatalf("ParseSchedule(%q) error: %v", tc.input, err)
			}
			if p.Type != ScheduleTypeInterval {
				t.Errorf("expected ScheduleTypeInterval, got %v", p.Type)
			}
			if p.Interval != tc.duration {
				t.Errorf("expected interval %v, got %v", tc.duration, p.Interval)
			}
		})
	}
}

func TestParseSchedule_Cron(t *testing.T) {
	cases := []string{
		"0 9 * * MON",
		"*/5 * * * *",
		"0 0 1 * *",
		"0 9 * * 1-5",
	}
	for _, tc := range cases {
		t.Run(tc, func(t *testing.T) {
			p, err := ParseSchedule(tc)
			if err != nil {
				t.Fatalf("ParseSchedule(%q) error: %v", tc, err)
			}
			if p.Type != ScheduleTypeCron {
				t.Errorf("expected ScheduleTypeCron, got %v", p.Type)
			}
			if p.Cron == nil {
				t.Error("expected non-nil Cron schedule")
			}
		})
	}
}

func TestParseSchedule_Invalid(t *testing.T) {
	cases := []string{
		"",
		"abc",
		"0",
		"-5m",
		"0 25 * * *", // invalid hour
	}
	for _, tc := range cases {
		t.Run(tc, func(t *testing.T) {
			_, err := ParseSchedule(tc)
			if err == nil {
				t.Errorf("ParseSchedule(%q) expected error, got nil", tc)
			}
		})
	}
}

func TestParseSchedule_ZeroInterval(t *testing.T) {
	_, err := ParseSchedule("0s")
	if err == nil {
		t.Error("expected error for zero interval")
	}
}

func TestValidateSchedule(t *testing.T) {
	if err := ValidateSchedule("5m"); err != nil {
		t.Errorf("ValidateSchedule(\"5m\") error: %v", err)
	}
	if err := ValidateSchedule("0 9 * * MON"); err != nil {
		t.Errorf("ValidateSchedule(\"0 9 * * MON\") error: %v", err)
	}
	if err := ValidateSchedule("invalid"); err == nil {
		t.Error("ValidateSchedule(\"invalid\") expected error")
	}
}

func TestIsDue_NeverFired(t *testing.T) {
	p, _ := ParseSchedule("5m")
	now := time.Now()
	if !p.IsDue(nil, now, time.UTC) {
		t.Error("expected IsDue=true when never fired")
	}
}

func TestIsDue_Interval_NotDue(t *testing.T) {
	p, _ := ParseSchedule("5m")
	now := time.Now()
	lastFired := now.Add(-2 * time.Minute) // 2 minutes ago, interval is 5m
	if p.IsDue(&lastFired, now, time.UTC) {
		t.Error("expected IsDue=false when interval not elapsed")
	}
}

func TestIsDue_Interval_Due(t *testing.T) {
	p, _ := ParseSchedule("5m")
	now := time.Now()
	lastFired := now.Add(-6 * time.Minute) // 6 minutes ago, interval is 5m
	if !p.IsDue(&lastFired, now, time.UTC) {
		t.Error("expected IsDue=true when interval elapsed")
	}
}

func TestIsDue_Interval_ExactBoundary(t *testing.T) {
	p, _ := ParseSchedule("5m")
	now := time.Now()
	lastFired := now.Add(-5 * time.Minute) // exactly 5 minutes ago
	if !p.IsDue(&lastFired, now, time.UTC) {
		t.Error("expected IsDue=true at exact interval boundary")
	}
}

func TestIsDue_Cron_NotDue(t *testing.T) {
	p, _ := ParseSchedule("0 9 * * MON") // every Monday 9am
	// Last fired was today at 9am; next fire is next Monday at 9am.
	loc, _ := time.LoadLocation("UTC")
	now := time.Date(2026, 8, 12, 10, 0, 0, 0, loc) // Wednesday 10am
	lastFired := time.Date(2026, 8, 10, 9, 0, 0, 0, loc) // Monday 9am
	if p.IsDue(&lastFired, now, loc) {
		t.Error("expected IsDue=false for cron not yet due")
	}
}

func TestIsDue_Cron_Due(t *testing.T) {
	p, _ := ParseSchedule("0 9 * * MON") // every Monday 9am
	loc, _ := time.LoadLocation("UTC")
	// Last fired was last Monday; now is this Monday at 10am — should be due.
	now := time.Date(2026, 8, 17, 10, 0, 0, 0, loc) // Monday 10am
	lastFired := time.Date(2026, 8, 10, 9, 0, 0, 0, loc) // previous Monday 9am
	if !p.IsDue(&lastFired, now, loc) {
		t.Error("expected IsDue=true for cron due")
	}
}

func TestNextFire_Interval(t *testing.T) {
	p, _ := ParseSchedule("5m")
	now := time.Now()
	lastFired := now.Add(-2 * time.Minute) // 2 minutes ago
	next := p.NextFire(&lastFired, now, time.UTC)
	expected := lastFired.Add(5 * time.Minute)
	if !next.Equal(expected) {
		t.Errorf("expected next fire %v, got %v", expected, next)
	}
}

func TestNextFire_NeverFired(t *testing.T) {
	p, _ := ParseSchedule("5m")
	now := time.Now()
	next := p.NextFire(nil, now, time.UTC)
	if !next.Equal(now) {
		t.Errorf("expected next fire %v (now), got %v", now, next)
	}
}

func TestLoadLocation_Valid(t *testing.T) {
	loc := LoadLocation("America/New_York")
	if loc.String() != "America/New_York" {
		t.Errorf("expected America/New_York, got %s", loc.String())
	}
}

func TestLoadLocation_Invalid(t *testing.T) {
	loc := LoadLocation("Invalid/Timezone")
	if loc.String() != "UTC" {
		t.Errorf("expected UTC fallback, got %s", loc.String())
	}
}
