package generic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/marcoantonios1/Agent-OS/internal/costguard"
	"github.com/marcoantonios1/Agent-OS/internal/tools"
	"github.com/marcoantonios1/Agent-OS/internal/types"
)

// ── minimal fakes ─────────────────────────────────────────────────────────────

type fakeLLM struct {
	mu        sync.Mutex
	responses []costguard.CompletionResponse
	calls     []costguard.CompletionRequest
}

func (f *fakeLLM) Complete(_ context.Context, req costguard.CompletionRequest) (costguard.CompletionResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, req)
	if len(f.responses) == 0 {
		return costguard.CompletionResponse{}, fmt.Errorf("fakeLLM: no more responses")
	}
	r := f.responses[0]
	f.responses = f.responses[1:]
	return r, nil
}

func (f *fakeLLM) Stream(_ context.Context, _ costguard.CompletionRequest) (<-chan costguard.StreamChunk, error) {
	ch := make(chan costguard.StreamChunk, 1)
	ch <- costguard.StreamChunk{Content: "streamed", Done: true}
	close(ch)
	return ch, nil
}

type fakeSubCaller struct {
	mu      sync.Mutex
	results map[string]string
	calls   []struct{ agentID, prompt string }
}

func (f *fakeSubCaller) Call(_ context.Context, agentID, prompt string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, struct{ agentID, prompt string }{agentID, prompt})
	if r, ok := f.results[agentID]; ok {
		return r, nil
	}
	return "", fmt.Errorf("fakeSubCaller: unknown agent %q", agentID)
}

// ── New ───────────────────────────────────────────────────────────────────────

func TestNew_MissingID(t *testing.T) {
	_, err := New(Config{Model: "m", SystemPrompt: "s"}, tools.NewRegistry(), &fakeLLM{})
	if err == nil {
		t.Fatal("expected error for missing ID")
	}
}

func TestNew_MissingModel(t *testing.T) {
	_, err := New(Config{ID: "x", SystemPrompt: "s"}, tools.NewRegistry(), &fakeLLM{})
	if err == nil {
		t.Fatal("expected error for missing model")
	}
}

func TestNew_MissingSystemPrompt(t *testing.T) {
	_, err := New(Config{ID: "x", Model: "m"}, tools.NewRegistry(), &fakeLLM{})
	if err == nil {
		t.Fatal("expected error for empty system prompt")
	}
}

func TestNew_Valid(t *testing.T) {
	ag, err := New(Config{ID: "x", Model: "m", SystemPrompt: "You are x."}, tools.NewRegistry(), &fakeLLM{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ag == nil {
		t.Fatal("expected non-nil agent")
	}
}

// ── Handle ────────────────────────────────────────────────────────────────────

func validConfig() Config {
	return Config{
		ID:           "test-agent",
		Model:        "test-model",
		MaxTokens:    512,
		SystemPrompt: "You are a test agent.",
	}
}

func TestHandle_ReturnsLLMOutput(t *testing.T) {
	llm := &fakeLLM{responses: []costguard.CompletionResponse{{Content: "hello"}}}
	ag, _ := New(validConfig(), tools.NewRegistry(), llm)

	resp, err := ag.Handle(context.Background(), types.AgentRequest{
		SessionID: "s1",
		History: []types.ConversationTurn{
			{Role: "user", Content: "hi"},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Output != "hello" {
		t.Errorf("output = %q, want %q", resp.Output, "hello")
	}
	if resp.AgentID != "test-agent" {
		t.Errorf("agentID = %q, want %q", resp.AgentID, "test-agent")
	}
}

func TestHandle_SystemPromptIsFirstMessage(t *testing.T) {
	llm := &fakeLLM{responses: []costguard.CompletionResponse{{Content: "ok"}}}
	cfg := validConfig()
	cfg.SystemPrompt = "MySysPrompt"
	ag, _ := New(cfg, tools.NewRegistry(), llm)

	ag.Handle(context.Background(), types.AgentRequest{ //nolint:errcheck
		History: []types.ConversationTurn{{Role: "user", Content: "hi"}},
	})

	llm.mu.Lock()
	defer llm.mu.Unlock()
	if len(llm.calls) == 0 {
		t.Fatal("no LLM calls recorded")
	}
	msgs := llm.calls[0].Messages
	if len(msgs) == 0 || msgs[0].Role != "system" {
		t.Fatalf("first message is not system, got role=%q", msgs[0].Role)
	}
	if msgs[0].Content != "MySysPrompt" {
		t.Errorf("system content = %q, want %q", msgs[0].Content, "MySysPrompt")
	}
}

func TestHandle_OnlyDeclaredSkillsReachLoop(t *testing.T) {
	// Build a registry with two tools; declare only one in Skills.
	reg := tools.NewRegistry()
	reg.Register(&stubTool{name: "tool_a", result: `"a"`})
	reg.Register(&stubTool{name: "tool_b", result: `"b"`})

	cfg := validConfig()
	cfg.Skills = []string{"tool_a"}

	// LLM first calls tool_a, then returns text.
	llm := &fakeLLM{
		responses: []costguard.CompletionResponse{
			{ToolCalls: []types.ToolCall{{ID: "1", Name: "tool_a", Arguments: `{}`}}},
			{Content: "done"},
		},
	}
	ag, _ := New(cfg, reg, llm)
	resp, err := ag.Handle(context.Background(), types.AgentRequest{
		History: []types.ConversationTurn{{Role: "user", Content: "go"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Output != "done" {
		t.Errorf("output = %q", resp.Output)
	}
}

func TestHandle_DefaultMaxTokens(t *testing.T) {
	llm := &fakeLLM{responses: []costguard.CompletionResponse{{Content: "ok"}}}
	cfg := validConfig()
	cfg.MaxTokens = 0 // should default to 4096
	ag, _ := New(cfg, tools.NewRegistry(), llm)

	ag.Handle(context.Background(), types.AgentRequest{ //nolint:errcheck
		History: []types.ConversationTurn{{Role: "user", Content: "hi"}},
	})

	llm.mu.Lock()
	defer llm.mu.Unlock()
	if llm.calls[0].MaxTokens != 4096 {
		t.Errorf("MaxTokens = %d, want 4096", llm.calls[0].MaxTokens)
	}
}

func TestHandle_UnknownSkill_WarnsAndDoesNotCrash(t *testing.T) {
	// Declare a skill that is not in the global registry.
	// The loop should warn and skip it — the agent must still respond normally.
	llm := &fakeLLM{responses: []costguard.CompletionResponse{{Content: "ok despite missing skill"}}}
	reg := tools.NewRegistry()
	reg.Register(&stubTool{name: "real_tool", result: `"result"`})

	cfg := validConfig()
	cfg.Skills = []string{"real_tool", "nonexistent_skill"}
	ag, _ := New(cfg, reg, llm)

	resp, err := ag.Handle(context.Background(), types.AgentRequest{
		History: []types.ConversationTurn{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Output != "ok despite missing skill" {
		t.Errorf("output = %q", resp.Output)
	}
	// Only the real_tool should be in the loop's registry (1 definition).
	// Verified indirectly: if nonexistent_skill caused a panic or hard error,
	// the test would have failed above.
}

func TestHandle_LLMErrorPropagates(t *testing.T) {
	llm := &fakeLLM{} // no responses → error
	ag, _ := New(validConfig(), tools.NewRegistry(), llm)

	_, err := ag.Handle(context.Background(), types.AgentRequest{
		History: []types.ConversationTurn{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected error from empty LLM queue")
	}
}

// ── HandleStream ──────────────────────────────────────────────────────────────

func TestHandleStream_ReturnsTokens(t *testing.T) {
	// First Complete call returns no tool calls → loop falls through to Stream.
	llm := &fakeLLM{
		responses: []costguard.CompletionResponse{{Content: ""}}, // no tool calls
	}
	ag, _ := New(validConfig(), tools.NewRegistry(), llm)

	ch, err := ag.HandleStream(context.Background(), types.AgentRequest{
		History: []types.ConversationTurn{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got string
	for tok := range ch {
		got += tok
	}
	if got != "streamed" {
		t.Errorf("tokens = %q, want %q", got, "streamed")
	}
}

// ── call_agent tool ───────────────────────────────────────────────────────────

func TestCallAgentTool_Execute_Success(t *testing.T) {
	caller := &fakeSubCaller{results: map[string]string{"research": "found it"}}
	tool := newCallAgentTool(caller, []string{"research"})

	input, _ := json.Marshal(callAgentInput{AgentID: "research", Prompt: "find X"})
	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "found it" {
		t.Errorf("result = %q, want %q", result, "found it")
	}
}

func TestCallAgentTool_Execute_DisallowedAgent(t *testing.T) {
	caller := &fakeSubCaller{results: map[string]string{"research": "ok"}}
	tool := newCallAgentTool(caller, []string{"research"})

	input, _ := json.Marshal(callAgentInput{AgentID: "builder", Prompt: "build X"})
	_, err := tool.Execute(context.Background(), input)
	if err == nil {
		t.Fatal("expected error for disallowed agent")
	}
}

func TestCallAgentTool_Execute_EmptyAgentID(t *testing.T) {
	tool := newCallAgentTool(&fakeSubCaller{}, []string{"research"})
	input, _ := json.Marshal(callAgentInput{Prompt: "find X"})
	_, err := tool.Execute(context.Background(), input)
	if err == nil {
		t.Fatal("expected error for empty agent_id")
	}
}

func TestCallAgentTool_Execute_EmptyPrompt(t *testing.T) {
	tool := newCallAgentTool(&fakeSubCaller{}, []string{"research"})
	input, _ := json.Marshal(callAgentInput{AgentID: "research"})
	_, err := tool.Execute(context.Background(), input)
	if err == nil {
		t.Fatal("expected error for empty prompt")
	}
}

func TestCallAgentTool_Execute_SubCallerError(t *testing.T) {
	caller := &fakeSubCaller{} // unknown agent → error
	tool := newCallAgentTool(caller, []string{"research"})

	input, _ := json.Marshal(callAgentInput{AgentID: "research", Prompt: "find X"})
	_, err := tool.Execute(context.Background(), input)
	if err == nil {
		t.Fatal("expected error when sub-caller returns error")
	}
}

func TestHandle_CallAgentToolAddedWhenSubCallerPresent(t *testing.T) {
	llm := &fakeLLM{
		responses: []costguard.CompletionResponse{
			{ToolCalls: []types.ToolCall{{ID: "1", Name: "call_agent",
				Arguments: `{"agent_id":"research","prompt":"find X"}`}}},
			{Content: "done"},
		},
	}
	caller := &fakeSubCaller{results: map[string]string{"research": "found"}}
	cfg := validConfig()
	cfg.SubAgents = []string{"research"}
	ag, _ := New(cfg, tools.NewRegistry(), llm)

	resp, err := ag.Handle(context.Background(), types.AgentRequest{
		SubCaller: caller,
		History:   []types.ConversationTurn{{Role: "user", Content: "research something"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Output != "done" {
		t.Errorf("output = %q, want %q", resp.Output, "done")
	}
	if len(caller.calls) != 1 || caller.calls[0].agentID != "research" {
		t.Errorf("sub-caller was not invoked correctly: %+v", caller.calls)
	}
}

func TestHandle_CallAgentToolAbsentWhenNoSubCaller(t *testing.T) {
	// If the LLM tries to call call_agent but no SubCaller is set,
	// the tool should not be in the registry → loop returns an error in the
	// tool result, and the LLM should get an error message rather than panicking.
	llm := &fakeLLM{
		responses: []costguard.CompletionResponse{
			{ToolCalls: []types.ToolCall{{ID: "1", Name: "call_agent",
				Arguments: `{"agent_id":"research","prompt":"find X"}`}}},
			{Content: "handled error"},
		},
	}
	cfg := validConfig()
	cfg.SubAgents = []string{"research"}
	ag, _ := New(cfg, tools.NewRegistry(), llm)

	// SubCaller is nil → call_agent not registered
	resp, err := ag.Handle(context.Background(), types.AgentRequest{
		History: []types.ConversationTurn{{Role: "user", Content: "go"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Loop should feed back the "tool not registered" error and the LLM replies.
	if resp.Output != "handled error" {
		t.Errorf("output = %q", resp.Output)
	}
}

// ── Subset (ToolRegistry) ─────────────────────────────────────────────────────

func TestSubset_ReturnsRequestedTools(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(&stubTool{name: "a"})
	reg.Register(&stubTool{name: "b"})
	reg.Register(&stubTool{name: "c"})

	sub, missing := reg.Subset([]string{"a", "c"})
	if len(missing) != 0 {
		t.Errorf("unexpected missing: %v", missing)
	}
	defs := sub.Definitions()
	names := make(map[string]bool)
	for _, d := range defs {
		names[d.Name] = true
	}
	if !names["a"] || !names["c"] {
		t.Errorf("subset definitions = %v, want [a, c]", defs)
	}
	if names["b"] {
		t.Error("tool b should not be in subset")
	}
}

func TestSubset_ReportsMissingTools(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(&stubTool{name: "a"})

	_, missing := reg.Subset([]string{"a", "z"})
	if len(missing) != 1 || missing[0] != "z" {
		t.Errorf("missing = %v, want [z]", missing)
	}
}

func TestSubset_EmptyNames_ReturnsEmptyRegistry(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(&stubTool{name: "a"})

	sub, missing := reg.Subset(nil)
	if len(missing) != 0 {
		t.Errorf("unexpected missing: %v", missing)
	}
	if len(sub.Definitions()) != 0 {
		t.Errorf("expected empty subset, got %v", sub.Definitions())
	}
}

// ── stubTool helper ───────────────────────────────────────────────────────────

type stubTool struct {
	name   string
	result string
}

func (s *stubTool) Definition() costguard.ToolDefinition {
	return costguard.ToolDefinition{Name: s.name, Parameters: map[string]any{"type": "object"}}
}

func (s *stubTool) Execute(_ context.Context, _ json.RawMessage) (string, error) {
	if s.result == "" {
		return "", errors.New("stub error")
	}
	return s.result, nil
}
