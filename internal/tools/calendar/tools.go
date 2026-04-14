package calendar

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/marcoantonios1/Agent-OS/internal/approval"
	"github.com/marcoantonios1/Agent-OS/internal/costguard"
)

// pendingResponse is returned (as JSON, not an error) when a tool call requires
// user approval that has not yet been granted.
type pendingResponse struct {
	Status      string `json:"status"`
	ActionID    string `json:"action_id"`
	Description string `json:"description"`
	Message     string `json:"message"`
}

func pendingJSON(actionID, description string) string {
	b, _ := json.Marshal(pendingResponse{
		Status:      "pending_approval",
		ActionID:    actionID,
		Description: description,
		Message:     "This action requires your explicit approval. Reply with 'confirm' or 'yes' to proceed.",
	})
	return string(b)
}

// NewListTool returns the calendar_list tool.
func NewListTool(p CalendarProvider) *ListTool { return &ListTool{p: p} }

// NewReadTool returns the calendar_read tool.
func NewReadTool(p CalendarProvider) *ReadTool { return &ReadTool{p: p} }

// NewCreateTool returns the calendar_create tool backed by the given approval store.
func NewCreateTool(p CalendarProvider, store approval.Store) *CreateTool {
	return &CreateTool{p: p, store: store}
}

// ── calendar_list ─────────────────────────────────────────────────────────────

type listInput struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// ListTool implements the calendar_list tool.
type ListTool struct{ p CalendarProvider }

func (t *ListTool) Definition() costguard.ToolDefinition {
	return costguard.ToolDefinition{
		Name:        "calendar_list",
		Description: "List calendar events for a date range. Use 'today' as a shorthand for the current day.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"from": map[string]any{
					"type":        "string",
					"description": `Start of range. RFC3339 (e.g. "2026-04-08T00:00:00Z") or "today".`,
				},
				"to": map[string]any{
					"type":        "string",
					"description": `End of range (exclusive). RFC3339 or "today" (treated as end-of-day).`,
				},
			},
			"required": []string{"from", "to"},
		},
	}
}

func (t *ListTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var in listInput
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("calendar_list: invalid input: %w", err)
	}
	if in.From == "" || in.To == "" {
		return "", fmt.Errorf("calendar_list: from and to are required")
	}
	from, err := parseTimeInput(in.From, false)
	if err != nil {
		return "", fmt.Errorf("calendar_list: invalid from: %w", err)
	}
	to, err := parseTimeInput(in.To, true)
	if err != nil {
		return "", fmt.Errorf("calendar_list: invalid to: %w", err)
	}
	events, err := t.p.List(ctx, from, to)
	if err != nil {
		return "", fmt.Errorf("calendar_list: %w", err)
	}
	out, _ := json.Marshal(events)
	return string(out), nil
}

// ── calendar_read ─────────────────────────────────────────────────────────────

type readInput struct {
	ID string `json:"id"`
}

// ReadTool implements the calendar_read tool.
type ReadTool struct{ p CalendarProvider }

func (t *ReadTool) Definition() costguard.ToolDefinition {
	return costguard.ToolDefinition{
		Name:        "calendar_read",
		Description: "Read the full details of a calendar event by its ID.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": map[string]any{
					"type":        "string",
					"description": "The event ID to read.",
				},
			},
			"required": []string{"id"},
		},
	}
}

func (t *ReadTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var in readInput
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("calendar_read: invalid input: %w", err)
	}
	if in.ID == "" {
		return "", fmt.Errorf("calendar_read: id is required")
	}
	event, err := t.p.Read(ctx, in.ID)
	if err != nil {
		return "", fmt.Errorf("calendar_read: %w", err)
	}
	out, _ := json.Marshal(event)
	return string(out), nil
}

// ── calendar_create ───────────────────────────────────────────────────────────

type createInput struct {
	Title       string   `json:"title"`
	Description string   `json:"description,omitempty"`
	Location    string   `json:"location,omitempty"`
	Start       string   `json:"start"`
	End         string   `json:"end"`
	Attendees   []string `json:"attendees,omitempty"`
	AllDay      bool     `json:"all_day,omitempty"`
}

// CreateTool implements the calendar_create tool with an ApprovalStore gate.
type CreateTool struct {
	p     CalendarProvider
	store approval.Store
}

func (t *CreateTool) Definition() costguard.ToolDefinition {
	return costguard.ToolDefinition{
		Name: "calendar_create",
		Description: "Create a new calendar event. " +
			"The first call registers the action and returns a pending_approval response — " +
			"the agent must ask the user to confirm. Once the user confirms, call this tool " +
			"again with identical parameters to execute the creation.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"title": map[string]any{
					"type":        "string",
					"description": "Event title.",
				},
				"description": map[string]any{
					"type":        "string",
					"description": "Optional event description.",
				},
				"location": map[string]any{
					"type":        "string",
					"description": "Optional event location.",
				},
				"start": map[string]any{
					"type":        "string",
					"description": "Event start time in RFC3339 with the user's local UTC offset (e.g. 2026-04-09T17:00:00+02:00). Never use Z unless the user is in UTC.",
				},
				"end": map[string]any{
					"type":        "string",
					"description": "Event end time in RFC3339 with the user's local UTC offset (e.g. 2026-04-09T18:00:00+02:00). Never use Z unless the user is in UTC.",
				},
				"attendees": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "Optional list of attendee email addresses.",
				},
				"all_day": map[string]any{
					"type":        "boolean",
					"description": "Set to true for all-day events.",
				},
			},
			"required": []string{"title", "start", "end"},
		},
	}
}

func (t *CreateTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var in createInput
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("calendar_create: invalid input: %w", err)
	}
	if in.Title == "" {
		return "", fmt.Errorf("calendar_create: title is required")
	}
	if in.Start == "" || in.End == "" {
		return "", fmt.Errorf("calendar_create: start and end are required")
	}
	start, err := time.Parse(time.RFC3339, in.Start)
	if err != nil {
		return "", fmt.Errorf("calendar_create: invalid start time: %w", err)
	}
	end, err := time.Parse(time.RFC3339, in.End)
	if err != nil {
		return "", fmt.Errorf("calendar_create: invalid end time: %w", err)
	}
	if !end.After(start) {
		return "", fmt.Errorf("calendar_create: end must be after start")
	}

	sessionID := approval.SessionIDFromContext(ctx)
	actionID := approval.ActionID("calendar_create", in.Title, in.Start, in.End, in.Location)
	desc := fmt.Sprintf("Create calendar event '%s' on %s", in.Title, start.Format("2 Jan 2006 15:04"))

	if !t.store.Approved(sessionID, actionID) {
		t.store.Pend(sessionID, actionID, desc)
		return pendingJSON(actionID, desc), nil
	}
	t.store.Consume(sessionID, actionID)

	event, err := t.p.Create(ctx, CreateEventInput{
		Title:       in.Title,
		Description: in.Description,
		Location:    in.Location,
		Start:       start,
		End:         end,
		Attendees:   in.Attendees,
		AllDay:      in.AllDay,
	})
	if err != nil {
		return "", fmt.Errorf("calendar_create: %w", err)
	}
	out, _ := json.Marshal(event)
	return string(out), nil
}

// ── helpers ───────────────────────────────────────────────────────────────────

func parseTimeInput(s string, endOfDay bool) (time.Time, error) {
	if s == "today" {
		now := time.Now()
		if endOfDay {
			return time.Date(now.Year(), now.Month(), now.Day(), 23, 59, 59, 0, now.Location()), nil
		}
		return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()), nil
	}
	return time.Parse(time.RFC3339, s)
}
