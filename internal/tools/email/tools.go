package email

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/marcoantonios1/Agent-OS/internal/costguard"
)

const (
	defaultListLimit = 10
	maxListLimit     = 50
)

// NewListTool returns the email_list tool.
func NewListTool(p EmailProvider) *ListTool { return &ListTool{p: p} }

// NewReadTool returns the email_read tool.
func NewReadTool(p EmailProvider) *ReadTool { return &ReadTool{p: p} }

// NewSearchTool returns the email_search tool.
func NewSearchTool(p EmailProvider) *SearchTool { return &SearchTool{p: p} }

// NewDraftTool returns the email_draft tool.
func NewDraftTool(p EmailProvider) *DraftTool { return &DraftTool{p: p} }

// ── email_list ────────────────────────────────────────────────────────────────

type listInput struct {
	Limit int `json:"limit"`
}

// ListTool implements the email_list tool.
type ListTool struct{ p EmailProvider }

func (t *ListTool) Definition() costguard.ToolDefinition {
	return costguard.ToolDefinition{
		Name:        "email_list",
		Description: "List recent emails. Returns subject, sender, date, and a short snippet for each.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"limit": map[string]any{
					"type":        "integer",
					"description": fmt.Sprintf("Maximum number of emails to return (1–%d, default %d).", maxListLimit, defaultListLimit),
				},
			},
		},
	}
}

func (t *ListTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var in listInput
	if len(input) > 0 {
		if err := json.Unmarshal(input, &in); err != nil {
			return "", fmt.Errorf("email_list: invalid input: %w", err)
		}
	}
	limit := in.Limit
	if limit <= 0 {
		limit = defaultListLimit
	}
	if limit > maxListLimit {
		limit = maxListLimit
	}

	summaries, err := t.p.List(ctx, limit)
	if err != nil {
		return "", fmt.Errorf("email_list: %w", err)
	}
	if len(summaries) == 0 {
		return "No emails found.", nil
	}
	out, _ := json.Marshal(summaries)
	return string(out), nil
}

// ── email_read ────────────────────────────────────────────────────────────────

type readInput struct {
	ID string `json:"id"`
}

// ReadTool implements the email_read tool.
type ReadTool struct{ p EmailProvider }

func (t *ReadTool) Definition() costguard.ToolDefinition {
	return costguard.ToolDefinition{
		Name:        "email_read",
		Description: "Read the full content of an email by its ID.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": map[string]any{
					"type":        "string",
					"description": "The email ID to read.",
				},
			},
			"required": []string{"id"},
		},
	}
}

func (t *ReadTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var in readInput
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("email_read: invalid input: %w", err)
	}
	if in.ID == "" {
		return "", fmt.Errorf("email_read: id is required")
	}

	email, err := t.p.Read(ctx, in.ID)
	if err != nil {
		return "", fmt.Errorf("email_read: %w", err)
	}
	out, _ := json.Marshal(email)
	return string(out), nil
}

// ── email_search ──────────────────────────────────────────────────────────────

type searchInput struct {
	Query string `json:"query"`
}

// SearchTool implements the email_search tool.
type SearchTool struct{ p EmailProvider }

func (t *SearchTool) Definition() costguard.ToolDefinition {
	return costguard.ToolDefinition{
		Name:        "email_search",
		Description: "Search emails by a query string (e.g. sender, subject keywords, date ranges).",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{
					"type":        "string",
					"description": "The search query, e.g. \"from:alice subject:budget\".",
				},
			},
			"required": []string{"query"},
		},
	}
}

func (t *SearchTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var in searchInput
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("email_search: invalid input: %w", err)
	}
	if in.Query == "" {
		return "", fmt.Errorf("email_search: query is required")
	}

	summaries, err := t.p.Search(ctx, in.Query)
	if err != nil {
		return "", fmt.Errorf("email_search: %w", err)
	}
	out, _ := json.Marshal(summaries)
	return string(out), nil
}

// ── email_draft ───────────────────────────────────────────────────────────────

type draftInput struct {
	To      string `json:"to"`
	Subject string `json:"subject"`
	Body    string `json:"body"`
}

// DraftTool implements the email_draft tool.
type DraftTool struct{ p EmailProvider }

func (t *DraftTool) Definition() costguard.ToolDefinition {
	return costguard.ToolDefinition{
		Name:        "email_draft",
		Description: "Compose an email draft. Returns the draft for review — does NOT send it.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"to":      map[string]any{"type": "string", "description": "Recipient email address."},
				"subject": map[string]any{"type": "string", "description": "Email subject line."},
				"body":    map[string]any{"type": "string", "description": "Email body text."},
			},
			"required": []string{"to", "subject", "body"},
		},
	}
}

func (t *DraftTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var in draftInput
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("email_draft: invalid input: %w", err)
	}
	if in.To == "" {
		return "", fmt.Errorf("email_draft: to is required")
	}
	if in.Subject == "" {
		return "", fmt.Errorf("email_draft: subject is required")
	}
	if in.Body == "" {
		return "", fmt.Errorf("email_draft: body is required")
	}

	draft, err := t.p.Draft(ctx, in.To, in.Subject, in.Body)
	if err != nil {
		return "", fmt.Errorf("email_draft: %w", err)
	}
	out, _ := json.Marshal(draft)
	return string(out), nil
}
