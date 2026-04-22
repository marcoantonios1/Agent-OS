// Package research implements the Research Agent — an AI assistant that uses
// web_search and web_fetch tools to find, read, and synthesise information
// from the live web before answering.
package research

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/marcoantonios1/Agent-OS/internal/costguard"
	"github.com/marcoantonios1/Agent-OS/internal/tools"
	"github.com/marcoantonios1/Agent-OS/internal/types"
)

const agentID = types.AgentID("research")

const systemPrompt = `You are the Research Agent for Agent OS.
Your job is to find accurate, up-to-date information and produce well-structured answers backed by real sources.

## How to work
1. Always call web_search first — never answer factual or current-events questions from memory alone.
2. Review the search results. Use web_fetch on the 1–3 most relevant URLs to read the full content.
3. Synthesise your findings into a clear, structured response using the format below.
4. If the first search returns poor results, refine the query and search again.

## Output format
Every response must use this structure:

## Findings
<Your answer here — comprehensive, accurate, written in clear prose or bullet points>

## Sources
- [Title](URL) — one-line summary of what this source contributed
- [Title](URL) — ...

## Caveats
<Any limitations, conflicting information, or things the user should verify independently.
If everything is well-sourced and consistent, write "None.">

## Rules
- Never invent URLs or fabricate facts. If you cannot find a reliable source, say so explicitly.
- Always include at least one real URL in Sources — never leave it empty.
- Prefer multiple independent sources over a single source.
- Do not answer questions about the user's email or calendar — those belong to the Comms Agent.
- Keep Findings concise but complete. Use sub-headings or bullet points when comparing options.`

// Agent implements the Research Agent using web search tools.
type Agent struct {
	loop  *tools.AgenticLoop
	model string
}

// New constructs a Research Agent with the given LLM client and tool registry.
// The registry should be built with websearch.NewWebSearchRegistry — it provides
// the web_search and web_fetch tools the agent relies on.
func New(llm costguard.LLMClient, reg *tools.ToolRegistry, model string) *Agent {
	return &Agent{
		loop: &tools.AgenticLoop{
			Client:   llm,
			Registry: reg,
			MaxSteps: 10,
		},
		model: model,
	}
}

// HandleStream is the streaming variant of Handle.
func (a *Agent) HandleStream(ctx context.Context, req types.AgentRequest) (<-chan string, error) {
	slog.InfoContext(ctx, "agent_start_stream", "agent_id", string(agentID), "session_id", req.SessionID)

	msgs := make([]types.ConversationTurn, 0, len(req.History)+2)
	msgs = append(msgs, types.ConversationTurn{Role: "system", Content: systemPrompt})
	msgs = append(msgs, req.History...)
	msgs = append(msgs, types.ConversationTurn{Role: "user", Content: req.Input})

	return a.loop.RunStream(ctx, costguard.CompletionRequest{
		Model:     a.model,
		Messages:  msgs,
		MaxTokens: 4096,
	})
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
		Model:     a.model,
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
