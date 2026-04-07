// Package router contains the intent classification and message routing logic.
package router

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/marcoantonios1/Agent-OS/internal/costguard"
	"github.com/marcoantonios1/Agent-OS/internal/types"
)

// Intent represents the classified destination agent for an inbound message.
type Intent string

const (
	// IntentComms routes to the communications agent (general conversation,
	// emails, scheduling, day-to-day assistance).
	IntentComms Intent = "comms"
	// IntentBuilder routes to the builder agent (writing, editing, or debugging
	// code; building software artefacts).
	IntentBuilder Intent = "builder"
	// IntentResearch routes to the research agent (searching for information,
	// summarising articles, analysing data, answering factual questions).
	IntentResearch Intent = "research"
	// IntentUnknown is returned when classification fails or is ambiguous.
	IntentUnknown Intent = "unknown"
)

// IntentClassifier classifies an inbound message into an Intent.
// Implementations must not call any LLM provider directly; they must use the
// costguard.LLMClient interface.
type IntentClassifier interface {
	Classify(ctx context.Context, sessionID, input string, history []types.ConversationTurn) (Intent, error)
}

const classifierModel = "claude-sonnet-4-6"

const systemPrompt = `You are an intent classifier for a multi-agent AI system.
Your job is to read the user's latest message (and optionally the conversation
history) and return exactly ONE JSON object with a single key "intent".

The valid intent values are:

- "comms"    – General conversation, casual chat, sending or drafting emails,
               scheduling messages, reminders, day-to-day personal assistance.
               Examples: "Send Alice an email about the meeting",
                         "Remind me to call the dentist",
                         "What's on my calendar today?"

- "builder"  – Writing, editing, reviewing, or debugging code; creating scripts,
               configuration files, or any software artefact; explaining how
               a piece of code works.
               Examples: "Write a Python function to parse CSV",
                         "Why is my Go build failing?",
                         "Refactor this function to use generics"

- "research" – Searching for information, summarising articles or documents,
               answering factual questions, comparing options, analysing data.
               Examples: "What are the main differences between REST and GraphQL?",
                         "Summarise the latest news about climate change",
                         "Which database is better for time-series data?"

- "unknown"  – The message is ambiguous, off-topic, or cannot be reliably
               classified into any of the above categories.

Respond with ONLY valid JSON. No markdown, no explanation, no extra keys.
Example response: {"intent": "builder"}`

// LLMClassifier is the production IntentClassifier that uses Costguard for
// LLM-based classification.
type LLMClassifier struct {
	client costguard.LLMClient
	log    *slog.Logger
}

// NewLLMClassifier returns an LLMClassifier using the provided LLMClient.
func NewLLMClassifier(client costguard.LLMClient) *LLMClassifier {
	return &LLMClassifier{
		client: client,
		log:    slog.Default(),
	}
}

// Classify sends the input (and optional history) to Costguard and parses the
// returned JSON intent. Falls back to IntentUnknown on any parse failure.
func (c *LLMClassifier) Classify(ctx context.Context, sessionID, input string, history []types.ConversationTurn) (Intent, error) {
	messages := buildMessages(history, input)

	req := costguard.CompletionRequest{
		Model:     classifierModel,
		Messages:  messages,
		MaxTokens: 32, // response is always {"intent":"<value>"} — tiny
	}

	resp, err := c.client.Complete(ctx, req)
	if err != nil {
		c.log.WarnContext(ctx, "classifier LLM call failed, defaulting to unknown",
			"session_id", sessionID, "error", err)
		return IntentUnknown, fmt.Errorf("classify: LLM call failed: %w", err)
	}

	intent := parseIntent(resp.Content)
	c.log.InfoContext(ctx, "intent classified",
		"session_id", sessionID,
		"intent", intent,
		"raw", resp.Content,
	)
	return intent, nil
}

// buildMessages constructs the message slice for the classifier request.
// The system prompt is injected as the first "user" turn so it is always
// present regardless of history length.
func buildMessages(history []types.ConversationTurn, input string) []types.ConversationTurn {
	msgs := make([]types.ConversationTurn, 0, len(history)+2)
	msgs = append(msgs, types.ConversationTurn{
		Role:    "user",
		Content: systemPrompt,
	})
	msgs = append(msgs, types.ConversationTurn{
		Role:    "assistant",
		Content: "Understood. Send me the message to classify.",
	})
	msgs = append(msgs, history...)
	msgs = append(msgs, types.ConversationTurn{
		Role:    "user",
		Content: input,
	})
	return msgs
}

// classifyResponse is the expected JSON shape from the LLM.
type classifyResponse struct {
	Intent string `json:"intent"`
}

// parseIntent extracts the Intent from the LLM's JSON response. On any parse
// or validation error it returns IntentUnknown.
func parseIntent(raw string) Intent {
	raw = strings.TrimSpace(raw)

	// Strip markdown code fences if the model wrapped its response.
	if strings.HasPrefix(raw, "```") {
		lines := strings.Split(raw, "\n")
		if len(lines) >= 3 {
			raw = strings.Join(lines[1:len(lines)-1], "\n")
		}
	}

	var result classifyResponse
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return IntentUnknown
	}

	switch Intent(result.Intent) {
	case IntentComms, IntentBuilder, IntentResearch, IntentUnknown:
		return Intent(result.Intent)
	default:
		return IntentUnknown
	}
}

// Compile-time check: *LLMClassifier satisfies IntentClassifier.
var _ IntentClassifier = (*LLMClassifier)(nil)
