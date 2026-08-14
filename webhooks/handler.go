package webhooks

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"commons/pipeline"
	"commons/plugin"
	"commons/store"
)

// Handler returns an http.HandlerFunc that dispatches incoming webhook calls.
// Registered at /webhook/{slug...}; allowed methods are enforced per-webhook.
func Handler(pool *pgxpool.Pool, encKey []byte, cancelRegistry pipeline.CancelRegistry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		slug := r.PathValue("slug")

		wh, err := store.GetWebhookBySlug(r.Context(), pool, encKey, slug)
		if errors.Is(err, store.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		if err != nil {
			log.Printf("webhooks: slug=%q db error: %v", slug, err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if !wh.Enabled {
			http.NotFound(w, r)
			return
		}

		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			http.Error(w, "failed to read body", http.StatusBadRequest)
			return
		}

		if !verifySignature(wh, r, body) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		// If a processor is assigned, call Extract() to verify and parse the payload.
		if wh.ProcessorType != nil && *wh.ProcessorType != "" {
			proc, ok := plugin.GetProcessor(*wh.ProcessorType)
			if !ok {
				log.Printf("webhooks: slug=%q unknown processor type %q", slug, *wh.ProcessorType)
				http.Error(w, "processor not found", http.StatusInternalServerError)
				return
			}

			// Extract writes the HTTP response and returns a data map.
			// nil map = processor handled everything (challenge, dedup skip, etc.).
			// non-nil map = pipeline should run.
			dataMap, err := proc.Extract(r.Context(), w, r, body, *wh)
			if err != nil {
				log.Printf("webhooks: slug=%q processor=%s extract error: %v", slug, *wh.ProcessorType, err)
				return
			}
			if dataMap == nil {
				return
			}

			// Merge query string params into the data map so {{key}} templates can
			// reference them. Processor-extracted keys take precedence on collision.
			for k, v := range r.URL.Query() {
				if _, exists := dataMap[k]; !exists && len(v) > 0 {
					dataMap[k] = v[0]
				}
			}

			if len(wh.Actions) > 0 {
				source := store.TriggerSource{
					ID:        wh.ID,
					Type:      "http.webhook",
					Name:      wh.Name,
					Enabled:   wh.Enabled,
					ManagedBy: wh.ManagedBy,
					CreatedAt: wh.CreatedAt,
					UpdatedAt: wh.UpdatedAt,
				}
				dataMap["webhook_id"] = wh.ID
				dataMap["webhook_slug"] = wh.Slug
				go pipeline.RunPipeline(context.Background(), pool, encKey, source, dataMap, cancelRegistry, true)
			} else {
				log.Printf("webhooks: slug=%q processor=%s: no actions configured", slug, *wh.ProcessorType)
			}
			return
		}

		// Generic (no processor): run actions inline against query/body params.
		params := extractParams(r, body)
		log.Printf("webhooks: slug=%q firing %d action(s)", slug, len(wh.Actions))
		for _, action := range wh.Actions {
			at, ok := plugin.GetActionType(action.Type)
			if !ok {
				log.Printf("webhooks: slug=%q action=%s type=%s: unknown action type", slug, action.ID, action.Type)
				continue
			}
			resolved := resolveStringParams(action.Params, params)
			if _, err := at.Execute(r.Context(), resolved, plugin.NoopActionContext{}); err != nil {
				log.Printf("webhooks: slug=%q action=%s type=%s: error: %v", slug, action.ID, action.Type, err)
			} else {
				log.Printf("webhooks: slug=%q action=%s type=%s: ok", slug, action.ID, action.Type)
			}
		}
		w.WriteHeader(http.StatusOK)
	}
}

func verifySignature(wh *store.Webhook, r *http.Request, body []byte) bool {
	if wh.VerificationMode == "none" || wh.Secret == "" {
		return true
	}
	got := r.Header.Get(wh.SecretHeader)
	switch wh.VerificationMode {
	case "header_secret":
		return hmac.Equal([]byte(got), []byte(wh.Secret))
	case "hmac_sha256":
		mac := hmac.New(sha256.New, []byte(wh.Secret))
		mac.Write(body)
		want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
		return hmac.Equal([]byte(got), []byte(want))
	}
	return false
}

// extractParams merges query string params and body params into a flat string map.
// Supports JSON and form-encoded bodies (both application/x-www-form-urlencoded
// and multipart/form-data query portion). Query string params are included regardless
// of content type and are overridden by same-named body params.
func extractParams(r *http.Request, body []byte) map[string]string {
	params := make(map[string]string)
	for k, v := range r.URL.Query() {
		if len(v) > 0 {
			params[k] = v[0]
		}
	}
	ct := r.Header.Get("Content-Type")
	switch {
	case strings.Contains(ct, "application/json"):
		var m map[string]any
		if json.Unmarshal(body, &m) == nil {
			for k, v := range m {
				params[k] = fmt.Sprintf("%v", v)
			}
		}
	case strings.Contains(ct, "application/x-www-form-urlencoded"):
		if vals, err := url.ParseQuery(string(body)); err == nil {
			for k, v := range vals {
				if len(v) > 0 {
					params[k] = v[0]
				}
			}
		}
	}
	return params
}

// resolveStringParams substitutes {{key}} placeholders in action param string values.
func resolveStringParams(actionParams map[string]any, data map[string]string) map[string]any {
	out := make(map[string]any, len(actionParams))
	for k, v := range actionParams {
		if s, ok := v.(string); ok {
			out[k] = applyTemplate(s, data)
		} else {
			out[k] = v
		}
	}
	return out
}

// applyTemplate replaces {{key}} placeholders in tmpl with string values from data.
func applyTemplate(tmpl string, data map[string]string) string {
	pairs := make([]string, 0, len(data)*2)
	for k, v := range data {
		pairs = append(pairs, "{{"+k+"}}", v)
	}
	return strings.NewReplacer(pairs...).Replace(tmpl)
}
