// Package memory provides an in-memory implementation of sessions.SessionStore.
// It is the default store for MVP. Swap it for a persistent backend by
// constructing a different SessionStore and injecting it into the app.
package memory

import (
	"errors"
	"sync"
	"time"

	"github.com/marcoantonios1/Agent-OS/internal/sessions"
	"github.com/marcoantonios1/Agent-OS/internal/types"
)

const defaultTTL = 30 * time.Minute

// ErrSessionNotFound is returned when the requested session does not exist or
// has expired.
var ErrSessionNotFound = errors.New("session not found")

// Store is a thread-safe, in-memory implementation of sessions.SessionStore
// with TTL-based expiry. Expired sessions are cleaned up lazily on access and
// proactively by a background goroutine started via NewStore.
type Store struct {
	mu       sync.RWMutex
	sessions map[string]*entry
	ttl      time.Duration
	stopOnce sync.Once
	stop     chan struct{}
}

type entry struct {
	session   *sessions.Session
	expiresAt time.Time
}

// Option is a functional option for Store.
type Option func(*Store)

// WithTTL overrides the default session TTL.
func WithTTL(ttl time.Duration) Option {
	return func(s *Store) { s.ttl = ttl }
}

// NewStore creates and starts an in-memory SessionStore. Call Close when the
// store is no longer needed to stop the background cleanup goroutine.
func NewStore(opts ...Option) *Store {
	s := &Store{
		sessions: make(map[string]*entry),
		ttl:      defaultTTL,
		stop:     make(chan struct{}),
	}
	for _, o := range opts {
		o(s)
	}
	go s.cleanupLoop()
	return s
}

// Close stops the background cleanup goroutine. Safe to call multiple times.
func (s *Store) Close() {
	s.stopOnce.Do(func() { close(s.stop) })
}

// Get returns the session for the given ID. Returns ErrSessionNotFound if the
// session does not exist or has expired.
func (s *Store) Get(sessionID string) (*sessions.Session, error) {
	s.mu.RLock()
	e, ok := s.sessions[sessionID]
	s.mu.RUnlock()

	if !ok || time.Now().After(e.expiresAt) {
		if ok {
			// Lazy delete of the expired entry.
			s.mu.Lock()
			delete(s.sessions, sessionID)
			s.mu.Unlock()
		}
		return nil, ErrSessionNotFound
	}

	// Return a shallow copy so callers cannot mutate store internals.
	cp := *e.session
	return &cp, nil
}

// Save stores or replaces a session, resetting its TTL. UpdatedAt is set to now.
func (s *Store) Save(session *sessions.Session) error {
	now := time.Now()
	session.UpdatedAt = now

	cp := *session
	s.mu.Lock()
	s.sessions[session.ID] = &entry{
		session:   &cp,
		expiresAt: now.Add(s.ttl),
	}
	s.mu.Unlock()
	return nil
}

// Delete removes the session with the given ID. It is a no-op if the session
// does not exist.
func (s *Store) Delete(sessionID string) error {
	s.mu.Lock()
	delete(s.sessions, sessionID)
	s.mu.Unlock()
	return nil
}

// AppendTurn appends a conversation turn to an existing session and resets its
// TTL. Returns ErrSessionNotFound if the session does not exist or has expired.
func (s *Store) AppendTurn(sessionID, role, content string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	e, ok := s.sessions[sessionID]
	if !ok || time.Now().After(e.expiresAt) {
		delete(s.sessions, sessionID)
		return ErrSessionNotFound
	}

	e.session.History = append(e.session.History, types.ConversationTurn{
		Role:    role,
		Content: content,
	})
	e.session.UpdatedAt = time.Now()
	e.expiresAt = time.Now().Add(s.ttl)
	return nil
}

// cleanupLoop periodically removes expired sessions from the map.
func (s *Store) cleanupLoop() {
	ticker := time.NewTicker(s.ttl / 2)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			s.evictExpired()
		case <-s.stop:
			return
		}
	}
}

func (s *Store) evictExpired() {
	now := time.Now()
	s.mu.Lock()
	for id, e := range s.sessions {
		if now.After(e.expiresAt) {
			delete(s.sessions, id)
		}
	}
	s.mu.Unlock()
}

// Compile-time assertion that *Store satisfies SessionStore.
var _ sessions.SessionStore = (*Store)(nil)
