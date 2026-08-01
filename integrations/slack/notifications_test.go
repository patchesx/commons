package slack

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	slacklib "github.com/slack-go/slack"
)

// fakeSlack captures Slack API calls made during a test.
// It responds with minimal valid JSON so the slack-go client doesn't error.
type fakeSlack struct {
	mu      sync.Mutex
	posts   []fakePost // chat.postMessage calls
	dmOpens []string   // user IDs passed to conversations.open
}

type fakePost struct {
	Channel string
	Text    string
}

func newFakeSlackServer(t *testing.T) (*httptest.Server, *fakeSlack) {
	t.Helper()
	fs := &fakeSlack{}
	mux := http.NewServeMux()

	// conversations.open — used by SendDM to get/create a DM channel.
	mux.HandleFunc("/api/conversations.open", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		users := r.FormValue("users")
		fs.mu.Lock()
		fs.dmOpens = append(fs.dmOpens, users)
		fs.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"ok":      true,
			"channel": map[string]string{"id": "DM_" + users},
		})
	})

	// chat.postMessage — used by both SendDM and direct channel posts.
	mux.HandleFunc("/api/chat.postMessage", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		fs.mu.Lock()
		fs.posts = append(fs.posts, fakePost{
			Channel: r.FormValue("channel"),
			Text:    r.FormValue("text"),
		})
		fs.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"ok":      true,
			"channel": r.FormValue("channel"),
			"ts":      "12345.67890",
		})
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, fs
}

func newTestSlackClient(apiURL string) *slacklib.Client {
	return slacklib.New("test-token", slacklib.OptionAPIURL(apiURL+"/api/"))
}
