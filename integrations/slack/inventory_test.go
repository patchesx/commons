package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	slacklib "github.com/slack-go/slack"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"commons/internal/testhelpers"
	"commons/store"
)

// invFake is a fake Slack API that records posts and view calls and answers the identifier lookups.
type invFake struct {
	mu           sync.Mutex
	posts        []invMessage // chat.postMessage
	updates      []invMessage // chat.update
	ephemerals   []invMessage // chat.postEphemeral
	dmOpens      []string
	views        []invViewCall
	botsInfoFail bool
}

// invMessage is a message sent to, or edited in, a channel. Blocks is the raw blocks JSON.
type invMessage struct {
	Channel string
	User    string // ephemeral messages only
	TS      string // chat.update only
	Text    string
	Blocks  string
}

type invViewCall struct {
	Method string
	Body   string
}

func newInvFakeSlack(t *testing.T) (*httptest.Server, *invFake) {
	t.Helper()
	prev := sendInterval
	sendInterval = 0
	t.Cleanup(func() { sendInterval = prev })

	f := &invFake{}
	mux := http.NewServeMux()
	reply := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(v)
	}

	mux.HandleFunc("/api/conversations.open", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		users := r.FormValue("users")
		f.mu.Lock()
		f.dmOpens = append(f.dmOpens, users)
		f.mu.Unlock()
		reply(w, map[string]any{"ok": true, "channel": map[string]string{"id": "DM_" + users}})
	})
	mux.HandleFunc("/api/chat.postMessage", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		f.mu.Lock()
		f.posts = append(f.posts, invMessage{Channel: r.FormValue("channel"), Text: r.FormValue("text"), Blocks: r.FormValue("blocks")})
		f.mu.Unlock()
		reply(w, map[string]any{"ok": true, "channel": r.FormValue("channel"), "ts": "1.1"})
	})
	mux.HandleFunc("/api/chat.update", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		f.mu.Lock()
		f.updates = append(f.updates, invMessage{Channel: r.FormValue("channel"), TS: r.FormValue("ts"), Text: r.FormValue("text"), Blocks: r.FormValue("blocks")})
		f.mu.Unlock()
		reply(w, map[string]any{"ok": true, "channel": r.FormValue("channel"), "ts": r.FormValue("ts"), "text": r.FormValue("text")})
	})
	mux.HandleFunc("/api/chat.postEphemeral", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		f.mu.Lock()
		f.ephemerals = append(f.ephemerals, invMessage{Channel: r.FormValue("channel"), User: r.FormValue("user"), Text: r.FormValue("text")})
		f.mu.Unlock()
		reply(w, map[string]any{"ok": true, "message_ts": "1.2"})
	})
	for _, method := range []string{"views.open", "views.update", "views.push", "views.publish"} {
		mux.HandleFunc("/api/"+method, func(w http.ResponseWriter, r *http.Request) {
			raw, _ := io.ReadAll(r.Body)
			body := string(raw)
			if !strings.Contains(r.Header.Get("Content-Type"), "json") {
				if form, err := url.ParseQuery(body); err == nil {
					body = form.Get("view")
				}
			}
			f.mu.Lock()
			f.views = append(f.views, invViewCall{Method: strings.TrimPrefix(r.URL.Path, "/api/"), Body: body})
			f.mu.Unlock()
			reply(w, map[string]any{"ok": true, "view": map[string]string{"id": "V1"}})
		})
	}
	mux.HandleFunc("/api/auth.test", func(w http.ResponseWriter, r *http.Request) {
		reply(w, map[string]any{"ok": true, "url": "https://x.slack.com/", "team": "X", "team_id": "T123", "user": "bot", "user_id": "UBOT", "bot_id": "B123"})
	})
	mux.HandleFunc("/api/bots.info", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		fail := f.botsInfoFail
		f.mu.Unlock()
		if fail {
			reply(w, map[string]any{"ok": false, "error": "missing_scope"})
			return
		}
		reply(w, map[string]any{"ok": true, "bot": map[string]string{"id": "B123", "app_id": "A123", "name": "commons"}})
	})
	mux.HandleFunc("/api/users.info", func(w http.ResponseWriter, r *http.Request) {
		reply(w, map[string]any{"ok": false, "error": "user_not_found"})
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, f
}

func (f *invFake) viewCalls(method string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, v := range f.views {
		if v.Method == method {
			out = append(out, v.Body)
		}
	}
	return out
}

func (f *invFake) lastView(t *testing.T, method string) string {
	t.Helper()
	calls := f.viewCalls(method)
	require.NotEmpty(t, calls, "expected a %s call", method)
	return calls[len(calls)-1]
}

func (f *invFake) postsSnapshot() []invMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]invMessage(nil), f.posts...)
}

func (f *invFake) updatesSnapshot() []invMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]invMessage(nil), f.updates...)
}

func (f *invFake) ephemeralsSnapshot() []invMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]invMessage(nil), f.ephemerals...)
}

func (f *invFake) postTexts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, p := range f.posts {
		out = append(out, p.Text)
	}
	return out
}

type invEnv struct {
	ctx    context.Context
	pool   *pgxpool.Pool
	encKey []byte
	fake   *invFake
	client *slacklib.Client
}

func newInvEnv(t *testing.T) *invEnv {
	t.Helper()
	srv, fake := newInvFakeSlack(t)
	return &invEnv{
		ctx:    context.Background(),
		pool:   testhelpers.SetupTestDB(t),
		encKey: testhelpers.EncKey(),
		fake:   fake,
		client: newTestSlackClient(srv.URL),
	}
}

// usePackageClient points the package-level client at the fake so getClient, and with it dispatchEvent and
// dispatchInteractionBody, talk to it. The previous state is restored afterwards.
func (e *invEnv) usePackageClient(t *testing.T) {
	t.Helper()
	require.NoError(t, store.SetServiceConfig(e.ctx, e.pool, "slack", "bot_token", "inv-test-token", false, nil, e.encKey))
	mu.Lock()
	prevToken, prevClient := cachedToken, cachedClient
	cachedToken, cachedClient = "inv-test-token", e.client
	mu.Unlock()
	prevPool, prevKey := pkgPool, pkgEncKey
	pkgPool, pkgEncKey = e.pool, e.encKey
	t.Cleanup(func() {
		mu.Lock()
		cachedToken, cachedClient = prevToken, prevClient
		mu.Unlock()
		pkgPool, pkgEncKey = prevPool, prevKey
	})
}

func (e *invEnv) user(t *testing.T, slackID, name string) *store.User {
	t.Helper()
	u, err := store.GetOrCreateUserByIdentity(e.ctx, e.pool, "slack", slackID, name, name)
	require.NoError(t, err)
	return u
}

func (e *invEnv) item(t *testing.T, code, name string) *store.InventoryItem {
	t.Helper()
	it, err := store.CreateInventoryItem(e.ctx, e.pool, store.InventoryItemInput{Name: name}, code)
	require.NoError(t, err)
	return it
}

func (e *invEnv) holderOf(t *testing.T, itemID string) string {
	t.Helper()
	it, err := store.GetInventoryItem(e.ctx, e.pool, itemID)
	require.NoError(t, err)
	if it.CurrentHolderUserID == nil {
		return ""
	}
	return *it.CurrentHolderUserID
}

func decodePayload(t *testing.T, raw string) *slacklib.InteractionCallback {
	t.Helper()
	var p slacklib.InteractionCallback
	require.NoError(t, json.Unmarshal([]byte(raw), &p))
	return &p
}

func blockActionPayload(t *testing.T, slackUser string) *slacklib.InteractionCallback {
	return decodePayload(t, fmt.Sprintf(`{"type":"block_actions","user":{"id":%q},"trigger_id":"trig","view":{"id":"V1","hash":"h1"}}`, slackUser))
}

func handOffSubmitJSON(slackUser, itemID, recipient string) string {
	return fmt.Sprintf(`{"type":"view_submission","user":{"id":%q},"view":{"id":"V2","root_view_id":"V1","callback_id":%q,"private_metadata":%q,"state":{"values":{%q:{%q:{"type":"users_select","selected_user":%q}}}}}}`,
		slackUser, ModalIDInventoryHandOff, itemID, inventoryHandOffBlockID, inventoryHandOffUserAction, recipient)
}

func modalJSON(t *testing.T, m slacklib.ModalViewRequest) string {
	t.Helper()
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	require.NoError(t, enc.Encode(m))
	// slack-go marshals nested text objects itself and escapes these characters; undo that so tests can
	// look for the text as the user would read it.
	bs := string(rune(92)) // one backslash, which starts the \uXXXX escapes
	return strings.NewReplacer(bs+"u0026", "&", bs+"u003c", "<", bs+"u003e", ">").Replace(buf.String())
}

func dmEvent(slackUser, text string) slackEventInner {
	return slackEventInner{
		Type:        "message",
		User:        json.RawMessage(fmt.Sprintf("%q", slackUser)),
		Channel:     "D1",
		ChannelType: "im",
		Text:        text,
	}
}

func TestInventoryModalStates(t *testing.T) {
	loc := "Closet"
	held := store.InventoryItem{ID: "item-1", Name: "Projector", Code: "K7M4QX", Location: &loc}

	t.Run("initial", func(t *testing.T) {
		m := InventoryModal(inventoryModalData{})
		body := modalJSON(t, m)
		assert.Contains(t, body, "Item code")
		assert.Contains(t, body, `"dispatch_action":true`, "Enter in the code box triggers the lookup")
		assert.Contains(t, body, ActionInventoryCodeInput)
		assert.Contains(t, body, "You aren't holding any items")
		assert.NotContains(t, body, "Confirm possession")
		assert.Equal(t, "", m.PrivateMetadata)
		assert.Nil(t, m.Submit, "no submit button: confirming is a button in the item card")
	})

	t.Run("found and available", func(t *testing.T) {
		it := store.InventoryItem{ID: "item-2", Name: "Laptop", Code: "LAP2TP"}
		m := InventoryModal(inventoryModalData{Code: "lap2tp", Found: &it})
		body := modalJSON(t, m)
		assert.Contains(t, body, "Laptop")
		assert.Contains(t, body, "LAP-2TP")
		assert.Contains(t, body, "Not currently held")
		assert.Contains(t, body, "Confirm possession")
		assert.Contains(t, body, `"value":"item-2"`)
		assert.Equal(t, "item-2", m.PrivateMetadata)
	})

	t.Run("found and held by someone else", func(t *testing.T) {
		m := InventoryModal(inventoryModalData{Found: &held, FoundHolder: "Bob"})
		body := modalJSON(t, m)
		assert.Contains(t, body, "Currently held by *Bob*")
		assert.Contains(t, body, "Confirm possession")
	})

	t.Run("already the holder", func(t *testing.T) {
		body := modalJSON(t, InventoryModal(inventoryModalData{Found: &held, AlreadyHolder: true}))
		assert.Contains(t, body, "You already have this item.")
		assert.NotContains(t, body, "Confirm possession")
	})

	t.Run("held items get verify and hand off buttons", func(t *testing.T) {
		body := modalJSON(t, InventoryModal(inventoryModalData{Held: []store.InventoryItem{held}}))
		assert.Contains(t, body, "Projector")
		assert.Contains(t, body, "_Closet_")
		assert.Contains(t, body, ActionInventoryVerify)
		assert.Contains(t, body, ActionInventoryHandOff)
		assert.Contains(t, body, `"block_id":"inventory_held_item-1"`)
	})

	t.Run("a long list pages, so it stays under Slack's block limit", func(t *testing.T) {
		var many []store.InventoryItem
		for i := range 25 {
			many = append(many, store.InventoryItem{ID: fmt.Sprintf("i%d", i), Name: fmt.Sprintf("Thing %02d", i), Code: "ABC234"})
		}
		count := func(m slacklib.ModalViewRequest) int { return strings.Count(modalJSON(t, m), `"block_id":"inventory_held_i`) }

		first := InventoryModal(inventoryModalData{Held: many, Notice: "hello"})
		assert.LessOrEqual(t, len(first.Blocks.BlockSet), 100)
		body := modalJSON(t, first)
		assert.Equal(t, inventoryHeldPageSize, count(first))
		assert.Contains(t, body, "1–10 of 25")
		assert.Contains(t, body, "Thing 09")
		assert.NotContains(t, body, "Thing 10")
		assert.Contains(t, body, ActionInventoryHeldNext)
		assert.Contains(t, body, "page:2")
		assert.NotContains(t, body, ActionInventoryHeldPrev, "no previous on the first page")

		last := InventoryModal(inventoryModalData{Held: many, Page: 3})
		body = modalJSON(t, last)
		assert.Equal(t, 5, count(last))
		assert.Contains(t, body, "21–25 of 25")
		assert.Contains(t, body, "Thing 24")
		assert.Contains(t, body, ActionInventoryHeldPrev)
		assert.NotContains(t, body, ActionInventoryHeldNext, "no next on the last page")
		assert.Contains(t, body, inventoryHeldPagePrefix+"3")

		assert.Contains(t, modalJSON(t, InventoryModal(inventoryModalData{Held: many, Page: 99})), "21–25 of 25", "past the end shows the last page")
		assert.Contains(t, modalJSON(t, InventoryModal(inventoryModalData{Held: many, Page: -4})), "1–10 of 25")

		small := modalJSON(t, InventoryModal(inventoryModalData{Held: many[:10]}))
		assert.NotContains(t, small, ActionInventoryHeldNext, "one page needs no pager")
		assert.NotContains(t, small, " of 10")

		huge := make([]store.InventoryItem, 150)
		for i := range huge {
			huge[i] = store.InventoryItem{ID: fmt.Sprintf("h%d", i), Name: "Item", Code: "ABC234"}
		}
		for page := 1; page <= 15; page++ {
			m := InventoryModal(inventoryModalData{Held: huge, Page: page, Notice: "n", Found: &huge[0], FoundHolder: "Someone"})
			require.LessOrEqual(t, len(m.Blocks.BlockSet), 100, "page %d", page)
		}
	})

	t.Run("names are escaped for mrkdwn", func(t *testing.T) {
		it := store.InventoryItem{ID: "x", Name: "A <b> & B", Code: "ABC234"}
		body := modalJSON(t, InventoryModal(inventoryModalData{Held: []store.InventoryItem{it}}))
		assert.Contains(t, body, "A &lt;b&gt; &amp; B")
	})

	t.Run("hand off modal asks for a person and carries the item", func(t *testing.T) {
		m := InventoryHandOffModal(held)
		body := modalJSON(t, m)
		assert.Contains(t, body, "users_select")
		assert.Contains(t, body, inventoryHandOffUserAction)
		assert.Equal(t, "item-1", m.PrivateMetadata)
		assert.Equal(t, ModalIDInventoryHandOff, m.CallbackID)
		assert.NotNil(t, m.Submit)
	})
}

func TestInventoryModalFlow(t *testing.T) {
	e := newInvEnv(t)
	alice := e.user(t, "U_ALICE", "Alice")
	bob := e.user(t, "U_BOB", "Bob")
	projector := e.item(t, "K7M4QX", "Projector")

	t.Run("opening shows the modal", func(t *testing.T) {
		w := httptest.NewRecorder()
		handleOpenInventory(e.ctx, w, e.pool, e.client, blockActionPayload(t, "U_ALICE"))
		assert.Equal(t, http.StatusOK, w.Code)
		body := e.fake.lastView(t, "views.open")
		assert.Contains(t, body, "Items I hold")
		assert.Contains(t, body, "trig")
	})

	t.Run("lookup accepts any case and the dash", func(t *testing.T) {
		w := httptest.NewRecorder()
		handleInventoryCodeInput(e.ctx, w, e.pool, e.client, blockActionPayload(t, "U_ALICE"), " k7m-4qx ")
		body := e.fake.lastView(t, "views.update")
		assert.Contains(t, body, "Projector")
		assert.Contains(t, body, "Confirm possession")
		assert.Contains(t, body, projector.ID)
	})

	t.Run("lookup of an unknown code says so", func(t *testing.T) {
		w := httptest.NewRecorder()
		handleInventoryCodeInput(e.ctx, w, e.pool, e.client, blockActionPayload(t, "U_ALICE"), "NOPE22")
		body := e.fake.lastView(t, "views.update")
		assert.Contains(t, body, "No active item has the code")
		assert.NotContains(t, body, "Confirm possession")
	})

	t.Run("confirming takes possession and refreshes the held list", func(t *testing.T) {
		w := httptest.NewRecorder()
		handleInventoryClaim(e.ctx, w, e.pool, e.client, blockActionPayload(t, "U_ALICE"), projector.ID)
		assert.Equal(t, alice.ID, e.holderOf(t, projector.ID))
		body := e.fake.lastView(t, "views.update")
		assert.Contains(t, body, "You now have")
		assert.Contains(t, body, ActionInventoryVerify, "the item shows under Items I hold")
	})

	t.Run("claiming it again is a friendly no-op", func(t *testing.T) {
		w := httptest.NewRecorder()
		handleInventoryClaim(e.ctx, w, e.pool, e.client, blockActionPayload(t, "U_ALICE"), projector.ID)
		assert.Contains(t, e.fake.lastView(t, "views.update"), "You already have")
		events, err := store.ListInventoryCustodyEvents(e.ctx, e.pool, projector.ID)
		require.NoError(t, err)
		assert.Len(t, events, 1)
	})

	t.Run("lookup of an item you hold offers no confirm button", func(t *testing.T) {
		w := httptest.NewRecorder()
		handleInventoryCodeInput(e.ctx, w, e.pool, e.client, blockActionPayload(t, "U_ALICE"), "K7M4QX")
		body := e.fake.lastView(t, "views.update")
		assert.Contains(t, body, "You already have this item.")
		assert.NotContains(t, body, "Confirm possession")
	})

	t.Run("lookup shows who else holds it", func(t *testing.T) {
		w := httptest.NewRecorder()
		handleInventoryCodeInput(e.ctx, w, e.pool, e.client, blockActionPayload(t, "U_BOB"), "K7M4QX")
		body := e.fake.lastView(t, "views.update")
		assert.Contains(t, body, "Currently held by *Alice*")
		assert.Contains(t, body, "Confirm possession")
	})

	t.Run("verify stamps the item and only works for the holder", func(t *testing.T) {
		w := httptest.NewRecorder()
		handleInventoryVerify(e.ctx, w, e.pool, e.client, blockActionPayload(t, "U_ALICE"), projector.ID)
		assert.Contains(t, e.fake.lastView(t, "views.update"), "verified")
		events, err := store.ListInventoryCustodyEvents(e.ctx, e.pool, projector.ID)
		require.NoError(t, err)
		require.Len(t, events, 2)
		assert.Equal(t, "verify", events[0].Source)

		w = httptest.NewRecorder()
		handleInventoryVerify(e.ctx, w, e.pool, e.client, blockActionPayload(t, "U_BOB"), projector.ID)
		assert.Contains(t, e.fake.lastView(t, "views.update"), "You no longer hold that item")
		events, _ = store.ListInventoryCustodyEvents(e.ctx, e.pool, projector.ID)
		assert.Len(t, events, 2, "a non-holder's verify writes nothing")
	})

	t.Run("hand off opens a person picker only for the holder", func(t *testing.T) {
		w := httptest.NewRecorder()
		handleInventoryHandOff(e.ctx, w, e.pool, e.client, blockActionPayload(t, "U_ALICE"), projector.ID)
		assert.Contains(t, e.fake.lastView(t, "views.push"), "users_select")

		pushes := len(e.fake.viewCalls("views.push"))
		w = httptest.NewRecorder()
		handleInventoryHandOff(e.ctx, w, e.pool, e.client, blockActionPayload(t, "U_BOB"), projector.ID)
		assert.Len(t, e.fake.viewCalls("views.push"), pushes, "Bob does not hold it, so no picker")
		assert.Contains(t, e.fake.lastView(t, "views.update"), "You no longer hold that item")
	})

	t.Run("hand off to someone the app has never seen changes nothing", func(t *testing.T) {
		w := httptest.NewRecorder()
		handleInventoryHandOffSubmit(e.ctx, w, e.pool, e.client, decodePayload(t, handOffSubmitJSON("U_ALICE", projector.ID, "U_NOBODY")))
		assert.Equal(t, alice.ID, e.holderOf(t, projector.ID))
		assert.Contains(t, e.fake.lastView(t, "views.update"), "hasn't opened this app yet")
		assert.Empty(t, e.fake.postTexts(), "nobody is messaged when the hand-off fails")
	})

	t.Run("hand off applies immediately, refreshes the root view, and messages the recipient", func(t *testing.T) {
		w := httptest.NewRecorder()
		handleInventoryHandOffSubmit(e.ctx, w, e.pool, e.client, decodePayload(t, handOffSubmitJSON("U_ALICE", projector.ID, "U_BOB")))
		assert.Equal(t, bob.ID, e.holderOf(t, projector.ID))
		assert.Contains(t, e.fake.lastView(t, "views.update"), "Handed off")

		posts := e.fake.postsSnapshot()
		require.Len(t, posts, 1, "only the person receiving the item is messaged")
		assert.Equal(t, "DM_U_BOB", posts[0].Channel)
		assert.Contains(t, posts[0].Text, "<@U_ALICE> handed *Projector* (`K7M-4QX`) off to you. You now have it.")

		events, err := store.ListInventoryCustodyEvents(e.ctx, e.pool, projector.ID)
		require.NoError(t, err)
		assert.Equal(t, alice.ID, *events[0].PreviousHolderUserID)
		assert.Equal(t, "Handed off by Alice", *events[0].Note)
	})

	t.Run("a stale hand off by the old holder is rejected", func(t *testing.T) {
		w := httptest.NewRecorder()
		handleInventoryHandOffSubmit(e.ctx, w, e.pool, e.client, decodePayload(t, handOffSubmitJSON("U_ALICE", projector.ID, "U_BOB")))
		assert.Equal(t, bob.ID, e.holderOf(t, projector.ID))
		assert.Contains(t, e.fake.lastView(t, "views.update"), "You no longer hold")
		assert.Len(t, e.fake.postsSnapshot(), 1, "no second message for a rejected hand-off")
	})
}

func TestInventoryRoutingThroughDispatchers(t *testing.T) {
	e := newInvEnv(t)
	e.usePackageClient(t)
	alice := e.user(t, "U_ALICE", "Alice")
	bob := e.user(t, "U_BOB", "Bob")
	it := e.item(t, "K7M4QX", "Projector")
	_, err := store.ApplyCustodyChange(e.ctx, e.pool, store.CustodyChange{ItemID: it.ID, UserID: alice.ID, Source: "slack"})
	require.NoError(t, err)

	dispatch := func(payload string) {
		body := url.Values{"payload": {payload}}.Encode()
		w := httptest.NewRecorder()
		dispatchInteractionBody(e.ctx, w, e.pool, e.encKey, nil, []byte(body))
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	}
	action := func(actionID, value string) string {
		return fmt.Sprintf(`{"type":"block_actions","user":{"id":"U_ALICE"},"trigger_id":"trig","view":{"id":"V1","hash":"h1"},"actions":[{"type":"button","block_id":"b1","action_id":%q,"value":%q}]}`, actionID, value)
	}

	dispatch(action(ActionOpenInventory, "open"))
	assert.Contains(t, e.fake.lastView(t, "views.open"), "Items I hold", "the Home button opens the modal")

	dispatch(action(ActionInventoryCodeInput, "k7m-4qx"))
	assert.Contains(t, e.fake.lastView(t, "views.update"), "You already have this item.")

	dispatch(action(ActionInventoryVerify, it.ID))
	assert.Contains(t, e.fake.lastView(t, "views.update"), "verified")

	dispatch(action(ActionInventoryHandOff, it.ID))
	assert.Contains(t, e.fake.lastView(t, "views.push"), "users_select")

	dispatch(handOffSubmitJSON("U_ALICE", it.ID, "U_BOB"))
	assert.Equal(t, bob.ID, e.holderOf(t, it.ID), "the hand-off view submission is routed and its user pick is read")

	dispatch(strings.Replace(action(ActionInventoryClaim, it.ID), "U_ALICE", "U_ALICE", 1))
	assert.Equal(t, alice.ID, e.holderOf(t, it.ID), "the claim button is routed")
}

func TestSplitDMCommand(t *testing.T) {
	cases := []struct{ in, prefix, args string }{
		{"Checkout 9AB-WU6", "Checkout", "9AB-WU6"},
		{"  checkout    9ab-wu6  ", "checkout", "9ab-wu6"},
		{"Checkout\n9AB-WU6", "Checkout", "9AB-WU6"},
		{"Checkout", "Checkout", ""},
		{"hello there friend", "hello", "there friend"},
		{"", "", ""},
	}
	for _, c := range cases {
		prefix, args := splitDMCommand(c.in)
		assert.Equal(t, c.prefix, prefix, "%q", c.in)
		assert.Equal(t, c.args, args, "%q", c.in)
	}
}

func TestDirectMessageCheckout(t *testing.T) {
	e := newInvEnv(t)
	alice := e.user(t, "U_ALICE", "Alice")
	it := e.item(t, "K7M4QX", "Projector")
	run := func(slackUser, text string) {
		handleDirectMessage(e.ctx, e.pool, e.encKey, e.client, dmEvent(slackUser, text))
	}
	lastPost := func() string {
		posts := e.fake.postTexts()
		require.NotEmpty(t, posts)
		return posts[len(posts)-1]
	}

	run("U_ALICE", "Checkout k7m-4qx")
	assert.Contains(t, lastPost(), "You now have *Projector*")
	assert.Contains(t, lastPost(), "K7M-4QX")
	assert.Equal(t, alice.ID, e.holderOf(t, it.ID))
	assert.Contains(t, e.fake.dmOpens, "U_ALICE", "the reply goes to the sender's DM")

	run("U_ALICE", "  CHECKOUT   K7M4QX ")
	assert.Contains(t, lastPost(), "You already have *Projector*")
	events, err := store.ListInventoryCustodyEvents(e.ctx, e.pool, it.ID)
	require.NoError(t, err)
	assert.Len(t, events, 1, "the prefix is case-insensitive and a repeat writes nothing")

	run("U_ALICE", "Checkout NOPE22")
	assert.Contains(t, lastPost(), "No active item has the code `NOPE22`")

	run("U_ALICE", "Checkout")
	assert.Contains(t, lastPost(), "Checkout <item code>")

	require.NoError(t, store.ArchiveInventoryItem(e.ctx, e.pool, it.ID))
	run("U_ALICE", "Checkout K7M4QX")
	assert.Contains(t, lastPost(), "No active item has the code", "archived items can't be claimed")

	t.Run("a sender the app has never seen is created from their Slack profile", func(t *testing.T) {
		fresh := e.item(t, "NEW234", "Camera")
		run("U_NEWBIE", "Checkout NEW234")
		assert.Contains(t, lastPost(), "You now have *Camera*")
		u, err := store.GetUserByExternalID(e.ctx, e.pool, "slack", "U_NEWBIE")
		require.NoError(t, err)
		assert.Equal(t, u.ID, e.holderOf(t, fresh.ID))
	})
}

func TestDirectMessageWithoutPrefixListsCommands(t *testing.T) {
	e := newInvEnv(t)

	for _, text := range []string{"hello there", "9AB-WU6", "can I take the projector?", "Check out 9AB-WU6"} {
		before := len(e.fake.postTexts())
		handleDirectMessage(e.ctx, e.pool, e.encKey, e.client, dmEvent("U_ALICE", text))
		posts := e.fake.postTexts()
		require.Len(t, posts, before+1, "%q", text)
		reply := posts[len(posts)-1]
		assert.Contains(t, reply, "not a chat bot", "%q", text)
		assert.Contains(t, reply, "`Checkout <item code>`", "%q", text)
		assert.Contains(t, reply, "`Checkout K7M-4QX`", "%q", text)
	}
}

func TestDirectMessageIgnoresEverythingElse(t *testing.T) {
	e := newInvEnv(t)

	cases := map[string]slackEventInner{
		"a bot's own message": func() slackEventInner { ev := dmEvent("U_ALICE", "Checkout K7M4QX"); ev.BotID = "B123"; return ev }(),
		"an edit": func() slackEventInner {
			ev := dmEvent("U_ALICE", "Checkout K7M4QX")
			ev.Subtype = "message_changed"
			return ev
		}(),
		"a file share": func() slackEventInner {
			ev := dmEvent("U_ALICE", "Checkout K7M4QX")
			ev.Subtype = "file_share"
			return ev
		}(),
		"a channel message": func() slackEventInner {
			ev := dmEvent("U_ALICE", "Checkout K7M4QX")
			ev.ChannelType = "channel"
			return ev
		}(),
		"empty text": dmEvent("U_ALICE", "   "),
		"no sender":  dmEvent("", "Checkout K7M4QX"),
	}
	for name, ev := range cases {
		handleDirectMessage(e.ctx, e.pool, e.encKey, e.client, ev)
		assert.Empty(t, e.fake.postTexts(), name)
	}
}

func TestDirectMessageRoutedFromEvents(t *testing.T) {
	e := newInvEnv(t)
	e.usePackageClient(t)
	alice := e.user(t, "U_ALICE", "Alice")
	it := e.item(t, "K7M4QX", "Projector")

	body := `{"type":"event_callback","event":{"type":"message","user":"U_ALICE","channel":"D1","channel_type":"im","text":"Checkout K7M-4QX"}}`
	dispatchEvent(e.ctx, e.pool, e.encKey, []byte(body))

	assert.Eventually(t, func() bool { return len(e.fake.postTexts()) > 0 }, 2e9, 20e6, "the message event reaches the DM handler")
	assert.Equal(t, alice.ID, e.holderOf(t, it.ID))
	assert.Contains(t, e.fake.postTexts()[0], "You now have *Projector*")
}

func TestDMHelpListsRegisteredPrefixes(t *testing.T) {
	saved := dmCommands
	t.Cleanup(func() { dmCommands = saved })

	assert.Contains(t, dmHelpText(), "`Checkout <item code>`")

	registerDMCommand(dmCommand{Prefix: "Status", Usage: "Status <thing>", Help: "check on a thing", Example: "Status widget"})
	help := dmHelpText()
	assert.Contains(t, help, "`Checkout <item code>`")
	assert.Contains(t, help, "`Status <thing>` — check on a thing. Example: `Status widget`", "later features add commands to the list")
}

func TestRegisterWorkspaceIdentifiers(t *testing.T) {
	e := newInvEnv(t)

	registerWorkspaceIdentifiers(e.ctx, e.pool, e.encKey, e.client)
	team, err := store.GetServiceConfig(e.ctx, e.pool, "slack", "team_id", e.encKey)
	require.NoError(t, err)
	assert.Equal(t, "T123", team)
	app, err := store.GetServiceConfig(e.ctx, e.pool, "slack", "app_id", e.encKey)
	require.NoError(t, err)
	assert.Equal(t, "A123", app)

	t.Run("a bots.info failure still keeps the team ID and does not panic", func(t *testing.T) {
		require.NoError(t, store.DeleteServiceConfig(e.ctx, e.pool, "slack", "team_id"))
		require.NoError(t, store.DeleteServiceConfig(e.ctx, e.pool, "slack", "app_id"))
		e.fake.mu.Lock()
		e.fake.botsInfoFail = true
		e.fake.mu.Unlock()

		registerWorkspaceIdentifiers(e.ctx, e.pool, e.encKey, e.client)
		team, err := store.GetServiceConfig(e.ctx, e.pool, "slack", "team_id", e.encKey)
		require.NoError(t, err)
		assert.Equal(t, "T123", team)
		_, err = store.GetServiceConfig(e.ctx, e.pool, "slack", "app_id", e.encKey)
		assert.ErrorIs(t, err, store.ErrNotFound)
	})
}

func TestManifestSubscribesToDirectMessages(t *testing.T) {
	manifest := buildManifestYAML("Acme", "https://commons.example.com", "slack-events", "slack-interactions")
	assert.Contains(t, manifest, "- message.im")
	assert.Contains(t, manifest, "- app_home_opened")
	assert.Contains(t, manifest, "- team_join")
	assert.Contains(t, manifest, "im:history", "the scope for DM events was already requested")
	assert.Contains(t, manifest, "messages_tab_enabled: true")
}

func TestInventoryHeldListPaging(t *testing.T) {
	e := newInvEnv(t)
	e.usePackageClient(t)
	alice := e.user(t, "U_ALICE", "Alice")
	var first string
	for i := range 25 {
		it, err := store.CreateInventoryItemWithNewCode(e.ctx, e.pool, store.InventoryItemInput{Name: fmt.Sprintf("Thing %02d", i)})
		require.NoError(t, err)
		_, err = store.ApplyCustodyChange(e.ctx, e.pool, store.CustodyChange{ItemID: it.ID, UserID: alice.ID, Source: "admin"})
		require.NoError(t, err)
		if i == 12 {
			first = it.ID // on page 2
		}
	}

	dispatch := func(actionID, value, viewBlocks string) {
		payload := fmt.Sprintf(`{"type":"block_actions","user":{"id":"U_ALICE"},"trigger_id":"trig","view":{"id":"V1","hash":"h1","blocks":[%s]},"actions":[{"type":"button","block_id":"b1","action_id":%q,"value":%q}]}`, viewBlocks, actionID, value)
		w := httptest.NewRecorder()
		dispatchInteractionBody(e.ctx, w, e.pool, e.encKey, nil, []byte(url.Values{"payload": {payload}}.Encode()))
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	}
	onPage := func(n int) string {
		return fmt.Sprintf(`{"type":"actions","block_id":"%s%d","elements":[]}`, inventoryHeldPagePrefix, n)
	}

	dispatch(ActionOpenInventory, "open", "")
	body := e.fake.lastView(t, "views.open")
	assert.Contains(t, body, "1–10 of 25")

	dispatch(ActionInventoryHeldNext, "page:2", onPage(1))
	body = e.fake.lastView(t, "views.update")
	assert.Contains(t, body, "11–20 of 25")
	assert.Contains(t, body, "Thing 12")
	assert.NotContains(t, body, "Thing 00")

	dispatch(ActionInventoryHeldNext, "page:3", onPage(2))
	assert.Contains(t, e.fake.lastView(t, "views.update"), "21–25 of 25")

	dispatch(ActionInventoryHeldPrev, "page:garbled", onPage(3))
	assert.Contains(t, e.fake.lastView(t, "views.update"), "1–10 of 25", "a garbled page means the first")

	t.Run("verifying keeps the page you are on", func(t *testing.T) {
		dispatch(ActionInventoryVerify, first, onPage(2))
		body := e.fake.lastView(t, "views.update")
		assert.Contains(t, body, "verified")
		assert.Contains(t, body, "11–20 of 25")
	})
}
