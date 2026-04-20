package memory

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/marcoantonios1/Agent-OS/internal/sessions"
)

// SQLiteUserStore implements sessions.UserStore backed by a SQLite database.
// Safe for concurrent use — database/sql manages the connection pool.
type SQLiteUserStore struct {
	db *sql.DB
}

// NewSQLiteUserStore returns a SQLiteUserStore using the provided *sql.DB.
// The database must already have the users table (see RunMigrations).
func NewSQLiteUserStore(db *sql.DB) *SQLiteUserStore {
	return &SQLiteUserStore{db: db}
}

// GetUser returns the profile for userID.
// Returns sessions.ErrUserNotFound if no profile exists.
func (s *SQLiteUserStore) GetUser(userID string) (*sessions.UserProfile, error) {
	row := s.db.QueryRow(
		`SELECT user_id, name, preferences, contacts, style, updated_at
		 FROM users WHERE user_id = ?`, userID)

	var (
		uid, name, style string
		prefsJSON        string
		contactsJSON     string
		updatedAt        time.Time
	)
	err := row.Scan(&uid, &name, &prefsJSON, &contactsJSON, &style, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: %s", sessions.ErrUserNotFound, userID)
	}
	if err != nil {
		return nil, fmt.Errorf("get user %s: %w", userID, err)
	}

	profile := &sessions.UserProfile{
		UserID:             uid,
		Name:               name,
		CommunicationStyle: style,
		UpdatedAt:          updatedAt,
	}
	if prefsJSON != "" && prefsJSON != "{}" {
		if err := json.Unmarshal([]byte(prefsJSON), &profile.Preferences); err != nil {
			return nil, fmt.Errorf("decode preferences for %s: %w", userID, err)
		}
	}
	if contactsJSON != "" && contactsJSON != "[]" {
		if err := json.Unmarshal([]byte(contactsJSON), &profile.RecurringContacts); err != nil {
			return nil, fmt.Errorf("decode contacts for %s: %w", userID, err)
		}
	}
	return profile, nil
}

// SaveUser creates or replaces the profile, setting UpdatedAt to now.
// Returns an error if UserID is empty.
func (s *SQLiteUserStore) SaveUser(profile *sessions.UserProfile) error {
	if profile.UserID == "" {
		return fmt.Errorf("user profile must have a non-empty UserID")
	}

	prefsJSON, err := json.Marshal(profile.Preferences)
	if err != nil {
		return fmt.Errorf("encode preferences: %w", err)
	}
	contactsJSON, err := json.Marshal(profile.RecurringContacts)
	if err != nil {
		return fmt.Errorf("encode contacts: %w", err)
	}

	_, err = s.db.Exec(`
		INSERT INTO users (user_id, name, preferences, contacts, style, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(user_id) DO UPDATE SET
			name        = excluded.name,
			preferences = excluded.preferences,
			contacts    = excluded.contacts,
			style       = excluded.style,
			updated_at  = excluded.updated_at`,
		profile.UserID,
		profile.Name,
		string(prefsJSON),
		string(contactsJSON),
		profile.CommunicationStyle,
		time.Now().UTC(),
	)
	if err != nil {
		return fmt.Errorf("save user %s: %w", profile.UserID, err)
	}
	return nil
}

// compile-time interface check
var _ sessions.UserStore = (*SQLiteUserStore)(nil)
