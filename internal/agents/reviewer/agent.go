// Package reviewer implements the Reviewer Agent — a sub-agent invoked by the
// Builder Agent at the end of codegen to check generated files and run tests.
// It is never reachable directly by users; the Router registers it only as a
// sub-agent target for SubAgentCaller.Call("reviewer", prompt).
package reviewer

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/marcoantonios1/Agent-OS/internal/costguard"
	"github.com/marcoantonios1/Agent-OS/internal/tools"
	"github.com/marcoantonios1/Agent-OS/internal/types"
)

const agentID = types.AgentID("reviewer")

const systemPrompt = `You are the Reviewer Agent for Agent OS.

You receive a description of a just-generated project. Your job is to review
the code and report whether it is ready to ship.

## Steps (follow in order)
1. Call file_list to discover what files were generated.
2. Call file_read on the key files — entry points first, then test files.
3. Call shell_run to execute the test suite (e.g. "go test ./..." for Go,
   "npm test" for Node, "pytest" for Python). If there is no test suite,
   run a build/lint check instead (e.g. "go build ./...").
4. Analyse the results.

## Report format
Produce a structured review in this exact format:

### Files Reviewed
- <filename> — <one-line assessment>

### Test Results
<paste the full shell output>

### Issues Found
- <specific problem — include file path and line number where possible>
(write "None" if no issues)

### Suggestions
- <actionable recommendation>
(write "None" if no suggestions)

### Verdict: APPROVED | NEEDS_WORK | BLOCKED

Use APPROVED   when all tests pass and code quality is acceptable.
Use NEEDS_WORK when there are fixable issues (failing tests, obvious bugs,
                missing error handling, incomplete implementation).
Use BLOCKED    when there are fundamental architectural problems that require
                a redesign before any fix is meaningful.`

// Agent implements the Reviewer Agent.
type Agent struct {
	loop  *tools.AgenticLoop
	model string
}

// New constructs a Reviewer Agent.
//
//   - llm is the LLM client (Costguard gateway).
//   - reg is a pre-built tool registry — the caller is responsible for
//     registering all required tools before passing it in.
//   - model is the LLM model name used for all completions.
func New(llm costguard.LLMClient, reg *tools.ToolRegistry, model string) *Agent {
	return &Agent{
		loop: &tools.AgenticLoop{
			Client:   llm,
			Registry: reg,
		},
		model: model,
	}
}

// Handle runs the reviewer loop. req.Input should contain the review prompt
// built by the Builder Agent (spec context + any relevant file hints).
func (a *Agent) Handle(ctx context.Context, req types.AgentRequest) (types.AgentResponse, error) {
	slog.InfoContext(ctx, "agent_start",
		"agent_id", string(agentID),
		"session_id", req.SessionID,
	)
	start := time.Now()

	msgs := []types.ConversationTurn{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: req.Input},
	}

	output, err := a.loop.Run(ctx, costguard.CompletionRequest{
		Model:     a.model,
		Messages:  msgs,
		MaxTokens: 4096,
	})
	if err != nil {
		return types.AgentResponse{}, fmt.Errorf("reviewer agent: %w", err)
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
