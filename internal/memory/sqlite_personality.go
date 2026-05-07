package memory

import (
	"database/sql"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/marcoantonios1/Agent-OS/internal/sessions"
)

// ── SQLitePersonalityStore ────────────────────────────────────────────────────

// SQLitePersonalityStore implements sessions.PersonalityStore backed by SQLite.
// Safe for concurrent use — database/sql manages the connection pool.
type SQLitePersonalityStore struct {
	db *sql.DB
}

// NewSQLitePersonalityStore returns a store using the provided *sql.DB.
// The database must already have the personality_signals table (see RunMigrations).
func NewSQLitePersonalityStore(db *sql.DB) *SQLitePersonalityStore {
	return &SQLitePersonalityStore{db: db}
}

// GetPersonality returns all signals for userID, or an empty profile when none exist.
func (s *SQLitePersonalityStore) GetPersonality(userID string) (*sessions.PersonalityProfile, error) {
	rows, err := s.db.Query(
		`SELECT key, value, confidence, count, last_seen
		 FROM personality_signals WHERE user_id = ?
		 ORDER BY key`, userID)
	if err != nil {
		return nil, fmt.Errorf("get personality %s: %w", userID, err)
	}
	defer rows.Close()

	profile := &sessions.PersonalityProfile{UserID: userID}
	for rows.Next() {
		var sig sessions.PersonalitySignal
		if err := rows.Scan(&sig.Key, &sig.Value, &sig.Confidence, &sig.Count, &sig.LastSeen); err != nil {
			return nil, fmt.Errorf("scan personality signal: %w", err)
		}
		profile.Signals = append(profile.Signals, sig)
		if sig.LastSeen.After(profile.UpdatedAt) {
			profile.UpdatedAt = sig.LastSeen
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate personality signals: %w", err)
	}
	return profile, nil
}

// SavePersonality replaces all signals for profile.UserID in a single transaction.
func (s *SQLitePersonalityStore) SavePersonality(profile *sessions.PersonalityProfile) error {
	if profile.UserID == "" {
		return fmt.Errorf("personality profile must have a non-empty UserID")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("save personality %s: begin tx: %w", profile.UserID, err)
	}
	defer tx.Rollback() //nolint:errcheck

	if _, err := tx.Exec(`DELETE FROM personality_signals WHERE user_id = ?`, profile.UserID); err != nil {
		return fmt.Errorf("save personality %s: delete: %w", profile.UserID, err)
	}
	for _, sig := range profile.Signals {
		if _, err := tx.Exec(
			`INSERT INTO personality_signals (user_id, key, value, confidence, count, last_seen)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			profile.UserID, sig.Key, sig.Value, sig.Confidence, sig.Count, sig.LastSeen.UTC(),
		); err != nil {
			return fmt.Errorf("save personality %s: insert signal %s: %w", profile.UserID, sig.Key, err)
		}
	}
	return tx.Commit()
}

// UpsertSignal increments the count for an existing signal or inserts a new one.
// Confidence is recalculated as min(count/10, 1.0) after the increment.
func (s *SQLitePersonalityStore) UpsertSignal(userID string, signal sessions.PersonalitySignal) error {
	if userID == "" {
		return fmt.Errorf("upsert signal: userID must not be empty")
	}
	now := time.Now().UTC()
	_, err := s.db.Exec(`
		INSERT INTO personality_signals (user_id, key, value, confidence, count, last_seen)
		VALUES (?, ?, ?, ?, 1, ?)
		ON CONFLICT(user_id, key) DO UPDATE SET
			value      = excluded.value,
			count      = personality_signals.count + 1,
			confidence = MIN(CAST(personality_signals.count + 1 AS REAL) / 10.0, 1.0),
			last_seen  = excluded.last_seen`,
		userID, signal.Key, signal.Value, confidenceForCount(1), now,
	)
	if err != nil {
		return fmt.Errorf("upsert signal %s/%s: %w", userID, signal.Key, err)
	}
	return nil
}

// confidenceForCount returns min(count/10, 1.0).
func confidenceForCount(count int) float64 {
	return math.Min(float64(count)/10.0, 1.0)
}

// compile-time interface check
var _ sessions.PersonalityStore = (*SQLitePersonalityStore)(nil)

// ── MemoryPersonalityStore ────────────────────────────────────────────────────

// MemoryPersonalityStore is a thread-safe in-memory implementation for tests.
type MemoryPersonalityStore struct {
	mu      sync.Mutex
	signals map[string]map[string]*sessions.PersonalitySignal // userID → key → signal
}

// NewPersonalityStore returns an empty in-memory PersonalityStore.
func NewPersonalityStore() *MemoryPersonalityStore {
	return &MemoryPersonalityStore{
		signals: make(map[string]map[string]*sessions.PersonalitySignal),
	}
}

func (s *MemoryPersonalityStore) GetPersonality(userID string) (*sessions.PersonalityProfile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	profile := &sessions.PersonalityProfile{UserID: userID}
	for _, sig := range s.signals[userID] {
		cp := *sig
		profile.Signals = append(profile.Signals, cp)
		if sig.LastSeen.After(profile.UpdatedAt) {
			profile.UpdatedAt = sig.LastSeen
		}
	}
	return profile, nil
}

func (s *MemoryPersonalityStore) SavePersonality(profile *sessions.PersonalityProfile) error {
	if profile.UserID == "" {
		return fmt.Errorf("personality profile must have a non-empty UserID")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	m := make(map[string]*sessions.PersonalitySignal, len(profile.Signals))
	for i := range profile.Signals {
		cp := profile.Signals[i]
		m[cp.Key] = &cp
	}
	s.signals[profile.UserID] = m
	return nil
}

func (s *MemoryPersonalityStore) UpsertSignal(userID string, signal sessions.PersonalitySignal) error {
	if userID == "" {
		return fmt.Errorf("upsert signal: userID must not be empty")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.signals[userID] == nil {
		s.signals[userID] = make(map[string]*sessions.PersonalitySignal)
	}
	existing, ok := s.signals[userID][signal.Key]
	if !ok {
		cp := signal
		cp.Count = 1
		cp.Confidence = confidenceForCount(1)
		cp.LastSeen = time.Now()
		s.signals[userID][signal.Key] = &cp
		return nil
	}
	existing.Value = signal.Value
	existing.Count++
	existing.Confidence = confidenceForCount(existing.Count)
	existing.LastSeen = time.Now()
	return nil
}

// compile-time interface check
var _ sessions.PersonalityStore = (*MemoryPersonalityStore)(nil)
