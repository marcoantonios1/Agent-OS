// Package userprofile provides tools for reading and updating the current
// user's persistent profile (preferences, contacts, communication style).
// These tools are registered in the Comms Agent so it can personalise replies.
package userprofile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/marcoantonios1/Agent-OS/internal/costguard"
	"github.com/marcoantonios1/Agent-OS/internal/sessions"
)

// ── ReadTool ──────────────────────────────────────────────────────────────────

// ReadTool implements the user_profile_read tool.
// It fetches the profile for the user ID carried in the request context.
type ReadTool struct {
	store sessions.UserStore
}

// NewReadTool returns a ReadTool backed by the given UserStore.
func NewReadTool(store sessions.UserStore) *ReadTool {
	return &ReadTool{store: store}
}

func (t *ReadTool) Definition() costguard.ToolDefinition {
	return costguard.ToolDefinition{
		Name:        "user_profile_read",
		Description: "Retrieve the current user's persistent profile: name, communication style, preferences (e.g. tone, timezone), and recurring contacts. Call this at the start of personalised tasks.",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
			"required":   []string{},
		},
	}
}

func (t *ReadTool) Execute(ctx context.Context, _ json.RawMessage) (string, error) {
	userID := sessions.UserIDFromContext(ctx)
	if userID == "" {
		return `{"error":"no user ID in context"}`, nil
	}

	profile, err := t.store.GetUser(userID)
	if err != nil {
		if errors.Is(err, sessions.ErrUserNotFound) {
			// Return an empty profile rather than an error so the agent can
			// populate it on first use.
			empty := sessions.UserProfile{UserID: userID}
			out, _ := json.Marshal(empty)
			return string(out), nil
		}
		return "", fmt.Errorf("user_profile_read: %w", err)
	}

	out, err := json.Marshal(profile)
	if err != nil {
		return "", fmt.Errorf("user_profile_read: marshal: %w", err)
	}
	return string(out), nil
}

// ── UpdateTool ────────────────────────────────────────────────────────────────

// UpdateTool implements the user_profile_update tool.
// It performs a partial merge: only fields present in the input are changed.
type UpdateTool struct {
	store sessions.UserStore
}

// NewUpdateTool returns an UpdateTool backed by the given UserStore.
func NewUpdateTool(store sessions.UserStore) *UpdateTool {
	return &UpdateTool{store: store}
}

func (t *UpdateTool) Definition() costguard.ToolDefinition {
	return costguard.ToolDefinition{
		Name:        "user_profile_update",
		Description: "Update the current user's profile. All fields are optional — only provided fields are changed. Use this to record a preferred name, tone, timezone, sign-off phrase, or add a recurring contact.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{
					"type":        "string",
					"description": "The user's preferred name.",
				},
				"communication_style": map[string]any{
					"type":        "string",
					"description": "How the user prefers to communicate, e.g. 'concise and direct', 'formal', 'casual'.",
				},
				"preferences": map[string]any{
					"type":                 "object",
					"additionalProperties": map[string]any{"type": "string"},
					"description":          "Key-value preferences to merge in. Keys like 'tone', 'timezone', 'language', 'sign_off' are common.",
				},
				"add_contact": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"name":  map[string]any{"type": "string"},
						"email": map[string]any{"type": "string"},
						"notes": map[string]any{"type": "string", "description": "Optional context about this contact."},
					},
					"required":    []string{"name", "email"},
					"description": "Add a new recurring contact to the user's address book.",
				},
			},
			"required": []string{},
		},
	}
}

type updateInput struct {
	Name               string            `json:"name"`
	CommunicationStyle string            `json:"communication_style"`
	Preferences        map[string]string `json:"preferences"`
	AddContact         *contactInput     `json:"add_contact"`
}

type contactInput struct {
	Name  string `json:"name"`
	Email string `json:"email"`
	Notes string `json:"notes"`
}

func (t *UpdateTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	userID := sessions.UserIDFromContext(ctx)
	if userID == "" {
		return `{"error":"no user ID in context"}`, nil
	}

	var in updateInput
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("user_profile_update: parse input: %w", err)
	}

	// Load existing profile or start fresh.
	profile, err := t.store.GetUser(userID)
	if err != nil {
		if errors.Is(err, sessions.ErrUserNotFound) {
			profile = &sessions.UserProfile{UserID: userID}
		} else {
			return "", fmt.Errorf("user_profile_update: load: %w", err)
		}
	}

	// Apply partial updates.
	if in.Name != "" {
		profile.Name = in.Name
	}
	if in.CommunicationStyle != "" {
		profile.CommunicationStyle = in.CommunicationStyle
	}
	if len(in.Preferences) > 0 {
		if profile.Preferences == nil {
			profile.Preferences = make(map[string]string)
		}
		for k, v := range in.Preferences {
			profile.Preferences[k] = v
		}
	}
	if in.AddContact != nil {
		profile.RecurringContacts = append(profile.RecurringContacts, sessions.Contact{
			Name:  in.AddContact.Name,
			Email: in.AddContact.Email,
			Notes: in.AddContact.Notes,
		})
	}

	if err := t.store.SaveUser(profile); err != nil {
		return "", fmt.Errorf("user_profile_update: save: %w", err)
	}

	out, _ := json.Marshal(map[string]string{"status": "ok", "user_id": userID})
	return string(out), nil
}
