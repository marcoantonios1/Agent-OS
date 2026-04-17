// Package project provides the project_list and project_load tools for the
// Builder Agent. These tools let users resume existing projects across sessions.
package project

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/marcoantonios1/Agent-OS/internal/approval"
	"github.com/marcoantonios1/Agent-OS/internal/costguard"
	"github.com/marcoantonios1/Agent-OS/internal/sessions"
)

// Session metadata keys mirrored from the builder agent package.
// These must match exactly; duplicating them here avoids an import cycle.
const (
	keyProjectID  = "builder.project_id"
	keyPhase      = "builder.phase"
	keySpec       = "builder.spec"
	keyTasks      = "builder.tasks"
	keyActiveTask = "builder.active_task"
)

// ── ListTool ──────────────────────────────────────────────────────────────────

// ListTool implements the project_list tool.
// It returns all projects for the current user, ordered by most recently updated.
type ListTool struct {
	projects     sessions.ProjectStore
	sessionStore sessions.SessionStore
}

// NewListTool returns a ListTool backed by the given stores.
func NewListTool(projects sessions.ProjectStore, sessionStore sessions.SessionStore) *ListTool {
	return &ListTool{projects: projects, sessionStore: sessionStore}
}

func (t *ListTool) Definition() costguard.ToolDefinition {
	return costguard.ToolDefinition{
		Name:        "project_list",
		Description: "List all Builder Agent projects for the current user. Returns project IDs, names, phases, and last-updated timestamps. Call this before project_load to find the right project ID.",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
			"required":   []string{},
		},
	}
}

func (t *ListTool) Execute(ctx context.Context, _ json.RawMessage) (string, error) {
	userID, err := t.userIDFromCtx(ctx)
	if err != nil {
		return `{"error":"no session found"}`, nil
	}

	summaries, err := t.projects.ListProjects(userID)
	if err != nil {
		return "", fmt.Errorf("project_list: %w", err)
	}

	out, err := json.Marshal(summaries)
	if err != nil {
		return "", fmt.Errorf("project_list: marshal: %w", err)
	}
	return string(out), nil
}

// userIDFromCtx resolves the user ID by reading the current session from the
// session store. The session ID is injected by the router via
// approval.WithSessionID before any tool is called.
func (t *ListTool) userIDFromCtx(ctx context.Context) (string, error) {
	sessionID := approval.SessionIDFromContext(ctx)
	if sessionID == "" {
		return "", fmt.Errorf("no session ID in context")
	}
	sess, err := t.sessionStore.Get(sessionID)
	if err != nil {
		return "", fmt.Errorf("session not found: %w", err)
	}
	return sess.UserID, nil
}

// ── LoadTool ──────────────────────────────────────────────────────────────────

// LoadTool implements the project_load tool.
// It loads a project by ID and writes its state into the current session metadata
// so the Builder Agent picks it up on the next turn.
type LoadTool struct {
	projects     sessions.ProjectStore
	sessionStore sessions.SessionStore
}

// NewLoadTool returns a LoadTool backed by the given stores.
func NewLoadTool(projects sessions.ProjectStore, sessionStore sessions.SessionStore) *LoadTool {
	return &LoadTool{projects: projects, sessionStore: sessionStore}
}

func (t *LoadTool) Definition() costguard.ToolDefinition {
	return costguard.ToolDefinition{
		Name:        "project_load",
		Description: "Load an existing project into the current session so you can resume work. Call project_list first to find the project ID. After loading, the next Builder Agent turn will start at the saved phase.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"project_id": map[string]any{
					"type":        "string",
					"description": "The project ID to load (from project_list).",
				},
			},
			"required": []string{"project_id"},
		},
	}
}

type loadInput struct {
	ProjectID string `json:"project_id"`
}

func (t *LoadTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var in loadInput
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("project_load: parse input: %w", err)
	}
	if in.ProjectID == "" {
		return `{"error":"project_id is required"}`, nil
	}

	project, err := t.projects.GetProject(in.ProjectID)
	if err != nil {
		if errors.Is(err, sessions.ErrProjectNotFound) {
			out, _ := json.Marshal(map[string]string{
				"error": fmt.Sprintf("project %q not found", in.ProjectID),
			})
			return string(out), nil
		}
		return "", fmt.Errorf("project_load: %w", err)
	}

	// Write project state into the current session so the builder agent picks
	// it up on the next turn. The session ID comes from the approval context.
	sessionID := approval.SessionIDFromContext(ctx)
	if sessionID != "" {
		_ = t.sessionStore.SetMetadata(sessionID, keyProjectID, project.ID)
		_ = t.sessionStore.SetMetadata(sessionID, keyPhase, project.Phase)
		if project.Spec != "" {
			_ = t.sessionStore.SetMetadata(sessionID, keySpec, project.Spec)
		}
		if project.Tasks != "" {
			_ = t.sessionStore.SetMetadata(sessionID, keyTasks, project.Tasks)
		}
		if project.ActiveTask != "" {
			_ = t.sessionStore.SetMetadata(sessionID, keyActiveTask, project.ActiveTask)
		}
	}

	out, _ := json.Marshal(map[string]string{
		"status":  "loaded",
		"id":      project.ID,
		"name":    project.Name,
		"phase":   project.Phase,
		"message": fmt.Sprintf("Project %q loaded. The Builder Agent will resume from the %q phase on your next message.", project.Name, project.Phase),
	})
	return string(out), nil
}
