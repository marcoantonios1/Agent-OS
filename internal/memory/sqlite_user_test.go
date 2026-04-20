package memory_test

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/marcoantonios1/Agent-OS/internal/memory"
	"github.com/marcoantonios1/Agent-OS/internal/sessions"
)

func newTestUserStore(t *testing.T) *memory.SQLiteUserStore {
	t.Helper()
	db, err := memory.OpenDB(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return memory.NewSQLiteUserStore(db)
}

func TestSQLiteUserStore_RoundTrip(t *testing.T) {
	store := newTestUserStore(t)

	profile := &sessions.UserProfile{
		UserID:             "user-1",
		Name:               "Alice",
		CommunicationStyle: "concise",
		Preferences:        map[string]string{"sign_off": "Alice", "tone": "formal"},
		RecurringContacts: []sessions.Contact{
			{Name: "Bob", Email: "bob@example.com", Notes: "colleague"},
		},
	}

	if err := store.SaveUser(profile); err != nil {
		t.Fatalf("SaveUser: %v", err)
	}

	got, err := store.GetUser("user-1")
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}

	if got.Name != "Alice" {
		t.Errorf("Name: got %q, want %q", got.Name, "Alice")
	}
	if got.CommunicationStyle != "concise" {
		t.Errorf("CommunicationStyle: got %q, want %q", got.CommunicationStyle, "concise")
	}
	if got.Preferences["sign_off"] != "Alice" {
		t.Errorf("Preferences[sign_off]: got %q, want %q", got.Preferences["sign_off"], "Alice")
	}
	if len(got.RecurringContacts) != 1 || got.RecurringContacts[0].Email != "bob@example.com" {
		t.Errorf("RecurringContacts: got %v", got.RecurringContacts)
	}
	if got.UpdatedAt.IsZero() {
		t.Error("UpdatedAt should be set by SaveUser")
	}
}

func TestSQLiteUserStore_NotFound(t *testing.T) {
	store := newTestUserStore(t)

	_, err := store.GetUser("nobody")
	if !errors.Is(err, sessions.ErrUserNotFound) {
		t.Errorf("expected ErrUserNotFound, got %v", err)
	}
}

func TestSQLiteUserStore_Upsert(t *testing.T) {
	store := newTestUserStore(t)

	p := &sessions.UserProfile{UserID: "u1", Name: "Original"}
	if err := store.SaveUser(p); err != nil {
		t.Fatalf("first save: %v", err)
	}

	p.Name = "Updated"
	p.CommunicationStyle = "verbose"
	if err := store.SaveUser(p); err != nil {
		t.Fatalf("second save: %v", err)
	}

	got, err := store.GetUser("u1")
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if got.Name != "Updated" {
		t.Errorf("Name after upsert: got %q, want %q", got.Name, "Updated")
	}
	if got.CommunicationStyle != "verbose" {
		t.Errorf("CommunicationStyle after upsert: got %q", got.CommunicationStyle)
	}
}

func TestSQLiteUserStore_EmptyUserID_Rejected(t *testing.T) {
	store := newTestUserStore(t)
	err := store.SaveUser(&sessions.UserProfile{Name: "NoID"})
	if err == nil {
		t.Error("expected error for empty UserID, got nil")
	}
}

func TestSQLiteUserStore_UpdatedAtSet(t *testing.T) {
	store := newTestUserStore(t)

	before := time.Now().Add(-time.Second)
	if err := store.SaveUser(&sessions.UserProfile{UserID: "u2", Name: "T"}); err != nil {
		t.Fatal(err)
	}

	got, err := store.GetUser("u2")
	if err != nil {
		t.Fatal(err)
	}
	if !got.UpdatedAt.After(before) {
		t.Errorf("UpdatedAt %v should be after %v", got.UpdatedAt, before)
	}
}

func TestSQLiteUserStore_NilPreferencesAndContacts(t *testing.T) {
	store := newTestUserStore(t)

	if err := store.SaveUser(&sessions.UserProfile{UserID: "u3", Name: "Minimal"}); err != nil {
		t.Fatalf("SaveUser: %v", err)
	}
	got, err := store.GetUser("u3")
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	// nil maps/slices should round-trip without error
	_ = got.Preferences
	_ = got.RecurringContacts
}

func TestSQLiteUserStore_PersistsAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "persist.db")

	// Write in one DB handle.
	db1, err := memory.OpenDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	s1 := memory.NewSQLiteUserStore(db1)
	if err := s1.SaveUser(&sessions.UserProfile{UserID: "u4", Name: "Persistent"}); err != nil {
		t.Fatal(err)
	}
	db1.Close()

	// Read back in a fresh DB handle.
	db2, err := memory.OpenDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	s2 := memory.NewSQLiteUserStore(db2)

	got, err := s2.GetUser("u4")
	if err != nil {
		t.Fatalf("GetUser after reopen: %v", err)
	}
	if got.Name != "Persistent" {
		t.Errorf("Name: got %q, want %q", got.Name, "Persistent")
	}
}
