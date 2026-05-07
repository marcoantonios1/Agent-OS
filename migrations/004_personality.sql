CREATE TABLE IF NOT EXISTS personality_signals (
    user_id     TEXT NOT NULL,
    key         TEXT NOT NULL,
    value       TEXT NOT NULL,
    confidence  REAL NOT NULL DEFAULT 0.0,
    count       INTEGER NOT NULL DEFAULT 1,
    last_seen   DATETIME NOT NULL,
    PRIMARY KEY (user_id, key)
);

CREATE INDEX IF NOT EXISTS idx_personality_user ON personality_signals(user_id);
