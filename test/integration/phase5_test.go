package integration

// Phase 5 consolidated integration tests:
//
//  1. TextMessage_Routes          — Telegram text message → dispatcher → bot.Send
//  2. Whitelist_EnforcedEnd2End   — non-allowed user → no LLM call, no bot.Send
//  3. ToolCallModel_UsedForToolSteps — cheap model for tool steps, full model for final
//  4. ToolCallModel_FallsBackWhenUnset — single model used when tool_call_model absent
//  5. CommunitySkill_RegisterAndUse — mock skill appears in LLM tool definitions
//  6. Voice_Pipeline_EndToEnd     — transcriber → route → agent reply → bot.Send

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"github.com/marcoantonios1/Agent-OS/internal/agents/generic"
	"github.com/marcoantonios1/Agent-OS/internal/channels/telegram"
	"github.com/marcoantonios1/Agent-OS/internal/costguard"
	"github.com/marcoantonios1/Agent-OS/internal/tools"
	"github.com/marcoantonios1/Agent-OS/internal/types"
)

// ── mock community tool ───────────────────────────────────────────────────────

// mockCommunityTool satisfies tools.Tool with a fixed name and no-op Execute.
// Used for Tests 3, 4, and 5.
type mockCommunityTool struct{ name string }

func (t *mockCommunityTool) Definition() costguard.ToolDefinition {
	return costguard.ToolDefinition{
		Name:        t.name,
		Description: "Mock community skill for integration testing.",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
	}
}

func (t *mockCommunityTool) Execute(_ context.Context, _ json.RawMessage) (string, error) {
	return "mock result", nil
}

// ── agent fixture helper ──────────────────────────────────────────────────────

// writeAgentFull writes agent.yaml + SYSTEM.md to dir/<name>/ with optional
// tool_call_model support. Pass an empty string to omit tool_call_model.
func writeAgentFull(t *testing.T, dir, name, model, toolCallModel string, skills []string) {
	t.Helper()
	agentDir := filepath.Join(dir, name)
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatalf("writeAgentFull mkdir: %v", err)
	}

	yaml := fmt.Sprintf("id: %s\nmodel: %s\nmax_tokens: 512\nintents:\n  - %s\n", name, model, name)
	if toolCallModel != "" {
		yaml += "tool_call_model: " + toolCallModel + "\n"
	}
	if len(skills) > 0 {
		yaml += "skills:\n"
		for _, s := range skills {
			yaml += "  - " + s + "\n"
		}
	}

	if err := os.WriteFile(filepath.Join(agentDir, "agent.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatalf("writeAgentFull yaml: %v", err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "SYSTEM.md"), []byte("You are "+name+"."), 0o644); err != nil {
		t.Fatalf("writeAgentFull system: %v", err)
	}
}

// ── Test 1: text message routes through Telegram handler → dispatcher → reply ─

// TestPhase5_Telegram_TextMessage_Routes sends a text message to the Telegram
// handler and verifies that the dispatcher receives the message and bot.Send is
// called exactly once with the agent reply.
func TestPhase5_Telegram_TextMessage_Routes(t *testing.T) {
	bot := &mockTGBot{}
	disp := &telegramRecordingDispatcher{}
	h := telegram.NewForTest(disp, bot, 111)

	msg := &tgbotapi.Message{
		MessageID: 1,
		From:      &tgbotapi.User{ID: 111},
		Chat:      &tgbotapi.Chat{ID: 100},
		Text:      "what is the weather today?",
	}
	h.HandleMessage(context.Background(), msg)

	disp.mu.Lock()
	dispatched := disp.msgs
	disp.mu.Unlock()

	if len(dispatched) != 1 {
		t.Fatalf("dispatcher called %d times, want 1", len(dispatched))
	}
	if dispatched[0].Text != msg.Text {
		t.Errorf("routed text = %q, want %q", dispatched[0].Text, msg.Text)
	}

	bot.mu.Lock()
	sent := bot.sent
	bot.mu.Unlock()

	if len(sent) != 1 {
		t.Fatalf("bot.Send called %d times, want 1", len(sent))
	}
	mc, ok := sent[0].(tgbotapi.MessageConfig)
	if !ok {
		t.Fatalf("expected MessageConfig, got %T", sent[0])
	}
	// telegramRecordingDispatcher.Route always returns OutboundMessage{Text: "ok"}.
	if mc.Text != "ok" {
		t.Errorf("bot reply = %q, want %q", mc.Text, "ok")
	}
}

// ── Test 2: whitelist enforcement is visible end-to-end ──────────────────────

// TestPhase5_Telegram_Whitelist_EnforcedEnd2End wires the Telegram handler to
// the full router stack and sends a message from a non-allowed user ID. The
// handler must drop the message silently — no LLM call, no bot.Send.
func TestPhase5_Telegram_Whitelist_EnforcedEnd2End(t *testing.T) {
	stack := newStack(stackConfig{
		llmResponses: []costguard.CompletionResponse{
			classifyResp("comms"),
			textResp("this should never be sent"),
		},
	})
	defer stack.Close()

	bot := &mockTGBot{}
	h := telegram.NewForTest(stack.router, bot, 111) // only uid 111 is allowed

	msg := &tgbotapi.Message{
		MessageID: 1,
		From:      &tgbotapi.User{ID: 999}, // not whitelisted
		Chat:      &tgbotapi.Chat{ID: 100},
		Text:      "I should be silently dropped",
	}
	h.HandleMessage(context.Background(), msg)

	if stack.llm.callCount() != 0 {
		t.Errorf("LLM called %d times for non-whitelisted user, want 0", stack.llm.callCount())
	}

	bot.mu.Lock()
	sent := len(bot.sent)
	bot.mu.Unlock()

	if sent != 0 {
		t.Errorf("bot.Send called %d times for non-whitelisted user, want 0", sent)
	}
}

// ── Test 3: tool_call_model → cheap model for tool steps, full for final ──────

// TestPhase5_ToolCallModel_UsedForToolSteps creates a generic agent with
// model="full-model" and tool_call_model="cheap-model", scripts the LLM with
// one tool-call step followed by a text response, and verifies that:
//   - intermediate Complete() calls used "cheap-model"
//   - the final synthesis call used "full-model"
func TestPhase5_ToolCallModel_UsedForToolSteps(t *testing.T) {
	dir := t.TempDir()
	const skill = "mock_tcm_tool"
	writeAgentFull(t, dir, "tcmtest", "full-model", "cheap-model", []string{skill})

	globalReg := tools.NewRegistry()
	globalReg.Register(&mockCommunityTool{name: skill})

	// Three scripted responses:
	//   1. cheap-model → tool call (one round-trip)
	//   2. cheap-model → text (triggers the "re-run with full model" branch)
	//   3. full-model  → final synthesis
	llm := &scriptedLLM{
		responses: []costguard.CompletionResponse{
			toolCallResp("tc1", skill, `{}`),
			textResp("cheap draft"),
			textResp("final answer"),
		},
	}

	ag, err := generic.Load(filepath.Join(dir, "tcmtest"), llm, globalReg)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if _, err = ag.Handle(context.Background(), types.AgentRequest{
		History: []types.ConversationTurn{{Role: "user", Content: "do something"}},
	}); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	llm.mu.Lock()
	calls := llm.calls
	llm.mu.Unlock()

	if len(calls) < 3 {
		t.Fatalf("expected ≥3 LLM calls (tool-step + text-step + re-run), got %d", len(calls))
	}
	// First two calls are tool-decision steps — must use cheap model.
	for i, c := range calls[:len(calls)-1] {
		if c.Model != "cheap-model" {
			t.Errorf("calls[%d].Model = %q, want cheap-model", i, c.Model)
		}
	}
	// Last call is the final synthesis — must use full model.
	if last := calls[len(calls)-1]; last.Model != "full-model" {
		t.Errorf("final call.Model = %q, want full-model", last.Model)
	}
}

// ── Test 4: when tool_call_model is unset, every call uses the agent model ───

// TestPhase5_ToolCallModel_FallsBackWhenUnset creates a generic agent with no
// tool_call_model and verifies that all Complete() calls — including the one
// that follows a tool execution — use a single model.
func TestPhase5_ToolCallModel_FallsBackWhenUnset(t *testing.T) {
	dir := t.TempDir()
	const skill = "mock_fallback_tool"
	writeAgentFull(t, dir, "notcm", "only-model", "", []string{skill}) // empty tool_call_model

	globalReg := tools.NewRegistry()
	globalReg.Register(&mockCommunityTool{name: skill})

	llm := &scriptedLLM{
		responses: []costguard.CompletionResponse{
			toolCallResp("tc1", skill, `{}`),
			textResp("final answer"), // no re-run because ToolCallModel is empty
		},
	}

	ag, err := generic.Load(filepath.Join(dir, "notcm"), llm, globalReg)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if _, err = ag.Handle(context.Background(), types.AgentRequest{
		History: []types.ConversationTurn{{Role: "user", Content: "do something"}},
	}); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	llm.mu.Lock()
	calls := llm.calls
	llm.mu.Unlock()

	if len(calls) < 2 {
		t.Fatalf("expected ≥2 LLM calls, got %d", len(calls))
	}
	for i, c := range calls {
		if c.Model != "only-model" {
			t.Errorf("calls[%d].Model = %q, want only-model (single-model path)", i, c.Model)
		}
	}
}

// ── Test 5: community skill appears in LLM tool definitions ──────────────────

// TestPhase5_CommunitySkill_RegisterAndUse registers a mock community skill,
// creates a generic agent that lists it in its skills, and verifies that the
// tool definition reaches the LLM in the CompletionRequest.Tools slice.
func TestPhase5_CommunitySkill_RegisterAndUse(t *testing.T) {
	dir := t.TempDir()
	const skillName = "community_ping"
	writeAgentFull(t, dir, "pingagent", "test-model", "", []string{skillName})

	globalReg := tools.NewRegistry()
	globalReg.Register(&mockCommunityTool{name: skillName})

	var mu sync.Mutex
	var capturedDefs []costguard.ToolDefinition
	capLLM := &capturingScriptedLLM{
		responses: []costguard.CompletionResponse{textResp("pong")},
		onCall: func(req costguard.CompletionRequest) {
			mu.Lock()
			capturedDefs = append(capturedDefs, req.Tools...)
			mu.Unlock()
		},
	}

	ag, err := generic.Load(filepath.Join(dir, "pingagent"), capLLM, globalReg)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if _, err = ag.Handle(context.Background(), types.AgentRequest{
		History: []types.ConversationTurn{{Role: "user", Content: "ping"}},
	}); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	mu.Lock()
	defs := capturedDefs
	mu.Unlock()

	found := false
	for _, d := range defs {
		if d.Name == skillName {
			found = true
			break
		}
	}
	if !found {
		names := make([]string, 0, len(defs))
		for _, d := range defs {
			names = append(names, d.Name)
		}
		t.Errorf("community skill %q not found in LLM tool definitions; got: %v", skillName, names)
	}
}

// ── Test 6: voice pipeline end-to-end ─────────────────────────────────────────

// TestPhase5_Voice_Pipeline_EndToEnd exercises the full inbound voice path:
//
//  1. HandleMessage receives a Telegram voice message.
//  2. The handler fetches audio bytes from a local httptest.Server.
//  3. The fixedTranscriber converts them to text.
//  4. The text is forwarded to the dispatcher as an InboundMessage.
//  5. The dispatcher reply is sent back to the user via bot.Send.
func TestPhase5_Voice_Pipeline_EndToEnd(t *testing.T) {
	audiSrv := audioServer()
	defer audiSrv.Close()

	const transcribedText = "remind me to call the dentist"
	const agentReply = "I have set a reminder for you."

	bot := &voiceBot{fileURL: audiSrv.URL + "/voice.ogg"}
	disp := &voiceRecordingDispatcher{reply: agentReply}
	tr := &fixedTranscriber{text: transcribedText}

	h := telegram.NewForTest(disp, bot, 111)
	h.SetTranscriber(tr)
	h.SetHTTPClient(audiSrv.Client())

	h.HandleMessage(context.Background(), voiceMsg(111, 100, "file-xyz"))

	// Dispatcher must have received the transcribed text with the expected prefix.
	disp.mu.Lock()
	msgs := disp.msgs
	disp.mu.Unlock()

	if len(msgs) != 1 {
		t.Fatalf("dispatcher called %d times, want 1", len(msgs))
	}
	const wantPrefix = "[Voice message transcribed]: "
	if !strings.HasPrefix(msgs[0].Text, wantPrefix) {
		t.Errorf("routed text = %q; want prefix %q", msgs[0].Text, wantPrefix)
	}
	if !strings.Contains(msgs[0].Text, transcribedText) {
		t.Errorf("routed text = %q; want to contain %q", msgs[0].Text, transcribedText)
	}

	// Agent reply must have been delivered to the user.
	bot.mu.Lock()
	sent := bot.sent
	bot.mu.Unlock()

	if len(sent) != 1 {
		t.Fatalf("bot.Send called %d times, want 1", len(sent))
	}
	mc, ok := sent[0].(tgbotapi.MessageConfig)
	if !ok {
		t.Fatalf("expected MessageConfig (text fallback from NoopSynthesizer), got %T", sent[0])
	}
	if mc.Text != agentReply {
		t.Errorf("bot reply = %q, want %q", mc.Text, agentReply)
	}
}
