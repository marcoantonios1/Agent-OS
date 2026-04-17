package memory

import (
	"errors"
	"sync"
	"testing"

	"github.com/marcoantonios1/Agent-OS/internal/sessions"
)

func TestUserStore_SaveAndGet(t *testing.T) {
	s := NewUserStore()

	profile := &sessions.UserProfile{
		UserID:             "user1",
		Name:               "Alice",
		CommunicationStyle: "concise",
		Preferences:        map[string]string{"tone": "formal", "sign_off": "Regards"},
		RecurringContacts: []sessions.Contact{
			{Name: "Bob", Email: "bob@example.com", Notes: "colleague"},
		},
	}

	if err := s.SaveUser(profile); err != nil {
		t.Fatalf("SaveUser: %v", err)
	}

	got, err := s.GetUser("user1")
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if got.Name != "Alice" {
		t.Errorf("Name: got %q, want %q", got.Name, "Alice")
	}
	if got.Preferences["sign_off"] != "Regards" {
		t.Errorf("Preferences[sign_off]: got %q", got.Preferences["sign_off"])
	}
	if len(got.RecurringContacts) != 1 || got.RecurringContacts[0].Email != "bob@example.com" {
		t.Errorf("RecurringContacts: %+v", got.RecurringContacts)
	}
}

func TestUserStore_NotFound(t *testing.T) {
	s := NewUserStore()
	_, err := s.GetUser("nobody")
	if !errors.Is(err, sessions.ErrUserNotFound) {
		t.Errorf("expected ErrUserNotFound, got %v", err)
	}
}

func TestUserStore_EmptyUserID_Rejected(t *testing.T) {
	s := NewUserStore()
	err := s.SaveUser(&sessions.UserProfile{UserID: ""})
	if err == nil {
		t.Error("expected error for empty UserID")
	}
}

func TestUserStore_ProfileSurvivesAcrossSessions(t *testing.T) {
	// Simulate saving a profile from one session and reading it from another
	// (different session IDs, same user ID).
	s := NewUserStore()

	_ = s.SaveUser(&sessions.UserProfile{
		UserID: "user42",
		Name:   "Marco",
	})

	// Simulate second "session" — fresh context, same UserID.
	got, err := s.GetUser("user42")
	if err != nil {
		t.Fatalf("GetUser in second session: %v", err)
	}
	if got.Name != "Marco" {
		t.Errorf("profile not persisted across sessions: got name %q", got.Name)
	}
}

func TestUserStore_ReturnsCopy_NoExternalMutation(t *testing.T) {
	s := NewUserStore()
	_ = s.SaveUser(&sessions.UserProfile{
		UserID:      "user1",
		Preferences: map[string]string{"tone": "formal"},
	})

	got, _ := s.GetUser("user1")
	got.Preferences["tone"] = "MUTATED"

	// A second read should still see the original value.
	got2, _ := s.GetUser("user1")
	if got2.Preferences["tone"] != "formal" {
		t.Errorf("stored profile was mutated via returned pointer: got %q", got2.Preferences["tone"])
	}
}

func TestUserStore_UpdatedAt_SetOnSave(t *testing.T) {
	s := NewUserStore()
	_ = s.SaveUser(&sessions.UserProfile{UserID: "u1"})
	got, _ := s.GetUser("u1")
	if got.UpdatedAt.IsZero() {
		t.Error("UpdatedAt should be set by SaveUser")
	}
}

func TestUserStore_ConcurrentReadWrite(t *testing.T) {
	s := NewUserStore()
	_ = s.SaveUser(&sessions.UserProfile{UserID: "shared", Name: "initial"})

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			s.GetUser("shared") //nolint:errcheck
		}()
		go func(n int) {
			defer wg.Done()
			s.SaveUser(&sessions.UserProfile{ //nolint:errcheck
				UserID: "shared",
				Name:   "writer",
			})
		}(i)
	}
	wg.Wait()
	// Just checking that no race detector failures occurred.
}
