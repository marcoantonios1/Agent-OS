package integration

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/marcoantonios1/Agent-OS/internal/costguard"
	"github.com/marcoantonios1/Agent-OS/internal/tools/email"
)

// ── Workflow A: Comms — check emails and draft a reply ─────────────────────────

func TestWorkflowA_CommsEmailDraft(t *testing.T) {
	// Seed the mock email provider with one inbox message.
	now := time.Now()
	inbox := []email.EmailSummary{
		{ID: "msg-1", From: "alice@example.com", Subject: "Meeting tomorrow", Date: now, Snippet: "Are you free tomorrow at 2pm?"},
	}
	emailProv := newMockEmail(inbox, nil)

	// Script LLM responses in call order:
	//   1. Classifier → comms
	//   2. Comms agent step 1 → call email_list
	//   3. Comms agent step 2 → call email_draft
	//   4. Comms agent step 3 → final text
	stack := newStack(stackConfig{
		llmResponses: []costguard.CompletionResponse{
			classifyResp("comms"),
			toolCallResp("tc-1", "email_list", `{"limit":5}`),
			toolCallResp("tc-2", "email_draft", `{"to":"alice@example.com","subject":"Re: Meeting tomorrow","body":"Hi Alice, yes I am free tomorrow at 2pm. See you then!"}`),
			textResp("I've checked your inbox and found 1 email from Alice about tomorrow's meeting. I've drafted a reply — no email has been sent yet."),
		},
		emailProv: emailProv,
	})
	defer stack.Close()

	resp, body, err := stack.post(chatRequest{
		SessionID: "wf-a-1",
		UserID:    "user-1",
		Text:      "Check my emails and draft a reply to the first one",
	})
	if err != nil {
		t.Fatalf("POST /v1/chat: %v", err)
	}

	// Status must be 200.
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}

	// Response must mention the draft.
	if !strings.Contains(body.Text, "draft") {
		t.Errorf("response should mention draft, got: %q", body.Text)
	}

	// NO email should have been sent.
	if len(emailProv.sentEmails) > 0 {
		t.Errorf("email_send must NOT be called in a draft-only workflow, but sent to: %v", emailProv.sentEmails)
	}

	// The draft should have been recorded by the mock provider.
	emailProv.mu.Lock()
	draftCount := len(emailProv.drafts)
	emailProv.mu.Unlock()
	if draftCount != 1 {
		t.Errorf("expected 1 draft in provider, got %d", draftCount)
	}

	// Session history must have been persisted (user + assistant turns).
	sess, err := stack.store.Get("wf-a-1")
	if err != nil {
		t.Fatalf("session not found after request: %v", err)
	}
	if len(sess.History) < 2 {
		t.Errorf("session history length = %d, want >= 2 (user + assistant)", len(sess.History))
	}
	if sess.History[0].Role != "user" {
		t.Errorf("history[0].Role = %q, want \"user\"", sess.History[0].Role)
	}
	if sess.History[1].Role != "assistant" {
		t.Errorf("history[1].Role = %q, want \"assistant\"", sess.History[1].Role)
	}

	// The classifier + 3 agent steps = 4 LLM calls total.
	if got := stack.llm.callCount(); got != 4 {
		t.Errorf("LLM call count = %d, want 4", got)
	}
}

// TestWorkflowA_MultiTurn verifies that a second request in the same session
// receives the full conversation history from the first turn.
func TestWorkflowA_MultiTurn_HistoryPropagated(t *testing.T) {
	inbox := []email.EmailSummary{
		{ID: "msg-2", From: "bob@example.com", Subject: "Invoice", Date: time.Now(), Snippet: "Please find the invoice attached."},
	}
	emailProv := newMockEmail(inbox, nil)

	// Turn 1: list emails + draft
	// Turn 2: ask a follow-up ("what was that email about?") → text reply only
	stack := newStack(stackConfig{
		llmResponses: []costguard.CompletionResponse{
			// Turn 1
			classifyResp("comms"),
			toolCallResp("tc-1", "email_list", `{"limit":5}`),
			textResp("You have one email from Bob about an invoice."),
			// Turn 2
			classifyResp("comms"),
			textResp("The email from Bob is about an invoice he sent you."),
		},
		emailProv: emailProv,
	})
	defer stack.Close()

	// First turn.
	if _, _, err := stack.post(chatRequest{SessionID: "wf-a-2", UserID: "u1", Text: "Check my emails"}); err != nil {
		t.Fatalf("turn 1: %v", err)
	}

	// Second turn — should see history from turn 1.
	_, body2, err := stack.post(chatRequest{SessionID: "wf-a-2", UserID: "u1", Text: "What was that email about?"})
	if err != nil {
		t.Fatalf("turn 2: %v", err)
	}
	if !strings.Contains(body2.Text, "invoice") {
		t.Errorf("turn 2 response should reference the prior context, got: %q", body2.Text)
	}

	// After two turns the session must have 4 history entries: user+assistant x2.
	sess, err := stack.store.Get("wf-a-2")
	if err != nil {
		t.Fatalf("session not found: %v", err)
	}
	if len(sess.History) != 4 {
		t.Errorf("history length = %d, want 4 after 2 turns", len(sess.History))
	}
}

// ── Workflow B: Builder — clarifying questions for new app ────────────────────

func TestWorkflowB_BuilderClarifyingQuestions(t *testing.T) {
	stack := newStack(stackConfig{
		llmResponses: []costguard.CompletionResponse{
			// Classifier → builder
			classifyResp("builder"),
			// Builder agent (requirements phase) → clarifying questions, no meta block
			textResp("I'd love to help you build a padel matching app! Before I write a spec, I have a few questions:\n\n1. Target platform: iOS, Android, or Web?\n2. Should users create matches directly or browse existing ones?\n3. Any authentication preferences (email, Google, Apple)?\n4. Do you need a built-in chat between matched players?\n5. Any specific design style or brand colours in mind?"),
		},
	})
	defer stack.Close()

	resp, body, err := stack.post(chatRequest{
		SessionID: "wf-b-1",
		UserID:    "user-2",
		Text:      "Build me a padel matching app like Tinder for padel",
	})
	if err != nil {
		t.Fatalf("POST /v1/chat: %v", err)
	}

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}

	// Response should contain clarifying questions (indicated by "?" characters).
	questionCount := strings.Count(body.Text, "?")
	if questionCount < 3 {
		t.Errorf("expected >= 3 clarifying questions in response, found %d question marks in: %q", questionCount, body.Text)
	}

	// The builder agent should be in requirements phase — no phase advancement.
	sess, err := stack.store.Get("wf-b-1")
	if err != nil {
		t.Fatalf("session not found: %v", err)
	}
	// Phase key should either be absent (defaults to requirements) or explicitly "requirements".
	phase := sess.Metadata["builder.phase"]
	if phase != "" && phase != "requirements" {
		t.Errorf("builder.phase = %q, want \"requirements\" or empty (default)", phase)
	}

	// Total LLM calls: 1 (classifier) + 1 (builder agent) = 2.
	if got := stack.llm.callCount(); got != 2 {
		t.Errorf("LLM call count = %d, want 2", got)
	}
}

// ── Workflow C: Mixed — comms + builder in one request ────────────────────────

func TestWorkflowC_MixedCommsAndBuilder(t *testing.T) {
	inbox := []email.EmailSummary{
		{
			ID:      "investor-1",
			From:    "vc@fund.com",
			Subject: "Series A interest",
			Date:    time.Now(),
			Snippet: "We're very interested in your startup...",
		},
	}
	emailProv := newMockEmail(inbox, nil)

	// Call order:
	//   1. Classifier → ["comms", "builder"]
	//   2. Comms agent step 1 → email_list
	//   3. Comms agent step 2 → email_draft (investor reply)
	//   4. Comms agent step 3 → final text for comms section
	//   5. Builder agent step 1 → clarifying questions (in requirements phase)
	stack := newStack(stackConfig{
		llmResponses: []costguard.CompletionResponse{
			classifyResp("comms", "builder"),
			toolCallResp("tc-1", "email_list", `{"limit":5}`),
			toolCallResp("tc-2", "email_draft", `{"to":"vc@fund.com","subject":"Re: Series A interest","body":"Thank you for reaching out! We'd love to schedule a call to discuss further."}`),
			textResp("I've drafted a reply to the investor email from vc@fund.com. No email has been sent — please review and confirm."),
			textResp("Happy to help with the landing page! A few questions to get started:\n1. What is the primary call-to-action?\n2. Do you have existing brand assets (logo, colours)?\n3. Should the page be a single long scroll or multi-section?"),
		},
		emailProv: emailProv,
	})
	defer stack.Close()

	resp, body, err := stack.post(chatRequest{
		SessionID: "wf-c-1",
		UserID:    "user-3",
		Text:      "Reply to this investor email, then continue building the app landing page",
	})
	if err != nil {
		t.Fatalf("POST /v1/chat: %v", err)
	}

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}

	// The merged response must contain output from BOTH agents.
	if !strings.Contains(body.Text, "investor") && !strings.Contains(body.Text, "draft") {
		t.Errorf("response should contain comms output (draft/investor), got: %q", body.Text)
	}
	if !strings.Contains(body.Text, "landing page") && !strings.Contains(body.Text, "questions") && !strings.Contains(body.Text, "?") {
		t.Errorf("response should contain builder output (questions/landing page), got: %q", body.Text)
	}

	// The two agent sections should be separated by the compound response divider.
	if !strings.Contains(body.Text, "---") {
		t.Errorf("compound response must contain --- separator between agent outputs, got: %q", body.Text)
	}

	// Comms section comes before builder section (order matches user request).
	commsIdx := strings.Index(body.Text, "[comms]")
	builderIdx := strings.Index(body.Text, "[builder]")
	if commsIdx < 0 || builderIdx < 0 {
		t.Errorf("expected [comms] and [builder] labels in response, got: %q", body.Text)
	} else if commsIdx >= builderIdx {
		t.Errorf("comms section (idx %d) must come before builder section (idx %d)", commsIdx, builderIdx)
	}

	// No email should have been sent (only drafted).
	if len(emailProv.sentEmails) > 0 {
		t.Errorf("email must not be sent without user confirmation, but sent to: %v", emailProv.sentEmails)
	}

	// Draft must have been created in the provider.
	emailProv.mu.Lock()
	draftCount := len(emailProv.drafts)
	emailProv.mu.Unlock()
	if draftCount != 1 {
		t.Errorf("expected 1 draft, got %d", draftCount)
	}

	// Session history: 1 user + 1 assistant turn.
	sess, err := stack.store.Get("wf-c-1")
	if err != nil {
		t.Fatalf("session not found: %v", err)
	}
	if len(sess.History) < 2 {
		t.Errorf("history length = %d, want >= 2", len(sess.History))
	}

	// Total LLM calls: 1 (classifier) + 3 (comms) + 1 (builder) = 5.
	if got := stack.llm.callCount(); got != 5 {
		t.Errorf("LLM call count = %d, want 5", got)
	}
}

// ── Additional edge-case tests ────────────────────────────────────────────────

// TestWorkflowA_HealthAndReadyz verifies health endpoints still work during
// normal operation (they're not affected by the mocked LLM).
func TestWorkflowA_HealthEndpoints(t *testing.T) {
	stack := newStack(stackConfig{})
	defer stack.Close()

	t.Run("healthz", func(t *testing.T) {
		resp, err := http.Get(stack.srv.URL + "/healthz")
		if err != nil {
			t.Fatalf("GET /healthz: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("status = %d, want 200", resp.StatusCode)
		}
	})

	t.Run("readyz_no_checker", func(t *testing.T) {
		resp, err := http.Get(stack.srv.URL + "/readyz")
		if err != nil {
			t.Fatalf("GET /readyz: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("status = %d, want 200", resp.StatusCode)
		}
	})
}

// TestWorkflowB_SessionMetadataPreservedAcrossTurns verifies that builder phase
// metadata written in one turn is read back correctly in the next.
func TestWorkflowB_PhaseAdvancesToSpec(t *testing.T) {
	stack := newStack(stackConfig{
		llmResponses: []costguard.CompletionResponse{
			// Turn 1: requirements phase, LLM asks clarifying questions
			classifyResp("builder"),
			textResp("Great idea! Let me ask a few things:\n1. Platform?\n2. Features?"),
			// Turn 2: user confirms, LLM advances to spec phase
			classifyResp("builder"),
			textResp("Here is the spec for your app.\n\n# Overview\nA padel matching platform.\n<builder_meta>{\"builder.phase\":\"spec\",\"builder.spec\":\"# Overview\\nA padel matching platform.\"}</builder_meta>"),
		},
	})
	defer stack.Close()

	// Turn 1: vague request → clarifying questions.
	if _, _, err := stack.post(chatRequest{SessionID: "wf-b-2", UserID: "u1", Text: "Build me a padel app"}); err != nil {
		t.Fatalf("turn 1: %v", err)
	}

	// Turn 2: user provides details → LLM advances to spec.
	_, body2, err := stack.post(chatRequest{SessionID: "wf-b-2", UserID: "u1", Text: "iOS app, need matching and chat, no brand yet"})
	if err != nil {
		t.Fatalf("turn 2: %v", err)
	}

	// Spec output should be visible (builder_meta block stripped).
	if !strings.Contains(body2.Text, "spec") && !strings.Contains(body2.Text, "Overview") {
		t.Errorf("turn 2 response should contain spec content, got: %q", body2.Text)
	}
	// builder_meta block must be stripped from visible output.
	if strings.Contains(body2.Text, "<builder_meta>") {
		t.Errorf("builder_meta block must not appear in visible output, got: %q", body2.Text)
	}

	// Phase must have advanced to "spec" in the session store.
	sess, err := stack.store.Get("wf-b-2")
	if err != nil {
		t.Fatalf("session not found: %v", err)
	}
	if got := sess.Metadata["builder.phase"]; got != "spec" {
		t.Errorf("builder.phase = %q after spec turn, want \"spec\"", got)
	}
	if sess.Metadata["builder.spec"] == "" {
		t.Error("builder.spec should be non-empty after spec turn")
	}
}
