-- A broadcast can be scheduled. Until it is due it has status 'scheduled' and no recipient rows: recipients
-- are snapshotted when sending starts, so members who join or leave in between are handled correctly.
-- Status is now: scheduled | sending | done | cancelled.
ALTER TABLE slack.broadcasts ADD COLUMN IF NOT EXISTS scheduled_for TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS idx_broadcasts_scheduled ON slack.broadcasts (scheduled_for) WHERE status = 'scheduled';
