package slack

// Event dispatch is now handled via plugin.Fire → events.Runner.
// Trigger types are registered in triggers.go.
// Historical slack_event_handlers rows are migrated to unified trigger sources.
