-- Episodic memories — specific things extracted from conversations
CREATE TABLE IF NOT EXISTS episodic_memories (
    id               TEXT PRIMARY KEY,
    user_id          TEXT NOT NULL,
    channel          TEXT NOT NULL,
    session_id       TEXT NOT NULL,
    content          TEXT NOT NULL,
    source           TEXT NOT NULL,
    importance       REAL NOT NULL DEFAULT 0.5,
    created_at       DATETIME NOT NULL DEFAULT (datetime('now')),
    last_accessed_at DATETIME,
    access_count     INTEGER NOT NULL DEFAULT 0
);

-- NOTE: The sqlite-vec virtual table (episodic_memories_vec) is created at
-- runtime by internal/memory/episodic.NewSQLiteStore after sqlite_vec.Auto()
-- has registered the vec0 module. It cannot be created in a migration because
-- the vec0 module is only available when the CGO sqlite-vec extension is loaded.

CREATE INDEX IF NOT EXISTS idx_episodic_memories_user
    ON episodic_memories(user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_episodic_memories_session
    ON episodic_memories(session_id);
