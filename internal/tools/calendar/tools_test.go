package calendar_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/marcoantonios1/Agent-OS/internal/tools/calendar"
)

// ── mock provider ─────────────────────────────────────────────────────────────

type mockProvider struct {
	events      []calendar.Event
	eventByID   map[string]*calendar.Event
	listErr     error
	readErr     error
	createErr   error
	createCalls []calendar.CreateEventInput
}

func (m *mockProvider) List(_ context.Context, from, to time.Time) ([]calendar.Event, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	var result []calendar.Event
	for _, e := range m.events {
		if !e.Start.Before(from) && e.Start.Before(to) {
			result = append(result, e)
		}
	}
	return result, nil
}

func (m *mockProvider) Read(_ context.Context, id string) (*calendar.Event, error) {
	if m.readErr != nil {
		return nil, m.readErr
	}
	e, ok := m.eventByID[id]
	if !ok {
		return nil, errors.New("event not found")
	}
	return e, nil
}

func (m *mockProvider) Create(_ context.Context, input calendar.CreateEventInput) (*calendar.Event, error) {
	if m.createErr != nil {
		return nil, m.createErr
	}
	m.createCalls = append(m.createCalls, input)
	return &calendar.Event{
		ID:          "new-event-id",
		Title:       input.Title,
		Description: input.Description,
		Location:    input.Location,
		Start:       input.Start,
		End:         input.End,
		Attendees:   input.Attendees,
	}, nil
}

// ── helpers ───────────────────────────────────────────────────────────────────

func mustMarshal(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, _ := json.Marshal(v)
	return b
}

func decodeMap(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("decode map: %v — raw: %s", err, s)
	}
	return m
}

func decodeSlice(t *testing.T, s string) []map[string]any {
	t.Helper()
	var v []map[string]any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatalf("decode slice: %v — raw: %s", err, s)
	}
	return v
}

func fixedDay(hour int) time.Time {
	return time.Date(2026, 4, 8, hour, 0, 0, 0, time.UTC)
}

func sampleEvents() []calendar.Event {
	return []calendar.Event{
		{
			ID:        "evt-1",
			Title:     "Team standup",
			Start:     fixedDay(9),
			End:       fixedDay(10),
			Attendees: []string{"alice@example.com", "marco_antonios1@outlook.com"},
		},
		{
			ID:    "evt-2",
			Title: "Lunch with Bob",
			Start: fixedDay(12),
			End:   fixedDay(13),
		},
		{
			ID:    "evt-3",
			Title: "Tomorrow's event",
			Start: fixedDay(24), // next day — should not appear in same-day list
			End:   fixedDay(25),
		},
	}
}

// ── calendar_list ─────────────────────────────────────────────────────────────

func TestListTool_Definition(t *testing.T) {
	def := calendar.NewListTool(&mockProvider{}).Definition()
	if def.Name != "calendar_list" {
		t.Errorf("name = %q", def.Name)
	}
}

func TestListTool_ReturnsEventsInRange(t *testing.T) {
	p := &mockProvider{events: sampleEvents()}
	result, err := calendar.NewListTool(p).Execute(context.Background(), mustMarshal(t, map[string]string{
		"from": fixedDay(0).Format(time.RFC3339),
		"to":   fixedDay(24).Format(time.RFC3339),
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	items := decodeSlice(t, result)
	if len(items) != 2 {
		t.Errorf("got %d events, want 2 (events within the day)", len(items))
	}
}

func TestListTool_TodayShorthand(t *testing.T) {
	p := &mockProvider{events: []calendar.Event{}}
	_, err := calendar.NewListTool(p).Execute(context.Background(), mustMarshal(t, map[string]string{
		"from": "today",
		"to":   "today",
	}))
	if err != nil {
		t.Errorf("'today' shorthand should be accepted, got: %v", err)
	}
}

func TestListTool_MissingFrom(t *testing.T) {
	p := &mockProvider{}
	_, err := calendar.NewListTool(p).Execute(context.Background(), mustMarshal(t, map[string]string{
		"to": fixedDay(24).Format(time.RFC3339),
	}))
	if err == nil {
		t.Fatal("expected error for missing from")
	}
}

func TestListTool_MissingTo(t *testing.T) {
	p := &mockProvider{}
	_, err := calendar.NewListTool(p).Execute(context.Background(), mustMarshal(t, map[string]string{
		"from": fixedDay(0).Format(time.RFC3339),
	}))
	if err == nil {
		t.Fatal("expected error for missing to")
	}
}

func TestListTool_InvalidJSON(t *testing.T) {
	_, err := calendar.NewListTool(&mockProvider{}).Execute(context.Background(), []byte(`{bad`))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestListTool_ProviderError(t *testing.T) {
	p := &mockProvider{listErr: errors.New("calendar unavailable")}
	_, err := calendar.NewListTool(p).Execute(context.Background(), mustMarshal(t, map[string]string{
		"from": fixedDay(0).Format(time.RFC3339),
		"to":   fixedDay(24).Format(time.RFC3339),
	}))
	if err == nil {
		t.Fatal("expected error from provider")
	}
}

func TestListTool_EmptyRange(t *testing.T) {
	p := &mockProvider{events: sampleEvents()}
	result, err := calendar.NewListTool(p).Execute(context.Background(), mustMarshal(t, map[string]string{
		"from": fixedDay(20).Format(time.RFC3339),
		"to":   fixedDay(24).Format(time.RFC3339),
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	items := decodeSlice(t, result)
	if len(items) != 0 {
		t.Errorf("got %d events, want 0 for empty range", len(items))
	}
}

// ── calendar_read ─────────────────────────────────────────────────────────────

func TestReadTool_Definition(t *testing.T) {
	def := calendar.NewReadTool(&mockProvider{}).Definition()
	if def.Name != "calendar_read" {
		t.Errorf("name = %q", def.Name)
	}
}

func TestReadTool_ReturnsEvent(t *testing.T) {
	evt := sampleEvents()[0]
	p := &mockProvider{eventByID: map[string]*calendar.Event{"evt-1": &evt}}
	result, err := calendar.NewReadTool(p).Execute(context.Background(),
		mustMarshal(t, map[string]string{"id": "evt-1"}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	m := decodeMap(t, result)
	if m["id"] != "evt-1" {
		t.Errorf("got id %v, want evt-1", m["id"])
	}
	if m["title"] != "Team standup" {
		t.Errorf("got title %v", m["title"])
	}
}

func TestReadTool_MissingID(t *testing.T) {
	_, err := calendar.NewReadTool(&mockProvider{}).Execute(context.Background(),
		mustMarshal(t, map[string]string{}))
	if err == nil {
		t.Fatal("expected error for missing id")
	}
}

func TestReadTool_NotFound(t *testing.T) {
	p := &mockProvider{eventByID: map[string]*calendar.Event{}}
	_, err := calendar.NewReadTool(p).Execute(context.Background(),
		mustMarshal(t, map[string]string{"id": "ghost"}))
	if err == nil {
		t.Fatal("expected error for non-existent event")
	}
}

func TestReadTool_ProviderError(t *testing.T) {
	p := &mockProvider{readErr: errors.New("read failed"), eventByID: map[string]*calendar.Event{}}
	_, err := calendar.NewReadTool(p).Execute(context.Background(),
		mustMarshal(t, map[string]string{"id": "evt-1"}))
	if err == nil {
		t.Fatal("expected error from provider")
	}
}

// ── calendar_create ───────────────────────────────────────────────────────────

func TestCreateTool_Definition(t *testing.T) {
	def := calendar.NewCreateTool(&mockProvider{}).Definition()
	if def.Name != "calendar_create" {
		t.Errorf("name = %q", def.Name)
	}
}

func validCreateInput(approved bool) map[string]any {
	return map[string]any{
		"approved": approved,
		"title":    "Team sync",
		"start":    fixedDay(14).Format(time.RFC3339),
		"end":      fixedDay(15).Format(time.RFC3339),
	}
}

func TestCreateTool_BlockedWithoutApproval(t *testing.T) {
	p := &mockProvider{}
	_, err := calendar.NewCreateTool(p).Execute(context.Background(),
		mustMarshal(t, validCreateInput(false)))
	if err == nil {
		t.Fatal("expected approval error, got nil")
	}
	if !errors.Is(err, calendar.ErrApprovalRequired) {
		t.Errorf("got %v, want ErrApprovalRequired", err)
	}
	if len(p.createCalls) != 0 {
		t.Errorf("provider Create must not be called without approval, got %d call(s)", len(p.createCalls))
	}
}

func TestCreateTool_BlockedWhenApprovedMissing(t *testing.T) {
	// approved field not included at all (defaults to false)
	p := &mockProvider{}
	input := map[string]any{
		"title": "Sneaky event",
		"start": fixedDay(14).Format(time.RFC3339),
		"end":   fixedDay(15).Format(time.RFC3339),
	}
	_, err := calendar.NewCreateTool(p).Execute(context.Background(), mustMarshal(t, input))
	if err == nil {
		t.Fatal("expected approval error when approved field is absent")
	}
	if len(p.createCalls) != 0 {
		t.Errorf("provider must not be called, got %d call(s)", len(p.createCalls))
	}
}

func TestCreateTool_CreatesWithApproval(t *testing.T) {
	p := &mockProvider{}
	result, err := calendar.NewCreateTool(p).Execute(context.Background(),
		mustMarshal(t, validCreateInput(true)))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	m := decodeMap(t, result)
	if m["title"] != "Team sync" {
		t.Errorf("got title %v", m["title"])
	}
	if len(p.createCalls) != 1 {
		t.Errorf("expected 1 provider call, got %d", len(p.createCalls))
	}
}

func TestCreateTool_WithAttendeesAndLocation(t *testing.T) {
	p := &mockProvider{}
	input := map[string]any{
		"approved":  true,
		"title":     "Product review",
		"start":     fixedDay(10).Format(time.RFC3339),
		"end":       fixedDay(11).Format(time.RFC3339),
		"location":  "Conference room A",
		"attendees": []string{"alice@example.com", "bob@example.com"},
	}
	result, err := calendar.NewCreateTool(p).Execute(context.Background(), mustMarshal(t, input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	m := decodeMap(t, result)
	if m["location"] != "Conference room A" {
		t.Errorf("location not preserved: %v", m["location"])
	}
	if p.createCalls[0].Attendees[0] != "alice@example.com" {
		t.Errorf("attendees not preserved: %v", p.createCalls[0].Attendees)
	}
}

func TestCreateTool_MissingTitle(t *testing.T) {
	p := &mockProvider{}
	input := map[string]any{
		"approved": true,
		"start":    fixedDay(10).Format(time.RFC3339),
		"end":      fixedDay(11).Format(time.RFC3339),
	}
	_, err := calendar.NewCreateTool(p).Execute(context.Background(), mustMarshal(t, input))
	if err == nil {
		t.Fatal("expected error for missing title")
	}
}

func TestCreateTool_EndBeforeStart(t *testing.T) {
	p := &mockProvider{}
	input := map[string]any{
		"approved": true,
		"title":    "Bad event",
		"start":    fixedDay(11).Format(time.RFC3339),
		"end":      fixedDay(10).Format(time.RFC3339), // end before start
	}
	_, err := calendar.NewCreateTool(p).Execute(context.Background(), mustMarshal(t, input))
	if err == nil {
		t.Fatal("expected error for end before start")
	}
}

func TestCreateTool_InvalidStartFormat(t *testing.T) {
	p := &mockProvider{}
	input := map[string]any{
		"approved": true,
		"title":    "Bad times",
		"start":    "not-a-time",
		"end":      fixedDay(11).Format(time.RFC3339),
	}
	_, err := calendar.NewCreateTool(p).Execute(context.Background(), mustMarshal(t, input))
	if err == nil {
		t.Fatal("expected error for invalid start format")
	}
}

func TestCreateTool_ProviderError(t *testing.T) {
	p := &mockProvider{createErr: errors.New("calendar write failed")}
	_, err := calendar.NewCreateTool(p).Execute(context.Background(),
		mustMarshal(t, validCreateInput(true)))
	if err == nil {
		t.Fatal("expected provider error")
	}
}
