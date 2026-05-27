//go:build cgo

package episodic_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	sqlite_vec "github.com/asg017/sqlite-vec-go-bindings/cgo"
	_ "github.com/mattn/go-sqlite3"
	"github.com/marcoantonios1/Agent-OS/internal/memory/episodic"
)

func TestMain(m *testing.M) {
	sqlite_vec.Auto()
	os.Exit(m.Run())
}

// openTestDB opens a fresh SQLite DB (CGO driver) with migrations applied.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "episodic_test.db")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	applyMigrations(t, db)
	return db
}

// applyMigrations creates the tables needed for episodic memory tests.
func applyMigrations(t *testing.T, db *sql.DB) {
	t.Helper()
	_, err := db.Exec(`
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
		CREATE VIRTUAL TABLE IF NOT EXISTS episodic_memories_vec USING vec0(
			memory_id TEXT PRIMARY KEY,
			embedding float[768]
		);
		CREATE INDEX IF NOT EXISTS idx_episodic_memories_user
			ON episodic_memories(user_id, created_at DESC);
		CREATE INDEX IF NOT EXISTS idx_episodic_memories_session
			ON episodic_memories(session_id);
	`)
	if err != nil {
		t.Fatalf("applyMigrations: %v", err)
	}
}

// dims768 returns a 768-dimensional float32 vector with the specified first few
// elements set and the rest zero.
func dims768(first ...float32) []float32 {
	v := make([]float32, 768)
	copy(v, first)
	return v
}

// fakeEmbedder returns a fixed embedding based on the input text.
// Close strings produce similar vectors; distant strings differ.
func fakeEmbedder(ctx context.Context, text string) ([]float32, error) {
	switch text {
	case "difficult meeting":
		return dims768(1.0), nil
	case "tense conversation":
		return dims768(0.9, 0.1), nil
	case "pizza for lunch":
		return dims768(0.0, 0.0, 1.0), nil
	default:
		return dims768(0.5), nil
	}
}

// ── Test 1: Save() persists memory and vector ─────────────────────────────────

func TestSave_PersistsMemoryAndVector(t *testing.T) {
	db := openTestDB(t)
	store, err := episodic.NewSQLiteStore(db, fakeEmbedder)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}

	mem := episodic.Memory{
		ID:         "mem-001",
		UserID:     "user-1",
		Channel:    "slack",
		SessionID:  "sess-abc",
		Content:    "Alice mentioned she is allergic to peanuts",
		Source:     "conversation",
		Importance: 0.8,
		CreatedAt:  time.Now().UTC().Truncate(time.Second),
	}
	embedding := dims768(0.5, 0.3, 0.2)

	if err := store.Save(context.Background(), mem, embedding); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Verify the memory row.
	var got episodic.Memory
	row := db.QueryRow(`SELECT id, user_id, channel, session_id, content, source, importance FROM episodic_memories WHERE id = ?`, mem.ID)
	if err := row.Scan(&got.ID, &got.UserID, &got.Channel, &got.SessionID, &got.Content, &got.Source, &got.Importance); err != nil {
		t.Fatalf("query episodic_memories: %v", err)
	}
	if got.ID != mem.ID {
		t.Errorf("ID: got %q, want %q", got.ID, mem.ID)
	}
	if got.Content != mem.Content {
		t.Errorf("Content: got %q, want %q", got.Content, mem.Content)
	}
	if got.Importance != mem.Importance {
		t.Errorf("Importance: got %f, want %f", got.Importance, mem.Importance)
	}

	// Verify the vector row exists.
	var vecID string
	if err := db.QueryRow(`SELECT memory_id FROM episodic_memories_vec WHERE memory_id = ?`, mem.ID).Scan(&vecID); err != nil {
		t.Fatalf("query episodic_memories_vec: %v", err)
	}
	if vecID != mem.ID {
		t.Errorf("vec memory_id: got %q, want %q", vecID, mem.ID)
	}
}

// ── Test 2: Search() returns semantically close memories ─────────────────────

func TestSearch_ReturnsSemanticallySimilarMemory(t *testing.T) {
	db := openTestDB(t)
	store, err := episodic.NewSQLiteStore(db, fakeEmbedder)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}

	ctx := context.Background()
	userID := "user-search"

	// Save a "difficult meeting" memory.
	meetingMem := episodic.Memory{
		UserID:    userID,
		Channel:   "web",
		SessionID: "sess-1",
		Content:   "difficult meeting with Alice",
		Source:    "conversation",
		Importance: 0.7,
		CreatedAt: time.Now().UTC(),
	}
	if err := store.Save(ctx, meetingMem, dims768(1.0)); err != nil {
		t.Fatalf("Save meeting: %v", err)
	}

	// Save a "pizza for lunch" memory.
	pizzaMem := episodic.Memory{
		UserID:    userID,
		Channel:   "web",
		SessionID: "sess-2",
		Content:   "pizza for lunch",
		Source:    "conversation",
		Importance: 0.3,
		CreatedAt: time.Now().UTC(),
	}
	if err := store.Save(ctx, pizzaMem, dims768(0.0, 0.0, 1.0)); err != nil {
		t.Fatalf("Save pizza: %v", err)
	}

	// Search with a query close to "difficult meeting".
	queryVec := dims768(0.95, 0.05)
	results, err := store.Search(ctx, userID, queryVec, 1)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Content != "difficult meeting with Alice" {
		t.Errorf("expected 'difficult meeting with Alice', got %q", results[0].Content)
	}
}

// ── Test 3: Prune() removes stale memories ───────────────────────────────────

func TestPrune_RemovesStaleMemories(t *testing.T) {
	db := openTestDB(t)
	store, err := episodic.NewSQLiteStore(db, fakeEmbedder)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}

	ctx := context.Background()
	userID := "user-prune"

	oldTime := time.Now().UTC().Add(-100 * 24 * time.Hour)

	// Two old memories (100 days old, never accessed).
	for i, content := range []string{"old memory 1", "old memory 2"} {
		id := "old-" + string(rune('0'+i))
		_, err := db.Exec(
			`INSERT INTO episodic_memories (id, user_id, channel, session_id, content, source, importance, created_at, access_count)
			 VALUES (?, ?, 'web', 'sess', ?, 'conv', 0.5, ?, 0)`,
			id, userID, content, oldTime,
		)
		if err != nil {
			t.Fatalf("insert old memory: %v", err)
		}
		// Also insert a zero vector so the row is self-consistent.
		emb := make([]float32, 768)
		if err := store.Save(ctx, episodic.Memory{
			ID: id + "-skip", // won't conflict; real insert is above
		}, emb); err != nil {
			// We already inserted directly; ignore failures from duplicate.
			_ = err
		}
		// Insert the vec row manually for the directly-inserted memory.
		_, _ = db.Exec(
			`INSERT INTO episodic_memories_vec (memory_id, embedding) VALUES (?, ?)`,
			id, string(make([]byte, 0)), // placeholder; vec row needed for Delete
		)
	}

	// One recent memory (now, never accessed).
	newMem := episodic.Memory{
		UserID:    userID,
		Channel:   "web",
		SessionID: "sess-new",
		Content:   "new memory",
		Source:    "conversation",
		Importance: 0.5,
		CreatedAt: time.Now().UTC(),
	}
	if err := store.Save(ctx, newMem, dims768(0.1)); err != nil {
		t.Fatalf("Save new memory: %v", err)
	}

	// Prune memories older than 90 days.
	if err := store.Prune(ctx, userID, 90*24*time.Hour); err != nil {
		t.Fatalf("Prune: %v", err)
	}

	// Only the new memory should remain.
	recent, err := store.Recent(ctx, userID, 10)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(recent) != 1 {
		t.Fatalf("expected 1 memory after prune, got %d", len(recent))
	}
	if recent[0].Content != "new memory" {
		t.Errorf("expected 'new memory', got %q", recent[0].Content)
	}
}
