package calendar

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/marcoantonios1/Agent-OS/internal/approval"
	"github.com/marcoantonios1/Agent-OS/internal/costguard"
)

// NewUpdateTool returns the calendar_update tool backed by the given approval store.
func NewUpdateTool(p CalendarProvider, store approval.Store) *UpdateTool {
	return &UpdateTool{p: p, store: store}
}

type updateInput struct {
	EventID     string `json:"event_id"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	Location    string `json:"location,omitempty"`
	Start       string `json:"start,omitempty"` // RFC3339
	End         string `json:"end,omitempty"`   // RFC3339
}

// UpdateTool implements the calendar_update tool with an ApprovalStore gate.
type UpdateTool struct {
	p     CalendarProvider
	store approval.Store
}

func (t *UpdateTool) Definition() costguard.ToolDefinition {
	return costguard.ToolDefinition{
		Name: "calendar_update",
		Description: "Update an existing calendar event. Only provide the fields you want to change. " +
			"The first call registers the action and returns a pending_approval response — " +
			"the agent must ask the user to confirm. Once the user confirms, call this tool " +
			"again with identical parameters to execute the update.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"event_id": map[string]any{
					"type":        "string",
					"description": "The ID of the event to update.",
				},
				"title": map[string]any{
					"type":        "string",
					"description": "New event title (omit to keep existing).",
				},
				"description": map[string]any{
					"type":        "string",
					"description": "New event description (omit to keep existing).",
				},
				"location": map[string]any{
					"type":        "string",
					"description": "New event location (omit to keep existing).",
				},
				"start": map[string]any{
					"type":        "string",
					"description": "New start time in RFC3339 format (omit to keep existing).",
				},
				"end": map[string]any{
					"type":        "string",
					"description": "New end time in RFC3339 format (omit to keep existing).",
				},
			},
			"required": []string{"event_id"},
		},
	}
}

func (t *UpdateTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var in updateInput
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("calendar_update: invalid input: %w", err)
	}
	if in.EventID == "" {
		return "", fmt.Errorf("calendar_update: event_id is required")
	}

	upd := UpdateEventInput{EventID: in.EventID}

	if in.Start != "" {
		t, err := time.Parse(time.RFC3339, in.Start)
		if err != nil {
			return "", fmt.Errorf("calendar_update: invalid start time: %w", err)
		}
		upd.Start = t
	}
	if in.End != "" {
		t, err := time.Parse(time.RFC3339, in.End)
		if err != nil {
			return "", fmt.Errorf("calendar_update: invalid end time: %w", err)
		}
		upd.End = t
	}
	if !upd.Start.IsZero() && !upd.End.IsZero() && !upd.End.After(upd.Start) {
		return "", fmt.Errorf("calendar_update: end must be after start")
	}
	upd.Title = in.Title
	upd.Description = in.Description
	upd.Location = in.Location

	sessionID := approval.SessionIDFromContext(ctx)
	actionID := approval.ActionID("calendar_update", in.EventID, in.Title, in.Start, in.End)
	desc := fmt.Sprintf("Update calendar event '%s'", in.EventID)
	if in.Title != "" {
		desc = fmt.Sprintf("Update calendar event '%s' to '%s'", in.EventID, in.Title)
	}

	if !t.store.Approved(sessionID, actionID) {
		t.store.Pend(sessionID, actionID, desc)
		return pendingJSON(actionID, desc), nil
	}
	t.store.Consume(sessionID, actionID)

	event, err := t.p.Update(ctx, upd)
	if err != nil {
		return "", fmt.Errorf("calendar_update: %w", err)
	}
	out, _ := json.Marshal(event)
	return string(out), nil
}
