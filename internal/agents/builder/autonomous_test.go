package builder_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/marcoantonios1/Agent-OS/internal/agents/builder"
	"github.com/marcoantonios1/Agent-OS/internal/costguard"
	"github.com/marcoantonios1/Agent-OS/internal/memory"
	"github.com/marcoantonios1/Agent-OS/internal/sessions"
	"github.com/marcoantonios1/Agent-OS/internal/tools/code"
	"github.com/marcoantonios1/Agent-OS/internal/types"
)

// ── captureNotifier ───────────────────────────────────────────────────────────

type captureNotifier struct {
	mu   sync.Mutex
	msgs []string
}

func (n *captureNotifier) NotifyProgress(_ context.Context, _, _, text string) error {
	n.mu.Lock()
	n.msgs = append(n.msgs, text)
	n.mu.Unlock()
	return nil
}

func (n *captureNotifier) captured() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := make([]string, len(n.msgs))
	copy(out, n.msgs)
	return out
}

// ── helpers ───────────────────────────────────────────────────────────────────

func metaResp(content string, meta map[string]string) costguard.CompletionResponse {
	b, _ := json.Marshal(meta)
	return costguard.CompletionResponse{
		Content: content + "\n<builder_meta>" + string(b) + "</builder_meta>",
	}
}

func buildTaskList(titles ...string) string {
	type task struct {
		Index       int      `json:"index"`
		Title       string   `json:"title"`
		Files       []string `json:"files"`
		Description string   `json:"description"`
	}
	var ts []task
	for i, title := range titles {
		ts = append(ts, task{Index: i, Title: title, Files: []string{"file.go"}, Description: title})
	}
	b, _ := json.Marshal(ts)
	return string(b)
}

// primeForAutonomous sets up session + project so Handle() enters autonomous
// codegen on the first call.
func primeForAutonomous(store *memory.Store, projects *memory.ProjectStore, sessionID, userID string) {
	taskList := buildTaskList("Init module", "Write handler", "Add tests")
	store.SetMetadata(sessionID, builder.KeyPhase, builder.PhaseCodegen)  //nolint:errcheck
	store.SetMetadata(sessionID, builder.KeyAutonomous, "true")            //nolint:errcheck
	store.SetMetadata(sessionID, builder.KeyTasks, taskList)               //nolint:errcheck
	store.SetMetadata(sessionID, builder.KeyActiveTask, "0")               //nolint:errcheck

	p := &sessions.Project{
		ID:         "proj_auto_test",
		UserID:     userID,
		Name:       "auto test project",
		Phase:      builder.PhaseCodegen,
		Tasks:      taskList,
		ActiveTask: "0",
	}
	projects.SaveProject(p)                                                          //nolint:errcheck
	store.SetMetadata(sessionID, builder.KeyProjectID, "proj_auto_test") //nolint:errcheck
}

func newAutonomousAgent(llm costguard.LLMClient) (*builder.Agent, *memory.Store, *memory.ProjectStore) {
	store := memory.NewStore()
	projects := memory.NewProjectStore()
	a := builder.New(llm, store, code.Config{SandboxDir: ""}, projects, "test-model")
	return a, store, projects
}

// ── tests ─────────────────────────────────────────────────────────────────────

// TestAutonomous_SkippedWhenFlagAbsent verifies that without the autonomous
// flag, Handle() runs a single normal turn (non-autonomous path unchanged).
func TestAutonomous_SkippedWhenFlagAbsent(t *testing.T) {
	llm := &seqLLM{responses: []costguard.CompletionResponse{
		textReply("Tell me more about what you want to build."),
	}}
	store := memory.NewStore()
	t.Cleanup(store.Close)
	a := builder.New(llm, store, code.Config{}, memory.NewProjectStore(), "m")

	_, err := a.Handle(context.Background(), types.AgentRequest{
		SessionID: "s-no-auto",
		UserID:    "u1",
		Input:     "build something",
	})
	if err != nil {
		t.Fatalf("Handle() error: %v", err)
	}
	// seqLLM.idx stays 0 if only one call was made (index is only bumped on advance).
	if llm.idx != 0 {
		t.Errorf("expected 1 LLM call in non-autonomous mode; idx advanced to %d", llm.idx)
	}
}

// TestAutonomous_RunsAllTasksToCompletion verifies the autonomous loop
// iterates until done without requiring further user input.
func TestAutonomous_RunsAllTasksToCompletion(t *testing.T) {
	llm := &seqLLM{responses: []costguard.CompletionResponse{
		// iter 0: advance task 0 → 1
		metaResp("Task 0 done.", map[string]string{
			"builder.phase": builder.PhaseCodegen, "builder.active_task": "1",
		}),
		// iter 1: advance task 1 → 2
		metaResp("Task 1 done.", map[string]string{
			"builder.phase": builder.PhaseCodegen, "builder.active_task": "2",
		}),
		// iter 2: all done
		metaResp("All done.", map[string]string{
			"builder.phase": builder.PhaseDone,
		}),
		textReply("should not reach here"),
	}}

	a, store, projects := newAutonomousAgent(llm)
	t.Cleanup(store.Close)
	primeForAutonomous(store, projects, "s-run-all", "u1")

	resp, err := a.Handle(context.Background(), types.AgentRequest{
		SessionID: "s-run-all",
		UserID:    "u1",
		Input:     "yes, build autonomously",
	})
	if err != nil {
		t.Fatalf("Handle() error: %v", err)
	}
	if resp.Output == "" {
		t.Error("expected non-empty output from autonomous run")
	}
	// seqLLM advanced through 3 responses (idx stops at last).
	if llm.idx < 2 {
		t.Errorf("expected at least 3 LLM calls; idx=%d", llm.idx)
	}
}

// TestAutonomous_SendsProgressNotifications verifies NotifyProgress is called
// once for each task that completes (active_task index advances).
func TestAutonomous_SendsProgressNotifications(t *testing.T) {
	llm := &seqLLM{responses: []costguard.CompletionResponse{
		metaResp("Task 0 done.", map[string]string{
			"builder.phase": builder.PhaseCodegen, "builder.active_task": "1",
		}),
		metaResp("Task 1 done.", map[string]string{
			"builder.phase": builder.PhaseCodegen, "builder.active_task": "2",
		}),
		metaResp("Task 2 done.", map[string]string{
			"builder.phase": builder.PhaseDone,
		}),
		textReply("should not reach"),
	}}

	notifier := &captureNotifier{}
	a, store, projects := newAutonomousAgent(llm)
	t.Cleanup(store.Close)
	primeForAutonomous(store, projects, "s-notif", "u1")

	_, err := a.Handle(context.Background(), types.AgentRequest{
		SessionID: "s-notif",
		UserID:    "u1",
		Input:     "go",
		Notifier:  notifier,
	})
	if err != nil {
		t.Fatalf("Handle() error: %v", err)
	}

	got := notifier.captured()
	// task advances: 0→1 and 1→2 = 2 notifications. task 2→done has no index advance.
	if len(got) != 2 {
		t.Fatalf("expected 2 progress notifications; got %d: %v", len(got), got)
	}
	for _, msg := range got {
		if !strings.Contains(msg, "Task") {
			t.Errorf("notification %q should mention 'Task'", msg)
		}
		if !strings.Contains(msg, "✅") {
			t.Errorf("notification %q should contain ✅", msg)
		}
	}
}

// TestAutonomous_BlockerAfterRetries verifies that consecutive iterations
// without task progress surface a 🛑 blocker after autonomousMaxRetries.
func TestAutonomous_BlockerAfterRetries(t *testing.T) {
	stuck := metaResp("Tests failing: cannot compile.", map[string]string{
		"builder.phase":       builder.PhaseCodegen,
		"builder.active_task": "0", // never advances
	})
	llm := &seqLLM{responses: []costguard.CompletionResponse{
		stuck, stuck, stuck, stuck, // four responses; only three needed
	}}

	a, store, projects := newAutonomousAgent(llm)
	t.Cleanup(store.Close)
	primeForAutonomous(store, projects, "s-stuck", "u1")

	resp, err := a.Handle(context.Background(), types.AgentRequest{
		SessionID: "s-stuck",
		UserID:    "u1",
		Input:     "continue",
	})
	if err != nil {
		t.Fatalf("Handle() error: %v", err)
	}
	if !strings.Contains(resp.Output, "🛑") {
		t.Errorf("expected 🛑 in output; got: %s", resp.Output)
	}
	if !strings.Contains(resp.Output, "What should I do?") {
		t.Errorf("expected user prompt in blocker output; got: %s", resp.Output)
	}
}

// TestAutonomous_NilNotifierNoPanic verifies nil Notifier is handled safely.
func TestAutonomous_NilNotifierNoPanic(t *testing.T) {
	llm := &seqLLM{responses: []costguard.CompletionResponse{
		metaResp("Task 0 done.", map[string]string{
			"builder.phase": builder.PhaseCodegen, "builder.active_task": "1",
		}),
		metaResp("Done.", map[string]string{"builder.phase": builder.PhaseDone}),
		textReply("should not reach"),
	}}

	a, store, projects := newAutonomousAgent(llm)
	t.Cleanup(store.Close)
	primeForAutonomous(store, projects, "s-nil-notif", "u1")

	_, err := a.Handle(context.Background(), types.AgentRequest{
		SessionID: "s-nil-notif",
		UserID:    "u1",
		Input:     "go",
		Notifier:  nil,
	})
	if err != nil {
		t.Fatalf("Handle() with nil Notifier: %v", err)
	}
}
