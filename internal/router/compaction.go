package router

import (
	"context"
	"fmt"
	"strings"

	"github.com/marcoantonios1/Agent-OS/internal/costguard"
	"github.com/marcoantonios1/Agent-OS/internal/types"
)

const (
	defaultCompactionThreshold = 6000 // estimated tokens; rough rule: chars / 4
	keepRecentTurns            = 10   // turns always sent verbatim
)

// estimateTokens returns a rough token count for the history (chars / 4).
func estimateTokens(history []types.ConversationTurn) int {
	total := 0
	for _, t := range history {
		total += len(t.Content)
		for _, p := range t.Parts {
			total += len(p.Text)
		}
	}
	return total / 4
}

// compact returns a shortened history when its estimated token count exceeds
// threshold. The most recent keepRecentTurns turns are preserved verbatim;
// everything before them is replaced by a single system summary turn.
//
// Returns the original slice unchanged when:
//   - threshold == 0 (disabled)
//   - estimated tokens are below threshold
//   - history is too short to have an "older" section
func compact(
	ctx context.Context,
	llm costguard.LLMClient,
	model string,
	threshold int,
	history []types.ConversationTurn,
) ([]types.ConversationTurn, error) {
	if threshold == 0 || estimateTokens(history) < threshold {
		return history, nil
	}
	if len(history) <= keepRecentTurns {
		return history, nil
	}

	older := history[:len(history)-keepRecentTurns]
	recent := history[len(history)-keepRecentTurns:]

	var sb strings.Builder
	for _, t := range older {
		if t.Content == "" {
			continue
		}
		sb.WriteString(t.Role)
		sb.WriteString(": ")
		sb.WriteString(t.Content)
		sb.WriteString("\n")
	}

	req := costguard.CompletionRequest{
		Model: model,
		Messages: []types.ConversationTurn{
			{
				Role: "system",
				Content: "Summarise the following conversation excerpt concisely. " +
					"Preserve all key facts, decisions, names, and context that " +
					"a future reader would need to continue the conversation naturally. " +
					"Output only the summary — no preamble.",
			},
			{Role: "user", Content: sb.String()},
		},
		MaxTokens: 512,
	}

	resp, err := llm.Complete(ctx, req)
	if err != nil {
		return history, fmt.Errorf("compact: summarise: %w", err)
	}

	summary := types.ConversationTurn{
		Role:    "system",
		Content: "[Earlier conversation summary: " + resp.Content + "]",
	}
	return append([]types.ConversationTurn{summary}, recent...), nil
}
