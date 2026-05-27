package tools_test

// Tests for AgenticLoop.Run() and RunStream() with ToolCallModel routing.

import (
	"context"
	"sync"
	"testing"

	"github.com/marcoantonios1/Agent-OS/internal/costguard"
	"github.com/marcoantonios1/Agent-OS/internal/tools"
	"github.com/marcoantonios1/Agent-OS/internal/types"
)

// ── model-recording LLM client ────────────────────────────────────────────────

// recordingLLM captures the model field from every Complete/Stream call.
// Responses are scripted in the same FIFO order as mockLLMClient.
type recordingLLM struct {
	mu            sync.Mutex
	responses     []costguard.CompletionResponse
	completeCalls []string // model name used in each Complete() call
	streamCalls   []string // model name used in each Stream() call
	streamChunks  []string // tokens emitted by Stream()
}

func (r *recordingLLM) Complete(_ context.Context, req costguard.CompletionRequest) (costguard.CompletionResponse, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.completeCalls = append(r.completeCalls, req.Model)
	if len(r.responses) == 0 {
		return costguard.CompletionResponse{Content: "fallback"}, nil
	}
	resp := r.responses[0]
	r.responses = r.responses[1:]
	return resp, nil
}

func (r *recordingLLM) Embed(_ context.Context, _ string) ([]float32, error) {
	return nil, nil
}

func (r *recordingLLM) Stream(_ context.Context, req costguard.CompletionRequest) (<-chan costguard.StreamChunk, error) {
	r.mu.Lock()
	r.streamCalls = append(r.streamCalls, req.Model)
	chunks := r.streamChunks
	r.mu.Unlock()

	ch := make(chan costguard.StreamChunk, len(chunks)+1)
	for i, c := range chunks {
		ch <- costguard.StreamChunk{Content: c, Done: i == len(chunks)-1}
	}
	if len(chunks) == 0 {
		ch <- costguard.StreamChunk{Content: "final answer", Done: true}
	}
	close(ch)
	return ch, nil
}

// ── helpers ───────────────────────────────────────────────────────────────────

func newRecordingLLM(responses ...costguard.CompletionResponse) *recordingLLM {
	return &recordingLLM{responses: responses}
}

func toolCallResponse(name string) costguard.CompletionResponse {
	return costguard.CompletionResponse{
		ToolCalls: []types.ToolCall{{ID: "tc1", Name: name, Arguments: `{}`}},
	}
}

func newLoopWithEcho() (*tools.AgenticLoop, *tools.ToolRegistry) {
	reg := tools.NewRegistry()
	reg.Register(&echoTool{name: "echo"})
	loop := &tools.AgenticLoop{Client: nil, Registry: reg, MaxSteps: 10}
	return loop, reg
}

// ── Run() tests ───────────────────────────────────────────────────────────────

// TestLoop_Run_NoToolCallModel_AllCallsUseModel verifies that when ToolCallModel
// is empty, every Complete() call uses req.Model (existing behaviour unchanged).
func TestLoop_Run_NoToolCallModel_AllCallsUseModel(t *testing.T) {
	llm := newRecordingLLM(
		toolCallResponse("echo"), // step 1: tool call
		textResp("done"),         // step 2: final answer
	)
	loop, _ := newLoopWithEcho()
	loop.Client = llm

	_, err := loop.Run(context.Background(), costguard.CompletionRequest{
		Model:    "expensive-model",
		Messages: []types.ConversationTurn{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	llm.mu.Lock()
	calls := llm.completeCalls
	llm.mu.Unlock()

	for i, model := range calls {
		if model != "expensive-model" {
			t.Errorf("call %d used model %q, want %q", i+1, model, "expensive-model")
		}
	}
}

// TestLoop_Run_ToolCallModel_ToolStepsUseCheapModel verifies that each
// intermediate Complete() call (returning tool calls) uses ToolCallModel.
func TestLoop_Run_ToolCallModel_ToolStepsUseCheapModel(t *testing.T) {
	llm := newRecordingLLM(
		toolCallResponse("echo"), // step 1: tool call → should use cheap model
		toolCallResponse("echo"), // step 2: tool call → should use cheap model
		textResp("done"),         // step 3: signal no more tools → cheap probe
		// step 4: final re-run with expensive model
	)
	loop, _ := newLoopWithEcho()
	loop.Client = llm

	_, err := loop.Run(context.Background(), costguard.CompletionRequest{
		Model:         "expensive-model",
		ToolCallModel: "cheap-model",
		Messages:      []types.ConversationTurn{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	llm.mu.Lock()
	calls := llm.completeCalls
	llm.mu.Unlock()

	// calls[0] = step 1 tool-call probe (cheap)
	// calls[1] = step 2 tool-call probe (cheap)
	// calls[2] = step 3 no-tool probe (cheap)
	// calls[3] = final synthesis re-run (expensive)
	if len(calls) < 3 {
		t.Fatalf("expected ≥3 Complete calls, got %d", len(calls))
	}
	for i := 0; i < len(calls)-1; i++ {
		if calls[i] != "cheap-model" {
			t.Errorf("call %d used %q, want cheap-model", i+1, calls[i])
		}
	}
	// Last call must use the expensive model.
	last := calls[len(calls)-1]
	if last != "expensive-model" {
		t.Errorf("final call used %q, want expensive-model", last)
	}
}

// TestLoop_Run_ToolCallModel_FinalStepUsesFullModel verifies that when the loop
// immediately gets a text response (no tool calls at all), the final re-run
// still uses req.Model.
func TestLoop_Run_ToolCallModel_FinalStepUsesFullModel(t *testing.T) {
	llm := newRecordingLLM(
		textResp("no tools needed"), // step 1: immediate final response (cheap probe)
		// step 2: re-run with expensive model
	)
	loop, _ := newLoopWithEcho()
	loop.Client = llm

	result, err := loop.Run(context.Background(), costguard.CompletionRequest{
		Model:         "expensive-model",
		ToolCallModel: "cheap-model",
		Messages:      []types.ConversationTurn{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result == "" {
		t.Error("expected non-empty result")
	}

	llm.mu.Lock()
	calls := llm.completeCalls
	llm.mu.Unlock()

	if len(calls) != 2 {
		t.Fatalf("expected 2 Complete calls (cheap probe + expensive final), got %d", len(calls))
	}
	if calls[0] != "cheap-model" {
		t.Errorf("first call (probe) used %q, want cheap-model", calls[0])
	}
	if calls[1] != "expensive-model" {
		t.Errorf("second call (final) used %q, want expensive-model", calls[1])
	}
}

// TestLoop_Run_SameModel_NoDuplicateCall verifies that when ToolCallModel ==
// Model, the loop does NOT make a redundant final re-run call.
func TestLoop_Run_SameModel_NoDuplicateCall(t *testing.T) {
	llm := newRecordingLLM(
		toolCallResponse("echo"), // step 1: tool call
		textResp("done"),         // step 2: final (same model, no re-run)
	)
	loop, _ := newLoopWithEcho()
	loop.Client = llm

	_, err := loop.Run(context.Background(), costguard.CompletionRequest{
		Model:         "same-model",
		ToolCallModel: "same-model", // same model — no re-run expected
		Messages:      []types.ConversationTurn{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	llm.mu.Lock()
	calls := llm.completeCalls
	llm.mu.Unlock()

	if len(calls) != 2 {
		t.Errorf("expected exactly 2 Complete calls (no re-run when models are equal), got %d", len(calls))
	}
}

// ── RunStream() tests ─────────────────────────────────────────────────────────

// TestLoop_RunStream_NoToolCallModel_AllCallsUseModel verifies existing
// behaviour: when ToolCallModel is empty, Complete() probes use req.Model.
func TestLoop_RunStream_NoToolCallModel_AllCallsUseModel(t *testing.T) {
	llm := newRecordingLLM(
		toolCallResponse("echo"), // step 1: tool call
		textResp(""),             // step 2: no tool calls → triggers Stream()
	)
	loop, _ := newLoopWithEcho()
	loop.Client = llm

	ch, err := loop.RunStream(context.Background(), costguard.CompletionRequest{
		Model:    "expensive-model",
		Messages: []types.ConversationTurn{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("RunStream: %v", err)
	}
	for range ch {
	} // drain

	llm.mu.Lock()
	completeCalls := llm.completeCalls
	streamCalls := llm.streamCalls
	llm.mu.Unlock()

	for i, model := range completeCalls {
		if model != "expensive-model" {
			t.Errorf("Complete call %d used %q, want expensive-model", i+1, model)
		}
	}
	if len(streamCalls) != 1 || streamCalls[0] != "expensive-model" {
		t.Errorf("Stream call used %v, want [expensive-model]", streamCalls)
	}
}

// TestLoop_RunStream_ToolCallModel_ProbesUseCheapModel verifies that
// intermediate Complete() probe calls use ToolCallModel.
func TestLoop_RunStream_ToolCallModel_ProbesUseCheapModel(t *testing.T) {
	llm := newRecordingLLM(
		toolCallResponse("echo"), // step 1: tool call → cheap
		toolCallResponse("echo"), // step 2: tool call → cheap
		textResp(""),             // step 3: no tool calls → triggers Stream() with expensive
	)
	loop, _ := newLoopWithEcho()
	loop.Client = llm

	ch, err := loop.RunStream(context.Background(), costguard.CompletionRequest{
		Model:         "expensive-model",
		ToolCallModel: "cheap-model",
		Messages:      []types.ConversationTurn{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("RunStream: %v", err)
	}
	for range ch {
	} // drain

	llm.mu.Lock()
	completeCalls := llm.completeCalls
	streamCalls := llm.streamCalls
	llm.mu.Unlock()

	// All Complete() probe calls must use the cheap model.
	for i, model := range completeCalls {
		if model != "cheap-model" {
			t.Errorf("Complete call %d used %q, want cheap-model", i+1, model)
		}
	}
	// The final Stream() must use the expensive model.
	if len(streamCalls) != 1 || streamCalls[0] != "expensive-model" {
		t.Errorf("Stream call used %v, want [expensive-model]", streamCalls)
	}
}
