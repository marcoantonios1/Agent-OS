package sessions

import (
	"errors"
	"time"

	"github.com/marcoantonios1/Agent-OS/internal/types"
)

// ErrReminderNotFound is returned by ReminderStore.Get when no reminder exists.
var ErrReminderNotFound = errors.New("reminder not found")

// Reminder represents a scheduled follow-up message for a user.
type Reminder struct {
	ID          string
	UserID      string
	SessionID   string
	ChannelID   types.ChannelID
	Message     string
	FireAt      time.Time
	CreatedAt   time.Time
	AgentPrompt string // if non-empty, routed through the Comms Agent when firing
}

// ReminderStore persists and retrieves reminders.
// Implementations must be safe for concurrent use.
type ReminderStore interface {
	// Save creates or replaces a reminder. ID must be non-empty.
	Save(r *Reminder) error

	// Get returns the reminder with the given ID.
	// Returns ErrReminderNotFound if it does not exist.
	Get(id string) (*Reminder, error)

	// Delete removes a reminder by ID. Not an error if it doesn't exist.
	Delete(id string) error

	// ListForUser returns all pending reminders for the given userID,
	// ordered by FireAt ascending.
	ListForUser(userID string) ([]*Reminder, error)

	// Due returns all reminders whose FireAt is on or before the given time,
	// then removes them from the store atomically.
	Due(now time.Time) ([]*Reminder, error)
}
