package api

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"commons/internal/httpx"
	"commons/plugin"
	"commons/store"
)

// processorActionTypes maps processor types to the action type they imply.
var processorActionTypes = map[string]string{
	"slack_events":       "slack.handle_events",
	"slack_interactions": "slack.handle_interactions",
}

// seedProcessorAction creates the implied action for a processor type if it
// doesn't already exist on the webhook.
func seedProcessorAction(ctx context.Context, pool *pgxpool.Pool, webhookID, processorType string) {
	actionType, ok := processorActionTypes[processorType]
	if !ok {
		return
	}
	if err := store.EnsureWebhookAction(ctx, pool, webhookID, actionType); err != nil {
		log.Printf("api: seed processor action: failed to ensure %s action for webhook %s: %v", actionType, webhookID, err)
	}
}

type webhookResponse struct {
	ID               string                  `json:"id"`
	Slug             string                  `json:"slug"`
	Name             string                  `json:"name"`
	Description      string                  `json:"description"`
	Enabled          bool                    `json:"enabled"`
	VerificationMode string                  `json:"verificationMode"`
	HasSecret        bool                    `json:"hasSecret"`
	SecretHeader     string                  `json:"secretHeader"`
	ProcessorType    *string                 `json:"processorType,omitempty"`
	Actions          []webhookActionResponse `json:"actions"`
	Filters          []webhookFilterResponse `json:"filters"`
	CreatedAt        string                  `json:"createdAt"`
	UpdatedAt        string                  `json:"updatedAt"`
}

type webhookFilterResponse struct {
	ID         string  `json:"id"`
	Position   int     `json:"position"`
	Field      string  `json:"field"`
	Operator   string  `json:"operator"`
	Value      *string `json:"value"`
	ConfigRef  *string `json:"configRef"`
	ValueScale float64 `json:"valueScale"`
}

type webhookActionResponse struct {
	ID       string         `json:"id"`
	Type     string         `json:"type"`
	Params   map[string]any `json:"params"`
	Position int            `json:"position"`
	RunOn    string         `json:"runOn"`
}

func toWebhookResponse(wh store.Webhook) webhookResponse {
	actions := make([]webhookActionResponse, len(wh.Actions))
	for i, a := range wh.Actions {
		actions[i] = webhookActionResponse{ID: a.ID, Type: a.Type, Params: a.Params, Position: a.Position, RunOn: a.RunOn}
	}
	filters := make([]webhookFilterResponse, len(wh.Filters))
	for i, f := range wh.Filters {
		filters[i] = webhookFilterResponse{
			ID:         f.ID,
			Position:   f.Position,
			Field:      f.Field,
			Operator:   f.Operator,
			Value:      f.Value,
			ConfigRef:  f.ConfigRef,
			ValueScale: f.ValueScale,
		}
	}
	return webhookResponse{
		ID:               wh.ID,
		Slug:             wh.Slug,
		Name:             wh.Name,
		Description:      wh.Description,
		Enabled:          wh.Enabled,
		VerificationMode: wh.VerificationMode,
		HasSecret:        wh.Secret != "",
		SecretHeader:     wh.SecretHeader,
		ProcessorType:    wh.ProcessorType,
		Actions:          actions,
		Filters:          filters,
		CreatedAt:        wh.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		UpdatedAt:        wh.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
}

// ListWebhookProcessorTypes returns all registered webhook processor types
// for use in UI dropdowns when creating or editing a webhook.
func ListWebhookProcessorTypes() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteJSON(w, http.StatusOK, plugin.ListProcessors())
	}
}

// ListWebhookActionTypes returns all enabled action types with their param schemas.
func ListWebhookActionTypes() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteJSON(w, http.StatusOK, plugin.ListActionTypes())
	}
}

func ListWebhooks(pool *pgxpool.Pool, encKey []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		hooks, err := store.ListWebhooks(r.Context(), pool, encKey)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to list webhooks")
			return
		}
		out := make([]webhookResponse, len(hooks))
		for i, h := range hooks {
			out[i] = toWebhookResponse(h)
		}
		httpx.WriteJSON(w, http.StatusOK, out)
	}
}

func GetWebhook(pool *pgxpool.Pool, encKey []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		hook, err := store.GetWebhookByID(r.Context(), pool, encKey, r.PathValue("id"))
		if errors.Is(err, store.ErrNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "not found")
			return
		}
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to get webhook")
			return
		}
		httpx.WriteJSON(w, http.StatusOK, toWebhookResponse(*hook))
	}
}

func CreateWebhook(pool *pgxpool.Pool, encKey []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Slug             string `json:"slug"`
			Name             string `json:"name"`
			Description      string `json:"description"`
			Enabled          bool   `json:"enabled"`
			VerificationMode string `json:"verificationMode"`
			Secret           string `json:"secret"`
			SecretHeader     string `json:"secretHeader"`
			ProcessorType    string `json:"processorType"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if body.Slug == "" || body.Name == "" {
			httpx.WriteError(w, http.StatusBadRequest, "slug and name are required")
			return
		}
		hook, err := store.CreateWebhook(r.Context(), pool, encKey, store.CreateWebhookParams{
			Slug:             body.Slug,
			Name:             body.Name,
			Description:      body.Description,
			Enabled:          body.Enabled,
			VerificationMode: body.VerificationMode,
			Secret:           body.Secret,
			SecretHeader:     body.SecretHeader,
			ProcessorType:    body.ProcessorType,
		})
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to create webhook")
			return
		}
		if body.ProcessorType != "" {
			seedProcessorAction(r.Context(), pool, hook.ID, body.ProcessorType)
		}
		httpx.WriteJSON(w, http.StatusCreated, toWebhookResponse(*hook))
	}
}

func UpdateWebhook(pool *pgxpool.Pool, encKey []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		var body struct {
			Name             string  `json:"name"`
			Description      string  `json:"description"`
			Enabled          bool    `json:"enabled"`
			VerificationMode string  `json:"verificationMode"`
			Secret           string  `json:"secret"`
			SecretHeader     string  `json:"secretHeader"`
			ClearSecret      bool    `json:"clearSecret"`
			ProcessorType    *string `json:"processorType"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if body.ClearSecret {
			if err := store.ClearWebhookSecret(r.Context(), pool, id); err != nil {
				httpx.WriteError(w, http.StatusInternalServerError, "failed to clear secret")
				return
			}
			body.Secret = ""
			body.VerificationMode = "none"
			body.SecretHeader = ""
		}
		hook, err := store.UpdateWebhook(r.Context(), pool, encKey, id, store.UpdateWebhookParams{
			Name:             body.Name,
			Description:      body.Description,
			Enabled:          body.Enabled,
			VerificationMode: body.VerificationMode,
			Secret:           body.Secret,
			SecretHeader:     body.SecretHeader,
			ProcessorType:    body.ProcessorType,
		})
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to update webhook")
			return
		}
		if hook == nil {
			httpx.WriteError(w, http.StatusNotFound, "not found")
			return
		}
		if body.ProcessorType != nil && *body.ProcessorType != "" {
			seedProcessorAction(r.Context(), pool, hook.ID, *body.ProcessorType)
		}
		httpx.WriteJSON(w, http.StatusOK, toWebhookResponse(*hook))
	}
}

func DeleteWebhook(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := store.DeleteWebhook(r.Context(), pool, r.PathValue("id")); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to delete webhook")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func CreateWebhookAction(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Type     string         `json:"type"`
			Params   map[string]any `json:"params"`
			Position int            `json:"position"`
			RunOn    string         `json:"runOn"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if body.Type == "" {
			httpx.WriteError(w, http.StatusBadRequest, "type is required")
			return
		}
		if body.Params == nil {
			body.Params = map[string]any{}
		}
		action, err := store.CreateWebhookAction(r.Context(), pool, r.PathValue("id"), store.WebhookActionParams{
			Type:     body.Type,
			Params:   body.Params,
			Position: body.Position,
			RunOn:    body.RunOn,
		})
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to create action")
			return
		}
		httpx.WriteJSON(w, http.StatusCreated, webhookActionResponse{
			ID: action.ID, Type: action.Type, Params: action.Params, Position: action.Position, RunOn: action.RunOn,
		})
	}
}

func UpdateWebhookAction(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Type     string         `json:"type"`
			Params   map[string]any `json:"params"`
			Position int            `json:"position"`
			RunOn    string         `json:"runOn"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if body.Params == nil {
			body.Params = map[string]any{}
		}
		action, err := store.UpdateWebhookAction(r.Context(), pool, r.PathValue("actionId"), store.WebhookActionParams{
			Type:     body.Type,
			Params:   body.Params,
			Position: body.Position,
			RunOn:    body.RunOn,
		})
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to update action")
			return
		}
		httpx.WriteJSON(w, http.StatusOK, webhookActionResponse{
			ID: action.ID, Type: action.Type, Params: action.Params, Position: action.Position, RunOn: action.RunOn,
		})
	}
}

func DeleteWebhookAction(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := store.DeleteWebhookAction(r.Context(), pool, r.PathValue("actionId")); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to delete action")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// --- Filter handlers ---

func ListWebhookFilters(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		filters, err := store.ListWebhookFilters(r.Context(), pool, r.PathValue("id"))
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to list filters")
			return
		}
		out := make([]webhookFilterResponse, len(filters))
		for i, f := range filters {
			out[i] = webhookFilterResponse{
				ID:         f.ID,
				Position:   f.Position,
				Field:      f.Field,
				Operator:   f.Operator,
				Value:      f.Value,
				ConfigRef:  f.ConfigRef,
				ValueScale: f.ValueScale,
			}
		}
		httpx.WriteJSON(w, http.StatusOK, out)
	}
}

func CreateWebhookFilter(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Position   int     `json:"position"`
			Field      string  `json:"field"`
			Operator   string  `json:"operator"`
			Value      *string `json:"value"`
			ConfigRef  *string `json:"configRef"`
			ValueScale float64 `json:"valueScale"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if body.Field == "" || body.Operator == "" {
			httpx.WriteError(w, http.StatusBadRequest, "field and operator are required")
			return
		}
		f, err := store.CreateWebhookFilter(r.Context(), pool, r.PathValue("id"), store.WebhookFilterParams{
			Position:   body.Position,
			Field:      body.Field,
			Operator:   body.Operator,
			Value:      body.Value,
			ConfigRef:  body.ConfigRef,
			ValueScale: body.ValueScale,
		})
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to create filter")
			return
		}
		httpx.WriteJSON(w, http.StatusCreated, webhookFilterResponse{
			ID:         f.ID,
			Position:   f.Position,
			Field:      f.Field,
			Operator:   f.Operator,
			Value:      f.Value,
			ConfigRef:  f.ConfigRef,
			ValueScale: f.ValueScale,
		})
	}
}

func UpdateWebhookFilter(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Position   int     `json:"position"`
			Field      string  `json:"field"`
			Operator   string  `json:"operator"`
			Value      *string `json:"value"`
			ConfigRef  *string `json:"configRef"`
			ValueScale float64 `json:"valueScale"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if body.Field == "" || body.Operator == "" {
			httpx.WriteError(w, http.StatusBadRequest, "field and operator are required")
			return
		}
		f, err := store.UpdateWebhookFilter(r.Context(), pool, r.PathValue("filterId"), store.WebhookFilterParams{
			Position:   body.Position,
			Field:      body.Field,
			Operator:   body.Operator,
			Value:      body.Value,
			ConfigRef:  body.ConfigRef,
			ValueScale: body.ValueScale,
		})
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to update filter")
			return
		}
		httpx.WriteJSON(w, http.StatusOK, webhookFilterResponse{
			ID:         f.ID,
			Position:   f.Position,
			Field:      f.Field,
			Operator:   f.Operator,
			Value:      f.Value,
			ConfigRef:  f.ConfigRef,
			ValueScale: f.ValueScale,
		})
	}
}

func DeleteWebhookFilter(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := store.DeleteWebhookFilter(r.Context(), pool, r.PathValue("filterId")); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to delete filter")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
