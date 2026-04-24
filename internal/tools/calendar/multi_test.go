package calendar_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/marcoantonios1/Agent-OS/internal/tools/calendar"
)

// ── mock ──────────────────────────────────────────────────────────────────────

type mockCalProvider struct {
	events   []calendar.Event
	event    *calendar.Event
	listErr  error
	readErr  error
	created  *calendar.Event
	updated  *calendar.Event
}

func (m *mockCalProvider) List(_ context.Context, _, _ time.Time) ([]calendar.Event, error) {
	return m.events, m.listErr
}
func (m *mockCalProvider) Read(_ context.Context, _ string) (*calendar.Event, error) {
	return m.event, m.readErr
}
func (m *mockCalProvider) Create(_ context.Context, ev calendar.CreateEventInput) (*calendar.Event, error) {
	m.created = &calendar.Event{ID: "new", Title: ev.Title}
	return m.created, nil
}
func (m *mockCalProvider) Update(_ context.Context, inp calendar.UpdateEventInput) (*calendar.Event, error) {
	m.updated = &calendar.Event{ID: inp.EventID}
	return m.updated, nil
}

// ── helpers ───────────────────────────────────────────────────────────────────

var (
	from = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	to   = from.Add(24 * time.Hour)
)

func event(id string, start time.Duration) calendar.Event {
	s := from.Add(start)
	return calendar.Event{ID: id, Title: id, Start: s, End: s.Add(time.Hour)}
}

// ── List ──────────────────────────────────────────────────────────────────────

func TestMultiCalendar_List_MergesSortsByStartAsc(t *testing.T) {
	a := &mockCalProvider{events: []calendar.Event{
		event("a1", 1*time.Hour),
		event("a2", 3*time.Hour),
	}}
	b := &mockCalProvider{events: []calendar.Event{
		event("b1", 2*time.Hour),
		event("b2", 4*time.Hour),
	}}
	mp := calendar.NewMultiProvider(a, a, b)

	got, err := mp.List(context.Background(), from, to)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := []string{"a1", "b1", "a2", "b2"}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d", len(got), len(want))
	}
	for i, id := range want {
		if got[i].ID != id {
			t.Errorf("[%d] ID = %q, want %q", i, got[i].ID, id)
		}
	}
}

func TestMultiCalendar_List_DeduplicatesByID(t *testing.T) {
	shared := event("dup", 1*time.Hour)
	a := &mockCalProvider{events: []calendar.Event{shared}}
	b := &mockCalProvider{events: []calendar.Event{shared, event("b1", 2*time.Hour)}}
	mp := calendar.NewMultiProvider(a, a, b)

	got, _ := mp.List(context.Background(), from, to)
	seen := map[string]int{}
	for _, e := range got {
		seen[e.ID]++
	}
	if seen["dup"] != 1 {
		t.Errorf("duplicate appeared %d times, want 1", seen["dup"])
	}
	if len(got) != 2 {
		t.Errorf("total events = %d, want 2", len(got))
	}
}

func TestMultiCalendar_List_PartialFailureReturnsAvailable(t *testing.T) {
	a := &mockCalProvider{events: []calendar.Event{event("a1", 1*time.Hour)}}
	b := &mockCalProvider{listErr: errors.New("network error")}
	mp := calendar.NewMultiProvider(a, a, b)

	got, _ := mp.List(context.Background(), from, to)
	if len(got) != 1 || got[0].ID != "a1" {
		t.Errorf("expected results from healthy provider, got %v", got)
	}
}

// ── Read ──────────────────────────────────────────────────────────────────────

func TestMultiCalendar_Read_FirstHitWins(t *testing.T) {
	target := &calendar.Event{ID: "e1", Title: "found"}
	a := &mockCalProvider{readErr: errors.New("not found")}
	b := &mockCalProvider{event: target}
	mp := calendar.NewMultiProvider(a, a, b)

	got, err := mp.Read(context.Background(), "e1")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.ID != "e1" {
		t.Errorf("ID = %q, want %q", got.ID, "e1")
	}
}

func TestMultiCalendar_Read_AllFailReturnsError(t *testing.T) {
	a := &mockCalProvider{readErr: errors.New("not found")}
	b := &mockCalProvider{readErr: errors.New("not found")}
	mp := calendar.NewMultiProvider(a, a, b)

	_, err := mp.Read(context.Background(), "missing")
	if err == nil {
		t.Fatal("expected error when all providers fail, got nil")
	}
}

// ── Create / Update (primary only) ───────────────────────────────────────────

func TestMultiCalendar_Create_UsesPrimaryOnly(t *testing.T) {
	primary := &mockCalProvider{}
	secondary := &mockCalProvider{}
	mp := calendar.NewMultiProvider(primary, primary, secondary)

	got, err := mp.Create(context.Background(), calendar.CreateEventInput{Title: "Meeting"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if primary.created == nil {
		t.Fatal("primary Create not called")
	}
	if secondary.created != nil {
		t.Error("secondary Create must not be called")
	}
	if got.Title != "Meeting" {
		t.Errorf("Title = %q, want %q", got.Title, "Meeting")
	}
}

func TestMultiCalendar_Update_UsesPrimaryOnly(t *testing.T) {
	primary := &mockCalProvider{}
	secondary := &mockCalProvider{}
	mp := calendar.NewMultiProvider(primary, primary, secondary)

	_, err := mp.Update(context.Background(), calendar.UpdateEventInput{EventID: "e1", Title: "Updated"})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if primary.updated == nil {
		t.Fatal("primary Update not called")
	}
	if secondary.updated != nil {
		t.Error("secondary Update must not be called")
	}
}
