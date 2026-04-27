package builder_test

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/marcoantonios1/Agent-OS/internal/agents/builder"
	"github.com/marcoantonios1/Agent-OS/internal/costguard"
	"github.com/marcoantonios1/Agent-OS/internal/memory"
	"github.com/marcoantonios1/Agent-OS/internal/sessions"
	"github.com/marcoantonios1/Agent-OS/internal/tools/code"
	"github.com/marcoantonios1/Agent-OS/internal/types"
)

// ── mock LLM ──────────────────────────────────────────────────────────────────

// seqLLM returns pre-programmed responses in order, cycling on the last one.
type seqLLM struct {
	responses []costguard.CompletionResponse
	idx       int
}

func (m *seqLLM) Complete(_ context.Context, _ costguard.CompletionRequest) (costguard.CompletionResponse, error) {
	resp := m.responses[m.idx]
	if m.idx < len(m.responses)-1 {
		m.idx++
	}
	return resp, nil
}

func (m *seqLLM) Stream(_ context.Context, _ costguard.CompletionRequest) (<-chan costguard.StreamChunk, error) {
	ch := make(chan costguard.StreamChunk)
	close(ch)
	return ch, nil
}

func textReply(text string) costguard.CompletionResponse {
	return costguard.CompletionResponse{Content: text}
}

// ── helpers ───────────────────────────────────────────────────────────────────

func newAgent(t *testing.T, llm costguard.LLMClient, store *memory.Store) *builder.Agent {
	t.Helper()
	return builder.New(llm, store, code.Config{SandboxDir: t.TempDir()}, memory.NewProjectStore(), "gemma4:26b")
}

func newStore(t *testing.T) *memory.Store {
	t.Helper()
	s := memory.NewStore()
	t.Cleanup(s.Close)
	return s
}

func handle(t *testing.T, a *builder.Agent, sessionID, text string, meta map[string]string) types.AgentResponse {
	t.Helper()
	resp, err := a.Handle(context.Background(), types.AgentRequest{
		SessionID: sessionID,
		UserID:    "test-user",
		History: []types.ConversationTurn{
			{Role: "user", Content: text},
		},
		Input:    text,
		Metadata: meta,
	})
	if err != nil {
		t.Fatalf("Handle() error: %v", err)
	}
	return resp
}

// ── extractMeta (exported for testing via white-box approach) ─────────────────

// ── tests ─────────────────────────────────────────────────────────────────────

func TestSystemPromptIsFirst(t *testing.T) {
	// Verify that the system prompt appears before the user message in the LLM call.
	var capturedMsgs []types.ConversationTurn
	captureLLM := &capturingLLM{
		reply: "I have some questions for you.",
		capture: func(msgs []types.ConversationTurn) {
			capturedMsgs = msgs
		},
	}

	a := newAgent(t, captureLLM, newStore(t))
	handle(t, a, "s1", "Build me an app", nil)

	if len(capturedMsgs) == 0 {
		t.Fatal("no messages captured")
	}
	if capturedMsgs[0].Role != "system" {
		t.Errorf("first message role = %q, want system", capturedMsgs[0].Role)
	}
	if !strings.Contains(capturedMsgs[0].Content, "Builder Agent") {
		t.Error("system prompt does not mention Builder Agent")
	}
}

func TestRequirementsPhase_ClarifyingQuestions(t *testing.T) {
	// Vague request → agent asks clarifying questions (no phase transition yet).
	llm := &seqLLM{responses: []costguard.CompletionResponse{
		textReply("Great idea! To get started, I have a few questions:\n1. Target platform (iOS/Android/Web)?\n2. Key user flows?\n3. Any existing backend?"),
	}}

	a := newAgent(t, llm, newStore(t))
	resp := handle(t, a, "s1", "Build me a padel matching app like Tinder for padel", nil)

	if strings.Contains(resp.Output, "<builder_meta>") {
		t.Error("builder_meta block should be stripped from output")
	}
	if !strings.Contains(resp.Output, "question") && !strings.Contains(resp.Output, "?") {
		t.Errorf("expected clarifying questions in response, got: %s", resp.Output)
	}
	// No phase transition yet — metadata should be empty.
	if resp.Metadata[builder.KeyPhase] != "" {
		t.Errorf("phase should not have advanced yet, got %q", resp.Metadata[builder.KeyPhase])
	}
}

func TestRequirementsPhase_AdvancesToSpec(t *testing.T) {
	// After gathering enough info, agent transitions to spec phase.
	llm := &seqLLM{responses: []costguard.CompletionResponse{
		textReply("Got it! Here's what I understood...\n<builder_meta>{\"builder.phase\":\"spec\"}</builder_meta>"),
	}}

	store := newStore(t)
	a := newAgent(t, llm, store)
	resp := handle(t, a, "s1", "Mobile app, swiping to find padel partners", nil)

	if resp.Metadata[builder.KeyPhase] != builder.PhaseSpec {
		t.Errorf("phase = %q, want %q", resp.Metadata[builder.KeyPhase], builder.PhaseSpec)
	}
	if strings.Contains(resp.Output, "<builder_meta>") {
		t.Error("builder_meta block should be stripped from output")
	}
}

func TestSpecPhase_ProducesStructuredSpec(t *testing.T) {
	specContent := "# Padel Match\n## Overview\nTinder for padel.\n## Architecture\nReact Native + Go API."
	llm := &seqLLM{responses: []costguard.CompletionResponse{
		textReply("Here is the spec:\n\n" + specContent + "\n\nDoes this look right? Reply 'yes' to continue.\n<builder_meta>{\"builder.phase\":\"tasks\",\"builder.spec\":\"" + escapeJSON(specContent) + "\"}</builder_meta>"),
	}}

	store := newStore(t)
	// Seed session in spec phase.
	store.Save(sessionWith("s1", map[string]string{builder.KeyPhase: builder.PhaseSpec}))

	a := newAgent(t, llm, store)
	resp := handle(t, a, "s1", "yes", map[string]string{builder.KeyPhase: builder.PhaseSpec})

	if resp.Metadata[builder.KeyPhase] != builder.PhaseTasks {
		t.Errorf("phase = %q, want %q", resp.Metadata[builder.KeyPhase], builder.PhaseTasks)
	}
	if resp.Metadata[builder.KeySpec] == "" {
		t.Error("spec should be saved in metadata")
	}
	if strings.Contains(resp.Output, "<builder_meta>") {
		t.Error("builder_meta block should be stripped from output")
	}
}

func TestTasksPhase_ProducesNumberedTaskList(t *testing.T) {
	tasksJSON := `[{"index":0,"title":"Init project","files":["main.go"],"description":"Scaffold Go module"},{"index":1,"title":"API routes","files":["routes.go"],"description":"Define HTTP routes"}]`
	llm := &seqLLM{responses: []costguard.CompletionResponse{
		textReply("Here are the tasks:\n\n1. Init project\n2. API routes\n\nReady to start coding? Reply 'yes'.\n<builder_meta>{\"builder.phase\":\"codegen\",\"builder.tasks\":" + tasksJSON + ",\"builder.active_task\":\"0\"}</builder_meta>"),
	}}

	store := newStore(t)
	store.Save(sessionWith("s1", map[string]string{
		builder.KeyPhase: builder.PhaseTasks,
		builder.KeySpec:  "# Padel spec",
	}))

	a := newAgent(t, llm, store)
	resp := handle(t, a, "s1", "yes", nil)

	if resp.Metadata[builder.KeyPhase] != builder.PhaseCodegen {
		t.Errorf("phase = %q, want %q", resp.Metadata[builder.KeyPhase], builder.PhaseCodegen)
	}
	if resp.Metadata[builder.KeyTasks] == "" {
		t.Error("tasks should be saved in metadata")
	}
	if resp.Metadata[builder.KeyActiveTask] != "0" {
		t.Errorf("active_task = %q, want 0", resp.Metadata[builder.KeyActiveTask])
	}
}

func TestCodegenPhase_WritesFileAndValidates(t *testing.T) {
	// LLM requests file_write then shell_run, then produces final text.
	dir := t.TempDir()
	fileContent := "package main\n\nfunc main() {}\n"

	writeArgs, _ := json.Marshal(map[string]string{
		"path":    "main.go",
		"content": fileContent,
	})
	shellArgs, _ := json.Marshal(map[string]string{
		"command": "echo ok",
	})

	llm := &seqLLM{responses: []costguard.CompletionResponse{
		// Step 1: request file_write
		{ToolCalls: []types.ToolCall{{ID: "c1", Name: "file_write", Arguments: string(writeArgs)}}},
		// Step 2: request shell_run
		{ToolCalls: []types.ToolCall{{ID: "c2", Name: "shell_run", Arguments: string(shellArgs)}}},
		// Step 3: final text
		textReply("Task 1 done! File written and validated.\n<builder_meta>{\"builder.active_task\":\"1\"}</builder_meta>"),
	}}

	store := newStore(t)
	tasksJSON := `[{"index":0,"title":"Scaffold","files":["main.go"],"description":"Create main.go"}]`
	store.Save(sessionWith("s1", map[string]string{
		builder.KeyPhase:      builder.PhaseCodegen,
		builder.KeyTasks:      tasksJSON,
		builder.KeyActiveTask: "0",
	}))

	cfg := code.Config{SandboxDir: dir}
	a := builder.New(llm, store, cfg, memory.NewProjectStore(), "gemma4:26b")

	resp, err := a.Handle(context.Background(), types.AgentRequest{
		SessionID: "s1",
		UserID:    "test",
		History:   []types.ConversationTurn{{Role: "user", Content: "start"}},
		Input:     "start",
	})
	if err != nil {
		t.Fatalf("Handle() error: %v", err)
	}

	// File should have been written to the sandbox.
	got, err := os.ReadFile(dir + "/main.go")
	if err != nil {
		t.Fatalf("file_write not executed: %v", err)
	}
	if string(got) != fileContent {
		t.Errorf("file content = %q, want %q", got, fileContent)
	}

	// Active task should have advanced.
	if resp.Metadata[builder.KeyActiveTask] != "1" {
		t.Errorf("active_task = %q, want 1", resp.Metadata[builder.KeyActiveTask])
	}
}

func TestMetaBlockStrippedFromOutput(t *testing.T) {
	llm := &seqLLM{responses: []costguard.CompletionResponse{
		textReply("Here is my answer.\n<builder_meta>{\"builder.phase\":\"spec\"}</builder_meta>"),
	}}

	a := newAgent(t, llm, newStore(t))
	resp := handle(t, a, "s1", "hello", nil)

	if strings.Contains(resp.Output, "<builder_meta>") {
		t.Error("<builder_meta> block was not stripped from output")
	}
	if strings.Contains(resp.Output, "</builder_meta>") {
		t.Error("</builder_meta> tag was not stripped from output")
	}
	if !strings.Contains(resp.Output, "Here is my answer") {
		t.Error("visible content was incorrectly removed")
	}
}

func TestMetaBlockMalformedJSON_ReturnsRaw(t *testing.T) {
	// Malformed JSON in metadata block → return the raw output unchanged.
	raw := "Here is my answer.\n<builder_meta>{bad json}</builder_meta>"
	llm := &seqLLM{responses: []costguard.CompletionResponse{textReply(raw)}}

	a := newAgent(t, llm, newStore(t))
	resp := handle(t, a, "s1", "hello", nil)

	if resp.Output != raw {
		t.Errorf("got %q, want raw output on parse error", resp.Output)
	}
}

func TestSessionMetadataPersistedAcrossTurns(t *testing.T) {
	// First turn: advance to spec. Second turn: spec prompt should be used.
	var promptsReceived []string
	llm := &recordingLLM{
		responses: []costguard.CompletionResponse{
			textReply("Questions...\n<builder_meta>{\"builder.phase\":\"spec\"}</builder_meta>"),
			textReply("Here is the spec."),
		},
		onCall: func(msgs []types.ConversationTurn) {
			if len(msgs) > 0 {
				promptsReceived = append(promptsReceived, msgs[0].Content)
			}
		},
	}

	store := newStore(t)
	a := newAgent(t, llm, store)

	// Turn 1: requirements phase.
	handle(t, a, "s1", "Build me a padel app", nil)

	// Turn 2: phase should have advanced to spec.
	handle(t, a, "s1", "yes", nil)

	if len(promptsReceived) < 2 {
		t.Fatalf("expected 2 LLM calls, got %d", len(promptsReceived))
	}
	if !strings.Contains(promptsReceived[1], "SPEC") {
		t.Errorf("second call should use spec prompt, got: %s", promptsReceived[1][:200])
	}
}

// ── SubAgentCaller / research_query tests ─────────────────────────────────────

// mockSubCaller is a SubAgentCaller that records calls and returns a fixed reply.
type mockSubCaller struct {
	calls  []subCall
	result string
	err    error
}

type subCall struct {
	agentID string
	prompt  string
}

func (m *mockSubCaller) Call(_ context.Context, agentID, prompt string) (string, error) {
	m.calls = append(m.calls, subCall{agentID, prompt})
	return m.result, m.err
}

func TestResearchQueryTool_RegisteredWhenSubCallerSet(t *testing.T) {
	// After SetSubAgentCaller, research_query must appear in the LLM's tool list.
	var capturedTools []string
	llm := &capturingLLM{
		reply: "Let me research that.",
		capture: func(msgs []types.ConversationTurn) {},
	}
	llm2 := &toolCapturingLLM{
		reply: "Done.",
		onCall: func(defs []costguard.ToolDefinition) {
			for _, d := range defs {
				capturedTools = append(capturedTools, d.Name)
			}
		},
	}
	_ = llm
	a := newAgent(t, llm2, newStore(t))
	a.SetSubAgentCaller(&mockSubCaller{result: "findings"})

	handle(t, a, "s1", "Build a padel app — research competitors first", nil)

	found := false
	for _, name := range capturedTools {
		if name == "research_query" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("research_query not found in LLM tool definitions; got: %v", capturedTools)
	}
}

func TestResearchQueryTool_NotRegisteredWhenSubCallerNil(t *testing.T) {
	// Without SetSubAgentCaller, research_query must NOT appear in the tool list.
	var capturedTools []string
	llm := &toolCapturingLLM{
		reply: "Done.",
		onCall: func(defs []costguard.ToolDefinition) {
			for _, d := range defs {
				capturedTools = append(capturedTools, d.Name)
			}
		},
	}

	a := newAgent(t, llm, newStore(t))
	// Do NOT call SetSubAgentCaller.
	handle(t, a, "s1", "Build a padel app", nil)

	for _, name := range capturedTools {
		if name == "research_query" {
			t.Error("research_query appeared in tool definitions but SubAgentCaller is nil")
		}
	}
}

func TestResearchQueryTool_InvokesSubCaller(t *testing.T) {
	// When the LLM calls research_query, the SubAgentCaller must be invoked with
	// agentID="research" and the query from the LLM arguments.
	caller := &mockSubCaller{result: "Competitor X charges $10/month."}

	queryArgs, _ := json.Marshal(map[string]string{
		"query": "padel app competitors pricing",
	})
	llm := &seqLLM{responses: []costguard.CompletionResponse{
		// Step 1: call research_query tool
		{ToolCalls: []types.ToolCall{{ID: "rq1", Name: "research_query", Arguments: string(queryArgs)}}},
		// Step 2: final text after seeing tool result
		textReply("Based on research: Competitor X charges $10/month. We'll price at $8."),
	}}

	a := newAgent(t, llm, newStore(t))
	a.SetSubAgentCaller(caller)

	resp := handle(t, a, "s1", "Research competitors for our padel app", nil)

	if len(caller.calls) != 1 {
		t.Fatalf("SubAgentCaller.Call invoked %d times, want 1", len(caller.calls))
	}
	if caller.calls[0].agentID != "research" {
		t.Errorf("agentID = %q, want %q", caller.calls[0].agentID, "research")
	}
	if caller.calls[0].prompt != "padel app competitors pricing" {
		t.Errorf("prompt = %q, want %q", caller.calls[0].prompt, "padel app competitors pricing")
	}
	if !strings.Contains(resp.Output, "Competitor X") {
		t.Errorf("research result not incorporated into output: %s", resp.Output)
	}
}

// toolCapturingLLM captures the ToolDefinitions passed with each Complete call.
type toolCapturingLLM struct {
	reply  string
	onCall func([]costguard.ToolDefinition)
}

func (l *toolCapturingLLM) Complete(_ context.Context, req costguard.CompletionRequest) (costguard.CompletionResponse, error) {
	l.onCall(req.Tools)
	return costguard.CompletionResponse{Content: l.reply}, nil
}

func (l *toolCapturingLLM) Stream(_ context.Context, _ costguard.CompletionRequest) (<-chan costguard.StreamChunk, error) {
	ch := make(chan costguard.StreamChunk)
	close(ch)
	return ch, nil
}

// ── test doubles ──────────────────────────────────────────────────────────────

type capturingLLM struct {
	reply   string
	capture func([]types.ConversationTurn)
}

func (c *capturingLLM) Complete(_ context.Context, req costguard.CompletionRequest) (costguard.CompletionResponse, error) {
	c.capture(req.Messages)
	return costguard.CompletionResponse{Content: c.reply}, nil
}

func (c *capturingLLM) Stream(_ context.Context, _ costguard.CompletionRequest) (<-chan costguard.StreamChunk, error) {
	ch := make(chan costguard.StreamChunk)
	close(ch)
	return ch, nil
}

type recordingLLM struct {
	responses []costguard.CompletionResponse
	idx       int
	onCall    func([]types.ConversationTurn)
}

func (r *recordingLLM) Complete(_ context.Context, req costguard.CompletionRequest) (costguard.CompletionResponse, error) {
	r.onCall(req.Messages)
	resp := r.responses[r.idx]
	if r.idx < len(r.responses)-1 {
		r.idx++
	}
	return resp, nil
}

func (r *recordingLLM) Stream(_ context.Context, _ costguard.CompletionRequest) (<-chan costguard.StreamChunk, error) {
	ch := make(chan costguard.StreamChunk)
	close(ch)
	return ch, nil
}

// ── helpers ───────────────────────────────────────────────────────────────────

func sessionWith(id string, meta map[string]string) *sessions.Session {
	return &sessions.Session{ID: id, UserID: "test", Metadata: meta}
}

func escapeJSON(s string) string {
	b, _ := json.Marshal(s)
	// Remove surrounding quotes — we're embedding in a JSON string value already.
	return string(b[1 : len(b)-1])
}
