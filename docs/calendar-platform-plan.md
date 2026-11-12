# Calendar Platform Plan

> Status: approved 2026-11-12 · Tracked in `.tmp/sessions/2026-11-12-calendar-platform/`
>
> Eight phases building on the existing calendar system (see `db/migrations/105_calendar.sql`,
> `store/calendars.go`, `api/ics.go`, `web/adminui/calendars.go`, `integrations/slack/calendar.go`).

## Decisions Locked In

| Topic | Decision |
|---|---|
| Composite model | First-class calendar entity (`kind='composite'`), appears in admin UI + Slack switcher, serves merged ICS/embed |
| Duplicate events in composites | Auto-dedupe on `(title, start_time)` |
| My Calendar | Top entry in existing App Home switcher; create-on-demand via button + multi-select modal; reuses `users.selected_calendar_id` |
| Layouts | TeamUp-style agenda/list view on admin page, dashboard widget, and embed — **not** Slack App Home |
| Feed sync | Background job syncs syndicated feeds into `calendar_events` every 30m, uid-based matching |
| Reminders | Configurable on any calendar incl. composites; deduped across composite/member; default 1h lead |
| Recurring | Full exception editing (skip dates, single-occurrence edits, "this and following") in admin + Slack |
| Embeds | Public, unauthenticated (consistent with existing `/cal/{slug}`) |

**Confirmed defaults** (proceed unless revisited): editing "all events" of a series clears its overrides; reminder dedupe = longest lead time wins, one message per channel per occurrence; sync interval fixed at 30m in v1; agenda default range = next 4 weeks.

---

## Phase 0 — `updated_at` Fix

`store.UpdateCalendar` and `store.UpdateCalendarEvent` add `updated_at = NOW()` to their SET clauses.
No migration needed (columns already exist with defaults). Store test verifies both.

## Phase 1 — Composite Calendars (foundation)

**Migration 146** (next free number after 145):

```sql
ALTER TABLE calendars ADD COLUMN kind TEXT NOT NULL DEFAULT 'standard'
  CHECK (kind IN ('standard','composite','personal'));
ALTER TABLE calendars ADD COLUMN color TEXT;  -- palette hex, powers agenda view
CREATE TABLE calendar_members (
  composite_id UUID NOT NULL REFERENCES calendars(id) ON DELETE CASCADE,
  member_id    UUID NOT NULL REFERENCES calendars(id) ON DELETE CASCADE,
  added_at TIMESTAMPTZ DEFAULT NOW(),
  PRIMARY KEY (composite_id, member_id),
  CHECK (composite_id <> member_id)
);
CREATE INDEX idx_calendar_members_member ON calendar_members(member_id);
CREATE UNIQUE INDEX idx_personal_calendar_per_user ON calendars(created_by) WHERE kind = 'personal';
```

**Semantics:**
- Composites are read-only merged views: no direct event CRUD, no `ical_url`. Nesting allowed
  (composite inside composite) with cycle prevention on member add (walk the membership graph).
  Personal calendars can never be members.
- `store.ListCompositeEvents(compositeID, from, to)`: recursively expands members (visited-set for
  cycles), pulls stored events for manual members, live-fetches for syndicated members (until
  Phase 4), merges, dedupes on `(title, start_time)`, sorts. One helper serves grid, App Home,
  ICS, embed, and later reminders.
- Admin calendar create/edit gets a type selector + member multi-select; composite grid renders
  merged read-only (reuse the existing syndicated read-only pattern). Slack switcher lists
  composites; manage-event buttons hidden for composite kind (guard currently checks `IcalURL`
  only — add kind check in `apphome.go`).
- `/cal/{slug}` works for composites: merged + deduped output.

## Phase 2 — My Calendar

No new migration (reuses Phase 1 structures).

- App Home switcher: if the user has a `kind='personal'` composite, pin "My Calendar" as the top
  dropdown option (label override). Selecting it sets `selected_calendar_id` to the personal
  composite's ID — existing machinery serves the merged view.
- No personal calendar yet → "Create My Calendar" button near the switcher opens a modal:
  multi-select of all non-personal calendars the user can see. Submission creates the `calendars`
  row (`kind='personal'`, slug NULL, name "My Calendar", user timezone) + `calendar_members` rows,
  then republishes App Home.
- Edit flow: when My Calendar is active, an "Edit" button reopens the same modal pre-checked.
- Privacy: personal composites excluded from admin calendar list, other users' switchers,
  `/cal/{slug}`, and REST API lists. Only the owner's App Home renders it. One per user.
- Gated by `calendar.view`.
- Edge case: user deletion → sweep orphaned personal composites in the user-delete path.
- Perf note: My Calendar with N syndicated members does N live fetches per App Home open until
  Phase 4 lands. Acceptable interim.

## Phase 3 — Recurring Events (full exception editing)

**Migration 147** — iCalendar-style rules, expanded at read time:

```sql
ALTER TABLE calendar_events ADD COLUMN rrule TEXT;  -- NULL = single event; uid column exists
CREATE TABLE calendar_event_overrides (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  series_id UUID NOT NULL REFERENCES calendar_events(id) ON DELETE CASCADE,
  occurrence_start TIMESTAMPTZ NOT NULL,   -- RECURRENCE-ID semantics
  title TEXT, description TEXT, location TEXT, url TEXT,
  start_time TIMESTAMPTZ, end_time TIMESTAMPTZ, all_day BOOLEAN,
  deleted BOOLEAN NOT NULL DEFAULT FALSE,  -- true = skipped occurrence (EXDATE)
  created_at TIMESTAMPTZ DEFAULT NOW(),
  UNIQUE (series_id, occurrence_start)
);
```

**Semantics** (mirrors iCalendar, keeps ICS import/export clean):
- Series template = a `calendar_events` row with `rrule`; its `start_time` is the first occurrence.
- `store.ExpandEvents(events, overrides, from, to, loc)` — one shared expansion helper (rrule-go)
  used by grid, agenda, dashboard, App Home, ICS export, and the reminder job. Virtual occurrences
  addressed as `(series_id, occurrence_start)`.
- Skip a date → override with `deleted=true`. Edit single occurrence → override with modified
  fields. This and following → truncate original rule with `UNTIL`, spawn a new series at the edit
  point. Edit all → update template and clear overrides.
- Admin form: recurrence picker (none/daily/weekly/monthly/yearly, interval, weekday checkboxes,
  monthly "day N" vs "Nth weekday", end: never/on date/after N). Editing/deleting a recurring
  event prompts scope: *This event / This and following / All events*.
- Slack modals: same options via dropdowns + radios (Block Kit has no RRULE widget).
- `ServeICS`: emit proper `RRULE` + `EXDATE` + `RECURRENCE-ID` override VEVENTs — no expansion.
- Existing `UNIQUE(calendar_id, title, start_time)` unchanged: overrides live in their own table.

## Phase 4 — Feed Sync

**Migration 148**: `calendars.last_synced_at TIMESTAMPTZ`; index on
`calendar_events(calendar_id, uid) WHERE uid IS NOT NULL`.

- Parser unification first: extract one shared package (`ical/`) taking the best of `api/ics.go`
  and `integrations/slack/calendar.go` (context-aware fetch, tz handling, rrule + EXDATE
  expansion, windowed). Both call sites delegate to it.
- Job (`jobs/calendarsync.go`): `callbackAction` `jobs.calendar_feed_sync` +
  `seedManagedTrigger(pool, "jobs", "Calendar Feed Sync", "30m", ...)` — the meeting-reminders
  pattern. For each calendar with `ical_url`: fetch via unified parser, group occurrences by
  source UID, reconcile per UID: insert new `(start_time)`s, delete stored ones no longer in the
  feed, update changed rows. Feed events lacking UID fall back to insert-skip (never deleted).
  The `ON CONFLICT (calendar_id, title, start_time)` net stays as a safety net.
- Behavior change: syndicated calendars render from synced DB rows (grid, App Home, composites,
  My Calendar all get faster and consistent). Admin gets "Last synced X ago" + "Sync now" button.
  Read-only guard stays — manual edits would be overwritten.
- Synced recurring feed events stay expanded flat rows (rules-in-DB for syndicated feeds is a
  possible follow-up, not v1).

## Phase 5 — Reminders

**Migration 149**:

```sql
CREATE TABLE calendar_reminders (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  calendar_id UUID NOT NULL REFERENCES calendars(id) ON DELETE CASCADE,
  channel_id TEXT NOT NULL,
  lead_minutes INT NOT NULL DEFAULT 60,
  created_at TIMESTAMPTZ DEFAULT NOW()
);
CREATE TABLE calendar_reminder_sent (
  reminder_id UUID REFERENCES calendar_reminders(id) ON DELETE CASCADE,
  fingerprint TEXT NOT NULL,          -- eventID|occurrenceStart (+channel context)
  sent_at TIMESTAMPTZ DEFAULT NOW(),
  PRIMARY KEY (reminder_id, fingerprint)
);
```

- Admin UI: per-calendar reminder editor (any calendar incl. composites) — rows of
  (Slack channel dropdown, lead minutes), defaulting to 60.
- Job: `jobs.calendar_reminders` seeded every 1m, following the decoupled meeting-reminder
  architecture — jobs layer computes due reminders and fires `scheduler.calendar_reminder`
  events; the Slack plugin's trigger handler renders and posts (title, start time in calendar
  tz, location, URL, calendar name).
- Due computation: expand stored + recurring + composite-merged events per reminder config within
  `[now, now + lead + poll interval]`, skip fingerprints already sent.
- Dedupe rule: one message per `(channel, occurrence)`. If both a composite and a member cover
  the same occurrence on the same channel, the config with the longest lead time fires first and
  marks it sent; later matches suppress.
- Personal calendars excluded from channel reminders. Separate from the existing meeting-reminder
  system entirely.

## Phase 6 — Agenda/List Layout

TeamUp-style agenda, one shared templ component used by three surfaces:

- Structure: sticky day headers ("Wed, Nov 12"), one compact row per event (time range or
  "All day", title, location), calendar color chip (Phase 1 `color` column — composites show
  member colors), optional inline description behind a details toggle. Vertically scrolling,
  default range next 4 weeks with navigation.
- Admin page: `?view=agenda` toggle fragment-swapping the month grid; grid remains default.
- Dashboard widget: existing `CalendarEventsFragment` restyled with the compact agenda component
  (still next 14 days).
- Parameterized (compact/full, range) so the embed reuses it in Phase 7.

## Phase 7 — IFrame Builder

- Public embed route: `GET /embed/{slug}` — standalone page, no auth/admin chrome. Query params:
  `view=agenda|month`, range, `details=0|1`. Works for standard + composite calendars;
  `kind='personal'` explicitly rejected. Short `Cache-Control` (60s); verify no frame-blocking
  headers (XFO/CSP) on this route.
- Builder UI: "Embed" panel on each calendar's admin page — options + live preview + generated
  `<iframe>` snippet with copy button. Month-grid embed renders read-only (day-click editing
  disabled).

---

## Sequencing

**0 → 1 → 2 → 3 → 4 → 5 → 6 → 7**

My Calendar directly after composites (priority). Recurring + feed sync are both prerequisites
for reminders, so they precede it. Layouts and embeds last. Phases 3 and 4 are independent of
each other and could swap or run in parallel.

**Cross-cutting**: every phase touches `store/` + tests (`testhelpers.SetupTestDB`); `.templ`
edits require `templ generate` before build; composites get REST API support in Phase 1;
reminder admin is fragments-only in v1.
