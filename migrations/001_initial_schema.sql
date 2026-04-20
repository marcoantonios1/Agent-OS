-- 001_initial_schema.sql
-- Initial schema for Agent OS persistent storage.

CREATE TABLE IF NOT EXISTS users (
    user_id     TEXT PRIMARY KEY,
    name        TEXT    NOT NULL DEFAULT '',
    preferences TEXT    NOT NULL DEFAULT '{}',  -- JSON object
    contacts    TEXT    NOT NULL DEFAULT '[]',  -- JSON array
    style       TEXT    NOT NULL DEFAULT '',
    updated_at  DATETIME NOT NULL
);

CREATE TABLE IF NOT EXISTS projects (
    project_id  TEXT PRIMARY KEY,
    user_id     TEXT NOT NULL,
    name        TEXT NOT NULL DEFAULT '',
    phase       TEXT NOT NULL DEFAULT '',
    spec        TEXT NOT NULL DEFAULT '',
    tasks       TEXT NOT NULL DEFAULT '',  -- JSON
    active_task TEXT NOT NULL DEFAULT '',
    adrs        TEXT NOT NULL DEFAULT '[]', -- JSON array
    milestones  TEXT NOT NULL DEFAULT '[]', -- JSON array
    created_at  DATETIME NOT NULL,
    updated_at  DATETIME NOT NULL
);

CREATE TABLE IF NOT EXISTS reminders (
    id          TEXT PRIMARY KEY,
    session_id  TEXT NOT NULL,
    user_id     TEXT NOT NULL,
    channel_id  TEXT NOT NULL,
    message     TEXT NOT NULL,
    fire_at     DATETIME NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_projects_user_id  ON projects(user_id);
CREATE INDEX IF NOT EXISTS idx_reminders_fire_at ON reminders(fire_at);
