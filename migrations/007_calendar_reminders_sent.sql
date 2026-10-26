-- Calendar reminders already delivered. reminder_key identifies one reminder
-- slot for one occurrence of an event (event ID + start time + slot name), so
-- each slot fires at most once and a rescheduled event gets fresh reminders.
CREATE TABLE IF NOT EXISTS calendar_reminders_sent (
    user_id      TEXT NOT NULL,
    reminder_key TEXT NOT NULL,
    sent_at      DATETIME NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (user_id, reminder_key)
);
