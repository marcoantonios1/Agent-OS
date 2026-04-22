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
	}, "gemma4:26b")
}

// firstIntent is a helper to unwrap the first element from Classify's []Intent.
func firstIntent(t *testing.T, c *LLMClassifier, sessionID, input string, history []types.ConversationTurn) Intent {
	t.Helper()
	got, err := c.Classify(context.Background(), sessionID, input, history)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("Classify returned empty slice")
	}
	return got[0]
}

// --- Single intent classification tests ---

func TestClassify_CommsWorkflow(t *testing.T) {
	c := newClassifier(`{"intents":["comms"]}`, nil)
	inputs := []string{
		"Send Alice an email about tomorrow's meeting",
		"Remind me to call the dentist on Friday",
		"What's on my calendar today?",
	}
	for _, input := range inputs {
		if got := firstIntent(t, c, "s1", input, nil); got != IntentComms {
			t.Errorf("input %q: got %q, want %q", input, got, IntentComms)
		}
	}
}

func TestClassify_BuilderWorkflow(t *testing.T) {
	c := newClassifier(`{"intents":["builder"]}`, nil)
	inputs := []string{
		"Write a Python function that parses CSV files",
		"Why is my Go build failing with undefined: foo?",
		"Refactor this function to use generics",
	}
	for _, input := range inputs {
		if got := firstIntent(t, c, "s2", input, nil); got != IntentBuilder {
			t.Errorf("input %q: got %q, want %q", input, got, IntentBuilder)
		}
	}
}

func TestClassify_ResearchWorkflow(t *testing.T) {
	c := newClassifier(`{"intents":["research"]}`, nil)
	inputs := []string{
		"What are the main differences between REST and GraphQL?",
		"Summarise the latest news about climate change",
		"Which database is best for time-series data?",
	}
	for _, input := range inputs {
		if got := firstIntent(t, c, "s3", input, nil); got != IntentResearch {
			t.Errorf("input %q: got %q, want %q", input, got, IntentResearch)
		}
	}
}

// --- Compound intent tests ---

func TestClassify_CompoundRequest_CommsAndBuilder(t *testing.T) {
	c := newClassifier(`{"intents":["comms","builder"]}`, nil)
	got, err := c.Classify(context.Background(), "s10",
		"Reply to that investor email, then continue building the landing page", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d intents, want 2: %v", len(got), got)
	}
	if got[0] != IntentComms {
		t.Errorf("got[0] = %q, want %q", got[0], IntentComms)
	}
	if got[1] != IntentBuilder {
		t.Errorf("got[1] = %q, want %q", got[1], IntentBuilder)
	}
}

func TestClassify_CompoundRequest_OrderPreserved(t *testing.T) {
	c := newClassifier(`{"intents":["research","builder"]}`, nil)
	got, err := c.Classify(context.Background(), "s11",
		"Research GraphQL then write an implementation", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d intents, want 2", len(got))
	}
	if got[0] != IntentResearch || got[1] != IntentBuilder {
		t.Errorf("order wrong: got %v", got)
	}
}

// --- Graceful fallback tests ---

func TestClassify_UnknownIntent(t *testing.T) {
	c := newClassifier(`{"intents":["unknown"]}`, nil)
	if got := firstIntent(t, c, "s4", "🤔", nil); got != IntentUnknown {
		t.Errorf("got %q, want %q", got, IntentUnknown)
	}
}

func TestClassify_InvalidJSON_FallsBackToUnknown(t *testing.T) {
	c := newClassifier(`not json at all`, nil)
	if got := firstIntent(t, c, "s5", "anything", nil); got != IntentUnknown {
		t.Errorf("got %q, want %q", got, IntentUnknown)
	}
}

func TestClassify_UnrecognisedIntentValue_FallsBackToUnknown(t *testing.T) {
	c := newClassifier(`{"intents":["haiku"]}`, nil)
	if got := firstIntent(t, c, "s6", "anything", nil); got != IntentUnknown {
		t.Errorf("got %q, want %q", got, IntentUnknown)
	}
}

func TestClassify_MarkdownWrappedJSON(t *testing.T) {
	wrapped := "```json\n{\"intents\":[\"builder\"]}\n```"
	c := newClassifier(wrapped, nil)
	if got := firstIntent(t, c, "s7", "fix my code", nil); got != IntentBuilder {
		t.Errorf("got %q, want %q", got, IntentBuilder)
	}
}

func TestClassify_LLMError_ReturnsUnknownAndError(t *testing.T) {
	c := newClassifier("", errors.New("gateway timeout"))
	got, err := c.Classify(context.Background(), "s8", "hello", nil)
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if len(got) == 0 || got[0] != IntentUnknown {
		t.Errorf("got %v, want [unknown]", got)
	}
}

func TestClassify_WithHistory(t *testing.T) {
	history := []types.ConversationTurn{
		{Role: "user", Content: "I need help with code"},
		{Role: "assistant", Content: "Sure, what are you building?"},
	}
	c := newClassifier(`{"intents":["builder"]}`, nil)
	if got := firstIntent(t, c, "s9", "here's my function", history); got != IntentBuilder {
		t.Errorf("got %q, want %q", got, IntentBuilder)
	}
}

// --- Legacy format backward compat ---

func TestClassify_LegacySingleIntentFormat(t *testing.T) {
	// Old {"intent":"comms"} format should still parse correctly.
	c := newClassifier(`{"intent":"comms"}`, nil)
	if got := firstIntent(t, c, "s-legacy", "send email", nil); got != IntentComms {
		t.Errorf("legacy format: got %q, want %q", got, IntentComms)
	}
}

// --- parseIntents unit tests ---

func TestParseIntents_SingleIntent(t *testing.T) {
	cases := []struct {
		raw  string
		want []Intent
	}{
		{`{"intents":["comms"]}`, []Intent{IntentComms}},
		{`{"intents":["builder"]}`, []Intent{IntentBuilder}},
		{`{"intents":["research"]}`, []Intent{IntentResearch}},
		{`{"intents":["unknown"]}`, []Intent{IntentUnknown}},
		{`{"intents":["COMMS"]}`, []Intent{IntentUnknown}}, // case-sensitive
		{`{}`, []Intent{IntentUnknown}},
		{``, []Intent{IntentUnknown}},
		{`null`, []Intent{IntentUnknown}},
		{"  {\"intents\":[\"research\"]}  ", []Intent{IntentResearch}},
	}
	for _, tc := range cases {
		got := parseIntents(tc.raw)
		if len(got) != len(tc.want) {
			t.Errorf("parseIntents(%q) len = %d, want %d", tc.raw, len(got), len(tc.want))
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("parseIntents(%q)[%d] = %q, want %q", tc.raw, i, got[i], tc.want[i])
			}
		}
	}
}

func TestParseIntents_CompoundIntent(t *testing.T) {
	got := parseIntents(`{"intents":["comms","builder"]}`)
	if len(got) != 2 || got[0] != IntentComms || got[1] != IntentBuilder {
		t.Errorf("compound parse = %v, want [comms builder]", got)
	}
}

func TestParseIntent_LegacyCompat(t *testing.T) {
	// parseIntent (singular) should still work for existing unit tests.
	cases := []struct {
		raw  string
		want Intent
	}{
		{`{"intent":"comms"}`, IntentComms},
		{`{"intent":"builder"}`, IntentBuilder},
		{`{"intent":"research"}`, IntentResearch},
		{`{"intent":"unknown"}`, IntentUnknown},
		{`{"intent":"COMMS"}`, IntentUnknown},
		{`{}`, IntentUnknown},
		{``, IntentUnknown},
		{`null`, IntentUnknown},
		{"  {\"intent\":\"research\"}  ", IntentResearch},
	}
	for _, tc := range cases {
		if got := parseIntent(tc.raw); got != tc.want {
			t.Errorf("parseIntent(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}
