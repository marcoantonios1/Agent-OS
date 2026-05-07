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
	// IntentReviewer routes to the reviewer agent. It is a sub-agent only —
	// users cannot invoke it directly and it never appears in the classifier prompt.
	// The Builder Agent calls it via SubAgentCaller.Call("reviewer", prompt).
	IntentReviewer Intent = "reviewer"
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

- "builder"  – Building, planning, or designing software products or features:
               writing, editing, reviewing, or debugging code; creating scripts,
               configuration files, or any software artefact; explaining how
               code works; gathering requirements, writing specs, or breaking a
               project into tasks — even when research is mentioned as a step
               ("research X before building" is still primarily builder).
               Examples: "Write a Python function to parse CSV",
                         "Why is my Go build failing?",
                         "Refactor this function to use generics",
                         "Build me a padel app",
                         "Build me a padel app — research competitors before writing the spec",
                         "Help me design the architecture for a booking system",
                         "Start building a todo app, look up best practices first"

- "research" – Searching the web or external sources for information; summarising
               articles, news, or documents found online; answering factual questions
               about the world; comparing external options or analysing public data.
               NOT for emails or calendar — those are always "comms".
               Examples: "What are the main differences between REST and GraphQL?",
                         "Summarise the latest news about climate change",
                         "Which database is better for time-series data?",
                         "Search for recent papers on LLMs"

- "doctor"   – Questions about health, symptoms, medical conditions, medications, or how to interpret lab results.
               Examples: "I have a headache and fever", "What does ibuprofen interact with?",
                         "What could cause lower back pain?", "Explain my blood test results"

- "companion" – The user wants to talk, vent, reflect, or think something through — casual conversation with no specific task.
               Also use this for light personal questions, small talk, or anything that is not a task for another agent.
               Examples: "I want to talk", "I've been feeling off lately", "Can we just chat?",
                         "I need to vent", "What do you think about my situation?",
                         "What should I have for lunch?", "Tell me something interesting",
                         "I'm bored", "How's your day?"

- "notes"    – Saving, finding, updating, or summarising personal notes and documents.
               Examples: "Save a note about today's meeting", "Find my note on the project plan",
                         "What notes do I have?", "Update my note about Alice", "Write a journal entry"

- "profile_query" – The user wants to know what the system has learned about them, see their inferred
               personality profile, or correct something about it.
               Examples: "What do you know about me?", "What's my personality profile?",
                         "What have you learned about me?", "Do you know my preferences?",
                         "What do you think of me?", "Tell me about myself",
                         "You've got me wrong", "That's not right about me"

- "unknown"  – use this when no other intent fits. Unknown messages are sent to the companion agent by default.

## Compound requests
If the user asks for multiple distinct tasks that belong to different agents,
return all applicable intents IN THE ORDER the user stated them.

Examples:
- "Reply to that investor email, then continue building the landing page"
  → {"intents": ["comms", "builder"]}
- "Research GraphQL vs REST, then write me an implementation"
  → {"intents": ["research", "builder"]}
- "Build me a padel app — research competitors before writing the spec"
  → {"intents": ["builder"]}
- "Build me a todo app, look up best practices first"
  → {"intents": ["builder"]}
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
  {"intents": ["comms", "builder"]}
  {"intents": ["doctor"]}
  {"intents": ["companion"]}
  {"intents": ["notes"]}`

// LLMClassifier is the production IntentClassifier that uses Costguard for
// LLM-based classification.
type LLMClassifier struct {
	client costguard.LLMClient
	model  string
	log    *slog.Logger
}

// NewLLMClassifier returns an LLMClassifier using the provided LLMClient and model.
func NewLLMClassifier(client costguard.LLMClient, model string) *LLMClassifier {
	return &LLMClassifier{
		client: client,
		model:  model,
		log:    slog.Default(),
	}
}

// Classify sends the input (and optional history) to Costguard and parses the
// returned JSON intent list. Falls back to [IntentUnknown] on any parse failure.
func (c *LLMClassifier) Classify(ctx context.Context, sessionID, input string, history []types.ConversationTurn) ([]Intent, error) {
	messages := buildMessages(history, input)

	req := costguard.CompletionRequest{
		Model:     c.model,
		Messages:  messages,
		MaxTokens: 256, // small JSON array; extra headroom for models that think before outputting
	}

	resp, err := c.client.Complete(ctx, req)
	if err != nil {
		c.log.WarnContext(ctx, "classifier LLM call failed, defaulting to unknown",
			"session_id", sessionID, "error", err)
		return []Intent{IntentUnknown}, fmt.Errorf("classify: LLM call failed: %w", err)
	}

	// Some models return empty content but populate ToolCalls even when no tools
	// are defined. Fall back to the first tool call's arguments in that case.
	raw := resp.Content
	if raw == "" && len(resp.ToolCalls) > 0 {
		raw = resp.ToolCalls[0].Arguments
	}

	intents := parseIntents(raw)
	c.log.InfoContext(ctx, "intent classified",
		"session_id", sessionID,
		"intents", intents,
		"raw", raw,
	)
	return intents, nil
}

// buildMessages constructs the message slice for the classifier request.
func buildMessages(history []types.ConversationTurn, input string) []types.ConversationTurn {
	msgs := make([]types.ConversationTurn, 0, len(history)+2)
	// Use system role so the instruction works across all model families.
	msgs = append(msgs, types.ConversationTurn{
		Role:    "system",
		Content: systemPrompt,
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

	// If the model added surrounding prose, extract the first JSON object.
	if start := strings.Index(raw, "{"); start != -1 {
		if end := strings.LastIndex(raw, "}"); end > start {
			raw = raw[start : end+1]
		}
	}

	var result classifyResponse
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return []Intent{IntentUnknown}
	}

	// New format: intents array. Pass all strings through — the router handles
	// unregistered intents gracefully, and the generic agent layer registers
	// arbitrary intent strings at startup.
	if len(result.Intents) > 0 {
		out := make([]Intent, 0, len(result.Intents))
		for _, s := range result.Intents {
			out = append(out, Intent(s))
		}
		return out
	}

	// Legacy fallback: single "intent" string.
	if result.Intent != "" {
		return []Intent{Intent(result.Intent)}
	}

	return []Intent{IntentUnknown}
}

// parseIntent is kept for backward-compat with existing unit tests.
func parseIntent(raw string) Intent {
	return parseIntents(raw)[0]
}

// Compile-time check: *LLMClassifier satisfies IntentClassifier.
var _ IntentClassifier = (*LLMClassifier)(nil)
