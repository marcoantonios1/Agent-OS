package memory

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/marcoantonios1/Agent-OS/internal/sessions"
)

func TestProjectStore_SaveAndGet(t *testing.T) {
	s := NewProjectStore()
	p := &sessions.Project{
		ID:     "proj_abc",
		UserID: "user1",
		Name:   "Padel app",
		Phase:  "spec",
		Spec:   "# Overview\nTinder for padel.",
	}
	if err := s.SaveProject(p); err != nil {
		t.Fatalf("SaveProject: %v", err)
	}
	got, err := s.GetProject("proj_abc")
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if got.Name != "Padel app" {
		t.Errorf("Name: got %q", got.Name)
	}
	if got.Spec != p.Spec {
		t.Errorf("Spec mismatch")
	}
}

func TestProjectStore_NotFound(t *testing.T) {
	s := NewProjectStore()
	_, err := s.GetProject("nonexistent")
	if !errors.Is(err, sessions.ErrProjectNotFound) {
		t.Errorf("expected ErrProjectNotFound, got %v", err)
	}
}

func TestProjectStore_EmptyID_Rejected(t *testing.T) {
	s := NewProjectStore()
	err := s.SaveProject(&sessions.Project{ID: "", UserID: "u1"})
	if err == nil {
		t.Error("expected error for empty ID")
	}
}

func TestProjectStore_EmptyUserID_Rejected(t *testing.T) {
	s := NewProjectStore()
	err := s.SaveProject(&sessions.Project{ID: "proj_1", UserID: ""})
	if err == nil {
		t.Error("expected error for empty UserID")
	}
}

func TestProjectStore_UpdatedAt_SetOnSave(t *testing.T) {
	s := NewProjectStore()
	_ = s.SaveProject(&sessions.Project{ID: "p1", UserID: "u1"})
	got, _ := s.GetProject("p1")
	if got.UpdatedAt.IsZero() {
		t.Error("UpdatedAt should be set by SaveProject")
	}
}

func TestProjectStore_ReturnsCopy_NoMutation(t *testing.T) {
	s := NewProjectStore()
	_ = s.SaveProject(&sessions.Project{ID: "p1", UserID: "u1", Phase: "requirements", ADRs: []string{"adr1"}})

	got, _ := s.GetProject("p1")
	got.Phase = "MUTATED"
	got.ADRs[0] = "MUTATED"

	got2, _ := s.GetProject("p1")
	if got2.Phase != "requirements" {
		t.Errorf("Phase was mutated via returned pointer: got %q", got2.Phase)
	}
	if got2.ADRs[0] != "adr1" {
		t.Errorf("ADRs were mutated via returned pointer: got %q", got2.ADRs[0])
	}
}

func TestProjectStore_ListProjects_ByUser(t *testing.T) {
	s := NewProjectStore()
	_ = s.SaveProject(&sessions.Project{ID: "p1", UserID: "alice", Name: "Project A"})
	_ = s.SaveProject(&sessions.Project{ID: "p2", UserID: "alice", Name: "Project B"})
	_ = s.SaveProject(&sessions.Project{ID: "p3", UserID: "bob", Name: "Project C"})

	list, err := s.ListProjects("alice")
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(list) != 2 {
		t.Errorf("expected 2 projects for alice, got %d", len(list))
	}
	for _, p := range list {
		if p.ID == "p3" {
			t.Error("bob's project should not appear in alice's list")
		}
	}
}

func TestProjectStore_ListProjects_EmptyForUnknownUser(t *testing.T) {
	s := NewProjectStore()
	list, err := s.ListProjects("nobody")
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("expected empty list, got %d entries", len(list))
	}
}

func TestProjectStore_ListProjects_SortedByUpdatedAt(t *testing.T) {
	s := NewProjectStore()
	_ = s.SaveProject(&sessions.Project{ID: "p1", UserID: "u1", Name: "Old"})
	time.Sleep(2 * time.Millisecond)
	_ = s.SaveProject(&sessions.Project{ID: "p2", UserID: "u1", Name: "New"})

	list, _ := s.ListProjects("u1")
	if len(list) < 2 {
		t.Fatalf("expected 2 entries")
	}
	if list[0].Name != "New" {
		t.Errorf("most recent project should be first, got %q", list[0].Name)
	}
}

func TestProjectStore_ConcurrentReadWrite(t *testing.T) {
	s := NewProjectStore()
	_ = s.SaveProject(&sessions.Project{ID: "shared", UserID: "u1", Phase: "requirements"})

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			s.GetProject("shared") //nolint:errcheck
		}()
		go func() {
			defer wg.Done()
			s.SaveProject(&sessions.Project{ID: "shared", UserID: "u1", Phase: "spec"}) //nolint:errcheck
		}()
	}
	wg.Wait()
}
