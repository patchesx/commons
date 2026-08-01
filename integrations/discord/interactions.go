package discord

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"

	"github.com/bwmarrin/discordgo"
	"github.com/jackc/pgx/v5/pgxpool"

	"commons/store"
)

// DispatchInteraction parses raw interaction JSON and routes it to the
// appropriate handler. It uses the interaction token to send a follow-up
// response to Discord's webhook API (since the HTTP response was already
// written as a deferred ACK by the processor).
func DispatchInteraction(ctx context.Context, pool *pgxpool.Pool, encKey []byte, rawBody string) {
	var base struct {
		Type          int             `json:"type"`
		Token         string          `json:"token"`
		ApplicationID string          `json:"application_id"`
		Data          json.RawMessage `json:"data"`
		Member        *struct {
			User *struct {
				ID         string `json:"id"`
				Username   string `json:"username"`
				GlobalName string `json:"global_name"`
				Bot        bool   `json:"bot"`
			} `json:"user"`
			Nick string `json:"nick"`
		} `json:"member"`
	}
	if err := json.Unmarshal([]byte(rawBody), &base); err != nil {
		log.Printf("discord/interactions: unmarshal: %v", err)
		return
	}

	if base.ApplicationID == "" || base.Token == "" {
		log.Printf("discord/interactions: missing application_id or token")
		return
	}

	// Reconstruct a minimal discordgo.Interaction so we can pass it to handlers.
	i := &discordgo.Interaction{
		Token: base.Token,
		AppID: base.ApplicationID,
		Type:  discordgo.InteractionType(base.Type),
	}
	if base.Member != nil {
		i.Member = &discordgo.Member{Nick: base.Member.Nick}
		if base.Member.User != nil {
			i.Member.User = &discordgo.User{
				ID:         base.Member.User.ID,
				Username:   base.Member.User.Username,
				GlobalName: base.Member.User.GlobalName,
				Bot:        base.Member.User.Bot,
			}
		}
	}

	var resp *discordgo.InteractionResponse

	switch base.Type {
	case int(discordgo.InteractionApplicationCommand):
		// Slash command
		var cmdData discordgo.ApplicationCommandInteractionData
		if err := json.Unmarshal(base.Data, &cmdData); err != nil {
			log.Printf("discord/interactions: unmarshal command data: %v", err)
			return
		}
		i.Data = cmdData
		resp = routeSlashCommand(ctx, pool, i, cmdData.Name)

	case int(discordgo.InteractionMessageComponent):
		// Button or select menu
		var compData discordgo.MessageComponentInteractionData
		if err := json.Unmarshal(base.Data, &compData); err != nil {
			log.Printf("discord/interactions: unmarshal component data: %v", err)
			return
		}
		i.Data = compData
		resp = routeComponent(ctx, pool, i, compData.CustomID)

	case int(discordgo.InteractionModalSubmit):
		// Modal submit
		var modalData discordgo.ModalSubmitInteractionData
		if err := json.Unmarshal(base.Data, &modalData); err != nil {
			log.Printf("discord/interactions: unmarshal modal data: %v", err)
			return
		}
		i.Data = modalData
		resp = routeModalSubmit(ctx, pool, i, modalData.CustomID)

	default:
		log.Printf("discord/interactions: unhandled type %d", base.Type)
		return
	}

	if resp == nil {
		return
	}

	if err := sendFollowup(ctx, base.ApplicationID, base.Token, resp); err != nil {
		log.Printf("discord/interactions: send followup: %v", err)
	}
}

// routeSlashCommand dispatches to the appropriate command handler.
func routeSlashCommand(ctx context.Context, pool *pgxpool.Pool, i *discordgo.Interaction, name string) *discordgo.InteractionResponse {
	switch name {
	case "home":
		return handleHomeCommand(ctx, pool, i)
	default:
		log.Printf("discord/interactions: unknown slash command: %s", name)
		return ephemeralText(fmt.Sprintf("Unknown command: /%s", name))
	}
}

// routeComponent dispatches button/select interactions by custom ID.
func routeComponent(ctx context.Context, pool *pgxpool.Pool, i *discordgo.Interaction, customID string) *discordgo.InteractionResponse {
	switch customID {
	case "discord:role_request":
		return handleRoleRequestButton(ctx, pool, i)
	case "discord:report_issue":
		return buildReportIssueModal()
	case "discord:resources":
		return ephemeralText("Use the Resources section in the web admin panel to browse resources.")
	case "discord:legislation":
		return ephemeralText("Use the Legislation section in the web admin panel to track bills.")
	case "discord:calendar":
		return ephemeralText("Use the Calendar section in the web admin panel to view upcoming meetings.")
	case "discord:library":
		return ephemeralText("Use the Library section in the web admin panel to browse and request books.")
	case "discord:schedule_meeting":
		return ephemeralText("Use the Scheduler section in the web admin panel to schedule meetings.")
	case "discord:manage_meetings":
		return ephemeralText("Use the Scheduler section in the web admin panel to manage meetings.")
	default:
		log.Printf("discord/interactions: unknown component custom_id: %s", customID)
		return nil
	}
}

// routeModalSubmit dispatches modal submits by custom ID.
func routeModalSubmit(ctx context.Context, pool *pgxpool.Pool, i *discordgo.Interaction, customID string) *discordgo.InteractionResponse {
	switch customID {
	case "discord:role_request_submit":
		return submitRoleRequest(ctx, pool, i)
	case "discord:report_issue_submit":
		return submitReportIssue(ctx, pool, i)
	default:
		log.Printf("discord/interactions: unknown modal custom_id: %s", customID)
		return nil
	}
}

// sendFollowup sends the interaction response to Discord's webhook followup API.
// This is used after the processor already ACKed with a deferred response.
func sendFollowup(ctx context.Context, appID, token string, resp *discordgo.InteractionResponse) error {
	body, err := json.Marshal(resp.Data)
	if err != nil {
		return fmt.Errorf("marshal response: %w", err)
	}

	url := fmt.Sprintf("https://discord.com/api/v10/webhooks/%s/%s/messages/@original", appID, token)
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	httpResp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode >= 400 {
		b, _ := io.ReadAll(httpResp.Body)
		return fmt.Errorf("discord API %d: %s", httpResp.StatusCode, b)
	}
	return nil
}

// resolveUserFromInteraction returns the internal user for the interaction member.
func resolveUserFromInteraction(ctx context.Context, pool *pgxpool.Pool, i *discordgo.Interaction) (*store.User, error) {
	if i.Member == nil || i.Member.User == nil {
		return nil, fmt.Errorf("no member in interaction")
	}
	u := i.Member.User
	displayName := u.GlobalName
	if displayName == "" {
		displayName = i.Member.Nick
	}
	if displayName == "" {
		displayName = u.Username
	}
	return store.GetOrCreateUserByIdentity(ctx, pool, "discord", u.ID, u.Username, displayName)
}
