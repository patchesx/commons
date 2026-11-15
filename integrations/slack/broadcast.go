package slack

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	slacklib "github.com/slack-go/slack"
)

// broadcastInterval spaces DMs to different members. sendDM only throttles per destination, so without this
// a broadcast would hit Slack's workspace-wide limits all at once.
var broadcastInterval = time.Second

// BroadcastConfigured reports whether a bot token is set, i.e. whether a broadcast can be sent at all.
func BroadcastConfigured(ctx context.Context) bool {
	return getClient(ctx) != nil
}

// StartBroadcast works a broadcast in the background. Safe to call more than once for the same broadcast.
func StartBroadcast(pool *pgxpool.Pool, id string) {
	go runBroadcast(context.Background(), pool, id)
}

// ResumeInterruptedBroadcasts restarts broadcasts left unfinished by a restart. Each continues over its
// remaining pending recipients, so no member is messaged twice. Called once at startup.
func ResumeInterruptedBroadcasts(ctx context.Context, pool *pgxpool.Pool) {
	ids, err := ListSendingBroadcastIDs(ctx, pool)
	if err != nil {
		log.Printf("slack/broadcast: list interrupted broadcasts: %v", err)
		return
	}
	for _, id := range ids {
		log.Printf("slack/broadcast: resuming %s", id)
		StartBroadcast(pool, id)
	}
}

// broadcastScheduleInterval is how often scheduled broadcasts are checked for being due.
const broadcastScheduleInterval = 30 * time.Second

// RunBroadcastScheduler starts any scheduled broadcast once its time has come. It checks immediately, so a
// broadcast that came due while the app was down starts on boot, and then every broadcastScheduleInterval
// until ctx is cancelled.
func RunBroadcastScheduler(ctx context.Context, pool *pgxpool.Pool) {
	ticker := time.NewTicker(broadcastScheduleInterval)
	defer ticker.Stop()
	for {
		startDueBroadcasts(ctx, pool)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func startDueBroadcasts(ctx context.Context, pool *pgxpool.Pool) {
	ids, err := ActivateDueBroadcasts(ctx, pool)
	if err != nil {
		log.Printf("slack/broadcast: activate due broadcasts: %v", err)
		return
	}
	for _, id := range ids {
		log.Printf("slack/broadcast: starting scheduled %s", id)
		StartBroadcast(pool, id)
	}
}

func runBroadcast(ctx context.Context, pool *pgxpool.Pool, id string) {
	claimed, err := ClaimBroadcast(ctx, pool, id)
	if err != nil {
		log.Printf("slack/broadcast: claim %s: %v", id, err)
		return
	}
	if !claimed {
		return
	}
	client := getClient(ctx)
	if client == nil {
		log.Printf("slack/broadcast: %s: bot_token not configured", id)
		ReleaseBroadcast(ctx, pool, id)
		return
	}
	b, err := GetBroadcast(ctx, pool, id)
	if err != nil {
		log.Printf("slack/broadcast: load %s: %v", id, err)
		ReleaseBroadcast(ctx, pool, id)
		return
	}
	pending, err := ListPendingBroadcastRecipients(ctx, pool, id)
	if err != nil {
		log.Printf("slack/broadcast: list recipients for %s: %v", id, err)
		ReleaseBroadcast(ctx, pool, id)
		return
	}
	for i, r := range pending {
		if i > 0 {
			time.Sleep(broadcastInterval)
		}
		sendBroadcastDM(ctx, pool, client, b.Body, r)
	}
	if err := CompleteBroadcast(ctx, pool, id); err != nil {
		log.Printf("slack/broadcast: complete %s: %v", id, err)
	}
}

// sendBroadcastDM sends one recipient's message and records the outcome. A transient failure is already
// queued for redelivery by SendDM, so it counts as sent rather than failed; retrying it here would duplicate it.
func sendBroadcastDM(ctx context.Context, pool *pgxpool.Pool, client *slacklib.Client, body string, r BroadcastRecipient) {
	_, _, err := SendDM(ctx, client, r.SlackID, renderBroadcast(body, r.DisplayName))
	status, errMsg := RecipientSent, ""
	switch {
	case err == nil:
	case isRetryableSendError(err):
		errMsg = "queued for retry: " + err.Error()
	default:
		status, errMsg = RecipientFailed, err.Error()
		log.Printf("slack/broadcast: message %s: %v", r.DisplayName, err)
	}
	if err := MarkBroadcastRecipient(ctx, pool, r.ID, status, errMsg); err != nil {
		log.Printf("slack/broadcast: record result for %s: %v", r.DisplayName, err)
	}
}

// renderBroadcast fills in {{member_name}} for one recipient.
func renderBroadcast(body, memberName string) string {
	return strings.ReplaceAll(body, "{{member_name}}", escapeMrkdwn(memberName))
}
