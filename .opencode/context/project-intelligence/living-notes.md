<!-- Context: project-intelligence/living | Priority: medium | Version: 1.0 | Updated: 2026-07-11 -->

# Living Notes — Commons

> Active issues, technical debt, and open questions. Update as things resolve or emerge.

## Open Questions

- **Web UI App Home replication** — planned but not yet started. Would give non-chat-app orgs the full experience currently only available via Slack.
- **CI / linting** — No CI config, no golangci-lint, no Makefile. Intentional or just low priority?

## Known Debt

- **Slack sync decomposition** — `SyncAllUsers` is still a thin-wrapper action (`slack.sync_members`). Full decomposition into granular actions (`slack.fetch_members`, `slack.upsert_member`, etc.) deferred. The `core.for_each` action enables custom loops using granular actions when they're added.
- **In-memory retry delays** — Retry backoff uses `time.After` (in-memory). On crash during a retry delay, the run resumes from `current_step` and the first attempt re-runs. Acceptable for v1; durable retry delays deferred.
- **for_each loop index not persisted** — On crash mid-loop, the entire loop re-runs from item 0. Loop-index persistence deferred.
- **Template placeholders** — `business-domain.md` and `decisions-log.md` were empty templates until 2026-07-11. Other project-intelligence files may still have stub content.
- **Plugin migration discovery in tests** — testhelpers reads SQL files from disk via glob to avoid import cycles. Fragile if plugins ever move to inline Go migrations.

## Completed

- [2026-07-11] Populated technical-domain.md with actual project patterns
- [2026-07-11] Populated business-domain.md with project identity and users
- [2026-07-11] Created AGENTS.md with build/test/architecture reference
