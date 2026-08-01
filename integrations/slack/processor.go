package slack

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/url"

	"github.com/jackc/pgx/v5/pgxpool"
	slacklib "github.com/slack-go/slack"

	"commons/plugin"
	"commons/store"
)

// verifySlackSignature checks the x-slack-signature header against the
// already-buffered payload using the signing_secret from config_store.
// Returns false and logs on any failure.
func verifySlackSignature(r *http.Request, payload []byte, pool *pgxpool.Pool, encKey []byte) bool {
	signingSecret, err := store.GetServiceConfig(r.Context(), pool, "slack", "signing_secret", encKey)
	if errors.Is(err, store.ErrNotFound) {
		log.Printf("slack/processor: signing_secret not configured")
		return false
	}
	if err != nil {
		log.Printf("slack/processor: failed to read signing_secret: %v", err)
		return false
	}
	verifier, err := slacklib.NewSecretsVerifier(r.Header, signingSecret)
	if err != nil {
		log.Printf("slack/processor: build verifier: %v", err)
		return false
	}
	if _, err := verifier.Write(payload); err != nil {
		log.Printf("slack/processor: write payload to verifier: %v", err)
		return false
	}
	if err := verifier.Ensure(); err != nil {
		log.Printf("slack/processor: invalid signature")
		return false
	}
	return true
}

// SlackEventsProcessor implements plugin.WebhookProcessor for the Slack Events API.
type SlackEventsProcessor struct {
	pool   *pgxpool.Pool
	encKey []byte
}

func (p *SlackEventsProcessor) Type() string                      { return "slack_events" }
func (p *SlackEventsProcessor) Label() string                     { return "Slack Events API" }
func (p *SlackEventsProcessor) VerificationMethod() string        { return "none" }
func (p *SlackEventsProcessor) DataSchema() []plugin.DataFieldDef { return nil }

// Extract verifies the Slack signature and returns raw_body for downstream actions.
// The actual event routing is handled by the slack.handle_events action type.
// URL verification challenges (sent once when configuring the app) are handled inline.
func (p *SlackEventsProcessor) Extract(_ context.Context, w http.ResponseWriter, r *http.Request, payload []byte, _ store.Webhook) (map[string]any, error) {
	if !verifySlackSignature(r, payload, p.pool, p.encKey) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return nil, nil
	}

	// Respond to Slack's one-time URL verification challenge.
	var envelope struct {
		Type      string `json:"type"`
		Challenge string `json:"challenge"`
	}
	if err := json.Unmarshal(payload, &envelope); err == nil && envelope.Type == "url_verification" {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"challenge": envelope.Challenge})
		return nil, nil
	}

	w.WriteHeader(http.StatusOK)
	return map[string]any{"raw_body": payload}, nil
}

// SlackInteractionsProcessor implements plugin.WebhookProcessor for Slack Interactivity.
type SlackInteractionsProcessor struct {
	pool   *pgxpool.Pool
	encKey []byte
}

func (p *SlackInteractionsProcessor) Type() string                      { return "slack_interactions" }
func (p *SlackInteractionsProcessor) Label() string                     { return "Slack Interactions" }
func (p *SlackInteractionsProcessor) VerificationMethod() string        { return "none" }
func (p *SlackInteractionsProcessor) DataSchema() []plugin.DataFieldDef { return nil }

// Extract verifies the Slack signature and returns raw_body for downstream actions.
// The actual interaction routing is handled by the slack.handle_interactions action type.
func (p *SlackInteractionsProcessor) Extract(_ context.Context, w http.ResponseWriter, r *http.Request, payload []byte, _ store.Webhook) (map[string]any, error) {
	if !verifySlackSignature(r, payload, p.pool, p.encKey) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return nil, nil
	}
	w.WriteHeader(http.StatusOK)
	return map[string]any{"raw_body": payload}, nil
}

// SlackSlashCommandProcessor handles Slack slash commands posted to a webhook endpoint.
// It handles the /welcome command inline and returns nil to suppress pipeline actions.
type SlackSlashCommandProcessor struct {
	pool   *pgxpool.Pool
	encKey []byte
}

func (p *SlackSlashCommandProcessor) Type() string                      { return "slack_slash_command" }
func (p *SlackSlashCommandProcessor) Label() string                     { return "Slack Slash Commands" }
func (p *SlackSlashCommandProcessor) VerificationMethod() string        { return "none" }
func (p *SlackSlashCommandProcessor) DataSchema() []plugin.DataFieldDef { return nil }

// Extract verifies the Slack signature, parses the slash command payload, and handles
// known commands inline. Returns nil so no pipeline actions run.
func (p *SlackSlashCommandProcessor) Extract(_ context.Context, w http.ResponseWriter, r *http.Request, payload []byte, _ store.Webhook) (map[string]any, error) {
	if !verifySlackSignature(r, payload, p.pool, p.encKey) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return nil, nil
	}

	values, err := url.ParseQuery(string(payload))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return nil, nil
	}

	command := values.Get("command")

	switch command {
	default:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{
			"response_type": "ephemeral",
			"text":          "Unknown command.",
		})
	}
	return nil, nil
}
