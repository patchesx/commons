package plugin

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestShouldRun(t *testing.T) {
	now := time.Now()
	zero := time.Time{}

	tests := []struct {
		name        string
		enabledStr  string
		intervalStr string
		lastRun     time.Time
		wantRun     bool
		wantErr     bool
	}{
		{
			name:        "enabled, never run (zero time)",
			enabledStr:  "true",
			intervalStr: "15",
			lastRun:     zero,
			wantRun:     true,
		},
		{
			name:        "enabled, interval not elapsed",
			enabledStr:  "true",
			intervalStr: "15",
			lastRun:     now.Add(-5 * time.Minute),
			wantRun:     false,
		},
		{
			name:        "enabled, interval elapsed",
			enabledStr:  "true",
			intervalStr: "15",
			lastRun:     now.Add(-20 * time.Minute),
			wantRun:     true,
		},
		{
			name:        "disabled",
			enabledStr:  "false",
			intervalStr: "15",
			lastRun:     zero,
			wantRun:     false,
		},
		{
			name:        "invalid interval string",
			enabledStr:  "true",
			intervalStr: "abc",
			lastRun:     zero,
			wantRun:     false,
			wantErr:     true,
		},
		{
			name:        "zero interval",
			enabledStr:  "true",
			intervalStr: "0",
			lastRun:     zero,
			wantRun:     false,
			wantErr:     true,
		},
		{
			name:        "negative interval",
			enabledStr:  "true",
			intervalStr: "-5",
			lastRun:     zero,
			wantRun:     false,
			wantErr:     true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			run, _, err := ShouldRun(tc.enabledStr, tc.intervalStr, tc.lastRun)
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tc.wantRun, run)
		})
	}
}
