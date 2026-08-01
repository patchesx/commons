package slack

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	slacklib "github.com/slack-go/slack"
)

// TestSendDMRecordsRateLimitOnOpen verifies that when conversations.open
// returns a 429 with Retry-After, SendDM records the backoff for the DM
// destination so the next DM to that user waits instead of failing again.
func TestSendDMRecordsRateLimitOnOpen(t *testing.T) {
	resetThrottle(t, 50*time.Millisecond)

	mux := http.NewServeMux()
	mux.HandleFunc("/api/conversations.open", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusTooManyRequests)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	client := slacklib.New("test-token", slacklib.OptionAPIURL(srv.URL+"/api/"))

	_, _, err := SendDM(context.Background(), client, "U123", "hello")
	if err == nil {
		t.Fatal("expected error from rate-limited conversations.open, got nil")
	}

	th := throttleFor("U123")
	if th.rateLimitedUntil.IsZero() {
		t.Fatal("expected rate-limit backoff recorded after 429 on conversations.open")
	}
}
