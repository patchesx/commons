// Package webhooks handles incoming HTTP webhook intake and dispatches to the
// unified pipeline runner. The pipeline execution logic (RunPipeline, filters,
// job tracking) has been moved to the pipeline/ package and is shared by all
// three trigger types (webhooks, events, scheduled).
package webhooks
