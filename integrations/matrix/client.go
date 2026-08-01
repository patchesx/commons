package matrix

import (
	"context"
	"fmt"
	"sync"

	"github.com/jackc/pgx/v5/pgxpool"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"commons/store"
)

var (
	mu           sync.Mutex
	pkgPool      *pgxpool.Pool
	pkgEncKey    []byte
	cachedToken  string
	cachedClient *mautrix.Client
)

// Init stores the pool and encryption key for config lookups.
// Must be called before any other matrix package function.
func Init(pool *pgxpool.Pool, encKey []byte) {
	pkgPool = pool
	pkgEncKey = encKey
}

// getClient reads credentials from config_store and returns a mautrix Client.
// The client is cached and only rebuilt when the access token changes.
// Returns nil if matrix.enabled != "true" or credentials are not configured.
func getClient(ctx context.Context) *mautrix.Client {
	if pkgPool == nil {
		return nil
	}
	enabled, err := store.GetServiceConfig(ctx, pkgPool, "matrix", "enabled", pkgEncKey)
	if err != nil || enabled != "true" {
		return nil
	}
	token, err := store.GetServiceConfig(ctx, pkgPool, "matrix", "access_token", pkgEncKey)
	if err != nil || token == "" {
		return nil
	}
	homeserver, err := store.GetServiceConfig(ctx, pkgPool, "matrix", "homeserver", pkgEncKey)
	if err != nil || homeserver == "" {
		return nil
	}
	userIDStr, err := store.GetServiceConfig(ctx, pkgPool, "matrix", "user_id", pkgEncKey)
	if err != nil || userIDStr == "" {
		return nil
	}

	mu.Lock()
	defer mu.Unlock()
	if token != cachedToken {
		c, err := mautrix.NewClient(homeserver, id.UserID(userIDStr), token)
		if err != nil {
			return nil
		}
		cachedToken = token
		cachedClient = c
	}
	return cachedClient
}

// MatrixRoom is a minimal room descriptor.
type MatrixRoom struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ListRooms returns all rooms the bot has joined.
func ListRooms(ctx context.Context) ([]MatrixRoom, error) {
	c := getClient(ctx)
	if c == nil {
		return nil, fmt.Errorf("matrix: bot not available")
	}
	resp, err := c.JoinedRooms(ctx)
	if err != nil {
		return nil, err
	}
	var out []MatrixRoom
	for _, roomID := range resp.JoinedRooms {
		displayName := roomID.String()
		var nameContent event.RoomNameEventContent
		if err := c.StateEvent(ctx, roomID, event.StateRoomName, "", &nameContent); err == nil && nameContent.Name != "" {
			displayName = nameContent.Name
		}
		out = append(out, MatrixRoom{ID: roomID.String(), Name: displayName})
	}
	return out, nil
}

// PostRoomMessage sends a plain text message to a Matrix room.
func PostRoomMessage(ctx context.Context, roomID, text string) error {
	c := getClient(ctx)
	if c == nil {
		return fmt.Errorf("matrix: bot not available")
	}
	_, err := c.SendText(ctx, id.RoomID(roomID), text)
	return err
}

// PostDirectMessage sends a DM to a Matrix user. Creates the DM room if needed.
func PostDirectMessage(ctx context.Context, matrixUserID, text string) error {
	c := getClient(ctx)
	if c == nil {
		return fmt.Errorf("matrix: bot not available")
	}
	roomID, err := ensureDMRoom(ctx, c, id.UserID(matrixUserID))
	if err != nil {
		return fmt.Errorf("matrix: ensure DM room with %s: %w", matrixUserID, err)
	}
	_, err = c.SendText(ctx, roomID, text)
	return err
}

// ensureDMRoom creates a direct message room with the target user.
func ensureDMRoom(ctx context.Context, c *mautrix.Client, target id.UserID) (id.RoomID, error) {
	resp, err := c.CreateRoom(ctx, &mautrix.ReqCreateRoom{
		IsDirect: true,
		Invite:   []id.UserID{target},
		Preset:   "trusted_private_chat",
	})
	if err != nil {
		return "", err
	}
	return resp.RoomID, nil
}
