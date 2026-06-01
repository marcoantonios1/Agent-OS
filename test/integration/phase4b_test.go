package integration

// Phase 4b integration tests: folder-based (generic) agent layer.
//
//  1. LoadAll discovers all valid agent folders in a directory
//  2. LoadAll skips a folder missing SYSTEM.md, other agents still load
//  3. Agent with an unknown skill name loads and handles requests without crashing
//  4. Generic agent's Handle places the system prompt as the first LLM message
//  5. Generic agent uses the model string from agent.yaml
//  6. "I have a headache" → classifier returns ["doctor"] → Doctor Agent handles it

import (
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marcoantonios1/Agent-OS/internal/agents/generic"
	"github.com/marcoantonios1/Agent-OS/internal/approval"
	"github.com/marcoantonios1/Agent-OS/internal/channels/web"
	"github.com/marcoantonios1/Agent-OS/internal/costguard"
	"github.com/marcoantonios1/Agent-OS/internal/memory"
	"github.com/marcoantonios1/Agent-OS/internal/router"
	"github.com/marcoantonios1/Agent-OS/internal/skills"
	"github.com/marcoantonios1/Agent-OS/internal/tools"
	"github.com/marcoantonios1/Agent-OS/internal/tools/code"
	"github.com/marcoantonios1/Agent-OS/internal/types"
)

// ── helpers ───────────────────────────────────────────────────────────────────

// writeAgent writes a minimal agent.yaml + SYSTEM.md to dir/<name>/
func writeAgent(t *testing.T, dir, name, model string, intents, skills []string) {
	t.Helper()
	agentDir := filepath.Join(dir, name)
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatalf("writeAgent mkdir: %v", err)
	}

	yaml := fmt.Sprintf("id: %s\nmodel: %s\nmax_tokens: 512\n", name, model)
	if len(intents) > 0 {
		yaml += "intents:\n"
		for _, i := range intents {
			yaml += "  - " + i + "\n"
		}
	}
	if len(skills) > 0 {
		yaml += "skills:\n"
		for _, s := range skills {
			yaml += "  - " + s + "\n"
		}
	}

	if err := os.WriteFile(filepath.Join(agentDir, "agent.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatalf("writeAgent yaml: %v", err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "SYSTEM.md"), []byte("You are "+name+"."), 0o644); err != nil {
		t.Fatalf("writeAgent system: %v", err)
	}
}

// ── Test 1: LoadAll discovers all valid folders ───────────────────────────────

func TestPhase4b_LoadAll_DiscoversFolders(t *testing.T) {
	dir := t.TempDir()
	writeAgent(t, dir, "agent-a", "model-a", []string{"intent-a"}, nil)
	writeAgent(t, dir, "agent-b", "model-b", []string{"intent-b"}, nil)
	writeAgent(t, dir, "agent-c", "model-c", []string{"intent-c"}, nil)

	agents, err := generic.LoadAll(dir, &scriptedLLM{}, tools.NewRegistry())
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if len(agents) != 3 {
		t.Errorf("got %d agents, want 3", len(agents))
	}
	for _, intent := range []string{"intent-a", "intent-b", "intent-c"} {
		if _, ok := agents[router.Intent(intent)]; !ok {
			t.Errorf("intent %q not present in loaded agents", intent)
		}
	}
}

// ── Test 2: LoadAll skips bad folder, others still load ───────────────────────

func TestPhase4b_LoadAll_SkipsBadFolder(t *testing.T) {
	dir := t.TempDir()
	writeAgent(t, dir, "good-a", "model", []string{"good-a"}, nil)
	writeAgent(t, dir, "good-b", "model", []string{"good-b"}, nil)

	// Bad folder: agent.yaml present but SYSTEM.md missing.
	badDir := filepath.Join(dir, "bad")
	os.MkdirAll(badDir, 0o755)                                                                       //nolint:errcheck
	os.WriteFile(filepath.Join(badDir, "agent.yaml"), []byte("id: bad\nmodel: m\nintents:\n  - bad\n"), 0o644) //nolint:errcheck

	agents, err := generic.LoadAll(dir, &scriptedLLM{}, tools.NewRegistry())
	if err != nil {
		t.Fatalf("LoadAll returned unexpected error: %v", err)
	}
	if len(agents) != 2 {
		t.Errorf("got %d agents, want 2 (bad folder should be skipped)", len(agents))
	}
	if _, ok := agents[router.Intent("bad")]; ok {
		t.Error("bad agent should not have been registered")
	}
}

// ── Test 3: unknown skill — agent still loads and handles requests ────────────

func TestPhase4b_LoadAll_UnknownSkill_Warning(t *testing.T) {
	dir := t.TempDir()
	writeAgent(t, dir, "myagent", "test-model", []string{"myagent"}, []string{"nonexistent_skill"})

	llm := &scriptedLLM{responses: []costguard.CompletionResponse{{Content: "ok despite missing skill"}}}
	agents, err := generic.LoadAll(dir, llm, tools.NewRegistry())
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if len(agents) != 1 {
		t.Fatalf("got %d agents, want 1", len(agents))
	}

	ag := agents[router.Intent("myagent")]
	resp, err := ag.Handle(context.Background(), types.AgentRequest{
		History: []types.ConversationTurn{{Role: "user", Content: "hello"}},
	})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if resp.Output != "ok despite missing skill" {
		t.Errorf("output = %q, want agent response", resp.Output)
	}
}

// ── Test 4: system prompt is the first message in the LLM call ───────────────

func TestPhase4b_GenericAgent_Handle_SystemPromptFirst(t *testing.T) {
	dir := t.TempDir()
	writeAgent(t, dir, "test", "test-model", []string{"test"}, nil)

	llm := &scriptedLLM{responses: []costguard.CompletionResponse{{Content: "response"}}}
	ag, err := generic.Load(filepath.Join(dir, "test"), llm, tools.NewRegistry())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	_, err = ag.Handle(context.Background(), types.AgentRequest{
		SessionID: "s1",
		History:   []types.ConversationTurn{{Role: "user", Content: "hello"}},
	})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}

	llm.mu.Lock()
	defer llm.mu.Unlock()
	if len(llm.calls) == 0 {
		t.Fatal("no LLM calls recorded")
	}
	msgs := llm.calls[0].Messages
	if len(msgs) == 0 || msgs[0].Role != "system" {
		t.Fatalf("first message role = %q, want system", msgs[0].Role)
	}
	if !strings.HasPrefix(msgs[0].Content, "You are test.") {
		t.Errorf("system content = %q, does not start with SYSTEM.md content", msgs[0].Content)
	}
}

// ── Test 5: agent uses the model string from agent.yaml ──────────────────────

func TestPhase4b_GenericAgent_UsesCorrectModel(t *testing.T) {
	dir := t.TempDir()
	writeAgent(t, dir, "test", "gemma4:27b", []string{"test"}, nil)

	llm := &scriptedLLM{responses: []costguard.CompletionResponse{{Content: "ok"}}}
	ag, err := generic.Load(filepath.Join(dir, "test"), llm, tools.NewRegistry())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	ag.Handle(context.Background(), types.AgentRequest{ //nolint:errcheck
		History: []types.ConversationTurn{{Role: "user", Content: "hi"}},
	})

	llm.mu.Lock()
	defer llm.mu.Unlock()
	if len(llm.calls) == 0 {
		t.Fatal("no LLM calls recorded")
	}
	if llm.calls[0].Model != "gemma4:27b" {
		t.Errorf("model = %q, want gemma4:27b", llm.calls[0].Model)
	}
}

// ── Test 6: "I have a headache" routes to the Doctor Agent ───────────────────

func TestPhase4b_DoctorAgent_RoutesCorrectly(t *testing.T) {
	// Create a temp doctor agent folder.
	dir := t.TempDir()
	writeAgent(t, dir, "doctor", "gemma4:26b",
		[]string{"doctor", "medical", "health"},
		[]string{"web_search", "web_fetch"},
	)

	// LLM call 0: classifier returns ["doctor"]
	// LLM call 1: doctor agent produces a text response
	llm := newScriptedLLM(
		classifyResp("doctor"),
		textResp("Headaches and fever together may indicate an infection. Please consult a doctor."),
	)

	// Build the global registry via skills.NewGlobalRegistry so web_search and
	// web_fetch are registered through the canonical single code path.
	store := memory.NewStore()
	defer store.Close()
	approvals := approval.NewMemoryStore()
	globalReg := skills.NewGlobalRegistry(
		nil, nil, &mockSearchProvider{},
		approvals, memory.NewUserStore(), memory.NewReminderStore(),
		memory.NewProjectStore(), store, code.Config{},
		nil,
	)

	// Load the doctor agent.
	genericAgents, err := generic.LoadAll(dir, llm, globalReg)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if len(genericAgents) == 0 {
		t.Fatal("LoadAll: no agents loaded from temp dir")
	}
	classifier := router.NewLLMClassifier(llm, "gemma4:26b")

	agentsMap := make(map[router.Intent]router.Agent, len(genericAgents))
	for intent, ag := range genericAgents {
		agentsMap[intent] = ag
	}
	r := router.New(classifier, agentsMap, store, approvals)
	srv := httptest.NewServer(web.NewHandler(r, nil))
	defer srv.Close()

	// POST a medical question.
	ts := &testStack{srv: srv, store: store, llm: llm}
	_, resp, err := ts.post(chatRequest{
		SessionID: "p4b-doctor-1",
		UserID:    "user-1",
		Text:      "I have a headache and fever",
	})
	if err != nil {
		t.Fatalf("POST /v1/chat: %v", err)
	}

	// Assert the doctor agent's response was returned.
	if !strings.Contains(resp.Text, "Headaches") {
		t.Errorf("response = %q, expected doctor agent response about headaches", resp.Text)
	}

	// Assert the agent LLM call used the model from agent.yaml.
	llm.mu.Lock()
	calls := llm.calls
	llm.mu.Unlock()
	if len(calls) < 2 {
		t.Fatalf("expected ≥2 LLM calls (classify + agent), got %d", len(calls))
	}
	if calls[1].Model != "gemma4:26b" {
		t.Errorf("agent call model = %q, want gemma4:26b", calls[1].Model)
	}
}
