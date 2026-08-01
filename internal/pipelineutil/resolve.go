// Package pipelineutil contains shared logic for resolving action params
// used by both the webhook pipeline runner and the event pipeline runner.
package pipelineutil

import (
	"fmt"
	"strings"
)

// ResolveActionParams builds the params map passed to an action's Execute().
// It starts with the current pipeline data map, then overlays the action's
// configured params with {{key}} template substitution applied to string values.
// Action-configured params take precedence over data map keys on collision.
func ResolveActionParams(actionParams map[string]any, data map[string]any) map[string]any {
	out := make(map[string]any, len(data)+len(actionParams))

	for k, v := range data {
		out[k] = v
	}

	for k, v := range actionParams {
		switch s := v.(type) {
		case string:
			trimmed := strings.TrimSpace(s)
			if strings.HasPrefix(trimmed, "{{") && strings.HasSuffix(trimmed, "}}") {
				key := trimmed[2 : len(trimmed)-2]
				if val, ok := data[key]; ok {
					out[k] = val
				} else {
					out[k] = nil
				}
			} else {
				out[k] = ApplyDataTemplate(s, data)
			}
		default:
			out[k] = v
		}
	}
	return out
}

// ApplyVariant selects the appropriate message variant from params["message_variants"]
// using the sequential round-robin cursor, injects the selected text into
// params["message_template"], and returns a cloned params map.
// If no message_variants key is present, returns params unchanged (no clone).
func ApplyVariant(params map[string]any, cursor int) map[string]any {
	variants, ok := params["message_variants"].([]any)
	if !ok || len(variants) == 0 {
		return params
	}
	out := make(map[string]any, len(params))
	for k, v := range params {
		out[k] = v
	}
	idx := cursor % len(variants)
	if s, ok := variants[idx].(string); ok {
		out["message_template"] = s
	}
	return out
}

// ApplyDataTemplate substitutes {{key}} placeholders in tmpl using the data map.
// Non-string values are formatted with %v. Unresolved keys become "".
func ApplyDataTemplate(tmpl string, data map[string]any) string {
	pairs := make([]string, 0, len(data)*2)
	for k, v := range data {
		pairs = append(pairs, "{{"+k+"}}", fmt.Sprintf("%v", v))
	}
	return strings.NewReplacer(pairs...).Replace(tmpl)
}
