package admintempl

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"commons/store"
)

func TestGroupByDay(t *testing.T) {
	loc, err := time.LoadLocation("America/Chicago")
	require.NoError(t, err)

	mk := func(id string, month, day, hour, min int) store.EventOccurrence {
		return store.EventOccurrence{
			ID:        id,
			StartTime: time.Date(2026, time.Month(month), day, hour, min, 0, 0, loc),
		}
	}

	// Sorted input: two on Nov 12 (different times), one on Nov 14.
	occs := []store.EventOccurrence{
		mk("a", 11, 12, 9, 0),
		mk("b", 11, 12, 15, 30),
		mk("c", 11, 14, 10, 0),
	}

	days := GroupByDay(occs, loc)
	require.Len(t, days, 2)
	assert.Equal(t, "Thu, Nov 12", days[0].DateLabel)
	assert.Len(t, days[0].Occs, 2)
	assert.Equal(t, "Sat, Nov 14", days[1].DateLabel)
	assert.Len(t, days[1].Occs, 1)

	assert.Empty(t, GroupByDay(nil, loc))

	// Late-night event stays on its local day even though UTC spills over.
	late := store.EventOccurrence{
		ID:        "late",
		StartTime: time.Date(2026, 11, 12, 23, 30, 0, 0, loc), // Nov 13 05:30 UTC
	}
	days = GroupByDay([]store.EventOccurrence{late}, loc)
	require.Len(t, days, 1)
	assert.Equal(t, "Thu, Nov 12", days[0].DateLabel)
}
