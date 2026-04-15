package websearch

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/marcoantonios1/Agent-OS/internal/costguard"
	"github.com/marcoantonios1/Agent-OS/internal/tools"
)

const (
	defaultSearchLimit = 5
	maxSearchLimit     = 10
	defaultMaxChars    = 4000
	maxFetchChars      = 16000
)

// NewWebSearchRegistry returns a ToolRegistry with web_search and web_fetch
// registered against the given SearchProvider.
func NewWebSearchRegistry(provider SearchProvider) *tools.ToolRegistry {
	reg := tools.NewRegistry()
	reg.Register(&SearchTool{provider: provider})
	reg.Register(&FetchTool{client: &http.Client{}})
	return reg
}

// ── web_search ────────────────────────────────────────────────────────────────

type searchInput struct {
	Query string `json:"query"`
	Limit int    `json:"limit"`
}

// SearchTool implements the web_search tool.
type SearchTool struct {
	provider SearchProvider
}

func (t *SearchTool) Definition() costguard.ToolDefinition {
	return costguard.ToolDefinition{
		Name:        "web_search",
		Description: "Search the web for current information. Returns a list of results with title, URL, and a short snippet.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{
					"type":        "string",
					"description": "The search query.",
				},
				"limit": map[string]any{
					"type":        "integer",
					"description": fmt.Sprintf("Maximum number of results to return (1–%d, default %d).", maxSearchLimit, defaultSearchLimit),
				},
			},
			"required": []string{"query"},
		},
	}
}

func (t *SearchTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var in searchInput
	if len(input) > 0 {
		if err := json.Unmarshal(input, &in); err != nil {
			return "", fmt.Errorf("web_search: invalid input: %w", err)
		}
	}
	if in.Query == "" {
		return "", fmt.Errorf("web_search: query is required")
	}
	limit := in.Limit
	if limit <= 0 {
		limit = defaultSearchLimit
	}
	if limit > maxSearchLimit {
		limit = maxSearchLimit
	}

	results, err := t.provider.Search(ctx, in.Query, limit)
	if err != nil {
		return "", fmt.Errorf("web_search: %w", err)
	}

	out, _ := json.Marshal(results)
	return string(out), nil
}

// ── web_fetch ─────────────────────────────────────────────────────────────────

type fetchInput struct {
	URL      string `json:"url"`
	MaxChars int    `json:"max_chars"`
}

// FetchTool implements the web_fetch tool.
type FetchTool struct {
	client *http.Client
}

func (t *FetchTool) Definition() costguard.ToolDefinition {
	return costguard.ToolDefinition{
		Name:        "web_fetch",
		Description: "Fetch a URL and return its readable text content. HTML tags are stripped. Use this to read the full content of a search result.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"url": map[string]any{
					"type":        "string",
					"description": "The URL to fetch.",
				},
				"max_chars": map[string]any{
					"type":        "integer",
					"description": fmt.Sprintf("Maximum characters to return (default %d, max %d).", defaultMaxChars, maxFetchChars),
				},
			},
			"required": []string{"url"},
		},
	}
}

func (t *FetchTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var in fetchInput
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("web_fetch: invalid input: %w", err)
	}
	if in.URL == "" {
		return "", fmt.Errorf("web_fetch: url is required")
	}
	maxChars := in.MaxChars
	if maxChars <= 0 {
		maxChars = defaultMaxChars
	}
	if maxChars > maxFetchChars {
		maxChars = maxFetchChars
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, in.URL, nil)
	if err != nil {
		return "", fmt.Errorf("web_fetch: build request: %w", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; AgentOS/1.0)")

	resp, err := t.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("web_fetch: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("web_fetch: server returned %s", resp.Status)
	}

	// Read up to 6× maxChars bytes pre-strip (HTML is verbose).
	raw, err := io.ReadAll(io.LimitReader(resp.Body, int64(maxChars)*6))
	if err != nil {
		return "", fmt.Errorf("web_fetch: read body: %w", err)
	}

	text := stripHTML(string(raw))
	text = truncateChars(text, maxChars)
	return text, nil
}

// ── HTML stripping ────────────────────────────────────────────────────────────

var (
	reScript     = regexp.MustCompile(`(?is)<(script|style)[^>]*>.*?</(script|style)>`)
	reTag        = regexp.MustCompile(`<[^>]+>`)
	reWhitespace = regexp.MustCompile(`[ \t]+`)
	reNewlines   = regexp.MustCompile(`\n{3,}`)
	htmlEntities = strings.NewReplacer(
		"&amp;", "&",
		"&lt;", "<",
		"&gt;", ">",
		"&quot;", `"`,
		"&#39;", "'",
		"&nbsp;", " ",
	)
)

// stripHTML removes script/style blocks, HTML tags, decodes common entities,
// and normalises whitespace into readable plain text.
func stripHTML(html string) string {
	text := reScript.ReplaceAllString(html, " ")
	text = reTag.ReplaceAllString(text, " ")
	text = htmlEntities.Replace(text)
	text = reWhitespace.ReplaceAllString(text, " ")
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	text = reNewlines.ReplaceAllString(text, "\n\n")
	return strings.TrimSpace(text)
}

// truncateChars truncates s to at most n Unicode code points.
func truncateChars(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}

// Compile-time checks.
var _ tools.Tool = (*SearchTool)(nil)
var _ tools.Tool = (*FetchTool)(nil)
