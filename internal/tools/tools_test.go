package tools_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/marcoantonios1/Agent-OS/internal/costguard"
	"github.com/marcoantonios1/Agent-OS/internal/tools"
	"github.com/marcoantonios1/Agent-OS/internal/types"
)

// ── test doubles ──────────────────────────────────────────────────────────────

// echoTool returns its input JSON as a string — useful for verifying the
// registry routes arguments correctly.
type echoTool struct{ name string }

func (e *echoTool) Definition() costguard.ToolDefinition {
	return costguard.ToolDefinition{
		Name:        e.name,
		Description: "echoes input back",
		Parameters:  map[string]any{"type": "object"},
	}
}

func (e *echoTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	return string(input), nil
}

// errorTool always returns an error.
type errorTool struct{}

func (t *errorTool) Definition() costguard.ToolDefinition {
	return costguard.ToolDefinition{Name: "fail_tool", Description: "always fails"}
}

func (t *errorTool) Execute(_ context.Context, _ json.RawMessage) (string, error) {
	return "", errors.New("tool execution failed")
}

// mockLLMClient returns a sequence of pre-configured responses, one per call.
type mockLLMClient struct {
	responses []costguard.CompletionResponse
	calls     int
}

func (m *mockLLMClient) Complete(_ context.Context, _ costguard.CompletionRequest) (costguard.CompletionResponse, error) {
	if m.calls >= len(m.responses) {
		return costguard.CompletionResponse{}, fmt.Errorf("unexpected call #%d", m.calls+1)
	}
	resp := m.responses[m.calls]
	m.calls++
	return resp, nil
}

func (m *mockLLMClient) Stream(_ context.Context, _ costguard.CompletionRequest) (<-chan costguard.StreamChunk, error) {
	ch := make(chan costguard.StreamChunk)
	close(ch)
	return ch, nil
}

func toolCall(id, name, args string) costguard.ToolCall {
	return costguard.ToolCall{ID: id, Name: name, Arguments: args}
}

func textResp(content string) costguard.CompletionResponse {
	return costguard.CompletionResponse{Content: content}
}

func toolCallResp(calls ...costguard.ToolCall) costguard.CompletionResponse {
	return costguard.CompletionResponse{ToolCalls: calls}
}

// ── ToolRegistry tests ────────────────────────────────────────────────────────

func TestRegistry_RegisterAndDefinitions(t *testing.T) {
	r := tools.NewRegistry()
	r.Register(&echoTool{name: "tool_a"})
	r.Register(&echoTool{name: "tool_b"})

	defs := r.Definitions()
	if len(defs) != 2 {
		t.Fatalf("got %d definitions, want 2", len(defs))
	}
	names := map[string]bool{}
	for _, d := range defs {
		names[d.Name] = true
	}
	if !names["tool_a"] || !names["tool_b"] {
		t.Errorf("definitions missing expected tool names: %v", names)
	}
}

func TestRegistry_Register_OverwritesDuplicate(t *testing.T) {
	r := tools.NewRegistry()
	r.Register(&echoTool{name: "my_tool"})
	r.Register(&echoTool{name: "my_tool"}) // replace

	if len(r.Definitions()) != 1 {
		t.Errorf("expected 1 definition after duplicate register, got %d", len(r.Definitions()))
	}
}

func TestRegistry_Execute_KnownTool(t *testing.T) {
	r := tools.NewRegistry()
	r.Register(&echoTool{name: "echo"})

	input := json.RawMessage(`{"msg":"hello"}`)
	result, err := r.Execute(context.Background(), "echo", input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != `{"msg":"hello"}` {
		t.Errorf("got %q, want %q", result, `{"msg":"hello"}`)
	}
}

func TestRegistry_Execute_UnknownTool(t *testing.T) {
	r := tools.NewRegistry()

	_, err := r.Execute(context.Background(), "nonexistent", nil)
	if err == nil {
		t.Fatal("expected error for unknown tool, got nil")
	}
	if err.Error() == "" {
		t.Error("error message should be non-empty")
	}
}

func TestRegistry_Execute_ToolError(t *testing.T) {
	r := tools.NewRegistry()
	r.Register(&errorTool{})

	_, err := r.Execute(context.Background(), "fail_tool", nil)
	if err == nil {
		t.Fatal("expected tool error, got nil")
	}
}

func TestRegistry_EmptyDefinitions(t *testing.T) {
	r := tools.NewRegistry()
	if defs := r.Definitions(); len(defs) != 0 {
		t.Errorf("empty registry should return 0 definitions, got %d", len(defs))
	}
}

// ── AgenticLoop tests ─────────────────────────────────────────────────────────

func newLoop(client costguard.LLMClient, reg *tools.ToolRegistry) *tools.AgenticLoop {
	return &tools.AgenticLoop{Client: client, Registry: reg}
}

func baseReq(text string) costguard.CompletionRequest {
	return costguard.CompletionRequest{
		Model: "test-model",
		Messages: []types.ConversationTurn{
			{Role: "user", Content: text},
		},
	}
}

func TestLoop_TextResponseNoCalls(t *testing.T) {
	client := &mockLLMClient{responses: []costguard.CompletionResponse{
		textResp("Here is your answer"),
	}}
	reg := tools.NewRegistry()
	loop := newLoop(client, reg)

	result, err := loop.Run(context.Background(), baseReq("hello"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "Here is your answer" {
		t.Errorf("got %q, want %q", result, "Here is your answer")
	}
	if client.calls != 1 {
		t.Errorf("got %d LLM calls, want 1", client.calls)
	}
}

func TestLoop_SingleToolCall(t *testing.T) {
	// Step 1: LLM requests echo tool.
	// Step 2: LLM returns final text after seeing the result.
	client := &mockLLMClient{responses: []costguard.CompletionResponse{
		toolCallResp(toolCall("c1", "echo", `{"q":"test"}`)),
		textResp("Done after echo"),
	}}
	reg := tools.NewRegistry()
	reg.Register(&echoTool{name: "echo"})

	result, err := newLoop(client, reg).Run(context.Background(), baseReq("use echo"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "Done after echo" {
		t.Errorf("got %q, want %q", result, "Done after echo")
	}
	if client.calls != 2 {
		t.Errorf("got %d LLM calls, want 2", client.calls)
	}
}

func TestLoop_MultiStepToolSequence(t *testing.T) {
	// Step 1: call tool_a
	// Step 2: call tool_b
	// Step 3: return text
	client := &mockLLMClient{responses: []costguard.CompletionResponse{
		toolCallResp(toolCall("c1", "tool_a", `{}`)),
		toolCallResp(toolCall("c2", "tool_b", `{}`)),
		textResp("All done"),
	}}
	reg := tools.NewRegistry()
	reg.Register(&echoTool{name: "tool_a"})
	reg.Register(&echoTool{name: "tool_b"})

	result, err := newLoop(client, reg).Run(context.Background(), baseReq("multi step"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "All done" {
		t.Errorf("got %q, want %q", result, "All done")
	}
	if client.calls != 3 {
		t.Errorf("got %d LLM calls, want 3", client.calls)
	}
}

func TestLoop_ParallelToolCallsInOneStep(t *testing.T) {
	// LLM requests two tools in a single response, then returns text.
	client := &mockLLMClient{responses: []costguard.CompletionResponse{
		toolCallResp(
			toolCall("c1", "tool_a", `{"n":1}`),
			toolCall("c2", "tool_b", `{"n":2}`),
		),
		textResp("parallel done"),
	}}
	reg := tools.NewRegistry()
	reg.Register(&echoTool{name: "tool_a"})
	reg.Register(&echoTool{name: "tool_b"})

	result, err := newLoop(client, reg).Run(context.Background(), baseReq("parallel"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "parallel done" {
		t.Errorf("got %q", result)
	}
	if client.calls != 2 {
		t.Errorf("got %d LLM calls, want 2", client.calls)
	}
}

func TestLoop_ToolError_ContinuesLoop(t *testing.T) {
	// The fail_tool errors; the loop should still feed the error back to the LLM
	// and allow it to return a final text response.
	client := &mockLLMClient{responses: []costguard.CompletionResponse{
		toolCallResp(toolCall("c1", "fail_tool", `{}`)),
		textResp("Handled the error"),
	}}
	reg := tools.NewRegistry()
	reg.Register(&errorTool{})

	result, err := newLoop(client, reg).Run(context.Background(), baseReq("try failing tool"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "Handled the error" {
		t.Errorf("got %q, want %q", result, "Handled the error")
	}
}

func TestLoop_UnknownToolName_ContinuesLoop(t *testing.T) {
	// LLM calls a tool that isn't registered; the error is fed back so the LLM
	// can recover, then it returns a text response.
	client := &mockLLMClient{responses: []costguard.CompletionResponse{
		toolCallResp(toolCall("c1", "ghost_tool", `{}`)),
		textResp("Recovered from unknown tool"),
	}}
	reg := tools.NewRegistry() // no tools registered

	result, err := newLoop(client, reg).Run(context.Background(), baseReq("unknown"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "Recovered from unknown tool" {
		t.Errorf("got %q", result)
	}
}

func TestLoop_MaxStepsExceeded(t *testing.T) {
	// LLM keeps requesting tools forever — loop must stop and return an error.
	responses := make([]costguard.CompletionResponse, 20)
	for i := range responses {
		responses[i] = toolCallResp(toolCall("c1", "echo", `{}`))
	}
	client := &mockLLMClient{responses: responses}
	reg := tools.NewRegistry()
	reg.Register(&echoTool{name: "echo"})

	loop := &tools.AgenticLoop{Client: client, Registry: reg, MaxSteps: 3}
	_, err := loop.Run(context.Background(), baseReq("infinite"))
	if err == nil {
		t.Fatal("expected max-steps error, got nil")
	}
}

func TestLoop_ToolDefinitionsInjected(t *testing.T) {
	// Verify that the registry's definitions are included in the CompletionRequest.
	var capturedReq costguard.CompletionRequest
	captureClient := &capturingLLMClient{
		response: textResp("ok"),
		capture:  &capturedReq,
	}
	reg := tools.NewRegistry()
	reg.Register(&echoTool{name: "my_echo"})

	loop := newLoop(captureClient, reg)
	loop.Run(context.Background(), baseReq("check defs")) //nolint:errcheck

	if len(capturedReq.Tools) != 1 || capturedReq.Tools[0].Name != "my_echo" {
		t.Errorf("tool definitions not injected into request: %+v", capturedReq.Tools)
	}
}

// capturingLLMClient captures the first CompletionRequest it receives.
type capturingLLMClient struct {
	response costguard.CompletionResponse
	capture  *costguard.CompletionRequest
}

func (c *capturingLLMClient) Complete(_ context.Context, req costguard.CompletionRequest) (costguard.CompletionResponse, error) {
	if c.capture != nil {
		*c.capture = req
		c.capture = nil // capture only the first call
	}
	return c.response, nil
}

func (c *capturingLLMClient) Stream(_ context.Context, _ costguard.CompletionRequest) (<-chan costguard.StreamChunk, error) {
	ch := make(chan costguard.StreamChunk)
	close(ch)
	return ch, nil
}
