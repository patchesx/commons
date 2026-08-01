package discord

import (
	"context"
	"fmt"
	"sync"

	"github.com/bwmarrin/discordgo"
	"github.com/jackc/pgx/v5/pgxpool"

	"commons/store"
)

var (
	mu            sync.Mutex
	pkgPool       *pgxpool.Pool
	pkgEncKey     []byte
	cachedToken   string
	cachedSession *discordgo.Session
)

// Init stores the pool and encryption key for config lookups.
// Must be called before any other discord package function.
func Init(pool *pgxpool.Pool, encKey []byte) {
	pkgPool = pool
	pkgEncKey = encKey
}

// getSession reads the bot token from config_store and returns a Discord session.
// The session is cached and only rebuilt when the token changes.
// Returns nil if discord.enabled != "true" or the token is not yet configured.
func getSession(ctx context.Context) *discordgo.Session {
	if pkgPool == nil {
		return nil
	}
	enabled, err := store.GetServiceConfig(ctx, pkgPool, "discord", "enabled", pkgEncKey)
	if err != nil || enabled != "true" {
		return nil
	}
	token, err := store.GetServiceConfig(ctx, pkgPool, "discord", "bot_token", pkgEncKey)
	if err != nil || token == "" {
		return nil
	}
	mu.Lock()
	defer mu.Unlock()
	if token != cachedToken {
		s, err := discordgo.New("Bot " + token)
		if err != nil {
			return nil
		}
		cachedToken = token
		cachedSession = s
	}
	return cachedSession
}

// DiscordChannel is a minimal channel descriptor returned by ListChannels.
type DiscordChannel struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ListChannels returns all text channels in the guild.
func ListChannels(ctx context.Context) ([]DiscordChannel, error) {
	s := getSession(ctx)
	if s == nil {
		return nil, fmt.Errorf("discord: bot not available")
	}
	guildID, err := store.GetServiceConfig(ctx, pkgPool, "discord", "guild_id", pkgEncKey)
	if err != nil || guildID == "" {
		return nil, fmt.Errorf("discord: guild_id not configured")
	}
	channels, err := s.GuildChannels(guildID)
	if err != nil {
		return nil, err
	}
	var out []DiscordChannel
	for _, ch := range channels {
		if ch.Type == discordgo.ChannelTypeGuildText {
			out = append(out, DiscordChannel{ID: ch.ID, Name: ch.Name})
		}
	}
	return out, nil
}

// PostChannelMessage posts a plain text message to a Discord channel by ID.
func PostChannelMessage(ctx context.Context, channelID, content string) error {
	s := getSession(ctx)
	if s == nil {
		return fmt.Errorf("discord: bot not available")
	}
	_, err := s.ChannelMessageSend(channelID, content)
	return err
}

// PostDirectMessage sends a DM to a Discord user by their Discord user ID.
func PostDirectMessage(ctx context.Context, discordUserID, content string) error {
	s := getSession(ctx)
	if s == nil {
		return fmt.Errorf("discord: bot not available")
	}
	ch, err := s.UserChannelCreate(discordUserID)
	if err != nil {
		return fmt.Errorf("discord: create DM channel with %s: %w", discordUserID, err)
	}
	_, err = s.ChannelMessageSend(ch.ID, content)
	return err
}
