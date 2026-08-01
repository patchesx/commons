<!-- Context: project-intelligence/business | Priority: high | Version: 1.0 | Updated: 2026-07-11 -->

# Business Domain — Commons

> Service hub for volunteer organizations — bridge third-party SaaS workflows through a self-hosted integration platform.

## Project Identity

```
Project Name: Commons
Tagline: SaaS workflow automation hub for volunteer organizations
Problem: Volunteer orgs juggle multiple SaaS tools (Slack, Zoom, Google)
         with no easy way to bridge workflows between them.
Solution: Self-hosted Go server that connects services through plugins,
          webhooks, and trigger pipelines — no custom code per workflow.
```

## Target Users

| User Segment | Who They Are | What They Need | Pain Points |
|---|---|---|---|
| Org administrators | Non-technical volunteers managing org tools | Configure integrations via web UI, no code required | Manual copying of meeting files, disconnected tools |
| Org members | Volunteers using the org's Slack/Discord/etc. | Automated notifications, resource access, tooling | Friction finding what they need across platforms |
| Self-hosters | Tech-savvy orgs running their own instance | Docker Compose deploy, add custom plugins, own their data | Existing tools are SaaS-only or closed-source |

## Value Proposition

**For organizations**:
- Automate cross-service workflows (Zoom recording → Google Drive, Slack event → notification channel)
- Single admin panel for all integrations instead of configuring each separately
- Self-hosted — no vendor lock-in, data stays on your infrastructure

**For open-source community**:
- Plugin system lets contributors add new services without touching core
- Entire stack is Go + Postgres — low ops burden, single binary

## Roadmap Context

```
Current Focus: Stable production release — current feature set is complete
Next Milestone: Evaluate and add integrations for additional services
Long-term Vision: Replicate the Slack "App Home" interface in the web UI
                  so orgs that don't use a chat app get the full experience
```

## Key Stakeholders

| Role | Responsibility |
|---|---|
| Solo developer / maintainer | Architecture, plugin system, core features |
| Open-source contributors | New integrations, bug fixes, documentation |
| Self-hosting orgs | Run their own instance, configure via admin UI |

## Related Files

- `technical-domain.md` — Stack, architecture, plugin system
- `decisions-log.md` — Why architecture choices were made
- `living-notes.md` — Active issues and open questions
