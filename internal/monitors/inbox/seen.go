package inbox

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
)

// SeenStore records which emails the monitor has already triaged. Lookups are
// exact by email ID, so an email is never evaluated or notified about twice.
type SeenStore interface {
	IsSeen(ctx context.Context, userID, emailID string) (bool, error)
	MarkSeen(ctx context.Context, userID, emailID string) error
}

// SQLiteSeenStore implements SeenStore on the inbox_seen table.
type SQLiteSeenStore struct {
	db *sql.DB
}

// NewSQLiteSeenStore returns a SeenStore backed by db. The inbox_seen table is
// created by migration 006_inbox_seen.sql.
func NewSQLiteSeenStore(db *sql.DB) *SQLiteSeenStore {
	return &SQLiteSeenStore{db: db}
}

func (s *SQLiteSeenStore) IsSeen(ctx context.Context, userID, emailID string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(1) FROM inbox_seen WHERE user_id = ? AND email_id = ?`,
		userID, emailID).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("inbox seen: lookup: %w", err)
	}
	return n > 0, nil
}

func (s *SQLiteSeenStore) MarkSeen(ctx context.Context, userID, emailID string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO inbox_seen (user_id, email_id) VALUES (?, ?)`,
		userID, emailID)
	if err != nil {
		return fmt.Errorf("inbox seen: mark: %w", err)
	}
	return nil
}

// MemorySeenStore is an in-process SeenStore. State is lost on restart.
type MemorySeenStore struct {
	mu   sync.Mutex
	seen map[string]bool
}

// NewMemorySeenStore returns an empty in-memory SeenStore.
func NewMemorySeenStore() *MemorySeenStore {
	return &MemorySeenStore{seen: make(map[string]bool)}
}

func (s *MemorySeenStore) IsSeen(_ context.Context, userID, emailID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.seen[userID+"\x00"+emailID], nil
}

func (s *MemorySeenStore) MarkSeen(_ context.Context, userID, emailID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seen[userID+"\x00"+emailID] = true
	return nil
}
