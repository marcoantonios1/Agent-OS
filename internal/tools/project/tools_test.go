package project

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/marcoantonios1/Agent-OS/internal/approval"
	"github.com/marcoantonios1/Agent-OS/internal/memory"
	"github.com/marcoantonios1/Agent-OS/internal/sessions"
)

// ctxWithSession returns a context with both an injected session ID (as the
// approval package does) and a live session in the store.
func ctxWithSession(t *testing.T, store sessions.SessionStore, sessionID, userID string) context.Context {
	t.Helper()
	_ = store.Save(&sessions.Session{ID: sessionID, UserID: userID})
	return approval.WithSessionID(context.Background(), sessionID)
}

// ── project_list ──────────────────────────────────────────────────────────────

func TestListTool_EmptyForNewUser(t *testing.T) {
	ps := memory.NewProjectStore()
	ss := memory.NewStore()
	t.Cleanup(ss.Close)
	tool := NewListTool(ps, ss)

	ctx := ctxWithSession(t, ss, "sess1", "user1")
	out, err := tool.Execute(ctx, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var list []any
	if jsonErr := json.Unmarshal([]byte(out), &list); jsonErr != nil {
		t.Fatalf("expected JSON array, got: %s", out)
	}
	if len(list) != 0 {
		t.Errorf("expected empty list for new user, got %d entries", len(list))
	}
}

func TestListTool_ReturnsUsersProjects(t *testing.T) {
	ps := memory.NewProjectStore()
	ss := memory.NewStore()
	t.Cleanup(ss.Close)

	_ = ps.SaveProject(&sessions.Project{ID: "proj_1", UserID: "alice", Name: "Padel app", Phase: "spec"})
	_ = ps.SaveProject(&sessions.Project{ID: "proj_2", UserID: "alice", Name: "Weather app", Phase: "requirements"})
	_ = ps.SaveProject(&sessions.Project{ID: "proj_3", UserID: "bob", Name: "Other", Phase: "codegen"})

	tool := NewListTool(ps, ss)
	ctx := ctxWithSession(t, ss, "sess-alice", "alice")

	out, err := tool.Execute(ctx, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var summaries []sessions.ProjectSummary
	if jsonErr := json.Unmarshal([]byte(out), &summaries); jsonErr != nil {
		t.Fatalf("unmarshal: %v, raw: %s", jsonErr, out)
	}
	if len(summaries) != 2 {
		t.Errorf("expected 2 projects for alice, got %d", len(summaries))
	}
	for _, s := range summaries {
		if s.ID == "proj_3" {
			t.Error("bob's project should not appear in alice's list")
		}
	}
}

func TestListTool_NoSession_ReturnsErrorJSON(t *testing.T) {
	ps := memory.NewProjectStore()
	ss := memory.NewStore()
	t.Cleanup(ss.Close)
	tool := NewListTool(ps, ss)

	out, err := tool.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var m map[string]string
	_ = json.Unmarshal([]byte(out), &m)
	if m["error"] == "" {
		t.Errorf("expected error JSON, got: %s", out)
	}
}

func TestListTool_Definition(t *testing.T) {
	tool := NewListTool(memory.NewProjectStore(), memory.NewStore())
	def := tool.Definition()
	if def.Name != "project_list" {
		t.Errorf("Name: got %q", def.Name)
	}
}

// ── project_load ──────────────────────────────────────────────────────────────

func TestLoadTool_LoadsProjectAndSetsSessionMetadata(t *testing.T) {
	ps := memory.NewProjectStore()
	ss := memory.NewStore()
	t.Cleanup(ss.Close)

	_ = ps.SaveProject(&sessions.Project{
		ID:         "proj_x",
		UserID:     "user1",
		Name:       "Padel app",
		Phase:      "codegen",
		Spec:       "# Overview\nTinder for padel.",
		Tasks:      `[{"index":0}]`,
		ActiveTask: "2",
	})

	tool := NewLoadTool(ps, ss)
	ctx := ctxWithSession(t, ss, "new-sess", "user1")

	out, err := tool.Execute(ctx, json.RawMessage(`{"project_id":"proj_x"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var result map[string]string
	if jsonErr := json.Unmarshal([]byte(out), &result); jsonErr != nil {
		t.Fatalf("unmarshal: %v, raw: %s", jsonErr, out)
	}
	if result["status"] != "loaded" {
		t.Errorf("status: got %q", result["status"])
	}
	if result["phase"] != "codegen" {
		t.Errorf("phase: got %q", result["phase"])
	}

	// Verify session metadata was set.
	sess, err := ss.Get("new-sess")
	if err != nil {
		t.Fatalf("session not found: %v", err)
	}
	if sess.Metadata[keyProjectID] != "proj_x" {
		t.Errorf("session[builder.project_id]: got %q", sess.Metadata[keyProjectID])
	}
	if sess.Metadata[keyPhase] != "codegen" {
		t.Errorf("session[builder.phase]: got %q", sess.Metadata[keyPhase])
	}
	if sess.Metadata[keySpec] == "" {
		t.Error("session[builder.spec] should be set")
	}
	if sess.Metadata[keyActiveTask] != "2" {
		t.Errorf("session[builder.active_task]: got %q", sess.Metadata[keyActiveTask])
	}
}

func TestLoadTool_UnknownProject_ReturnsErrorJSON(t *testing.T) {
	ps := memory.NewProjectStore()
	ss := memory.NewStore()
	t.Cleanup(ss.Close)
	tool := NewLoadTool(ps, ss)
	ctx := ctxWithSession(t, ss, "s1", "u1")

	out, err := tool.Execute(ctx, json.RawMessage(`{"project_id":"does_not_exist"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var m map[string]string
	_ = json.Unmarshal([]byte(out), &m)
	if m["error"] == "" {
		t.Errorf("expected error JSON, got: %s", out)
	}
}

func TestLoadTool_MissingProjectID_ReturnsErrorJSON(t *testing.T) {
	ps := memory.NewProjectStore()
	ss := memory.NewStore()
	t.Cleanup(ss.Close)
	tool := NewLoadTool(ps, ss)
	ctx := ctxWithSession(t, ss, "s1", "u1")

	out, err := tool.Execute(ctx, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var m map[string]string
	_ = json.Unmarshal([]byte(out), &m)
	if m["error"] == "" {
		t.Errorf("expected error JSON for missing project_id, got: %s", out)
	}
}

func TestLoadTool_Definition(t *testing.T) {
	tool := NewLoadTool(memory.NewProjectStore(), memory.NewStore())
	def := tool.Definition()
	if def.Name != "project_load" {
		t.Errorf("Name: got %q", def.Name)
	}
}

// ErrProjectNotFound sentinel is exported and wrappable.
func TestErrProjectNotFound_IsCheckable(t *testing.T) {
	ps := memory.NewProjectStore()
	_, err := ps.GetProject("no-such-id")
	if !errors.Is(err, sessions.ErrProjectNotFound) {
		t.Errorf("expected errors.Is(err, ErrProjectNotFound), got: %v", err)
	}
}
