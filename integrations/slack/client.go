package slack

import (
	"context"
	"fmt"
	"sync"

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
	channels, _, err := client.GetConversationsContext(ctx, params)
	if err != nil {
		return nil, err
	}
	out := make([]SlackChannel, len(channels))
	for i, ch := range channels {
		out[i] = SlackChannel{ID: ch.ID, Name: ch.Name}
	}
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

// PostChannelMessage posts a plain text message to a Slack channel by ID.
func PostChannelMessage(ctx context.Context, channel, text string) error {
	client := getClient(ctx)
	if client == nil {
		return fmt.Errorf("bot_token not configured")
	}
	_, _, err := client.PostMessageContext(ctx, channel, slacklib.MsgOptionText(text, false))
	return err
}

// SendDM opens a direct message channel with a Slack user and sends a message.
// Errors are returned but callers should log and continue — a failed DM should
// not crash the upload pipeline or interrupt other notifications.
// SendDM opens a DM with userSlackID and posts a message.
// Returns the DM channel ID and message timestamp so callers can update the message later.
func SendDM(ctx context.Context, client *slacklib.Client, userSlackID, text string, blocks ...slacklib.Block) (channelID, timestamp string, err error) {
	ch, _, _, err := client.OpenConversationContext(ctx, &slacklib.OpenConversationParameters{
		Users: []string{userSlackID},
	})
	if err != nil {
		return "", "", fmt.Errorf("open DM with %s: %w", userSlackID, err)
	}

	opts := []slacklib.MsgOption{slacklib.MsgOptionText(text, false)}
	if len(blocks) > 0 {
		opts = append(opts, slacklib.MsgOptionBlocks(blocks...))
	}

	_, ts, err := client.PostMessageContext(ctx, ch.ID, opts...)
	return ch.ID, ts, err
}
