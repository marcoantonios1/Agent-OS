package integration

// Phase 3 integration tests covering six net-new capabilities:
//
//  1. WhatsApp session key format and per-JID session isolation.
//  2. Reminder worker fires a due reminder through a Discord-format notifier.
//  3. Builder agent calls the research sub-agent via a mock SubAgentCaller.
//  4. Builder requests reviewer → APPROVED verdict → session phase set to done.
//  5. Autonomous builder runs 3 tasks, emits 3 progress notifications, reaches done.
//  6. MultiProvider email merges and date-sorts results from two mock inboxes.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marcoantonios1/Agent-OS/internal/agents/builder"
	"github.com/marcoantonios1/Agent-OS/internal/costguard"
	"github.com/marcoantonios1/Agent-OS/internal/memory"
	"github.com/marcoantonios1/Agent-OS/internal/sessions"
	"github.com/marcoantonios1/Agent-OS/internal/tools/code"
	"github.com/marcoantonios1/Agent-OS/internal/tools/email"
	toolreminder "github.com/marcoantonios1/Agent-OS/internal/tools/reminder"
	"github.com/marcoantonios1/Agent-OS/internal/types"
)

// ── shared mocks ──────────────────────────────────────────────────────────────

type recordingNotifier struct {
	mu    sync.Mutex
	calls []*sessions.Reminder
}

func (n *recordingNotifier) NotifyReminder(_ context.Context, r *sessions.Reminder) error {
	n.mu.Lock()
	cp := *r
	n.calls = append(n.calls, &cp)
	n.mu.Unlock()
	return nil
}

type recordingProgressNotifier struct {
	mu    sync.Mutex
	calls []string // the text of each NotifyProgress call
}

func (n *recordingProgressNotifier) NotifyProgress(_ context.Context, _, _, text string) error {
	n.mu.Lock()
	n.calls = append(n.calls, text)
	n.mu.Unlock()
	return nil
}

type mockSubAgentCaller struct {
	mu     sync.Mutex
	calls  []subCall
	result string
}

type subCall struct {
	agentID string
	prompt  string
}

func (m *mockSubAgentCaller) Call(_ context.Context, agentID, prompt string) (string, error) {
	m.mu.Lock()
	m.calls = append(m.calls, subCall{agentID, prompt})
	m.mu.Unlock()
	return m.result, nil
}

// ── Test 1: WhatsApp session key format and isolation ─────────────────────────

// TestPhase3_WhatsApp_SessionKey verifies that the WhatsApp channel uses session
// IDs in the format whatsapp:{jid} and that two different JIDs get completely
// isolated session histories — neither can see the other's messages.
func TestPhase3_WhatsApp_SessionKey(t *testing.T) {
	stack := newStack(stackConfig{
		llmResponses: []costguard.CompletionResponse{
			// JID-1 request
			classifyResp("comms"),
			textResp("Hello from JID-1"),
			// JID-2 request
			classifyResp("comms"),
			textResp("Hello from JID-2"),
			// JID-1 second request — must NOT see JID-2 history
			classifyResp("comms"),
			textResp("Second reply to JID-1"),
		},
		emailProv: newMockEmail(nil, nil),
	})
	defer stack.Close()

	jid1 := "96170000001@s.whatsapp.net"
	jid2 := "96170000002@s.whatsapp.net"
	sid1 := "whatsapp:" + jid1
	sid2 := "whatsapp:" + jid2

	// Request from JID-1.
	resp1, body1, err := stack.post(chatRequest{SessionID: sid1, UserID: jid1, Text: "hi from jid1"})
	if err != nil {
		t.Fatalf("JID-1 POST: %v", err)
	}
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("JID-1 status = %d, want 200", resp1.StatusCode)
	}
	if !strings.Contains(body1.Text, "JID-1") {
		t.Errorf("JID-1 response = %q, want mention of JID-1", body1.Text)
	}

	// Request from JID-2 — different session key.
	resp2, body2, err := stack.post(chatRequest{SessionID: sid2, UserID: jid2, Text: "hi from jid2"})
	if err != nil {
		t.Fatalf("JID-2 POST: %v", err)
	}
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("JID-2 status = %d, want 200", resp2.StatusCode)
	}
	if !strings.Contains(body2.Text, "JID-2") {
		t.Errorf("JID-2 response = %q, want mention of JID-2", body2.Text)
	}

	// Verify sessions are stored under whatsapp:{jid} keys.
	sess1, err := stack.store.Get(sid1)
	if err != nil {
		t.Fatalf("session %q not found: %v", sid1, err)
	}
	sess2, err := stack.store.Get(sid2)
	if err != nil {
		t.Fatalf("session %q not found: %v", sid2, err)
	}

	// Session key format check.
	if sess1.ID != sid1 {
		t.Errorf("session1.ID = %q, want %q", sess1.ID, sid1)
	}
	if sess2.ID != sid2 {
		t.Errorf("session2.ID = %q, want %q", sess2.ID, sid2)
	}

	// Isolation check: JID-1's history must not contain anything from JID-2.
	for _, turn := range sess1.History {
		if strings.Contains(turn.Content, "jid2") || strings.Contains(turn.Content, "JID-2") {
			t.Errorf("JID-1 session contains JID-2 content: %q", turn.Content)
		}
	}

	// Second request for JID-1 must still go to the same isolated session.
	resp3, _, err := stack.post(chatRequest{SessionID: sid1, UserID: jid1, Text: "second message from jid1"})
	if err != nil {
		t.Fatalf("JID-1 second POST: %v", err)
	}
	if resp3.StatusCode != http.StatusOK {
		t.Fatalf("JID-1 second status = %d, want 200", resp3.StatusCode)
	}

	sess1After, _ := stack.store.Get(sid1)
	sess2After, _ := stack.store.Get(sid2)
	if len(sess1After.History) <= len(sess1.History) {
		t.Errorf("JID-1 history did not grow after second request")
	}
	if len(sess2After.History) != len(sess2.History) {
		t.Errorf("JID-2 history grew unexpectedly after JID-1 second request")
	}
}

// ── Test 2: Reminder delivery through a Discord-format notifier ───────────────

// TestPhase3_ReminderDelivery_Discord creates a due reminder with a Discord
// session ID, ticks the worker via FireNow, and verifies that NotifyReminder is
// called on the registered notifier with the exact reminder message.
func TestPhase3_ReminderDelivery_Discord(t *testing.T) {
	store := memory.NewReminderStore()
	r := &sessions.Reminder{
		ID:        "rem-discord-1",
		UserID:    "u1",
		SessionID: "discord:dm:ch-999:u1",
		ChannelID: "discord",
		Message:   "Follow up with Alice about the invoice",
		FireAt:    time.Now().Add(-time.Minute), // already due
		CreatedAt: time.Now().Add(-2 * time.Minute),
	}
	if err := store.Save(r); err != nil {
		t.Fatalf("Save reminder: %v", err)
	}

	notifier := &recordingNotifier{}
	w := toolreminder.NewWorker(store)
	w.AddNotifier(notifier)

	w.FireNow(context.Background(), time.Now())

	notifier.mu.Lock()
	calls := notifier.calls
	notifier.mu.Unlock()

	if len(calls) != 1 {
		t.Fatalf("NotifyReminder call count = %d, want 1", len(calls))
	}
	if calls[0].ID != r.ID {
		t.Errorf("reminder ID = %q, want %q", calls[0].ID, r.ID)
	}
	if calls[0].Message != r.Message {
		t.Errorf("message = %q, want %q", calls[0].Message, r.Message)
	}
	if calls[0].SessionID != r.SessionID {
		t.Errorf("SessionID = %q, want %q", calls[0].SessionID, r.SessionID)
	}

	// Verify the reminder was atomically removed — a second tick should fire nothing.
	notifier.mu.Lock()
	notifier.calls = nil
	notifier.mu.Unlock()

	w.FireNow(context.Background(), time.Now())

	notifier.mu.Lock()
	afterCalls := notifier.calls
	notifier.mu.Unlock()
	if len(afterCalls) != 0 {
		t.Errorf("reminder fired twice — store should have removed it after first fire")
	}
}

// ── Test 3: Builder → research sub-agent call via mock SubAgentCaller ─────────

// TestPhase3_SubAgentCall_BuilderToResearch verifies that when the Builder LLM
// emits a research_query tool call, the SubAgentCaller.Call() is invoked with
// agentID="research" and the exact query string the LLM requested.
func TestPhase3_SubAgentCall_BuilderToResearch(t *testing.T) {
	const query = "padel app market size 2025"

	queryArgs, _ := json.Marshal(map[string]string{"query": query})

	llm := newScriptedLLM(
		// Step 1: builder emits research_query tool call.
		costguard.CompletionResponse{
			ToolCalls: []types.ToolCall{
				{ID: "rq-1", Name: "research_query", Arguments: string(queryArgs)},
			},
		},
		// Step 2: builder receives tool result, returns final text.
		textResp("Based on the research, the padel market is worth $2B. Here is my plan."),
	)

	caller := &mockSubAgentCaller{result: "Padel app market is valued at $2 billion globally."}

	store := memory.NewStore()
	defer store.Close()

	cfg := code.Config{SandboxDir: t.TempDir()}
	agent := builder.New(llm, store, cfg, memory.NewProjectStore(), "gemma4:26b")
	agent.SetSubAgentCaller(caller)

	resp, err := agent.Handle(context.Background(), types.AgentRequest{
		SessionID: "sub-agent-direct-1",
		UserID:    "user-1",
		Input:     "research the padel market then plan the app",
		Metadata:  map[string]string{},
	})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if resp.Output == "" {
		t.Fatal("expected non-empty response")
	}

	caller.mu.Lock()
	calls := caller.calls
	caller.mu.Unlock()

	if len(calls) == 0 {
		t.Fatal("SubAgentCaller.Call was never invoked — research_query tool not executed")
	}
	if calls[0].agentID != "research" {
		t.Errorf("agentID = %q, want %q", calls[0].agentID, "research")
	}
	if calls[0].prompt != query {
		t.Errorf("prompt = %q, want %q", calls[0].prompt, query)
	}
}

// ── Test 4: Reviewer APPROVED verdict → session phase set to done ─────────────

// TestPhase3_ReviewerAgent_ApprovedVerdict verifies the full chain:
// Builder requests review → Reviewer LLM returns APPROVED → response contains ✅
// and the session's builder.phase metadata is advanced to "done".
func TestPhase3_ReviewerAgent_ApprovedVerdict(t *testing.T) {
	stack := newStack(stackConfig{
		llmResponses: []costguard.CompletionResponse{
			// 1. Classifier → builder
			classifyResp("builder"),
			// 2. Builder: signal review requested
			textResp("All tasks complete.\n<builder_meta>{\"builder.phase\":\"review\",\"request_reviewer\":\"true\"}</builder_meta>"),
			// 3. Reviewer LLM: APPROVED verdict
			textResp("### Files Reviewed\n- main.go\n\n### Test Results\nAll pass.\n\n### Issues Found\nNone.\n\n### Suggestions\nNone.\n\n### Verdict: APPROVED"),
		},
		sandboxDir: t.TempDir(),
	})
	defer stack.Close()

	resp, body, err := stack.post(chatRequest{
		SessionID: "p3-reviewer-approved-1",
		UserID:    "marco",
		Text:      "all tasks done, review please",
	})
	if err != nil {
		t.Fatalf("POST /v1/chat: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if !strings.Contains(body.Text, "APPROVED") {
		t.Errorf("response missing APPROVED; got: %s", body.Text)
	}
	if !strings.Contains(body.Text, "✅") {
		t.Errorf("response missing ✅; got: %s", body.Text)
	}

	// Session phase must be "done" after an APPROVED verdict.
	sess, err := stack.store.Get("p3-reviewer-approved-1")
	if err != nil {
		t.Fatalf("session not found: %v", err)
	}
	if got := sess.Metadata[builder.KeyPhase]; got != builder.PhaseDone {
		t.Errorf("builder.phase = %q, want %q", got, builder.PhaseDone)
	}
}

// ── Test 5: Autonomous builder — 3 tasks, 3 progress notifications ────────────

// TestPhase3_AutonomousBuilder_AllTasksComplete places the builder in tasks phase
// with three tasks pre-loaded, sends "run autonomously", and verifies that:
//   - The agent runs all three tasks autonomously without further user input.
//   - Three progress notifications are sent (one per task completion).
//   - The final phase in session metadata is "done".
func TestPhase3_AutonomousBuilder_AllTasksComplete(t *testing.T) {
	tasksJSON := `[{"index":0,"title":"Set up project"},{"index":1,"title":"Write core logic"},{"index":2,"title":"Add tests"}]`

	notifier := &recordingProgressNotifier{}

	stack := newStack(stackConfig{
		llmResponses: []costguard.CompletionResponse{
			// Request 1: classifier + builder (advances to tasks phase with task list).
			classifyResp("builder"),
			textResp(fmt.Sprintf(
				"Great plan! Here are the 3 tasks.\n<builder_meta>{\"builder.phase\":\"tasks\",\"builder.tasks\":%s}</builder_meta>",
				tasksJSON,
			)),
			// Request 2: classifier receives "run autonomously".
			classifyResp("builder"),
			// Autonomous iter 0: completes task 0 → active_task advances to 1.
			textResp("Task 1 done: project scaffolded.\n<builder_meta>{\"builder.active_task\":\"1\"}</builder_meta>"),
			// Autonomous iter 1: completes task 1 → active_task advances to 2.
			textResp("Task 2 done: core logic written.\n<builder_meta>{\"builder.active_task\":\"2\"}</builder_meta>"),
			// Autonomous iter 2: completes task 2 → active_task=3 and phase=done.
			textResp("Task 3 done: tests passing.\n<builder_meta>{\"builder.active_task\":\"3\",\"builder.phase\":\"done\"}</builder_meta>"),
		},
		builderNotifier: notifier,
		sandboxDir:      t.TempDir(),
	})
	defer stack.Close()

	// Request 1: establish the task list.
	_, _, err := stack.post(chatRequest{
		SessionID: "p3-autonomous-1",
		UserID:    "marco",
		Text:      "Build me a padel app",
	})
	if err != nil {
		t.Fatalf("setup POST: %v", err)
	}

	// Request 2: trigger autonomous execution.
	resp, body, err := stack.post(chatRequest{
		SessionID: "p3-autonomous-1",
		UserID:    "marco",
		Text:      "run autonomously",
	})
	if err != nil {
		t.Fatalf("autonomous POST: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if body.Text == "" {
		t.Fatal("expected non-empty response from autonomous run")
	}

	// Three progress notifications must have fired — one per completed task.
	notifier.mu.Lock()
	notifications := make([]string, len(notifier.calls))
	copy(notifications, notifier.calls)
	notifier.mu.Unlock()

	if len(notifications) != 3 {
		t.Fatalf("progress notification count = %d, want 3; got: %v", len(notifications), notifications)
	}
	for i, n := range notifications {
		wantPrefix := fmt.Sprintf("✅ Task %d/3", i+1)
		if !strings.HasPrefix(n, wantPrefix) {
			t.Errorf("notification[%d] = %q, want prefix %q", i, n, wantPrefix)
		}
	}

	// Session phase must be "done".
	sess, err := stack.store.Get("p3-autonomous-1")
	if err != nil {
		t.Fatalf("session not found: %v", err)
	}
	if got := sess.Metadata[builder.KeyPhase]; got != builder.PhaseDone {
		t.Errorf("builder.phase = %q, want %q", got, builder.PhaseDone)
	}
}

// ── Test 6: MultiProvider email — merged list sorted by date ──────────────────

// TestPhase3_MultiProviderEmail_MergedList creates a MultiProvider wrapping two
// mock inboxes with interleaved timestamps, calls email_list through the tool
// layer, and verifies the merged result is ordered newest-first with no duplicates.
func TestPhase3_MultiProviderEmail_MergedList(t *testing.T) {
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	age := func(d time.Duration) time.Time { return base.Add(-d) }

	// Provider A has emails at 1h and 3h ago.
	provA := newMockEmail([]email.EmailSummary{
		{ID: "a1", From: "alice@example.com", Subject: "Invoice", Date: age(1 * time.Hour)},
		{ID: "a2", From: "alice@example.com", Subject: "Follow up", Date: age(3 * time.Hour)},
	}, nil)

	// Provider B has emails at 2h and 4h ago.
	provB := newMockEmail([]email.EmailSummary{
		{ID: "b1", From: "bob@example.com", Subject: "Project update", Date: age(2 * time.Hour)},
		{ID: "b2", From: "bob@example.com", Subject: "Meeting notes", Date: age(4 * time.Hour)},
	}, nil)

	// Both providers registered; provA is the primary (writes go there).
	multi := email.NewMultiProvider(provA, provA, provB)
	tool := email.NewListTool(multi)

	argsJSON, _ := json.Marshal(map[string]int{"limit": 10})
	result, err := tool.Execute(context.Background(), argsJSON)
	if err != nil {
		t.Fatalf("email_list Execute: %v", err)
	}
	if result == "" {
		t.Fatal("expected non-empty result from email_list")
	}

	var summaries []email.EmailSummary
	if err := json.Unmarshal([]byte(result), &summaries); err != nil {
		t.Fatalf("unmarshal result: %v\nraw: %s", err, result)
	}

	// All four emails must appear exactly once.
	if len(summaries) != 4 {
		t.Fatalf("result count = %d, want 4; got IDs: %v", len(summaries), emailIDs(summaries))
	}

	// No duplicates.
	seen := make(map[string]int, 4)
	for _, s := range summaries {
		seen[s.ID]++
	}
	for id, count := range seen {
		if count > 1 {
			t.Errorf("duplicate email ID %q appears %d times", id, count)
		}
	}

	// Sorted newest-first: a1, b1, a2, b2.
	wantOrder := []string{"a1", "b1", "a2", "b2"}
	for i, want := range wantOrder {
		if summaries[i].ID != want {
			t.Errorf("summaries[%d].ID = %q, want %q (full order: %v)", i, summaries[i].ID, want, emailIDs(summaries))
		}
	}

	// Verify provA is primary: Draft goes only to provA.
	if _, err := multi.Draft(context.Background(), "x@y.com", "Test", "body"); err != nil {
		t.Fatalf("Draft: %v", err)
	}
	provA.mu.Lock()
	draftCount := len(provA.drafts)
	provA.mu.Unlock()
	provB.mu.Lock()
	bDraftCount := len(provB.drafts)
	provB.mu.Unlock()

	if draftCount != 1 {
		t.Errorf("provA.drafts = %d, want 1 (primary should receive draft)", draftCount)
	}
	if bDraftCount != 0 {
		t.Errorf("provB.drafts = %d, want 0 (secondary must not receive draft)", bDraftCount)
	}
}

// emailIDs is a test helper that extracts just the IDs for readable error messages.
func emailIDs(ss []email.EmailSummary) []string {
	ids := make([]string, len(ss))
	for i, s := range ids {
		_ = s
		ids[i] = ss[i].ID
	}
	return ids
}
