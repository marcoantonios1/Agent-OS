package memory

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/marcoantonios1/Agent-OS/internal/sessions"
)

// ReminderStore is an in-memory implementation of sessions.ReminderStore.
// Safe for concurrent use via a mutex.
type ReminderStore struct {
	mu    sync.Mutex
	items map[string]*sessions.Reminder
}

// NewReminderStore returns an empty in-memory ReminderStore.
func NewReminderStore() *ReminderStore {
	return &ReminderStore{items: make(map[string]*sessions.Reminder)}
}

func (s *ReminderStore) Save(r *sessions.Reminder) error {
	if r.ID == "" {
		return fmt.Errorf("reminder must have a non-empty ID")
	}
	cp := *r
	s.mu.Lock()
	s.items[r.ID] = &cp
	s.mu.Unlock()
	return nil
}

func (s *ReminderStore) Get(id string) (*sessions.Reminder, error) {
	s.mu.Lock()
	r, ok := s.items[id]
	s.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("%w: %s", sessions.ErrReminderNotFound, id)
	}
	cp := *r
	return &cp, nil
}

func (s *ReminderStore) Delete(id string) error {
	s.mu.Lock()
	delete(s.items, id)
	s.mu.Unlock()
	return nil
}

func (s *ReminderStore) ListForUser(userID string) ([]*sessions.Reminder, error) {
	s.mu.Lock()
	var out []*sessions.Reminder
	for _, r := range s.items {
		if r.UserID == userID {
			cp := *r
			out = append(out, &cp)
		}
	}
	s.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].FireAt.Before(out[j].FireAt) })
	return out, nil
}

func (s *ReminderStore) Due(now time.Time) ([]*sessions.Reminder, error) {
	s.mu.Lock()
	var due []*sessions.Reminder
	for id, r := range s.items {
		if !r.FireAt.After(now) {
			cp := *r
			due = append(due, &cp)
			delete(s.items, id)
		}
	}
	s.mu.Unlock()
	return due, nil
}

// compile-time interface check
var _ sessions.ReminderStore = (*ReminderStore)(nil)
