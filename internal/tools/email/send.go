package email

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/marcoantonios1/Agent-OS/internal/approval"
	"github.com/marcoantonios1/Agent-OS/internal/costguard"
)

// pendingResponse is returned (as JSON, not an error) when the tool call
// requires user approval that has not yet been granted.
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

// NewSendTool returns the email_send tool backed by the given approval store.
func NewSendTool(p EmailProvider, store approval.Store) *SendTool {
	return &SendTool{p: p, store: store}
}

type sendInput struct {
	To      string `json:"to"`
	Subject string `json:"subject"`
	Body    string `json:"body"`
}

// SendTool implements the email_send tool with an ApprovalStore gate.
// No email is ever delivered without an approved record in the store.
type SendTool struct {
	p     EmailProvider
	store approval.Store
}

func (t *SendTool) Definition() costguard.ToolDefinition {
	return costguard.ToolDefinition{
		Name: "email_send",
		Description: "Send an email. " +
			"The first call registers the action and returns a pending_approval response — " +
			"the agent must show the draft to the user and ask for explicit confirmation. " +
			"Once the user confirms, call this tool again with identical parameters to deliver the email.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"to": map[string]any{
					"type":        "string",
					"description": "Recipient email address.",
				},
				"subject": map[string]any{
					"type":        "string",
					"description": "Email subject line.",
				},
				"body": map[string]any{
					"type":        "string",
					"description": "Plain-text email body.",
				},
			},
			"required": []string{"to", "subject", "body"},
		},
	}
}

func (t *SendTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var in sendInput
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("email_send: invalid input: %w", err)
	}
	if in.To == "" {
		return "", fmt.Errorf("email_send: to is required")
	}
	if in.Subject == "" {
		return "", fmt.Errorf("email_send: subject is required")
	}
	if in.Body == "" {
		return "", fmt.Errorf("email_send: body is required")
	}

	sessionID := approval.SessionIDFromContext(ctx)
	actionID := approval.ActionID("email_send", in.To, in.Subject, in.Body)
	desc := fmt.Sprintf("Send email to %s — Subject: %s", in.To, in.Subject)

	if !t.store.Approved(sessionID, actionID) {
		t.store.Pend(sessionID, actionID, desc)
		return pendingJSON(actionID, desc), nil
	}
	t.store.Consume(sessionID, actionID)

	if err := t.p.Send(ctx, in.To, in.Subject, in.Body); err != nil {
		return "", fmt.Errorf("email_send: %w", err)
	}

	b, _ := json.Marshal(map[string]string{
		"status":  "sent",
		"to":      in.To,
		"subject": in.Subject,
	})
	return string(b), nil
}
