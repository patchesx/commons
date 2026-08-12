package pipeline

import (
	"testing"
	"time"

	"commons/store"
)

func TestComputeBackoff_Nil(t *testing.T) {
	if d := computeBackoff(nil, 1); d != 0 {
		t.Errorf("nil config should return 0, got %v", d)
	}
}

func TestComputeBackoff_Fixed(t *testing.T) {
	cfg := &store.RetryConfig{Backoff: "fixed", InitialDelay: "5s"}
	if d := computeBackoff(cfg, 1); d != 5*time.Second {
		t.Errorf("fixed attempt 1: expected 5s, got %v", d)
	}
	if d := computeBackoff(cfg, 2); d != 5*time.Second {
		t.Errorf("fixed attempt 2: expected 5s, got %v", d)
	}
	if d := computeBackoff(cfg, 3); d != 5*time.Second {
		t.Errorf("fixed attempt 3: expected 5s, got %v", d)
	}
}

func TestComputeBackoff_Exponential(t *testing.T) {
	cfg := &store.RetryConfig{Backoff: "exponential", InitialDelay: "5s"}
	expected := []time.Duration{
		5 * time.Second,  // 5 * 2^0
		10 * time.Second, // 5 * 2^1
		20 * time.Second, // 5 * 2^2
		40 * time.Second, // 5 * 2^3
	}
	for i, want := range expected {
		if d := computeBackoff(cfg, i+1); d != want {
			t.Errorf("exponential attempt %d: expected %v, got %v", i+1, want, d)
		}
	}
}

func TestComputeBackoff_MaxDelay(t *testing.T) {
	cfg := &store.RetryConfig{Backoff: "exponential", InitialDelay: "5s", MaxDelay: "15s"}
	// attempt 1: 5s (under cap)
	if d := computeBackoff(cfg, 1); d != 5*time.Second {
		t.Errorf("attempt 1: expected 5s, got %v", d)
	}
	// attempt 2: 10s (under cap)
	if d := computeBackoff(cfg, 2); d != 10*time.Second {
		t.Errorf("attempt 2: expected 10s, got %v", d)
	}
	// attempt 3: 20s → capped at 15s
	if d := computeBackoff(cfg, 3); d != 15*time.Second {
		t.Errorf("attempt 3: expected 15s (capped), got %v", d)
	}
	// attempt 4: 40s → capped at 15s
	if d := computeBackoff(cfg, 4); d != 15*time.Second {
		t.Errorf("attempt 4: expected 15s (capped), got %v", d)
	}
}

func TestComputeBackoff_DefaultInitialDelay(t *testing.T) {
	cfg := &store.RetryConfig{Backoff: "fixed"} // no InitialDelay
	if d := computeBackoff(cfg, 1); d != 5*time.Second {
		t.Errorf("default initial delay: expected 5s, got %v", d)
	}
}

func TestComputeBackoff_InvalidInitialDelay(t *testing.T) {
	cfg := &store.RetryConfig{Backoff: "fixed", InitialDelay: "not-a-duration"}
	if d := computeBackoff(cfg, 1); d != 5*time.Second {
		t.Errorf("invalid initial delay: expected 5s default, got %v", d)
	}
}

func TestComputeBackoff_InvalidMaxDelay(t *testing.T) {
	cfg := &store.RetryConfig{Backoff: "exponential", InitialDelay: "5s", MaxDelay: "invalid"}
	// invalid max delay should be ignored — no cap
	if d := computeBackoff(cfg, 5); d != 80*time.Second {
		t.Errorf("invalid max delay: expected 80s (no cap), got %v", d)
	}
}
