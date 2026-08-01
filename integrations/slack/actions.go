package slack

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	slacklib "github.com/slack-go/slack"

	"commons/platform"
	"commons/plugin"
	"commons/store"
)

// ChannelMessageAction implements plugin.ActionType for "slack.channel".
type ChannelMessageAction struct{}

func (a *ChannelMessageAction) ID() string                          { return "slack.channel" }
func (a *ChannelMessageAction) Label() string                       { return "Post to Slack Channel" }
func (a *ChannelMessageAction) RequiredCapabilities() []string      { return []string{"slack.notify"} }
func (a *ChannelMessageAction) OutputSchema() []plugin.DataFieldDef { return nil }
func (a *ChannelMessageAction) ParamSchema() []plugin.ParamDef {
	return []plugin.ParamDef{
		{Key: "channel_id", Label: "Channel", Type: "channel_select", Required: true,
			Description: "Channels are fetched from your connected Slack workspace."},
		{Key: "message_variants", Label: "Message", Type: "message_variants", Required: true, Dynamic: true,
			Description: "Add multiple variants to cycle through them sequentially."},
	}
}
func (a *ChannelMessageAction) Execute(ctx context.Context, params map[string]any, _ plugin.ActionContext) (map[string]any, error) {
	channelID, _ := params["channel_id"].(string)
	tmpl, _ := params["message_template"].(string)
	if channelID == "" {
		return nil, fmt.Errorf("slack.channel: channel_id is required")
	}
	if tmpl == "" {
		return nil, fmt.Errorf("slack.channel: message_template is required")
	}
	return nil, PostChannelMessage(ctx, channelID, tmpl)
}

// DirectMessageAction implements plugin.ActionType for "slack.dm".
type DirectMessageAction struct{}

func (a *DirectMessageAction) ID() string                          { return "slack.dm" }
func (a *DirectMessageAction) Label() string                       { return "Send Slack DM" }
func (a *DirectMessageAction) RequiredCapabilities() []string      { return []string{"slack.notify"} }
func (a *DirectMessageAction) OutputSchema() []plugin.DataFieldDef { return nil }
func (a *DirectMessageAction) ParamSchema() []plugin.ParamDef {
	return []plugin.ParamDef{
		{Key: "user_id", Label: "User", Type: "user_select", Required: true,
			Description: "Select a specific user, or pick from pipeline data to target dynamically."},
		{Key: "message_variants", Label: "Message", Type: "message_variants", Required: true, Dynamic: true,
			Description: "Add multiple variants to cycle through them sequentially."},
	}
}
func (a *DirectMessageAction) Execute(ctx context.Context, params map[string]any, _ plugin.ActionContext) (map[string]any, error) {
	userID, _ := params["user_id"].(string)
	tmpl, _ := params["message_template"].(string)
	if userID == "" {
		return nil, fmt.Errorf("slack.dm: user_id is required")
	}
	if tmpl == "" {
		return nil, fmt.Errorf("slack.dm: message_template is required")
	}
	return nil, PostDirectMessage(ctx, userID, tmpl)
}

// HandleEventsAction implements plugin.ActionType for "slack.handle_events".
type HandleEventsAction struct {
	pool   *pgxpool.Pool
	encKey []byte
}

func (a *HandleEventsAction) ID() string                          { return "slack.handle_events" }
func (a *HandleEventsAction) Label() string                       { return "Route Slack Events" }
func (a *HandleEventsAction) RequiredCapabilities() []string      { return []string{"slack.app_home"} }
func (a *HandleEventsAction) OutputSchema() []plugin.DataFieldDef { return nil }
func (a *HandleEventsAction) ParamSchema() []plugin.ParamDef      { return nil }
func (a *HandleEventsAction) Execute(ctx context.Context, params map[string]any, _ plugin.ActionContext) (map[string]any, error) {
	rawBody, err := extractRawBody(params)
	if err != nil {
		return nil, fmt.Errorf("slack.handle_events: %w", err)
	}
	dispatchEvent(ctx, a.pool, a.encKey, rawBody)
	return nil, nil
}

// HandleInteractionsAction implements plugin.ActionType for "slack.handle_interactions".
type HandleInteractionsAction struct {
	pool   *pgxpool.Pool
	encKey []byte
	pctx   plugin.PluginContext // for lazy scheduler lookup at Execute time
}

func (a *HandleInteractionsAction) ID() string                          { return "slack.handle_interactions" }
func (a *HandleInteractionsAction) Label() string                       { return "Route Slack Interactions" }
func (a *HandleInteractionsAction) RequiredCapabilities() []string      { return []string{"slack.app_home"} }
func (a *HandleInteractionsAction) OutputSchema() []plugin.DataFieldDef { return nil }
func (a *HandleInteractionsAction) ParamSchema() []plugin.ParamDef      { return nil }
func (a *HandleInteractionsAction) Execute(ctx context.Context, params map[string]any, _ plugin.ActionContext) (map[string]any, error) {
	rawBody, err := extractRawBody(params)
	if err != nil {
		return nil, fmt.Errorf("slack.handle_interactions: %w", err)
	}
	sched := a.pctx.MeetingScheduler() // resolved after all plugins initialized
	dispatchInteraction(ctx, a.pool, a.encKey, sched, rawBody)
	return nil, nil
}

// extractRawBody pulls []byte or string from params["raw_body"].
func extractRawBody(params map[string]any) ([]byte, error) {
	switch v := params["raw_body"].(type) {
	case []byte:
		return v, nil
	case string:
		return []byte(v), nil
	}
	return nil, fmt.Errorf("raw_body missing or wrong type")
}

// dispatchEvent routes an already-verified Slack Events API payload.
func dispatchEvent(ctx context.Context, pool *pgxpool.Pool, encKey []byte, body []byte) {
	var envelope slackEventEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return
	}
	if envelope.Type == "url_verification" {
		return // challenge responses are handled in Extract(); skip here
	}

	evt := envelope.Event
	client := getClient(ctx)
	switch evt.Type {
	case "team_join":
		if client == nil {
			return
		}
		var tj teamJoinEnvelope
		if err := json.Unmarshal(body, &tj); err != nil || tj.Event.User.ID == "" {
			return
		}
		u := tj.Event.User
		realName := u.Profile.RealName
		if realName == "" {
			realName = u.Profile.DisplayName
		}
		if realName == "" {
			realName = u.ID
		}
		go func() {
			bgCtx := context.Background()
			user, err := store.GetOrCreateUserByIdentity(bgCtx, pool, "slack", u.ID, realName, u.Profile.DisplayName)
			if err != nil {
				log.Printf("slack/events: team_join upsert %s: %v", u.ID, err)
				return
			}
			if err := store.EnsureDefaultRoleGroup(bgCtx, pool, user.ID); err != nil {
				log.Printf("slack/events: team_join assign default group %s: %v", u.ID, err)
			}
			if u.Profile.Email != "" {
				if err := store.UpdateUserEmail(bgCtx, pool, user.ID, u.Profile.Email); err != nil {
					log.Printf("slack/events: team_join update email %s: %v", u.ID, err)
				}
			}
			if err := plugin.Fire(bgCtx, "slack.team_join", u.ID, map[string]any{
				"user_id":      u.ID,
				"user_name":    realName,
				"display_name": u.Profile.DisplayName,
			}); err != nil {
				log.Printf("slack/events: team_join trigger: %v", err)
			}
		}()

	case "app_home_opened":
		if userID := evt.UserID(); evt.Tab == "home" && userID != "" && client != nil {
			go Publish(context.Background(), pool, encKey, client, userID)
		}
	}
}

// dispatchInteraction routes an already-verified Slack Interactions payload.
// The HTTP 200 was already sent by the processor; modal updates must use views.update API.
// If the handler writes a non-empty response body (validation error or server error),
// the error is recorded in interaction_errors, the user is notified via DM, and all
// web admins with linked Slack accounts are also notified.
func dispatchInteraction(ctx context.Context, pool *pgxpool.Pool, encKey []byte, sched platform.MeetingScheduler, body []byte) {
	rw := httptest.NewRecorder()
	dispatchInteractionBody(ctx, rw, pool, encKey, sched, body)

	if rw.Body.Len() == 0 {
		return // no response body — handler completed normally
	}

	// Extract a human-readable error message from the response body.
	errText := rw.Body.String()
	var resp struct {
		Errors map[string]string `json:"errors"`
	}
	if err := json.Unmarshal(rw.Body.Bytes(), &resp); err == nil && len(resp.Errors) > 0 {
		msgs := make([]string, 0, len(resp.Errors))
		for _, v := range resp.Errors {
			msgs = append(msgs, v)
		}
		errText = strings.Join(msgs, "\n")
	}
	errText = strings.TrimSpace(errText)

	// Parse the original body to get the Slack user ID and request type.
	var payload slacklib.InteractionCallback
	func() {
		r, err := http.NewRequestWithContext(ctx, http.MethodPost, "/", strings.NewReader(string(body)))
		if err != nil {
			return
		}
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if err := r.ParseForm(); err != nil {
			return
		}
		json.Unmarshal([]byte(r.FormValue("payload")), &payload) //nolint:errcheck
	}()

	requestType := string(payload.Type)
	if payload.View.CallbackID != "" {
		requestType = payload.View.CallbackID
	}

	// Record the error.
	rec := &store.InteractionError{
		Source:       "slack",
		RequestType:  requestType,
		SlackUserID:  payload.User.ID,
		ErrorMessage: errText,
	}
	if dbErr := store.CreateInteractionError(ctx, pool, rec); dbErr != nil {
		log.Printf("slack/interactions: record error: %v", dbErr)
	} else {
		log.Printf("slack/interactions: error recorded id=%s user=%s type=%s: %s",
			rec.ID, payload.User.ID, requestType, errText)
	}

	client := getClient(ctx)
	if client == nil || payload.User.ID == "" {
		return
	}

	// DM the user — tell them an admin has been notified.
	userMsg := fmt.Sprintf(
		"Something went wrong with your *%s* request. An administrator has been notified.\nError ID: `%s`",
		requestType, rec.ID,
	)
	if _, _, err := SendDM(ctx, client, payload.User.ID, userMsg); err != nil {
		log.Printf("slack/interactions: DM user %s: %v", payload.User.ID, err)
	}

	// DM all web admins with linked Slack accounts.
	adminIDs, err := store.ListWebAdminSlackIDs(ctx, pool)
	if err != nil {
		log.Printf("slack/interactions: list admin slack IDs: %v", err)
		return
	}
	adminMsg := fmt.Sprintf(
		"*Interaction error* (id: `%s`)\nUser: <@%s>\nType: %s\nError: %s",
		rec.ID, payload.User.ID, requestType, errText,
	)
	for _, adminID := range adminIDs {
		if adminID == payload.User.ID {
			continue // don't double-DM if the user is also an admin
		}
		if _, _, err := SendDM(ctx, client, adminID, adminMsg); err != nil {
			log.Printf("slack/interactions: DM admin %s: %v", adminID, err)
		}
	}
}
