package memory_test

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/marcoantonios1/Agent-OS/internal/memory"
	"github.com/marcoantonios1/Agent-OS/internal/sessions"
)

func newTestProjectStore(t *testing.T) *memory.SQLiteProjectStore {
	t.Helper()
	db, err := memory.OpenDB(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return memory.NewSQLiteProjectStore(db)
}

func TestSQLiteProjectStore_RoundTrip(t *testing.T) {
	store := newTestProjectStore(t)

	project := &sessions.Project{
		ID:         "proj-1",
		UserID:     "user-1",
		Name:       "Padel App",
		Phase:      "requirements",
		Spec:       "# Overview\nA padel booking app.",
		Tasks:      `[{"index":0,"title":"scaffold"}]`,
		ActiveTask: "0",
		ADRs:       []string{"Use Go for backend"},
		Milestones: []string{"MVP", "Beta"},
		CreatedAt:  time.Now(),
	}

	if err := store.SaveProject(project); err != nil {
		t.Fatalf("SaveProject: %v", err)
	}

	got, err := store.GetProject("proj-1")
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}

	if got.Name != "Padel App" {
		t.Errorf("Name: got %q, want %q", got.Name, "Padel App")
	}
	if got.Phase != "requirements" {
		t.Errorf("Phase: got %q", got.Phase)
	}
	if got.Spec != project.Spec {
		t.Errorf("Spec mismatch")
	}
	if got.Tasks != project.Tasks {
		t.Errorf("Tasks: got %q", got.Tasks)
	}
	if len(got.ADRs) != 1 || got.ADRs[0] != "Use Go for backend" {
		t.Errorf("ADRs: got %v", got.ADRs)
	}
	if len(got.Milestones) != 2 {
		t.Errorf("Milestones: got %v", got.Milestones)
	}
	if got.UpdatedAt.IsZero() {
		t.Error("UpdatedAt should be set by SaveProject")
	}
}

func TestSQLiteProjectStore_NotFound(t *testing.T) {
	store := newTestProjectStore(t)

	_, err := store.GetProject("no-such-project")
	if !errors.Is(err, sessions.ErrProjectNotFound) {
		t.Errorf("expected ErrProjectNotFound, got %v", err)
	}
}

func TestSQLiteProjectStore_Upsert(t *testing.T) {
	store := newTestProjectStore(t)

	p := &sessions.Project{ID: "p1", UserID: "u1", Name: "Original", Phase: "requirements"}
	if err := store.SaveProject(p); err != nil {
		t.Fatalf("first save: %v", err)
	}

	p.Phase = "spec"
	p.Spec = "# Spec content"
	if err := store.SaveProject(p); err != nil {
		t.Fatalf("second save: %v", err)
	}

	got, err := store.GetProject("p1")
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if got.Phase != "spec" {
		t.Errorf("Phase after upsert: got %q, want spec", got.Phase)
	}
	if got.Spec != "# Spec content" {
		t.Errorf("Spec after upsert: got %q", got.Spec)
	}
}

func TestSQLiteProjectStore_ListProjects_OrderedByUpdatedAt(t *testing.T) {
	store := newTestProjectStore(t)

	for _, p := range []sessions.Project{
		{ID: "p1", UserID: "u1", Name: "First"},
		{ID: "p2", UserID: "u1", Name: "Second"},
		{ID: "p3", UserID: "u1", Name: "Third"},
	} {
		pp := p
		if err := store.SaveProject(&pp); err != nil {
			t.Fatalf("SaveProject %s: %v", p.ID, err)
		}
		// Small sleep ensures distinct updated_at timestamps.
		time.Sleep(2 * time.Millisecond)
	}

	list, err := store.ListProjects("u1")
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("expected 3 projects, got %d", len(list))
	}
	// Most-recently-updated (Third) should be first.
	if list[0].Name != "Third" {
		t.Errorf("first item: got %q, want %q", list[0].Name, "Third")
	}
	if list[2].Name != "First" {
		t.Errorf("last item: got %q, want %q", list[2].Name, "First")
	}
}

func TestSQLiteProjectStore_ListProjects_IsolatedByUser(t *testing.T) {
	store := newTestProjectStore(t)

	_ = store.SaveProject(&sessions.Project{ID: "p-alice", UserID: "alice", Name: "Alice's Project"})
	_ = store.SaveProject(&sessions.Project{ID: "p-bob", UserID: "bob", Name: "Bob's Project"})

	list, err := store.ListProjects("alice")
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(list) != 1 || list[0].Name != "Alice's Project" {
		t.Errorf("expected only Alice's project, got %v", list)
	}
}

func TestSQLiteProjectStore_EmptyID_Rejected(t *testing.T) {
	store := newTestProjectStore(t)
	err := store.SaveProject(&sessions.Project{UserID: "u1"})
	if err == nil {
		t.Error("expected error for empty ID, got nil")
	}
}

func TestSQLiteProjectStore_EmptyUserID_Rejected(t *testing.T) {
	store := newTestProjectStore(t)
	err := store.SaveProject(&sessions.Project{ID: "p1"})
	if err == nil {
		t.Error("expected error for empty UserID, got nil")
	}
}

func TestSQLiteProjectStore_PersistsAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "persist.db")

	db1, err := memory.OpenDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	s1 := memory.NewSQLiteProjectStore(db1)
	if err := s1.SaveProject(&sessions.Project{
		ID: "persist-proj", UserID: "u1", Name: "Persistent", Phase: "spec",
	}); err != nil {
		t.Fatal(err)
	}
	db1.Close()

	db2, err := memory.OpenDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	s2 := memory.NewSQLiteProjectStore(db2)

	got, err := s2.GetProject("persist-proj")
	if err != nil {
		t.Fatalf("GetProject after reopen: %v", err)
	}
	if got.Phase != "spec" {
		t.Errorf("Phase: got %q, want spec", got.Phase)
	}
}
