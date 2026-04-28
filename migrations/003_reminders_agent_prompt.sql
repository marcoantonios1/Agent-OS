-- 003_reminders_agent_prompt.sql
-- Add agent_prompt column for context-aware reminder firing.
ALTER TABLE reminders ADD COLUMN agent_prompt TEXT NOT NULL DEFAULT '';
