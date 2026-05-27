package episodic

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/marcoantonios1/Agent-OS/internal/costguard"
	"github.com/marcoantonios1/Agent-OS/internal/types"
)

const extractionPrompt = `You extract memorable facts from conversations for a personal AI assistant.
Extract ONLY facts that would be useful to remember in future conversations.
Ignore small talk, questions about the weather, and transient requests.
Extract facts about:

People the user mentions (name, relationship, context)
Emotional states or reactions ("the meeting was stressful")
Preferences expressed ("I prefer short summaries")
Events and outcomes ("the project was approved")
Problems or blockers mentioned ("the API is broken")
Commitments made ("I told Alice I'd send the report by Friday")

Return a JSON array of strings. Each string is one memory.
Return [] if nothing is worth remembering.
Return only valid JSON with no preamble.
Example output:
["User's colleague Alice is difficult to work with on budget topics",
"User committed to sending a report to Alice by Friday",
"User prefers morning meetings over afternoon ones"]`

// Extractor uses an LLM to identify memorable facts from a conversation turn,
// then embeds and persists them to the episodic Store.
// All public methods are safe for concurrent use.
type Extractor struct {
	client costguard.LLMClient
	store  Store
	model  string
	log    *slog.Logger
}

// NewExtractor returns an Extractor. model is the LLM model identifier;
// store is the episodic Store where extracted memories are saved.
func NewExtractor(client costguard.LLMClient, store Store, model string) *Extractor {
	return &Extractor{
		client: client,
		store:  store,
		model:  model,
		log:    slog.Default(),
	}
}

// Extract calls the LLM with the extraction prompt and returns a list of
// memorable fact strings. Returns an empty slice (not an error) when the LLM
// returns [] or a response that cannot be parsed.
func (e *Extractor) Extract(ctx context.Context, userMsg, agentMsg string) ([]string, error) {
	turnText := fmt.Sprintf("User: %s\n\nAssistant: %s", userMsg, agentMsg)

	messages := []types.ConversationTurn{
		{Role: "system", Content: extractionPrompt},
		{Role: "user", Content: turnText},
	}

	resp, err := e.client.Complete(ctx, costguard.CompletionRequest{
		Model:     e.model,
		Messages:  messages,
		MaxTokens: 512,
	})
	if err != nil {
		return nil, fmt.Errorf("episodic extract: llm: %w", err)
	}

	raw := strings.TrimSpace(resp.Content)
	if raw == "" || raw == "[]" {
		return nil, nil
	}

	// Strip markdown code fences if the model wrapped the JSON.
	if strings.HasPrefix(raw, "```") {
		if idx := strings.Index(raw, "\n"); idx >= 0 {
			raw = raw[idx+1:]
		}
		if idx := strings.LastIndex(raw, "```"); idx >= 0 {
			raw = raw[:idx]
		}
		raw = strings.TrimSpace(raw)
	}

	var memories []string
	if err := json.Unmarshal([]byte(raw), &memories); err != nil {
		return nil, fmt.Errorf("episodic extract: parse json: %w", err)
	}
	return memories, nil
}

// ObserveAsync extracts memories from the completed turn and saves them
// to the episodic store, all in a background goroutine. Never blocks. Logs
// warnings on failure; never panics.
func (e *Extractor) ObserveAsync(
	userID, sessionID, channel, userMsg, agentMsg string,
) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		memories, err := e.Extract(ctx, userMsg, agentMsg)
		if err != nil {
			e.log.Warn("episodic extractor: extract failed",
				"user_id", userID, "error", err)
			return
		}
		if len(memories) == 0 {
			return
		}

		for _, content := range memories {
			mem := Memory{
				ID:         uuid.NewString(),
				UserID:     userID,
				Channel:    channel,
				SessionID:  sessionID,
				Content:    content,
				Source:     "conversation",
				Importance: 0.5,
				CreatedAt:  time.Now().UTC(),
			}
			if err := e.store.SaveText(ctx, mem); err != nil {
				preview := content
				if len(preview) > 60 {
					preview = preview[:60]
				}
				e.log.Warn("episodic extractor: save failed",
					"user_id", userID, "content", preview, "error", err)
			}
		}

		e.log.Info("episodic extractor: memories saved",
			"user_id", userID, "count", len(memories))
	}()
}
