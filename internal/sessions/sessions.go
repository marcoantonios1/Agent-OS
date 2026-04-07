// Package sessions defines the session data model and the SessionStore interface.
// Agents must only reference SessionStore; they must not depend on any concrete implementation.
package sessions

import (
	"time"

	"github.com/marcoantonios1/Agent-OS/internal/types"
)

// Session holds the full state of a single user conversation.
type Session struct {
	// ID is the unique session identifier.
	ID string
	// UserID is the identifier of the user owning this session.
	UserID string
	// ChannelID is the channel the session originated on.
	ChannelID types.ChannelID
	// History is the ordered list of conversation turns.
	History []types.ConversationTurn
	// Metadata holds arbitrary session-scoped key/value pairs.
	Metadata map[string]string
	// CreatedAt is when the session was first created.
	CreatedAt time.Time
	// UpdatedAt is when the session was last modified.
	UpdatedAt time.Time
}

// SessionStore is the persistence interface for sessions.
// All agents interact with sessions exclusively through this interface so that
// the backing store (in-memory, Redis, SQL, …) can be swapped without touching
// agent code.
type SessionStore interface {
	// Get returns the session for the given ID, or an error if it does not exist
	// or has expired.
	Get(sessionID string) (*Session, error)
	// Save creates or replaces the session. Implementations must update UpdatedAt.
	Save(session *Session) error
	// Delete removes the session. It is not an error to delete a session that
	// does not exist.
	Delete(sessionID string) error
	// AppendTurn appends a single conversation turn to an existing session.
	AppendTurn(sessionID, role, content string) error
}
