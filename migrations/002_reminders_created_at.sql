-- 002_reminders_created_at.sql
-- Add created_at column to reminders table.
ALTER TABLE reminders ADD COLUMN created_at DATETIME NOT NULL DEFAULT '';
