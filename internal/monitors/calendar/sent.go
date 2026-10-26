package calendarmonitor

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
)

// SentStore records which reminder slots have already been delivered, so each
// slot fires at most once even across restarts.
type SentStore interface {
	IsSent(ctx context.Context, userID, key string) (bool, error)
	MarkSent(ctx context.Context, userID, key string) error
}

// SQLiteSentStore implements SentStore on the calendar_reminders_sent table.
type SQLiteSentStore struct {
	db *sql.DB
}

// NewSQLiteSentStore returns a SentStore backed by db. The table is created by
// migration 007_calendar_reminders_sent.sql.
func NewSQLiteSentStore(db *sql.DB) *SQLiteSentStore {
	return &SQLiteSentStore{db: db}
}

func (s *SQLiteSentStore) IsSent(ctx context.Context, userID, key string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(1) FROM calendar_reminders_sent WHERE user_id = ? AND reminder_key = ?`,
		userID, key).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("calendar sent: lookup: %w", err)
	}
	return n > 0, nil
}

func (s *SQLiteSentStore) MarkSent(ctx context.Context, userID, key string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO calendar_reminders_sent (user_id, reminder_key) VALUES (?, ?)`,
		userID, key)
	if err != nil {
		return fmt.Errorf("calendar sent: mark: %w", err)
	}
	return nil
}

// MemorySentStore is an in-process SentStore. State is lost on restart.
type MemorySentStore struct {
	mu   sync.Mutex
	sent map[string]bool
}

// NewMemorySentStore returns an empty in-memory SentStore.
func NewMemorySentStore() *MemorySentStore {
	return &MemorySentStore{sent: make(map[string]bool)}
}

func (s *MemorySentStore) IsSent(_ context.Context, userID, key string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sent[userID+"\x00"+key], nil
}

func (s *MemorySentStore) MarkSent(_ context.Context, userID, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent[userID+"\x00"+key] = true
	return nil
}
