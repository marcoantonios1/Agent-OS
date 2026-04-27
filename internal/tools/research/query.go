// Package research provides the research_query tool, which lets the Builder
// Agent delegate web-research tasks to the Research Agent via SubAgentCaller.
package research

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/marcoantonios1/Agent-OS/internal/costguard"
	"github.com/marcoantonios1/Agent-OS/internal/types"
)

type queryInput struct {
	Query string `json:"query"`
}

// QueryTool implements the research_query tool. It dispatches the query to
// the Research Agent via SubAgentCaller and returns its plain-text findings.
type QueryTool struct {
	caller types.SubAgentCaller
}

// NewQueryTool returns a research_query tool backed by the given SubAgentCaller.
func NewQueryTool(caller types.SubAgentCaller) *QueryTool {
	return &QueryTool{caller: caller}
}

func (t *QueryTool) Definition() costguard.ToolDefinition {
	return costguard.ToolDefinition{
		Name: "research_query",
		Description: "Search the web for information relevant to the current project. " +
			"Use this during requirements or spec phases to gather context about " +
			"competitors, technologies, or existing solutions.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{
					"type":        "string",
					"description": "The research question or search query.",
				},
			},
			"required": []string{"query"},
		},
	}
}

func (t *QueryTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var in queryInput
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("research_query: invalid input: %w", err)
	}
	if in.Query == "" {
		return "", fmt.Errorf("research_query: query must not be empty")
	}
	result, err := t.caller.Call(ctx, "research", in.Query)
	if err != nil {
		return "", fmt.Errorf("research_query: %w", err)
	}
	return result, nil
}
