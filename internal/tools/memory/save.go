package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/marcoantonios1/Agent-OS/internal/costguard"
	"github.com/marcoantonios1/Agent-OS/internal/memory/episodic"
	"github.com/marcoantonios1/Agent-OS/internal/sessions"
)

// SaveTool implements memory_save.
type SaveTool struct {
	store episodic.Store
}

// NewSaveTool returns a SaveTool backed by the given episodic.Store.
func NewSaveTool(store episodic.Store) *SaveTool {
	return &SaveTool{store: store}
}

func (t *SaveTool) Definition() costguard.ToolDefinition {
	return costguard.ToolDefinition{
		Name:        "memory_save",
		Description: "Save a specific fact or piece of information to long-term memory for future conversations.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"fact": map[string]any{
					"type":        "string",
					"description": "The fact or information to remember, written as a complete sentence.",
				},
			},
			"required": []string{"fact"},
		},
	}
}

type saveInput struct {
	Fact string `json:"fact"`
}

func (t *SaveTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var in saveInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return "", fmt.Errorf("memory_save: invalid input: %w", err)
	}
	if in.Fact == "" {
		return "", fmt.Errorf("memory_save: fact must not be empty")
	}

	userID := sessions.UserIDFromContext(ctx)
	sessionID := string(sessions.ChannelIDFromContext(ctx))

	mem := episodic.Memory{
		ID:         uuid.NewString(),
		UserID:     userID,
		SessionID:  sessionID,
		Channel:    "explicit",
		Content:    in.Fact,
		Source:     "explicit",
		Importance: 0.8,
		CreatedAt:  time.Now().UTC(),
	}

	if err := t.store.SaveText(ctx, mem); err != nil {
		return "", fmt.Errorf("memory_save: %w", err)
	}
	return fmt.Sprintf("Remembered: %s", in.Fact), nil
}
