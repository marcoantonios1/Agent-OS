package profile_test

import (
	"context"
	"testing"

	"github.com/marcoantonios1/Agent-OS/internal/agents/profile"
	"github.com/marcoantonios1/Agent-OS/internal/costguard"
	"github.com/marcoantonios1/Agent-OS/internal/memory"
	"github.com/marcoantonios1/Agent-OS/internal/sessions"
	"github.com/marcoantonios1/Agent-OS/internal/types"
)

// ── mock LLM ──────────────────────────────────────────────────────────────────

type mockLLM struct {
	content string
	err     error
	called  int
}

func (m *mockLLM) Complete(_ context.Context, _ costguard.CompletionRequest) (costguard.CompletionResponse, error) {
	m.called++
	return costguard.CompletionResponse{Content: m.content}, m.err
}

func (m *mockLLM) Stream(_ context.Context, _ costguard.CompletionRequest) (<-chan costguard.StreamChunk, error) {
	ch := make(chan costguard.StreamChunk, 1)
	ch <- costguard.StreamChunk{Content: m.content, Done: true}
	close(ch)
	return ch, m.err
}

// ── helpers ───────────────────────────────────────────────────────────────────

func turns(n int) []types.ConversationTurn {
	out := make([]types.ConversationTurn, n)
	for i := range out {
		if i%2 == 0 {
			out[i] = types.ConversationTurn{Role: "user", Content: "hello"}
		} else {
			out[i] = types.ConversationTurn{Role: "assistant", Content: "hi there"}
		}
	}
	return out
}

// ── tests ─────────────────────────────────────────────────────────────────────

func TestObserve_SkipsShortConversations(t *testing.T) {
	llm := &mockLLM{content: `[{"key":"response_length","value":"brief"}]`}
	store := memory.NewPersonalityStore()
	a := profile.New(llm, store, "test-model")

	if err := a.Observe(context.Background(), "u1", turns(2)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if llm.called != 0 {
		t.Errorf("LLM should not be called for < 3 turns, got %d calls", llm.called)
	}
	p, _ := store.GetPersonality("u1")
	if len(p.Signals) != 0 {
		t.Errorf("expected 0 signals for skipped observation, got %d", len(p.Signals))
	}
}

func TestObserve_ExtractsAndPersistsSignals(t *testing.T) {
	llm := &mockLLM{
		content: `[{"key":"response_length","value":"brief"},{"key":"technical_depth","value":"high"}]`,
	}
	store := memory.NewPersonalityStore()
	a := profile.New(llm, store, "test-model")

	if err := a.Observe(context.Background(), "u1", turns(4)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if llm.called != 1 {
		t.Errorf("expected 1 LLM call, got %d", llm.called)
	}

	p, err := store.GetPersonality("u1")
	if err != nil {
		t.Fatalf("GetPersonality: %v", err)
	}
	if len(p.Signals) != 2 {
		t.Fatalf("expected 2 signals, got %d", len(p.Signals))
	}

	byKey := make(map[string]sessions.PersonalitySignal, len(p.Signals))
	for _, s := range p.Signals {
		byKey[s.Key] = s
	}
	if byKey[sessions.SignalResponseLength].Value != "brief" {
		t.Errorf("response_length = %q, want brief", byKey[sessions.SignalResponseLength].Value)
	}
	if byKey[sessions.SignalTechnicalDepth].Value != "high" {
		t.Errorf("technical_depth = %q, want high", byKey[sessions.SignalTechnicalDepth].Value)
	}
}

func TestObserve_HandlesInvalidJSON(t *testing.T) {
	llm := &mockLLM{content: "not valid json at all"}
	store := memory.NewPersonalityStore()
	a := profile.New(llm, store, "test-model")

	// Invalid JSON must not return an error — it's treated as a no-op.
	if err := a.Observe(context.Background(), "u1", turns(4)); err != nil {
		t.Fatalf("expected nil error for bad JSON, got: %v", err)
	}
	p, _ := store.GetPersonality("u1")
	if len(p.Signals) != 0 {
		t.Errorf("expected 0 signals after parse failure, got %d", len(p.Signals))
	}
}

func TestObserve_HandlesEmptyJSONArray(t *testing.T) {
	llm := &mockLLM{content: "[]"}
	store := memory.NewPersonalityStore()
	a := profile.New(llm, store, "test-model")

	if err := a.Observe(context.Background(), "u1", turns(4)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	p, _ := store.GetPersonality("u1")
	if len(p.Signals) != 0 {
		t.Errorf("expected 0 signals for empty array, got %d", len(p.Signals))
	}
}

func TestObserve_AccumulatesCountOnRepeatedCalls(t *testing.T) {
	llm := &mockLLM{content: `[{"key":"response_length","value":"brief"}]`}
	store := memory.NewPersonalityStore()
	a := profile.New(llm, store, "test-model")

	for i := 0; i < 3; i++ {
		if err := a.Observe(context.Background(), "u1", turns(4)); err != nil {
			t.Fatalf("iteration %d: %v", i, err)
		}
	}

	p, _ := store.GetPersonality("u1")
	if len(p.Signals) != 1 {
		t.Fatalf("expected 1 signal, got %d", len(p.Signals))
	}
	if p.Signals[0].Count != 3 {
		t.Errorf("Count = %d, want 3", p.Signals[0].Count)
	}
}

func TestObserve_IsolatedByUser(t *testing.T) {
	llm := &mockLLM{content: `[{"key":"response_length","value":"brief"}]`}
	store := memory.NewPersonalityStore()
	a := profile.New(llm, store, "test-model")

	a.Observe(context.Background(), "alice", turns(4)) //nolint:errcheck
	a.Observe(context.Background(), "bob", turns(4))   //nolint:errcheck

	alice, _ := store.GetPersonality("alice")
	bob, _ := store.GetPersonality("bob")

	if len(alice.Signals) != 1 {
		t.Errorf("alice: expected 1 signal, got %d", len(alice.Signals))
	}
	if len(bob.Signals) != 1 {
		t.Errorf("bob: expected 1 signal, got %d", len(bob.Signals))
	}
}

func TestObserve_MinTurnsBoundary(t *testing.T) {
	llm := &mockLLM{content: `[{"key":"response_length","value":"brief"}]`}
	store := memory.NewPersonalityStore()
	a := profile.New(llm, store, "test-model")

	// Exactly minTurns (3) should trigger the LLM.
	if err := a.Observe(context.Background(), "u1", turns(3)); err != nil {
		t.Fatalf("unexpected error at minTurns: %v", err)
	}
	if llm.called != 1 {
		t.Errorf("expected 1 LLM call at minTurns boundary, got %d", llm.called)
	}
}
