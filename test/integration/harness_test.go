// Package integration contains end-to-end tests for the four core MVP workflows.
// The full HTTP stack is spun up via httptest.Server; external dependencies
// (LLM, email, calendar, search) are replaced with lightweight in-process mocks.
package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"time"

	"github.com/marcoantonios1/Agent-OS/internal/agents/builder"
	"github.com/marcoantonios1/Agent-OS/internal/agents/comms"
	"github.com/marcoantonios1/Agent-OS/internal/agents/research"
	"github.com/marcoantonios1/Agent-OS/internal/approval"
	"github.com/marcoantonios1/Agent-OS/internal/channels/web"
	"github.com/marcoantonios1/Agent-OS/internal/costguard"
	"github.com/marcoantonios1/Agent-OS/internal/memory"
	"github.com/marcoantonios1/Agent-OS/internal/router"
	"github.com/marcoantonios1/Agent-OS/internal/tools/calendar"
	"github.com/marcoantonios1/Agent-OS/internal/tools/code"
	"github.com/marcoantonios1/Agent-OS/internal/tools/email"
	"github.com/marcoantonios1/Agent-OS/internal/tools"
	"github.com/marcoantonios1/Agent-OS/internal/tools/websearch"
	"github.com/marcoantonios1/Agent-OS/internal/types"
)

// ── scriptedLLM ───────────────────────────────────────────────────────────────

// scriptedLLM is a mock LLMClient that returns pre-scripted CompletionResponses
// in FIFO order. Each Complete call pops the next response from the queue.
// Tests will fail with a clear message if more calls are made than scripted.
type scriptedLLM struct {
	mu        sync.Mutex
	responses []costguard.CompletionResponse
	calls     []costguard.CompletionRequest // recorded for assertions
}

func newScriptedLLM(responses ...costguard.CompletionResponse) *scriptedLLM {
	return &scriptedLLM{responses: responses}
}

func (s *scriptedLLM) Complete(_ context.Context, req costguard.CompletionRequest) (costguard.CompletionResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, req)
	if len(s.responses) == 0 {
		return costguard.CompletionResponse{}, fmt.Errorf("scriptedLLM: no more scripted responses (call #%d)", len(s.calls))
	}
	resp := s.responses[0]
	s.responses = s.responses[1:]
	return resp, nil
}

func (s *scriptedLLM) Stream(_ context.Context, _ costguard.CompletionRequest) (<-chan costguard.StreamChunk, error) {
	ch := make(chan costguard.StreamChunk)
	close(ch)
	return ch, nil
}

// callCount returns how many times Complete was called.
func (s *scriptedLLM) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

// ── mockEmailProvider ─────────────────────────────────────────────────────────

// mockEmailProvider is a deterministic, in-memory EmailProvider.
type mockEmailProvider struct {
	mu       sync.Mutex
	inbox    []email.EmailSummary
	emails   map[string]*email.Email
	drafts   []*email.Draft
	sentEmails []string // to: addresses — should stay empty in draft-only tests
}

func newMockEmail(summaries []email.EmailSummary, full map[string]*email.Email) *mockEmailProvider {
	if full == nil {
		full = make(map[string]*email.Email)
	}
	return &mockEmailProvider{inbox: summaries, emails: full}
}

func (m *mockEmailProvider) List(_ context.Context, limit int) ([]email.EmailSummary, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if limit > len(m.inbox) {
		limit = len(m.inbox)
	}
	return m.inbox[:limit], nil
}

func (m *mockEmailProvider) Read(_ context.Context, id string) (*email.Email, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.emails[id]
	if !ok {
		return nil, fmt.Errorf("mockEmailProvider: email %q not found", id)
	}
	return e, nil
}

func (m *mockEmailProvider) Search(_ context.Context, query string) ([]email.EmailSummary, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.inbox, nil // return all for simplicity
}

func (m *mockEmailProvider) Draft(_ context.Context, to, subject, body string) (*email.Draft, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d := &email.Draft{To: to, Subject: subject, Body: body}
	m.drafts = append(m.drafts, d)
	return d, nil
}

func (m *mockEmailProvider) Send(_ context.Context, to, subject, body string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sentEmails = append(m.sentEmails, to)
	return nil
}

// ── mockCalendarProvider ──────────────────────────────────────────────────────

type mockCalendarProvider struct {
	events []*calendar.Event
}

func (m *mockCalendarProvider) List(_ context.Context, from, to time.Time) ([]calendar.Event, error) {
	out := make([]calendar.Event, 0, len(m.events))
	for _, e := range m.events {
		out = append(out, *e)
	}
	return out, nil
}

func (m *mockCalendarProvider) Read(_ context.Context, id string) (*calendar.Event, error) {
	for _, e := range m.events {
		if e.ID == id {
			return e, nil
		}
	}
	return nil, fmt.Errorf("mockCalendarProvider: event %q not found", id)
}

func (m *mockCalendarProvider) Create(_ context.Context, ev calendar.CreateEventInput) (*calendar.Event, error) {
	out := &calendar.Event{ID: "new-event", Title: ev.Title, Start: ev.Start, End: ev.End}
	m.events = append(m.events, out)
	return out, nil
}

func (m *mockCalendarProvider) Update(_ context.Context, in calendar.UpdateEventInput) (*calendar.Event, error) {
	for _, e := range m.events {
		if e.ID == in.EventID {
			if in.Title != "" {
				e.Title = in.Title
			}
			return e, nil
		}
	}
	return nil, fmt.Errorf("mockCalendarProvider: event %q not found", in.EventID)
}

// ── mockSearchProvider ────────────────────────────────────────────────────────

// mockSearchProvider is a deterministic, in-memory SearchProvider for tests.
type mockSearchProvider struct {
	results []websearch.SearchResult
}

func (m *mockSearchProvider) Search(_ context.Context, _ string, _ int) ([]websearch.SearchResult, error) {
	return m.results, nil
}

// newWebSearchRegistry builds a ToolRegistry backed by the given provider.
// Mirrors the helper in main.go so tests exercise the same wiring.
func newWebSearchRegistry(prov websearch.SearchProvider) *tools.ToolRegistry {
	return websearch.NewWebSearchRegistry(prov)
}

// ── testStack ─────────────────────────────────────────────────────────────────

// testStack holds the full HTTP test stack for one scenario.
type testStack struct {
	srv       *httptest.Server
	store     *memory.Store
	llm       *scriptedLLM
	emailProv *mockEmailProvider
}

// stackConfig configures what providers the stack includes.
type stackConfig struct {
	llmResponses []costguard.CompletionResponse
	emailProv    *mockEmailProvider
	calProv      calendar.CalendarProvider
	searchProv   websearch.SearchProvider // nil → stub (empty results)
	sandboxDir   string
}

// newStack spins up a full HTTP server with the given mocks. The caller must
// call stack.Close() when the test completes.
func newStack(cfg stackConfig) *testStack {
	llm := newScriptedLLM(cfg.llmResponses...)
	store := memory.NewStore()
	approvals := approval.NewMemoryStore()
	classifier := router.NewLLMClassifier(llm)

	sandboxDir := cfg.sandboxDir
	if sandboxDir == "" {
		sandboxDir = "testdata/sandbox"
	}

	searchProv := cfg.searchProv
	if searchProv == nil {
		searchProv = &mockSearchProvider{} // empty results stub
	}

	agents := map[router.Intent]router.Agent{
		router.IntentComms:    comms.New(llm, cfg.emailProv, cfg.calProv, approvals, memory.NewUserStore()),
		router.IntentBuilder:  builder.New(llm, store, code.Config{SandboxDir: sandboxDir}),
		router.IntentResearch: research.New(llm, newWebSearchRegistry(searchProv)),
	}

	r := router.New(classifier, agents, store, approvals)
	h := web.NewHandler(r, nil)
	srv := httptest.NewServer(h)

	return &testStack{
		srv:       srv,
		store:     store,
		llm:       llm,
		emailProv: cfg.emailProv,
	}
}

func (ts *testStack) Close() {
	ts.srv.Close()
	ts.store.Close()
}

// ── HTTP helpers ──────────────────────────────────────────────────────────────

type chatRequest struct {
	SessionID string `json:"session_id"`
	UserID    string `json:"user_id"`
	Text      string `json:"text"`
}

type chatResponse struct {
	SessionID string `json:"session_id"`
	Text      string `json:"text"`
}

// post sends a POST /v1/chat request and decodes the response.
func (ts *testStack) post(req chatRequest) (*http.Response, chatResponse, error) {
	b, _ := json.Marshal(req)
	resp, err := http.Post(ts.srv.URL+"/v1/chat", "application/json", bytes.NewReader(b))
	if err != nil {
		return nil, chatResponse{}, err
	}
	defer resp.Body.Close()
	var out chatResponse
	json.NewDecoder(resp.Body).Decode(&out) //nolint:errcheck
	return resp, out, nil
}

// ── LLM response builders ─────────────────────────────────────────────────────

// classifyResp returns a CompletionResponse that the classifier will interpret
// as the given intents.
func classifyResp(intents ...string) costguard.CompletionResponse {
	b, _ := json.Marshal(map[string][]string{"intents": intents})
	return costguard.CompletionResponse{Content: string(b)}
}

// textResp returns a CompletionResponse with plain text content (no tool calls).
func textResp(content string) costguard.CompletionResponse {
	return costguard.CompletionResponse{Content: content}
}

// toolCallResp returns a CompletionResponse that triggers a single tool call.
func toolCallResp(id, name, argsJSON string) costguard.CompletionResponse {
	return costguard.CompletionResponse{
		ToolCalls: []types.ToolCall{
			{ID: id, Name: name, Arguments: argsJSON},
		},
	}
}
