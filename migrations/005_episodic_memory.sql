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

-- sqlite-vec virtual table for semantic similarity search
-- Dimension must match EMBEDDING_DIMENSIONS env var (default 768)
CREATE VIRTUAL TABLE IF NOT EXISTS episodic_memories_vec USING vec0(
    memory_id TEXT PRIMARY KEY,
    embedding float[768]
);

CREATE INDEX IF NOT EXISTS idx_episodic_memories_user
    ON episodic_memories(user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_episodic_memories_session
    ON episodic_memories(session_id);
