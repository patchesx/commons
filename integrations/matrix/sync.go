package matrix

import (
	"context"
	"log"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"commons/store"
)

// StartSync starts the Matrix sync loop in a background goroutine.
// It is a no-op if the bot is not configured. The loop exits when ctx is cancelled.
func StartSync(ctx context.Context, pool *pgxpool.Pool, encKey []byte) {
	c := getClient(ctx)
	if c == nil {
		log.Printf("matrix/sync: not configured, skipping")
		return
	}

	syncer := c.Syncer.(*mautrix.DefaultSyncer)

	// Handle room messages — route to command handler.
	syncer.OnEventType(event.EventMessage, func(evtCtx context.Context, evt *event.Event) {
		if evt.Sender == c.UserID {
			return // ignore own messages
		}
		content, ok := evt.Content.Parsed.(*event.MessageEventContent)
		if !ok || content.MsgType != event.MsgText {
			return
		}
		prefix := commandPrefix(evtCtx)
		if !strings.HasPrefix(content.Body, prefix) {
			return
		}
		go HandleCommand(evtCtx, pool, encKey, c, evt.RoomID, evt.Sender, content.Body)
	})

	// Handle member events for the configured home room.
	syncer.OnEventType(event.StateMember, func(evtCtx context.Context, evt *event.Event) {
		homeRoomID, err := store.GetServiceConfig(evtCtx, pool, "matrix", "home_room_id", encKey)
		if err != nil || homeRoomID == "" || evt.RoomID != id.RoomID(homeRoomID) {
			return
		}
		if evt.StateKey == nil {
			return
		}
		memberID := id.UserID(*evt.StateKey)
		if memberID == c.UserID {
			return
		}
		content, ok := evt.Content.Parsed.(*event.MemberEventContent)
		if !ok {
			return
		}
		username := memberID.Localpart()
		displayName := content.Displayname
		if displayName == "" {
			displayName = username
		}
		switch content.Membership {
		case event.MembershipJoin:
			if _, err := store.GetOrCreateUserByIdentity(evtCtx, pool, "matrix", memberID.String(), username, displayName); err != nil {
				log.Printf("matrix/sync: member join %s: %v", memberID, err)
			}
		case event.MembershipLeave, event.MembershipBan:
			if err := store.UpdateIdentityStatus(evtCtx, pool, "matrix", memberID.String(), "deactivated"); err != nil {
				log.Printf("matrix/sync: member leave %s: %v", memberID, err)
			}
		}
	})

	go func() {
		go func() {
			if err := c.SyncWithContext(ctx); err != nil && ctx.Err() == nil {
				log.Printf("matrix/sync: error: %v", err)
			}
		}()
		<-ctx.Done()
		c.StopSync()
		log.Printf("matrix/sync: stopped")
	}()

	log.Printf("matrix/sync: started")
}

// commandPrefix reads the configured prefix (default "!") from config_store.
func commandPrefix(ctx context.Context) string {
	if pkgPool == nil {
		return "!"
	}
	p, err := store.GetServiceConfig(ctx, pkgPool, "matrix", "command_prefix", pkgEncKey)
	if err != nil || p == "" {
		return "!"
	}
	return p
}
