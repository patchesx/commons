<!-- Context: project-intelligence/decisions | Priority: medium | Version: 1.0 | Updated: 2026-07-11 -->

# Decisions Log — Commons

> Major architectural decisions with rationale and alternatives considered.

| # | Decision | Rationale | Impact |
|---|---|---|---|
| 1 | **Go single binary** over multi-service or interpreted languages | Simplicity, low ops burden. Single `docker compose up` starts everything. No runtime dependencies, no interpreter to manage. | Deployment is a single binary. Adding integrations doesn't require new services. |
| 2 | **a-h/templ + HTMX** over JS framework (React/Vue/Svelte) | Server-side rendering limits performance issues across devices. No Node toolchain, type-safe Go templates, zero JS dependencies. | UI development stays in Go. Tradeoff: less rich interactivity than a SPA, but acceptable for an admin panel. |
| 3 | **Plugin system via blank imports + init()** over config-driven plugins | Compile-time safety — broken config caught at build, not runtime. Self-registration eliminates wiring boilerplate. | Adding a plugin requires a code change (blank import in main.go). Open to shifting to config-driven plugins if adoption requires non-code plugin installation. |
| 4 | **Two-tier migrations (core → plugin)** over flat migration directory | Plugins are optional and independent. Core schema must run first. Plugin migrations shouldn't block core upgrades. | Adding a plugin's migrations is isolated. Test setup reads plugin SQL from disk to avoid import cycles with store package. |
| 5 | **PostgreSQL** over SQLite or MySQL | Mature, well-supported by pgx driver. Docker Compose makes Postgres trivial to run. Required for features like FORCE on DROP DATABASE in tests. | All deployments need Postgres 16. No embedded DB path for single-container deploys. |
| 6 | **Docker Compose** over Kubernetes or bare-metal | Target audience is volunteer orgs with minimal ops expertise. `docker compose up` is a single command. | Won't scale to multi-node deployments without additional tooling — but that's not the target. |

## Related Files

- `technical-domain.md` — Implementation details for these decisions
- `living-notes.md` — Decisions still under discussion
