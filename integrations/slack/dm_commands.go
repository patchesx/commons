package slack

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	slacklib "github.com/slack-go/slack"
)

// dmContext is what a DM command handler needs to act for, and answer, the sender.
type dmContext struct {
	pool        *pgxpool.Pool
	encKey      []byte
	client      *slacklib.Client
	slackUserID string
}

// reply sends the sender a DM.
func (d dmContext) reply(ctx context.Context, text string) {
	if _, _, err := SendDM(ctx, d.client, d.slackUserID, text); err != nil {
		log.Printf("slack/dm: reply to %s: %v", d.slackUserID, err)
	}
}

// dmCommand is a programmatic direct-message command: a prefix followed by arguments. These are not a chat
// bot; a message that doesn't start with a registered prefix just gets the list of commands back.
type dmCommand struct {
	Prefix  string // first word of the message, matched case-insensitively
	Usage   string // for the command list, for example "Checkout <item code>"
	Help    string // one line on what it does
	Example string // for example "Checkout K7M-4QX"
	Handle  func(ctx context.Context, d dmContext, args string)
}

var dmCommands []dmCommand

// registerDMCommand adds a command. Features call it from init().
func registerDMCommand(c dmCommand) {
	dmCommands = append(dmCommands, c)
}

// splitDMCommand splits a message into its first word and the rest.
func splitDMCommand(text string) (prefix, args string) {
	text = strings.TrimSpace(text)
	i := strings.IndexAny(text, " \t\r\n")
	if i < 0 {
		return text, ""
	}
	return text[:i], strings.TrimSpace(text[i+1:])
}

// dmHelpText explains that the app only understands commands and lists the registered prefixes.
func dmHelpText() string {
	var b strings.Builder
	b.WriteString("I'm not a chat bot, so I can't hold a conversation. I only respond to specific commands, and a command has to start with one of these:\n")
	for _, c := range dmCommands {
		fmt.Fprintf(&b, "• `%s` — %s. Example: `%s`\n", c.Usage, c.Help, c.Example)
	}
	return strings.TrimRight(b.String(), "\n")
}

// handleDirectMessage runs a DM command, or replies with the command list. It ignores anything that isn't a
// plain message a person typed in a DM to the app, so the app never answers its own replies.
func handleDirectMessage(ctx context.Context, pool *pgxpool.Pool, encKey []byte, client *slacklib.Client, evt slackEventInner) {
	if evt.ChannelType != "im" || evt.BotID != "" || evt.Subtype != "" {
		return
	}
	slackUserID := evt.UserID()
	if slackUserID == "" || strings.TrimSpace(evt.Text) == "" {
		return
	}
	d := dmContext{pool: pool, encKey: encKey, client: client, slackUserID: slackUserID}

	prefix, args := splitDMCommand(evt.Text)
	for _, c := range dmCommands {
		if strings.EqualFold(c.Prefix, prefix) {
			c.Handle(ctx, d, args)
			return
		}
	}
	d.reply(ctx, dmHelpText())
}
