// Package approval implements a server-side gate for sensitive agent actions.
//
// Flow:
//  1. A tool that requires approval calls Pend(sessionID, actionID, description)
//     and returns a JSON pending_approval response to the LLM.
//  2. The LLM informs the user and waits for explicit confirmation.
//  3. The router detects a confirmation message, calls Grant(sessionID, actionID),
//     and re-runs the agent turn.
//  4. The tool calls Approved / Consume and executes the action.
//
// Approval records expire 5 minutes after being granted. Pending records that
// are never confirmed expire after 30 minutes to prevent memory growth.
package approval

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"
)

const (
	approvalTTL = 5 * time.Minute
	pendingTTL  = 30 * time.Minute
)

// contextKey is the unexported key used to attach a session ID to a context.
type contextKey struct{}

// WithSessionID attaches sessionID to ctx so tools can retrieve it.
func WithSessionID(ctx context.Context, sessionID string) context.Context {
	return context.WithValue(ctx, contextKey{}, sessionID)
}

// SessionIDFromContext retrieves the session ID attached by WithSessionID.
// Returns "" if no session ID is present.
func SessionIDFromContext(ctx context.Context) string {
	v, _ := ctx.Value(contextKey{}).(string)
	return v
}

// ActionID generates a short, deterministic identifier from a set of string
// components. The same components always produce the same ID, so a tool that
// sees its own pending response can derive the same ID on retry.
func ActionID(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0}) // null-byte separator
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// Record holds the state of a single action awaiting or granted approval.
type Record struct {
	ActionID    string
	SessionID   string
	Description string
	CreatedAt   time.Time
	GrantedAt   *time.Time // nil while pending
}

func (r *Record) isPending() bool { return r.GrantedAt == nil }

func (r *Record) isExpired() bool {
	if r.GrantedAt == nil {
		return time.Since(r.CreatedAt) > pendingTTL
	}
	return time.Since(*r.GrantedAt) > approvalTTL
}

// Store manages pending and approved actions.
type Store interface {
	// Pend registers a pending action for sessionID. Idempotent if already pending.
	Pend(sessionID, actionID, description string)
	// Grant approves a pending action. Returns false if not found or expired.
	Grant(sessionID, actionID string) bool
	// Approved returns true if an approved, non-expired record exists.
	Approved(sessionID, actionID string) bool
	// Consume atomically removes an approved record and returns true if found.
	Consume(sessionID, actionID string) bool
	// ListPending returns all pending (not yet granted) actions for a session.
	ListPending(sessionID string) []*Record
}

// ── in-memory implementation ──────────────────────────────────────────────────

type memoryStore struct {
	mu      sync.Mutex
	records map[string]*Record // key: sessionID + ":" + actionID
}

// NewMemoryStore returns an in-memory Store suitable for development and tests.
func NewMemoryStore() Store {
	return &memoryStore{records: make(map[string]*Record)}
}

func storeKey(sessionID, actionID string) string { return sessionID + ":" + actionID }

func (s *memoryStore) Pend(sessionID, actionID, description string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := storeKey(sessionID, actionID)
	if _, exists := s.records[k]; exists {
		return // already registered — idempotent
	}
	s.records[k] = &Record{
		ActionID:    actionID,
		SessionID:   sessionID,
		Description: description,
		CreatedAt:   time.Now(),
	}
}

func (s *memoryStore) Grant(sessionID, actionID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.records[storeKey(sessionID, actionID)]
	if !ok || r.isExpired() {
		return false
	}
	now := time.Now()
	r.GrantedAt = &now
	return true
}

func (s *memoryStore) Approved(sessionID, actionID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := storeKey(sessionID, actionID)
	r, ok := s.records[k]
	if !ok {
		return false
	}
	if r.isExpired() {
		delete(s.records, k)
		return false
	}
	return r.GrantedAt != nil
}

func (s *memoryStore) Consume(sessionID, actionID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := storeKey(sessionID, actionID)
	r, ok := s.records[k]
	if !ok || r.GrantedAt == nil || r.isExpired() {
		return false
	}
	delete(s.records, k)
	return true
}

func (s *memoryStore) ListPending(sessionID string) []*Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	var result []*Record
	for _, r := range s.records {
		if r.SessionID == sessionID && r.isPending() && !r.isExpired() {
			cp := *r
			result = append(result, &cp)
		}
	}
	return result
}
