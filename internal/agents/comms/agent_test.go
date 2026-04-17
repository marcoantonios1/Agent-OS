package comms_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/marcoantonios1/Agent-OS/internal/agents/comms"
	"github.com/marcoantonios1/Agent-OS/internal/approval"
	"github.com/marcoantonios1/Agent-OS/internal/costguard"
	"github.com/marcoantonios1/Agent-OS/internal/memory"
	"github.com/marcoantonios1/Agent-OS/internal/tools/calendar"
	"github.com/marcoantonios1/Agent-OS/internal/tools/email"
	"github.com/marcoantonios1/Agent-OS/internal/types"
)

// ── sequenced mock LLM ────────────────────────────────────────────────────────
//
// seqLLM returns pre-defined responses in order. Each test sets up the exact
// sequence of LLM replies it expects (tool calls followed by a final text reply).

type seqLLM struct {
	responses []costguard.CompletionResponse
	idx       int
	calls     []costguard.CompletionRequest
}

func (m *seqLLM) Complete(_ context.Context, req costguard.CompletionRequest) (costguard.CompletionResponse, error) {
	m.calls = append(m.calls, req)
	if m.idx >= len(m.responses) {
		return costguard.CompletionResponse{Content: "[no more responses]"}, nil
	}
	resp := m.responses[m.idx]
	m.idx++
	return resp, nil
}

func (m *seqLLM) Stream(_ context.Context, _ costguard.CompletionRequest) (<-chan costguard.StreamChunk, error) {
	ch := make(chan costguard.StreamChunk)
	close(ch)
	return ch, nil
}

// toolCall builds a CompletionResponse that requests a single tool invocation.
func toolCall(id, name string, args any) costguard.CompletionResponse {
	b, _ := json.Marshal(args)
	return costguard.CompletionResponse{
		ToolCalls: []types.ToolCall{{ID: id, Name: name, Arguments: string(b)}},
	}
}

// textReply builds a CompletionResponse with a plain text answer.
func textReply(text string) costguard.CompletionResponse {
	return costguard.CompletionResponse{Content: text}
}

// ── stub email provider ───────────────────────────────────────────────────────

type stubEmailProvider struct {
	summaries []email.EmailSummary
	emails    map[string]*email.Email
	drafts    []*email.Draft
	sentCount int
}

func newStubEmailProvider() *stubEmailProvider {
	now := time.Now()
	summaries := []email.EmailSummary{
		{ID: "msg-1", From: "alice@example.com", Subject: "Q2 budget review",
			Date: now.Add(-2 * time.Hour), Snippet: "Can you review the attached spreadsheet?"},
		{ID: "msg-2", From: "bob@startup.io", Subject: "Coffee catch-up?",
			Date: now.Add(-5 * time.Hour), Snippet: "Free next week?"},
	}
	emails := map[string]*email.Email{
		"msg-1": {
			ID: "msg-1", From: "alice@example.com", To: []string{"marco@example.com"},
			Subject: "Q2 budget review", Date: now.Add(-2 * time.Hour),
			Body: "Hi Marco,\n\nCan you review the spreadsheet before Friday?\n\nAlice",
		},
	}
	return &stubEmailProvider{summaries: summaries, emails: emails}
}

func (p *stubEmailProvider) List(_ context.Context, limit int) ([]email.EmailSummary, error) {
	if limit > len(p.summaries) {
		limit = len(p.summaries)
	}
	return p.summaries[:limit], nil
}

func (p *stubEmailProvider) Read(_ context.Context, id string) (*email.Email, error) {
	e, ok := p.emails[id]
	if !ok {
		return nil, fmt.Errorf("email %q not found", id)
	}
	return e, nil
}

func (p *stubEmailProvider) Search(_ context.Context, _ string) ([]email.EmailSummary, error) {
	return p.summaries, nil
}

func (p *stubEmailProvider) Draft(_ context.Context, to, subject, body string) (*email.Draft, error) {
	d := &email.Draft{To: to, Subject: subject, Body: body}
	p.drafts = append(p.drafts, d)
	return d, nil
}

func (p *stubEmailProvider) Send(_ context.Context, _, _, _ string) error {
	p.sentCount++
	return nil
}

// ── stub calendar provider ────────────────────────────────────────────────────

type stubCalendarProvider struct {
	events map[string]calendar.Event
}

func newStubCalendarProvider() *stubCalendarProvider {
	tomorrow := time.Now().Add(24 * time.Hour)
	day := time.Date(tomorrow.Year(), tomorrow.Month(), tomorrow.Day(), 0, 0, 0, 0, time.UTC)
	events := map[string]calendar.Event{
		"evt-1": {
			ID:    "evt-1",
			Title: "Team standup",
			Start: day.Add(9 * time.Hour),
			End:   day.Add(9*time.Hour + 30*time.Minute),
		},
		"evt-2": {
			ID:    "evt-2",
			Title: "Product review",
			Start: day.Add(14 * time.Hour),
			End:   day.Add(15 * time.Hour),
		},
	}
	return &stubCalendarProvider{events: events}
}

func (p *stubCalendarProvider) List(_ context.Context, from, to time.Time) ([]calendar.Event, error) {
	var result []calendar.Event
	for _, e := range p.events {
		if !e.Start.Before(from) && e.Start.Before(to) {
			result = append(result, e)
		}
	}
	return result, nil
}

func (p *stubCalendarProvider) Read(_ context.Context, id string) (*calendar.Event, error) {
	e, ok := p.events[id]
	if !ok {
		return nil, fmt.Errorf("event %q not found", id)
	}
	return &e, nil
}

func (p *stubCalendarProvider) Create(_ context.Context, input calendar.CreateEventInput) (*calendar.Event, error) {
	e := calendar.Event{
		ID: fmt.Sprintf("new-%d", time.Now().UnixNano()), Title: input.Title,
		Start: input.Start, End: input.End,
	}
	p.events[e.ID] = e
	return &e, nil
}

func (p *stubCalendarProvider) Update(_ context.Context, input calendar.UpdateEventInput) (*calendar.Event, error) {
	e, ok := p.events[input.EventID]
	if !ok {
		return nil, fmt.Errorf("event %q not found", input.EventID)
	}
	if input.Title != "" {
		e.Title = input.Title
	}
	p.events[e.ID] = e
	return &e, nil
}

// ── helpers ───────────────────────────────────────────────────────────────────

func newRequest(sessionID, input string, history ...types.ConversationTurn) types.AgentRequest {
	h := append(history, types.ConversationTurn{Role: "user", Content: input})
	return types.AgentRequest{
		SessionID: sessionID,
		UserID:    "user-1",
		Intent:    "comms",
		History:   h,
		Input:     input,
	}
}

func agentCtx(sessionID string) context.Context {
	return approval.WithSessionID(context.Background(), sessionID)
}

// ── tests ─────────────────────────────────────────────────────────────────────

// TestHandle_CheckEmails verifies that "check my emails" results in an email_list
// tool call and a non-empty summary — without any send occurring.
func TestHandle_CheckEmails(t *testing.T) {
	emailProv := newStubEmailProvider()
	store := approval.NewMemoryStore()

	llm := &seqLLM{responses: []costguard.CompletionResponse{
		toolCall("tc1", "email_list", map[string]int{"limit": 5}),
		textReply("You have 2 emails: from alice@example.com about 'Q2 budget review', and from bob@startup.io about 'Coffee catch-up?'."),
	}}

	agent := comms.New(llm, emailProv, newStubCalendarProvider(), store, memory.NewUserStore())
	resp, err := agent.Handle(agentCtx("sess-1"), newRequest("sess-1", "Check my emails"))

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(resp.Output, "alice") {
		t.Errorf("expected email summary mentioning alice, got: %s", resp.Output)
	}
	if emailProv.sentCount > 0 {
		t.Error("no email should be sent during a list operation")
	}
	if len(llm.calls) != 2 {
		t.Errorf("expected 2 LLM calls (tool call + text), got %d", len(llm.calls))
	}
}

// TestHandle_CalendarQuery verifies that "what's on my calendar tomorrow?"
// results in a calendar_list tool call and a response mentioning the events.
func TestHandle_CalendarQuery(t *testing.T) {
	calProv := newStubCalendarProvider()
	store := approval.NewMemoryStore()

	tomorrow := time.Now().Add(24 * time.Hour)
	from := time.Date(tomorrow.Year(), tomorrow.Month(), tomorrow.Day(), 0, 0, 0, 0, time.UTC)
	to := from.Add(24 * time.Hour)

	llm := &seqLLM{responses: []costguard.CompletionResponse{
		toolCall("tc1", "calendar_list", map[string]string{
			"from": from.Format(time.RFC3339),
			"to":   to.Format(time.RFC3339),
		}),
		textReply("Tomorrow you have: Team standup at 9:00 AM and Product review at 2:00 PM."),
	}}

	agent := comms.New(llm, newStubEmailProvider(), calProv, store, memory.NewUserStore())
	resp, err := agent.Handle(agentCtx("sess-2"), newRequest("sess-2", "What's on my calendar tomorrow?"))

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(resp.Output, "standup") {
		t.Errorf("expected calendar summary mentioning standup, got: %s", resp.Output)
	}
}

// TestHandle_DraftReply verifies that "draft a reply" uses email_draft (safe)
// and returns the draft content — without triggering the approval gate.
func TestHandle_DraftReply(t *testing.T) {
	emailProv := newStubEmailProvider()
	store := approval.NewMemoryStore()

	llm := &seqLLM{responses: []costguard.CompletionResponse{
		toolCall("tc1", "email_draft", map[string]string{
			"to":      "alice@example.com",
			"subject": "Re: Q2 budget review",
			"body":    "Hi Alice,\n\nI've reviewed the spreadsheet and left some comments.\n\nMarco",
		}),
		textReply("Draft saved: to alice@example.com — 'Re: Q2 budget review'. Reply 'confirm' to send."),
	}}

	agent := comms.New(llm, emailProv, newStubCalendarProvider(), store, memory.NewUserStore())
	resp, err := agent.Handle(agentCtx("sess-3"), newRequest("sess-3", "Draft a reply to alice about the budget"))

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(emailProv.drafts) != 1 {
		t.Errorf("expected 1 draft saved, got %d", len(emailProv.drafts))
	}
	if emailProv.sentCount > 0 {
		t.Error("draft must not trigger a send")
	}
	if !strings.Contains(resp.Output, "Draft") {
		t.Errorf("expected output to mention draft, got: %s", resp.Output)
	}
}

// TestHandle_SendEmail_RequiresApproval verifies that email_send returns a
// pending_approval response on first call, and the agent surfaces an approval
// prompt rather than sending automatically.
func TestHandle_SendEmail_RequiresApproval(t *testing.T) {
	emailProv := newStubEmailProvider()
	store := approval.NewMemoryStore()

	llm := &seqLLM{responses: []costguard.CompletionResponse{
		// LLM tries to call email_send.
		toolCall("tc1", "email_send", map[string]string{
			"to":      "alice@example.com",
			"subject": "Re: Q2 budget review",
			"body":    "Hi Alice, reviewed it. Looks good!",
		}),
		// After seeing the pending_approval result, LLM asks the user to confirm.
		textReply("I need your approval to send this email to alice@example.com. Reply 'confirm' to proceed."),
	}}

	agent := comms.New(llm, emailProv, newStubCalendarProvider(), store, memory.NewUserStore())
	resp, err := agent.Handle(agentCtx("sess-4"), newRequest("sess-4", "Send the reply to alice"))

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The email must NOT have been sent.
	if emailProv.sentCount > 0 {
		t.Error("email must not be sent without explicit user approval")
	}
	// The agent must surface an approval prompt.
	output := strings.ToLower(resp.Output)
	if !strings.Contains(output, "confirm") && !strings.Contains(output, "approval") && !strings.Contains(output, "approve") {
		t.Errorf("expected approval prompt in output, got: %s", resp.Output)
	}
	// The action must be registered as pending in the store.
	pending := store.ListPending("sess-4")
	if len(pending) == 0 {
		t.Error("expected a pending approval record in the store")
	}
}

// TestHandle_CreateCalendarEvent_RequiresApproval verifies that calendar_create
// returns a pending_approval response and the agent asks for confirmation.
func TestHandle_CreateCalendarEvent_RequiresApproval(t *testing.T) {
	calProv := newStubCalendarProvider()
	store := approval.NewMemoryStore()

	nextWeek := time.Now().Add(7 * 24 * time.Hour)
	start := time.Date(nextWeek.Year(), nextWeek.Month(), nextWeek.Day(), 10, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)

	llm := &seqLLM{responses: []costguard.CompletionResponse{
		toolCall("tc1", "calendar_create", map[string]any{
			"title": "Design review",
			"start": start.Format(time.RFC3339),
			"end":   end.Format(time.RFC3339),
		}),
		textReply("I need your approval to create 'Design review' on " + start.Format("Mon 2 Jan") + ". Reply 'confirm' to proceed."),
	}}

	agent := comms.New(llm, newStubEmailProvider(), calProv, store, memory.NewUserStore())
	resp, err := agent.Handle(agentCtx("sess-5"), newRequest("sess-5", "Schedule a design review next week"))

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Event must not have been created.
	if len(calProv.events) != 2 { // only the two stub events
		t.Errorf("event must not be created without approval, got %d events", len(calProv.events))
	}
	// Agent must have asked for confirmation.
	output := strings.ToLower(resp.Output)
	if !strings.Contains(output, "confirm") && !strings.Contains(output, "approval") && !strings.Contains(output, "approve") {
		t.Errorf("expected approval prompt in output, got: %s", resp.Output)
	}
	pending := store.ListPending("sess-5")
	if len(pending) == 0 {
		t.Error("expected a pending calendar_create approval record")
	}
}

// TestHandle_MultiStep verifies that the agent correctly handles a two-step
// workflow: read an email, then draft a reply.
func TestHandle_MultiStep(t *testing.T) {
	emailProv := newStubEmailProvider()
	store := approval.NewMemoryStore()

	llm := &seqLLM{responses: []costguard.CompletionResponse{
		// Step 1: read the email to get context.
		toolCall("tc1", "email_read", map[string]string{"id": "msg-1"}),
		// Step 2: draft a reply using the email body.
		toolCall("tc2", "email_draft", map[string]string{
			"to":      "alice@example.com",
			"subject": "Re: Q2 budget review",
			"body":    "Hi Alice, I reviewed it. Looks good!\n\nMarco",
		}),
		// Final text response.
		textReply("I've read Alice's email and drafted a reply. Let me know if you'd like to send it."),
	}}

	agent := comms.New(llm, emailProv, newStubCalendarProvider(), store, memory.NewUserStore())
	resp, err := agent.Handle(agentCtx("sess-6"), newRequest("sess-6", "Read alice's email and draft a reply"))

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(llm.calls) != 3 {
		t.Errorf("expected 3 LLM calls (read + draft + text), got %d", len(llm.calls))
	}
	if len(emailProv.drafts) != 1 {
		t.Errorf("expected 1 draft, got %d", len(emailProv.drafts))
	}
	if emailProv.sentCount > 0 {
		t.Error("multi-step draft must not send")
	}
	if resp.Output == "" {
		t.Error("expected non-empty response")
	}
}

// TestHandle_NilProviders verifies that the agent starts and responds even when
// email and calendar providers are nil (tools simply won't be registered).
func TestHandle_NilProviders(t *testing.T) {
	store := approval.NewMemoryStore()
	llm := &seqLLM{responses: []costguard.CompletionResponse{
		textReply("I don't have access to your email or calendar right now."),
	}}

	agent := comms.New(llm, nil, nil, store, memory.NewUserStore())
	resp, err := agent.Handle(agentCtx("sess-7"), newRequest("sess-7", "Check my emails"))

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Output == "" {
		t.Error("expected non-empty response even with no providers")
	}
}

// TestHandle_SystemPromptIsFirst verifies that the system prompt is always the
// first message sent to the LLM.
func TestHandle_SystemPromptIsFirst(t *testing.T) {
	store := approval.NewMemoryStore()
	llm := &seqLLM{responses: []costguard.CompletionResponse{
		textReply("Got it."),
	}}

	agent := comms.New(llm, nil, nil, store, memory.NewUserStore())
	agent.Handle(agentCtx("sess-8"), newRequest("sess-8", "Hello")) //nolint:errcheck

	if len(llm.calls) == 0 {
		t.Fatal("expected at least one LLM call")
	}
	first := llm.calls[0].Messages
	if len(first) == 0 || first[0].Role != "system" {
		t.Errorf("first message must be system prompt, got role=%q", first[0].Role)
	}
}
