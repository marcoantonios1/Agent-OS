// Package reminder provides the reminder_set, reminder_cancel, and
// reminder_list tools for the Comms Agent. Reminders fire a follow-up
// message back to the user via the background Worker.
package reminder

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/marcoantonios1/Agent-OS/internal/approval"
	"github.com/marcoantonios1/Agent-OS/internal/costguard"
	"github.com/marcoantonios1/Agent-OS/internal/sessions"
	"github.com/marcoantonios1/Agent-OS/internal/types"
)

// ── SetTool ───────────────────────────────────────────────────────────────────

// SetTool implements reminder_set.
type SetTool struct {
	store sessions.ReminderStore
}

// NewSetTool returns a SetTool backed by the given ReminderStore.
func NewSetTool(store sessions.ReminderStore) *SetTool {
	return &SetTool{store: store}
}

func (t *SetTool) Definition() costguard.ToolDefinition {
	return costguard.ToolDefinition{
		Name:        "reminder_set",
		Description: "Schedule a follow-up reminder for the user. The reminder message will be sent back to the user at the specified time. Use natural language for 'when', e.g. 'in 30 minutes', 'in 2 hours', 'tomorrow at 9am', or an ISO-8601 datetime.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"message": map[string]any{
					"type":        "string",
					"description": "The reminder message to send the user when the reminder fires.",
				},
				"when": map[string]any{
					"type":        "string",
					"description": "When to fire the reminder. Accepts relative durations ('in 30 minutes', 'in 2 hours', 'in 1 day') or absolute ISO-8601 datetimes.",
				},
			},
			"required": []string{"message", "when"},
		},
	}
}

type setInput struct {
	Message string `json:"message"`
	When    string `json:"when"`
}

func (t *SetTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	userID := sessions.UserIDFromContext(ctx)
	if userID == "" {
		return `{"error":"no user ID in context"}`, nil
	}
	sessionID := approval.SessionIDFromContext(ctx)
	channelID := channelIDFromContext(ctx)

	var in setInput
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("reminder_set: parse input: %w", err)
	}
	if in.Message == "" {
		return `{"error":"message is required"}`, nil
	}
	if in.When == "" {
		return `{"error":"when is required"}`, nil
	}

	fireAt, err := parseWhen(in.When)
	if err != nil {
		return fmt.Sprintf(`{"error":%q}`, err.Error()), nil
	}

	r := &sessions.Reminder{
		ID:        uuid.NewString(),
		UserID:    userID,
		SessionID: sessionID,
		ChannelID: channelID,
		Message:   in.Message,
		FireAt:    fireAt,
		CreatedAt: time.Now().UTC(),
	}
	if err := t.store.Save(r); err != nil {
		return "", fmt.Errorf("reminder_set: save: %w", err)
	}

	out, _ := json.Marshal(map[string]string{
		"status":  "ok",
		"id":      r.ID,
		"fire_at": r.FireAt.Format(time.RFC3339),
	})
	return string(out), nil
}

// ── CancelTool ────────────────────────────────────────────────────────────────

// CancelTool implements reminder_cancel.
type CancelTool struct {
	store sessions.ReminderStore
}

// NewCancelTool returns a CancelTool backed by the given ReminderStore.
func NewCancelTool(store sessions.ReminderStore) *CancelTool {
	return &CancelTool{store: store}
}

func (t *CancelTool) Definition() costguard.ToolDefinition {
	return costguard.ToolDefinition{
		Name:        "reminder_cancel",
		Description: "Cancel a previously scheduled reminder by its ID.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": map[string]any{
					"type":        "string",
					"description": "The reminder ID returned by reminder_set.",
				},
			},
			"required": []string{"id"},
		},
	}
}

type cancelInput struct {
	ID string `json:"id"`
}

func (t *CancelTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	userID := sessions.UserIDFromContext(ctx)
	if userID == "" {
		return `{"error":"no user ID in context"}`, nil
	}

	var in cancelInput
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("reminder_cancel: parse input: %w", err)
	}
	if in.ID == "" {
		return `{"error":"id is required"}`, nil
	}

	// Verify ownership before deleting.
	r, err := t.store.Get(in.ID)
	if err != nil {
		return `{"status":"not_found"}`, nil
	}
	if r.UserID != userID {
		return `{"error":"reminder not found"}`, nil
	}

	if err := t.store.Delete(in.ID); err != nil {
		return "", fmt.Errorf("reminder_cancel: delete: %w", err)
	}
	out, _ := json.Marshal(map[string]string{"status": "cancelled", "id": in.ID})
	return string(out), nil
}

// ── ListTool ──────────────────────────────────────────────────────────────────

// ListTool implements reminder_list.
type ListTool struct {
	store sessions.ReminderStore
}

// NewListTool returns a ListTool backed by the given ReminderStore.
func NewListTool(store sessions.ReminderStore) *ListTool {
	return &ListTool{store: store}
}

func (t *ListTool) Definition() costguard.ToolDefinition {
	return costguard.ToolDefinition{
		Name:        "reminder_list",
		Description: "List all pending reminders for the current user, ordered by fire time ascending.",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
			"required":   []string{},
		},
	}
}

type reminderView struct {
	ID      string `json:"id"`
	Message string `json:"message"`
	FireAt  string `json:"fire_at"`
}

func (t *ListTool) Execute(ctx context.Context, _ json.RawMessage) (string, error) {
	userID := sessions.UserIDFromContext(ctx)
	if userID == "" {
		return `{"error":"no user ID in context"}`, nil
	}

	reminders, err := t.store.ListForUser(userID)
	if err != nil {
		return "", fmt.Errorf("reminder_list: %w", err)
	}

	views := make([]reminderView, len(reminders))
	for i, r := range reminders {
		views[i] = reminderView{
			ID:      r.ID,
			Message: r.Message,
			FireAt:  r.FireAt.Format(time.RFC3339),
		}
	}
	out, _ := json.Marshal(views)
	return string(out), nil
}

// ── helpers ───────────────────────────────────────────────────────────────────

// parseWhen converts a human-friendly time string into an absolute time.Time.
// Supported formats:
//   - "in N minutes/hours/days"   — relative from now
//   - ISO-8601 / RFC3339 absolute — parsed directly
func parseWhen(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	lower := strings.ToLower(s)

	// Relative: "in N unit"
	if strings.HasPrefix(lower, "in ") {
		rest := strings.TrimPrefix(lower, "in ")
		// Try time.ParseDuration first (handles "30m", "2h", "1h30m").
		if d, err := time.ParseDuration(rest); err == nil {
			return time.Now().Add(d), nil
		}
		// Handle "N minutes", "N hours", "N days"
		var n int
		var unit string
		if _, err := fmt.Sscanf(rest, "%d %s", &n, &unit); err == nil && n > 0 {
			unit = strings.TrimSuffix(unit, "s") // normalise plural
			switch unit {
			case "minute", "min":
				return time.Now().Add(time.Duration(n) * time.Minute), nil
			case "hour":
				return time.Now().Add(time.Duration(n) * time.Hour), nil
			case "day":
				return time.Now().Add(time.Duration(n) * 24 * time.Hour), nil
			case "week":
				return time.Now().Add(time.Duration(n) * 7 * 24 * time.Hour), nil
			}
		}
		return time.Time{}, fmt.Errorf("cannot parse relative time %q", s)
	}

	// Absolute: try RFC3339 and common date-only formats.
	for _, layout := range []string{
		time.RFC3339,
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05",
		"2006-01-02 15:04",
		"2006-01-02",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}

	return time.Time{}, fmt.Errorf("cannot parse time %q — use 'in N minutes/hours/days' or ISO-8601", s)
}

// channelIDFromContext extracts the channel ID injected by the router/handler.
// Returns empty string if not present; the worker will skip delivery gracefully.
func channelIDFromContext(ctx context.Context) types.ChannelID {
	v, _ := ctx.Value(channelIDKey{}).(types.ChannelID)
	return v
}

type channelIDKey struct{}

// WithChannelID returns a copy of ctx carrying the given channelID.
func WithChannelID(ctx context.Context, id types.ChannelID) context.Context {
	return context.WithValue(ctx, channelIDKey{}, id)
}
