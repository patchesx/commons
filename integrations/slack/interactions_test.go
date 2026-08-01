package slack

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"commons/store"
)

func occ(start time.Time, durationMinutes int) store.UpcomingOccurrence {
	return store.UpcomingOccurrence{
		MeetingOccurrence: store.MeetingOccurrence{
			StartTime:       start,
			DurationMinutes: durationMinutes,
		},
	}
}

func TestCheckOverlapConflict(t *testing.T) {
	base := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)

	existing := []store.UpcomingOccurrence{
		occ(base, 60), // 10:00–11:00
	}

	t.Run("no existing occurrences", func(t *testing.T) {
		result := checkOverlapConflict(
			[]proposedWindow{{start: base, duration: 60}},
			nil,
			false, 1,
		)
		assert.Nil(t, result)
	})

	t.Run("non-overlapping after", func(t *testing.T) {
		result := checkOverlapConflict(
			[]proposedWindow{{start: base.Add(60 * time.Minute), duration: 30}}, // 11:00–11:30
			existing,
			false, 1,
		)
		assert.Nil(t, result)
	})

	t.Run("non-overlapping before", func(t *testing.T) {
		result := checkOverlapConflict(
			[]proposedWindow{{start: base.Add(-30 * time.Minute), duration: 30}}, // 09:30–10:00
			existing,
			false, 1,
		)
		assert.Nil(t, result)
	})

	t.Run("exact same window", func(t *testing.T) {
		result := checkOverlapConflict(
			[]proposedWindow{{start: base, duration: 60}},
			existing,
			false, 1,
		)
		assert.NotNil(t, result)
		assert.Equal(t, base, *result)
	})

	t.Run("proposed starts during existing", func(t *testing.T) {
		result := checkOverlapConflict(
			[]proposedWindow{{start: base.Add(30 * time.Minute), duration: 60}}, // 10:30–11:30
			existing,
			false, 1,
		)
		assert.NotNil(t, result)
	})

	t.Run("proposed contains existing", func(t *testing.T) {
		result := checkOverlapConflict(
			[]proposedWindow{{start: base.Add(-30 * time.Minute), duration: 120}}, // 09:30–11:30
			existing,
			false, 1,
		)
		assert.NotNil(t, result)
	})

	t.Run("allow_overlap true, concurrent within limit", func(t *testing.T) {
		result := checkOverlapConflict(
			[]proposedWindow{{start: base, duration: 60}},
			existing,
			true, 1, // max 1 additional concurrent = 2 total; concurrent here is 1
		)
		assert.Nil(t, result)
	})

	t.Run("allow_overlap true, concurrent exceeds limit", func(t *testing.T) {
		two := append(existing, occ(base.Add(15*time.Minute), 30)) // second overlap
		result := checkOverlapConflict(
			[]proposedWindow{{start: base, duration: 60}},
			two,
			true, 1,
		)
		assert.NotNil(t, result)
	})

	t.Run("multiple proposed windows, only second conflicts", func(t *testing.T) {
		second := base.Add(2 * time.Hour) // same day, 12:00
		existingLater := []store.UpcomingOccurrence{
			occ(second, 60), // 12:00–13:00
		}
		proposed := []proposedWindow{
			{start: base.Add(-2 * time.Hour), duration: 30}, // 08:00–08:30 — no conflict
			{start: second, duration: 30},                   // 12:00–12:30 — conflict
		}
		result := checkOverlapConflict(proposed, existingLater, false, 1)
		assert.NotNil(t, result)
		assert.Equal(t, second, *result)
	})
}
