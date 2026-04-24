package calendar_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/marcoantonios1/Agent-OS/internal/approval"
	"github.com/marcoantonios1/Agent-OS/internal/tools/calendar"
)

// ── mock provider ─────────────────────────────────────────────────────────────

type mockProvider struct {
	events      []calendar.Event
	eventByID   map[string]*calendar.Event
	listErr     error
	readErr     error
	createErr   error
	updateErr   error
	createCalls []calendar.CreateEventInput
	updateCalls []calendar.UpdateEventInput
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
		ID:        "new-event-id",
		Title:     input.Title,
		Location:  input.Location,
		Start:     input.Start,
		End:       input.End,
		Attendees: input.Attendees,
	}, nil
}

func (m *mockProvider) Update(_ context.Context, input calendar.UpdateEventInput) (*calendar.Event, error) {
	if m.updateErr != nil {
		return nil, m.updateErr
	}
	m.updateCalls = append(m.updateCalls, input)
	return &calendar.Event{ID: input.EventID, Title: input.Title}, nil
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
		{ID: "evt-1", Title: "Team standup", Start: fixedDay(9), End: fixedDay(10),
			Attendees: []string{"alice@example.com", "marco_antonios1@outlook.com"}},
		{ID: "evt-2", Title: "Lunch with Bob", Start: fixedDay(12), End: fixedDay(13)},
		{ID: "evt-3", Title: "Tomorrow's event", Start: fixedDay(24), End: fixedDay(25)},
	}
}

// grantedCtx returns a ctx with sessionID, and pre-grants actionID in store.
func grantedCtx(store approval.Store, sessionID, actionID string) context.Context {
	store.Pend(sessionID, actionID, "test")
	store.Grant(sessionID, actionID)
	return approval.WithSessionID(context.Background(), sessionID)
}

// ── calendar_list ─────────────────────────────────────────────────────────────

func TestListTool_Definition(t *testing.T) {
	if calendar.NewListTool(&mockProvider{}).Definition().Name != "calendar_list" {
		t.Error("wrong name")
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
	if items := decodeSlice(t, result); len(items) != 2 {
		t.Errorf("got %d events, want 2", len(items))
	}
}

func TestListTool_TodayShorthand(t *testing.T) {
	_, err := calendar.NewListTool(&mockProvider{}).Execute(context.Background(), mustMarshal(t, map[string]string{
		"from": "today", "to": "today",
	}))
	if err != nil {
		t.Errorf("'today' shorthand rejected: %v", err)
	}
}

func TestListTool_MissingFrom(t *testing.T) {
	_, err := calendar.NewListTool(&mockProvider{}).Execute(context.Background(),
		mustMarshal(t, map[string]string{"to": fixedDay(24).Format(time.RFC3339)}))
	if err == nil {
		t.Fatal("expected error for missing from")
	}
}

func TestListTool_MissingTo(t *testing.T) {
	_, err := calendar.NewListTool(&mockProvider{}).Execute(context.Background(),
		mustMarshal(t, map[string]string{"from": fixedDay(0).Format(time.RFC3339)}))
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
	_, err := calendar.NewListTool(&mockProvider{listErr: errors.New("down")}).Execute(
		context.Background(), mustMarshal(t, map[string]string{
			"from": fixedDay(0).Format(time.RFC3339),
			"to":   fixedDay(24).Format(time.RFC3339),
		}))
	if err == nil {
		t.Fatal("expected provider error")
	}
}

func TestListTool_EmptyRange(t *testing.T) {
	result, err := calendar.NewListTool(&mockProvider{events: sampleEvents()}).Execute(
		context.Background(), mustMarshal(t, map[string]string{
			"from": fixedDay(20).Format(time.RFC3339),
			"to":   fixedDay(24).Format(time.RFC3339),
		}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "No events found in the requested date range." {
		t.Errorf("got %q, want no-events message", result)
	}
}

// ── calendar_read ─────────────────────────────────────────────────────────────

func TestReadTool_Definition(t *testing.T) {
	if calendar.NewReadTool(&mockProvider{}).Definition().Name != "calendar_read" {
		t.Error("wrong name")
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
	if m["id"] != "evt-1" || m["title"] != "Team standup" {
		t.Errorf("unexpected event: %v", m)
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
	if calendar.NewCreateTool(&mockProvider{}, approval.NewMemoryStore()).Definition().Name != "calendar_create" {
		t.Error("wrong name")
	}
}

func TestCreateTool_ReturnsPendingWithoutApproval(t *testing.T) {
	store := approval.NewMemoryStore()
	p := &mockProvider{}
	ctx := approval.WithSessionID(context.Background(), "sess")
	result, err := calendar.NewCreateTool(p, store).Execute(ctx, mustMarshal(t, map[string]any{
		"title": "Team sync",
		"start": fixedDay(14).Format(time.RFC3339),
		"end":   fixedDay(15).Format(time.RFC3339),
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	m := decodeMap(t, result)
	if m["status"] != "pending_approval" {
		t.Errorf("expected pending_approval, got %v", m["status"])
	}
	if m["action_id"] == "" {
		t.Error("expected non-empty action_id")
	}
	if len(p.createCalls) != 0 {
		t.Errorf("provider must not be called without approval")
	}
}

func TestCreateTool_ActionIDIsDeterministic(t *testing.T) {
	store := approval.NewMemoryStore()
	p := &mockProvider{}
	ctx := approval.WithSessionID(context.Background(), "sess-det")
	input := mustMarshal(t, map[string]any{
		"title": "Team sync",
		"start": fixedDay(14).Format(time.RFC3339),
		"end":   fixedDay(15).Format(time.RFC3339),
	})
	r1, _ := calendar.NewCreateTool(p, store).Execute(ctx, input)
	r2, _ := calendar.NewCreateTool(p, store).Execute(ctx, input)
	if decodeMap(t, r1)["action_id"] != decodeMap(t, r2)["action_id"] {
		t.Error("action_id must be deterministic across identical inputs")
	}
}

func TestCreateTool_CreatesAfterApproval(t *testing.T) {
	store := approval.NewMemoryStore()
	p := &mockProvider{}
	const sess = "sess-approved"
	input := mustMarshal(t, map[string]any{
		"title": "Team sync",
		"start": fixedDay(14).Format(time.RFC3339),
		"end":   fixedDay(15).Format(time.RFC3339),
	})
	ctx := approval.WithSessionID(context.Background(), sess)

	r, _ := calendar.NewCreateTool(p, store).Execute(ctx, input)
	store.Grant(sess, decodeMap(t, r)["action_id"].(string))

	result, err := calendar.NewCreateTool(p, store).Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error after approval: %v", err)
	}
	if decodeMap(t, result)["title"] != "Team sync" {
		t.Error("wrong title in created event")
	}
	if len(p.createCalls) != 1 {
		t.Errorf("expected 1 provider call, got %d", len(p.createCalls))
	}
}

func TestCreateTool_WithAttendeesAndLocation(t *testing.T) {
	store := approval.NewMemoryStore()
	p := &mockProvider{}
	const sess = "sess-attendees"
	input := mustMarshal(t, map[string]any{
		"title":     "Product review",
		"start":     fixedDay(10).Format(time.RFC3339),
		"end":       fixedDay(11).Format(time.RFC3339),
		"location":  "Conference room A",
		"attendees": []string{"alice@example.com", "bob@example.com"},
	})
	ctx := approval.WithSessionID(context.Background(), sess)

	r, _ := calendar.NewCreateTool(p, store).Execute(ctx, input)
	store.Grant(sess, decodeMap(t, r)["action_id"].(string))

	result, err := calendar.NewCreateTool(p, store).Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decodeMap(t, result)["location"] != "Conference room A" {
		t.Error("location not preserved")
	}
	if p.createCalls[0].Attendees[0] != "alice@example.com" {
		t.Errorf("attendees not preserved: %v", p.createCalls[0].Attendees)
	}
}

func TestCreateTool_MissingTitle(t *testing.T) {
	store := approval.NewMemoryStore()
	_, err := calendar.NewCreateTool(&mockProvider{}, store).Execute(
		approval.WithSessionID(context.Background(), "sess"),
		mustMarshal(t, map[string]any{
			"start": fixedDay(10).Format(time.RFC3339),
			"end":   fixedDay(11).Format(time.RFC3339),
		}))
	if err == nil {
		t.Fatal("expected error for missing title")
	}
}

func TestCreateTool_EndBeforeStart(t *testing.T) {
	store := approval.NewMemoryStore()
	_, err := calendar.NewCreateTool(&mockProvider{}, store).Execute(
		approval.WithSessionID(context.Background(), "sess"),
		mustMarshal(t, map[string]any{
			"title": "Bad event",
			"start": fixedDay(11).Format(time.RFC3339),
			"end":   fixedDay(10).Format(time.RFC3339),
		}))
	if err == nil {
		t.Fatal("expected error for end before start")
	}
}

func TestCreateTool_InvalidStartFormat(t *testing.T) {
	store := approval.NewMemoryStore()
	_, err := calendar.NewCreateTool(&mockProvider{}, store).Execute(
		approval.WithSessionID(context.Background(), "sess"),
		mustMarshal(t, map[string]any{
			"title": "Bad times",
			"start": "not-a-time",
			"end":   fixedDay(11).Format(time.RFC3339),
		}))
	if err == nil {
		t.Fatal("expected error for invalid start format")
	}
}

func TestCreateTool_ProviderError(t *testing.T) {
	store := approval.NewMemoryStore()
	p := &mockProvider{createErr: errors.New("calendar write failed")}
	const sess = "sess-err"
	input := mustMarshal(t, map[string]any{
		"title": "Team sync",
		"start": fixedDay(14).Format(time.RFC3339),
		"end":   fixedDay(15).Format(time.RFC3339),
	})
	ctx := approval.WithSessionID(context.Background(), sess)

	r, _ := calendar.NewCreateTool(p, store).Execute(ctx, input)
	store.Grant(sess, decodeMap(t, r)["action_id"].(string))

	_, err := calendar.NewCreateTool(p, store).Execute(ctx, input)
	if err == nil {
		t.Fatal("expected provider error")
	}
}
