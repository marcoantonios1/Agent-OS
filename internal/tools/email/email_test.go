package email_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/marcoantonios1/Agent-OS/internal/tools/email"
)

// ── mock provider ─────────────────────────────────────────────────────────────

type mockProvider struct {
	summaries []email.EmailSummary
	emailByID map[string]*email.Email
	listErr   error
	readErr   error
	searchErr error
	draftErr  error
	// tracks whether Draft (not Send) was called
	draftCalls []struct{ to, subject, body string }
	sendCalls  int // must stay 0 — no tool should send
}

func (m *mockProvider) List(_ context.Context, limit int) ([]email.EmailSummary, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	if limit > len(m.summaries) {
		limit = len(m.summaries)
	}
	return m.summaries[:limit], nil
}

func (m *mockProvider) Read(_ context.Context, id string) (*email.Email, error) {
	if m.readErr != nil {
		return nil, m.readErr
	}
	e, ok := m.emailByID[id]
	if !ok {
		return nil, errors.New("email not found")
	}
	return e, nil
}

func (m *mockProvider) Search(_ context.Context, _ string) ([]email.EmailSummary, error) {
	if m.searchErr != nil {
		return nil, m.searchErr
	}
	return m.summaries, nil
}

func (m *mockProvider) Draft(_ context.Context, to, subject, body string) (*email.Draft, error) {
	if m.draftErr != nil {
		return nil, m.draftErr
	}
	m.draftCalls = append(m.draftCalls, struct{ to, subject, body string }{to, subject, body})
	return &email.Draft{To: to, Subject: subject, Body: body}, nil
}

// ── helpers ───────────────────────────────────────────────────────────────────

func fixedTime() time.Time { return time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC) }

func sampleSummaries() []email.EmailSummary {
	return []email.EmailSummary{
		{ID: "1", From: "alice@example.com", Subject: "Hello", Date: fixedTime(), Snippet: "Hi there"},
		{ID: "2", From: "bob@example.com", Subject: "Meeting", Date: fixedTime(), Snippet: "Are you free?"},
	}
}

func sampleEmail(id string) *email.Email {
	return &email.Email{
		ID:      id,
		From:    "alice@example.com",
		To:      []string{"me@example.com"},
		Subject: "Hello",
		Date:    fixedTime(),
		Body:    "Hi there, this is the full body.",
	}
}

func mustMarshal(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func decodeSlice(t *testing.T, result string) []map[string]any {
	t.Helper()
	var out []map[string]any
	if err := json.Unmarshal([]byte(result), &out); err != nil {
		t.Fatalf("decode slice: %v — raw: %s", err, result)
	}
	return out
}

func decodeMap(t *testing.T, result string) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(result), &out); err != nil {
		t.Fatalf("decode map: %v — raw: %s", err, result)
	}
	return out
}

// ── email_list ────────────────────────────────────────────────────────────────

func TestListTool_Definition(t *testing.T) {
	tool := email.NewListTool(&mockProvider{})
	def := tool.Definition()
	if def.Name != "email_list" {
		t.Errorf("name = %q, want %q", def.Name, "email_list")
	}
	if def.Description == "" {
		t.Error("description should not be empty")
	}
}

func TestListTool_ReturnsEmailSummaries(t *testing.T) {
	p := &mockProvider{summaries: sampleSummaries()}
	result, err := email.NewListTool(p).Execute(context.Background(),
		mustMarshal(t, map[string]int{"limit": 2}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	items := decodeSlice(t, result)
	if len(items) != 2 {
		t.Errorf("got %d items, want 2", len(items))
	}
	if items[0]["id"] != "1" {
		t.Errorf("unexpected first item: %v", items[0])
	}
}

func TestListTool_DefaultLimit(t *testing.T) {
	summaries := make([]email.EmailSummary, 20)
	for i := range summaries {
		summaries[i] = email.EmailSummary{ID: "x"}
	}
	p := &mockProvider{summaries: summaries}
	// no limit in input → defaults to 10
	result, err := email.NewListTool(p).Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	items := decodeSlice(t, result)
	if len(items) != 10 {
		t.Errorf("got %d items with default limit, want 10", len(items))
	}
}

func TestListTool_CapsAtMaxLimit(t *testing.T) {
	summaries := make([]email.EmailSummary, 100)
	for i := range summaries {
		summaries[i] = email.EmailSummary{ID: "x"}
	}
	p := &mockProvider{summaries: summaries}
	result, err := email.NewListTool(p).Execute(context.Background(),
		mustMarshal(t, map[string]int{"limit": 999}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	items := decodeSlice(t, result)
	if len(items) != 50 {
		t.Errorf("got %d items, want 50 (max cap)", len(items))
	}
}

func TestListTool_EmptyInput(t *testing.T) {
	p := &mockProvider{summaries: sampleSummaries()}
	_, err := email.NewListTool(p).Execute(context.Background(), nil)
	if err != nil {
		t.Errorf("nil input should be accepted, got: %v", err)
	}
}

func TestListTool_ProviderError(t *testing.T) {
	p := &mockProvider{listErr: errors.New("network error")}
	_, err := email.NewListTool(p).Execute(context.Background(), json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// ── email_read ────────────────────────────────────────────────────────────────

func TestReadTool_Definition(t *testing.T) {
	def := email.NewReadTool(&mockProvider{}).Definition()
	if def.Name != "email_read" {
		t.Errorf("name = %q", def.Name)
	}
}

func TestReadTool_ReturnsFullEmail(t *testing.T) {
	p := &mockProvider{emailByID: map[string]*email.Email{"42": sampleEmail("42")}}
	result, err := email.NewReadTool(p).Execute(context.Background(),
		mustMarshal(t, map[string]string{"id": "42"}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	m := decodeMap(t, result)
	if m["id"] != "42" {
		t.Errorf("got id %v, want %q", m["id"], "42")
	}
	if m["body"] == "" {
		t.Error("body should not be empty")
	}
}

func TestReadTool_MissingID(t *testing.T) {
	_, err := email.NewReadTool(&mockProvider{}).Execute(context.Background(),
		mustMarshal(t, map[string]string{}))
	if err == nil {
		t.Fatal("expected error for missing id")
	}
}

func TestReadTool_InvalidJSON(t *testing.T) {
	_, err := email.NewReadTool(&mockProvider{}).Execute(context.Background(),
		json.RawMessage(`not json`))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestReadTool_ProviderError(t *testing.T) {
	p := &mockProvider{readErr: errors.New("not found"), emailByID: map[string]*email.Email{}}
	_, err := email.NewReadTool(p).Execute(context.Background(),
		mustMarshal(t, map[string]string{"id": "99"}))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// ── email_search ──────────────────────────────────────────────────────────────

func TestSearchTool_Definition(t *testing.T) {
	def := email.NewSearchTool(&mockProvider{}).Definition()
	if def.Name != "email_search" {
		t.Errorf("name = %q", def.Name)
	}
}

func TestSearchTool_ReturnsSummaries(t *testing.T) {
	p := &mockProvider{summaries: sampleSummaries()}
	result, err := email.NewSearchTool(p).Execute(context.Background(),
		mustMarshal(t, map[string]string{"query": "from:alice"}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	items := decodeSlice(t, result)
	if len(items) == 0 {
		t.Error("expected non-empty results")
	}
}

func TestSearchTool_MissingQuery(t *testing.T) {
	_, err := email.NewSearchTool(&mockProvider{}).Execute(context.Background(),
		mustMarshal(t, map[string]string{}))
	if err == nil {
		t.Fatal("expected error for missing query")
	}
}

func TestSearchTool_ProviderError(t *testing.T) {
	p := &mockProvider{searchErr: errors.New("search failed")}
	_, err := email.NewSearchTool(p).Execute(context.Background(),
		mustMarshal(t, map[string]string{"query": "test"}))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// ── email_draft ───────────────────────────────────────────────────────────────

func TestDraftTool_Definition(t *testing.T) {
	def := email.NewDraftTool(&mockProvider{}).Definition()
	if def.Name != "email_draft" {
		t.Errorf("name = %q", def.Name)
	}
}

func TestDraftTool_ReturnsDraftWithoutSending(t *testing.T) {
	p := &mockProvider{}
	result, err := email.NewDraftTool(p).Execute(context.Background(),
		mustMarshal(t, map[string]string{
			"to":      "alice@example.com",
			"subject": "Hello",
			"body":    "How are you?",
		}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	m := decodeMap(t, result)
	if m["to"] != "alice@example.com" {
		t.Errorf("to = %v", m["to"])
	}
	if m["subject"] != "Hello" {
		t.Errorf("subject = %v", m["subject"])
	}
	if m["body"] != "How are you?" {
		t.Errorf("body = %v", m["body"])
	}

	// Critical: Draft was called, but nothing was sent.
	if len(p.draftCalls) != 1 {
		t.Errorf("Draft should be called once, got %d", len(p.draftCalls))
	}
	if p.sendCalls != 0 {
		t.Errorf("Send must never be called by any tool, got %d call(s)", p.sendCalls)
	}
}

func TestDraftTool_MissingTo(t *testing.T) {
	_, err := email.NewDraftTool(&mockProvider{}).Execute(context.Background(),
		mustMarshal(t, map[string]string{"subject": "Hi", "body": "Body"}))
	if err == nil {
		t.Fatal("expected error for missing to")
	}
}

func TestDraftTool_MissingSubject(t *testing.T) {
	_, err := email.NewDraftTool(&mockProvider{}).Execute(context.Background(),
		mustMarshal(t, map[string]string{"to": "a@b.com", "body": "Body"}))
	if err == nil {
		t.Fatal("expected error for missing subject")
	}
}

func TestDraftTool_MissingBody(t *testing.T) {
	_, err := email.NewDraftTool(&mockProvider{}).Execute(context.Background(),
		mustMarshal(t, map[string]string{"to": "a@b.com", "subject": "Hi"}))
	if err == nil {
		t.Fatal("expected error for missing body")
	}
}

func TestDraftTool_ProviderError(t *testing.T) {
	p := &mockProvider{draftErr: errors.New("draft service unavailable")}
	_, err := email.NewDraftTool(p).Execute(context.Background(),
		mustMarshal(t, map[string]string{"to": "a@b.com", "subject": "Hi", "body": "Body"}))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}
