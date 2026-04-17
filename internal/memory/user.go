package memory

import (
	"fmt"
	"sync"
	"time"

	"github.com/marcoantonios1/Agent-OS/internal/sessions"
)

// UserStore is an in-memory implementation of sessions.UserStore.
// Profiles are never evicted — they persist for the lifetime of the process.
// Safe for concurrent use via a read/write mutex.
type UserStore struct {
	mu       sync.RWMutex
	profiles map[string]*sessions.UserProfile
}

// NewUserStore returns an empty in-memory UserStore.
func NewUserStore() *UserStore {
	return &UserStore{profiles: make(map[string]*sessions.UserProfile)}
}

// GetUser returns a copy of the profile for userID.
// Returns sessions.ErrUserNotFound if no profile has been saved yet.
func (s *UserStore) GetUser(userID string) (*sessions.UserProfile, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.profiles[userID]
	if !ok {
		return nil, fmt.Errorf("%w: %s", sessions.ErrUserNotFound, userID)
	}
	cp := cloneProfile(p)
	return &cp, nil
}

// SaveUser stores a copy of profile, setting UpdatedAt to now.
// Returns an error if UserID is empty.
func (s *UserStore) SaveUser(profile *sessions.UserProfile) error {
	if profile.UserID == "" {
		return fmt.Errorf("user profile must have a non-empty UserID")
	}
	cp := cloneProfile(profile)
	cp.UpdatedAt = time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.profiles[profile.UserID] = &cp
	return nil
}

// cloneProfile returns a deep copy so external callers cannot mutate stored state.
func cloneProfile(p *sessions.UserProfile) sessions.UserProfile {
	cp := *p
	if p.Preferences != nil {
		cp.Preferences = make(map[string]string, len(p.Preferences))
		for k, v := range p.Preferences {
			cp.Preferences[k] = v
		}
	}
	if p.RecurringContacts != nil {
		cp.RecurringContacts = make([]sessions.Contact, len(p.RecurringContacts))
		copy(cp.RecurringContacts, p.RecurringContacts)
	}
	return cp
}
