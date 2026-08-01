-- Migration 127: Consolidated config seeds — library feature and overdue config.
-- Seeds config_schema entries and default config_store values for
-- library lending settings and the overdue reminder scheduler job.

-- ============================================================================
-- Library feature settings
-- ============================================================================

INSERT INTO config_schema (service, key, label, description, sensitive, required) VALUES

    ('library', 'loan_period_days',                  'Default Loan Period (days)',
     'How many days a member can keep a book before it is overdue. 0 = no due date.',
     FALSE, FALSE),

    ('library', 'max_checkouts',                     'Max Active Checkouts per Member',
     'Maximum number of books a member can have checked out at once. 0 = unlimited.',
     FALSE, FALSE),

    ('library', 'overdue_reminder_interval_minutes', 'Overdue Reminder Interval (minutes)',
     'How often to check for and notify members of overdue books. 0 = disabled.',
     FALSE, FALSE),

    ('library', 'auto_notify_holds',                 'Auto-notify Next Hold on Return',
     'When enabled, automatically DMs the next member in the hold queue when a book is returned.',
     FALSE, FALSE),

    ('library', 'show_checkout_history',             'Show Member Checkout History',
     'When enabled, members can see their past borrowing history in the My Library modal.',
     FALSE, FALSE)

ON CONFLICT DO NOTHING;

-- Defaults: 0 = unlimited/disabled for all numeric limits.
INSERT INTO config_store (service, key, value, sensitive) VALUES
    ('library', 'loan_period_days',                  '0',     FALSE),
    ('library', 'max_checkouts',                     '0',     FALSE),
    ('library', 'overdue_reminder_interval_minutes', '0',     FALSE),
    ('library', 'auto_notify_holds',                 'false', FALSE),
    ('library', 'show_checkout_history',             'false', FALSE)
ON CONFLICT (service, key) DO NOTHING;

-- ============================================================================
-- Library overdue reminder scheduler job
-- ============================================================================

INSERT INTO config_schema (service, key, label, description, sensitive, required) VALUES

    ('jobs', 'library_overdue_enabled',          'Library Overdue Reminders',
     'Send DM reminders to members with overdue books.', FALSE, FALSE),

    ('jobs', 'library_overdue_interval_minutes', 'Library Overdue Interval (minutes)',
     'How often to check for overdue books.', FALSE, FALSE)

ON CONFLICT DO NOTHING;

INSERT INTO config_store (service, key, value, sensitive) VALUES
    ('jobs', 'library_overdue_enabled',          'true',  FALSE),
    ('jobs', 'library_overdue_interval_minutes', '1440',  FALSE)
ON CONFLICT (service, key) DO NOTHING;
