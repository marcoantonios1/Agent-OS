package sessions

import "time"

// UserProfile stores long-lived information about a user that persists across
// sessions. It is keyed by UserID, not SessionID, so the same profile is
// available regardless of which channel or conversation thread the user is in.
type UserProfile struct {
	UserID             string
	Name               string
	Preferences        map[string]string // e.g. "tone": "concise", "timezone": "UTC+3"
	RecurringContacts  []Contact
	CommunicationStyle string
	UpdatedAt          time.Time
}

// Contact is a frequently-contacted person stored in the user's profile.
type Contact struct {
	Name  string
	Email string
	Notes string
}

// UserStore persists user profiles independently of session lifetime.
// Implementations must be safe for concurrent use.
type UserStore interface {
	// GetUser returns the profile for userID.
	// Returns an error wrapping ErrUserNotFound if no profile exists yet.
	GetUser(userID string) (*UserProfile, error)

	// SaveUser creates or replaces the profile. UserID must be non-empty.
	// Implementations set UpdatedAt automatically.
	SaveUser(profile *UserProfile) error
}
