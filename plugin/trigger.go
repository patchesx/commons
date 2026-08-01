package plugin

import (
	"context"
	"sort"
	"sync"
)

// TriggerType describes an internal event source that can fire pipeline actions.
// Plugins register trigger types during Init; the event runner dispatches to
// all enabled trigger_sources rows matching the trigger ID.
type TriggerType interface {
	// ID returns the unique namespaced identifier, e.g. "slack.team_join".
	ID() string

	// Label returns a human-readable name for the admin Triggers UI.
	Label() string

	// DataSchema declares the variables this trigger puts in the data map,
	// available as {{key}} references in downstream action params.
	DataSchema() []DataFieldDef

	// FireOnce returns true if each (pipeline, entityID) pair should fire at
	// most once. The event runner consults trigger_fires to enforce this.
	// Pass a non-empty entityID to Fire when FireOnce is true.
	FireOnce() bool
}

var (
	triggerMu    sync.RWMutex
	triggerTypes = map[string]TriggerType{}
)

// RegisterTriggerType adds a trigger type to the registry. Called by plugins during Init.
func RegisterTriggerType(t TriggerType) {
	triggerMu.Lock()
	defer triggerMu.Unlock()
	triggerTypes[t.ID()] = t
}

// GetTriggerType returns the registered trigger type for the given ID, if any.
func GetTriggerType(id string) (TriggerType, bool) {
	triggerMu.RLock()
	defer triggerMu.RUnlock()
	t, ok := triggerTypes[id]
	return t, ok
}

// ListTriggerTypes returns all registered trigger types sorted by label.
func ListTriggerTypes() []TriggerType {
	triggerMu.RLock()
	defer triggerMu.RUnlock()
	out := make([]TriggerType, 0, len(triggerTypes))
	for _, t := range triggerTypes {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Label() < out[j].Label() })
	return out
}

// EventDispatcher executes event pipelines for a given trigger.
// The implementation lives in the events package and is injected from main.go
// to avoid a plugin → store import cycle.
type EventDispatcher interface {
	Fire(ctx context.Context, triggerID string, entityID string, data map[string]any) error
}

var dispatcher EventDispatcher

// SetDispatcher wires the event dispatcher. Called from main.go after plugin.InitAll.
func SetDispatcher(d EventDispatcher) { dispatcher = d }

// Fire dispatches an event to all enabled pipelines registered for triggerID.
// entityID is used for fire-once deduplication; pass "" when not applicable.
// Returns nil (no-op) if no dispatcher has been set yet.
func Fire(ctx context.Context, triggerID string, entityID string, data map[string]any) error {
	if dispatcher == nil {
		return nil
	}
	return dispatcher.Fire(ctx, triggerID, entityID, data)
}
