package web_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/marcoantonios1/Agent-OS/internal/channels/web"
	"github.com/marcoantonios1/Agent-OS/internal/approval"
	"github.com/marcoantonios1/Agent-OS/internal/memory"
	"github.com/marcoantonios1/Agent-OS/internal/router"
	"github.com/marcoantonios1/Agent-OS/internal/types"
)

// --- test doubles ---

type stubClassifier struct{ intent router.Intent }

func (s *stubClassifier) Classify(_ context.Context, _, _ string, _ []types.ConversationTurn) ([]router.Intent, error) {
	return []router.Intent{s.intent}, nil
}

type recordingAgent struct {
	output string
	calls  []types.AgentRequest
}

func (a *recordingAgent) Handle(_ context.Context, req types.AgentRequest) (types.AgentResponse, error) {
	a.calls = append(a.calls, req)
	return types.AgentResponse{AgentID: "stub", Output: a.output}, nil
}

type panicAgent struct{}

func (p *panicAgent) Handle(_ context.Context, _ types.AgentRequest) (types.AgentResponse, error) {
	panic("something went very wrong")
}

// --- builder ---

type testFixture struct {
	srv   *httptest.Server
	agent *recordingAgent
}

func newFixture(t *testing.T, intent router.Intent, agentOutput string) *testFixture {
	t.Helper()
	store := memory.NewStore()
	agent := &recordingAgent{output: agentOutput}
	r := router.New(
		&stubClassifier{intent: intent},
		map[router.Intent]router.Agent{intent: agent},
		store,
		approval.NewMemoryStore(),
	)
	srv := httptest.NewServer(web.NewHandler(r))
	t.Cleanup(srv.Close)
	t.Cleanup(store.Close)
	return &testFixture{srv: srv, agent: agent}
}

func post(t *testing.T, srv *httptest.Server, body any) *http.Response {
	t.Helper()
	b, _ := json.Marshal(body)
	resp, err := http.Post(srv.URL+"/v1/chat", "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatalf("POST /v1/chat: %v", err)
	}
	return resp
}

func decodeChat(t *testing.T, resp *http.Response) map[string]string {
	t.Helper()
	var m map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	resp.Body.Close()
	return m
}

// --- tests ---

func TestHealthz(t *testing.T) {
	f := newFixture(t, router.IntentComms, "ok")
	resp, err := http.Get(f.srv.URL + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("got %d, want 200", resp.StatusCode)
	}
}

func TestChat_Success(t *testing.T) {
	f := newFixture(t, router.IntentComms, "Hello from comms agent")

	resp := post(t, f.srv, map[string]string{
		"session_id": "s1",
		"user_id":    "u1",
		"text":       "Send an email to Alice",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("got status %d, want 200", resp.StatusCode)
	}
	body := decodeChat(t, resp)
	if body["text"] != "Hello from comms agent" {
		t.Errorf("got text %q, want %q", body["text"], "Hello from comms agent")
	}
	if body["session_id"] != "s1" {
		t.Errorf("got session_id %q, want %q", body["session_id"], "s1")
	}
}

func TestChat_RequestIDHeader(t *testing.T) {
	f := newFixture(t, router.IntentComms, "hi")

	resp := post(t, f.srv, map[string]string{
		"session_id": "s-rid",
		"user_id":    "u1",
		"text":       "hello",
	})
	defer resp.Body.Close()
	if resp.Header.Get("X-Request-ID") == "" {
		t.Error("expected X-Request-ID header to be set")
	}
}

func TestChat_HonoursIncomingRequestID(t *testing.T) {
	f := newFixture(t, router.IntentComms, "hi")

	b, _ := json.Marshal(map[string]string{"session_id": "s2", "user_id": "u1", "text": "hi"})
	req, _ := http.NewRequest(http.MethodPost, f.srv.URL+"/v1/chat", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-ID", "my-custom-id")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request error: %v", err)
	}
	defer resp.Body.Close()
	if got := resp.Header.Get("X-Request-ID"); got != "my-custom-id" {
		t.Errorf("got X-Request-ID %q, want %q", got, "my-custom-id")
	}
}

func TestChat_MultiTurn_HistoryPreserved(t *testing.T) {
	f := newFixture(t, router.IntentBuilder, "")
	sessionID := "multi-turn"

	f.agent.output = "first reply"
	post(t, f.srv, map[string]string{
		"session_id": sessionID,
		"user_id":    "u1",
		"text":       "first message",
	}).Body.Close()

	f.agent.output = "second reply"
	resp := post(t, f.srv, map[string]string{
		"session_id": sessionID,
		"user_id":    "u1",
		"text":       "second message",
	})
	body := decodeChat(t, resp)
	if body["text"] != "second reply" {
		t.Errorf("got %q, want %q", body["text"], "second reply")
	}

	// The agent must have been called twice total.
	if len(f.agent.calls) != 2 {
		t.Fatalf("got %d agent calls, want 2", len(f.agent.calls))
	}
	// On the second call the agent must see the first exchange in its history.
	// History passed to agent = [user:first, assistant:first reply, user:second]
	h := f.agent.calls[1].History
	if len(h) != 3 {
		t.Fatalf("second call history len = %d, want 3", len(h))
	}
	if h[0].Role != "user" || h[0].Content != "first message" {
		t.Errorf("h[0] = %+v", h[0])
	}
	if h[1].Role != "assistant" || h[1].Content != "first reply" {
		t.Errorf("h[1] = %+v", h[1])
	}
	if h[2].Role != "user" || h[2].Content != "second message" {
		t.Errorf("h[2] = %+v", h[2])
	}
}

// --- validation tests ---

func TestChat_MissingSessionID(t *testing.T) {
	f := newFixture(t, router.IntentComms, "ok")
	resp := post(t, f.srv, map[string]string{"user_id": "u1", "text": "hi"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("got %d, want 400", resp.StatusCode)
	}
}

func TestChat_MissingUserID(t *testing.T) {
	f := newFixture(t, router.IntentComms, "ok")
	resp := post(t, f.srv, map[string]string{"session_id": "s1", "text": "hi"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("got %d, want 400", resp.StatusCode)
	}
}

func TestChat_MissingText(t *testing.T) {
	f := newFixture(t, router.IntentComms, "ok")
	resp := post(t, f.srv, map[string]string{"session_id": "s1", "user_id": "u1"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("got %d, want 400", resp.StatusCode)
	}
}

func TestChat_InvalidJSON(t *testing.T) {
	f := newFixture(t, router.IntentComms, "ok")
	resp, err := http.Post(f.srv.URL+"/v1/chat", "application/json",
		strings.NewReader("{not valid json"))
	if err != nil {
		t.Fatalf("request error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("got %d, want 400", resp.StatusCode)
	}
}

func TestChat_ValidationErrorBody(t *testing.T) {
	f := newFixture(t, router.IntentComms, "ok")
	resp := post(t, f.srv, map[string]string{"user_id": "u1", "text": "hi"})
	var body map[string]string
	json.NewDecoder(resp.Body).Decode(&body)
	resp.Body.Close()
	if body["error"] == "" {
		t.Error("expected non-empty error field in 400 response")
	}
}

func TestChat_PanicRecovery(t *testing.T) {
	store := memory.NewStore()
	defer store.Close()

	r := router.New(
		&stubClassifier{intent: router.IntentComms},
		map[router.Intent]router.Agent{router.IntentComms: &panicAgent{}},
		store,
		approval.NewMemoryStore(),
	)
	srv := httptest.NewServer(web.NewHandler(r))
	defer srv.Close()

	resp := post(t, srv, map[string]string{
		"session_id": "panic-sess",
		"user_id":    "u1",
		"text":       "trigger panic",
	})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("got %d, want 500 after panic", resp.StatusCode)
	}
}

func TestChat_UnknownIntent_NoError(t *testing.T) {
	store := memory.NewStore()
	defer store.Close()

	r := router.New(
		&stubClassifier{intent: router.IntentUnknown},
		map[router.Intent]router.Agent{},
		store,
		approval.NewMemoryStore(),
	)
	srv := httptest.NewServer(web.NewHandler(r))
	defer srv.Close()

	resp := post(t, srv, map[string]string{
		"session_id": "s-unk",
		"user_id":    "u1",
		"text":       "🤔",
	})
	body := decodeChat(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("got %d, want 200", resp.StatusCode)
	}
	if body["text"] == "" {
		t.Error("expected fallback message, got empty text")
	}
}
