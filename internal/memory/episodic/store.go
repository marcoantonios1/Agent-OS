package episodic

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Memory represents a single episodic memory extracted from a conversation.
type Memory struct {
	ID             string
	UserID         string
	Channel        string
	SessionID      string
	Content        string
	Source         string
	Importance     float64
	CreatedAt      time.Time
	LastAccessedAt *time.Time
	AccessCount    int
}

// Store is the interface for persisting and retrieving episodic memories.
type Store interface {
	Save(ctx context.Context, m Memory, embedding []float32) error
	// SaveText embeds the memory's Content using the store's configured embed func,
	// then calls Save. Convenience method for callers that don't hold embeddings.
	SaveText(ctx context.Context, m Memory) error
	Search(ctx context.Context, userID string, queryEmbedding []float32, k int) ([]Memory, error)
	SearchByText(ctx context.Context, userID, query string, k int) ([]Memory, error)
	Recent(ctx context.Context, userID string, k int) ([]Memory, error)
	Delete(ctx context.Context, id string) error
	Prune(ctx context.Context, userID string, maxAge time.Duration) error
}

// SQLiteStore implements Store backed by a SQLite database with sqlite-vec for
// vector similarity search.
type SQLiteStore struct {
	db    *sql.DB
	embed func(ctx context.Context, text string) ([]float32, error)
	dims  int // embedding dimensions, default 768
}

// NewSQLiteStore returns a SQLiteStore using the provided *sql.DB and embed function.
// The embed function is used by SearchByText to convert text queries to vectors.
//
// The database must already have the episodic_memories table created by migration
// 005_episodic_memory.sql. This constructor also creates the vec0 virtual table
// (episodic_memories_vec) if it does not already exist; sqlite_vec.Auto() must
// therefore have been called before opening the DB connection.
func NewSQLiteStore(
	db *sql.DB,
	embed func(ctx context.Context, text string) ([]float32, error),
) (*SQLiteStore, error) {
	dims := 768
	if v := os.Getenv("EMBEDDING_DIMENSIONS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			dims = n
		}
	}

	// Create the vec0 virtual table now that sqlite-vec is loaded.
	// The dimension string is built dynamically so it respects EMBEDDING_DIMENSIONS.
	ddl := fmt.Sprintf(`CREATE VIRTUAL TABLE IF NOT EXISTS episodic_memories_vec USING vec0(
		memory_id TEXT PRIMARY KEY,
		embedding float[%d]
	)`, dims)
	if _, err := db.Exec(ddl); err != nil {
		return nil, fmt.Errorf("episodic: create vec0 table: %w", err)
	}

	return &SQLiteStore{db: db, embed: embed, dims: dims}, nil
}

// Save persists a memory and its embedding vector in a single transaction.
// If m.ID is empty, a new UUID is generated.
func (s *SQLiteStore) Save(ctx context.Context, m Memory, embedding []float32) error {
	if m.ID == "" {
		m.ID = uuid.NewString()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("episodic save: begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	_, err = tx.ExecContext(ctx, `
		INSERT INTO episodic_memories
			(id, user_id, channel, session_id, content, source, importance, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ID, m.UserID, m.Channel, m.SessionID,
		m.Content, m.Source, m.Importance, m.CreatedAt.UTC(),
	)
	if err != nil {
		return fmt.Errorf("episodic save: insert memory: %w", err)
	}

	vecJSON, err := json.Marshal(embedding)
	if err != nil {
		return fmt.Errorf("episodic save: marshal embedding: %w", err)
	}
	_, err = tx.ExecContext(ctx,
		`INSERT INTO episodic_memories_vec (memory_id, embedding) VALUES (?, ?)`,
		m.ID, string(vecJSON),
	)
	if err != nil {
		return fmt.Errorf("episodic save: insert vector: %w", err)
	}
	return tx.Commit()
}

// Search returns up to k memories for the given user, ordered by semantic
// similarity to queryEmbedding.  Access stats are updated asynchronously.
func (s *SQLiteStore) Search(ctx context.Context, userID string, queryEmbedding []float32, k int) ([]Memory, error) {
	vecJSON, err := json.Marshal(queryEmbedding)
	if err != nil {
		return nil, fmt.Errorf("episodic search: marshal query vector: %w", err)
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT
			m.id, m.user_id, m.channel, m.session_id, m.content,
			m.source, m.importance, m.created_at, m.last_accessed_at, m.access_count
		FROM episodic_memories_vec v
		JOIN episodic_memories m ON m.id = v.memory_id
		WHERE v.embedding MATCH ?
		  AND k = ?
		  AND m.user_id = ?
		ORDER BY v.distance
		LIMIT ?`,
		string(vecJSON), k, userID, k,
	)
	if err != nil {
		return nil, fmt.Errorf("episodic search: query: %w", err)
	}
	defer rows.Close()

	var memories []Memory
	var ids []string
	for rows.Next() {
		var mem Memory
		var lastAccessed sql.NullTime
		if err := rows.Scan(
			&mem.ID, &mem.UserID, &mem.Channel, &mem.SessionID, &mem.Content,
			&mem.Source, &mem.Importance, &mem.CreatedAt, &lastAccessed, &mem.AccessCount,
		); err != nil {
			return nil, fmt.Errorf("episodic search: scan: %w", err)
		}
		if lastAccessed.Valid {
			mem.LastAccessedAt = &lastAccessed.Time
		}
		memories = append(memories, mem)
		ids = append(ids, mem.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("episodic search: iterate: %w", err)
	}

	// Update access stats asynchronously.
	if len(ids) > 0 {
		go func() {
			placeholders := strings.Repeat("?,", len(ids))
			placeholders = strings.TrimRight(placeholders, ",")
			args := make([]any, 0, len(ids)+1)
			for _, id := range ids {
				args = append(args, id)
			}
			s.db.Exec( //nolint:errcheck
				`UPDATE episodic_memories
				 SET last_accessed_at = datetime('now'), access_count = access_count + 1
				 WHERE id IN (`+placeholders+`)`,
				args...,
			)
		}()
	}

	return memories, nil
}

// SearchByText embeds the query text and delegates to Search.
func (s *SQLiteStore) SearchByText(ctx context.Context, userID, query string, k int) ([]Memory, error) {
	if s.embed == nil {
		return nil, fmt.Errorf("episodic: embed function not configured")
	}
	vec, err := s.embed(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("episodic search by text: embed: %w", err)
	}
	return s.Search(ctx, userID, vec, k)
}

// Recent returns up to k memories for the given user, ordered by creation time
// (most recent first).
func (s *SQLiteStore) Recent(ctx context.Context, userID string, k int) ([]Memory, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, user_id, channel, session_id, content, source,
		       importance, created_at, last_accessed_at, access_count
		FROM episodic_memories
		WHERE user_id = ?
		ORDER BY created_at DESC
		LIMIT ?`, userID, k)
	if err != nil {
		return nil, fmt.Errorf("episodic recent: %w", err)
	}
	defer rows.Close()

	var memories []Memory
	for rows.Next() {
		var mem Memory
		var lastAccessed sql.NullTime
		if err := rows.Scan(
			&mem.ID, &mem.UserID, &mem.Channel, &mem.SessionID, &mem.Content,
			&mem.Source, &mem.Importance, &mem.CreatedAt, &lastAccessed, &mem.AccessCount,
		); err != nil {
			return nil, fmt.Errorf("episodic recent: scan: %w", err)
		}
		if lastAccessed.Valid {
			mem.LastAccessedAt = &lastAccessed.Time
		}
		memories = append(memories, mem)
	}
	return memories, rows.Err()
}

// Delete removes a memory and its vector from both tables in a transaction.
func (s *SQLiteStore) Delete(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("episodic delete: begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM episodic_memories_vec WHERE memory_id = ?`, id); err != nil {
		return fmt.Errorf("episodic delete: vec: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM episodic_memories WHERE id = ?`, id); err != nil {
		return fmt.Errorf("episodic delete: memory: %w", err)
	}
	return tx.Commit()
}

// Prune removes memories for userID that have never been accessed and are older
// than maxAge.
func (s *SQLiteStore) Prune(ctx context.Context, userID string, maxAge time.Duration) error {
	cutoff := time.Now().UTC().Add(-maxAge)
	rows, err := s.db.QueryContext(ctx,
		`SELECT id FROM episodic_memories
		 WHERE user_id = ? AND access_count = 0 AND created_at < ?`,
		userID, cutoff)
	if err != nil {
		return fmt.Errorf("episodic prune: query: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return fmt.Errorf("episodic prune: scan: %w", err)
		}
		ids = append(ids, id)
	}
	rows.Close()

	for _, id := range ids {
		if err := s.Delete(ctx, id); err != nil {
			return fmt.Errorf("episodic prune: delete %s: %w", id, err)
		}
	}
	return nil
}

// SaveText embeds the memory's Content using the store's configured embed func,
// then calls Save. Convenience method for callers that don't hold embeddings.
func (s *SQLiteStore) SaveText(ctx context.Context, m Memory) error {
	if s.embed == nil {
		return fmt.Errorf("episodic: embed function not configured")
	}
	vec, err := s.embed(ctx, m.Content)
	if err != nil {
		return fmt.Errorf("episodic save text: embed: %w", err)
	}
	return s.Save(ctx, m, vec)
}

// compile-time interface check
var _ Store = (*SQLiteStore)(nil)
