package integration

import (
	"context"
	"strings"
	"testing"

	"github.com/marcoantonios1/Agent-OS/internal/costguard"
	"github.com/marcoantonios1/Agent-OS/internal/memory"
	"github.com/marcoantonios1/Agent-OS/internal/sessions"
)

// TestContextInjection_CommsAgentAddressesUserByName verifies that when a
// UserProfile exists the Comms Agent's system prompt contains the user's name
// so the LLM can address them personally.
func TestContextInjection_CommsAgentAddressesUserByName(t *testing.T) {
	userStore := memory.NewUserStore()
	_ = userStore.SaveUser(&sessions.UserProfile{
		UserID:             "user-marco",
		Name:               "Marco",
		CommunicationStyle: "concise",
		Preferences:        map[string]string{"sign_off": "Marco", "tone": "formal"},
		RecurringContacts: []sessions.Contact{
			{Name: "Alice", Email: "alice@example.com", Notes: "colleague"},
		},
	})

	var systemPrompt string
	capLLM := &capturingScriptedLLM{
		responses: []costguard.CompletionResponse{
			classifyResp("comms"),
			{Content: "Hi Marco, you have no emails."},
		},
		onCall: func(req costguard.CompletionRequest) {
			for _, msg := range req.Messages {
				if msg.Role == "system" && systemPrompt == "" {
					systemPrompt = msg.Content
				}
			}
		},
	}

	stack := newStack(stackConfig{
		customLLM: capLLM,
		userStore: userStore,
		emailProv: newMockEmail(nil, nil),
	})
	defer stack.Close()

	_, resp, err := stack.post(chatRequest{
		SessionID: "sess-context-1",
		UserID:    "user-marco",
		Text:      "Check my emails",
	})
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	if resp.Text == "" {
		t.Error("expected non-empty response")
	}

	if systemPrompt == "" {
		t.Fatal("no system prompt was captured")
	}
	if !strings.Contains(systemPrompt, "Marco") {
		t.Errorf("system prompt should contain the user's name 'Marco', got:\n%s", systemPrompt[:min(len(systemPrompt), 500)])
	}
	if !strings.Contains(systemPrompt, "concise") {
		t.Errorf("system prompt should contain communication style 'concise'")
	}
	if !strings.Contains(systemPrompt, "alice@example.com") {
		t.Errorf("system prompt should contain recurring contact email")
	}
	if !strings.Contains(systemPrompt, "sign_off") {
		t.Errorf("system prompt should contain preferences")
	}
}

// TestContextInjection_NoProfile_GracefulAbsence verifies that when no profile
// exists the agents still work correctly and produce a valid response.
func TestContextInjection_NoProfile_GracefulAbsence(t *testing.T) {
	stack := newStack(stackConfig{
		llmResponses: []costguard.CompletionResponse{
			{Content: "comms"},
			{Content: "You have no emails."},
		},
		emailProv: newMockEmail(nil, nil),
	})
	defer stack.Close()

	_, resp, err := stack.post(chatRequest{
		SessionID: "sess-no-profile",
		UserID:    "unknown-user",
		Text:      "Check my emails",
	})
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	if resp.Text == "" {
		t.Error("expected a non-empty response even with no user profile")
	}
}

// TestContextInjection_BuilderSystemPromptIncludesProject verifies that the
// Builder Agent's system prompt contains the active project name and the user's name.
func TestContextInjection_BuilderSystemPromptIncludesProject(t *testing.T) {
	userStore := memory.NewUserStore()
	_ = userStore.SaveUser(&sessions.UserProfile{
		UserID: "builder-user",
		Name:   "Marco",
	})

	var capturedPrompts []string
	capLLM := &capturingScriptedLLM{
		responses: []costguard.CompletionResponse{
			classifyResp("builder"),
			{Content: "I have some questions.\n<builder_meta>{\"builder.phase\":\"requirements\"}</builder_meta>"},
		},
		onCall: func(req costguard.CompletionRequest) {
			for _, msg := range req.Messages {
				if msg.Role == "system" {
					capturedPrompts = append(capturedPrompts, msg.Content)
				}
			}
		},
	}

	stack := newStack(stackConfig{
		customLLM:  capLLM,
		userStore:  userStore,
		sandboxDir: t.TempDir(),
	})
	defer stack.Close()

	_, _, err := stack.post(chatRequest{
		SessionID: "sess-builder-ctx",
		UserID:    "builder-user",
		Text:      "Build me a padel app",
	})
	if err != nil {
		t.Fatalf("post: %v", err)
	}

	var builderPrompt string
	for _, p := range capturedPrompts {
		if strings.Contains(p, "Builder Agent") {
			builderPrompt = p
			break
		}
	}
	if builderPrompt == "" {
		t.Fatal("no builder system prompt captured")
	}
	if !strings.Contains(builderPrompt, "Marco") {
		t.Errorf("builder prompt should contain user name 'Marco'")
	}
	if !strings.Contains(builderPrompt, "padel") {
		t.Errorf("builder prompt should contain project name derived from input")
	}
}

// ── capturingScriptedLLM ──────────────────────────────────────────────────────

type capturingScriptedLLM struct {
	responses []costguard.CompletionResponse
	idx       int
	onCall    func(costguard.CompletionRequest)
}

func (c *capturingScriptedLLM) Complete(_ context.Context, req costguard.CompletionRequest) (costguard.CompletionResponse, error) {
	c.onCall(req)
	if c.idx >= len(c.responses) {
		return costguard.CompletionResponse{Content: "[no more responses]"}, nil
	}
	resp := c.responses[c.idx]
	c.idx++
	return resp, nil
}

func (c *capturingScriptedLLM) Stream(_ context.Context, _ costguard.CompletionRequest) (<-chan costguard.StreamChunk, error) {
	ch := make(chan costguard.StreamChunk)
	close(ch)
	return ch, nil
}

var _ costguard.LLMClient = (*capturingScriptedLLM)(nil)

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
