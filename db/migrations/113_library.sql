-- Consolidated library schema, config, and permissions.
-- Combines: 056_library.sql + 057_library_overdue.sql

CREATE SCHEMA IF NOT EXISTS library;

-- ---------------------------------------------------------------------------
-- Books: bibliographic records
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS library.books (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    title           TEXT NOT NULL,
    author          TEXT NOT NULL,
    isbn            TEXT,
    cover_url       TEXT,
    description     TEXT,
    lt_work_id      TEXT,              -- LibraryThing work ID, nullable
    added_by        UUID REFERENCES web_admins(id) ON DELETE SET NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS library_books_isbn_idx ON library.books (isbn)
    WHERE isbn IS NOT NULL AND isbn != '';

CREATE UNIQUE INDEX IF NOT EXISTS library_books_lt_work_idx ON library.books (lt_work_id)
    WHERE lt_work_id IS NOT NULL;

-- ---------------------------------------------------------------------------
-- Copies: physical copies of a book
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS library.copies (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    book_id             UUID NOT NULL REFERENCES library.books(id) ON DELETE CASCADE,
    barcode             TEXT UNIQUE,
    condition           TEXT NOT NULL DEFAULT 'good', -- good, fair, damaged
    notes               TEXT,
    loan_period_days    INT,                          -- NULL = use org default
    is_active           BOOL NOT NULL DEFAULT TRUE,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS library_copies_book_idx ON library.copies (book_id);

-- ---------------------------------------------------------------------------
-- Checkouts: tracks a copy being lent to a member
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS library.checkouts (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    copy_id             UUID NOT NULL REFERENCES library.copies(id),
    user_id             UUID NOT NULL REFERENCES users(id),
    status              TEXT NOT NULL DEFAULT 'pending',  -- pending, active, returned, denied
    due_date            DATE,                             -- set when admin approves
    checked_out_at      TIMESTAMPTZ,                      -- set when admin approves
    returned_at         TIMESTAMPTZ,
    approved_by         UUID REFERENCES web_admins(id) ON DELETE SET NULL,
    returned_by         UUID REFERENCES web_admins(id) ON DELETE SET NULL,
    notes               TEXT,
    overdue_notified_at TIMESTAMPTZ,                      -- from 057_library_overdue
    requested_at        TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS library_checkouts_copy_idx    ON library.checkouts (copy_id);
CREATE INDEX IF NOT EXISTS library_checkouts_user_idx    ON library.checkouts (user_id);
CREATE INDEX IF NOT EXISTS library_checkouts_status_idx  ON library.checkouts (status);

-- ---------------------------------------------------------------------------
-- Holds: queue of members waiting for a book (any available copy)
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS library.holds (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    book_id         UUID NOT NULL REFERENCES library.books(id) ON DELETE CASCADE,
    user_id         UUID NOT NULL REFERENCES users(id),
    position        INT NOT NULL,
    requested_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    notified_at     TIMESTAMPTZ,   -- when we DM'd them a copy is available
    fulfilled_at    TIMESTAMPTZ,   -- when the hold became a checkout
    cancelled_at    TIMESTAMPTZ,
    UNIQUE (book_id, user_id) -- one hold per user per book
);

CREATE INDEX IF NOT EXISTS library_holds_book_idx ON library.holds (book_id);
CREATE INDEX IF NOT EXISTS library_holds_user_idx ON library.holds (user_id);

-- ---------------------------------------------------------------------------
-- Config schema for library feature settings
-- ---------------------------------------------------------------------------
-- Ensure columns added by later migrations exist even if the table was created
-- by an earlier migration before this consolidation.
DO $$ BEGIN
    ALTER TABLE library.checkouts ADD COLUMN overdue_notified_at TIMESTAMPTZ;
EXCEPTION WHEN duplicate_column THEN NULL;
END $$;

INSERT INTO config_schema (service, key, label, description, sensitive, required)
VALUES
  ('library', 'loan_period_days',                 'Default Loan Period (days)',          'How many days a member can keep a book before it is overdue. 0 = no due date.',                      FALSE, FALSE),
  ('library', 'max_checkouts',                    'Max Active Checkouts per Member',     'Maximum number of books a member can have checked out at once. 0 = unlimited.',                       FALSE, FALSE),
  ('library', 'overdue_reminder_interval_minutes','Overdue Reminder Interval (minutes)', 'How often to check for and notify members of overdue books. 0 = disabled.',                          FALSE, FALSE),
  ('library', 'auto_notify_holds',               'Auto-notify Next Hold on Return',     'When enabled, automatically DMs the next member in the hold queue when a book is returned.',          FALSE, FALSE),
  ('library', 'show_checkout_history',           'Show Member Checkout History',        'When enabled, members can see their past borrowing history in the My Library modal.',                 FALSE, FALSE)
ON CONFLICT (service, key) DO NOTHING;

-- Defaults: 0 = unlimited/disabled for all numeric limits.
INSERT INTO config_store (service, key, value, sensitive)
VALUES
  ('library', 'loan_period_days',                  '0',     FALSE),
  ('library', 'max_checkouts',                     '0',     FALSE),
  ('library', 'overdue_reminder_interval_minutes', '0',     FALSE),
  ('library', 'auto_notify_holds',                 'false', FALSE),
  ('library', 'show_checkout_history',             'false', FALSE)
ON CONFLICT (service, key) DO NOTHING;

-- Scheduler config for the library overdue reminders job (from 057_library_overdue)
INSERT INTO config_schema (service, key, label, description, sensitive, required)
VALUES
  ('jobs', 'library_overdue_enabled',          'Library Overdue Reminders',          'Send DM reminders to members with overdue books.', FALSE, FALSE),
  ('jobs', 'library_overdue_interval_minutes', 'Library Overdue Interval (minutes)', 'How often to check for overdue books.',            FALSE, FALSE)
ON CONFLICT (service, key) DO NOTHING;

INSERT INTO config_store (service, key, value, sensitive)
VALUES
  ('jobs', 'library_overdue_enabled',          'true',  FALSE),
  ('jobs', 'library_overdue_interval_minutes', '1440',  FALSE)
ON CONFLICT (service, key) DO NOTHING;

-- ---------------------------------------------------------------------------
-- Permissions
-- ---------------------------------------------------------------------------
INSERT INTO permissions (key, description)
VALUES
  ('library.view',   'Browse the library catalog and manage own checkouts/holds'),
  ('library.manage', 'Approve/deny checkout requests, manage returns and holds, edit catalog')
ON CONFLICT (key) DO NOTHING;

-- Seed to roles
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM roles r, permissions p
WHERE r.name IN ('owner', 'viewer') AND p.key = 'library.view'
ON CONFLICT DO NOTHING;

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM roles r, permissions p
WHERE r.name = 'owner' AND p.key = 'library.manage'
ON CONFLICT DO NOTHING;

-- ---------------------------------------------------------------------------
-- LibraryThing integration
-- ---------------------------------------------------------------------------
INSERT INTO integrations (type, name)
SELECT 'librarything', 'LibraryThing'
WHERE NOT EXISTS (SELECT 1 FROM integrations WHERE type = 'librarything');

INSERT INTO config_schema (service, key, label, description, sensitive, required)
VALUES
  ('librarything', 'api_key',      'API Key',      'LibraryThing developer API key.',                                                                           TRUE,  FALSE),
  ('librarything', 'sync_enabled', 'Catalog Sync', 'Write new books back to your LibraryThing member catalog when added here.',                                 FALSE, FALSE)
ON CONFLICT (service, key) DO NOTHING;
