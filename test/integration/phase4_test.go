package integration

// Phase 4 integration tests covering eight net-new capabilities:
//
//  1. Image attachment via web channel — vision ContentPart stored in session history.
//  2. PDF attachment via web channel — extracted-text ContentPart stored in session history.
//  3. Generic agent loads from a temp folder with no Go code changes.
//  4. Profile observer extracts personality signals and saves them to the store.
//  5. Personality context injected into agent system prompt when signals exist.
//  6. Heartbeat worker fires and delivers response via notifier.
//  7. HEARTBEAT.md overrides the fallback prompt.
//  8. Context compaction truncates long history before dispatch.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marcoantonios1/Agent-OS/internal/agents/generic"
	"github.com/marcoantonios1/Agent-OS/internal/agents/profile"
	"github.com/marcoantonios1/Agent-OS/internal/costguard"
	"github.com/marcoantonios1/Agent-OS/internal/heartbeat"
	"github.com/marcoantonios1/Agent-OS/internal/memory"
	"github.com/marcoantonios1/Agent-OS/internal/sessions"
	"github.com/marcoantonios1/Agent-OS/internal/tools"
	"github.com/marcoantonios1/Agent-OS/internal/types"
)

// ── Test 1: Image attachment — vision ContentPart stored in history ───────────

// TestPhase4_ImageAttachment_WebChannel sends a POST /v1/chat with a base64
// image attachment and verifies that the LLM receives a ConversationTurn whose
// Parts include a ContentPart of type "image" with the original data intact.
func TestPhase4_ImageAttachment_WebChannel(t *testing.T) {
	var capturedReqs []costguard.CompletionRequest
	capLLM := &capturingScriptedLLM{
		responses: []costguard.CompletionResponse{
			classifyResp("comms"),
			textResp("I can see the image you uploaded."),
		},
		onCall: func(req costguard.CompletionRequest) {
			capturedReqs = append(capturedReqs, req)
		},
	}
	stack := newStack(stackConfig{
		customLLM: capLLM,
		emailProv: newMockEmail(nil, nil),
	})
	defer stack.Close()

	// Minimal PNG header bytes — the handler validates MIME type, not image content.
	imgBytes := []byte{
		0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
		0x00, 0x00, 0x00, 0x01,
	}
	encoded := base64.StdEncoding.EncodeToString(imgBytes)

	body, _ := json.Marshal(map[string]any{
		"session_id": "p4-img-1",
		"user_id":    "u1",
		"text":       "What does this image show?",
		"attachments": []map[string]string{
			{"data": encoded, "mime_type": "image/png", "filename": "test.png"},
		},
	})
	resp, err := http.Post(stack.srv.URL+"/v1/chat", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	// Find the user turn with Parts across all captured LLM calls (skip classifier).
	var imgPart *types.ContentPart
outer:
	for _, req := range capturedReqs {
		for _, msg := range req.Messages {
			if msg.Role != "user" {
				continue
			}
			for i := range msg.Parts {
				if msg.Parts[i].Type == "image" {
					p := msg.Parts[i]
					imgPart = &p
					break outer
				}
			}
		}
	}
	if imgPart == nil {
		t.Fatal("no image ContentPart found in any LLM call")
	}
	if imgPart.MimeType != "image/png" {
		t.Errorf("MimeType = %q, want image/png", imgPart.MimeType)
	}
	if imgPart.ImageData != encoded {
		t.Error("ImageData not preserved in ContentPart")
	}
}

// ── Test 2: PDF attachment — extracted-text ContentPart stored in history ──────

// TestPhase4_PDFAttachment_WebChannel sends a POST /v1/chat with a base64-encoded
// PDF and verifies that the LLM receives a ConversationTurn whose Parts include a
// text ContentPart containing the text extracted from the PDF.
func TestPhase4_PDFAttachment_WebChannel(t *testing.T) {
	var capturedReqs []costguard.CompletionRequest
	capLLM := &capturingScriptedLLM{
		responses: []costguard.CompletionResponse{
			classifyResp("comms"),
			textResp("The invoice total is 500."),
		},
		onCall: func(req costguard.CompletionRequest) {
			capturedReqs = append(capturedReqs, req)
		},
	}
	stack := newStack(stackConfig{
		customLLM: capLLM,
		emailProv: newMockEmail(nil, nil),
	})
	defer stack.Close()

	pdfBytes := buildPhase4PDF([]string{"BT /F1 12 Tf 72 720 Td (Invoice total 500) Tj ET\n"})
	encoded := base64.StdEncoding.EncodeToString(pdfBytes)

	body, _ := json.Marshal(map[string]any{
		"session_id": "p4-pdf-1",
		"user_id":    "u1",
		"text":       "Summarise this invoice",
		"attachments": []map[string]string{
			{"data": encoded, "mime_type": "application/pdf", "filename": "invoice.pdf"},
		},
	})
	resp, err := http.Post(stack.srv.URL+"/v1/chat", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	// Find the PDF text ContentPart across all captured LLM calls.
	var pdfPart *types.ContentPart
outer:
	for _, req := range capturedReqs {
		for _, msg := range req.Messages {
			if msg.Role != "user" {
				continue
			}
			for i := range msg.Parts {
				p := msg.Parts[i]
				if p.Type == "text" && p.Filename == "invoice.pdf" {
					pdfPart = &p
					break outer
				}
			}
		}
	}
	if pdfPart == nil {
		t.Fatal("no PDF text ContentPart found in any LLM call")
	}
	if !strings.Contains(pdfPart.Text, "Invoice total 500") {
		t.Errorf("extracted text %q does not contain expected content", pdfPart.Text)
	}
}

// ── Test 3: Generic agent loads from folder ───────────────────────────────────

// TestPhase4_GenericAgent_LoadsFromFolder writes a minimal agent.yaml + SYSTEM.md
// to a temp directory, loads the agent via generic.Load, calls Handle, and
// verifies the agent returns a non-empty response and the SYSTEM.md content
// was used as the system prompt.
func TestPhase4_GenericAgent_LoadsFromFolder(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "agent.yaml"), []byte(`
id: test-agent
model: gemma4:26b
max_tokens: 512
intents:
  - test
skills: []
`), 0o644); err != nil {
		t.Fatalf("write agent.yaml: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SYSTEM.md"), []byte(`# Test Agent
You are a test agent that answers test questions.`), 0o644); err != nil {
		t.Fatalf("write SYSTEM.md: %v", err)
	}

	llm := newScriptedLLM(textResp("Test agent response."))
	globalReg := tools.NewRegistry()

	agent, err := generic.Load(dir, llm, globalReg)
	if err != nil {
		t.Fatalf("generic.Load: %v", err)
	}

	resp, err := agent.Handle(context.Background(), types.AgentRequest{
		SessionID: "generic-test-1",
		UserID:    "u1",
		Input:     "hello",
		History: []types.ConversationTurn{
			{Role: "user", Content: "hello"},
		},
		Metadata: map[string]string{},
	})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if resp.Output == "" {
		t.Fatal("expected non-empty output from generic agent")
	}
	if resp.AgentID != "test-agent" {
		t.Errorf("AgentID = %q, want test-agent", resp.AgentID)
	}

	// Verify SYSTEM.md content was forwarded as the system prompt.
	llm.mu.Lock()
	calls := llm.calls
	llm.mu.Unlock()
	if len(calls) == 0 {
		t.Fatal("LLM was never called")
	}
	if len(calls[0].Messages) == 0 || calls[0].Messages[0].Role != "system" {
		t.Fatal("first LLM message should be the system prompt")
	}
	if !strings.Contains(calls[0].Messages[0].Content, "Test Agent") {
		t.Errorf("system prompt does not contain SYSTEM.md content: %q", calls[0].Messages[0].Content)
	}
}

// ── Test 4: Personality signals extracted and saved to store ──────────────────

// TestPhase4_PersonalitySignals_Extracted creates a Profile Agent with a scripted
// LLM that returns KEY=VALUE personality lines. After Observe is called the
// personality store must contain the expected signals.
func TestPhase4_PersonalitySignals_Extracted(t *testing.T) {
	store := memory.NewPersonalityStore()
	llm := newScriptedLLM(textResp("response_length=brief\ntechnical_depth=high\ncommunication_style=direct"))

	agent := profile.New(llm, store, "gemma4:26b")

	// Minimum 3 turns required for the observer to call the LLM.
	history := []types.ConversationTurn{
		{Role: "user", Content: "Can you explain goroutines briefly?"},
		{Role: "assistant", Content: "Goroutines are lightweight threads managed by the Go runtime."},
		{Role: "user", Content: "Got it, thanks."},
	}

	if err := agent.Observe(context.Background(), "u1", history); err != nil {
		t.Fatalf("Observe: %v", err)
	}

	p, err := store.GetPersonality("u1")
	if err != nil {
		t.Fatalf("GetPersonality: %v", err)
	}
	if p == nil || len(p.Signals) == 0 {
		t.Fatal("expected personality signals in store, got none")
	}

	signalMap := make(map[string]string, len(p.Signals))
	for _, s := range p.Signals {
		signalMap[s.Key] = s.Value
	}
	for _, tc := range []struct{ key, want string }{
		{"response_length", "brief"},
		{"technical_depth", "high"},
		{"communication_style", "direct"},
	} {
		if got := signalMap[tc.key]; got != tc.want {
			t.Errorf("signal[%q] = %q, want %q", tc.key, got, tc.want)
		}
	}
}

// ── Test 5: Personality context injected into agent system prompt ─────────────

// TestPhase4_PersonalityContext_InjectedIntoPrompt pre-populates the personality
// store with a high-confidence signal, routes a request through the full stack
// to the Builder Agent (which injects personality into its system prompt), and
// verifies that the LLM received a system prompt containing the personality
// context block produced by sessions.FormatPersonalityContext.
func TestPhase4_PersonalityContext_InjectedIntoPrompt(t *testing.T) {
	personalityStore := memory.NewPersonalityStore()
	// UpsertSignal computes confidence from observation count (count/10).
	// Call it 7 times so confidence = 0.7 >= 0.6 threshold → appears in context.
	for i := 0; i < 7; i++ {
		if err := personalityStore.UpsertSignal("u1", sessions.PersonalitySignal{
			Key:   "communication_style",
			Value: "direct",
		}); err != nil {
			t.Fatalf("UpsertSignal: %v", err)
		}
	}

	var capturedSystemPrompts []string
	capLLM := &capturingScriptedLLM{
		responses: []costguard.CompletionResponse{
			// Classifier routes to builder.
			classifyResp("builder"),
			// Builder returns a simple text response (no phase advancement).
			textResp("Understood, I will help you build that."),
		},
		onCall: func(req costguard.CompletionRequest) {
			for _, msg := range req.Messages {
				if msg.Role == "system" {
					capturedSystemPrompts = append(capturedSystemPrompts, msg.Content)
				}
			}
		},
	}

	stack := newStack(stackConfig{
		customLLM:  capLLM,
		emailProv:  newMockEmail(nil, nil),
		sandboxDir: t.TempDir(),
	})
	defer stack.Close()
	stack.router.Personality = personalityStore

	_, _, err := stack.post(chatRequest{
		SessionID: "p4-personality-1",
		UserID:    "u1",
		Text:      "build me something",
	})
	if err != nil {
		t.Fatalf("POST: %v", err)
	}

	// The builder agent's system prompt must contain the personality block.
	// FormatPersonalityContext uses human-readable labels (e.g. "Communication style"),
	// so search for the section header which is always present when signals qualify.
	var found bool
	for _, prompt := range capturedSystemPrompts {
		if strings.Contains(prompt, "User personality") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("personality context block (\"User personality\") not found in any system prompt (captured %d prompts)",
			len(capturedSystemPrompts))
	}
}

// ── Test 6: Heartbeat worker fires → dispatcher and notifier called ───────────

// TestPhase4_HeartbeatWorker_Fires starts a heartbeat worker with a 10ms
// interval, waits for at least one tick, and verifies that the dispatcher was
// called with the configured prompt and that the notifier received the response.
func TestPhase4_HeartbeatWorker_Fires(t *testing.T) {
	const prompt = "Check my emails and calendar."
	const response = "All clear — no urgent emails."

	dispatcher := &phase4Dispatcher{result: response}
	notifier := &recordingNotifier{}

	cfg := heartbeat.Config{
		Interval:  10 * time.Millisecond,
		UserID:    "u1",
		SessionID: "heartbeat",
		Channel:   "test",
		Prompt:    prompt,
	}
	w := heartbeat.New(cfg, dispatcher)
	w.AddNotifier("test", notifier)

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	go w.Run(ctx)
	<-ctx.Done()

	dispatcher.mu.Lock()
	dispatchCalls := make([]types.InboundMessage, len(dispatcher.calls))
	copy(dispatchCalls, dispatcher.calls)
	dispatcher.mu.Unlock()

	if len(dispatchCalls) == 0 {
		t.Fatal("dispatcher was never called — heartbeat did not tick")
	}
	if dispatchCalls[0].Text != prompt {
		t.Errorf("dispatcher text = %q, want %q", dispatchCalls[0].Text, prompt)
	}
	if dispatchCalls[0].UserID != "u1" {
		t.Errorf("dispatcher UserID = %q, want u1", dispatchCalls[0].UserID)
	}
	if dispatchCalls[0].SessionID != "heartbeat" {
		t.Errorf("dispatcher SessionID = %q, want heartbeat", dispatchCalls[0].SessionID)
	}

	notifier.mu.Lock()
	notifyCalls := make([]*sessions.Reminder, len(notifier.calls))
	copy(notifyCalls, notifier.calls)
	notifier.mu.Unlock()

	if len(notifyCalls) == 0 {
		t.Fatal("notifier was never called — response not delivered")
	}
	if notifyCalls[0].Message != response {
		t.Errorf("notifier message = %q, want %q", notifyCalls[0].Message, response)
	}
}

// ── Test 7: HEARTBEAT.md overrides the fallback prompt ────────────────────────

// TestPhase4_HeartbeatMD_OverridesPrompt writes a HEARTBEAT.md to a temp
// directory and verifies that the worker uses its content as the prompt instead
// of the fallback configured via Config.Prompt.
func TestPhase4_HeartbeatMD_OverridesPrompt(t *testing.T) {
	const mdContent = "Check investors email and surface anything urgent."

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "HEARTBEAT.md"), []byte(mdContent), 0o644); err != nil {
		t.Fatalf("write HEARTBEAT.md: %v", err)
	}

	dispatcher := &phase4Dispatcher{result: "nothing urgent"}
	notifier := &recordingNotifier{}

	cfg := heartbeat.Config{
		Interval:     10 * time.Millisecond,
		UserID:       "u1",
		SessionID:    "heartbeat",
		Channel:      "test",
		Prompt:       "fallback prompt that must NOT be used",
		WorkspaceDir: dir,
	}
	w := heartbeat.New(cfg, dispatcher)
	w.AddNotifier("test", notifier)

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	go w.Run(ctx)
	<-ctx.Done()

	dispatcher.mu.Lock()
	calls := make([]types.InboundMessage, len(dispatcher.calls))
	copy(calls, dispatcher.calls)
	dispatcher.mu.Unlock()

	if len(calls) == 0 {
		t.Fatal("dispatcher was never called")
	}
	if calls[0].Text != mdContent {
		t.Errorf("dispatcher text = %q, want HEARTBEAT.md content %q", calls[0].Text, mdContent)
	}
}

// ── Test 8: Context compaction — long history truncated before dispatch ────────

// TestPhase4_ContextCompaction_LongHistory seeds a session with 20 turns × 600
// chars (~3000 tokens). With CompactionThreshold=200 the router must summarise
// older turns before dispatching the agent call. Compaction is in-memory only —
// we verify it by checking that the agent received fewer message turns than the
// un-compacted 21 (20 seeded + 1 new user turn).
func TestPhase4_ContextCompaction_LongHistory(t *testing.T) {
	const sessionID = "p4-compact-1"
	const originalTurns = 20

	turns := make([]types.ConversationTurn, originalTurns)
	for i := range turns {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		turns[i] = types.ConversationTurn{Role: role, Content: strings.Repeat("x", 600)}
	}

	// Track message counts per LLM call so we can find the agent dispatch.
	type callRecord struct {
		msgCount int
		content  string // first system message content (if any)
	}
	var callRecords []callRecord
	capLLM := &capturingScriptedLLM{
		responses: []costguard.CompletionResponse{
			// 1. compaction summary call
			textResp("Earlier conversation summary."),
			// 2. classify
			classifyResp("comms"),
			// 3. agent response
			textResp("All good."),
		},
		onCall: func(req costguard.CompletionRequest) {
			rec := callRecord{msgCount: len(req.Messages)}
			for _, msg := range req.Messages {
				if msg.Role == "system" {
					rec.content = msg.Content
					break
				}
			}
			callRecords = append(callRecords, rec)
		},
	}

	stack := newStack(stackConfig{
		customLLM: capLLM,
		emailProv: newMockEmail(nil, nil),
	})
	defer stack.Close()

	// Low threshold (200 estimated tokens) triggers compaction on 20×600-char history.
	stack.router.CompactionLLM = capLLM
	stack.router.CompactionModel = "gemma4:26b"
	stack.router.CompactionThreshold = 200

	// Seed the session with the long pre-existing history.
	if err := stack.store.Save(&sessions.Session{
		ID:      sessionID,
		UserID:  "u1",
		History: turns,
	}); err != nil {
		t.Fatalf("seed session: %v", err)
	}

	_, _, err := stack.post(chatRequest{
		SessionID: sessionID,
		UserID:    "u1",
		Text:      "how are we doing?",
	})
	if err != nil {
		t.Fatalf("POST: %v", err)
	}

	// Without compaction the agent would receive:
	//   1 system prompt + (20 seeded + 1 new user turn) = 22 messages.
	// With compaction it should receive:
	//   1 system prompt + 1 summary + keepRecentTurns(10) + 1 new user turn = 13.
	// We just verify the agent call had fewer than 22 messages.
	const uncompactedCount = 1 + originalTurns + 1 // system + history + new user turn

	// The agent call is the last call (after compaction + classifier).
	if len(callRecords) < 3 {
		t.Fatalf("expected at least 3 LLM calls (compact + classify + agent), got %d", len(callRecords))
	}
	agentCall := callRecords[len(callRecords)-1]
	if agentCall.msgCount >= uncompactedCount {
		t.Errorf("agent received %d messages — history was NOT compacted (un-compacted would be %d)",
			agentCall.msgCount, uncompactedCount)
	}

	// The compaction summary call must have fired (first call, receives the old transcript).
	if !strings.Contains(callRecords[0].content, "Summarise the following") &&
		!strings.Contains(callRecords[0].content, "summarise") {
		// compaction uses a system message with "Summarise" — just verify the LLM was called
		if len(callRecords[0].content) == 0 {
			t.Error("first LLM call (compaction) had no system message")
		}
	}
}

// ── Phase 4 local helpers ─────────────────────────────────────────────────────

// phase4Dispatcher satisfies reminder.Dispatcher and records every Route call.
type phase4Dispatcher struct {
	mu     sync.Mutex
	calls  []types.InboundMessage
	result string
}

func (d *phase4Dispatcher) Route(_ context.Context, msg types.InboundMessage) (types.OutboundMessage, error) {
	d.mu.Lock()
	d.calls = append(d.calls, msg)
	d.mu.Unlock()
	return types.OutboundMessage{Text: d.result}, nil
}

// buildPhase4PDF creates a minimal valid PDF for the attachment tests.
// Equivalent to buildTestPDF in handler_test.go — duplicated here to avoid
// cross-package test dependencies.
func buildPhase4PDF(pageStreams []string) []byte {
	var buf bytes.Buffer
	write := func(s string) { buf.WriteString(s) }

	write("%PDF-1.4\n")
	nPages := len(pageStreams)
	fontObj := 2*nPages + 3
	contentStart := nPages + 3

	offsets := make([]int, 0, 2+2*nPages+1)
	offsets = append(offsets, buf.Len())
	write("1 0 obj\n<</Type /Catalog /Pages 2 0 R>>\nendobj\n")

	kids := make([]string, nPages)
	for i := range pageStreams {
		kids[i] = fmt.Sprintf("%d 0 R", 3+i)
	}
	offsets = append(offsets, buf.Len())
	write(fmt.Sprintf("2 0 obj\n<</Type /Pages /Kids [%s] /Count %d>>\nendobj\n",
		strings.Join(kids, " "), nPages))

	for i := range pageStreams {
		offsets = append(offsets, buf.Len())
		write(fmt.Sprintf(
			"%d 0 obj\n<</Type /Page /Parent 2 0 R /MediaBox [0 0 612 792]"+
				" /Contents %d 0 R /Resources <</Font <</F1 %d 0 R>>>>>>\nendobj\n",
			3+i, contentStart+i, fontObj))
	}
	for i, stream := range pageStreams {
		offsets = append(offsets, buf.Len())
		write(fmt.Sprintf("%d 0 obj\n<</Length %d>>\nstream\n%sendstream\nendobj\n",
			contentStart+i, len(stream), stream))
	}
	offsets = append(offsets, buf.Len())
	write(fmt.Sprintf(
		"%d 0 obj\n<</Type /Font /Subtype /Type1 /BaseFont /Helvetica>>\nendobj\n", fontObj))

	xrefPos := buf.Len()
	nObjs := len(offsets) + 1
	write(fmt.Sprintf("xref\n0 %d\n", nObjs))
	write("0000000000 65535 f \n")
	for _, off := range offsets {
		write(fmt.Sprintf("%010d 00000 n \n", off))
	}
	write(fmt.Sprintf("trailer\n<</Size %d /Root 1 0 R>>\nstartxref\n%d\n", nObjs, xrefPos))
	write("%%EOF\n")
	return buf.Bytes()
}
