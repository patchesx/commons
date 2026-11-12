-- Calendar reminders: per-calendar Slack channel notification configs and a
-- sent-fingerprint ledger that prevents duplicate sends across composite/member
-- overlaps and job re-runs.
--
-- A reminder = (calendar, channel, lead_minutes). Any calendar kind except
-- personal can carry reminders. The fingerprint keys a sent occurrence on the
-- channel + event + occurrence start, so a single occurrence fires once per
-- channel no matter how many calendar configs cover it.

CREATE TABLE IF NOT EXISTS calendar_reminders (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    calendar_id  UUID NOT NULL REFERENCES calendars(id) ON DELETE CASCADE,
    channel_id   TEXT NOT NULL,
    lead_minutes INT NOT NULL DEFAULT 60 CHECK (lead_minutes > 0),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_calendar_reminders_calendar ON calendar_reminders(calendar_id);

CREATE TABLE IF NOT EXISTS calendar_reminder_sent (
    reminder_id UUID NOT NULL REFERENCES calendar_reminders(id) ON DELETE CASCADE,
    fingerprint TEXT NOT NULL,
    sent_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (reminder_id, fingerprint)
);

CREATE INDEX IF NOT EXISTS idx_calendar_reminder_sent_fingerprint ON calendar_reminder_sent(fingerprint);
