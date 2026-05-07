// Package profile implements the Profile Agent — a background observer that
// analyses completed conversations and extracts personality signals for each user.
// It never blocks the response path: callers must invoke Observe in a goroutine.
package profile

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/marcoantonios1/Agent-OS/internal/costguard"
	"github.com/marcoantonios1/Agent-OS/internal/sessions"
	"github.com/marcoantonios1/Agent-OS/internal/types"
)

// minTurns is the minimum number of conversation turns required before Observe
// will call the LLM. Short exchanges don't carry enough signal to be useful.
const minTurns = 3

const observePrompt = `You are a personality-signal extractor. Given a conversation between a user and an AI assistant, identify observable behavioural traits and output them as a JSON array.

Each signal object must have exactly two fields:
- "key":   one of the allowed keys listed below
- "value": the observed value for that key

Allowed keys and values:
- response_length:     "brief" | "detailed" | "verbose"
- technical_depth:     "low" | "medium" | "high"
- communication_style: "formal" | "casual" | "direct"
- humor_tolerance:     "none" | "light" | "high"
- question_style:      "asks_followup" | "assumes" | "guesses"
- working_hours:       "morning" | "evening" | "night" | "mixed"
- urgency_pattern:     "high" | "medium" | "low"
- topic_interests:     comma-separated list of topics the user showed genuine interest in

Rules:
- Only include signals you can observe with confidence from this specific conversation.
- If you have no clear evidence for a signal, omit it entirely.
- Output ONLY valid JSON — no prose, no markdown fences, no explanation.
- If no signals are detectable, output an empty array: []

Example output:
[{"key":"response_length","value":"brief"},{"key":"technical_depth","value":"high"}]`

// signal is the minimal JSON shape the LLM returns.
type signal struct {
	Key   string `json:"key"`
	Value string `json:"value"`
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

	messages := make([]types.ConversationTurn, 0, len(history)+1)
	messages = append(messages, types.ConversationTurn{Role: "system", Content: observePrompt})
	messages = append(messages, history...)

	resp, err := a.llm.Complete(ctx, costguard.CompletionRequest{
		Model:     a.model,
		Messages:  messages,
		MaxTokens: 512,
	})
	if err != nil {
		return fmt.Errorf("profile observe: llm: %w", err)
	}

	var signals []signal
	if err := json.Unmarshal([]byte(resp.Content), &signals); err != nil {
		a.log.Warn("profile observe: could not parse LLM response",
			"user_id", userID, "response", resp.Content, "error", err)
		return nil
	}

	for _, s := range signals {
		if upsertErr := a.store.UpsertSignal(userID, sessions.PersonalitySignal{
			Key:   s.Key,
			Value: s.Value,
		}); upsertErr != nil {
			a.log.Warn("profile observe: upsert signal",
				"user_id", userID, "key", s.Key, "error", upsertErr)
		}
	}
	return nil
}
