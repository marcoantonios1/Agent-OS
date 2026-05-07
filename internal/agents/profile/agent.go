// Package profile implements the Profile Agent — a background observer that
// analyses completed conversations and extracts personality signals for each user.
// It never blocks the response path: callers must invoke Observe in a goroutine.
package profile

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/marcoantonios1/Agent-OS/internal/costguard"
	"github.com/marcoantonios1/Agent-OS/internal/sessions"
	"github.com/marcoantonios1/Agent-OS/internal/types"
)

// minTurns is the minimum number of conversation turns required before Observe
// will call the LLM. Short exchanges don't carry enough signal to be useful.
const minTurns = 3

const observePrompt = `You are analysing a conversation transcript to identify behavioural traits of the USER.

For each observable trait, output exactly one line in this format:
KEY=VALUE

Allowed keys and their allowed values (only use these exact strings):
response_length=brief|detailed|verbose
technical_depth=low|medium|high
communication_style=formal|casual|direct
humor_tolerance=none|light|high
question_style=asks_followup|assumes|guesses
working_hours=morning|evening|night|mixed
urgency_pattern=high|medium|low
topic_interests=<comma-separated topics>

Rules:
- Only output traits you can observe with confidence from the transcript.
- If there is no clear evidence for a trait, do not include it.
- Output ONLY the KEY=VALUE lines, nothing else — no headers, no explanation, no blank lines.
- If no traits are detectable, output nothing at all.

Example output:
response_length=brief
technical_depth=high
topic_interests=golang,concurrency`

// signal is a key=value personality trait extracted from the LLM response.
type signal struct {
	Key   string
	Value string
}

// Agent analyses conversation history and persists personality signals.
type Agent struct {
	llm   costguard.LLMClient
	store sessions.PersonalityStore
	model string
	log   *slog.Logger
}

// New returns a Profile Agent using the provided LLM client, personality store,
// and model identifier.
func New(llm costguard.LLMClient, store sessions.PersonalityStore, model string) *Agent {
	return &Agent{
		llm:   llm,
		store: store,
		model: model,
		log:   slog.Default(),
	}
}

// Observe analyses history and upserts detected personality signals for userID.
// Returns nil without calling the LLM when history has fewer than minTurns turns.
// A bad JSON response from the LLM is logged and treated as a no-op (not an error)
// so transient model failures never surface to the user.
func (a *Agent) Observe(ctx context.Context, userID string, history []types.ConversationTurn) error {
	if len(history) < minTurns {
		return nil
	}

	// Format the conversation as plain text inside a single user message.
	// Passing the conversation as real chat turns causes models to treat themselves
	// as a participant continuing the chat rather than an external analyser, which
	// results in empty or conversational (non-JSON) completions.
	var transcript strings.Builder
	transcript.WriteString("Analyse the following conversation transcript and output the personality signals JSON array.\n\n<transcript>\n")
	for _, turn := range history {
		switch turn.Role {
		case "user":
			transcript.WriteString("User: ")
			transcript.WriteString(turn.Content)
			transcript.WriteString("\n")
		case "assistant":
			transcript.WriteString("Assistant: ")
			transcript.WriteString(turn.Content)
			transcript.WriteString("\n")
		}
	}
	transcript.WriteString("</transcript>")

	messages := []types.ConversationTurn{
		{Role: "system", Content: observePrompt},
		{Role: "user", Content: transcript.String()},
	}

	resp, err := a.llm.Complete(ctx, costguard.CompletionRequest{
		Model:     a.model,
		Messages:  messages,
		MaxTokens: 512,
	})
	if err != nil {
		return fmt.Errorf("profile observe: llm: %w", err)
	}

	content := strings.TrimSpace(resp.Content)
	if content == "" {
		a.log.Warn("profile observe: empty LLM response",
			"user_id", userID, "model", a.model, "turns", len(history))
		return nil
	}

	signals := parseKeyValue(content)

	for _, s := range signals {
		if upsertErr := a.store.UpsertSignal(userID, sessions.PersonalitySignal{
			Key:   s.Key,
			Value: s.Value,
		}); upsertErr != nil {
			a.log.Warn("profile observe: upsert signal",
				"user_id", userID, "key", s.Key, "error", upsertErr)
		}
	}
	if len(signals) > 0 {
		a.log.Info("profile observe: signals stored", "user_id", userID, "count", len(signals))
	}
	return nil
}

// parseKeyValue parses the LLM's KEY=VALUE line format into signals.
// Lines that don't contain "=" are silently skipped so stray prose doesn't crash parsing.
func parseKeyValue(s string) []signal {
	// Strip markdown code fences if the model wrapped its output anyway.
	if strings.HasPrefix(s, "```") {
		if idx := strings.Index(s, "\n"); idx >= 0 {
			s = s[idx+1:]
		}
		if idx := strings.LastIndex(s, "```"); idx >= 0 {
			s = s[:idx]
		}
		s = strings.TrimSpace(s)
	}

	var out []signal
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		idx := strings.IndexByte(line, '=')
		if idx <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		val := strings.TrimSpace(line[idx+1:])
		if key != "" && val != "" {
			out = append(out, signal{Key: key, Value: val})
		}
	}
	return out
}
