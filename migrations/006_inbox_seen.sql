-- Emails the inbox monitor has already triaged, so each one is evaluated
-- (and notified about) at most once.
CREATE TABLE IF NOT EXISTS inbox_seen (
    user_id   TEXT NOT NULL,
    email_id  TEXT NOT NULL,
    seen_at   DATETIME NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (user_id, email_id)
);
