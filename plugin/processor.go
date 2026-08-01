package plugin

import (
	"context"
	"net/http"
	"sync"

	"commons/store"
)

// DataFieldDef describes a single field that a processor puts in the data map.
// Used by phase 7.5 filter UI to show type-aware field pickers.
type DataFieldDef struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Type        string `json:"type"`        // "string", "number", "boolean"
	Description string `json:"description,omitempty"`
}

// WebhookProcessor handles webhook intake for a specific integration.
type WebhookProcessor interface {
	// Type returns the unique identifier stored in webhook_processor_types.
	Type() string

	// Label returns a human-readable name for UI dropdowns.
	Label() string

	// VerificationMethod declares the verification scheme used by this processor.
	// "none" means the processor performs its own verification inside Extract.
	VerificationMethod() string

	// DataSchema declares the fields this processor puts in the data map.
	// Returns nil for processors that don't declare a schema (generic webhooks).
	DataSchema() []DataFieldDef

	// Extract performs processor-specific verification and parses the payload
	// into a flat data map available for pipeline param resolution.
	//
	// Extract is responsible for writing the HTTP response in all cases —
	// the handler does not write a response when a processor is present.
	//
	// Returns (dataMap, nil) to signal the pipeline should run.
	// Returns (nil, nil) to signal Extract handled everything (challenge reply,
	// dedup skip, unsupported event) and the pipeline should not run.
	// Returns (nil, err) to signal an internal error; handler logs and returns 500.
	Extract(ctx context.Context, w http.ResponseWriter, r *http.Request, payload []byte, webhook store.Webhook) (map[string]any, error)
}

// ProcessorInfo describes a registered processor for UI dropdowns.
type ProcessorInfo struct {
	Type       string         `json:"type"`
	Label      string         `json:"label"`
	DataSchema []DataFieldDef `json:"dataSchema"` // nil for processors without a declared schema
}

var (
	processorMu       sync.RWMutex
	processorRegistry = map[string]WebhookProcessor{}
)

// RegisterProcessor adds a processor to the registry. Called by plugins during Init.
func RegisterProcessor(p WebhookProcessor) {
	processorMu.Lock()
	defer processorMu.Unlock()
	processorRegistry[p.Type()] = p
}

// GetProcessor returns the registered processor for the given type, if any.
func GetProcessor(typ string) (WebhookProcessor, bool) {
	processorMu.RLock()
	defer processorMu.RUnlock()
	p, ok := processorRegistry[typ]
	return p, ok
}

// ListProcessors returns all registered processors sorted by type.
func ListProcessors() []ProcessorInfo {
	processorMu.RLock()
	defer processorMu.RUnlock()
	out := make([]ProcessorInfo, 0, len(processorRegistry))
	for _, p := range processorRegistry {
		out = append(out, ProcessorInfo{Type: p.Type(), Label: p.Label(), DataSchema: p.DataSchema()})
	}
	return out
}
