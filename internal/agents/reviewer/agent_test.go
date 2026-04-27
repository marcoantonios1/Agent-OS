package reviewer_test

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/marcoantonios1/Agent-OS/internal/agents/reviewer"
	"github.com/marcoantonios1/Agent-OS/internal/costguard"
	"github.com/marcoantonios1/Agent-OS/internal/tools/code"
	"github.com/marcoantonios1/Agent-OS/internal/types"
)

// ── mock LLM ──────────────────────────────────────────────────────────────────

type seqLLM struct {
	responses []costguard.CompletionResponse
	idx       int
	calls     []costguard.CompletionRequest
}

func (m *seqLLM) Complete(_ context.Context, req costguard.CompletionRequest) (costguard.CompletionResponse, error) {
	m.calls = append(m.calls, req)
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

func text(s string) costguard.CompletionResponse { return costguard.CompletionResponse{Content: s} }

func toolCall(id, name string, args any) costguard.CompletionResponse {
	b, _ := json.Marshal(args)
	return costguard.CompletionResponse{
		ToolCalls: []types.ToolCall{{ID: id, Name: name, Arguments: string(b)}},
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────

func newAgent(llm costguard.LLMClient, dir string) *reviewer.Agent {
	return reviewer.New(llm, "test-model", code.Config{SandboxDir: dir})
}

func handle(t *testing.T, a *reviewer.Agent, prompt string) types.AgentResponse {
	t.Helper()
	resp, err := a.Handle(context.Background(), types.AgentRequest{Input: prompt})
	if err != nil {
		t.Fatalf("Handle() error: %v", err)
	}
	return resp
}

// ── tests ─────────────────────────────────────────────────────────────────────

func TestReviewer_SystemPromptFirst(t *testing.T) {
	llm := &seqLLM{responses: []costguard.CompletionResponse{
		text("### Verdict: APPROVED"),
	}}
	a := newAgent(llm, t.TempDir())
	handle(t, a, "review this")

	if len(llm.calls) == 0 {
		t.Fatal("no LLM calls made")
	}
	msgs := llm.calls[0].Messages
	if len(msgs) == 0 || msgs[0].Role != "system" {
		t.Error("first message must be the system prompt")
	}
	if !strings.Contains(msgs[0].Content, "Reviewer Agent") {
		t.Error("system prompt does not mention Reviewer Agent")
	}
}

func TestReviewer_InputPassedAsUserMessage(t *testing.T) {
	llm := &seqLLM{responses: []costguard.CompletionResponse{
		text("### Verdict: APPROVED"),
	}}
	a := newAgent(llm, t.TempDir())
	handle(t, a, "check the padel app code")

	msgs := llm.calls[0].Messages
	last := msgs[len(msgs)-1]
	if last.Role != "user" || last.Content != "check the padel app code" {
		t.Errorf("last message = {%q, %q}, want {user, check the padel app code}", last.Role, last.Content)
	}
}

func TestReviewer_FileListToolRegistered(t *testing.T) {
	llm := &seqLLM{responses: []costguard.CompletionResponse{
		text("### Verdict: APPROVED"),
	}}
	a := newAgent(llm, t.TempDir())
	handle(t, a, "review")

	var names []string
	for _, td := range llm.calls[0].Tools {
		names = append(names, td.Name)
	}
	for _, want := range []string{"file_list", "file_read", "shell_run"} {
		found := false
		for _, n := range names {
			if n == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("tool %q not registered; got %v", want, names)
		}
	}
}

func TestReviewer_FileWriteNotRegistered(t *testing.T) {
	llm := &seqLLM{responses: []costguard.CompletionResponse{
		text("### Verdict: APPROVED"),
	}}
	a := newAgent(llm, t.TempDir())
	handle(t, a, "review")

	for _, td := range llm.calls[0].Tools {
		if td.Name == "file_write" {
			t.Error("file_write must not be registered in the reviewer (read-only agent)")
		}
	}
}

func TestReviewer_ExecutesFileReadTool(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/main.go", []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	llm := &seqLLM{responses: []costguard.CompletionResponse{
		toolCall("c1", "file_read", map[string]string{"path": "main.go"}),
		text("### Verdict: APPROVED"),
	}}
	a := newAgent(llm, dir)
	resp := handle(t, a, "review")

	if !strings.Contains(resp.Output, "APPROVED") {
		t.Errorf("expected APPROVED in output; got: %s", resp.Output)
	}
}

func TestReviewer_ExecutesShellRunTool(t *testing.T) {
	llm := &seqLLM{responses: []costguard.CompletionResponse{
		toolCall("c1", "shell_run", map[string]string{"command": "echo tests_ok"}),
		text("### Verdict: APPROVED\n\nAll tests passed."),
	}}
	a := newAgent(llm, t.TempDir())
	resp := handle(t, a, "review")

	if !strings.Contains(resp.Output, "APPROVED") {
		t.Errorf("expected APPROVED in output; got: %s", resp.Output)
	}
}

func TestReviewer_ReturnsVerdictInOutput(t *testing.T) {
	cases := []struct{ verdict, want string }{
		{"### Verdict: APPROVED\n\nAll good.", "APPROVED"},
		{"### Verdict: NEEDS_WORK\n\nFix the tests.", "NEEDS_WORK"},
		{"### Verdict: BLOCKED\n\nArchitecture is broken.", "BLOCKED"},
	}
	for _, tc := range cases {
		llm := &seqLLM{responses: []costguard.CompletionResponse{text(tc.verdict)}}
		a := newAgent(llm, t.TempDir())
		resp := handle(t, a, "review")
		if !strings.Contains(resp.Output, tc.want) {
			t.Errorf("verdict %q: output %q does not contain %q", tc.want, resp.Output, tc.want)
		}
	}
}
