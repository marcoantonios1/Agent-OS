package calendar

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/marcoantonios1/Agent-OS/internal/costguard"
)

// ErrApprovalRequired is returned by calendar_create when the approval gate
// has not been set. No event is ever created without explicit approval.
var ErrApprovalRequired = fmt.Errorf("calendar_create: explicit approval required — set approved=true in the tool input")

// NewListTool returns the calendar_list tool.
func NewListTool(p CalendarProvider) *ListTool { return &ListTool{p: p} }

// NewReadTool returns the calendar_read tool.
func NewReadTool(p CalendarProvider) *ReadTool { return &ReadTool{p: p} }

// NewCreateTool returns the calendar_create tool.
func NewCreateTool(p CalendarProvider) *CreateTool { return &CreateTool{p: p} }

// ── calendar_list ─────────────────────────────────────────────────────────────

type listInput struct {
	From string `json:"from"` // RFC3339 or "today"
	To   string `json:"to"`   // RFC3339 or "today"
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
	// Approved must be explicitly set to true by the caller. Any other value
	// causes the tool to return ErrApprovalRequired without touching the provider.
	Approved    bool     `json:"approved"`
	Title       string   `json:"title"`
	Description string   `json:"description,omitempty"`
	Location    string   `json:"location,omitempty"`
	Start       string   `json:"start"` // RFC3339
	End         string   `json:"end"`   // RFC3339
	Attendees   []string `json:"attendees,omitempty"`
	AllDay      bool     `json:"all_day,omitempty"`
}

// CreateTool implements the calendar_create tool.
type CreateTool struct{ p CalendarProvider }

func (t *CreateTool) Definition() costguard.ToolDefinition {
	return costguard.ToolDefinition{
		Name: "calendar_create",
		Description: "Create a new calendar event. " +
			"The 'approved' field MUST be set to true — the agent must confirm with the user before setting this. " +
			"Without approval the tool returns an error and no event is created.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"approved": map[string]any{
					"type":        "boolean",
					"description": "Must be true. The agent must obtain explicit user confirmation before setting this.",
				},
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
					"description": "Event start time in RFC3339 format.",
				},
				"end": map[string]any{
					"type":        "string",
					"description": "Event end time in RFC3339 format.",
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
			"required": []string{"approved", "title", "start", "end"},
		},
	}
}

func (t *CreateTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var in createInput
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("calendar_create: invalid input: %w", err)
	}

	// Approval gate — checked before any field validation.
	if !in.Approved {
		return "", ErrApprovalRequired
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

// parseTimeInput parses a time string that is either RFC3339 or the special
// value "today". endOfDay controls whether "today" resolves to 00:00 or 23:59.
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
