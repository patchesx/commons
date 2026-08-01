package adminui

import (
	"context"
	crand "crypto/rand"
	"encoding/base64"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"commons/platform"
	"commons/store"
	"golang.org/x/crypto/bcrypt"
)

func promoteUserToAdmin(ctx context.Context, pool *pgxpool.Pool, encKey []byte, notifier platform.Notifier, userID, email string) error {
	raw := make([]byte, 16)
	if _, err := crand.Read(raw); err != nil {
		return err
	}
	tempPassword := base64.URLEncoding.EncodeToString(raw)
	hash, err := bcrypt.GenerateFromPassword([]byte(tempPassword), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	if err := store.PromoteUserToWebAdmin(ctx, pool, userID, email, string(hash)); err != nil {
		return err
	}
	if notifier != nil {
		adminURL, _ := store.GetServiceConfig(ctx, pool, "bot", "admin_url", encKey)
		var text string
		if adminURL != "" {
			text = fmt.Sprintf("You've been added as an admin.\nLogin at: %s\nTemporary password: `%s`\nYou'll be prompted to set a new password on first login.", adminURL, tempPassword)
		} else {
			text = fmt.Sprintf("You've been added as an admin.\nTemporary password: `%s`\nYou'll be prompted to set a new password on first login.", tempPassword)
		}
		notifier.NotifyUser(ctx, userID, platform.Message{Text: text}) //nolint:errcheck
	}
	return nil
}
