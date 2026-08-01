package discord

import (
	"context"
	"log"

	"github.com/bwmarrin/discordgo"
	"github.com/jackc/pgx/v5/pgxpool"

	"commons/store"
)

// StartGateway opens the Discord Gateway WebSocket and registers member event handlers.
// It is a no-op if the bot is not configured. The connection is closed when ctx is cancelled.
func StartGateway(ctx context.Context, pool *pgxpool.Pool, encKey []byte) error {
	s := getSession(ctx)
	if s == nil {
		log.Printf("discord/gateway: not configured, skipping")
		return nil
	}

	s.AddHandler(func(_ *discordgo.Session, m *discordgo.GuildMemberAdd) {
		if m.User == nil {
			return
		}
		displayName := m.User.GlobalName
		if displayName == "" {
			displayName = m.User.Username
		}
		if _, err := store.GetOrCreateUserByIdentity(context.Background(), pool, "discord", m.User.ID, m.User.Username, displayName); err != nil {
			log.Printf("discord/gateway: member add %s: %v", m.User.ID, err)
		}
	})

	s.AddHandler(func(_ *discordgo.Session, m *discordgo.GuildMemberRemove) {
		if m.User == nil {
			return
		}
		if err := store.UpdateIdentityStatus(context.Background(), pool, "discord", m.User.ID, "deactivated"); err != nil {
			log.Printf("discord/gateway: member remove %s: %v", m.User.ID, err)
		}
	})

	s.AddHandler(func(_ *discordgo.Session, m *discordgo.GuildMemberUpdate) {
		if m.User == nil {
			return
		}
		displayName := m.User.GlobalName
		if displayName == "" {
			displayName = m.User.Username
		}
		if _, err := store.GetOrCreateUserByIdentity(context.Background(), pool, "discord", m.User.ID, m.User.Username, displayName); err != nil {
			log.Printf("discord/gateway: member update %s: %v", m.User.ID, err)
		}
	})

	// Request guild member events.
	s.Identify.Intents = discordgo.IntentsGuildMembers

	go func() {
		if err := s.Open(); err != nil {
			log.Printf("discord/gateway: open: %v", err)
			return
		}
		log.Printf("discord/gateway: connected")
		<-ctx.Done()
		if err := s.Close(); err != nil {
			log.Printf("discord/gateway: close: %v", err)
		}
		log.Printf("discord/gateway: disconnected")
	}()

	return nil
}
