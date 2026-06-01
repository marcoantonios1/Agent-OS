package memory

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/marcoantonios1/Agent-OS/internal/costguard"
	"github.com/marcoantonios1/Agent-OS/internal/memory/episodic"
	"github.com/marcoantonios1/Agent-OS/internal/sessions"
)

// ForgetTool implements memory_forget.
type ForgetTool struct {
	store episodic.Store
}

// NewForgetTool returns a ForgetTool backed by the given episodic.Store.
func NewForgetTool(store episodic.Store) *ForgetTool {
	return &ForgetTool{store: store}
}

func (t *ForgetTool) Definition() costguard.ToolDefinition {
	return costguard.ToolDefinition{
		Name:        "memory_forget",
		Description: "Delete the memory most closely matching the given description. Use when the user explicitly asks to forget something.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"fact": map[string]any{
					"type":        "string",
					"description": "Description of the memory to forget.",
				},
			},
			"required": []string{"fact"},
		},
	}
}

type forgetInput struct {
	Fact string `json:"fact"`
}

func (t *ForgetTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var in forgetInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return "", fmt.Errorf("memory_forget: invalid input: %w", err)
	}
	if in.Fact == "" {
		return "", fmt.Errorf("memory_forget: fact must not be empty")
	}

	userID := sessions.UserIDFromContext(ctx)
	memories, err := t.store.SearchByText(ctx, userID, in.Fact, 1)
	if err != nil {
		return "", fmt.Errorf("memory_forget: search: %w", err)
	}
	if len(memories) == 0 {
		return "No matching memory found to forget.", nil
	}
	if memories[0].Distance >= 0.8 {
		return "No sufficiently close memory found to forget.", nil
	}

	content := memories[0].Content
	if err := t.store.Delete(ctx, memories[0].ID); err != nil {
		return "", fmt.Errorf("memory_forget: delete: %w", err)
	}
	return fmt.Sprintf("Forgotten: %s", content), nil
}
