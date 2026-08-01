package slack

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	slacklib "github.com/slack-go/slack"

	"commons/integrations/librarything"
	"commons/permissions"
	"commons/platform"
	"commons/store"
	"commons/util"
)

// InteractionsHandler returns an http.HandlerFunc for POST /slack/interactions.
func InteractionsHandler(pool *pgxpool.Pool, encKey []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		// Read the signing secret from config_store on each request so it can be
		// rotated via the web UI without a service restart.
		signingSecret, err := store.GetServiceConfig(ctx, pool, "slack", "signing_secret", encKey)
		if errors.Is(err, store.ErrNotFound) {
			log.Printf("slack/interactions: signing_secret not configured in config_store")
			http.Error(w, "server misconfigured", http.StatusInternalServerError)
			return
		}
		if err != nil {
			log.Printf("slack/interactions: failed to read signing_secret: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		// Verify signature while reading the body.
		verifier, err := slacklib.NewSecretsVerifier(r.Header, signingSecret)
		if err != nil {
			log.Printf("slack/interactions: build verifier: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		body, err := io.ReadAll(io.TeeReader(r.Body, &verifier))
		if err != nil {
			http.Error(w, "read error", http.StatusInternalServerError)
			return
		}
		if err := verifier.Ensure(); err != nil {
			log.Printf("slack/interactions: invalid signature")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		dispatchInteractionBody(ctx, w, pool, encKey, nil, body)
	}
}

// dispatchInteractionBody parses and routes an already-verified Slack interaction payload.
// Separated from InteractionsHandler so the pipeline action can call it without re-verifying.
func dispatchInteractionBody(ctx context.Context, w http.ResponseWriter, pool *pgxpool.Pool, encKey []byte, sched platform.MeetingScheduler, body []byte) {
	// Slack sends interactions as a form-encoded "payload" field.
	r, _ := http.NewRequestWithContext(ctx, http.MethodPost, "/", strings.NewReader(string(body)))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}

	var payload slacklib.InteractionCallback
	if err := json.Unmarshal([]byte(r.FormValue("payload")), &payload); err != nil {
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}

	client := getClient(ctx)
	if client == nil {
		log.Printf("slack/interactions: bot_token not configured")
		http.Error(w, "server misconfigured", http.StatusInternalServerError)
		return
	}

	switch payload.Type {
	case slacklib.InteractionTypeBlockActions:
		handleBlockAction(ctx, w, pool, encKey, sched, client, &payload)
	case slacklib.InteractionTypeViewSubmission:
		handleViewSubmission(ctx, w, pool, encKey, sched, client, &payload)
	default:
		w.WriteHeader(http.StatusOK)
	}
}

func handleBlockAction(ctx context.Context, w http.ResponseWriter, pool *pgxpool.Pool, encKey []byte, sched platform.MeetingScheduler, client *slacklib.Client, payload *slacklib.InteractionCallback) {
	if len(payload.ActionCallback.BlockActions) == 0 {
		w.WriteHeader(http.StatusOK)
		return
	}
	action := payload.ActionCallback.BlockActions[0]

	switch action.ActionID {

	case ActionOpenResources:
		user, err := store.GetOrCreateUserByIdentity(ctx, pool, "slack", payload.User.ID, "", "")
		if err != nil {
			log.Printf("slack/interactions: get user for resources: %v", err)
			w.WriteHeader(http.StatusOK)
			return
		}
		perms, err := store.GetUserPermissions(ctx, pool, user.ID)
		if err != nil {
			log.Printf("slack/interactions: get permissions for resources: %v", err)
			w.WriteHeader(http.StatusOK)
			return
		}
		categories, err := store.ListCategoriesForUser(ctx, pool, perms)
		if err != nil {
			log.Printf("slack/interactions: list categories: %v", err)
			w.WriteHeader(http.StatusOK)
			return
		}
		modal := ResourceCategoryModal(categories)
		if _, err := client.OpenViewContext(ctx, payload.TriggerID, modal); err != nil {
			log.Printf("slack/interactions: open resources modal: %v", err)
		}
		w.WriteHeader(http.StatusOK)

	case ActionSelectResourceCategory:
		category := action.SelectedOption.Value

		// Verify the user can access this category (defense in depth against replayed payloads).
		user, err := store.GetOrCreateUserByIdentity(ctx, pool, "slack", payload.User.ID, "", "")
		if err != nil {
			log.Printf("slack/interactions: get user for category select: %v", err)
			w.WriteHeader(http.StatusOK)
			return
		}
		perms, err := store.GetUserPermissions(ctx, pool, user.ID)
		if err != nil {
			log.Printf("slack/interactions: get permissions for category select: %v", err)
			w.WriteHeader(http.StatusOK)
			return
		}
		allowed, err := store.ListCategoriesForUser(ctx, pool, perms)
		if err != nil {
			log.Printf("slack/interactions: verify category access: %v", err)
			w.WriteHeader(http.StatusOK)
			return
		}
		accessible := false
		for _, c := range allowed {
			if c == category {
				accessible = true
				break
			}
		}
		if !accessible {
			log.Printf("slack/interactions: user %s attempted to access restricted category %q", payload.User.ID, category)
			w.WriteHeader(http.StatusOK)
			return
		}

		resources, err := store.ListResourcesByCategory(ctx, pool, category)
		if err != nil {
			log.Printf("slack/interactions: list resources for %s: %v", category, err)
			w.WriteHeader(http.StatusOK)
			return
		}
		modal := ResourceLinksModal(category, resources)
		if _, err := client.UpdateViewContext(ctx, modal, "", payload.View.Hash, payload.View.ID); err != nil {
			log.Printf("slack/interactions: update resources modal: %v", err)
		}
		w.WriteHeader(http.StatusOK)

	case ActionRequestChannelAccess:
		params := &slacklib.GetConversationsParameters{
			Types:           []string{"private_channel"},
			Limit:           200,
			ExcludeArchived: true,
		}
		channels, _, err := client.GetConversationsContext(ctx, params)
		if err != nil {
			log.Printf("slack/interactions: list private channels: %v", err)
			// Still open modal with empty channels list to show user a message
			channels = nil
		}
		modal := ChannelRequestModal(channels)
		if _, err := client.OpenViewContext(ctx, payload.TriggerID, modal); err != nil {
			log.Printf("slack/interactions: open channel request modal: %v", err)
		}
		w.WriteHeader(http.StatusOK)

	case ActionOpenQuickLinks:
		links, err := store.ListActiveQuickLinks(ctx, pool)
		if err != nil {
			log.Printf("slack/interactions: list quick links: %v", err)
			w.WriteHeader(http.StatusOK)
			return
		}
		modal := QuickLinksModal(links)
		if _, err := client.OpenViewContext(ctx, payload.TriggerID, modal); err != nil {
			log.Printf("slack/interactions: open quick links modal: %v", err)
		}
		w.WriteHeader(http.StatusOK)

	case ActionOpenContacts:
		contacts, err := store.ListContacts(ctx, pool)
		if err != nil {
			log.Printf("slack/interactions: list contacts: %v", err)
			w.WriteHeader(http.StatusOK)
			return
		}
		profilePics := make(map[string]string)
		for _, c := range contacts {
			if info, err := client.GetUserInfoContext(ctx, c.ExternalID); err == nil {
				profilePics[c.ExternalID] = info.Profile.Image192
			}
		}
		var teamID string
		if authInfo, err := client.AuthTestContext(ctx); err == nil {
			teamID = authInfo.TeamID
		}
		modal := ContactsModal(contacts, profilePics, teamID)
		if _, err := client.OpenViewContext(ctx, payload.TriggerID, modal); err != nil {
			log.Printf("slack/interactions: open contacts modal: %v", err)
		}
		w.WriteHeader(http.StatusOK)

	// ActionOpenRecentUploads removed — recordings are now accessible via the Resources button under "Meeting Recording" category.

	case ActionOpenPendingRequests:
		opener, err := store.GetUserByExternalID(ctx, pool, "slack", payload.User.ID)
		if err != nil || opener == nil {
			log.Printf("slack/interactions: pending requests: user %s not found", payload.User.ID)
			w.WriteHeader(http.StatusOK)
			return
		}
		channelIDs, err := store.GetUserChannelApprovals(ctx, pool, opener.ID)
		if err != nil {
			log.Printf("slack/interactions: list channel approvals for %s: %v", opener.ID, err)
			w.WriteHeader(http.StatusOK)
			return
		}
		pending, err := store.ListPendingRequestsForChannels(ctx, pool, channelIDs)
		if err != nil {
			log.Printf("slack/interactions: list pending requests: %v", err)
			w.WriteHeader(http.StatusOK)
			return
		}
		displays := buildRequestDisplays(ctx, pool, pending)
		modal := PendingRequestsModal(displays)
		if _, err := client.OpenViewContext(ctx, payload.TriggerID, modal); err != nil {
			log.Printf("slack/interactions: open pending requests modal: %v", err)
		}
		w.WriteHeader(http.StatusOK)

	case ActionOpenLegislation:
		user, err := store.GetOrCreateUserByIdentity(ctx, pool, "slack", payload.User.ID, "", "")
		if err != nil {
			log.Printf("slack/interactions: get user for legislation: %v", err)
			w.WriteHeader(http.StatusOK)
			return
		}
		perms, err := store.GetUserPermissions(ctx, pool, user.ID)
		if err != nil || !hasPermission(perms, permissions.LegislationView) {
			w.WriteHeader(http.StatusOK)
			return
		}
		go openLegislationModal(context.Background(), pool, client, payload.TriggerID, payload.User.ID)
		w.WriteHeader(http.StatusOK)

	case ActionSubscribeBill:
		billID := action.Value
		user, err := store.GetOrCreateUserByIdentity(ctx, pool, "slack", payload.User.ID, "", "")
		if err != nil {
			log.Printf("slack/interactions: get user for subscribe: %v", err)
			w.WriteHeader(http.StatusOK)
			return
		}
		if err := store.SubscribeUserToBill(ctx, pool, user.ID, billID); err != nil {
			log.Printf("slack/interactions: subscribe %s to bill %s: %v", payload.User.ID, billID, err)
		}
		go refreshLegislationModal(context.Background(), pool, client, payload.View.ID, payload.View.Hash, payload.User.ID)
		w.WriteHeader(http.StatusOK)

	case ActionUnsubscribeBill:
		billID := action.Value
		user, err := store.GetOrCreateUserByIdentity(ctx, pool, "slack", payload.User.ID, "", "")
		if err != nil {
			log.Printf("slack/interactions: get user for unsubscribe: %v", err)
			w.WriteHeader(http.StatusOK)
			return
		}
		if err := store.UnsubscribeUserFromBill(ctx, pool, user.ID, billID); err != nil {
			log.Printf("slack/interactions: unsubscribe %s from bill %s: %v", payload.User.ID, billID, err)
		}
		go refreshLegislationModal(context.Background(), pool, client, payload.View.ID, payload.View.Hash, payload.User.ID)
		w.WriteHeader(http.StatusOK)

	case ActionReportIssue:
		user, err := store.GetOrCreateUserByIdentity(ctx, pool, "slack", payload.User.ID, "", "")
		if err != nil {
			log.Printf("slack/interactions: get user for report issue: %v", err)
			w.WriteHeader(http.StatusOK)
			return
		}
		perms, err := store.GetUserPermissions(ctx, pool, user.ID)
		if err != nil || !hasPermission(perms, permissions.WorkItemsCreate) {
			w.WriteHeader(http.StatusOK)
			return
		}
		if _, err := client.OpenViewContext(ctx, payload.TriggerID, ReportIssueModal()); err != nil {
			log.Printf("slack/interactions: open report issue modal: %v", err)
		}
		w.WriteHeader(http.StatusOK)

	case ActionScheduleMeeting:
		user, err := store.GetOrCreateUserByIdentity(ctx, pool, "slack", payload.User.ID, "", "")
		if err != nil {
			log.Printf("slack/interactions: get user for schedule meeting: %v", err)
			w.WriteHeader(http.StatusOK)
			return
		}
		perms, err := store.GetUserPermissions(ctx, pool, user.ID)
		if err != nil || !hasPermission(perms, permissions.MeetingsSchedule) {
			w.WriteHeader(http.StatusOK)
			return
		}
		defaultTZ := util.DefaultTimezone(ctx, pool, encKey)
		if _, err := client.OpenViewContext(ctx, payload.TriggerID, ScheduleMeetingModal(defaultTZ)); err != nil {
			log.Printf("slack/interactions: open schedule meeting modal: %v", err)
		}
		w.WriteHeader(http.StatusOK)

	case ActionManageMeetings:
		user, err := store.GetOrCreateUserByIdentity(ctx, pool, "slack", payload.User.ID, "", "")
		if err != nil {
			log.Printf("slack/interactions: manage meetings: get user: %v", err)
			w.WriteHeader(http.StatusOK)
			return
		}
		perms, err := store.GetUserPermissions(ctx, pool, user.ID)
		if err != nil || !hasPermission(perms, permissions.MeetingsManage) {
			w.WriteHeader(http.StatusOK)
			return
		}
		zoomInteg, err := store.GetIntegrationByType(ctx, pool, "zoom")
		if err != nil || zoomInteg == nil {
			w.WriteHeader(http.StatusOK)
			return
		}
		meetings, err := store.ListUpcomingMeetingSummaries(ctx, pool, zoomInteg.ID)
		if err != nil {
			log.Printf("slack/interactions: manage meetings: list summaries: %v", err)
			w.WriteHeader(http.StatusOK)
			return
		}
		if _, err := client.OpenViewContext(ctx, payload.TriggerID, ManageMeetingsModal(meetings)); err != nil {
			log.Printf("slack/interactions: open manage meetings modal: %v", err)
		}
		w.WriteHeader(http.StatusOK)

	case ActionMeetingOverflow:
		user, err := store.GetOrCreateUserByIdentity(ctx, pool, "slack", payload.User.ID, "", "")
		if err != nil {
			log.Printf("slack/interactions: meeting overflow: get user: %v", err)
			w.WriteHeader(http.StatusOK)
			return
		}
		perms, err := store.GetUserPermissions(ctx, pool, user.ID)
		if err != nil || !hasPermission(perms, permissions.MeetingsManage) {
			w.WriteHeader(http.StatusOK)
			return
		}
		val := action.SelectedOption.Value
		sep := strings.Index(val, ":")
		if sep < 0 {
			w.WriteHeader(http.StatusOK)
			return
		}
		subAction, id := val[:sep], val[sep+1:]

		refreshModal := func() {
			zoomInteg, _ := store.GetIntegrationByType(ctx, pool, "zoom")
			var updated []store.MeetingSummary
			if zoomInteg != nil {
				updated, _ = store.ListUpcomingMeetingSummaries(ctx, pool, zoomInteg.ID)
			}
			if _, err := client.UpdateViewContext(ctx, ManageMeetingsModal(updated), "", payload.View.Hash, payload.View.ID); err != nil {
				log.Printf("slack/interactions: refresh manage meetings modal: %v", err)
			}
		}

		switch subAction {
		case "edit_meeting":
			m, err := store.GetScheduledMeetingByID(ctx, pool, encKey, id)
			if err != nil || m == nil {
				log.Printf("slack/interactions: edit meeting: get meeting %s: %v", id, err)
				w.WriteHeader(http.StatusOK)
				return
			}
			defaultTZ := util.DefaultTimezone(ctx, pool, encKey)
			zoomInteg, _ := store.GetIntegrationByType(ctx, pool, "zoom")
			var firstOccStart time.Time
			if zoomInteg != nil {
				occs, _ := store.ListUpcomingOccurrences(ctx, pool, zoomInteg.ID, "", "")
				for _, o := range occs {
					if o.MeetingID == m.ID {
						firstOccStart = o.StartTime
						break
					}
				}
			}
			if _, err := client.PushViewContext(ctx, payload.TriggerID, EditMeetingModal(m, firstOccStart, defaultTZ)); err != nil {
				log.Printf("slack/interactions: push edit meeting modal: %v", err)
			}
		case "cancel_meeting":
			m, err := store.GetScheduledMeetingByID(ctx, pool, encKey, id)
			if err != nil || m == nil {
				log.Printf("slack/interactions: cancel meeting: get meeting %s: %v", id, err)
				w.WriteHeader(http.StatusOK)
				return
			}
			if sched != nil {
				if err := sched.DeleteMeeting(ctx, m.ZoomMeetingID); err != nil {
					log.Printf("slack/interactions: cancel meeting: zoom delete %d: %v", m.ZoomMeetingID, err)
				}
			}
			if err := store.DeleteScheduledMeeting(ctx, pool, m.ID); err != nil {
				log.Printf("slack/interactions: cancel meeting: store delete %s: %v", m.ID, err)
			}
			refreshModal()
		case "edit_occurrence":
			occ, err := store.GetMeetingOccurrenceByID(ctx, pool, id)
			if err != nil || occ == nil {
				log.Printf("slack/interactions: edit occurrence: get occurrence %s: %v", id, err)
				w.WriteHeader(http.StatusOK)
				return
			}
			if _, err := client.PushViewContext(ctx, payload.TriggerID, EditOccurrenceModal(occ)); err != nil {
				log.Printf("slack/interactions: push edit occurrence modal: %v", err)
			}
		case "cancel_occurrence":
			occ, err := store.GetMeetingOccurrenceByID(ctx, pool, id)
			if err != nil || occ == nil {
				log.Printf("slack/interactions: cancel occurrence: get occurrence %s: %v", id, err)
				w.WriteHeader(http.StatusOK)
				return
			}
			if sched != nil {
				if err := sched.DeleteOccurrence(ctx, occ.ZoomMeetingID, occ.ZoomOccurrenceID); err != nil {
					log.Printf("slack/interactions: cancel occurrence: zoom delete %s: %v", occ.ZoomOccurrenceID, err)
				}
			}
			if err := store.DeleteMeetingOccurrence(ctx, pool, occ.OccurrenceID); err != nil {
				log.Printf("slack/interactions: cancel occurrence: store delete %s: %v", occ.OccurrenceID, err)
			}
			refreshModal()
		}
		w.WriteHeader(http.StatusOK)

	case ActionManageLibrary:
		handleManageLibrary(ctx, w, pool, encKey, client, payload)

	case ActionLibraryAdminTabRequests, ActionLibraryAdminTabActive, ActionLibraryAdminTabHolds, ActionLibraryAdminTabOverdue, ActionLibraryAdminTabCatalog:
		handleLibraryAdminTab(ctx, w, pool, encKey, client, payload, action.Value)

	case ActionLibraryApprove:
		handleLibraryApprove(ctx, w, pool, encKey, client, payload, action.Value)

	case ActionLibraryDeny:
		handleLibraryDeny(ctx, w, pool, encKey, client, payload, action.Value)

	case ActionLibraryMarkReturned:
		handleLibraryMarkReturned(ctx, w, pool, encKey, client, payload, action.Value)

	case ActionLibraryExtendDue:
		handleLibraryExtendDue(ctx, w, pool, encKey, client, payload, action.Value)

	case ActionLibraryNotifyHold:
		handleLibraryNotifyHold(ctx, w, pool, encKey, client, payload, action.Value)

	case ActionLibraryCancelHoldAdmin:
		handleLibraryCancelHoldAdmin(ctx, w, pool, encKey, client, payload, action.Value)

	case ActionLibraryAddBook:
		handleLibraryAddBook(ctx, w, pool, encKey, client, payload)

	case ActionLibraryEditBook:
		handleLibraryEditBook(ctx, w, pool, encKey, client, payload, action.Value)

	case ActionLibraryManageCopies:
		handleLibraryManageCopies(ctx, w, pool, encKey, client, payload, action.Value)

	case ActionLibraryAddCopy:
		handleLibraryAddCopy(ctx, w, pool, encKey, client, payload, action.Value)

	case ActionLibraryDeactivateCopy:
		handleLibraryDeactivateCopy(ctx, w, pool, encKey, client, payload, action.Value)

	case ActionLibraryAdminSearch:
		handleLibraryAdminSearch(ctx, w, pool, encKey, client, payload, action.Value)

	case ActionLibraryAdminPaginate:
		handleLibraryAdminPaginate(ctx, w, pool, encKey, client, payload, action.Value)

	case ActionOpenLibrary:
		user, err := store.GetOrCreateUserByIdentity(ctx, pool, "slack", payload.User.ID, "", "")
		if err != nil {
			log.Printf("slack/interactions: open library: get user: %v", err)
			w.WriteHeader(http.StatusOK)
			return
		}
		perms, err := store.GetUserPermissions(ctx, pool, user.ID)
		if err != nil || !hasPermission(perms, permissions.LibraryView) {
			w.WriteHeader(http.StatusOK)
			return
		}
		modal, err := buildLibraryBrowseModal(ctx, pool, user.ID, "", 1)
		if err != nil {
			log.Printf("slack/interactions: open library: build modal: %v", err)
			w.WriteHeader(http.StatusOK)
			return
		}
		if _, err := client.OpenViewContext(ctx, payload.TriggerID, modal); err != nil {
			log.Printf("slack/interactions: open library modal: %v", err)
		}
		w.WriteHeader(http.StatusOK)

	case ActionLibrarySearch:
		user, err := store.GetOrCreateUserByIdentity(ctx, pool, "slack", payload.User.ID, "", "")
		if err != nil {
			log.Printf("slack/interactions: library search: get user: %v", err)
			w.WriteHeader(http.StatusOK)
			return
		}
		search := action.Value
		modal, err := buildLibraryBrowseModal(ctx, pool, user.ID, search, 1)
		if err != nil {
			log.Printf("slack/interactions: library search: build modal: %v", err)
			w.WriteHeader(http.StatusOK)
			return
		}
		if _, err := client.UpdateViewContext(ctx, modal, "", payload.View.Hash, payload.View.ID); err != nil {
			log.Printf("slack/interactions: library search: update modal: %v", err)
		}
		w.WriteHeader(http.StatusOK)

	case ActionLibraryPaginate:
		user, err := store.GetOrCreateUserByIdentity(ctx, pool, "slack", payload.User.ID, "", "")
		if err != nil {
			log.Printf("slack/interactions: library paginate: get user: %v", err)
			w.WriteHeader(http.StatusOK)
			return
		}
		// Parse "page:N" from action value.
		pageNum := 1
		if strings.HasPrefix(action.Value, "page:") {
			if n, err := strconv.Atoi(strings.TrimPrefix(action.Value, "page:")); err == nil && n > 0 {
				pageNum = n
			}
		}
		// Read current search term from modal state.
		search := ""
		if stateVals := payload.View.State.Values; stateVals != nil {
			if block, ok := stateVals["library_search_block"]; ok {
				search = block[ActionLibrarySearch].Value
			}
		}
		modal, err := buildLibraryBrowseModal(ctx, pool, user.ID, search, pageNum)
		if err != nil {
			log.Printf("slack/interactions: library paginate: build modal: %v", err)
			w.WriteHeader(http.StatusOK)
			return
		}
		if _, err := client.UpdateViewContext(ctx, modal, "", payload.View.Hash, payload.View.ID); err != nil {
			log.Printf("slack/interactions: library paginate: update modal: %v", err)
		}
		w.WriteHeader(http.StatusOK)

	case ActionLibraryCheckout:
		bookID := action.Value
		user, err := store.GetOrCreateUserByIdentity(ctx, pool, "slack", payload.User.ID, "", "")
		if err != nil {
			log.Printf("slack/interactions: library checkout: get user: %v", err)
			w.WriteHeader(http.StatusOK)
			return
		}
		book, err := store.GetBook(ctx, pool, bookID)
		if err != nil || book == nil {
			log.Printf("slack/interactions: library checkout: get book %s: %v", bookID, err)
			w.WriteHeader(http.StatusOK)
			return
		}
		// Guard: check user checkout limit before opening confirm modal.
		maxStr, _ := store.GetServiceConfig(ctx, pool, "library", "max_checkouts", nil)
		maxCheckouts, _ := strconv.Atoi(maxStr)
		if maxCheckouts > 0 {
			count, err := store.UserActiveCheckoutCount(ctx, pool, user.ID)
			if err == nil && count >= maxCheckouts {
				log.Printf("slack/interactions: library checkout: user %s at max checkouts", payload.User.ID)
				w.WriteHeader(http.StatusOK)
				return
			}
		}
		if _, err := client.PushViewContext(ctx, payload.TriggerID, LibraryCheckoutConfirmModal(book)); err != nil {
			log.Printf("slack/interactions: push checkout confirm modal: %v", err)
		}
		w.WriteHeader(http.StatusOK)

	case ActionLibraryHold:
		bookID := action.Value
		user, err := store.GetOrCreateUserByIdentity(ctx, pool, "slack", payload.User.ID, "", "")
		if err != nil {
			log.Printf("slack/interactions: library hold: get user: %v", err)
			w.WriteHeader(http.StatusOK)
			return
		}
		hold, err := store.PlaceHold(ctx, pool, bookID, user.ID)
		if err != nil {
			if err == store.ErrAlreadyHasHold {
				log.Printf("slack/interactions: library hold: user already has hold on book %s", bookID)
			} else {
				log.Printf("slack/interactions: library hold: place hold: %v", err)
			}
			w.WriteHeader(http.StatusOK)
			return
		}
		// Refresh browse modal to reflect new hold state.
		search := ""
		pageNum := 1
		if stateVals := payload.View.State.Values; stateVals != nil {
			if block, ok := stateVals["library_search_block"]; ok {
				search = block[ActionLibrarySearch].Value
			}
		}
		if hold != nil {
			_ = hold // position used in MyBooks modal
		}
		modal, err := buildLibraryBrowseModal(ctx, pool, user.ID, search, pageNum)
		if err == nil {
			if _, err := client.UpdateViewContext(ctx, modal, "", payload.View.Hash, payload.View.ID); err != nil {
				log.Printf("slack/interactions: library hold: update browse modal: %v", err)
			}
		}
		w.WriteHeader(http.StatusOK)

	case ActionLibraryCancelHold:
		holdID := action.Value
		if err := store.CancelHold(ctx, pool, holdID); err != nil {
			log.Printf("slack/interactions: library cancel hold %s: %v", holdID, err)
			w.WriteHeader(http.StatusOK)
			return
		}
		// Refresh the My Library modal.
		user, err := store.GetOrCreateUserByIdentity(ctx, pool, "slack", payload.User.ID, "", "")
		if err != nil {
			log.Printf("slack/interactions: library cancel hold: get user: %v", err)
			w.WriteHeader(http.StatusOK)
			return
		}
		checkouts, _ := store.ListCheckoutsForUser(ctx, pool, user.ID)
		holds, _ := store.ListHoldsForUser(ctx, pool, user.ID)
		updatedModal := LibraryMyBooksModal(checkouts, holds)
		if _, err := client.UpdateViewContext(ctx, updatedModal, "", payload.View.Hash, payload.View.ID); err != nil {
			log.Printf("slack/interactions: library cancel hold: update my books modal: %v", err)
		}
		w.WriteHeader(http.StatusOK)

	case ActionLibraryMyBooks:
		user, err := store.GetOrCreateUserByIdentity(ctx, pool, "slack", payload.User.ID, "", "")
		if err != nil {
			log.Printf("slack/interactions: library my books: get user: %v", err)
			w.WriteHeader(http.StatusOK)
			return
		}
		checkouts, _ := store.ListCheckoutsForUser(ctx, pool, user.ID)
		holds, _ := store.ListHoldsForUser(ctx, pool, user.ID)
		if _, err := client.PushViewContext(ctx, payload.TriggerID, LibraryMyBooksModal(checkouts, holds)); err != nil {
			log.Printf("slack/interactions: push my books modal: %v", err)
		}
		w.WriteHeader(http.StatusOK)

	case ActionAddCalendarEvent:
		calendarID := action.Value
		actor, err := store.GetUserByExternalID(ctx, pool, "slack", payload.User.ID)
		if err != nil || actor == nil {
			w.WriteHeader(http.StatusOK)
			return
		}
		perms, _ := store.GetUserPermissions(ctx, pool, actor.ID)
		if !hasPermission(perms, permissions.CalendarManage) {
			w.WriteHeader(http.StatusOK)
			return
		}
		if _, err := client.OpenViewContext(ctx, payload.TriggerID, CalendarCreateEventModal(calendarID)); err != nil {
			log.Printf("slack/interactions: open calendar create modal: %v", err)
		}
		w.WriteHeader(http.StatusOK)

	case ActionCalendarShowList:
		calendarID := action.Value
		actor, err := store.GetUserByExternalID(ctx, pool, "slack", payload.User.ID)
		if err != nil || actor == nil {
			w.WriteHeader(http.StatusOK)
			return
		}
		perms, _ := store.GetUserPermissions(ctx, pool, actor.ID)
		if !hasPermission(perms, permissions.CalendarManage) {
			w.WriteHeader(http.StatusOK)
			return
		}
		events, _ := store.ListCalendarEvents(ctx, pool, calendarID)
		if _, err := client.OpenViewContext(ctx, payload.TriggerID, CalendarEventListModal(calendarID, events)); err != nil {
			log.Printf("slack/interactions: open calendar event list: %v", err)
		}
		w.WriteHeader(http.StatusOK)

	case ActionCalendarEventEdit:
		eventID := action.Value
		actor, err := store.GetUserByExternalID(ctx, pool, "slack", payload.User.ID)
		if err != nil || actor == nil {
			w.WriteHeader(http.StatusOK)
			return
		}
		perms, _ := store.GetUserPermissions(ctx, pool, actor.ID)
		if !hasPermission(perms, permissions.CalendarManage) {
			w.WriteHeader(http.StatusOK)
			return
		}
		event, err := store.GetCalendarEventByID(ctx, pool, eventID)
		if err != nil {
			log.Printf("slack/interactions: get calendar event %s: %v", eventID, err)
			w.WriteHeader(http.StatusOK)
			return
		}
		if _, err := client.PushViewContext(ctx, payload.TriggerID, CalendarEditEventModal(*event)); err != nil {
			log.Printf("slack/interactions: push calendar edit modal: %v", err)
		}
		w.WriteHeader(http.StatusOK)

	case ActionCalendarEventDelete:
		eventID := action.Value
		parts := strings.SplitN(payload.View.PrivateMetadata, "|", 2)
		if len(parts) != 2 {
			log.Printf("slack/interactions: calendar event delete: bad private metadata %q", payload.View.PrivateMetadata)
			w.WriteHeader(http.StatusOK)
			return
		}
		calendarID := parts[1]
		actor, err := store.GetUserByExternalID(ctx, pool, "slack", payload.User.ID)
		if err != nil || actor == nil {
			w.WriteHeader(http.StatusOK)
			return
		}
		perms, _ := store.GetUserPermissions(ctx, pool, actor.ID)
		if !hasPermission(perms, permissions.CalendarManage) {
			w.WriteHeader(http.StatusOK)
			return
		}
		if err := store.DeleteCalendarEvent(ctx, pool, eventID); err != nil {
			log.Printf("slack/interactions: delete calendar event %s: %v", eventID, err)
			w.WriteHeader(http.StatusOK)
			return
		}
		events, _ := store.ListCalendarEvents(ctx, pool, calendarID)
		if _, err := client.UpdateViewContext(ctx, CalendarEventListModal(calendarID, events), "", payload.View.Hash, payload.View.ID); err != nil {
			log.Printf("slack/interactions: update calendar event list after delete: %v", err)
		}
		w.WriteHeader(http.StatusOK)

	case ActionSelectCalendar:
		calendarID := action.SelectedOption.Value
		actor, err := store.GetUserByExternalID(ctx, pool, "slack", payload.User.ID)
		if err != nil || actor == nil {
			log.Printf("slack/interactions: ActionSelectCalendar from unknown user %s", payload.User.ID)
			w.WriteHeader(http.StatusOK)
			return
		}
		if err := store.UpdateUserSelectedCalendar(ctx, pool, actor.ID, calendarID); err != nil {
			log.Printf("slack/interactions: UpdateUserSelectedCalendar: %v", err)
			w.WriteHeader(http.StatusOK)
			return
		}
		go Publish(context.Background(), pool, encKey, client, payload.User.ID)
		w.WriteHeader(http.StatusOK)

	case ActionRefreshHome:
		go Publish(context.Background(), pool, encKey, client, payload.User.ID)
		w.WriteHeader(http.StatusOK)

	case ActionApproveChannelRequest:
		go handleApproveRequest(context.Background(), pool, encKey, client, payload, action.Value)
		w.WriteHeader(http.StatusOK)

	case ActionDeclineChannelRequest:
		go handleDeclineRequest(context.Background(), pool, encKey, client, payload, action.Value)
		w.WriteHeader(http.StatusOK)

	default:
		// Unknown action (e.g. URL button clicks from resource links) — acknowledge silently.
		w.WriteHeader(http.StatusOK)
	}
}

func handleViewSubmission(ctx context.Context, w http.ResponseWriter, pool *pgxpool.Pool, encKey []byte, sched platform.MeetingScheduler, client *slacklib.Client, payload *slacklib.InteractionCallback) {
	switch payload.View.CallbackID {

	case ModalIDScheduleMeeting:
		handleScheduleMeetingSubmission(ctx, w, pool, encKey, sched, client, payload)

	case ModalIDChannelRequest:
		stateVals := payload.View.State.Values
		selectedVal := stateVals["channel_input"]["selected_channel"].SelectedOption.Value

		// Value is encoded as "channelID|channelName".
		parts := strings.SplitN(selectedVal, "|", 2)
		channelID, channelName := parts[0], ""
		if len(parts) == 2 {
			channelName = parts[1]
		}

		actor, err := store.GetUserByExternalID(ctx, pool, "slack", payload.User.ID)
		if err != nil || actor == nil {
			log.Printf("slack/interactions: channel request from unknown user %s", payload.User.ID)
			w.WriteHeader(http.StatusOK)
			return
		}

		req := &store.ChannelRequest{
			RequesterID:      &actor.ID,
			SlackChannelID:   channelID,
			SlackChannelName: channelName,
		}
		if err := store.CreateChannelRequest(ctx, pool, req); err != nil {
			log.Printf("slack/interactions: create channel request: %v", err)
			w.WriteHeader(http.StatusOK)
			return
		}

		go NotifyChannelAccessRequest(context.Background(), pool, client, req)
		w.WriteHeader(http.StatusOK)

	case ModalIDEditMeeting:
		handleEditMeetingSubmission(ctx, w, pool, encKey, sched, client, payload)

	case ModalIDEditOccurrence:
		handleEditOccurrenceSubmission(ctx, w, pool, encKey, sched, payload)

	case ModalIDReportIssue:
		stateVals := payload.View.State.Values
		itemType := stateVals["type_block"]["type_select"].SelectedOption.Value
		title := stateVals["title_block"]["title_input"].Value
		var description *string
		if d := stateVals["description_block"]["description_input"].Value; d != "" {
			description = &d
		}

		actor, err := store.GetUserByExternalID(ctx, pool, "slack", payload.User.ID)
		if err != nil || actor == nil {
			log.Printf("slack/interactions: report issue from unknown user %s", payload.User.ID)
			w.WriteHeader(http.StatusOK)
			return
		}

		item, err := store.CreateWorkItem(ctx, pool, actor.ID, itemType, title, description)
		if err != nil {
			log.Printf("slack/interactions: create work item: %v", err)
			w.WriteHeader(http.StatusOK)
			return
		}

		go NotifyAdminsNewWorkItem(context.Background(), pool, client, item)
		w.WriteHeader(http.StatusOK)

	case ModalIDLibraryCheckout:
		bookID := payload.View.PrivateMetadata
		user, err := store.GetUserByExternalID(ctx, pool, "slack", payload.User.ID)
		if err != nil || user == nil {
			log.Printf("slack/interactions: library checkout submit: unknown user %s", payload.User.ID)
			w.WriteHeader(http.StatusOK)
			return
		}
		co, err := store.RequestCheckout(ctx, pool, bookID, user.ID)
		if err != nil {
			log.Printf("slack/interactions: library checkout submit: %v", err)
		}
		if co != nil {
			go NotifyNewCheckoutRequest(context.Background(), pool, user.DisplayName, co.BookTitle)
		}
		w.WriteHeader(http.StatusOK)

	case ModalIDLibraryApprove:
		handleLibraryApproveSubmit(ctx, w, pool, encKey, client, payload)

	case ModalIDLibraryDeny:
		handleLibraryDenySubmit(ctx, w, pool, encKey, client, payload)

	case ModalIDLibraryAddBook:
		handleLibraryAddBookSubmit(ctx, w, pool, encKey, client, payload)

	case ModalIDLibraryEditBook:
		handleLibraryEditBookSubmit(ctx, w, pool, encKey, client, payload)

	case ModalIDLibraryExtendDue:
		handleLibraryExtendDueSubmit(ctx, w, pool, encKey, client, payload)

	case ModalIDLibraryAdmin, ModalIDLibraryManageCopies:
		// Navigation/management modals — actions handled inline via block actions.
		w.WriteHeader(http.StatusOK)

	case ModalIDLibraryBrowse, ModalIDLibraryMyBooks:
		// Read-only modals — nothing to process on submit.
		w.WriteHeader(http.StatusOK)

	case ModalIDCalendarCreateEvent:
		calendarID := payload.View.PrivateMetadata
		actor, err := store.GetUserByExternalID(ctx, pool, "slack", payload.User.ID)
		if err != nil || actor == nil {
			w.WriteHeader(http.StatusOK)
			return
		}
		perms, _ := store.GetUserPermissions(ctx, pool, actor.ID)
		if !hasPermission(perms, permissions.CalendarManage) {
			w.WriteHeader(http.StatusOK)
			return
		}
		sv := payload.View.State.Values
		title := sv["ce_title"]["ce_title_input"].Value
		startUnix := sv["ce_start"]["ce_start_picker"].SelectedDateTime
		endUnix := sv["ce_end"]["ce_end_picker"].SelectedDateTime
		allDay := len(sv["ce_all_day"]["ce_all_day_check"].SelectedOptions) > 0

		if title == "" {
			modalError(w, "ce_title", "Title is required")
			return
		}

		event := &store.CalendarEvent{
			CalendarID: calendarID,
			Title:      title,
			StartTime:  time.Unix(startUnix, 0).UTC(),
			AllDay:     allDay,
		}
		if endUnix != 0 {
			t := time.Unix(endUnix, 0).UTC()
			event.EndTime = &t
		}
		if d := sv["ce_description"]["ce_description_input"].Value; d != "" {
			event.Description = &d
		}
		if l := sv["ce_location"]["ce_location_input"].Value; l != "" {
			event.Location = &l
		}
		if u := sv["ce_url"]["ce_url_input"].Value; u != "" {
			event.URL = &u
		}
		if err := store.CreateCalendarEvent(ctx, pool, event); err != nil {
			log.Printf("slack/interactions: create calendar event: %v", err)
		}
		w.WriteHeader(http.StatusOK)

	case ModalIDCalendarEditEvent:
		parts := strings.SplitN(payload.View.PrivateMetadata, "|", 2)
		if len(parts) != 2 {
			log.Printf("slack/interactions: calendar edit submit: bad private metadata %q", payload.View.PrivateMetadata)
			w.WriteHeader(http.StatusOK)
			return
		}
		eventID, calendarID := parts[0], parts[1]
		actor, err := store.GetUserByExternalID(ctx, pool, "slack", payload.User.ID)
		if err != nil || actor == nil {
			w.WriteHeader(http.StatusOK)
			return
		}
		perms, _ := store.GetUserPermissions(ctx, pool, actor.ID)
		if !hasPermission(perms, permissions.CalendarManage) {
			w.WriteHeader(http.StatusOK)
			return
		}
		sv := payload.View.State.Values
		title := sv["ce_title"]["ce_title_input"].Value
		startUnix := sv["ce_start"]["ce_start_picker"].SelectedDateTime
		endUnix := sv["ce_end"]["ce_end_picker"].SelectedDateTime
		allDay := len(sv["ce_all_day"]["ce_all_day_check"].SelectedOptions) > 0

		if title == "" {
			modalError(w, "ce_title", "Title is required")
			return
		}

		event := &store.CalendarEvent{
			ID:         eventID,
			CalendarID: calendarID,
			Title:      title,
			StartTime:  time.Unix(startUnix, 0).UTC(),
			AllDay:     allDay,
		}
		if endUnix != 0 {
			t := time.Unix(endUnix, 0).UTC()
			event.EndTime = &t
		}
		if d := sv["ce_description"]["ce_description_input"].Value; d != "" {
			event.Description = &d
		}
		if l := sv["ce_location"]["ce_location_input"].Value; l != "" {
			event.Location = &l
		}
		if u := sv["ce_url"]["ce_url_input"].Value; u != "" {
			event.URL = &u
		}
		if err := store.UpdateCalendarEvent(ctx, pool, event); err != nil {
			log.Printf("slack/interactions: update calendar event %s: %v", eventID, err)
		}
		w.WriteHeader(http.StatusOK)

	case ModalIDCalendarEventList:
		// List modal has no submit; acknowledge.
		w.WriteHeader(http.StatusOK)

	case ModalIDResourceBrowser, ModalIDQuickLinks, ModalIDContacts, ModalIDPendingRequests, ModalIDLegislation, ModalIDManageMeetings:
		// Read-only modals — nothing to process on submit.
		w.WriteHeader(http.StatusOK)

	default:
		w.WriteHeader(http.StatusOK)
	}
}

func handleApproveRequest(ctx context.Context, pool *pgxpool.Pool, encKey []byte, client *slacklib.Client, payload *slacklib.InteractionCallback, requestID string) {
	req, err := store.GetChannelRequest(ctx, pool, requestID)
	if err != nil || req == nil {
		log.Printf("slack/interactions: approve: request %s not found: %v", requestID, err)
		return
	}

	reviewer, err := store.GetUserByExternalID(ctx, pool, "slack", payload.User.ID)
	if err != nil || reviewer == nil {
		log.Printf("slack/interactions: approve: reviewer %s not found: %v", payload.User.ID, err)
		return
	}

	// Get requester's Slack ID for the channel invite.
	var requesterSlackID string
	if req.RequesterID != nil {
		requesterSlackID, _ = store.GetUserExternalID(ctx, pool, *req.RequesterID, "slack")
	}

	if requesterSlackID != "" {
		if _, err := client.InviteUsersToConversationContext(ctx, req.SlackChannelID, requesterSlackID); err != nil {
			log.Printf("slack/interactions: invite %s to %s: %v", requesterSlackID, req.SlackChannelID, err)
			// Continue even if invite fails — update the record and notify anyway.
		}
	}

	if err := store.UpdateRequestStatus(ctx, pool, requestID, store.ChannelRequestApproved, reviewer.ID); err != nil {
		log.Printf("slack/interactions: update request status: %v", err)
	}

	if err := store.LogAction(ctx, pool, &reviewer.ID, "channel_request.approved", req.SlackChannelName,
		map[string]string{"request_id": requestID, "channel_id": req.SlackChannelID}); err != nil {
		log.Printf("slack/interactions: audit log: %v", err)
	}

	if requesterSlackID != "" {
		msg := "Your request to join *#" + req.SlackChannelName + "* was approved. :white_check_mark:"
		if _, _, err := SendDM(ctx, client, requesterSlackID, msg); err != nil {
			log.Printf("slack/interactions: DM approval to requester: %v", err)
		}
	}

	resolved := ":white_check_mark: *Approved* by " + reviewer.DisplayName
	if payload.View.Type == slacklib.VTModal {
		// Update all approver DM notifications, then refresh the modal.
		UpdateRequestNotificationDMs(ctx, pool, client, req.ID, resolved)
		channelIDs, _ := store.GetUserChannelApprovals(ctx, pool, reviewer.ID)
		remaining, _ := store.ListPendingRequestsForChannels(ctx, pool, channelIDs)
		displays := buildRequestDisplays(ctx, pool, remaining)
		updatedModal := PendingRequestsModal(displays)
		if _, err := client.UpdateViewContext(ctx, updatedModal, "", payload.View.Hash, payload.View.ID); err != nil {
			log.Printf("slack/interactions: update pending requests modal after approve: %v", err)
		}
	} else if payload.Channel.ID != "" && payload.Message.Timestamp != "" {
		// Directly update the clicked message — reliable even for requests that predate notification tracking.
		if _, _, _, err := client.UpdateMessageContext(ctx, payload.Channel.ID, payload.Message.Timestamp,
			slacklib.MsgOptionText(resolved, false),
			slacklib.MsgOptionBlocks(),
		); err != nil {
			log.Printf("slack/interactions: update DM after approve: %v", err)
		}
		// Also update any other approver copies recorded in the DB.
		UpdateRequestNotificationDMs(ctx, pool, client, req.ID, resolved)
	}
	// Refresh approver's App Home so the button count updates.
	go Publish(ctx, pool, encKey, client, payload.User.ID)
}

func handleDeclineRequest(ctx context.Context, pool *pgxpool.Pool, encKey []byte, client *slacklib.Client, payload *slacklib.InteractionCallback, requestID string) {
	req, err := store.GetChannelRequest(ctx, pool, requestID)
	if err != nil || req == nil {
		log.Printf("slack/interactions: decline: request %s not found: %v", requestID, err)
		return
	}

	reviewer, err := store.GetUserByExternalID(ctx, pool, "slack", payload.User.ID)
	if err != nil || reviewer == nil {
		log.Printf("slack/interactions: decline: reviewer %s not found: %v", payload.User.ID, err)
		return
	}

	if err := store.UpdateRequestStatus(ctx, pool, requestID, store.ChannelRequestDeclined, reviewer.ID); err != nil {
		log.Printf("slack/interactions: update request status: %v", err)
	}

	if err := store.LogAction(ctx, pool, &reviewer.ID, "channel_request.declined", req.SlackChannelName,
		map[string]string{"request_id": requestID, "channel_id": req.SlackChannelID}); err != nil {
		log.Printf("slack/interactions: audit log: %v", err)
	}

	var requesterSlackID string
	if req.RequesterID != nil {
		requesterSlackID, _ = store.GetUserExternalID(ctx, pool, *req.RequesterID, "slack")
	}

	if requesterSlackID != "" {
		msg := "Your request to join *#" + req.SlackChannelName + "* was not approved."
		if _, _, err := SendDM(ctx, client, requesterSlackID, msg); err != nil {
			log.Printf("slack/interactions: DM decline to requester: %v", err)
		}
	}

	resolved := ":no_entry_sign: *Declined* by " + reviewer.DisplayName
	if payload.View.Type == slacklib.VTModal {
		// Update all approver DM notifications, then refresh the modal.
		UpdateRequestNotificationDMs(ctx, pool, client, req.ID, resolved)
		channelIDs, _ := store.GetUserChannelApprovals(ctx, pool, reviewer.ID)
		remaining, _ := store.ListPendingRequestsForChannels(ctx, pool, channelIDs)
		displays := buildRequestDisplays(ctx, pool, remaining)
		updatedModal := PendingRequestsModal(displays)
		if _, err := client.UpdateViewContext(ctx, updatedModal, "", payload.View.Hash, payload.View.ID); err != nil {
			log.Printf("slack/interactions: update pending requests modal after decline: %v", err)
		}
	} else if payload.Channel.ID != "" && payload.Message.Timestamp != "" {
		// Directly update the clicked message — reliable even for requests that predate notification tracking.
		if _, _, _, err := client.UpdateMessageContext(ctx, payload.Channel.ID, payload.Message.Timestamp,
			slacklib.MsgOptionText(resolved, false),
			slacklib.MsgOptionBlocks(),
		); err != nil {
			log.Printf("slack/interactions: update DM after decline: %v", err)
		}
		// Also update any other approver copies recorded in the DB.
		UpdateRequestNotificationDMs(ctx, pool, client, req.ID, resolved)
	}
	go Publish(ctx, pool, encKey, client, payload.User.ID)
}

// modalError writes a Slack modal validation error response.
func modalError(w http.ResponseWriter, blockID, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]any{
		"response_action": "errors",
		"errors":          map[string]string{blockID: message},
	})
}

// proposedWindow is a start time + duration pair used for overlap pre-checks.
type proposedWindow struct {
	start    time.Time
	duration int // minutes
}

// checkOverlapConflict returns a pointer to the start time of the first proposed
// window that violates the overlap policy against a set of existing occurrences.
// Returns nil if no conflict is found.
func checkOverlapConflict(proposed []proposedWindow, existing []store.UpcomingOccurrence, allowOverlap bool, maxOverlap int) *time.Time {
	for _, p := range proposed {
		propEnd := p.start.Add(time.Duration(p.duration) * time.Minute)
		concurrent := 0
		for _, ex := range existing {
			exEnd := ex.StartTime.Add(time.Duration(ex.DurationMinutes) * time.Minute)
			if p.start.Before(exEnd) && ex.StartTime.Before(propEnd) {
				concurrent++
			}
		}
		var reject bool
		if !allowOverlap {
			reject = concurrent > 0
		} else {
			reject = concurrent > maxOverlap
		}
		if reject {
			t := p.start
			return &t
		}
	}
	return nil
}

func handleScheduleMeetingSubmission(ctx context.Context, w http.ResponseWriter, pool *pgxpool.Pool, encKey []byte, sched platform.MeetingScheduler, client *slacklib.Client, payload *slacklib.InteractionCallback) {
	sv := payload.View.State.Values

	// 1. Parse form fields.
	topic := sv["topic_block"]["topic_input"].Value
	agenda := sv["agenda_block"]["agenda_input"].Value
	startUnix := sv["start_time_block"]["start_time_picker"].SelectedDateTime
	durationStr := sv["duration_block"]["duration_input"].Value
	timezone := strings.TrimSpace(sv["timezone_block"]["timezone_input"].SelectedOption.Value)
	password := sv["password_block"]["password_input"].Value
	recType := sv["recurrence_type_block"]["recurrence_type"].SelectedOption.Value
	intervalStr := sv["recurrence_interval_block"]["recurrence_interval"].Value
	endTimesStr := sv["end_times_block"]["end_times"].Value
	monthlyDayStr := sv["monthly_day_block"]["monthly_day"].Value
	monthlyWeekStr := sv["monthly_week_block"]["monthly_week"].SelectedOption.Value
	monthlyWeekdayStr := sv["monthly_weekday_block"]["monthly_weekday"].SelectedOption.Value
	weeklyDayOptions := sv["weekly_days_block"]["weekly_days"].SelectedOptions

	// 2. Validate start time.
	minAdvStr, _ := store.GetServiceConfig(ctx, pool, "meetings", "min_advance_minutes", encKey)
	minAdv := 15
	if n, err := strconv.Atoi(minAdvStr); err == nil && n > 0 {
		minAdv = n
	}
	startTime := time.Unix(startUnix, 0)
	if startTime.Before(time.Now().Add(time.Duration(minAdv) * time.Minute)) {
		modalError(w, "start_time_block", fmt.Sprintf("Start time must be at least %d minutes from now", minAdv))
		return
	}

	// 3. Validate duration.
	duration, _ := strconv.Atoi(durationStr)
	if duration < 1 {
		modalError(w, "duration_block", "Duration must be at least 1 minute")
		return
	}
	maxDurStr, _ := store.GetServiceConfig(ctx, pool, "meetings", "max_duration_minutes", encKey)
	maxDur := 1440
	if n, err := strconv.Atoi(maxDurStr); err == nil && n > 0 {
		maxDur = n
	}
	if duration > maxDur {
		modalError(w, "duration_block", fmt.Sprintf("Duration exceeds the maximum of %d minutes", maxDur))
		return
	}

	// 4. Build recurrence pattern.
	var recurrence *platform.RecurrencePattern
	if recType != "none" && recType != "" {
		endTimes, _ := strconv.Atoi(endTimesStr)
		if endTimes < 1 || endTimes > 60 {
			modalError(w, "end_times_block", "Repeat count must be between 1 and 60")
			return
		}
		interval, _ := strconv.Atoi(intervalStr)
		if interval < 1 {
			interval = 1
		}
		rec := &platform.RecurrencePattern{RepeatInterval: interval, EndTimes: endTimes}
		switch recType {
		case "weekly":
			if len(weeklyDayOptions) == 0 {
				modalError(w, "weekly_days_block", "Select at least one day for weekly recurrence")
				return
			}
			rec.Type = 2
			days := make([]string, 0, len(weeklyDayOptions))
			for _, opt := range weeklyDayOptions {
				days = append(days, opt.Value)
			}
			rec.WeeklyDays = strings.Join(days, ",")
		case "monthly_date":
			day, _ := strconv.Atoi(monthlyDayStr)
			if day < 1 || day > 28 {
				modalError(w, "monthly_day_block", "Day of month must be between 1 and 28")
				return
			}
			rec.Type = 3
			rec.MonthlyDay = day
		case "monthly_weekday":
			if monthlyWeekStr == "" {
				modalError(w, "monthly_week_block", "Select a week of the month")
				return
			}
			if monthlyWeekdayStr == "" {
				modalError(w, "monthly_weekday_block", "Select a day of the week")
				return
			}
			week, _ := strconv.Atoi(monthlyWeekStr)
			weekday, _ := strconv.Atoi(monthlyWeekdayStr)
			rec.Type = 3
			rec.MonthlyWeek = week
			rec.MonthlyWeekDay = weekday
		}
		recurrence = rec
	}

	// 5. Resolve Zoom integration.
	zoomInteg, err := store.GetIntegrationByType(ctx, pool, "zoom")
	if err != nil || zoomInteg == nil {
		modalError(w, "topic_block", "Zoom integration is not configured")
		return
	}

	// 6. Threshold check (before calling Zoom).
	maxSchedStr, _ := store.GetServiceConfig(ctx, pool, "meetings", "max_scheduled", encKey)
	if maxSchedStr != "" {
		limit, _ := strconv.Atoi(maxSchedStr)
		existing, _ := store.CountUpcomingOccurrences(ctx, pool, zoomInteg.ID)
		newCount := 1
		if recurrence != nil {
			newCount = recurrence.EndTimes
		}
		if existing+newCount > limit {
			modalError(w, "end_times_block", fmt.Sprintf("Scheduling %d more would exceed the limit of %d", newCount, limit))
			return
		}
	}

	// 7. Resolve created_by UUID.
	user, err := store.GetOrCreateUserByIdentity(ctx, pool, "slack", payload.User.ID, "", "")
	if err != nil {
		log.Printf("slack/interactions: schedule meeting: get user: %v", err)
		modalError(w, "topic_block", "Internal error — please try again")
		return
	}

	// 7.5. Overlap pre-check against local DB before calling Zoom.
	allowOverlap, _ := store.GetServiceConfig(ctx, pool, "meetings", "allow_overlap", encKey)
	maxOverlap := 1
	if maxOverlapStr, _ := store.GetServiceConfig(ctx, pool, "meetings", "max_overlap", encKey); maxOverlapStr != "" {
		if n, err := strconv.Atoi(maxOverlapStr); err == nil {
			maxOverlap = n
		}
	}
	existingOccs, preCheckErr := store.ListUpcomingOccurrences(ctx, pool, zoomInteg.ID, "", "")
	if preCheckErr != nil {
		log.Printf("slack/interactions: schedule meeting: pre-check list failed: %v", preCheckErr)
		// fall through — post-creation check will catch conflicts
	} else if t := checkOverlapConflict(
		[]proposedWindow{{start: startTime, duration: duration}},
		existingOccs,
		allowOverlap == "true",
		maxOverlap,
	); t != nil {
		modalError(w, "start_time_block", fmt.Sprintf(
			"Conflicts with an existing meeting at %s. Max additional concurrent: %d",
			t.In(time.UTC).Format("Jan 2, 2006 3:04 PM MST"), maxOverlap,
		))
		return
	}

	// 8. Create meeting in Zoom.
	if sched == nil {
		modalError(w, "topic_block", "Meeting scheduling is not configured.")
		return
	}
	meeting, err := sched.ScheduleMeeting(ctx, platform.MeetingParams{
		Topic:      topic,
		Agenda:     agenda,
		StartTime:  startTime,
		Duration:   duration,
		Timezone:   timezone,
		Password:   password,
		Recurrence: recurrence,
	})
	if err != nil {
		log.Printf("slack/interactions: schedule meeting: zoom API error: %v", err)
		modalError(w, "topic_block", "Failed to create meeting in Zoom. Check credentials and try again.")
		return
	}

	// 9. Post-creation overlap check — only needed for recurring meetings since Zoom
	// computes the full set of occurrence times. One-off meetings were fully pre-checked above.
	if recurrence != nil {
		for _, proposed := range meeting.Occurrences {
			wins := []proposedWindow{{start: proposed.StartTime, duration: proposed.Duration}}
			if t := checkOverlapConflict(wins, existingOccs, allowOverlap == "true", maxOverlap); t != nil {
				if rollbackErr := sched.DeleteMeeting(ctx, meeting.ID); rollbackErr != nil {
					log.Printf("slack/interactions: schedule meeting: rollback delete failed: %v", rollbackErr)
				}
				modalError(w, "start_time_block", fmt.Sprintf(
					"Conflicts with an existing meeting at %s. Max additional concurrent: %d",
					proposed.StartTime.In(time.UTC).Format("Jan 2, 2006 3:04 PM MST"), maxOverlap,
				))
				return
			}
		}
	}

	// 10. Persist to DB.
	sendReminder := len(sv["reminder_block"]["send_reminder"].SelectedOptions) > 0
	skipUpload := len(sv["skip_upload_block"]["skip_upload"].SelectedOptions) > 0

	occurrences := make([]store.MeetingOccurrence, len(meeting.Occurrences))
	for i, o := range meeting.Occurrences {
		occurrences[i] = store.MeetingOccurrence{
			ZoomOccurrenceID: o.OccurrenceID,
			StartTime:        o.StartTime,
			DurationMinutes:  o.Duration,
			Status:           o.Status,
		}
	}
	if err := store.CreateScheduledMeeting(ctx, pool, encKey, &store.ScheduledMeeting{
		IntegrationID:   zoomInteg.ID,
		ZoomMeetingID:   meeting.ID,
		HostEmail:       meeting.HostEmail,
		Topic:           topic,
		Agenda:          agenda,
		DurationMinutes: duration,
		Timezone:        timezone,
		Password:        password,
		JoinURL:         meeting.JoinURL,
		StartURL:        meeting.StartURL,
		IsRecurring:     recurrence != nil,
		CreatedBy:       user.ID,
		SendReminder:    sendReminder,
		SkipUpload:      skipUpload,
	}, occurrences); err != nil {
		log.Printf("slack/interactions: schedule meeting: failed to persist (meeting %d exists in Zoom): %v", meeting.ID, err)
		// Non-fatal — meeting is live in Zoom; log for ops visibility.
	}

	// 11. DM the host.
	firstOcc := meeting.Occurrences[0]
	loc, err := time.LoadLocation(timezone)
	if err != nil {
		loc = time.UTC
	}
	startLocal := firstOcc.StartTime.In(loc)

	var lines []string
	lines = append(lines, fmt.Sprintf("*%s* scheduled for *%s*", topic, startLocal.Format("Mon Jan 2, 2006 at 3:04 PM MST")))
	if recurrence != nil {
		lines = append(lines, fmt.Sprintf("Recurrence: %s, %d times", recurrenceSummary(recurrence), recurrence.EndTimes))
	}
	lines = append(lines, fmt.Sprintf("Join URL: %s", meeting.JoinURL))
	lines = append(lines, fmt.Sprintf("*Host start link — do not share:* %s", meeting.StartURL))
	if password != "" {
		lines = append(lines, fmt.Sprintf("Password: %s", password))
	}

	if _, _, err := SendDM(ctx, client, payload.User.ID, strings.Join(lines, "\n")); err != nil {
		log.Printf("slack/interactions: schedule meeting: DM to host failed: %v", err)
	}

	w.WriteHeader(http.StatusOK)
}

func handleEditMeetingSubmission(ctx context.Context, w http.ResponseWriter, pool *pgxpool.Pool, encKey []byte, sched platform.MeetingScheduler, client *slacklib.Client, payload *slacklib.InteractionCallback) {
	meetingID := payload.View.PrivateMetadata
	sv := payload.View.State.Values

	topic := sv["topic_block"]["topic_input"].Value
	agenda := sv["agenda_block"]["agenda_input"].Value
	startUnix := sv["start_time_block"]["start_time_picker"].SelectedDateTime
	durationStr := sv["duration_block"]["duration_input"].Value
	timezone := strings.TrimSpace(sv["timezone_block"]["timezone_input"].SelectedOption.Value)
	password := sv["password_block"]["password_input"].Value
	recType := sv["recurrence_type_block"]["recurrence_type"].SelectedOption.Value
	intervalStr := sv["recurrence_interval_block"]["recurrence_interval"].Value
	endTimesStr := sv["end_times_block"]["end_times"].Value
	monthlyDayStr := sv["monthly_day_block"]["monthly_day"].Value
	monthlyWeekStr := sv["monthly_week_block"]["monthly_week"].SelectedOption.Value
	monthlyWeekdayStr := sv["monthly_weekday_block"]["monthly_weekday"].SelectedOption.Value
	weeklyDayOptions := sv["weekly_days_block"]["weekly_days"].SelectedOptions

	// Validate start time.
	minAdvStr, _ := store.GetServiceConfig(ctx, pool, "meetings", "min_advance_minutes", encKey)
	minAdv := 15
	if n, err := strconv.Atoi(minAdvStr); err == nil && n > 0 {
		minAdv = n
	}
	startTime := time.Unix(startUnix, 0)
	if startTime.Before(time.Now().Add(time.Duration(minAdv) * time.Minute)) {
		modalError(w, "start_time_block", fmt.Sprintf("Start time must be at least %d minutes from now", minAdv))
		return
	}

	duration, _ := strconv.Atoi(durationStr)
	if duration < 1 {
		modalError(w, "duration_block", "Duration must be at least 1 minute")
		return
	}
	maxDurStr, _ := store.GetServiceConfig(ctx, pool, "meetings", "max_duration_minutes", encKey)
	maxDur := 1440
	if n, err := strconv.Atoi(maxDurStr); err == nil && n > 0 {
		maxDur = n
	}
	if duration > maxDur {
		modalError(w, "duration_block", fmt.Sprintf("Duration exceeds the maximum of %d minutes", maxDur))
		return
	}

	var recurrence *platform.RecurrencePattern
	if recType != "none" && recType != "" {
		endTimes, _ := strconv.Atoi(endTimesStr)
		if endTimes < 1 || endTimes > 60 {
			modalError(w, "end_times_block", "Repeat count must be between 1 and 60")
			return
		}
		interval, _ := strconv.Atoi(intervalStr)
		if interval < 1 {
			interval = 1
		}
		rec := &platform.RecurrencePattern{RepeatInterval: interval, EndTimes: endTimes}
		switch recType {
		case "weekly":
			if len(weeklyDayOptions) == 0 {
				modalError(w, "weekly_days_block", "Select at least one day for weekly recurrence")
				return
			}
			rec.Type = 2
			days := make([]string, 0, len(weeklyDayOptions))
			for _, opt := range weeklyDayOptions {
				days = append(days, opt.Value)
			}
			rec.WeeklyDays = strings.Join(days, ",")
		case "monthly_date":
			day, _ := strconv.Atoi(monthlyDayStr)
			if day < 1 || day > 28 {
				modalError(w, "monthly_day_block", "Day of month must be between 1 and 28")
				return
			}
			rec.Type = 3
			rec.MonthlyDay = day
		case "monthly_weekday":
			if monthlyWeekStr == "" {
				modalError(w, "monthly_week_block", "Select a week of the month")
				return
			}
			if monthlyWeekdayStr == "" {
				modalError(w, "monthly_weekday_block", "Select a day of the week")
				return
			}
			week, _ := strconv.Atoi(monthlyWeekStr)
			weekday, _ := strconv.Atoi(monthlyWeekdayStr)
			rec.Type = 3
			rec.MonthlyWeek = week
			rec.MonthlyWeekDay = weekday
		}
		recurrence = rec
	}

	zoomInteg, err := store.GetIntegrationByType(ctx, pool, "zoom")
	if err != nil || zoomInteg == nil {
		modalError(w, "topic_block", "Zoom integration is not configured")
		return
	}

	existing, err := store.GetScheduledMeetingByID(ctx, pool, encKey, meetingID)
	if err != nil || existing == nil {
		modalError(w, "topic_block", "Meeting not found — it may have been cancelled")
		return
	}

	// Overlap pre-check — exclude this series from the comparison.
	allowOverlap, _ := store.GetServiceConfig(ctx, pool, "meetings", "allow_overlap", encKey)
	otherOccs, preCheckErr := store.ListUpcomingOccurrences(ctx, pool, zoomInteg.ID, meetingID, "")
	maxOverlap := 1
	if maxOverlapStr, _ := store.GetServiceConfig(ctx, pool, "meetings", "max_overlap", encKey); maxOverlapStr != "" {
		if n, err := strconv.Atoi(maxOverlapStr); err == nil {
			maxOverlap = n
		}
	}
	if preCheckErr != nil {
		log.Printf("slack/interactions: edit meeting: pre-check list failed: %v", preCheckErr)
		// fall through — post-update check will catch conflicts
	} else if t := checkOverlapConflict(
		[]proposedWindow{{start: startTime, duration: duration}},
		otherOccs,
		allowOverlap == "true",
		maxOverlap,
	); t != nil {
		modalError(w, "start_time_block", fmt.Sprintf(
			"Conflicts with an existing meeting at %s. Max additional concurrent: %d",
			t.In(time.UTC).Format("Jan 2, 2006 3:04 PM MST"), maxOverlap,
		))
		return
	}

	if sched == nil {
		modalError(w, "topic_block", "Meeting scheduling is not configured.")
		return
	}
	updatedMeeting, err := sched.UpdateMeeting(ctx, existing.ZoomMeetingID, platform.MeetingParams{
		Topic:      topic,
		Agenda:     agenda,
		StartTime:  startTime,
		Duration:   duration,
		Timezone:   timezone,
		Password:   password,
		Recurrence: recurrence,
	})
	if err != nil {
		log.Printf("slack/interactions: edit meeting: zoom update %d: %v", existing.ZoomMeetingID, err)
		modalError(w, "topic_block", "Failed to update meeting in Zoom.")
		return
	}

	// Post-update check for recurring series — Zoom returns the full occurrence list so
	// we can catch conflicts on later occurrences that weren't visible from start_time alone.
	// No rollback possible; log desync and surface error if a conflict is found.
	for _, proposed := range updatedMeeting.Occurrences {
		wins := []proposedWindow{{start: proposed.StartTime, duration: proposed.Duration}}
		if t := checkOverlapConflict(wins, otherOccs, allowOverlap == "true", maxOverlap); t != nil {
			log.Printf("slack/interactions: edit meeting: overlap detected after zoom update for meeting %s — user must re-edit to resolve", meetingID)
			modalError(w, "start_time_block", "This time conflicts with another scheduled meeting. The Zoom meeting has been updated — please edit again to choose a different time.")
			return
		}
	}

	// Build recurrence JSONB bytes for storage.
	var recBytes []byte
	if recurrence != nil {
		recBytes, _ = json.Marshal(recurrence)
	}

	sendReminder := len(sv["reminder_block"]["send_reminder"].SelectedOptions) > 0
	skipUpload := len(sv["skip_upload_block"]["skip_upload"].SelectedOptions) > 0

	occurrences := make([]store.MeetingOccurrence, len(updatedMeeting.Occurrences))
	for i, o := range updatedMeeting.Occurrences {
		occurrences[i] = store.MeetingOccurrence{
			ZoomOccurrenceID: o.OccurrenceID,
			StartTime:        o.StartTime,
			DurationMinutes:  o.Duration,
			Status:           o.Status,
		}
	}
	if err := store.UpdateScheduledMeeting(ctx, pool, encKey, &store.ScheduledMeeting{
		ID:                meetingID,
		Topic:             topic,
		Agenda:            agenda,
		DurationMinutes:   duration,
		Timezone:          timezone,
		Password:          password,
		JoinURL:           updatedMeeting.JoinURL,
		StartURL:          updatedMeeting.StartURL,
		IsRecurring:       recurrence != nil,
		RecurrencePattern: recBytes,
		SendReminder:      sendReminder,
		SkipUpload:        skipUpload,
	}, occurrences); err != nil {
		log.Printf("slack/interactions: edit meeting: store update %s: %v", meetingID, err)
	}

	w.WriteHeader(http.StatusOK)
}

func handleEditOccurrenceSubmission(ctx context.Context, w http.ResponseWriter, pool *pgxpool.Pool, encKey []byte, sched platform.MeetingScheduler, payload *slacklib.InteractionCallback) {
	occurrenceID := payload.View.PrivateMetadata
	sv := payload.View.State.Values

	dateStr := sv["date_block"]["date_picker"].SelectedDate
	timeStr := sv["time_block"]["time_picker"].SelectedTime
	durationStr := sv["duration_block"]["duration_input"].Value

	occ, err := store.GetMeetingOccurrenceByID(ctx, pool, occurrenceID)
	if err != nil || occ == nil {
		modalError(w, "date_block", "Occurrence not found — it may have been cancelled")
		return
	}

	loc, err := time.LoadLocation(occ.Timezone)
	if err != nil {
		loc = time.UTC
	}

	// Combine date + time in the occurrence's timezone.
	combined, err := time.ParseInLocation("2006-01-02 15:04", dateStr+" "+timeStr, loc)
	if err != nil {
		modalError(w, "date_block", "Invalid date or time format")
		return
	}

	if !combined.After(time.Now()) {
		modalError(w, "date_block", "Start time must be in the future")
		return
	}

	duration, _ := strconv.Atoi(durationStr)
	if duration < 1 {
		modalError(w, "duration_block", "Duration must be at least 1 minute")
		return
	}

	// Overlap pre-check — exclude this occurrence from the comparison.
	zoomInteg, _ := store.GetIntegrationByType(ctx, pool, "zoom")
	if zoomInteg != nil {
		allowOverlap, _ := store.GetServiceConfig(ctx, pool, "meetings", "allow_overlap", encKey)
		otherOccs, preCheckErr := store.ListUpcomingOccurrences(ctx, pool, zoomInteg.ID, "", occurrenceID)
		maxOverlap := 1
		if maxOverlapStr, _ := store.GetServiceConfig(ctx, pool, "meetings", "max_overlap", encKey); maxOverlapStr != "" {
			if n, err := strconv.Atoi(maxOverlapStr); err == nil {
				maxOverlap = n
			}
		}
		if preCheckErr != nil {
			log.Printf("slack/interactions: edit occurrence: pre-check list failed: %v", preCheckErr)
			// fall through — Zoom update proceeds; no post-check possible without integration
		} else if t := checkOverlapConflict(
			[]proposedWindow{{start: combined, duration: duration}},
			otherOccs,
			allowOverlap == "true",
			maxOverlap,
		); t != nil {
			modalError(w, "date_block", fmt.Sprintf(
				"Conflicts with an existing meeting at %s. Max additional concurrent: %d",
				t.In(time.UTC).Format("Jan 2, 2006 3:04 PM MST"), maxOverlap,
			))
			return
		}
	}

	if sched == nil {
		modalError(w, "date_block", "Meeting scheduling is not configured.")
		return
	}
	if err := sched.UpdateOccurrence(ctx, occ.ZoomMeetingID, occ.ZoomOccurrenceID, platform.OccurrenceParams{
		StartTime:       combined,
		DurationMinutes: duration,
	}); err != nil {
		log.Printf("slack/interactions: edit occurrence: zoom update %s: %v", occ.ZoomOccurrenceID, err)
		modalError(w, "date_block", "Failed to update occurrence in Zoom.")
		return
	}

	if err := store.UpdateMeetingOccurrence(ctx, pool, occurrenceID, combined, duration); err != nil {
		log.Printf("slack/interactions: edit occurrence: store update %s: %v", occurrenceID, err)
	}

	w.WriteHeader(http.StatusOK)
}

// recurrenceSummary returns a human-readable one-line summary of a recurrence pattern.
func recurrenceSummary(r *platform.RecurrencePattern) string {
	dayNames := map[string]string{
		"1": "Sunday", "2": "Monday", "3": "Tuesday", "4": "Wednesday",
		"5": "Thursday", "6": "Friday", "7": "Saturday",
	}
	weekNames := map[int]string{1: "first", 2: "second", 3: "third", 4: "fourth", -1: "last"}
	switch r.Type {
	case 2: // Weekly
		var days []string
		for _, d := range strings.Split(r.WeeklyDays, ",") {
			if name, ok := dayNames[strings.TrimSpace(d)]; ok {
				days = append(days, name)
			}
		}
		if r.RepeatInterval > 1 {
			return fmt.Sprintf("every %d weeks on %s", r.RepeatInterval, strings.Join(days, ", "))
		}
		return "weekly on " + strings.Join(days, ", ")
	case 3: // Monthly
		if r.MonthlyDay > 0 {
			if r.RepeatInterval > 1 {
				return fmt.Sprintf("every %d months on day %d", r.RepeatInterval, r.MonthlyDay)
			}
			return fmt.Sprintf("monthly on day %d", r.MonthlyDay)
		}
		week := weekNames[r.MonthlyWeek]
		day := dayNames[strconv.Itoa(r.MonthlyWeekDay)]
		if r.RepeatInterval > 1 {
			return fmt.Sprintf("every %d months on the %s %s", r.RepeatInterval, week, day)
		}
		return fmt.Sprintf("monthly on the %s %s", week, day)
	}
	return "recurring"
}

const libraryPerPage = 10
const libraryAdminPerPage = 15

// buildLibraryAdminData loads the data needed for the given admin modal tab.
func buildLibraryAdminData(ctx context.Context, pool *pgxpool.Pool, tab, search string, page int) (LibraryAdminData, error) {
	if page < 1 {
		page = 1
	}
	data := LibraryAdminData{Tab: tab, CatalogSearch: search, CatalogPage: page}
	switch tab {
	case "requests":
		checkouts, _, err := store.ListCheckouts(ctx, pool, "pending", 1, libraryAdminPerPage)
		if err != nil {
			return data, err
		}
		data.Requests = checkouts
	case "active":
		checkouts, _, err := store.ListCheckouts(ctx, pool, "active", 1, libraryAdminPerPage)
		if err != nil {
			return data, err
		}
		data.Active = checkouts
	case "holds":
		holds, err := store.ListAllActiveHolds(ctx, pool)
		if err != nil {
			return data, err
		}
		if len(holds) > libraryAdminPerPage {
			holds = holds[:libraryAdminPerPage]
		}
		data.Holds = holds
	case "overdue":
		checkouts, err := store.ListOverdueCheckouts(ctx, pool)
		if err != nil {
			return data, err
		}
		if len(checkouts) > libraryAdminPerPage {
			checkouts = checkouts[:libraryAdminPerPage]
		}
		data.Overdue = checkouts
	case "catalog":
		const catalogPerPage = 10
		books, total, err := store.ListBooksWithAvailabilityForUser(ctx, pool, "", search, page, catalogPerPage)
		if err != nil {
			return data, err
		}
		data.Catalog = books
		totalPages := (total + catalogPerPage - 1) / catalogPerPage
		if totalPages == 0 {
			totalPages = 1
		}
		data.CatalogPages = totalPages
	}
	return data, nil
}

// handleManageLibrary opens the admin library modal.
func handleManageLibrary(ctx context.Context, w http.ResponseWriter, pool *pgxpool.Pool, encKey []byte, client *slacklib.Client, payload *slacklib.InteractionCallback) {
	user, err := store.GetOrCreateUserByIdentity(ctx, pool, "slack", payload.User.ID, "", "")
	if err != nil {
		log.Printf("slack/interactions: manage library: get user: %v", err)
		w.WriteHeader(http.StatusOK)
		return
	}
	perms, err := store.GetUserPermissions(ctx, pool, user.ID)
	if err != nil || !hasPermission(perms, permissions.LibraryManage) {
		w.WriteHeader(http.StatusOK)
		return
	}
	data, err := buildLibraryAdminData(ctx, pool, "requests", "", 1)
	if err != nil {
		log.Printf("slack/interactions: manage library: load data: %v", err)
		w.WriteHeader(http.StatusOK)
		return
	}
	if _, err := client.OpenViewContext(ctx, payload.TriggerID, LibraryAdminModal(data)); err != nil {
		log.Printf("slack/interactions: open library admin modal: %v", err)
	}
	w.WriteHeader(http.StatusOK)
}

// handleLibraryAdminTab switches to a different tab in the admin modal.
func handleLibraryAdminTab(ctx context.Context, w http.ResponseWriter, pool *pgxpool.Pool, encKey []byte, client *slacklib.Client, payload *slacklib.InteractionCallback, tab string) {
	var meta libraryAdminMeta
	if err := json.Unmarshal([]byte(payload.View.PrivateMetadata), &meta); err != nil {
		meta = libraryAdminMeta{Tab: tab, Page: 1}
	}
	meta.Tab = tab
	if tab != "catalog" {
		meta.Search = ""
		meta.Page = 1
	}
	data, err := buildLibraryAdminData(ctx, pool, meta.Tab, meta.Search, meta.Page)
	if err != nil {
		log.Printf("slack/interactions: library admin tab %s: load data: %v", tab, err)
		w.WriteHeader(http.StatusOK)
		return
	}
	if _, err := client.UpdateViewContext(ctx, LibraryAdminModal(data), "", payload.View.Hash, payload.View.ID); err != nil {
		log.Printf("slack/interactions: library admin tab: update modal: %v", err)
	}
	w.WriteHeader(http.StatusOK)
}

// handleLibraryApprove opens the approve-checkout modal.
func handleLibraryApprove(ctx context.Context, w http.ResponseWriter, pool *pgxpool.Pool, encKey []byte, client *slacklib.Client, payload *slacklib.InteractionCallback, checkoutID string) {
	co, err := store.GetCheckoutByID(ctx, pool, checkoutID)
	if err != nil || co == nil {
		log.Printf("slack/interactions: library approve: get checkout %s: %v", checkoutID, err)
		w.WriteHeader(http.StatusOK)
		return
	}
	loanDays := 14
	if s, err := store.GetServiceConfig(ctx, pool, "library", "loan_period_days", encKey); err == nil {
		if n, _ := strconv.Atoi(s); n > 0 {
			loanDays = n
		}
	}
	modal := LibraryApproveModal(checkoutID, co.UserName, co.BookTitle, payload.View.ID, time.Now().AddDate(0, 0, loanDays))
	if _, err := client.PushViewContext(ctx, payload.TriggerID, modal); err != nil {
		log.Printf("slack/interactions: push approve modal: %v", err)
	}
	w.WriteHeader(http.StatusOK)
}

// handleLibraryApproveSubmit processes the approve-checkout modal submission.
func handleLibraryApproveSubmit(ctx context.Context, w http.ResponseWriter, pool *pgxpool.Pool, encKey []byte, client *slacklib.Client, payload *slacklib.InteractionCallback) {
	parts := strings.SplitN(payload.View.PrivateMetadata, "|", 2)
	if len(parts) != 2 {
		w.WriteHeader(http.StatusOK)
		return
	}
	checkoutID, adminViewID := parts[0], parts[1]

	dateStr := payload.View.State.Values["due_date_block"]["due_date_picker"].SelectedDate
	dueDate, err := time.Parse("2006-01-02", dateStr)
	if err != nil {
		modalError(w, "due_date_block", "Invalid date")
		return
	}

	co, err := store.GetCheckoutByID(ctx, pool, checkoutID)
	if err != nil || co == nil {
		log.Printf("slack/interactions: library approve submit: get checkout %s: %v", checkoutID, err)
		w.WriteHeader(http.StatusOK)
		return
	}
	if err := store.ApproveCheckout(ctx, pool, checkoutID, "", dueDate); err != nil {
		log.Printf("slack/interactions: library approve submit: %v", err)
		modalError(w, "due_date_block", "Failed to approve checkout")
		return
	}
	go NotifyCheckoutApproved(context.Background(), pool, co.UserID, co.BookTitle, dueDate)
	w.WriteHeader(http.StatusOK)
	go func() {
		data, err := buildLibraryAdminData(context.Background(), pool, "requests", "", 1)
		if err != nil {
			return
		}
		if _, err := client.UpdateViewContext(context.Background(), LibraryAdminModal(data), "", "", adminViewID); err != nil {
			log.Printf("slack/interactions: library approve: refresh admin modal: %v", err)
		}
	}()
}

// handleLibraryDeny opens the deny-checkout modal.
func handleLibraryDeny(ctx context.Context, w http.ResponseWriter, pool *pgxpool.Pool, encKey []byte, client *slacklib.Client, payload *slacklib.InteractionCallback, checkoutID string) {
	co, err := store.GetCheckoutByID(ctx, pool, checkoutID)
	if err != nil || co == nil {
		log.Printf("slack/interactions: library deny: get checkout %s: %v", checkoutID, err)
		w.WriteHeader(http.StatusOK)
		return
	}
	modal := LibraryDenyModal(checkoutID, co.UserName, co.BookTitle, payload.View.ID)
	if _, err := client.PushViewContext(ctx, payload.TriggerID, modal); err != nil {
		log.Printf("slack/interactions: push deny modal: %v", err)
	}
	w.WriteHeader(http.StatusOK)
}

// handleLibraryDenySubmit processes the deny-checkout modal submission.
func handleLibraryDenySubmit(ctx context.Context, w http.ResponseWriter, pool *pgxpool.Pool, encKey []byte, client *slacklib.Client, payload *slacklib.InteractionCallback) {
	parts := strings.SplitN(payload.View.PrivateMetadata, "|", 2)
	if len(parts) != 2 {
		w.WriteHeader(http.StatusOK)
		return
	}
	checkoutID, adminViewID := parts[0], parts[1]
	notes := payload.View.State.Values["reason_block"]["reason_input"].Value

	co, err := store.GetCheckoutByID(ctx, pool, checkoutID)
	if err != nil || co == nil {
		log.Printf("slack/interactions: library deny submit: get checkout %s: %v", checkoutID, err)
		w.WriteHeader(http.StatusOK)
		return
	}
	if err := store.DenyCheckout(ctx, pool, checkoutID, "", notes); err != nil {
		log.Printf("slack/interactions: library deny submit: %v", err)
	}
	go NotifyCheckoutDenied(context.Background(), pool, co.UserID, co.BookTitle, notes)
	w.WriteHeader(http.StatusOK)
	go func() {
		data, err := buildLibraryAdminData(context.Background(), pool, "requests", "", 1)
		if err != nil {
			return
		}
		if _, err := client.UpdateViewContext(context.Background(), LibraryAdminModal(data), "", "", adminViewID); err != nil {
			log.Printf("slack/interactions: library deny: refresh admin modal: %v", err)
		}
	}()
}

// handleLibraryMarkReturned marks a checkout returned and refreshes the admin modal.
func handleLibraryMarkReturned(ctx context.Context, w http.ResponseWriter, pool *pgxpool.Pool, encKey []byte, client *slacklib.Client, payload *slacklib.InteractionCallback, checkoutID string) {
	co, err := store.GetCheckoutByID(ctx, pool, checkoutID)
	if err != nil || co == nil {
		log.Printf("slack/interactions: library mark returned: get checkout %s: %v", checkoutID, err)
		w.WriteHeader(http.StatusOK)
		return
	}
	if err := store.MarkReturned(ctx, pool, checkoutID, ""); err != nil {
		log.Printf("slack/interactions: library mark returned: %v", err)
		w.WriteHeader(http.StatusOK)
		return
	}
	holds, _ := store.ListHoldsForBook(ctx, pool, co.BookID)
	go NotifyBookReturned(context.Background(), pool, co.BookTitle, len(holds))
	var meta libraryAdminMeta
	if err := json.Unmarshal([]byte(payload.View.PrivateMetadata), &meta); err != nil || meta.Tab == "" {
		meta = libraryAdminMeta{Tab: "active", Page: 1}
	}
	data, err := buildLibraryAdminData(ctx, pool, meta.Tab, meta.Search, meta.Page)
	if err != nil {
		log.Printf("slack/interactions: library mark returned: reload data: %v", err)
		w.WriteHeader(http.StatusOK)
		return
	}
	if _, err := client.UpdateViewContext(ctx, LibraryAdminModal(data), "", payload.View.Hash, payload.View.ID); err != nil {
		log.Printf("slack/interactions: library mark returned: update modal: %v", err)
	}
	w.WriteHeader(http.StatusOK)
}

// handleLibraryExtendDue opens the extend-due-date modal.
func handleLibraryExtendDue(ctx context.Context, w http.ResponseWriter, pool *pgxpool.Pool, encKey []byte, client *slacklib.Client, payload *slacklib.InteractionCallback, checkoutID string) {
	co, err := store.GetCheckoutByID(ctx, pool, checkoutID)
	if err != nil || co == nil {
		log.Printf("slack/interactions: library extend due: get checkout %s: %v", checkoutID, err)
		w.WriteHeader(http.StatusOK)
		return
	}
	modal := LibraryExtendDueDateModal(checkoutID, payload.View.ID, co.DueDate)
	if _, err := client.PushViewContext(ctx, payload.TriggerID, modal); err != nil {
		log.Printf("slack/interactions: push extend due modal: %v", err)
	}
	w.WriteHeader(http.StatusOK)
}

// handleLibraryExtendDueSubmit processes the extend-due-date modal submission.
func handleLibraryExtendDueSubmit(ctx context.Context, w http.ResponseWriter, pool *pgxpool.Pool, encKey []byte, client *slacklib.Client, payload *slacklib.InteractionCallback) {
	parts := strings.SplitN(payload.View.PrivateMetadata, "|", 2)
	if len(parts) != 2 {
		w.WriteHeader(http.StatusOK)
		return
	}
	checkoutID, adminViewID := parts[0], parts[1]
	dateStr := payload.View.State.Values["new_due_date_block"]["new_due_date_picker"].SelectedDate
	newDate, err := time.Parse("2006-01-02", dateStr)
	if err != nil {
		modalError(w, "new_due_date_block", "Invalid date")
		return
	}
	if err := store.ExtendDueDate(ctx, pool, checkoutID, newDate); err != nil {
		log.Printf("slack/interactions: library extend due submit: %v", err)
	}
	w.WriteHeader(http.StatusOK)
	go func() {
		data, err := buildLibraryAdminData(context.Background(), pool, "active", "", 1)
		if err != nil {
			return
		}
		if _, err := client.UpdateViewContext(context.Background(), LibraryAdminModal(data), "", "", adminViewID); err != nil {
			log.Printf("slack/interactions: library extend due: refresh admin modal: %v", err)
		}
	}()
}

// handleLibraryNotifyHold marks a hold notified and refreshes the holds tab.
func handleLibraryNotifyHold(ctx context.Context, w http.ResponseWriter, pool *pgxpool.Pool, encKey []byte, client *slacklib.Client, payload *slacklib.InteractionCallback, holdID string) {
	hold, err := store.GetHoldByID(ctx, pool, holdID)
	if err != nil || hold == nil {
		log.Printf("slack/interactions: library notify hold: get hold %s: %v", holdID, err)
		w.WriteHeader(http.StatusOK)
		return
	}
	if err := store.MarkHoldNotified(ctx, pool, holdID); err != nil {
		log.Printf("slack/interactions: library notify hold: mark notified: %v", err)
		w.WriteHeader(http.StatusOK)
		return
	}
	go NotifyHoldAvailable(context.Background(), pool, hold.UserID, hold.BookTitle)
	data, err := buildLibraryAdminData(ctx, pool, "holds", "", 1)
	if err != nil {
		log.Printf("slack/interactions: library notify hold: reload data: %v", err)
		w.WriteHeader(http.StatusOK)
		return
	}
	if _, err := client.UpdateViewContext(ctx, LibraryAdminModal(data), "", payload.View.Hash, payload.View.ID); err != nil {
		log.Printf("slack/interactions: library notify hold: update modal: %v", err)
	}
	w.WriteHeader(http.StatusOK)
}

// handleLibraryCancelHoldAdmin cancels a hold from the admin view and refreshes the holds tab.
func handleLibraryCancelHoldAdmin(ctx context.Context, w http.ResponseWriter, pool *pgxpool.Pool, encKey []byte, client *slacklib.Client, payload *slacklib.InteractionCallback, holdID string) {
	if err := store.CancelHold(ctx, pool, holdID); err != nil {
		log.Printf("slack/interactions: library cancel hold admin: %v", err)
		w.WriteHeader(http.StatusOK)
		return
	}
	data, err := buildLibraryAdminData(ctx, pool, "holds", "", 1)
	if err != nil {
		log.Printf("slack/interactions: library cancel hold admin: reload data: %v", err)
		w.WriteHeader(http.StatusOK)
		return
	}
	if _, err := client.UpdateViewContext(ctx, LibraryAdminModal(data), "", payload.View.Hash, payload.View.ID); err != nil {
		log.Printf("slack/interactions: library cancel hold admin: update modal: %v", err)
	}
	w.WriteHeader(http.StatusOK)
}

// handleLibraryAddBook opens the add-book modal.
func handleLibraryAddBook(ctx context.Context, w http.ResponseWriter, pool *pgxpool.Pool, encKey []byte, client *slacklib.Client, payload *slacklib.InteractionCallback) {
	if _, err := client.PushViewContext(ctx, payload.TriggerID, LibraryAddBookModal(payload.View.ID)); err != nil {
		log.Printf("slack/interactions: push add book modal: %v", err)
	}
	w.WriteHeader(http.StatusOK)
}

// handleLibraryAddBookSubmit processes the add-book modal submission.
func handleLibraryAddBookSubmit(ctx context.Context, w http.ResponseWriter, pool *pgxpool.Pool, encKey []byte, client *slacklib.Client, payload *slacklib.InteractionCallback) {
	adminViewID := payload.View.PrivateMetadata
	sv := payload.View.State.Values

	isbn := strings.TrimSpace(sv["isbn_block"]["isbn_input"].Value)
	title := strings.TrimSpace(sv["title_block"]["title_input"].Value)
	author := strings.TrimSpace(sv["author_block"]["author_input"].Value)
	description := strings.TrimSpace(sv["description_block"]["description_input"].Value)
	copiesStr := sv["copies_block"]["copies_input"].Value

	copies, _ := strconv.Atoi(copiesStr)
	if copies < 1 {
		copies = 1
	}

	// If ISBN provided and title is blank, look up metadata.
	if isbn != "" && title == "" {
		apiKey, _ := store.GetServiceConfig(ctx, pool, "librarything", "api_key", encKey)
		if meta, err := librarything.LookupISBN(ctx, isbn, apiKey); err != nil {
			log.Printf("slack/interactions: library add book: ISBN lookup: %v", err)
		} else if meta != nil {
			if title == "" {
				title = meta.Title
			}
			if author == "" {
				author = meta.Author
			}
			if description == "" {
				description = meta.Description
			}
		}
	}

	if title == "" {
		modalError(w, "title_block", "Title is required — enter a title or provide an ISBN to look up metadata")
		return
	}

	actor, err := store.GetUserByExternalID(ctx, pool, "slack", payload.User.ID)
	if err != nil || actor == nil {
		log.Printf("slack/interactions: library add book: unknown user %s", payload.User.ID)
		w.WriteHeader(http.StatusOK)
		return
	}

	book := store.Book{Title: title, Author: author}
	if isbn != "" {
		book.ISBN = &isbn
	}
	if description != "" {
		book.Description = &description
	}
	created, err := store.CreateBook(ctx, pool, book, actor.ID)
	if err != nil {
		log.Printf("slack/interactions: library add book: create: %v", err)
		modalError(w, "title_block", "Failed to create book")
		return
	}
	for i := 0; i < copies; i++ {
		if _, err := store.CreateCopy(ctx, pool, store.Copy{BookID: created.ID, Condition: "good"}); err != nil {
			log.Printf("slack/interactions: library add book: create copy %d: %v", i+1, err)
		}
	}
	w.WriteHeader(http.StatusOK)
	go func() {
		data, err := buildLibraryAdminData(context.Background(), pool, "catalog", "", 1)
		if err != nil {
			return
		}
		if _, err := client.UpdateViewContext(context.Background(), LibraryAdminModal(data), "", "", adminViewID); err != nil {
			log.Printf("slack/interactions: library add book: refresh admin modal: %v", err)
		}
	}()
}

// handleLibraryEditBook opens the edit-book modal.
func handleLibraryEditBook(ctx context.Context, w http.ResponseWriter, pool *pgxpool.Pool, encKey []byte, client *slacklib.Client, payload *slacklib.InteractionCallback, bookID string) {
	book, err := store.GetBook(ctx, pool, bookID)
	if err != nil || book == nil {
		log.Printf("slack/interactions: library edit book: get book %s: %v", bookID, err)
		w.WriteHeader(http.StatusOK)
		return
	}
	if _, err := client.PushViewContext(ctx, payload.TriggerID, LibraryEditBookModal(book, payload.View.ID)); err != nil {
		log.Printf("slack/interactions: push edit book modal: %v", err)
	}
	w.WriteHeader(http.StatusOK)
}

// handleLibraryEditBookSubmit processes the edit-book modal submission.
func handleLibraryEditBookSubmit(ctx context.Context, w http.ResponseWriter, pool *pgxpool.Pool, encKey []byte, client *slacklib.Client, payload *slacklib.InteractionCallback) {
	parts := strings.SplitN(payload.View.PrivateMetadata, "|", 2)
	if len(parts) != 2 {
		w.WriteHeader(http.StatusOK)
		return
	}
	bookID, adminViewID := parts[0], parts[1]
	sv := payload.View.State.Values
	title := strings.TrimSpace(sv["title_block"]["title_input"].Value)
	author := strings.TrimSpace(sv["author_block"]["author_input"].Value)
	description := strings.TrimSpace(sv["description_block"]["description_input"].Value)

	if title == "" {
		modalError(w, "title_block", "Title is required")
		return
	}
	book := store.Book{Title: title, Author: author}
	if description != "" {
		book.Description = &description
	}
	if err := store.UpdateBook(ctx, pool, bookID, book); err != nil {
		log.Printf("slack/interactions: library edit book: update %s: %v", bookID, err)
		modalError(w, "title_block", "Failed to save changes")
		return
	}
	w.WriteHeader(http.StatusOK)
	go func() {
		data, err := buildLibraryAdminData(context.Background(), pool, "catalog", "", 1)
		if err != nil {
			return
		}
		if _, err := client.UpdateViewContext(context.Background(), LibraryAdminModal(data), "", "", adminViewID); err != nil {
			log.Printf("slack/interactions: library edit book: refresh admin modal: %v", err)
		}
	}()
}

// handleLibraryManageCopies opens the manage-copies modal.
func handleLibraryManageCopies(ctx context.Context, w http.ResponseWriter, pool *pgxpool.Pool, encKey []byte, client *slacklib.Client, payload *slacklib.InteractionCallback, bookID string) {
	book, err := store.GetBook(ctx, pool, bookID)
	if err != nil || book == nil {
		log.Printf("slack/interactions: library manage copies: get book %s: %v", bookID, err)
		w.WriteHeader(http.StatusOK)
		return
	}
	if _, err := client.PushViewContext(ctx, payload.TriggerID, LibraryManageCopiesModal(book)); err != nil {
		log.Printf("slack/interactions: push manage copies modal: %v", err)
	}
	w.WriteHeader(http.StatusOK)
}

// handleLibraryAddCopy creates a new copy with default settings and refreshes the manage-copies modal.
func handleLibraryAddCopy(ctx context.Context, w http.ResponseWriter, pool *pgxpool.Pool, encKey []byte, client *slacklib.Client, payload *slacklib.InteractionCallback, bookID string) {
	if _, err := store.CreateCopy(ctx, pool, store.Copy{BookID: bookID, Condition: "good"}); err != nil {
		log.Printf("slack/interactions: library add copy: %v", err)
		w.WriteHeader(http.StatusOK)
		return
	}
	book, err := store.GetBook(ctx, pool, bookID)
	if err != nil || book == nil {
		log.Printf("slack/interactions: library add copy: reload book %s: %v", bookID, err)
		w.WriteHeader(http.StatusOK)
		return
	}
	if _, err := client.UpdateViewContext(ctx, LibraryManageCopiesModal(book), "", payload.View.Hash, payload.View.ID); err != nil {
		log.Printf("slack/interactions: library add copy: update modal: %v", err)
	}
	w.WriteHeader(http.StatusOK)
}

// handleLibraryDeactivateCopy deactivates a copy and refreshes the manage-copies modal.
// action value: "copyID|bookID"
func handleLibraryDeactivateCopy(ctx context.Context, w http.ResponseWriter, pool *pgxpool.Pool, encKey []byte, client *slacklib.Client, payload *slacklib.InteractionCallback, value string) {
	parts := strings.SplitN(value, "|", 2)
	if len(parts) != 2 {
		w.WriteHeader(http.StatusOK)
		return
	}
	copyID, bookID := parts[0], parts[1]
	if err := store.DeactivateCopy(ctx, pool, copyID); err != nil {
		log.Printf("slack/interactions: library deactivate copy: %v", err)
		w.WriteHeader(http.StatusOK)
		return
	}
	book, err := store.GetBook(ctx, pool, bookID)
	if err != nil || book == nil {
		log.Printf("slack/interactions: library deactivate copy: reload book %s: %v", bookID, err)
		w.WriteHeader(http.StatusOK)
		return
	}
	if _, err := client.UpdateViewContext(ctx, LibraryManageCopiesModal(book), "", payload.View.Hash, payload.View.ID); err != nil {
		log.Printf("slack/interactions: library deactivate copy: update modal: %v", err)
	}
	w.WriteHeader(http.StatusOK)
}

// handleLibraryAdminSearch handles dispatch-action search in the catalog tab.
func handleLibraryAdminSearch(ctx context.Context, w http.ResponseWriter, pool *pgxpool.Pool, encKey []byte, client *slacklib.Client, payload *slacklib.InteractionCallback, search string) {
	data, err := buildLibraryAdminData(ctx, pool, "catalog", search, 1)
	if err != nil {
		log.Printf("slack/interactions: library admin search: %v", err)
		w.WriteHeader(http.StatusOK)
		return
	}
	if _, err := client.UpdateViewContext(ctx, LibraryAdminModal(data), "", payload.View.Hash, payload.View.ID); err != nil {
		log.Printf("slack/interactions: library admin search: update modal: %v", err)
	}
	w.WriteHeader(http.StatusOK)
}

// handleLibraryAdminPaginate handles catalog pagination in the admin modal.
func handleLibraryAdminPaginate(ctx context.Context, w http.ResponseWriter, pool *pgxpool.Pool, encKey []byte, client *slacklib.Client, payload *slacklib.InteractionCallback, value string) {
	var meta libraryAdminMeta
	if err := json.Unmarshal([]byte(payload.View.PrivateMetadata), &meta); err != nil {
		meta = libraryAdminMeta{Tab: "catalog"}
	}
	if strings.HasPrefix(value, "page:") {
		if n, err := strconv.Atoi(strings.TrimPrefix(value, "page:")); err == nil && n > 0 {
			meta.Page = n
		}
	}
	data, err := buildLibraryAdminData(ctx, pool, "catalog", meta.Search, meta.Page)
	if err != nil {
		log.Printf("slack/interactions: library admin paginate: %v", err)
		w.WriteHeader(http.StatusOK)
		return
	}
	if _, err := client.UpdateViewContext(ctx, LibraryAdminModal(data), "", payload.View.Hash, payload.View.ID); err != nil {
		log.Printf("slack/interactions: library admin paginate: update modal: %v", err)
	}
	w.WriteHeader(http.StatusOK)
}

// buildLibraryBrowseModal loads books with per-user availability and builds the browse modal.
func buildLibraryBrowseModal(ctx context.Context, pool *pgxpool.Pool, userID, search string, page int) (slacklib.ModalViewRequest, error) {
	books, total, err := store.ListBooksWithAvailabilityForUser(ctx, pool, userID, search, page, libraryPerPage)
	if err != nil {
		return slacklib.ModalViewRequest{}, err
	}
	totalPages := (total + libraryPerPage - 1) / libraryPerPage
	if totalPages == 0 {
		totalPages = 1
	}

	// Determine whether the user is at their checkout limit.
	userAtMax := false
	maxStr, _ := store.GetServiceConfig(ctx, pool, "library", "max_checkouts", nil)
	if maxCheckouts, err := strconv.Atoi(maxStr); err == nil && maxCheckouts > 0 {
		if count, err := store.UserActiveCheckoutCount(ctx, pool, userID); err == nil && count >= maxCheckouts {
			userAtMax = true
		}
	}

	return LibraryBrowseModal(books, page, totalPages, search, userAtMax), nil
}
