package slack

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	slacklib "github.com/slack-go/slack"

	"commons/store"
)

// Action IDs for Block Kit interactions.
const (
	ActionOpenResources          = "open_resources"
	ActionOpenQuickLinks         = "open_quick_links"
	ActionOpenContacts           = "open_contacts"
	ActionOpenPendingRequests    = "open_pending_requests"
	ActionRequestChannelAccess   = "request_channel_access"
	ActionApproveChannelRequest  = "approve_channel_request"
	ActionDeclineChannelRequest  = "decline_channel_request"
	ActionSelectResourceCategory = "select_resource_category"
	ActionRefreshHome            = "refresh_home"
	ActionOpenLegislation        = "open_legislation"
	ActionSubscribeBill          = "subscribe_bill"
	ActionUnsubscribeBill        = "unsubscribe_bill"
	ActionSelectCalendar         = "select_calendar" // interaction value is the calendar UUID

	// Calendar event management — only for manually-managed calendars.
	ActionAddCalendarEvent    = "add_calendar_event"    // opens create modal; value = calendarID
	ActionCalendarShowList    = "calendar_show_list"    // opens event list modal; value = calendarID
	ActionCalendarEventEdit   = "calendar_event_edit"   // push edit modal; value = eventID
	ActionCalendarEventDelete = "calendar_event_delete" // delete event; value = eventID
)

// Modal callback IDs.
const (
	ModalIDChannelRequest  = "channel_access_request"
	ModalIDResourceBrowser = "resource_browser"
	ModalIDQuickLinks      = "quick_links"
	ModalIDContacts        = "contacts"
	ModalIDPendingRequests = "pending_requests"
	ModalIDLegislation     = "legislation"
	ModalIDScheduleMeeting = "schedule_meeting_modal"
	ModalIDReportIssue     = "report_issue_submit"
	ModalIDManageMeetings  = "manage_meetings_modal"
	ModalIDEditMeeting     = "edit_meeting_modal"
	ModalIDEditOccurrence  = "edit_occurrence_modal"

	// Calendar event management modal callback IDs.
	ModalIDCalendarCreateEvent = "calendar_create_event"
	ModalIDCalendarEventList   = "calendar_event_list"
	ModalIDCalendarEditEvent   = "calendar_edit_event"
)

// ActionScheduleMeeting opens the schedule meeting modal from App Home.
const ActionScheduleMeeting = "schedule_meeting"

// ActionReportIssue opens the report issue / feature request modal from App Home.
const ActionReportIssue = "report_issue"

// Meeting management action IDs.
const (
	ActionManageMeetings  = "manage_meetings"
	ActionMeetingOverflow = "meeting_overflow"
)

// Library action IDs — member flow.
const (
	ActionOpenLibrary       = "open_library"
	ActionLibrarySearch     = "library_search_input" // dispatch_action element action_id
	ActionLibraryCheckout   = "library_checkout"
	ActionLibraryHold       = "library_hold"
	ActionLibraryCancelHold = "library_cancel_hold"
	ActionLibraryMyBooks    = "library_my_books"
	ActionLibraryPaginate   = "library_paginate"
)

// Library action IDs — admin flow.
const (
	ActionManageLibrary = "manage_library"
	// Tab buttons each need a unique action_id (Slack rejects duplicates in an actions block).
	ActionLibraryAdminTabRequests = "library_admin_tab_requests"
	ActionLibraryAdminTabActive   = "library_admin_tab_active"
	ActionLibraryAdminTabHolds    = "library_admin_tab_holds"
	ActionLibraryAdminTabOverdue  = "library_admin_tab_overdue"
	ActionLibraryAdminTabCatalog  = "library_admin_tab_catalog"
	ActionLibraryApprove          = "library_approve"           // value: checkoutID
	ActionLibraryDeny             = "library_deny"              // value: checkoutID
	ActionLibraryMarkReturned     = "library_mark_returned"     // value: checkoutID
	ActionLibraryExtendDue        = "library_extend_due"        // value: checkoutID
	ActionLibraryNotifyHold       = "library_notify_hold"       // value: holdID
	ActionLibraryCancelHoldAdmin  = "library_cancel_hold_admin" // value: holdID
	ActionLibraryAddBook          = "library_add_book"
	ActionLibraryEditBook         = "library_edit_book"       // value: bookID
	ActionLibraryManageCopies     = "library_manage_copies"   // value: bookID
	ActionLibraryAddCopy          = "library_add_copy"        // value: bookID
	ActionLibraryDeactivateCopy   = "library_deactivate_copy" // value: copyID|bookID
	ActionLibraryAdminSearch      = "library_admin_search"    // dispatch_action
	ActionLibraryAdminPaginate    = "library_admin_paginate"  // value: "page:N"
)

// Library modal callback IDs — member flow.
const (
	ModalIDLibraryBrowse   = "library_browse"
	ModalIDLibraryCheckout = "library_checkout_confirm"
	ModalIDLibraryMyBooks  = "library_my_books"
)

// Library modal callback IDs — admin flow.
const (
	ModalIDLibraryAdmin        = "library_admin"
	ModalIDLibraryApprove      = "library_approve"
	ModalIDLibraryDeny         = "library_deny"
	ModalIDLibraryAddBook      = "library_add_book"
	ModalIDLibraryEditBook     = "library_edit_book"
	ModalIDLibraryManageCopies = "library_manage_copies"
	ModalIDLibraryExtendDue    = "library_extend_due"
)

var (
	mu           sync.Mutex
	pkgPool      *pgxpool.Pool
	pkgEncKey    []byte
	cachedToken  string
	cachedClient *slacklib.Client
)

// sendInterval is the minimum spacing enforced between Slack message posts to
// the same destination (channel or DM) to avoid hitting Slack's per-channel
// rate limits, which cause message delivery failures.
// Override in tests to keep them fast.
var sendInterval = 5 * time.Second

// defaultRetryAfter is the backoff applied when Slack returns a 429 without a
// Retry-After header (e.g. a workspace-level message limit), where Slack gives
// no explicit wait duration.
var defaultRetryAfter = 5 * time.Second

// sendThrottle tracks the send cadence for a single destination (channel or
// DM): the minimum spacing between posts, plus any active rate-limit backoff
// recorded after a Slack 429 response.
type sendThrottle struct {
	mu               sync.Mutex
	lastTime         time.Time // last time a send was allowed through
	rateLimitedUntil time.Time // if set, no sends to this destination until this time
}

var (
	throttlesMu sync.Mutex
	throttles   = map[string]*sendThrottle{}
)

// throttleFor returns the throttle for the given destination key, creating it
// on first use. The key is a Slack channel ID for channel posts, or a user ID
// for DMs (a DM channel is 1:1 with its user, so the user ID is a stable proxy
// for the DM channel).
func throttleFor(key string) *sendThrottle {
	throttlesMu.Lock()
	defer throttlesMu.Unlock()
	t, ok := throttles[key]
	if !ok {
		t = &sendThrottle{}
		throttles[key] = t
	}
	return t
}

// throttleSlackSend blocks until both the minimum spacing (sendInterval since
// the last send) and any active rate-limit backoff for the destination have
// elapsed, then records the send time. Per-destination locking means posts to
// different channels proceed independently. Returns ctx.Err() if the context is
// cancelled while waiting.
func throttleSlackSend(ctx context.Context, key string) error {
	t := throttleFor(key)
	t.mu.Lock()
	defer t.mu.Unlock()
	now := time.Now()
	// Earliest we may send: sendInterval after the last send.
	waitUntil := t.lastTime.Add(sendInterval)
	// Honor any active rate-limit backoff (e.g. from a 429 Retry-After).
	if t.rateLimitedUntil.After(waitUntil) {
		waitUntil = t.rateLimitedUntil
	}
	if waitUntil.After(now) {
		select {
		case <-time.After(waitUntil.Sub(now)):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	t.lastTime = time.Now()
	return nil
}

// markRateLimited records that Slack rate-limited sends to the given
// destination, blocking subsequent sends for at least retryAfter. Only extends
// the backoff (never shortens an existing one set by a concurrent request).
func markRateLimited(key string, retryAfter time.Duration) {
	t := throttleFor(key)
	t.mu.Lock()
	defer t.mu.Unlock()
	if deadline := time.Now().Add(retryAfter); deadline.After(t.rateLimitedUntil) {
		t.rateLimitedUntil = deadline
	}
}

// recordRateLimitIfAny inspects an error returned by a Slack send call. If Slack
// rate-limited the request (HTTP 429), it records the backoff for the
// destination so subsequent sends wait rather than failing repeatedly: the
// Retry-After duration when Slack provides one, or defaultRetryAfter otherwise.
// The original error is returned unchanged for the caller to handle or log.
func recordRateLimitIfAny(key string, err error) error {
	if err == nil {
		return nil
	}
	var rl *slacklib.RateLimitedError
	if errors.As(err, &rl) {
		markRateLimited(key, rl.RetryAfter)
		return err
	}
	var sce slacklib.StatusCodeError
	if errors.As(err, &sce) && sce.Code == http.StatusTooManyRequests {
		markRateLimited(key, defaultRetryAfter)
		return err
	}
	return err
}

// Init stores the pool and encryption key for bot token lookups.
// Must be called from main before the server starts.
func Init(pool *pgxpool.Pool, encKey []byte) {
	pkgPool = pool
	pkgEncKey = encKey
}

// getClient reads the bot token from config_store and returns a Slack API client.
// The client is cached and only rebuilt when the token changes.
// Returns nil if the token is not yet configured.
func getClient(ctx context.Context) *slacklib.Client {
	if pkgPool == nil {
		return nil
	}
	token, err := store.GetServiceConfig(ctx, pkgPool, "slack", "bot_token", pkgEncKey)
	if err != nil || token == "" {
		return nil
	}
	mu.Lock()
	defer mu.Unlock()
	if token != cachedToken {
		cachedToken = token
		cachedClient = slacklib.New(token)
	}
	return cachedClient
}

// SlackChannel is a minimal channel descriptor returned by ListChannels.
type SlackChannel struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ListChannels returns all non-archived public and private channels visible to the bot.
func ListChannels(ctx context.Context) ([]SlackChannel, error) {
	client := getClient(ctx)
	if client == nil {
		return nil, fmt.Errorf("bot_token not configured")
	}
	params := &slacklib.GetConversationsParameters{
		Types:           []string{"public_channel", "private_channel"},
		Limit:           1000,
		ExcludeArchived: true,
	}
	var out []SlackChannel
	for {
		channels, cursor, err := client.GetConversationsContext(ctx, params)
		if err != nil {
			return nil, err
		}
		for _, ch := range channels {
			out = append(out, SlackChannel{ID: ch.ID, Name: ch.Name})
		}
		if cursor == "" {
			break
		}
		params.Cursor = cursor
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// SlackEmoji is a minimal emoji descriptor returned by ListEmojis.
type SlackEmoji struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

// ListEmojis returns all custom emojis in the workspace via emoji.list.
// Aliases (whose value is "alias:target") have an empty URL since they have no
// image of their own.
func ListEmojis(ctx context.Context) ([]SlackEmoji, error) {
	client := getClient(ctx)
	if client == nil {
		return nil, fmt.Errorf("bot_token not configured")
	}
	emojiMap, err := client.GetEmojiContext(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]SlackEmoji, 0, len(emojiMap))
	for name, url := range emojiMap {
		if strings.HasPrefix(url, "alias:") {
			url = ""
		}
		out = append(out, SlackEmoji{Name: name, URL: url})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// PostDirectMessage sends a plain text DM to a Slack user by ID.
func PostDirectMessage(ctx context.Context, userID, text string) error {
	client := getClient(ctx)
	if client == nil {
		return fmt.Errorf("bot_token not configured")
	}
	_, _, err := SendDM(ctx, client, userID, text)
	return err
}

// postChannelMessage throttles and posts a plain text message to a channel. It
// records rate-limit backoffs but does NOT enqueue on failure — the caller (or
// the retry drainer) decides what to do with the error.
func postChannelMessage(ctx context.Context, client *slacklib.Client, channel, text string) error {
	if err := throttleSlackSend(ctx, channel); err != nil {
		return fmt.Errorf("throttle slack send: %w", err)
	}
	_, _, err := client.PostMessageContext(ctx, channel, slacklib.MsgOptionText(text, false))
	return recordRateLimitIfAny(channel, err)
}

// PostChannelMessage posts a plain text message to a Slack channel by ID. On a
// transient failure (rate limit or 5xx) the message is persisted to the retry
// queue for redelivery; the original error is still returned.
func PostChannelMessage(ctx context.Context, channel, text string) error {
	client := getClient(ctx)
	if client == nil {
		return fmt.Errorf("bot_token not configured")
	}
	err := postChannelMessage(ctx, client, channel, text)
	if err != nil {
		enqueueRetryableIfFailed(ctx, pkgPool, channel, false, text, nil, err)
	}
	return err
}

// sendDM throttles, opens a DM channel, and posts a message. It records
// rate-limit backoffs but does NOT enqueue on failure — the caller (or the
// retry drainer) decides what to do with the error.
func sendDM(ctx context.Context, client *slacklib.Client, userSlackID, text string, blocks ...slacklib.Block) (channelID, timestamp string, err error) {
	if err := throttleSlackSend(ctx, userSlackID); err != nil {
		return "", "", fmt.Errorf("throttle slack send: %w", err)
	}
	ch, _, _, err := client.OpenConversationContext(ctx, &slacklib.OpenConversationParameters{
		Users: []string{userSlackID},
	})
	if err != nil {
		recordRateLimitIfAny(userSlackID, err)
		return "", "", fmt.Errorf("open DM with %s: %w", userSlackID, err)
	}

	opts := []slacklib.MsgOption{slacklib.MsgOptionText(text, false)}
	if len(blocks) > 0 {
		opts = append(opts, slacklib.MsgOptionBlocks(blocks...))
	}

	_, ts, err := client.PostMessageContext(ctx, ch.ID, opts...)
	return ch.ID, ts, recordRateLimitIfAny(userSlackID, err)
}

// SendDM opens a DM with userSlackID and posts a message. On a transient
// failure (rate limit or 5xx) the message is persisted to the retry queue so
// the drainer can redeliver it; the original error is still returned.
// Returns the DM channel ID and message timestamp so callers can update the message later.
func SendDM(ctx context.Context, client *slacklib.Client, userSlackID, text string, blocks ...slacklib.Block) (channelID, timestamp string, err error) {
	chID, ts, err := sendDM(ctx, client, userSlackID, text, blocks...)
	if err != nil {
		enqueueRetryableIfFailed(ctx, pkgPool, userSlackID, true, text, blocks, err)
	}
	return chID, ts, err
}
