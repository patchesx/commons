package core

import (
	"testing"
	"time"

	"commons/plugin"
)

func TestDelayAction_ValidDuration(t *testing.T) {
	action := &DelayAction{}
	_, err := action.Execute(nil, map[string]any{"duration": "5m"}, nil)
	if err == nil {
		t.Fatal("expected PauseSignal error, got nil")
	}

	var pause plugin.PauseSignal
	if !asPauseSignal(err, &pause) {
		t.Fatalf("expected PauseSignal, got %T: %v", err, err)
	}

	expected := time.Now().Add(5 * time.Minute)
	tolerance := 5 * time.Second
	if pause.ResumeAt.Before(expected.Add(-tolerance)) || pause.ResumeAt.After(expected.Add(tolerance)) {
		t.Errorf("expected resume_at ~%v, got %v", expected, pause.ResumeAt)
	}
}

func TestDelayAction_InvalidDuration(t *testing.T) {
	action := &DelayAction{}
	_, err := action.Execute(nil, map[string]any{"duration": "not-a-duration"}, nil)
	if err == nil {
		t.Fatal("expected error for invalid duration")
	}

	// Should NOT be a PauseSignal.
	var pause plugin.PauseSignal
	if asPauseSignal(err, &pause) {
		t.Error("invalid duration should not return PauseSignal")
	}
}

func TestDelayAction_Seconds(t *testing.T) {
	action := &DelayAction{}
	_, err := action.Execute(nil, map[string]any{"duration": "30s"}, nil)
	var pause plugin.PauseSignal
	if !asPauseSignal(err, &pause) {
		t.Fatalf("expected PauseSignal, got %T: %v", err, err)
	}

	if pause.ResumeAt.Before(time.Now()) {
		t.Error("resume_at should be in the future")
	}
}

// asPauseSignal checks if err is a plugin.PauseSignal using errors.As semantics.
func asPauseSignal(err error, pause *plugin.PauseSignal) bool {
	if ps, ok := err.(plugin.PauseSignal); ok {
		*pause = ps
		return true
	}
	return false
}
