package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/marcoantonios1/Agent-OS/internal/costguard"
	"github.com/marcoantonios1/Agent-OS/internal/types"
)

// RunStream executes the agentic loop using Complete() for tool-call steps and
// Stream() for the final text generation step. Returns a channel of tokens.
// The channel is closed when the stream ends, the step limit is reached, or ctx
// is cancelled.
func (l *AgenticLoop) RunStream(ctx context.Context, req costguard.CompletionRequest) (<-chan string, error) {
	maxSteps := l.MaxSteps
	if maxSteps <= 0 {
		maxSteps = defaultMaxSteps
	}

	msgs := make([]types.ConversationTurn, len(req.Messages))
	copy(msgs, req.Messages)
	req.Tools = l.Registry.Definitions()

	for step := range maxSteps {
		req.Messages = msgs

		resp, err := l.Client.Complete(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("agentic loop stream step %d: LLM error: %w", step+1, err)
		}

		if len(resp.ToolCalls) == 0 {
			// All tool work is done — stream the final reply.
			slog.InfoContext(ctx, "agentic loop stream: dispatching final step", "tool_steps", step)
			chunks, err := l.Client.Stream(ctx, req)
			if err != nil {
				return nil, fmt.Errorf("agentic loop stream final step: %w", err)
			}
			return streamChunksToStrings(ctx, chunks), nil
		}

		slog.InfoContext(ctx, "agentic loop stream tool calls", "step", step+1, "count", len(resp.ToolCalls))
		msgs = append(msgs, types.ConversationTurn{Role: "assistant", ToolCalls: resp.ToolCalls})
		for _, tc := range resp.ToolCalls {
			result, execErr := l.Registry.Execute(ctx, tc.Name, json.RawMessage(tc.Arguments))
			var content string
			if execErr != nil {
				slog.WarnContext(ctx, "tool execution error", "tool", tc.Name, "error", execErr)
				content = fmt.Sprintf(`{"error":%q}`, execErr.Error())
			} else {
				content = result
			}
			msgs = append(msgs, types.ConversationTurn{
				Role:       "tool",
				ToolCallID: tc.ID,
				Content:    content,
			})
		}
	}

	return nil, fmt.Errorf("agentic loop stream: exceeded %d steps without a final response", maxSteps)
}

// streamChunksToStrings adapts a <-chan costguard.StreamChunk to <-chan string,
// forwarding non-empty Content and stopping on Done, Error, or ctx cancellation.
func streamChunksToStrings(ctx context.Context, in <-chan costguard.StreamChunk) <-chan string {
	out := make(chan string)
	go func() {
		defer close(out)
		for {
			select {
			case chunk, ok := <-in:
				if !ok {
					return
				}
				if chunk.Error != nil {
					slog.WarnContext(ctx, "stream chunk error", "error", chunk.Error)
					return
				}
				if chunk.Content != "" {
					select {
					case out <- chunk.Content:
					case <-ctx.Done():
						return
					}
				}
				if chunk.Done {
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()
	return out
}

const defaultMaxSteps = 10

// AgenticLoop runs the LLM → tool-call → execute → feed-back cycle until the
// model returns a plain text response or the step limit is reached.
//
// Each round-trip follows the OpenAI tool-use protocol:
//  1. Call LLM with current messages and tool definitions.
//  2. If the response contains tool calls → record them as an assistant turn,
//     execute each, append one tool-role turn per result, then repeat.
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

		// Append assistant turn recording which tools were requested.
		msgs = append(msgs, types.ConversationTurn{
			Role:      "assistant",
			ToolCalls: resp.ToolCalls,
		})

		// Execute each tool and append one tool-role turn per result.
		for _, tc := range resp.ToolCalls {
			result, execErr := l.Registry.Execute(ctx, tc.Name, json.RawMessage(tc.Arguments))
			var content string
			if execErr != nil {
				slog.WarnContext(ctx, "tool execution error",
					"tool", tc.Name, "error", execErr)
				content = fmt.Sprintf(`{"error":%q}`, execErr.Error())
			} else {
				content = result
			}
			msgs = append(msgs, types.ConversationTurn{
				Role:       "tool",
				ToolCallID: tc.ID,
				Content:    content,
			})
		}
	}

	return "", fmt.Errorf("agentic loop: exceeded %d steps without a final response", maxSteps)
}
