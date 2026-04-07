package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/marcoantonios1/Agent-OS/internal/costguard"
	"github.com/marcoantonios1/Agent-OS/internal/types"
)

const defaultMaxSteps = 10

// AgenticLoop runs the LLM → tool-call → execute → feed-back cycle until the
// model returns a plain text response or the step limit is reached.
//
// Flow for each step:
//  1. Call LLM with current messages and tool definitions.
//  2. If the response contains tool calls → execute each, append results, repeat.
//  3. If the response contains text (no tool calls) → return it.
type AgenticLoop struct {
	// Client is the LLM client used for every completion call.
	Client costguard.LLMClient
	// Registry holds the tools the LLM may call.
	Registry *ToolRegistry
	// MaxSteps caps the number of LLM round-trips (default 10).
	// Set to 0 to use the default.
	MaxSteps int
}

// toolCallRecord is serialised into the conversation so the LLM can see what
// it previously requested.
type toolCallRecord struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// toolResultRecord is serialised into the conversation so the LLM can see what
// each tool returned.
type toolResultRecord struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Result string `json:"result,omitempty"`
	Error  string `json:"error,omitempty"`
}

// Run executes the agentic loop starting from req. Tool definitions from the
// registry are injected into every CompletionRequest automatically — callers
// should not set req.Tools.
//
// Returns the final text response from the LLM.
func (l *AgenticLoop) Run(ctx context.Context, req costguard.CompletionRequest) (string, error) {
	maxSteps := l.MaxSteps
	if maxSteps <= 0 {
		maxSteps = defaultMaxSteps
	}

	// Work on a copy of the message slice so the caller's slice is not mutated.
	msgs := make([]types.ConversationTurn, len(req.Messages))
	copy(msgs, req.Messages)

	req.Tools = l.Registry.Definitions()

	for step := range maxSteps {
		req.Messages = msgs

		resp, err := l.Client.Complete(ctx, req)
		if err != nil {
			return "", fmt.Errorf("agentic loop step %d: LLM error: %w", step+1, err)
		}

		// No tool calls → the model produced a final text response.
		if len(resp.ToolCalls) == 0 {
			slog.InfoContext(ctx, "agentic loop complete", "steps", step+1)
			return resp.Content, nil
		}

		slog.InfoContext(ctx, "agentic loop tool calls",
			"step", step+1, "count", len(resp.ToolCalls))

		// Record what the assistant requested so the LLM sees its own decision.
		records := make([]toolCallRecord, len(resp.ToolCalls))
		for i, tc := range resp.ToolCalls {
			records[i] = toolCallRecord{ID: tc.ID, Name: tc.Name, Arguments: tc.Arguments}
		}
		assistantContent, _ := json.Marshal(records)
		msgs = append(msgs, types.ConversationTurn{
			Role:    "assistant",
			Content: string(assistantContent),
		})

		// Execute each tool and collect results.
		results := make([]toolResultRecord, 0, len(resp.ToolCalls))
		for _, tc := range resp.ToolCalls {
			result, execErr := l.Registry.Execute(ctx, tc.Name, json.RawMessage(tc.Arguments))
			rec := toolResultRecord{ID: tc.ID, Name: tc.Name}
			if execErr != nil {
				slog.WarnContext(ctx, "tool execution error",
					"tool", tc.Name, "error", execErr)
				rec.Error = execErr.Error()
			} else {
				rec.Result = result
			}
			results = append(results, rec)
		}

		// Feed all results back as a single user turn.
		resultsContent, _ := json.Marshal(results)
		msgs = append(msgs, types.ConversationTurn{
			Role:    "user",
			Content: string(resultsContent),
		})
	}

	return "", fmt.Errorf("agentic loop: exceeded %d steps without a final response", maxSteps)
}
