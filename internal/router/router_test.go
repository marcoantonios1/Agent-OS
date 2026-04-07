package router

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/marcoantonios1/Agent-OS/internal/memory"
	"github.com/marcoantonios1/Agent-OS/internal/types"
)

// --- test doubles ---

type stubClassifier struct {
	intent Intent
	err    error
}

func (s *stubClassifier) Classify(_ context.Context, _, _ string, _ []types.ConversationTurn) (Intent, error) {
	return s.intent, s.err
}

type stubAgent struct {
	output string
	err    error
	calls  []types.AgentRequest
}

func (a *stubAgent) Handle(_ context.Context, req types.AgentRequest) (types.AgentResponse, error) {
	a.calls = append(a.calls, req)
	return types.AgentResponse{
		AgentID: "stub",
		Output:  a.output,
	}, a.err
}

// --- helpers ---

func newMsg(sessionID, userID, text string) types.InboundMessage {
	return types.InboundMessage{
		ID:        "msg-1",
		ChannelID: types.ChannelID("web"),
		UserID:    userID,
		SessionID: sessionID,
		Text:      text,
		Timestamp: time.Now(),
	}
}

func newRouter(intent Intent, agentOutput string) (*Router, *stubAgent, *memory.Store) {
	store := memory.NewStore()
	agent := &stubAgent{output: agentOutput}
	r := New(
		&stubClassifier{intent: intent},
		map[Intent]Agent{intent: agent},
		store,
	)
	return r, agent, store
}

// --- end-to-end route tests ---

func TestRoute_CommsAgent(t *testing.T) {
	r, agent, _ := newRouter(IntentComms, "Sure, I'll send that email.")
	msg := newMsg("sess-1", "user-1", "Send an email to Alice")

	out, err := r.Route(context.Background(), msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Text != "Sure, I'll send that email." {
		t.Errorf("got %q, want %q", out.Text, "Sure, I'll send that email.")
	}
	if out.SessionID != "sess-1" || out.UserID != "user-1" {
		t.Errorf("outbound message fields wrong: %+v", out)
	}
	if len(agent.calls) != 1 {
		t.Errorf("agent called %d times, want 1", len(agent.calls))
	}
}

func TestRoute_BuilderAgent(t *testing.T) {
	r, agent, _ := newRouter(IntentBuilder, "Here is the function:")
	out, err := r.Route(context.Background(), newMsg("sess-2", "user-2", "Write a sort function"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Text != "Here is the function:" {
		t.Errorf("got %q", out.Text)
	}
	if len(agent.calls) != 1 {
		t.Errorf("agent called %d times, want 1", len(agent.calls))
	}
}

func TestRoute_ResearchAgent(t *testing.T) {
	r, agent, _ := newRouter(IntentResearch, "According to my research…")
	out, err := r.Route(context.Background(), newMsg("sess-3", "user-3", "Explain REST vs GraphQL"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Text != "According to my research…" {
		t.Errorf("got %q", out.Text)
	}
	if len(agent.calls) != 1 {
		t.Errorf("agent called %d times, want 1", len(agent.calls))
	}
}

func TestRoute_UnknownIntent_ReturnsHelpfulMessage(t *testing.T) {
	store := memory.NewStore()
	r := New(
		&stubClassifier{intent: IntentUnknown},
		map[Intent]Agent{},
		store,
	)
	out, err := r.Route(context.Background(), newMsg("sess-4", "user-4", "🤔"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Text == "" {
		t.Error("expected a non-empty fallback reply for unknown intent")
	}
	if out.Text == "" {
		t.Error("reply should not be empty")
	}
}

func TestRoute_UnregisteredIntent_ReturnsHelpfulMessage(t *testing.T) {
	store := memory.NewStore()
	// Classifier returns "builder" but no builder agent is registered.
	r := New(
		&stubClassifier{intent: IntentBuilder},
		map[Intent]Agent{},
		store,
	)
	out, err := r.Route(context.Background(), newMsg("sess-5", "user-5", "help"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Text == "" {
		t.Error("expected a fallback reply when no agent is registered for the intent")
	}
}

func TestRoute_ClassifierError_FallsBackToUnknown(t *testing.T) {
	store := memory.NewStore()
	r := New(
		&stubClassifier{intent: IntentUnknown, err: errors.New("LLM unavailable")},
		map[Intent]Agent{},
		store,
	)
	// Should not return an error — classifier failures are non-fatal.
	out, err := r.Route(context.Background(), newMsg("sess-6", "user-6", "hello"))
	if err != nil {
		t.Fatalf("classifier error should be non-fatal, got: %v", err)
	}
	if out.Text == "" {
		t.Error("expected fallback reply")
	}
}

func TestRoute_AgentError_PropagatesError(t *testing.T) {
	store := memory.NewStore()
	agent := &stubAgent{err: errors.New("agent exploded")}
	r := New(
		&stubClassifier{intent: IntentComms},
		map[Intent]Agent{IntentComms: agent},
		store,
	)
	_, err := r.Route(context.Background(), newMsg("sess-7", "user-7", "hi"))
	if err == nil {
		t.Fatal("expected error from failing agent, got nil")
	}
}

// --- session history tests ---

func TestRoute_NewSessionIsCreated(t *testing.T) {
	r, _, store := newRouter(IntentComms, "reply")
	r.Route(context.Background(), newMsg("new-sess", "user-1", "hello"))

	sess, err := store.Get("new-sess")
	if err != nil {
		t.Fatalf("session not found after route: %v", err)
	}
	if sess.UserID != "user-1" {
		t.Errorf("session UserID = %q, want %q", sess.UserID, "user-1")
	}
}

func TestRoute_HistoryPersistedAfterTurn(t *testing.T) {
	r, _, store := newRouter(IntentComms, "got it")
	r.Route(context.Background(), newMsg("sess-h1", "user-1", "first message"))

	sess, err := store.Get("sess-h1")
	if err != nil {
		t.Fatalf("session not found: %v", err)
	}
	if len(sess.History) != 2 {
		t.Fatalf("got %d turns, want 2 (user + assistant)", len(sess.History))
	}
	if sess.History[0].Role != "user" || sess.History[0].Content != "first message" {
		t.Errorf("unexpected user turn: %+v", sess.History[0])
	}
	if sess.History[1].Role != "assistant" || sess.History[1].Content != "got it" {
		t.Errorf("unexpected assistant turn: %+v", sess.History[1])
	}
}

func TestRoute_MultiTurnHistoryGrowsCorrectly(t *testing.T) {
	r, agent, store := newRouter(IntentComms, "")
	sessionID := "sess-multi"

	turns := []struct{ input, reply string }{
		{"turn one", "reply one"},
		{"turn two", "reply two"},
		{"turn three", "reply three"},
	}

	for _, tc := range turns {
		agent.output = tc.reply
		_, err := r.Route(context.Background(), newMsg(sessionID, "user-1", tc.input))
		if err != nil {
			t.Fatalf("route error on %q: %v", tc.input, err)
		}
	}

	sess, err := store.Get(sessionID)
	if err != nil {
		t.Fatalf("session not found: %v", err)
	}
	// 3 user turns + 3 assistant turns = 6
	if len(sess.History) != 6 {
		t.Fatalf("got %d turns, want 6", len(sess.History))
	}
	// The second agent call should have received the first two turns as history.
	if len(agent.calls) != 3 {
		t.Fatalf("got %d agent calls, want 3", len(agent.calls))
	}
	// By the third call the history passed in should have 5 entries
	// (2 complete turns + current user turn).
	thirdCallHistory := agent.calls[2].History
	if len(thirdCallHistory) != 5 {
		t.Errorf("third agent call got history len %d, want 5", len(thirdCallHistory))
	}
}

func TestRoute_AgentReceivesFullHistory(t *testing.T) {
	r, agent, _ := newRouter(IntentBuilder, "done")
	sessionID := "sess-history"

	r.Route(context.Background(), newMsg(sessionID, "u1", "first"))
	r.Route(context.Background(), newMsg(sessionID, "u1", "second"))

	// On the second call the agent should see [user:first, assistant:done, user:second].
	if len(agent.calls) != 2 {
		t.Fatalf("want 2 agent calls, got %d", len(agent.calls))
	}
	h := agent.calls[1].History
	if len(h) != 3 {
		t.Fatalf("second call history len = %d, want 3", len(h))
	}
	if h[0].Content != "first" || h[0].Role != "user" {
		t.Errorf("h[0] = %+v", h[0])
	}
	if h[1].Content != "done" || h[1].Role != "assistant" {
		t.Errorf("h[1] = %+v", h[1])
	}
	if h[2].Content != "second" || h[2].Role != "user" {
		t.Errorf("h[2] = %+v", h[2])
	}
}
