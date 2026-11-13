package adminui

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"commons/internal/testhelpers"
	"commons/store"
	"commons/web"
)

func claimForm(name, email string, confirm bool) url.Values {
	v := url.Values{"name": {name}, "email": {email}}
	if confirm {
		v.Set("confirm", "on")
	}
	return v
}

// expectCall waits for the next notification hook call and checks its (item, member) arguments.
func expectCall(t *testing.T, ch chan [2]string, want [2]string) {
	t.Helper()
	select {
	case got := <-ch:
		assert.Equal(t, want, got)
	case <-time.After(2 * time.Second):
		t.Fatal("the member was not notified")
	}
}

// assertNoCall checks that no notification hook call is pending or arrives shortly.
func assertNoCall(t *testing.T, ch chan [2]string) {
	t.Helper()
	select {
	case got := <-ch:
		t.Fatalf("unexpected notification for %v", got)
	case <-time.After(150 * time.Millisecond):
	}
}

func postClaim(t *testing.T, d Deps, code string, form url.Values, mutate ...func(*invReq)) *httptest.ResponseRecorder {
	t.Helper()
	rq := invReq{method: "POST", target: "/inventory/" + code, form: form, path: map[string]string{"code": code}}
	for _, m := range mutate {
		m(&rq)
	}
	return invDo(t, d.InventoryClaimSubmit(), rq)
}

func TestInventoryClaimPage(t *testing.T) {
	d := Deps{Pool: testhelpers.SetupTestDB(t), EncKey: testhelpers.EncKey()}
	it, err := store.CreateInventoryItem(t.Context(), d.Pool, store.InventoryItemInput{Name: "Projector"}, "K7M4QX")
	require.NoError(t, err)

	get := func(code string) *httptest.ResponseRecorder {
		return invDo(t, d.InventoryClaimPage(), invReq{method: "GET", target: "/inventory/" + code, path: map[string]string{"code": code}})
	}

	t.Run("shows the item, a form, and how to check in through Slack", func(t *testing.T) {
		w := get("k7m-4qx")
		require.Equal(t, http.StatusOK, w.Code)
		body := w.Body.String()
		assert.Contains(t, body, "Projector")
		assert.Contains(t, body, "K7M-4QX")
		assert.Contains(t, body, `action="/inventory/K7M4QX"`, "posts back to the canonical code")
		assert.Contains(t, body, `name="name"`)
		assert.Contains(t, body, `name="email"`)
		assert.Contains(t, body, `name="confirm"`)
		assert.Contains(t, body, `name="website"`, "honeypot field")
		assert.Contains(t, body, "<code>Checkout K7M-4QX</code>", "a text note about Slack")
		assert.NotContains(t, body, "slack://", "no Slack link")
		assert.NotContains(t, body, "/admin/inventory", "no links into the admin")
		assert.NotContains(t, body, "Skip to main content", "no admin chrome")
		assert.NotContains(t, body, "sidebar", "no admin chrome")
		assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
		assert.Contains(t, w.Header().Get("X-Robots-Tag"), "noindex")
		assert.Contains(t, body, `name="robots"`)
	})

	t.Run("unknown and archived codes are 404", func(t *testing.T) {
		w := get("NOPE22")
		assert.Equal(t, http.StatusNotFound, w.Code)
		assert.Contains(t, w.Body.String(), "couldn't find that item")

		require.NoError(t, store.ArchiveInventoryItem(t.Context(), d.Pool, it.ID))
		assert.Equal(t, http.StatusNotFound, get("K7M4QX").Code)
	})
}

func TestInventoryClaimSubmit(t *testing.T) {
	pool := testhelpers.SetupTestDB(t)
	notified := make(chan string, 4)
	applied := make(chan [2]string, 8)
	d := Deps{
		Pool:                 pool,
		EncKey:               testhelpers.EncKey(),
		ClaimLimiter:         web.NewRateLimiter(),
		NotifyUnmatchedClaim: func(_ context.Context, id string) { notified <- id },
		NotifyClaimApplied:   func(_ context.Context, itemID, userID string) { applied <- [2]string{itemID, userID} },
	}
	ctx := t.Context()
	// These subtests share one client address; the limits have their own test below.
	require.NoError(t, store.SetServiceConfig(ctx, pool, "inventory", "claim_rate_limit_per_hour", "1000", false, nil, d.EncKey))

	alice := invUser(t, pool, "Alice")
	invExec(t, pool, `UPDATE users SET email = 'alice@example.com' WHERE id = $1`, alice)
	viaSlack := invUser(t, pool, "Via Slack")
	invExec(t, pool, `INSERT INTO user_identities (user_id, provider, external_id, external_email)
		VALUES ($1, 'slack', 'U9', 'slack.person@example.com')`, viaSlack)

	it, err := store.CreateInventoryItem(ctx, pool, store.InventoryItemInput{Name: "Projector"}, "K7M4QX")
	require.NoError(t, err)

	const confirmation = "Thanks, your check-in has been recorded."

	var matchedBody string
	t.Run("a matching email takes possession and records what was typed", func(t *testing.T) {
		w := postClaim(t, d, "k7m-4qx", claimForm("Alice A", " ALICE@example.com ", true), func(r *invReq) {
			r.remoteAddr = "203.0.113.7:5555"
			r.userAgent = "TestBrowser/1.0"
		})
		require.Equal(t, http.StatusOK, w.Code)
		matchedBody = w.Body.String()
		assert.Contains(t, matchedBody, confirmation)
		assert.NotContains(t, matchedBody, `<form`, "the form is replaced by the confirmation")

		got, _ := store.GetInventoryItem(ctx, pool, it.ID)
		assert.Equal(t, alice, *got.CurrentHolderUserID)
		evs, err := store.ListInventoryCustodyEvents(ctx, pool, it.ID)
		require.NoError(t, err)
		require.Len(t, evs, 1)
		assert.Equal(t, "web_form", evs[0].Source)
		assert.Equal(t, "applied", evs[0].Status)
		assert.Equal(t, "Alice A", *evs[0].RawName)
		assert.Equal(t, "ALICE@example.com", *evs[0].RawEmail)
		assert.Equal(t, "203.0.113.7", *evs[0].IPAddress)
		assert.Equal(t, "TestBrowser/1.0", *evs[0].UserAgent)
		assert.Empty(t, notified, "admins are only told about unmatched claims")
		expectCall(t, applied, [2]string{it.ID, alice})
	})

	t.Run("an email that matches no member is held for an admin and looks identical to the visitor", func(t *testing.T) {
		w := postClaim(t, d, "K7M4QX", claimForm("Robert", "bob@elsewhere.example", true))
		require.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, matchedBody, w.Body.String(), "the page can't be used to learn who is a member")

		got, _ := store.GetInventoryItem(ctx, pool, it.ID)
		assert.Equal(t, alice, *got.CurrentHolderUserID, "the holder is unchanged until an admin links the claim")

		pending, err := store.ListPendingUnmatched(ctx, pool)
		require.NoError(t, err)
		require.Len(t, pending, 1)
		assert.Equal(t, "Robert", *pending[0].RawName)
		assert.Equal(t, "bob@elsewhere.example", *pending[0].RawEmail)
		assert.Equal(t, alice, *pending[0].PreviousHolderUserID)

		select {
		case id := <-notified:
			assert.Equal(t, pending[0].ID, id)
		case <-time.After(2 * time.Second):
			t.Fatal("admins were not notified")
		}
	})

	t.Run("a member's Slack profile email also matches", func(t *testing.T) {
		w := postClaim(t, d, "K7M4QX", claimForm("Via", "Slack.Person@example.com", true))
		require.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, matchedBody, w.Body.String())
		got, _ := store.GetInventoryItem(ctx, pool, it.ID)
		assert.Equal(t, viaSlack, *got.CurrentHolderUserID)
		expectCall(t, applied, [2]string{it.ID, viaSlack})
	})

	t.Run("claiming something you already hold records nothing and looks the same", func(t *testing.T) {
		before, _ := store.ListInventoryCustodyEvents(ctx, pool, it.ID)
		w := postClaim(t, d, "K7M4QX", claimForm("Via", "slack.person@example.com", true))
		require.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, matchedBody, w.Body.String())
		after, _ := store.ListInventoryCustodyEvents(ctx, pool, it.ID)
		assert.Len(t, after, len(before))
	})

	t.Run("the honeypot records nothing but looks like success", func(t *testing.T) {
		before, _ := store.ListInventoryCustodyEvents(ctx, pool, it.ID)
		pendingBefore, _ := store.CountPendingUnmatched(ctx, pool)
		form := claimForm("Spam", "alice@example.com", true)
		form.Set("website", "http://spam.example")
		w := postClaim(t, d, "K7M4QX", form)
		require.Equal(t, http.StatusOK, w.Code)
		assert.Contains(t, w.Body.String(), confirmation)
		after, _ := store.ListInventoryCustodyEvents(ctx, pool, it.ID)
		pendingAfter, _ := store.CountPendingUnmatched(ctx, pool)
		assert.Len(t, after, len(before))
		assert.Equal(t, pendingBefore, pendingAfter)
	})

	t.Run("incomplete forms are sent back with what was typed, and record nothing", func(t *testing.T) {
		before, _ := store.ListInventoryCustodyEvents(ctx, pool, it.ID)
		cases := []struct {
			name string
			form url.Values
			want string
		}{
			{"no name", claimForm("  ", "alice@example.com", true), "Please enter your name."},
			{"bad email", claimForm("Alice", "not-an-email", true), "valid email address"},
			{"display-name email", claimForm("Alice", "Alice <alice@example.com>", true), "valid email address"},
			{"not confirmed", claimForm("Alice", "alice@example.com", false), "tick the box"},
			{"name too long", claimForm(strings.Repeat("x", 201), "alice@example.com", true), "Please enter your name."},
		}
		for _, c := range cases {
			w := postClaim(t, d, "K7M4QX", c.form)
			assert.Equal(t, http.StatusBadRequest, w.Code, c.name)
			assert.Contains(t, w.Body.String(), c.want, c.name)
			assert.Contains(t, w.Body.String(), `<form`, c.name)
		}
		after, _ := store.ListInventoryCustodyEvents(ctx, pool, it.ID)
		assert.Len(t, after, len(before))

		w := postClaim(t, d, "K7M4QX", claimForm(`<script>alert(1)</script>`, "a@b.example", false))
		assert.NotContains(t, w.Body.String(), "<script>alert(1)</script>")
		assert.Contains(t, w.Body.String(), "&lt;script&gt;alert(1)&lt;/script&gt;")
	})

	t.Run("unknown and archived codes are 404", func(t *testing.T) {
		assert.Equal(t, http.StatusNotFound, postClaim(t, d, "NOPE22", claimForm("A", "alice@example.com", true)).Code)
		gone, err := store.CreateInventoryItem(ctx, pool, store.InventoryItemInput{Name: "Retired"}, "GON222")
		require.NoError(t, err)
		require.NoError(t, store.ArchiveInventoryItem(ctx, pool, gone.ID))
		assert.Equal(t, http.StatusNotFound, postClaim(t, d, "GON222", claimForm("A", "alice@example.com", true)).Code)
	})

	t.Run("works without a limiter or a notification hook", func(t *testing.T) {
		bare := Deps{Pool: pool, EncKey: d.EncKey}
		w := postClaim(t, bare, "K7M4QX", claimForm("Nobody", "nobody@elsewhere.example", true))
		assert.Equal(t, http.StatusOK, w.Code)
		w = postClaim(t, bare, "K7M4QX", claimForm("Alice", "alice@example.com", true))
		assert.Equal(t, http.StatusOK, w.Code, "a matched claim with no hook is fine too")
	})

	t.Run("only a check-in that gave someone the item notifies a member", func(t *testing.T) {
		// Every positive case above consumed its call. The unmatched claim, the already-holder repeat, the
		// honeypot, the rejected forms, and the unknown codes must not have produced any.
		assertNoCall(t, applied)
	})
}

func TestInventoryClaimRateLimits(t *testing.T) {
	pool := testhelpers.SetupTestDB(t)
	d := Deps{Pool: pool, EncKey: testhelpers.EncKey(), ClaimLimiter: web.NewRateLimiter()}
	ctx := t.Context()

	_, err := store.CreateInventoryItem(ctx, pool, store.InventoryItemInput{Name: "One"}, "ONE222")
	require.NoError(t, err)
	_, err = store.CreateInventoryItem(ctx, pool, store.InventoryItemInput{Name: "Two"}, "TWO222")
	require.NoError(t, err)

	fromIP := func(ip string) func(*invReq) {
		return func(r *invReq) { r.remoteAddr = ip + ":4000" }
	}
	post := func(code, ip string) int {
		return postClaim(t, d, code, claimForm("Someone", "someone@elsewhere.example", true), fromIP(ip)).Code
	}

	t.Run("per IP, from config", func(t *testing.T) {
		require.NoError(t, store.SetServiceConfig(ctx, pool, "inventory", "claim_rate_limit_per_hour", "2", false, nil, d.EncKey))
		assert.Equal(t, http.StatusOK, post("ONE222", "198.51.100.1"))
		assert.Equal(t, http.StatusOK, post("TWO222", "198.51.100.1"))
		assert.Equal(t, http.StatusTooManyRequests, post("ONE222", "198.51.100.1"), "the third submission from one IP in an hour")
		assert.Equal(t, http.StatusOK, post("ONE222", "198.51.100.2"), "other addresses are unaffected")
	})

	t.Run("a bad config value falls back to the default of 10", func(t *testing.T) {
		require.NoError(t, store.SetServiceConfig(ctx, pool, "inventory", "claim_rate_limit_per_hour", "lots", false, nil, d.EncKey))
		assert.Equal(t, defaultClaimsPerIPPerHour, d.claimsPerIP(ctx))
	})

	t.Run("per code, across addresses", func(t *testing.T) {
		require.NoError(t, store.SetServiceConfig(ctx, pool, "inventory", "claim_rate_limit_per_hour", "1000", false, nil, d.EncKey))
		okCount := 0
		for i := 0; i < claimsPerCodePerHour+5; i++ {
			if post("TWO222", fmt.Sprintf("10.1.0.%d", i+1)) == http.StatusOK {
				okCount++
			}
		}
		// The per-IP test above already spent one of this code's attempts.
		assert.Equal(t, claimsPerCodePerHour-1, okCount, "one code can only be submitted so often, however many addresses are used")
		assert.Equal(t, http.StatusOK, post("ONE222", "10.9.9.9"), "other codes are unaffected")
	})

	t.Run("probing unknown codes counts against the IP", func(t *testing.T) {
		require.NoError(t, store.SetServiceConfig(ctx, pool, "inventory", "claim_rate_limit_per_hour", "3", false, nil, d.EncKey))
		for i := 0; i < 3; i++ {
			assert.Equal(t, http.StatusNotFound, post(fmt.Sprintf("ZZZ22%d", i), "192.0.2.50"))
		}
		assert.Equal(t, http.StatusTooManyRequests, post("ZZZ229", "192.0.2.50"))
	})
}
