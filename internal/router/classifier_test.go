package router

import (
	"context"
	"errors"
	"testing"

	"github.com/marcoantonios1/Agent-OS/internal/costguard"
	"github.com/marcoantonios1/Agent-OS/internal/types"
)

// mockLLMClient is a test double for costguard.LLMClient.
type mockLLMClient struct {
	response costguard.CompletionResponse
	err      error
}

func (m *mockLLMClient) Complete(_ context.Context, _ costguard.CompletionRequest) (costguard.CompletionResponse, error) {
	return m.response, m.err
}

func (m *mockLLMClient) Stream(_ context.Context, _ costguard.CompletionRequest) (<-chan costguard.StreamChunk, error) {
	ch := make(chan costguard.StreamChunk)
	close(ch)
	return ch, nil
}

func newClassifier(content string, err error) *LLMClassifier {
	return NewLLMClassifier(&mockLLMClient{
		response: costguard.CompletionResponse{Content: content},
		err:      err,
	})
}

// --- Intent classification tests (requirement: three example workflows) ---

func TestClassify_CommsWorkflow(t *testing.T) {
	c := newClassifier(`{"intent":"comms"}`, nil)
	inputs := []string{
		"Send Alice an email about tomorrow's meeting",
		"Remind me to call the dentist on Friday",
		"What's on my calendar today?",
	}
	for _, input := range inputs {
		got, err := c.Classify(context.Background(), "s1", input, nil)
		if err != nil {
			t.Fatalf("unexpected error for %q: %v", input, err)
		}
		if got != IntentComms {
			t.Errorf("input %q: got %q, want %q", input, got, IntentComms)
		}
	}
}

func TestClassify_BuilderWorkflow(t *testing.T) {
	c := newClassifier(`{"intent":"builder"}`, nil)
	inputs := []string{
		"Write a Python function that parses CSV files",
		"Why is my Go build failing with undefined: foo?",
		"Refactor this function to use generics",
	}
	for _, input := range inputs {
		got, err := c.Classify(context.Background(), "s2", input, nil)
		if err != nil {
			t.Fatalf("unexpected error for %q: %v", input, err)
		}
		if got != IntentBuilder {
			t.Errorf("input %q: got %q, want %q", input, got, IntentBuilder)
		}
	}
}

func TestClassify_ResearchWorkflow(t *testing.T) {
	c := newClassifier(`{"intent":"research"}`, nil)
	inputs := []string{
		"What are the main differences between REST and GraphQL?",
		"Summarise the latest news about climate change",
		"Which database is best for time-series data?",
	}
	for _, input := range inputs {
		got, err := c.Classify(context.Background(), "s3", input, nil)
		if err != nil {
			t.Fatalf("unexpected error for %q: %v", input, err)
		}
		if got != IntentResearch {
			t.Errorf("input %q: got %q, want %q", input, got, IntentResearch)
		}
	}
}

// --- Graceful fallback tests ---

func TestClassify_UnknownIntent(t *testing.T) {
	c := newClassifier(`{"intent":"unknown"}`, nil)
	got, err := c.Classify(context.Background(), "s4", "🤔", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != IntentUnknown {
		t.Errorf("got %q, want %q", got, IntentUnknown)
	}
}

func TestClassify_InvalidJSON_FallsBackToUnknown(t *testing.T) {
	c := newClassifier(`not json at all`, nil)
	got, err := c.Classify(context.Background(), "s5", "anything", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != IntentUnknown {
		t.Errorf("got %q, want %q", got, IntentUnknown)
	}
}

func TestClassify_UnrecognisedIntentValue_FallsBackToUnknown(t *testing.T) {
	c := newClassifier(`{"intent":"haiku"}`, nil)
	got, _ := c.Classify(context.Background(), "s6", "anything", nil)
	if got != IntentUnknown {
		t.Errorf("got %q, want %q", got, IntentUnknown)
	}
}

func TestClassify_MarkdownWrappedJSON(t *testing.T) {
	wrapped := "```json\n{\"intent\":\"builder\"}\n```"
	c := newClassifier(wrapped, nil)
	got, err := c.Classify(context.Background(), "s7", "fix my code", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != IntentBuilder {
		t.Errorf("got %q, want %q", got, IntentBuilder)
	}
}

func TestClassify_LLMError_ReturnsUnknownAndError(t *testing.T) {
	c := newClassifier("", errors.New("gateway timeout"))
	got, err := c.Classify(context.Background(), "s8", "hello", nil)
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if got != IntentUnknown {
		t.Errorf("got %q, want %q", got, IntentUnknown)
	}
}

func TestClassify_WithHistory(t *testing.T) {
	history := []types.ConversationTurn{
		{Role: "user", Content: "I need help with code"},
		{Role: "assistant", Content: "Sure, what are you building?"},
	}
	c := newClassifier(`{"intent":"builder"}`, nil)
	got, err := c.Classify(context.Background(), "s9", "here's my function", history)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != IntentBuilder {
		t.Errorf("got %q, want %q", got, IntentBuilder)
	}
}

// --- parseIntent unit tests ---

func TestParseIntent(t *testing.T) {
	cases := []struct {
		raw  string
		want Intent
	}{
		{`{"intent":"comms"}`, IntentComms},
		{`{"intent":"builder"}`, IntentBuilder},
		{`{"intent":"research"}`, IntentResearch},
		{`{"intent":"unknown"}`, IntentUnknown},
		{`{"intent":"COMMS"}`, IntentUnknown},  // case-sensitive
		{`{}`, IntentUnknown},
		{``, IntentUnknown},
		{`null`, IntentUnknown},
		{"  {\"intent\":\"research\"}  ", IntentResearch}, // leading/trailing space
	}
	for _, tc := range cases {
		got := parseIntent(tc.raw)
		if got != tc.want {
			t.Errorf("parseIntent(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}
