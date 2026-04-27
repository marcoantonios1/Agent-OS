package router

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/marcoantonios1/Agent-OS/internal/memory"
	"github.com/marcoantonios1/Agent-OS/internal/types"
)

// ── test double ───────────────────────────────────────────────────────────────

// capturingAgent records the AgentRequest it received and returns a fixed reply.
type capturingAgent struct {
	got   types.AgentRequest
	reply string
	err   error
}

func (a *capturingAgent) Handle(_ context.Context, req types.AgentRequest) (types.AgentResponse, error) {
	a.got = req
	return types.AgentResponse{Output: a.reply}, a.err
}

// newSubAgentRouter builds a minimal Router for sub-agent tests.
func newSubAgentRouter(agents map[Intent]Agent) *Router {
	return &Router{
		Classifier: NewLLMClassifier(&mockLLMClient{}, "test-model"),
		Agents:     agents,
		Sessions:   memory.NewStore(),
		log:        slog.Default(),
	}
}

// ── Call() tests ──────────────────────────────────────────────────────────────

func TestSubAgentCaller_CallsNamedAgent(t *testing.T) {
	agent := &capturingAgent{reply: "research output"}
	r := newSubAgentRouter(map[Intent]Agent{IntentResearch: agent})

	got, err := r.Call(context.Background(), "research", "Find competitors for X")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "research output" {
		t.Errorf("output = %q, want %q", got, "research output")
	}
	if agent.got.Input != "Find competitors for X" {
		t.Errorf("agent received input %q, want %q", agent.got.Input, "Find competitors for X")
	}
}

func TestSubAgentCaller_UnknownAgent_ReturnsError(t *testing.T) {
	r := newSubAgentRouter(map[Intent]Agent{})

	_, err := r.Call(context.Background(), "nonexistent", "hello")
	if err == nil {
		t.Fatal("expected error for unregistered agent, got nil")
	}
	want := `sub-agent "nonexistent" is not registered`
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
}

func TestSubAgentCaller_AgentError_Propagates(t *testing.T) {
	inner := errors.New("llm timeout")
	agent := &capturingAgent{err: inner}
	r := newSubAgentRouter(map[Intent]Agent{IntentResearch: agent})

	_, err := r.Call(context.Background(), "research", "anything")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, inner) {
		t.Errorf("error %v does not wrap original: %v", err, inner)
	}
}

func TestSubAgentCaller_DoesNotPersistHistory(t *testing.T) {
	agent := &capturingAgent{reply: "done"}
	store := memory.NewStore()
	r := &Router{
		Classifier: NewLLMClassifier(&mockLLMClient{}, "test-model"),
		Agents:     map[Intent]Agent{IntentResearch: agent},
		Sessions:   store,
		log:        slog.Default(),
	}

	_, err := r.Call(context.Background(), "research", "Find X")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Call must not create a session. Any session ID we probe should not exist.
	_, getErr := store.Get("any-session-id")
	if getErr == nil {
		t.Error("sub-call created a session in the store — history was polluted")
	}
}

// ── Injection test ────────────────────────────────────────────────────────────

func TestSubAgentCaller_InjectedIntoAgentRequest(t *testing.T) {
	// When the router dispatches a top-level call, the AgentRequest must carry
	// a non-nil SubCaller pointing to the router itself.
	agent := &capturingAgent{reply: "ok"}
	r := newSubAgentRouter(map[Intent]Agent{IntentResearch: agent})

	msg := types.InboundMessage{
		SessionID: "sess-inject",
		UserID:    "u1",
		Text:      "Find something",
	}
	_, _ = r.dispatch(context.Background(), msg, IntentResearch, nil, nil)

	if agent.got.SubCaller == nil {
		t.Error("AgentRequest.SubCaller was nil — router did not inject it")
	}
	if agent.got.SubCaller != r {
		t.Error("AgentRequest.SubCaller is not the router")
	}
}

func TestSubAgentCaller_SubCallHasNoHistory(t *testing.T) {
	// A sub-call must not carry the parent session's history — it is a fresh
	// invocation scoped to its own prompt only.
	agent := &capturingAgent{reply: "ok"}
	r := newSubAgentRouter(map[Intent]Agent{IntentResearch: agent})

	_, _ = r.Call(context.Background(), "research", "Find X")

	if len(agent.got.History) != 0 {
		t.Errorf("sub-call agent received %d history turns, want 0", len(agent.got.History))
	}
	if agent.got.SessionID != "" {
		t.Errorf("sub-call agent received session ID %q, want empty", agent.got.SessionID)
	}
}
