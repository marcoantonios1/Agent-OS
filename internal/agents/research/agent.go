// Package research implements the Research Agent — an AI assistant that uses
// web_search and web_fetch tools to find, read, and synthesise information.
package research

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/marcoantonios1/Agent-OS/internal/costguard"
	"github.com/marcoantonios1/Agent-OS/internal/tools"
	"github.com/marcoantonios1/Agent-OS/internal/tools/websearch"
	"github.com/marcoantonios1/Agent-OS/internal/types"
)

const agentID = types.AgentID("research")

const systemPrompt = `You are the Research Agent for Agent OS.
Your job is to find accurate, up-to-date information using web search and answer the user's question thoroughly.

## How to work
1. Use web_search to find relevant sources for the user's query.
2. Use web_fetch on the most promising URLs to read their full content.
3. Synthesise what you found into a clear, well-structured answer.
4. Always cite your sources — include the URL for any fact you reference.

## Rules
- Never make up information. If you cannot find a reliable source, say so.
- If the first search does not return useful results, try a refined query.
- Keep answers concise but complete. Use markdown for structure when it helps.
- Do not answer questions about the user's own email or calendar — those belong to the Comms Agent.`

// Agent implements the Research Agent using web search tools.
type Agent struct {
	loop *tools.AgenticLoop
}

// New constructs a Research Agent with the given LLM client and search provider.
func New(llm costguard.LLMClient, provider websearch.SearchProvider) *Agent {
	reg := websearch.NewWebSearchRegistry(provider)
	return &Agent{
		loop: &tools.AgenticLoop{
			Client:   llm,
			Registry: reg,
			MaxSteps: 10,
		},
	}
}

// Handle processes a single user research request.
func (a *Agent) Handle(ctx context.Context, req types.AgentRequest) (types.AgentResponse, error) {
	slog.InfoContext(ctx, "agent_start",
		"agent_id", string(agentID),
		"session_id", req.SessionID,
	)
	start := time.Now()

	msgs := make([]types.ConversationTurn, 0, len(req.History)+2)
	msgs = append(msgs, types.ConversationTurn{Role: "system", Content: systemPrompt})
	msgs = append(msgs, req.History...)
	msgs = append(msgs, types.ConversationTurn{Role: "user", Content: req.Input})

	output, err := a.loop.Run(ctx, costguard.CompletionRequest{
		Model:     "claude-sonnet-4-6",
		Messages:  msgs,
		MaxTokens: 4096,
	})
	if err != nil {
		return types.AgentResponse{}, fmt.Errorf("research agent: %w", err)
	}

	slog.InfoContext(ctx, "agent_complete",
		"agent_id", string(agentID),
		"session_id", req.SessionID,
		"latency_ms", time.Since(start).Milliseconds(),
	)

	return types.AgentResponse{
		AgentID: agentID,
		Output:  output,
	}, nil
}
