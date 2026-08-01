package discord

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"log"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"commons/plugin"
	"commons/store"
)

// DiscordInteractionsProcessor implements plugin.WebhookProcessor for Discord Interactions.
type DiscordInteractionsProcessor struct {
	pool   *pgxpool.Pool
	encKey []byte
}

func (p *DiscordInteractionsProcessor) Type() string               { return "discord_interactions" }
func (p *DiscordInteractionsProcessor) Label() string              { return "Discord Interactions" }
func (p *DiscordInteractionsProcessor) VerificationMethod() string { return "ed25519" }
func (p *DiscordInteractionsProcessor) DataSchema() []plugin.DataFieldDef {
	return []plugin.DataFieldDef{
		{Key: "raw_body", Label: "Raw Body", Type: "string"},
	}
}

// Extract verifies the Ed25519 signature and returns raw_body for downstream actions.
// PING interactions are responded to immediately and return nil (pipeline does not run).
func (p *DiscordInteractionsProcessor) Extract(ctx context.Context, w http.ResponseWriter, r *http.Request, payload []byte, _ store.Webhook) (map[string]any, error) {
	publicKeyHex, err := store.GetServiceConfig(ctx, p.pool, "discord", "public_key", p.encKey)
	if err != nil || publicKeyHex == "" {
		log.Printf("discord/processor: public_key not configured")
		http.Error(w, "public key not configured", http.StatusInternalServerError)
		return nil, nil
	}

	publicKeyBytes, err := hex.DecodeString(publicKeyHex)
	if err != nil {
		log.Printf("discord/processor: invalid public_key hex: %v", err)
		http.Error(w, "invalid public key", http.StatusInternalServerError)
		return nil, nil
	}

	sig := r.Header.Get("X-Signature-Ed25519")
	timestamp := r.Header.Get("X-Signature-Timestamp")

	sigBytes, err := hex.DecodeString(sig)
	if err != nil || !ed25519.Verify(ed25519.PublicKey(publicKeyBytes), []byte(timestamp+string(payload)), sigBytes) {
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return nil, nil
	}

	// Respond to PING immediately — Discord sends this to verify the endpoint.
	var interaction struct {
		Type int `json:"type"`
	}
	if err := json.Unmarshal(payload, &interaction); err == nil && interaction.Type == 1 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"type":1}`))
		return nil, nil
	}

	// For all other interactions, respond with a deferred message so Discord doesn't
	// time out while the pipeline processes the action.
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	// Type 5 = DEFERRED_CHANNEL_MESSAGE_WITH_SOURCE with ephemeral flag (64).
	w.Write([]byte(`{"type":5,"data":{"flags":64}}`))

	return map[string]any{"raw_body": string(payload)}, nil
}
