package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/marcoantonios1/Agent-OS/internal/costguard"
	"github.com/marcoantonios1/Agent-OS/internal/memory/episodic"
	"github.com/marcoantonios1/Agent-OS/internal/sessions"
)

// SearchTool implements memory_search.
type SearchTool struct {
	store episodic.Store
}

// NewSearchTool returns a SearchTool backed by the given episodic.Store.
func NewSearchTool(store episodic.Store) *SearchTool {
	return &SearchTool{store: store}
}

func (t *SearchTool) Definition() costguard.ToolDefinition {
	return costguard.ToolDefinition{
		Name:        "memory_search",
		Description: "Search long-term memory for facts related to a query. Returns matching memories with their timestamps.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{
					"type":        "string",
					"description": "What to search for in memory.",
				},
				"limit": map[string]any{
					"type":        "integer",
					"description": "Maximum number of memories to return. Default 5.",
				},
			},
			"required": []string{"query"},
		},
	}
}

type searchInput struct {
	Query string `json:"query"`
	Limit int    `json:"limit"`
}

func (t *SearchTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var in searchInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return "", fmt.Errorf("memory_search: invalid input: %w", err)
	}
	if in.Query == "" {
		return "", fmt.Errorf("memory_search: query must not be empty")
	}
	if in.Limit <= 0 {
		in.Limit = 5
	}

	userID := sessions.UserIDFromContext(ctx)
	memories, err := t.store.SearchByText(ctx, userID, in.Query, in.Limit)
	if err != nil {
		return "", fmt.Errorf("memory_search: %w", err)
	}

	if len(memories) == 0 {
		return "No relevant memories found.", nil
	}

	now := time.Now()
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Found %d relevant memories:\n", len(memories)))
	for i, m := range memories {
		age := now.Sub(m.CreatedAt)
		sb.WriteString(fmt.Sprintf("%d. [%s] %s\n", i+1, formatMemoryAge(age), m.Content))
	}
	return sb.String(), nil
}

func formatMemoryAge(d time.Duration) string {
	days := int(d.Hours() / 24)
	switch {
	case days < 1:
		return "today"
	case days == 1:
		return "1 day ago"
	case days < 7:
		return fmt.Sprintf("%d days ago", days)
	case days < 14:
		return "1 week ago"
	case days < 30:
		return fmt.Sprintf("%d weeks ago", days/7)
	case days < 60:
		return "1 month ago"
	default:
		return fmt.Sprintf("%d months ago", days/30)
	}
}
