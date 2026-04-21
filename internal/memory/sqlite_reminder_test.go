package memory_test

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/marcoantonios1/Agent-OS/internal/memory"
	"github.com/marcoantonios1/Agent-OS/internal/sessions"
	"github.com/marcoantonios1/Agent-OS/internal/types"
)

func newTestReminderStore(t *testing.T) *memory.SQLiteReminderStore {
	t.Helper()
	db, err := memory.OpenDB(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return memory.NewSQLiteReminderStore(db)
}

func makeReminder(id, userID string, fireAt time.Time) *sessions.Reminder {
	return &sessions.Reminder{
		ID:        id,
		UserID:    userID,
		SessionID: "sess-1",
		ChannelID: types.ChannelID("discord"),
		Message:   "follow up on proposal",
		FireAt:    fireAt,
		CreatedAt: time.Now().UTC(),
	}
}

func TestSQLiteReminderStore_RoundTrip(t *testing.T) {
	store := newTestReminderStore(t)

	r := makeReminder("r1", "u1", time.Now().Add(time.Hour))
	if err := store.Save(r); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := store.Get("r1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Message != r.Message {
		t.Errorf("Message: got %q, want %q", got.Message, r.Message)
	}
	if string(got.ChannelID) != "discord" {
		t.Errorf("ChannelID: got %q", got.ChannelID)
	}
	if got.FireAt.IsZero() {
		t.Error("FireAt should be set")
	}
}

func TestSQLiteReminderStore_NotFound(t *testing.T) {
	store := newTestReminderStore(t)

	_, err := store.Get("no-such-id")
	if !errors.Is(err, sessions.ErrReminderNotFound) {
		t.Errorf("expected ErrReminderNotFound, got %v", err)
	}
}

func TestSQLiteReminderStore_Delete(t *testing.T) {
	store := newTestReminderStore(t)

	r := makeReminder("r1", "u1", time.Now().Add(time.Hour))
	store.Save(r) //nolint:errcheck

	if err := store.Delete("r1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	_, err := store.Get("r1")
	if !errors.Is(err, sessions.ErrReminderNotFound) {
		t.Errorf("expected ErrReminderNotFound after delete, got %v", err)
	}
}

func TestSQLiteReminderStore_Delete_NonExistent(t *testing.T) {
	store := newTestReminderStore(t)
	// Should not return an error.
	if err := store.Delete("ghost"); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestSQLiteReminderStore_Upsert(t *testing.T) {
	store := newTestReminderStore(t)

	r := makeReminder("r1", "u1", time.Now().Add(time.Hour))
	store.Save(r) //nolint:errcheck

	r.Message = "updated message"
	if err := store.Save(r); err != nil {
		t.Fatalf("second Save: %v", err)
	}
	got, _ := store.Get("r1")
	if got.Message != "updated message" {
		t.Errorf("Message after upsert: got %q", got.Message)
	}
}

func TestSQLiteReminderStore_EmptyID_Rejected(t *testing.T) {
	store := newTestReminderStore(t)
	err := store.Save(&sessions.Reminder{UserID: "u1", Message: "no id"})
	if err == nil {
		t.Error("expected error for empty ID")
	}
}

func TestSQLiteReminderStore_ListForUser_IsolatedByUser(t *testing.T) {
	store := newTestReminderStore(t)

	store.Save(makeReminder("r1", "alice", time.Now().Add(1*time.Hour))) //nolint:errcheck
	store.Save(makeReminder("r2", "alice", time.Now().Add(2*time.Hour))) //nolint:errcheck
	store.Save(makeReminder("r3", "bob", time.Now().Add(1*time.Hour)))   //nolint:errcheck

	list, err := store.ListForUser("alice")
	if err != nil {
		t.Fatalf("ListForUser: %v", err)
	}
	if len(list) != 2 {
		t.Errorf("expected 2 reminders for alice, got %d", len(list))
	}
	// Ordered by fire_at ascending.
	if list[0].ID != "r1" {
		t.Errorf("first item: got %q, want r1", list[0].ID)
	}
}

func TestSQLiteReminderStore_Due_FetchesAndDeletes(t *testing.T) {
	store := newTestReminderStore(t)

	past := time.Now().Add(-2 * time.Second)
	future := time.Now().Add(1 * time.Hour)

	store.Save(makeReminder("past1", "u1", past))   //nolint:errcheck
	store.Save(makeReminder("past2", "u1", past))   //nolint:errcheck
	store.Save(makeReminder("future", "u1", future)) //nolint:errcheck

	due, err := store.Due(time.Now())
	if err != nil {
		t.Fatalf("Due: %v", err)
	}
	if len(due) != 2 {
		t.Errorf("expected 2 due reminders, got %d", len(due))
	}

	// Due reminders must be removed from the store.
	list, _ := store.ListForUser("u1")
	if len(list) != 1 || list[0].ID != "future" {
		t.Errorf("expected only 'future' to remain, got %v", list)
	}
}

func TestSQLiteReminderStore_Due_Idempotent(t *testing.T) {
	store := newTestReminderStore(t)

	store.Save(makeReminder("r1", "u1", time.Now().Add(-time.Second))) //nolint:errcheck

	first, _ := store.Due(time.Now())
	second, _ := store.Due(time.Now())

	if len(first) != 1 {
		t.Errorf("first Due: expected 1, got %d", len(first))
	}
	if len(second) != 0 {
		t.Errorf("second Due: expected 0 (already consumed), got %d", len(second))
	}
}

func TestSQLiteReminderStore_PersistsAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "persist.db")

	db1, err := memory.OpenDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	s1 := memory.NewSQLiteReminderStore(db1)
	s1.Save(makeReminder("r-persist", "u1", time.Now().Add(time.Hour))) //nolint:errcheck
	db1.Close()

	db2, err := memory.OpenDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	s2 := memory.NewSQLiteReminderStore(db2)

	got, err := s2.Get("r-persist")
	if err != nil {
		t.Fatalf("Get after reopen: %v", err)
	}
	if got.Message != "follow up on proposal" {
		t.Errorf("Message: got %q", got.Message)
	}
}
