package discord

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/bwmarrin/discordgo"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"commons/internal/testhelpers"
	"commons/platform"
	"commons/store"
)

// fakeDiscordDM represents a single DM captured by the fake Discord server.
type fakeDiscordDM struct {
	RecipientID string
	Content     string
}

// fakeDiscord captures Discord API calls during a test.
type fakeDiscord struct {
	mu        sync.Mutex
	dms       []*fakeDiscordDM
	byChannel map[string]*fakeDiscordDM
}

// endpointPath extracts the URL path from a discordgo endpoint constant so the
// fake server's routes track the API version used by the installed library.
func endpointPath(t *testing.T, endpoint string) string {
	t.Helper()
	u, err := url.Parse(endpoint)
	require.NoError(t, err)
	return u.Path
}

func newFakeDiscordServer(t *testing.T) (*httptest.Server, *fakeDiscord) {
	t.Helper()
	fd := &fakeDiscord{byChannel: map[string]*fakeDiscordDM{}}
	mux := http.NewServeMux()
	channelsPath := endpointPath(t, discordgo.EndpointChannels) // ends with "/" — subtree match

	// UserChannelCreate — opens a DM channel with a recipient.
	mux.HandleFunc(endpointPath(t, discordgo.EndpointUserChannels("@me")), func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			RecipientID string `json:"recipient_id"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		dm := &fakeDiscordDM{RecipientID: body.RecipientID}
		chID := "DM_" + body.RecipientID
		fd.mu.Lock()
		fd.dms = append(fd.dms, dm)
		fd.byChannel[chID] = dm
		fd.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"id": chID})
	})

	// ChannelMessageSend — sends a message to a channel.
	mux.HandleFunc(channelsPath, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		// Path: {channelsPath}{channelID}/messages
		chID := strings.Split(strings.TrimPrefix(r.URL.Path, channelsPath), "/")[0]
		var body struct {
			Content string `json:"content"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		fd.mu.Lock()
		if dm, ok := fd.byChannel[chID]; ok {
			dm.Content = body.Content
		}
		fd.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"id": "MSG_1", "channel_id": chID})
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, fd
}

// redirectTransport rewrites all outbound HTTP requests to the given base URL.
type redirectTransport struct{ base string }

func (rt *redirectTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req2 := req.Clone(req.Context())
	u, _ := url.Parse(rt.base)
	req2.URL.Scheme = u.Scheme
	req2.URL.Host = u.Host
	return http.DefaultTransport.RoundTrip(req2)
}

// wireDiscordFakeSession injects a discordgo.Session whose HTTP traffic is
// redirected to srvURL, and seeds the config_store rows that getSession
// checks before consulting the session cache (discord/enabled and a
// bot_token equal to the cached fake token). Restores the package cache on
// test cleanup.
func wireDiscordFakeSession(t *testing.T, pool *pgxpool.Pool, srvURL string) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, store.SetServiceConfig(ctx, pool, "discord", "enabled", "true", false, nil, testhelpers.EncKey()))
	require.NoError(t, store.SetServiceConfig(ctx, pool, "discord", "bot_token", "fake-test-token", false, nil, testhelpers.EncKey()))
	s, err := discordgo.New("Bot fake-test-token")
	require.NoError(t, err)
	s.Client = &http.Client{Transport: &redirectTransport{base: srvURL}}
	mu.Lock()
	cachedToken = "fake-test-token"
	cachedSession = s
	mu.Unlock()
	t.Cleanup(func() {
		mu.Lock()
		cachedToken = ""
		cachedSession = nil
		mu.Unlock()
	})
}

func TestDiscordNotifyUserNoIdentity(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	srv, fd := newFakeDiscordServer(t)
	wireDiscordFakeSession(t, pool, srv.URL)
	pkgPool = pool
	pkgEncKey = testhelpers.EncKey()

	// Create a user with only a Slack identity — no Discord identity.
	u, err := store.GetOrCreateUserByIdentity(ctx, pool, "slack", "U_ALICE", "alice", "Alice")
	require.NoError(t, err)

	n := NewNotifier(pool)
	err = n.NotifyUser(ctx, u.ID, platform.Message{Text: "hello"})
	require.NoError(t, err, "missing Discord identity should return nil (skip silently)")

	fd.mu.Lock()
	defer fd.mu.Unlock()
	assert.Empty(t, fd.dms, "no DM should be opened when user has no Discord identity")
}
