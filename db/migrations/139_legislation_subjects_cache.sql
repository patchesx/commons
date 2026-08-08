-- Cached subject list per legislative body, populated by RefreshSubjects and sync.
-- OpenStates has no /subjects endpoint, so we enumerate from bill records and cache here.
CREATE TABLE IF NOT EXISTS legislative_body_subjects (
    body_id      UUID NOT NULL REFERENCES legislative_bodies(id) ON DELETE CASCADE,
    subject      TEXT NOT NULL,
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (body_id, subject)
);

-- Backfill from existing bills.subjects arrays so the cache is immediately useful
-- for bodies that already have imported bills.
INSERT INTO legislative_body_subjects (body_id, subject)
SELECT b.body_id, subject
FROM bills b
CROSS JOIN LATERAL unnest(b.subjects) AS subject
WHERE b.subjects <> '{}'
ON CONFLICT DO NOTHING;
