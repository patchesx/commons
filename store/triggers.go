// Store access for the unified trigger schema: trigger_sources,
// http_trigger_config, pipeline_actions, and pipeline_filters.
//
// There is no "webhooks" table. An HTTP webhook is a trigger_sources row
// (type = 'http.webhook') joined with its http_trigger_config row; the
// Webhook struct below is that joined view. Non-HTTP event sources
// (scheduler events, etc.) use the plain TriggerSource struct.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TriggerSource is a non-HTTP event source row from trigger_sources.
// HTTP webhook sources are accessed via the Webhook type instead.
type TriggerSource struct {
	ID        string
	Type      string
	Name      string
	Enabled   bool
	ManagedBy *string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// ListTriggerSourcesByType returns all enabled trigger_sources rows with the given type.
func ListTriggerSourcesByType(ctx context.Context, pool *pgxpool.Pool, triggerType string) ([]TriggerSource, error) {
	rows, err := pool.Query(ctx, `
		SELECT id, type, name, enabled, managed_by, created_at, updated_at
		FROM trigger_sources
		WHERE type = $1 AND enabled = TRUE
		ORDER BY created_at`, triggerType)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []TriggerSource
	for rows.Next() {
		var ts TriggerSource
		if err := rows.Scan(&ts.ID, &ts.Type, &ts.Name, &ts.Enabled, &ts.ManagedBy, &ts.CreatedAt, &ts.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, ts)
	}
	return out, rows.Err()
}

// TryRecordTriggerFire inserts (trigger_id, entity_id) into trigger_fires.
// Returns (true, nil) on first fire, (false, nil) if already fired, (false, err) on DB error.
func TryRecordTriggerFire(ctx context.Context, pool *pgxpool.Pool, triggerID, entityID string) (bool, error) {
	tag, err := pool.Exec(ctx, `
		INSERT INTO trigger_fires (trigger_id, entity_id)
		VALUES ($1, $2)
		ON CONFLICT DO NOTHING`,
		triggerID, entityID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// ListAllTriggerSourcesByType returns all trigger_sources rows with the given type (enabled + disabled).
// Used by the admin UI; events/runner.go uses ListTriggerSourcesByType (enabled only).
func ListAllTriggerSourcesByType(ctx context.Context, pool *pgxpool.Pool, triggerType string) ([]TriggerSource, error) {
	rows, err := pool.Query(ctx, `
		SELECT id, type, name, enabled, managed_by, created_at, updated_at
		FROM trigger_sources
		WHERE type = $1
		ORDER BY name`, triggerType)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []TriggerSource
	for rows.Next() {
		var ts TriggerSource
		if err := rows.Scan(&ts.ID, &ts.Type, &ts.Name, &ts.Enabled, &ts.ManagedBy, &ts.CreatedAt, &ts.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, ts)
	}
	return out, rows.Err()
}

// GetTriggerSourceByID fetches a single trigger_sources row by ID. Returns ErrNotFound if absent.
func GetTriggerSourceByID(ctx context.Context, pool *pgxpool.Pool, id string) (*TriggerSource, error) {
	var ts TriggerSource
	err := pool.QueryRow(ctx, `
		SELECT id, type, name, enabled, managed_by, created_at, updated_at
		FROM trigger_sources WHERE id = $1`, id).
		Scan(&ts.ID, &ts.Type, &ts.Name, &ts.Enabled, &ts.ManagedBy, &ts.CreatedAt, &ts.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &ts, nil
}

// CreateTriggerSource inserts a new trigger_sources row (non-HTTP types only).
func CreateTriggerSource(ctx context.Context, pool *pgxpool.Pool, triggerType, name string) (*TriggerSource, error) {
	var ts TriggerSource
	err := pool.QueryRow(ctx, `
		INSERT INTO trigger_sources (type, name, enabled)
		VALUES ($1, $2, TRUE)
		RETURNING id, type, name, enabled, managed_by, created_at, updated_at`,
		triggerType, name).
		Scan(&ts.ID, &ts.Type, &ts.Name, &ts.Enabled, &ts.ManagedBy, &ts.CreatedAt, &ts.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &ts, nil
}

// DeleteTriggerSource deletes a trigger_sources row. Cascades to pipeline_actions and pipeline_filters.
func DeleteTriggerSource(ctx context.Context, pool *pgxpool.Pool, id string) error {
	_, err := pool.Exec(ctx, `DELETE FROM trigger_sources WHERE id = $1 AND type != 'http.webhook'`, id)
	return err
}

// SetTriggerSourceEnabled toggles the enabled flag on a trigger_sources row.
func SetTriggerSourceEnabled(ctx context.Context, pool *pgxpool.Pool, id string, enabled bool) error {
	_, err := pool.Exec(ctx, `UPDATE trigger_sources SET enabled = $1, updated_at = NOW() WHERE id = $2`, enabled, id)
	return err
}

// ListPipelineActions returns pipeline_actions for a trigger source, filtered by run_on,
// ordered by position then created_at.
func ListPipelineActions(ctx context.Context, pool *pgxpool.Pool, triggerID, runOn string) ([]WebhookAction, error) {
	rows, err := pool.Query(ctx, `
		SELECT id, trigger_id, type, params, position, run_on, variant_cursor, condition, action_group, retry_config, timeout_seconds
		FROM pipeline_actions
		WHERE trigger_id = $1 AND run_on = $2
		ORDER BY position, created_at`, triggerID, runOn)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []WebhookAction
	for rows.Next() {
		var a WebhookAction
		var raw, rawCondition, rawRetry []byte
		if err := rows.Scan(&a.ID, &a.WebhookID, &a.Type, &raw, &a.Position, &a.RunOn, &a.VariantCursor, &rawCondition, &a.ActionGroup, &rawRetry, &a.TimeoutSeconds); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &a.Params); err != nil {
			a.Params = map[string]any{}
		}
		if len(rawCondition) > 0 {
			var cond ActionCondition
			if json.Unmarshal(rawCondition, &cond) == nil {
				a.Condition = &cond
			}
		}
		if len(rawRetry) > 0 {
			var rc RetryConfig
			if json.Unmarshal(rawRetry, &rc) == nil {
				a.RetryConfig = &rc
			}
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// --- HTTP webhook view ---

// ListActionsByGroup returns pipeline_actions for a trigger source filtered by
// action_group, ordered by position. Used by core.for_each to load loop body actions.
func ListActionsByGroup(ctx context.Context, pool *pgxpool.Pool, triggerID, group string) ([]WebhookAction, error) {
	rows, err := pool.Query(ctx, `
		SELECT id, trigger_id, type, params, position, run_on, variant_cursor, condition, action_group, retry_config, timeout_seconds
		FROM pipeline_actions
		WHERE trigger_id = $1 AND action_group = $2
		ORDER BY position, created_at`, triggerID, group)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []WebhookAction
	for rows.Next() {
		var a WebhookAction
		var raw, rawCondition, rawRetry []byte
		if err := rows.Scan(&a.ID, &a.WebhookID, &a.Type, &raw, &a.Position, &a.RunOn, &a.VariantCursor, &rawCondition, &a.ActionGroup, &rawRetry, &a.TimeoutSeconds); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &a.Params); err != nil {
			a.Params = map[string]any{}
		}
		if len(rawCondition) > 0 {
			var cond ActionCondition
			if json.Unmarshal(rawCondition, &cond) == nil {
				a.Condition = &cond
			}
		}
		if len(rawRetry) > 0 {
			var rc RetryConfig
			if json.Unmarshal(rawRetry, &rc) == nil {
				a.RetryConfig = &rc
			}
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// --- HTTP webhook view ---

// Webhook is the joined view of a trigger_sources row (type = 'http.webhook')
// and its http_trigger_config row.
type Webhook struct {
	ID               string
	Slug             string
	Name             string
	Description      string
	Enabled          bool
	VerificationMode string
	Secret           string // plaintext after decrypt; empty if not set
	SecretHeader     string
	ProcessorType    *string
	ManagedBy        *string
	CreatedAt        time.Time
	UpdatedAt        time.Time
	Actions          []WebhookAction
	Filters          []WebhookFilter
}

// WebhookFilter is a declarative condition (pipeline_filters row) evaluated before
// pipeline actions run. All filters for a trigger are AND'd — all must pass for the
// pipeline to fire.
type WebhookFilter struct {
	ID         string
	WebhookID  string
	Position   int
	Field      string
	Operator   string  // eq, neq, gt, gte, lt, lte, contains, not_contains, exists, not_exists
	Value      *string // literal comparison value; nil for exists/not_exists
	ConfigRef  *string // "service.key" resolved from config_store at eval time; overrides Value when set
	ValueScale float64 // multiplier applied to the config_ref value before comparison (e.g. 60 for minutes→seconds)
}

// WebhookAction is a pipeline_actions row.
type WebhookAction struct {
	ID             string
	WebhookID      string
	Type           string
	Params         map[string]any
	Position       int
	RunOn          string // "success" | "filter_fail" | "action_fail"
	VariantCursor  int
	Condition      *ActionCondition // nil = always run
	ActionGroup    *string          // nil = main flow; non-nil = body action in a for_each loop
	RetryConfig    *RetryConfig     // nil = no retry
	TimeoutSeconds *int             // nil = no timeout
}

// ActionCondition is a single filter expression evaluated before an action runs.
// If the condition is not met, the action is skipped. nil means always run.
type ActionCondition struct {
	Field    string  `json:"field"`
	Operator string  `json:"operator"` // eq, neq, contains, not_contains, exists, not_exists
	Value    *string `json:"value,omitempty"`
}

// RetryConfig configures per-action retry behavior.
// nil means no retry (default).
type RetryConfig struct {
	MaxAttempts  int    `json:"max_attempts"`           // total attempts including the first
	Backoff      string `json:"backoff"`                // "fixed" or "exponential"
	InitialDelay string `json:"initial_delay"`          // e.g. "5s"
	MaxDelay     string `json:"max_delay"`              // e.g. "60s"; caps exponential growth
}

func runOnOrDefault(s string) string {
	if s == "filter_fail" || s == "action_fail" {
		return s
	}
	return "success"
}

const webhookSelectCols = `
	ts.id, htc.slug, ts.name, COALESCE(htc.description,''), ts.enabled,
	COALESCE(htc.verification_mode,'none'), COALESCE(htc.secret,''), COALESCE(htc.secret_header,''),
	htc.processor_type, ts.managed_by, ts.created_at, ts.updated_at`

const webhookFromJoin = `
	FROM trigger_sources ts
	JOIN http_trigger_config htc ON htc.trigger_id = ts.id
	WHERE ts.type = 'http.webhook'`

func scanWebhook(row interface {
	Scan(...any) error
}, encKey []byte) (Webhook, error) {
	var w Webhook
	var rawSecret string
	if err := row.Scan(&w.ID, &w.Slug, &w.Name, &w.Description, &w.Enabled,
		&w.VerificationMode, &rawSecret, &w.SecretHeader,
		&w.ProcessorType, &w.ManagedBy, &w.CreatedAt, &w.UpdatedAt); err != nil {
		return Webhook{}, err
	}
	if rawSecret != "" {
		if plain, err := Decrypt(encKey, rawSecret); err == nil {
			w.Secret = plain
		}
	}
	w.Actions = []WebhookAction{}
	w.Filters = []WebhookFilter{}
	return w, nil
}

func ListWebhooks(ctx context.Context, pool *pgxpool.Pool, encKey []byte) ([]Webhook, error) {
	rows, err := pool.Query(ctx,
		`SELECT`+webhookSelectCols+webhookFromJoin+` ORDER BY ts.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Webhook
	idxByID := map[string]int{}
	for rows.Next() {
		w, err := scanWebhook(rows, encKey)
		if err != nil {
			return nil, err
		}
		idxByID[w.ID] = len(out)
		out = append(out, w)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return []Webhook{}, nil
	}

	arows, err := pool.Query(ctx, `
		SELECT id, trigger_id, type, params, position, run_on, variant_cursor, condition, action_group, retry_config, timeout_seconds
		FROM pipeline_actions
		ORDER BY trigger_id, position, created_at`)
	if err != nil {
		return nil, err
	}
	defer arows.Close()
	for arows.Next() {
		var a WebhookAction
		var raw, rawCondition, rawRetry []byte
		if err := arows.Scan(&a.ID, &a.WebhookID, &a.Type, &raw, &a.Position, &a.RunOn, &a.VariantCursor, &rawCondition, &a.ActionGroup, &rawRetry, &a.TimeoutSeconds); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &a.Params); err != nil {
			a.Params = map[string]any{}
		}
		if len(rawCondition) > 0 {
			var cond ActionCondition
			if json.Unmarshal(rawCondition, &cond) == nil {
				a.Condition = &cond
			}
		}
		if len(rawRetry) > 0 {
			var rc RetryConfig
			if json.Unmarshal(rawRetry, &rc) == nil {
				a.RetryConfig = &rc
			}
		}
		if idx, ok := idxByID[a.WebhookID]; ok {
			out[idx].Actions = append(out[idx].Actions, a)
		}
	}
	if err := arows.Err(); err != nil {
		return nil, err
	}

	frows, err := pool.Query(ctx, `
		SELECT id, trigger_id, position, field, operator, value, config_ref, value_scale
		FROM pipeline_filters
		ORDER BY trigger_id, position, created_at`)
	if err != nil {
		return nil, err
	}
	defer frows.Close()
	for frows.Next() {
		var f WebhookFilter
		if err := frows.Scan(&f.ID, &f.WebhookID, &f.Position, &f.Field, &f.Operator, &f.Value, &f.ConfigRef, &f.ValueScale); err != nil {
			return nil, err
		}
		if idx, ok := idxByID[f.WebhookID]; ok {
			out[idx].Filters = append(out[idx].Filters, f)
		}
	}
	return out, frows.Err()
}

// GetWebhookBySlug returns the webhook with the given slug. Returns ErrNotFound if absent.
func GetWebhookBySlug(ctx context.Context, pool *pgxpool.Pool, encKey []byte, slug string) (*Webhook, error) {
	row := pool.QueryRow(ctx,
		`SELECT`+webhookSelectCols+webhookFromJoin+` AND htc.slug = $1`, slug)
	w, err := scanWebhook(row, encKey)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := loadWebhookRelations(ctx, pool, &w); err != nil {
		return nil, err
	}
	return &w, nil
}

// GetWebhookByID returns the webhook with the given trigger ID. Returns ErrNotFound if absent.
func GetWebhookByID(ctx context.Context, pool *pgxpool.Pool, encKey []byte, id string) (*Webhook, error) {
	row := pool.QueryRow(ctx,
		`SELECT`+webhookSelectCols+webhookFromJoin+` AND ts.id = $1`, id)
	w, err := scanWebhook(row, encKey)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := loadWebhookRelations(ctx, pool, &w); err != nil {
		return nil, err
	}
	return &w, nil
}

func loadWebhookRelations(ctx context.Context, pool *pgxpool.Pool, w *Webhook) error {
	actions, err := listWebhookActions(ctx, pool, w.ID)
	if err != nil {
		return err
	}
	w.Actions = actions
	filters, err := listWebhookFilters(ctx, pool, w.ID)
	if err != nil {
		return err
	}
	w.Filters = filters
	return nil
}

type CreateWebhookParams struct {
	Slug             string
	Name             string
	Description      string
	Enabled          bool
	VerificationMode string
	Secret           string // plaintext; encrypted before storage
	SecretHeader     string
	ProcessorType    string // empty = no processor
}

func CreateWebhook(ctx context.Context, pool *pgxpool.Pool, encKey []byte, p CreateWebhookParams) (*Webhook, error) {
	var encSecret *string
	if p.Secret != "" {
		s, err := Encrypt(encKey, p.Secret)
		if err != nil {
			return nil, fmt.Errorf("encrypt secret: %w", err)
		}
		encSecret = &s
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	var id string
	if err := tx.QueryRow(ctx, `
		INSERT INTO trigger_sources (type, name, enabled)
		VALUES ('http.webhook', $1, $2)
		RETURNING id`,
		p.Name, p.Enabled).Scan(&id); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO http_trigger_config (trigger_id, slug, description, verification_mode, secret, secret_header, processor_type)
		VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		id, p.Slug, nilIfEmpty(p.Description), p.VerificationMode,
		encSecret, nilIfEmpty(p.SecretHeader), nilIfEmpty(p.ProcessorType)); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return GetWebhookByID(ctx, pool, encKey, id)
}

type UpdateWebhookParams struct {
	Name             string
	Description      string
	Enabled          bool
	VerificationMode string
	// Secret: non-empty means update to this value; empty means leave unchanged.
	// To remove the secret entirely, call ClearWebhookSecret.
	Secret        string
	SecretHeader  string
	ProcessorType *string // nil = clear; non-nil = set to this value
}

func UpdateWebhook(ctx context.Context, pool *pgxpool.Pool, encKey []byte, id string, p UpdateWebhookParams) (*Webhook, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx,
		`UPDATE trigger_sources SET name=$2, enabled=$3, updated_at=NOW() WHERE id=$1`,
		id, p.Name, p.Enabled); err != nil {
		return nil, err
	}

	if p.Secret != "" {
		enc, err := Encrypt(encKey, p.Secret)
		if err != nil {
			return nil, fmt.Errorf("encrypt secret: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE http_trigger_config
			SET description=$2, verification_mode=$3, secret=$4, secret_header=$5, processor_type=$6
			WHERE trigger_id=$1`,
			id, nilIfEmpty(p.Description), p.VerificationMode, enc,
			nilIfEmpty(p.SecretHeader), p.ProcessorType); err != nil {
			return nil, err
		}
	} else {
		if _, err := tx.Exec(ctx, `
			UPDATE http_trigger_config
			SET description=$2, verification_mode=$3, secret_header=$4, processor_type=$5
			WHERE trigger_id=$1`,
			id, nilIfEmpty(p.Description), p.VerificationMode,
			nilIfEmpty(p.SecretHeader), p.ProcessorType); err != nil {
			return nil, err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return GetWebhookByID(ctx, pool, encKey, id)
}

func ClearWebhookSecret(ctx context.Context, pool *pgxpool.Pool, id string) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx,
		`UPDATE http_trigger_config SET secret=NULL, secret_header=NULL, verification_mode='none' WHERE trigger_id=$1`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE trigger_sources SET updated_at=NOW() WHERE id=$1`, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func DeleteWebhook(ctx context.Context, pool *pgxpool.Pool, id string) error {
	_, err := pool.Exec(ctx, `DELETE FROM trigger_sources WHERE id=$1`, id)
	return err
}

// --- Actions ---

func listWebhookActions(ctx context.Context, pool *pgxpool.Pool, triggerID string) ([]WebhookAction, error) {
	rows, err := pool.Query(ctx, `
		SELECT id, trigger_id, type, params, position, run_on, variant_cursor, condition, action_group, retry_config, timeout_seconds
		FROM pipeline_actions WHERE trigger_id=$1 ORDER BY position, created_at`, triggerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []WebhookAction
	for rows.Next() {
		var a WebhookAction
		var raw, rawCondition, rawRetry []byte
		if err := rows.Scan(&a.ID, &a.WebhookID, &a.Type, &raw, &a.Position, &a.RunOn, &a.VariantCursor, &rawCondition, &a.ActionGroup, &rawRetry, &a.TimeoutSeconds); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &a.Params); err != nil {
			a.Params = map[string]any{}
		}
		if len(rawCondition) > 0 {
			var cond ActionCondition
			if json.Unmarshal(rawCondition, &cond) == nil {
				a.Condition = &cond
			}
		}
		if len(rawRetry) > 0 {
			var rc RetryConfig
			if json.Unmarshal(rawRetry, &rc) == nil {
				a.RetryConfig = &rc
			}
		}
		out = append(out, a)
	}
	if out == nil {
		out = []WebhookAction{}
	}
	return out, nil
}

type WebhookActionParams struct {
	Type           string
	Params         map[string]any
	Position       int
	RunOn          string // "success" | "filter_fail" | "action_fail"
	Condition      *ActionCondition
	ActionGroup    *string
	RetryConfig    *RetryConfig
	TimeoutSeconds *int
}

func CreateWebhookAction(ctx context.Context, pool *pgxpool.Pool, webhookID string, p WebhookActionParams) (*WebhookAction, error) {
	raw, err := json.Marshal(p.Params)
	if err != nil {
		return nil, err
	}
	var rawCondition []byte
	if p.Condition != nil {
		rawCondition, _ = json.Marshal(p.Condition)
	}
	var rawRetryConfig []byte
	if p.RetryConfig != nil {
		rawRetryConfig, _ = json.Marshal(p.RetryConfig)
	}
	var id string
	if err := pool.QueryRow(ctx, `
		INSERT INTO pipeline_actions (trigger_id, type, params, position, run_on, condition, action_group, retry_config, timeout_seconds)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING id`,
		webhookID, p.Type, raw, p.Position, runOnOrDefault(p.RunOn), rawCondition, p.ActionGroup, rawRetryConfig, p.TimeoutSeconds).Scan(&id); err != nil {
		return nil, err
	}
	var a WebhookAction
	var rawOut, rawCond, rawRetry []byte
	if err := pool.QueryRow(ctx,
		`SELECT id, trigger_id, type, params, position, run_on, variant_cursor, condition, action_group, retry_config, timeout_seconds FROM pipeline_actions WHERE id=$1`, id).
		Scan(&a.ID, &a.WebhookID, &a.Type, &rawOut, &a.Position, &a.RunOn, &a.VariantCursor, &rawCond, &a.ActionGroup, &rawRetry, &a.TimeoutSeconds); err != nil {
		return nil, err
	}
	_ = json.Unmarshal(rawOut, &a.Params)
	if len(rawCond) > 0 {
		var cond ActionCondition
		if json.Unmarshal(rawCond, &cond) == nil {
			a.Condition = &cond
		}
	}
	if len(rawRetry) > 0 {
		var rc RetryConfig
		if json.Unmarshal(rawRetry, &rc) == nil {
			a.RetryConfig = &rc
		}
	}
	return &a, nil
}

func UpdateWebhookAction(ctx context.Context, pool *pgxpool.Pool, id string, p WebhookActionParams) (*WebhookAction, error) {
	raw, err := json.Marshal(p.Params)
	if err != nil {
		return nil, err
	}
	var rawCondition []byte
	if p.Condition != nil {
		rawCondition, _ = json.Marshal(p.Condition)
	}
	var rawRetryConfig []byte
	if p.RetryConfig != nil {
		rawRetryConfig, _ = json.Marshal(p.RetryConfig)
	}
	if _, err = pool.Exec(ctx,
		`UPDATE pipeline_actions SET type=$2, params=$3, position=$4, run_on=$5, condition=$6, action_group=$7, retry_config=$8, timeout_seconds=$9 WHERE id=$1`,
		id, p.Type, raw, p.Position, runOnOrDefault(p.RunOn), rawCondition, p.ActionGroup, rawRetryConfig, p.TimeoutSeconds); err != nil {
		return nil, err
	}
	var a WebhookAction
	var rawOut, rawCond, rawRetry []byte
	if err := pool.QueryRow(ctx,
		`SELECT id, trigger_id, type, params, position, run_on, variant_cursor, condition, action_group, retry_config, timeout_seconds FROM pipeline_actions WHERE id=$1`, id).
		Scan(&a.ID, &a.WebhookID, &a.Type, &rawOut, &a.Position, &a.RunOn, &a.VariantCursor, &rawCond, &a.ActionGroup, &rawRetry, &a.TimeoutSeconds); err != nil {
		return nil, err
	}
	_ = json.Unmarshal(rawOut, &a.Params)
	if len(rawCond) > 0 {
		var cond ActionCondition
		if json.Unmarshal(rawCond, &cond) == nil {
			a.Condition = &cond
		}
	}
	if len(rawRetry) > 0 {
		var rc RetryConfig
		if json.Unmarshal(rawRetry, &rc) == nil {
			a.RetryConfig = &rc
		}
	}
	return &a, nil
}

// ClaimActionVariantCursor atomically increments the variant_cursor and returns the
// value before the increment. Callers use the returned index to select the variant,
// so concurrent firings always get distinct slots even when actions load simultaneously.
func ClaimActionVariantCursor(ctx context.Context, pool *pgxpool.Pool, actionID string) (int, error) {
	var cursor int
	err := pool.QueryRow(ctx,
		`UPDATE pipeline_actions SET variant_cursor = variant_cursor + 1 WHERE id = $1 RETURNING variant_cursor - 1`,
		actionID).Scan(&cursor)
	return cursor, err
}

func DeleteWebhookAction(ctx context.Context, pool *pgxpool.Pool, id string) error {
	_, err := pool.Exec(ctx, `DELETE FROM pipeline_actions WHERE id=$1`, id)
	return err
}

// ListManagedWebhooks returns all webhooks owned by the given plugin (matched by managed_by).
// No actions or filters are loaded — callers that need only basic webhook info should use this
// instead of ListWebhooks to avoid the extra queries.
func ListManagedWebhooks(ctx context.Context, pool *pgxpool.Pool, managedBy string) ([]Webhook, error) {
	rows, err := pool.Query(ctx,
		`SELECT`+webhookSelectCols+webhookFromJoin+` AND ts.managed_by = $1`, managedBy)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Webhook
	for rows.Next() {
		w, err := scanWebhook(rows, []byte{})
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// EnsureWebhookAction creates actionType for webhookID if no action of that
// type already exists. Safe to call multiple times.
func EnsureWebhookAction(ctx context.Context, pool *pgxpool.Pool, webhookID, actionType string) error {
	var count int
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM pipeline_actions WHERE trigger_id=$1 AND type=$2`,
		webhookID, actionType).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	_, err := pool.Exec(ctx,
		`INSERT INTO pipeline_actions (trigger_id, type, params, position) VALUES ($1, $2, '{}', 0)`,
		webhookID, actionType)
	return err
}

// --- Filters ---

func listWebhookFilters(ctx context.Context, pool *pgxpool.Pool, triggerID string) ([]WebhookFilter, error) {
	rows, err := pool.Query(ctx, `
		SELECT id, trigger_id, position, field, operator, value, config_ref, value_scale
		FROM pipeline_filters WHERE trigger_id=$1 ORDER BY position, created_at`, triggerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WebhookFilter
	for rows.Next() {
		var f WebhookFilter
		if err := rows.Scan(&f.ID, &f.WebhookID, &f.Position, &f.Field, &f.Operator, &f.Value, &f.ConfigRef, &f.ValueScale); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	if out == nil {
		out = []WebhookFilter{}
	}
	return out, rows.Err()
}

// ListWebhookFilters returns all filters for a webhook, ordered by position.
func ListWebhookFilters(ctx context.Context, pool *pgxpool.Pool, webhookID string) ([]WebhookFilter, error) {
	return listWebhookFilters(ctx, pool, webhookID)
}

type WebhookFilterParams struct {
	Position   int
	Field      string
	Operator   string
	Value      *string // nil for exists/not_exists
	ConfigRef  *string // nil = use literal Value
	ValueScale float64 // multiplier for config_ref numeric values
}

func CreateWebhookFilter(ctx context.Context, pool *pgxpool.Pool, webhookID string, p WebhookFilterParams) (*WebhookFilter, error) {
	var id string
	if err := pool.QueryRow(ctx, `
		INSERT INTO pipeline_filters (trigger_id, position, field, operator, value, config_ref, value_scale)
		VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING id`,
		webhookID, p.Position, p.Field, p.Operator, p.Value, p.ConfigRef, p.ValueScale).Scan(&id); err != nil {
		return nil, err
	}
	var f WebhookFilter
	if err := pool.QueryRow(ctx, `
		SELECT id, trigger_id, position, field, operator, value, config_ref, value_scale
		FROM pipeline_filters WHERE id=$1`, id).
		Scan(&f.ID, &f.WebhookID, &f.Position, &f.Field, &f.Operator, &f.Value, &f.ConfigRef, &f.ValueScale); err != nil {
		return nil, err
	}
	return &f, nil
}

func UpdateWebhookFilter(ctx context.Context, pool *pgxpool.Pool, id string, p WebhookFilterParams) (*WebhookFilter, error) {
	if _, err := pool.Exec(ctx, `
		UPDATE pipeline_filters SET position=$2, field=$3, operator=$4, value=$5, config_ref=$6, value_scale=$7
		WHERE id=$1`,
		id, p.Position, p.Field, p.Operator, p.Value, p.ConfigRef, p.ValueScale); err != nil {
		return nil, err
	}
	var f WebhookFilter
	if err := pool.QueryRow(ctx, `
		SELECT id, trigger_id, position, field, operator, value, config_ref, value_scale
		FROM pipeline_filters WHERE id=$1`, id).
		Scan(&f.ID, &f.WebhookID, &f.Position, &f.Field, &f.Operator, &f.Value, &f.ConfigRef, &f.ValueScale); err != nil {
		return nil, err
	}
	return &f, nil
}

func DeleteWebhookFilter(ctx context.Context, pool *pgxpool.Pool, id string) error {
	_, err := pool.Exec(ctx, `DELETE FROM pipeline_filters WHERE id=$1`, id)
	return err
}

// SeedWebhookProcessorType upserts a row in webhook_processor_types.
// Called by plugin Init() to register the processor type for UI dropdowns.
func SeedWebhookProcessorType(ctx context.Context, pool *pgxpool.Pool, typ, label string) error {
	_, err := pool.Exec(ctx, `
		INSERT INTO webhook_processor_types (type, label) VALUES ($1, $2)
		ON CONFLICT (type) DO UPDATE SET label = EXCLUDED.label`,
		typ, label)
	return err
}

// UpsertManagedWebhookParams holds parameters for creating or updating a plugin-managed webhook.
type UpsertManagedWebhookParams struct {
	Slug          string
	Name          string
	ProcessorType string
	ManagedBy     string
	Enabled       bool
}

// UpsertManagedWebhook creates or updates a plugin-managed webhook.
// On conflict (slug already exists with the same ManagedBy), updates only
// processor_type — leaving name, slug, and enabled unchanged.
// Returns an error if the slug is already taken by a webhook with a different
// owner (managed_by IS NULL or managed_by != p.ManagedBy).
func UpsertManagedWebhook(ctx context.Context, pool *pgxpool.Pool, p UpsertManagedWebhookParams) (*Webhook, error) {
	// Check for slug conflict with a different owner.
	var existingTriggerID string
	var existingManagedBy *string
	err := pool.QueryRow(ctx, `
		SELECT ts.id, ts.managed_by
		FROM trigger_sources ts
		JOIN http_trigger_config htc ON htc.trigger_id = ts.id
		WHERE htc.slug = $1`, p.Slug).Scan(&existingTriggerID, &existingManagedBy)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if err == nil {
		// Row exists — check ownership.
		if existingManagedBy == nil || *existingManagedBy != p.ManagedBy {
			return nil, fmt.Errorf("webhook with slug %q already exists and is not managed by %q — rename or delete it first", p.Slug, p.ManagedBy)
		}
		// Ownership matches — update processor_type only.
		if _, err := pool.Exec(ctx,
			`UPDATE http_trigger_config SET processor_type=$2 WHERE trigger_id=$1`,
			existingTriggerID, nilIfEmpty(p.ProcessorType)); err != nil {
			return nil, err
		}
		return GetWebhookByID(ctx, pool, []byte{}, existingTriggerID)
	}

	// No existing slug — insert new rows.
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	var id string
	if err := tx.QueryRow(ctx, `
		INSERT INTO trigger_sources (type, name, enabled, managed_by)
		VALUES ('http.webhook', $1, $2, $3)
		RETURNING id`,
		p.Name, p.Enabled, p.ManagedBy).Scan(&id); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO http_trigger_config (trigger_id, slug, verification_mode, processor_type)
		VALUES ($1, $2, 'none', $3)`,
		id, p.Slug, nilIfEmpty(p.ProcessorType)); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	var w Webhook
	row := pool.QueryRow(ctx,
		`SELECT`+webhookSelectCols+webhookFromJoin+` AND ts.id = $1`, id)
	w, err = scanWebhook(row, []byte{})
	if err != nil {
		return nil, err
	}
	return &w, nil
}

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
