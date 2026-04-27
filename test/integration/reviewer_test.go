package integration

import (
	"net/http"
	"strings"
	"testing"

	"github.com/marcoantonios1/Agent-OS/internal/costguard"
	"github.com/marcoantonios1/Agent-OS/internal/types"
)

// TestReviewer_ApprovedVerdictAdvancesToDone verifies the full chain:
//
//  1. Classifier → builder
//  2. Builder (codegen phase) → emits request_reviewer:true in meta block
//  3. Builder calls SubAgentCaller.Call("reviewer", prompt)
//  4. Reviewer LLM → returns APPROVED verdict (no tool calls for simplicity)
//  5. Builder receives verdict, sets phase=done, returns ✅ message
func TestReviewer_ApprovedVerdictAdvancesToDone(t *testing.T) {
	stack := newStack(stackConfig{
		llmResponses: []costguard.CompletionResponse{
			// 1. Classifier
			classifyResp("builder"),
			// 2. Builder LLM: signal codegen is done, request reviewer
			textResp(`All tasks complete. Requesting code review.` +
				"\n<builder_meta>{\"builder.phase\":\"review\",\"request_reviewer\":\"true\"}</builder_meta>"),
			// 3. Reviewer LLM: returns verdict directly (no tool calls)
			textResp("### Files Reviewed\n- main.go — looks good\n\n### Test Results\nok\n\n### Issues Found\nNone\n\n### Suggestions\nNone\n\n### Verdict: APPROVED"),
		},
	})
	defer stack.Close()

	resp, body, err := stack.post(chatRequest{
		SessionID: "reviewer-approved-1",
		UserID:    "marco",
		Text:      "all tasks are done, review the code",
	})
	if err != nil {
		t.Fatalf("POST /v1/chat: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if !strings.Contains(body.Text, "APPROVED") {
		t.Errorf("expected APPROVED in response; got: %s", body.Text)
	}
	if !strings.Contains(body.Text, "✅") {
		t.Errorf("expected ✅ in response; got: %s", body.Text)
	}
}

// TestReviewer_NeedsWorkReturnsToCodegen verifies that a NEEDS_WORK verdict
// sends the builder back to codegen and surfaces the review report.
func TestReviewer_NeedsWorkReturnsToCodegen(t *testing.T) {
	stack := newStack(stackConfig{
		llmResponses: []costguard.CompletionResponse{
			classifyResp("builder"),
			textResp(`Wrapping up codegen.` +
				"\n<builder_meta>{\"builder.phase\":\"review\",\"request_reviewer\":\"true\"}</builder_meta>"),
			textResp("### Files Reviewed\n- main.go\n\n### Test Results\nFAIL: TestFoo\n\n### Issues Found\n- main.go:42 unhandled error\n\n### Suggestions\n- Add error check\n\n### Verdict: NEEDS_WORK"),
		},
	})
	defer stack.Close()

	resp, body, err := stack.post(chatRequest{
		SessionID: "reviewer-needs-work-1",
		UserID:    "marco",
		Text:      "done coding, review please",
	})
	if err != nil {
		t.Fatalf("POST /v1/chat: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if !strings.Contains(body.Text, "NEEDS_WORK") {
		t.Errorf("expected NEEDS_WORK in response; got: %s", body.Text)
	}
	if !strings.Contains(body.Text, "🔄") {
		t.Errorf("expected 🔄 in response; got: %s", body.Text)
	}
}

// TestReviewer_BlockedStaysInReview verifies that a BLOCKED verdict surfaces
// the problem to the user without advancing or regressing the phase.
func TestReviewer_BlockedStaysInReview(t *testing.T) {
	stack := newStack(stackConfig{
		llmResponses: []costguard.CompletionResponse{
			classifyResp("builder"),
			textResp(`Requesting review.` +
				"\n<builder_meta>{\"builder.phase\":\"review\",\"request_reviewer\":\"true\"}</builder_meta>"),
			textResp("### Verdict: BLOCKED\n\nThe architecture is fundamentally broken."),
		},
	})
	defer stack.Close()

	resp, body, err := stack.post(chatRequest{
		SessionID: "reviewer-blocked-1",
		UserID:    "marco",
		Text:      "review the code",
	})
	if err != nil {
		t.Fatalf("POST /v1/chat: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if !strings.Contains(body.Text, "BLOCKED") {
		t.Errorf("expected BLOCKED in response; got: %s", body.Text)
	}
	if !strings.Contains(body.Text, "🚫") {
		t.Errorf("expected 🚫 in response; got: %s", body.Text)
	}
}

// TestReviewer_ToolCallsExecuted verifies that when the reviewer LLM calls
// file_list and shell_run, those tools are actually executed before the verdict.
func TestReviewer_ToolCallsExecuted(t *testing.T) {
	stack := newStack(stackConfig{
		llmResponses: []costguard.CompletionResponse{
			// 1. Classifier
			classifyResp("builder"),
			// 2. Builder requests review
			textResp("done\n<builder_meta>{\"builder.phase\":\"review\",\"request_reviewer\":\"true\"}</builder_meta>"),
			// 3. Reviewer step 1: call file_list
			{ToolCalls: []types.ToolCall{{ID: "fl1", Name: "file_list", Arguments: `{}`}}},
			// 4. Reviewer step 2: call shell_run
			{ToolCalls: []types.ToolCall{{ID: "sr1", Name: "shell_run", Arguments: `{"command":"echo ok"}`}}},
			// 5. Reviewer final verdict
			textResp("### Verdict: APPROVED\n\nAll checks pass."),
		},
		sandboxDir: t.TempDir(),
	})
	defer stack.Close()

	resp, body, err := stack.post(chatRequest{
		SessionID: "reviewer-tools-1",
		UserID:    "marco",
		Text:      "review code",
	})
	if err != nil {
		t.Fatalf("POST /v1/chat: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if !strings.Contains(body.Text, "APPROVED") {
		t.Errorf("expected APPROVED in response; got: %s", body.Text)
	}
}
