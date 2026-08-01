-- Web inbox notifications for portal users.
-- Referenced by store/notifications.go (CreateNotification, ListNotifications,
-- CountUnread, MarkRead, MarkAllRead) and store.NotifyUser.
-- Was missing from the consolidated migration set — created here.
CREATE TABLE IF NOT EXISTS notifications (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    type        TEXT NOT NULL,
    title       TEXT NOT NULL,
    body        TEXT,
    action_url  TEXT,
    read        BOOLEAN NOT NULL DEFAULT FALSE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_notifications_user_id ON notifications (user_id);
CREATE INDEX IF NOT EXISTS idx_notifications_user_read ON notifications (user_id, read);
