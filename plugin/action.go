package plugin

import (
	"context"
	"log"
	"sort"
	"sync"
)

// ActionContext provides pipeline-level lifecycle operations to action implementations.
// The pipeline runner constructs a concrete impl and passes it to each Execute call.
// Actions that don't need phase tracking should accept it as `_ plugin.ActionContext`.
type ActionContext interface {
	JobID() string
	// SetPhase records the current sub-stage of a long-running action
	// (e.g. "downloading", "uploading", "youtube_processing").
	SetPhase(ctx context.Context, phase string) error
	// ClearPhase removes the sub-stage marker, indicating the action has finished.
	ClearPhase(ctx context.Context) error
}

// NoopActionContext is an ActionContext whose phase operations do nothing.
// Used when there is no associated job (e.g. non-recording pipelines).
type NoopActionContext struct{}

func (NoopActionContext) JobID() string                               { return "" }
func (NoopActionContext) SetPhase(_ context.Context, _ string) error { return nil }
func (NoopActionContext) ClearPhase(_ context.Context) error         { return nil }

// ActionType is the interface all registered action types must implement.
type ActionType interface {
	// ID returns the unique namespaced identifier, e.g. "slack.channel".
	ID() string

	// Label returns a human-readable name for UI dropdowns.
	Label() string

	// ParamSchema describes every configurable field for this action.
	ParamSchema() []ParamDef

	// OutputSchema declares the keys this action adds to the pipeline data map
	// when Execute succeeds. Used by the UI to show available {{key}} variables
	// to downstream steps. Return nil for actions that produce no output.
	OutputSchema() []DataFieldDef

	// RequiredCapabilities lists plugin capabilities this action depends on.
	// If any are missing at startup the action type is disabled.
	RequiredCapabilities() []string

	// Execute runs the action with fully-resolved params.
	// All {{key}} templates have already been substituted by the pipeline runner.
	// Returns output values to be merged into the pipeline's shared data map.
	Execute(ctx context.Context, params map[string]any, ac ActionContext) (map[string]any, error)
}

// ParamDef describes a single configurable field for an action type.
type ParamDef struct {
	Key         string         `json:"key"`
	Label       string         `json:"label"`
	Description string         `json:"description,omitempty"`
	Type        string         `json:"type"` // "text", "boolean", "select", "channel_select", "user_select", "storage_location_select"
	Required    bool           `json:"required"`
	Dynamic     bool           `json:"dynamic"` // accepts {{key}} template references
	Default     any            `json:"default,omitempty"`
	Options     []SelectOption `json:"options,omitempty"` // for "select" type only
}

// SelectOption is an option for a "select" type param.
type SelectOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// ActionTypeInfo is a serializable summary of an action type for API responses.
type ActionTypeInfo struct {
	ID           string         `json:"id"`
	Label        string         `json:"label"`
	PluginName   string         `json:"pluginName,omitempty"`
	ParamSchema  []ParamDef     `json:"paramSchema"`
	OutputSchema []DataFieldDef `json:"outputSchema"`
}

var (
	actionMu       sync.RWMutex
	actionRegistry = map[string]ActionType{}
	actionEnabled  = map[string]bool{}
	actionPlugin   = map[string]string{} // action ID → plugin Name()
	currentInitPlugin string
)

// SetInitPlugin records the plugin currently being initialized so that
// RegisterActionType calls during Init() can associate the action with its plugin.
func SetInitPlugin(name string) {
	currentInitPlugin = name
}

// ClearInitPlugin resets the current init plugin. Call after Init() returns.
func ClearInitPlugin() {
	currentInitPlugin = ""
}

// RegisterActionType adds an action type to the registry. Called by plugins during Init.
func RegisterActionType(a ActionType) {
	actionMu.Lock()
	defer actionMu.Unlock()
	actionRegistry[a.ID()] = a
	actionEnabled[a.ID()] = true
	actionPlugin[a.ID()] = currentInitPlugin
}

// GetActionType returns the enabled action type for the given ID, if any.
func GetActionType(id string) (ActionType, bool) {
	actionMu.RLock()
	defer actionMu.RUnlock()
	a, ok := actionRegistry[id]
	if !ok || !actionEnabled[a.ID()] {
		return nil, false
	}
	return a, true
}

// ListActionTypes returns all enabled action types with their param schemas,
// sorted alphabetically by Label.
func ListActionTypes() []ActionTypeInfo {
	actionMu.RLock()
	defer actionMu.RUnlock()
	out := make([]ActionTypeInfo, 0, len(actionRegistry))
	for _, a := range actionRegistry {
		if actionEnabled[a.ID()] {
			out = append(out, ActionTypeInfo{
				ID:           a.ID(),
				Label:        a.Label(),
				PluginName:   actionPlugin[a.ID()],
				ParamSchema:  a.ParamSchema(),
				OutputSchema: a.OutputSchema(),
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Label < out[j].Label
	})
	return out
}

// FinalizeActionTypes disables action types with unmet capability requirements.
// Must be called after plugin.InitAll() completes.
func FinalizeActionTypes() {
	actionMu.Lock()
	defer actionMu.Unlock()
	for id, a := range actionRegistry {
		for _, cap := range a.RequiredCapabilities() {
			if !hasCapabilityUnlocked(cap) {
				log.Printf("plugin: action type %q disabled — missing capability %q", id, cap)
				actionEnabled[id] = false
				break
			}
		}
	}
}

// hasCapabilityUnlocked checks capabilities without acquiring actionMu.
// Must be called with actionMu held.
func hasCapabilityUnlocked(cap string) bool {
	for _, p := range All() {
		for _, provided := range p.Provides() {
			if provided == cap {
				return true
			}
		}
	}
	return false
}
