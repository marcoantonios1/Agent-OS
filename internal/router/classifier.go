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

// IntentClassifier classifies an inbound message into an ordered list of
// Intents. A single-intent message returns a one-element slice. A compound
// message (e.g. "reply to that email AND continue building the app") returns
// intents in the order the user stated them.
type IntentClassifier interface {
	Classify(ctx context.Context, sessionID, input string, history []types.ConversationTurn) ([]Intent, error)
}

const classifierModel = "claude-sonnet-4-6"

const systemPrompt = `You are an intent classifier for a multi-agent AI system.
Your job is to read the user's latest message (and optionally the conversation
history) and return a JSON object with an "intents" array.

The valid intent values are:

- "comms"    – Anything involving the user's own emails or calendar.
               This includes: reading, listing, searching, summarising, drafting,
               sending emails; checking, creating, or updating calendar events;
               reminders and scheduling.
               The key signal: the user is asking about THEIR inbox or THEIR calendar.
               Examples: "Send Alice an email about the meeting",
                         "Check my latest emails",
                         "Summarise the emails I received today",
                         "What are my last 5 emails?",
                         "Read that email from Bob",
                         "Remind me to call the dentist",
                         "What's on my calendar today?",
                         "Schedule a meeting with Alice tomorrow at 3pm"

- "builder"  – Writing, editing, reviewing, or debugging code; creating scripts,
               configuration files, or any software artefact; explaining how
               a piece of code works.
               Examples: "Write a Python function to parse CSV",
                         "Why is my Go build failing?",
                         "Refactor this function to use generics"

- "research" – Searching the web or external sources for information; summarising
               articles, news, or documents found online; answering factual questions
               about the world; comparing external options or analysing public data.
               NOT for emails or calendar — those are always "comms".
               Examples: "What are the main differences between REST and GraphQL?",
                         "Summarise the latest news about climate change",
                         "Which database is better for time-series data?",
                         "Search for recent papers on LLMs"

- "unknown"  – The message is ambiguous, off-topic, or cannot be reliably
               classified into any of the above categories.

## Compound requests
If the user asks for multiple distinct tasks that belong to different agents,
return all applicable intents IN THE ORDER the user stated them.

Examples:
- "Reply to that investor email, then continue building the landing page"
  → {"intents": ["comms", "builder"]}
- "Research GraphQL vs REST, then write me an implementation"
  → {"intents": ["research", "builder"]}
- "Send an email to Alice"
  → {"intents": ["comms"]}
- "Check my emails and summarise them"
  → {"intents": ["comms"]}
- "What are my last 5 emails?"
  → {"intents": ["comms"]}

Respond with ONLY valid JSON. No markdown, no explanation, no extra keys.
Always use the "intents" array — never a bare "intent" string.
Example responses:
  {"intents": ["builder"]}
  {"intents": ["comms", "builder"]}`

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
// returned JSON intent list. Falls back to [IntentUnknown] on any parse failure.
func (c *LLMClassifier) Classify(ctx context.Context, sessionID, input string, history []types.ConversationTurn) ([]Intent, error) {
	messages := buildMessages(history, input)

	req := costguard.CompletionRequest{
		Model:     classifierModel,
		Messages:  messages,
		MaxTokens: 64, // response is always a small JSON array
	}

	resp, err := c.client.Complete(ctx, req)
	if err != nil {
		c.log.WarnContext(ctx, "classifier LLM call failed, defaulting to unknown",
			"session_id", sessionID, "error", err)
		return []Intent{IntentUnknown}, fmt.Errorf("classify: LLM call failed: %w", err)
	}

	intents := parseIntents(resp.Content)
	c.log.InfoContext(ctx, "intent classified",
		"session_id", sessionID,
		"intents", intents,
		"raw", resp.Content,
	)
	return intents, nil
}

// buildMessages constructs the message slice for the classifier request.
func buildMessages(history []types.ConversationTurn, input string) []types.ConversationTurn {
	msgs := make([]types.ConversationTurn, 0, len(history)+2)
	msgs = append(msgs, types.ConversationTurn{
		Role:    "user",
		Content: systemPrompt,
	})
	msgs = append(msgs, types.ConversationTurn{
		Role:    "assistant",
		Content: `Understood. Send me the message to classify and I'll return {"intents":[...]}`,
	})
	msgs = append(msgs, history...)
	msgs = append(msgs, types.ConversationTurn{
		Role:    "user",
		Content: input,
	})
	return msgs
}

// classifyResponse accepts both the new array format and the legacy single-
// intent format so tests and older integrations keep working.
type classifyResponse struct {
	Intents []string `json:"intents"` // preferred: ["comms","builder"]
	Intent  string   `json:"intent"`  // legacy fallback: "comms"
}

// parseIntents extracts the []Intent from the LLM's JSON response.
// On any parse or validation error it returns [IntentUnknown].
// Supports both {"intents":["comms","builder"]} and legacy {"intent":"comms"}.
func parseIntents(raw string) []Intent {
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
		return []Intent{IntentUnknown}
	}

	// New format: intents array.
	if len(result.Intents) > 0 {
		out := make([]Intent, 0, len(result.Intents))
		for _, s := range result.Intents {
			intent := Intent(s)
			switch intent {
			case IntentComms, IntentBuilder, IntentResearch, IntentUnknown:
				out = append(out, intent)
			default:
				out = append(out, IntentUnknown)
			}
		}
		return out
	}

	// Legacy fallback: single "intent" string.
	if result.Intent != "" {
		switch Intent(result.Intent) {
		case IntentComms, IntentBuilder, IntentResearch, IntentUnknown:
			return []Intent{Intent(result.Intent)}
		}
	}

	return []Intent{IntentUnknown}
}

// parseIntent is kept for backward-compat with existing unit tests.
func parseIntent(raw string) Intent {
	return parseIntents(raw)[0]
}

// Compile-time check: *LLMClassifier satisfies IntentClassifier.
var _ IntentClassifier = (*LLMClassifier)(nil)
