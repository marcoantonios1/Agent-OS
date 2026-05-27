package router

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/marcoantonios1/Agent-OS/internal/costguard"
	"github.com/marcoantonios1/Agent-OS/internal/types"
)

// stubLLM returns a fixed summary response for any Complete call.
type stubLLM struct {
	summary string
	calls   int
}

func (s *stubLLM) Complete(_ context.Context, _ costguard.CompletionRequest) (costguard.CompletionResponse, error) {
	s.calls++
	return costguard.CompletionResponse{Content: s.summary}, nil
}

func (s *stubLLM) Embed(_ context.Context, _ string) ([]float32, error) {
	return nil, nil
}

func (s *stubLLM) Stream(_ context.Context, _ costguard.CompletionRequest) (<-chan costguard.StreamChunk, error) {
	ch := make(chan costguard.StreamChunk, 1)
	ch <- costguard.StreamChunk{Content: s.summary, Done: true}
	close(ch)
	return ch, nil
}

// errorLLM always returns an error.
type errorLLM struct{}

func (e *errorLLM) Embed(_ context.Context, _ string) ([]float32, error) {
	return nil, nil
}

func (e *errorLLM) Complete(_ context.Context, _ costguard.CompletionRequest) (costguard.CompletionResponse, error) {
	return costguard.CompletionResponse{}, fmt.Errorf("llm error")
}
func (e *errorLLM) Stream(_ context.Context, _ costguard.CompletionRequest) (<-chan costguard.StreamChunk, error) {
	return nil, fmt.Errorf("llm error")
}

func makeTurns(n int, charsEach int) []types.ConversationTurn {
	turns := make([]types.ConversationTurn, n)
	for i := range turns {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		turns[i] = types.ConversationTurn{
			Role:    role,
			Content: strings.Repeat("x", charsEach),
		}
	}
	return turns
}

func TestCompact_ShortHistory_Unchanged(t *testing.T) {
	llm := &stubLLM{summary: "summary"}
	// 5 turns × 100 chars = 500 chars → ~125 tokens, well below 6000
	history := makeTurns(5, 100)
	got, err := compact(context.Background(), llm, "model", 6000, history)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != len(history) {
		t.Errorf("expected history unchanged (len %d), got len %d", len(history), len(got))
	}
	if llm.calls != 0 {
		t.Errorf("expected no LLM calls for short history, got %d", llm.calls)
	}
}

func TestCompact_LongHistory_Compacted(t *testing.T) {
	llm := &stubLLM{summary: "the user discussed Go and distributed systems"}
	// 20 turns × 1500 chars = 30000 chars → ~7500 tokens, above 6000
	history := makeTurns(20, 1500)
	got, err := compact(context.Background(), llm, "model", 6000, history)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Result: 1 summary turn + keepRecentTurns verbatim
	want := 1 + keepRecentTurns
	if len(got) != want {
		t.Errorf("expected %d turns after compaction, got %d", want, len(got))
	}
	if got[0].Role != "system" {
		t.Errorf("first turn should be system summary, got role %q", got[0].Role)
	}
	if !strings.Contains(got[0].Content, "[Earlier conversation summary:") {
		t.Errorf("summary turn missing expected prefix: %q", got[0].Content)
	}
	if llm.calls != 1 {
		t.Errorf("expected 1 LLM call, got %d", llm.calls)
	}
}

func TestCompact_LastTurnIsUserMessage(t *testing.T) {
	llm := &stubLLM{summary: "summary"}
	history := makeTurns(20, 1500)
	// Force last turn to be a known user message
	history[len(history)-1] = types.ConversationTurn{Role: "user", Content: "what is the capital of France?"}

	got, err := compact(context.Background(), llm, "model", 6000, history)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	last := got[len(got)-1]
	if last.Role != "user" || last.Content != "what is the capital of France?" {
		t.Errorf("most recent user message lost after compaction: %+v", last)
	}
}

func TestCompact_DisabledWhenThresholdZero(t *testing.T) {
	llm := &stubLLM{summary: "summary"}
	history := makeTurns(20, 1500)
	got, err := compact(context.Background(), llm, "model", 0, history)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != len(history) {
		t.Errorf("expected history unchanged when threshold=0, got len %d", len(got))
	}
	if llm.calls != 0 {
		t.Errorf("expected no LLM calls when disabled, got %d", llm.calls)
	}
}

func TestCompact_LLMError_ReturnsOriginal(t *testing.T) {
	history := makeTurns(20, 1500)
	got, err := compact(context.Background(), &errorLLM{}, "model", 6000, history)
	if err == nil {
		t.Fatal("expected error from LLM failure")
	}
	if len(got) != len(history) {
		t.Errorf("expected original history on LLM error, got len %d", len(got))
	}
}

func TestCompact_TooFewTurns_Unchanged(t *testing.T) {
	llm := &stubLLM{summary: "summary"}
	// Only keepRecentTurns turns — nothing to summarise
	history := makeTurns(keepRecentTurns, 2000)
	got, err := compact(context.Background(), llm, "model", 6000, history)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != len(history) {
		t.Errorf("expected history unchanged, got len %d", len(got))
	}
}

func TestEstimateTokens(t *testing.T) {
	history := []types.ConversationTurn{
		{Role: "user", Content: strings.Repeat("a", 400)},    // 400 chars
		{Role: "assistant", Content: strings.Repeat("b", 400)}, // 400 chars
	}
	got := estimateTokens(history)
	want := 200 // 800 chars / 4
	if got != want {
		t.Errorf("estimateTokens = %d, want %d", got, want)
	}
}
