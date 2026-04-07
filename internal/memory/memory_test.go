package memory

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/marcoantonios1/Agent-OS/internal/sessions"
	"github.com/marcoantonios1/Agent-OS/internal/types"
)

func newSession(id string) *sessions.Session {
	return &sessions.Session{
		ID:        id,
		UserID:    "user-1",
		ChannelID: types.ChannelID("web"),
		History:   nil,
		Metadata:  map[string]string{},
		CreatedAt: time.Now(),
	}
}

func TestGet_NotFound(t *testing.T) {
	s := NewStore()
	defer s.Close()

	_, err := s.Get("missing")
	if err != ErrSessionNotFound {
		t.Fatalf("got %v, want ErrSessionNotFound", err)
	}
}

func TestSaveAndGet(t *testing.T) {
	s := NewStore()
	defer s.Close()

	sess := newSession("s1")
	if err := s.Save(sess); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := s.Get("s1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID != "s1" || got.UserID != "user-1" {
		t.Errorf("unexpected session: %+v", got)
	}
}

func TestSave_SetsUpdatedAt(t *testing.T) {
	s := NewStore()
	defer s.Close()

	before := time.Now()
	sess := newSession("s2")
	s.Save(sess)
	after := time.Now()

	got, _ := s.Get("s2")
	if got.UpdatedAt.Before(before) || got.UpdatedAt.After(after) {
		t.Errorf("UpdatedAt %v not in [%v, %v]", got.UpdatedAt, before, after)
	}
}

func TestDelete(t *testing.T) {
	s := NewStore()
	defer s.Close()

	s.Save(newSession("s3"))
	s.Delete("s3")

	_, err := s.Get("s3")
	if err != ErrSessionNotFound {
		t.Fatalf("expected ErrSessionNotFound after delete, got %v", err)
	}
}

func TestDelete_NonExistent(t *testing.T) {
	s := NewStore()
	defer s.Close()

	if err := s.Delete("ghost"); err != nil {
		t.Errorf("Delete non-existent should not error, got %v", err)
	}
}

func TestAppendTurn(t *testing.T) {
	s := NewStore()
	defer s.Close()

	s.Save(newSession("s4"))
	s.AppendTurn("s4", "user", "hello")
	s.AppendTurn("s4", "assistant", "hi there")

	got, _ := s.Get("s4")
	if len(got.History) != 2 {
		t.Fatalf("got %d turns, want 2", len(got.History))
	}
	if got.History[0].Role != "user" || got.History[0].Content != "hello" {
		t.Errorf("unexpected turn 0: %+v", got.History[0])
	}
	if got.History[1].Role != "assistant" || got.History[1].Content != "hi there" {
		t.Errorf("unexpected turn 1: %+v", got.History[1])
	}
}

func TestAppendTurn_NotFound(t *testing.T) {
	s := NewStore()
	defer s.Close()

	err := s.AppendTurn("missing", "user", "hello")
	if err != ErrSessionNotFound {
		t.Fatalf("got %v, want ErrSessionNotFound", err)
	}
}

func TestExpiry_Get(t *testing.T) {
	s := NewStore(WithTTL(50 * time.Millisecond))
	defer s.Close()

	s.Save(newSession("exp1"))
	time.Sleep(100 * time.Millisecond)

	_, err := s.Get("exp1")
	if err != ErrSessionNotFound {
		t.Fatalf("expected expired session to return ErrSessionNotFound, got %v", err)
	}
}

func TestExpiry_AppendTurn(t *testing.T) {
	s := NewStore(WithTTL(50 * time.Millisecond))
	defer s.Close()

	s.Save(newSession("exp2"))
	time.Sleep(100 * time.Millisecond)

	err := s.AppendTurn("exp2", "user", "too late")
	if err != ErrSessionNotFound {
		t.Fatalf("expected ErrSessionNotFound on expired session, got %v", err)
	}
}

func TestExpiry_BackgroundCleanup(t *testing.T) {
	ttl := 50 * time.Millisecond
	s := NewStore(WithTTL(ttl))
	defer s.Close()

	s.Save(newSession("bg1"))

	// Wait for TTL + one cleanup cycle (ttl/2 ticker fires twice within 2*ttl).
	time.Sleep(3 * ttl)

	s.mu.RLock()
	_, exists := s.sessions["bg1"]
	s.mu.RUnlock()

	if exists {
		t.Error("expected expired session to be removed by background cleanup")
	}
}

func TestConcurrentReadWrite(t *testing.T) {
	s := NewStore()
	defer s.Close()

	const goroutines = 50
	var wg sync.WaitGroup
	wg.Add(goroutines * 3)

	// Writers
	for i := range goroutines {
		go func(i int) {
			defer wg.Done()
			sess := newSession(fmt.Sprintf("c%d", i))
			s.Save(sess)
		}(i)
	}

	// Readers
	for i := range goroutines {
		go func(i int) {
			defer wg.Done()
			s.Get(fmt.Sprintf("c%d", i)) //nolint:errcheck — may not exist yet
		}(i)
	}

	// Appenders
	for i := range goroutines {
		go func(i int) {
			defer wg.Done()
			s.AppendTurn(fmt.Sprintf("c%d", i), "user", "msg") //nolint:errcheck — may not exist yet
		}(i)
	}

	wg.Wait()
}

func TestGet_ReturnsCopy(t *testing.T) {
	s := NewStore()
	defer s.Close()

	s.Save(newSession("copy1"))
	got, _ := s.Get("copy1")

	// Mutating the returned session must not affect the stored one.
	got.UserID = "hacked"

	stored, _ := s.Get("copy1")
	if stored.UserID == "hacked" {
		t.Error("Get returned a reference to internal state, not a copy")
	}
}
