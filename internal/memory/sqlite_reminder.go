package memory

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/marcoantonios1/Agent-OS/internal/sessions"
	"github.com/marcoantonios1/Agent-OS/internal/types"
)

// SQLiteReminderStore implements sessions.ReminderStore backed by SQLite.
// Safe for concurrent use — database/sql manages the connection pool.
type SQLiteReminderStore struct {
	db *sql.DB
}

// NewSQLiteReminderStore returns a SQLiteReminderStore using the provided *sql.DB.
// The database must already have the reminders table (see RunMigrations).
func NewSQLiteReminderStore(db *sql.DB) *SQLiteReminderStore {
	return &SQLiteReminderStore{db: db}
}

func (s *SQLiteReminderStore) Save(r *sessions.Reminder) error {
	if r.ID == "" {
		return fmt.Errorf("reminder must have a non-empty ID")
	}
	createdAt := r.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	_, err := s.db.Exec(`
		INSERT INTO reminders (id, session_id, user_id, channel_id, message, fire_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			session_id = excluded.session_id,
			user_id    = excluded.user_id,
			channel_id = excluded.channel_id,
			message    = excluded.message,
			fire_at    = excluded.fire_at,
			created_at = excluded.created_at`,
		r.ID,
		r.SessionID,
		r.UserID,
		string(r.ChannelID),
		r.Message,
		r.FireAt.UTC(),
		createdAt,
	)
	if err != nil {
		return fmt.Errorf("save reminder %s: %w", r.ID, err)
	}
	return nil
}

func (s *SQLiteReminderStore) Get(id string) (*sessions.Reminder, error) {
	row := s.db.QueryRow(
		`SELECT id, session_id, user_id, channel_id, message, fire_at, created_at
		 FROM reminders WHERE id = ?`, id)
	r, err := scanReminder(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: %s", sessions.ErrReminderNotFound, id)
	}
	if err != nil {
		return nil, fmt.Errorf("get reminder %s: %w", id, err)
	}
	return r, nil
}

func (s *SQLiteReminderStore) Delete(id string) error {
	_, err := s.db.Exec(`DELETE FROM reminders WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete reminder %s: %w", id, err)
	}
	return nil
}

func (s *SQLiteReminderStore) ListForUser(userID string) ([]*sessions.Reminder, error) {
	rows, err := s.db.Query(
		`SELECT id, session_id, user_id, channel_id, message, fire_at, created_at
		 FROM reminders WHERE user_id = ? ORDER BY fire_at ASC`, userID)
	if err != nil {
		return nil, fmt.Errorf("list reminders for %s: %w", userID, err)
	}
	defer rows.Close()
	return collectReminders(rows)
}

// Due returns all reminders whose fire_at <= now and removes them atomically.
func (s *SQLiteReminderStore) Due(now time.Time) ([]*sessions.Reminder, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("due reminders: begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	rows, err := tx.Query(
		`SELECT id, session_id, user_id, channel_id, message, fire_at, created_at
		 FROM reminders WHERE fire_at <= ?`, now.UTC())
	if err != nil {
		return nil, fmt.Errorf("due reminders: query: %w", err)
	}
	due, err := collectReminders(rows)
	rows.Close()
	if err != nil {
		return nil, fmt.Errorf("due reminders: scan: %w", err)
	}

	for _, r := range due {
		if _, err := tx.Exec(`DELETE FROM reminders WHERE id = ?`, r.ID); err != nil {
			return nil, fmt.Errorf("due reminders: delete %s: %w", r.ID, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("due reminders: commit: %w", err)
	}

	sort.Slice(due, func(i, j int) bool { return due[i].FireAt.Before(due[j].FireAt) })
	return due, nil
}

// ── helpers ───────────────────────────────────────────────────────────────────

type scannable interface {
	Scan(dest ...any) error
}

func scanReminder(row scannable) (*sessions.Reminder, error) {
	var (
		id, sessionID, userID, channelID, message string
		fireAt, createdAt                         time.Time
	)
	if err := row.Scan(&id, &sessionID, &userID, &channelID, &message, &fireAt, &createdAt); err != nil {
		return nil, err
	}
	return &sessions.Reminder{
		ID:        id,
		SessionID: sessionID,
		UserID:    userID,
		ChannelID: types.ChannelID(channelID),
		Message:   message,
		FireAt:    fireAt,
		CreatedAt: createdAt,
	}, nil
}

func collectReminders(rows *sql.Rows) ([]*sessions.Reminder, error) {
	var out []*sessions.Reminder
	for rows.Next() {
		r, err := scanReminder(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// compile-time interface check
var _ sessions.ReminderStore = (*SQLiteReminderStore)(nil)
