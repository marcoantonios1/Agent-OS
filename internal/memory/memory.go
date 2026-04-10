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
	// Hold the read lock through the copy so no concurrent writer can mutate
	// the session struct between the existence check and the copy.
	s.mu.RLock()
	e, ok := s.sessions[sessionID]
	expired := ok && time.Now().After(e.expiresAt)
	var cp sessions.Session
	if ok && !expired {
		cp = *e.session
	}
	s.mu.RUnlock()

	if !ok || expired {
		if expired {
			// Lazy delete: re-check under write lock before deleting.
			s.mu.Lock()
			if e2, still := s.sessions[sessionID]; still && time.Now().After(e2.expiresAt) {
				delete(s.sessions, sessionID)
			}
			s.mu.Unlock()
		}
		return nil, ErrSessionNotFound
	}

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

// SetMetadata sets a single key/value pair on the session metadata map.
// If the session does not exist it is created with just the metadata entry
// (history-less), which allows agents to persist state even when called
// outside the router's normal session-creation flow.
func (s *Store) SetMetadata(sessionID, key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	e, ok := s.sessions[sessionID]
	if !ok || now.After(e.expiresAt) {
		// Upsert: create a minimal session so the metadata is not lost.
		sess := &sessions.Session{
			ID:        sessionID,
			Metadata:  map[string]string{key: value},
			CreatedAt: now,
			UpdatedAt: now,
		}
		s.sessions[sessionID] = &entry{session: sess, expiresAt: now.Add(s.ttl)}
		return nil
	}
	if e.session.Metadata == nil {
		e.session.Metadata = make(map[string]string)
	}
	e.session.Metadata[key] = value
	e.session.UpdatedAt = now
	e.expiresAt = now.Add(s.ttl)
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
