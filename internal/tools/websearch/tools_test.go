package websearch_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/marcoantonios1/Agent-OS/internal/tools/websearch"
)

// ── mock provider ─────────────────────────────────────────────────────────────

type mockProvider struct {
	results []websearch.SearchResult
	err     error
}

func (m *mockProvider) Search(_ context.Context, _ string, _ int) ([]websearch.SearchResult, error) {
	return m.results, m.err
}

// ── web_search tests ──────────────────────────────────────────────────────────

func TestWebSearch_ReturnsResults(t *testing.T) {
	provider := &mockProvider{
		results: []websearch.SearchResult{
			{Title: "Go Language", URL: "https://go.dev", Snippet: "The Go programming language."},
			{Title: "Go Docs", URL: "https://pkg.go.dev", Snippet: "Go package documentation."},
		},
	}
	reg := websearch.NewWebSearchRegistry(provider)

	input := json.RawMessage(`{"query":"golang","limit":2}`)
	got, err := reg.Execute(context.Background(), "web_search", input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var results []websearch.SearchResult
	if err := json.Unmarshal([]byte(got), &results); err != nil {
		t.Fatalf("response is not valid JSON: %v — got: %s", err, got)
	}
	if len(results) != 2 {
		t.Errorf("expected 2 results, got %d", len(results))
	}
	if results[0].Title != "Go Language" {
		t.Errorf("unexpected first result title: %q", results[0].Title)
	}
}

func TestWebSearch_DefaultLimit(t *testing.T) {
	var capturedLimit int
	provider := &captureProvider{onSearch: func(_ string, limit int) {
		capturedLimit = limit
	}}
	reg := websearch.NewWebSearchRegistry(provider)

	_, _ = reg.Execute(context.Background(), "web_search", json.RawMessage(`{"query":"test"}`))
	if capturedLimit != 5 {
		t.Errorf("expected default limit 5, got %d", capturedLimit)
	}
}

func TestWebSearch_CapsLimit(t *testing.T) {
	var capturedLimit int
	provider := &captureProvider{onSearch: func(_ string, limit int) {
		capturedLimit = limit
	}}
	reg := websearch.NewWebSearchRegistry(provider)

	_, _ = reg.Execute(context.Background(), "web_search", json.RawMessage(`{"query":"test","limit":999}`))
	if capturedLimit != 10 {
		t.Errorf("expected capped limit 10, got %d", capturedLimit)
	}
}

func TestWebSearch_MissingQuery_ReturnsError(t *testing.T) {
	reg := websearch.NewWebSearchRegistry(&mockProvider{})
	_, err := reg.Execute(context.Background(), "web_search", json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("expected error for missing query, got nil")
	}
}

func TestWebSearch_ProviderError_ReturnsError(t *testing.T) {
	provider := &mockProvider{err: fmt.Errorf("API rate limit exceeded")}
	reg := websearch.NewWebSearchRegistry(provider)

	_, err := reg.Execute(context.Background(), "web_search", json.RawMessage(`{"query":"test"}`))
	if err == nil {
		t.Fatal("expected error from provider, got nil")
	}
	if !strings.Contains(err.Error(), "rate limit") {
		t.Errorf("error %q does not mention rate limit", err.Error())
	}
}

// ── web_fetch tests ───────────────────────────────────────────────────────────

func TestWebFetch_ReturnsStrippedText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<html><head><title>Test</title><style>body{color:red}</style></head>
<body><h1>Hello World</h1><p>This is a <b>test</b> page.</p>
<script>alert('hi')</script></body></html>`)
	}))
	defer srv.Close()

	reg := websearch.NewWebSearchRegistry(&mockProvider{})
	input := json.RawMessage(`{"url":"` + srv.URL + `"}`)
	got, err := reg.Execute(context.Background(), "web_fetch", input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if strings.Contains(got, "<") || strings.Contains(got, ">") {
		t.Errorf("response contains HTML tags: %q", got)
	}
	if !strings.Contains(got, "Hello World") {
		t.Errorf("response missing expected text: %q", got)
	}
	if strings.Contains(got, "alert") {
		t.Error("response contains script content that should have been stripped")
	}
	if strings.Contains(got, "color:red") {
		t.Error("response contains style content that should have been stripped")
	}
}

func TestWebFetch_TruncatesAtMaxChars(t *testing.T) {
	content := strings.Repeat("a", 2000)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, content)
	}))
	defer srv.Close()

	reg := websearch.NewWebSearchRegistry(&mockProvider{})
	input := json.RawMessage(`{"url":"` + srv.URL + `","max_chars":100}`)
	got, err := reg.Execute(context.Background(), "web_fetch", input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Allow a little slack for the trailing "…"
	if len([]rune(got)) > 110 {
		t.Errorf("response not truncated: got %d chars", len([]rune(got)))
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("truncated response missing ellipsis: %q", got)
	}
}

func TestWebFetch_ServerError_ReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer srv.Close()

	reg := websearch.NewWebSearchRegistry(&mockProvider{})
	input := json.RawMessage(`{"url":"` + srv.URL + `"}`)
	_, err := reg.Execute(context.Background(), "web_fetch", input)
	if err == nil {
		t.Fatal("expected error for 404 response, got nil")
	}
}

func TestWebFetch_MissingURL_ReturnsError(t *testing.T) {
	reg := websearch.NewWebSearchRegistry(&mockProvider{})
	_, err := reg.Execute(context.Background(), "web_fetch", json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("expected error for missing url, got nil")
	}
}

func TestWebFetch_HTMLEntitiesDecoded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<p>Tom &amp; Jerry &lt;cartoon&gt; &quot;classic&quot;</p>`)
	}))
	defer srv.Close()

	reg := websearch.NewWebSearchRegistry(&mockProvider{})
	got, err := reg.Execute(context.Background(), "web_fetch", json.RawMessage(`{"url":"`+srv.URL+`"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(got, "Tom & Jerry") {
		t.Errorf("entities not decoded: %q", got)
	}
}

// ── registry ──────────────────────────────────────────────────────────────────

func TestNewWebSearchRegistry_RegistersBothTools(t *testing.T) {
	reg := websearch.NewWebSearchRegistry(&mockProvider{})

	// Both tools should be reachable; unknown tools should error.
	_, err := reg.Execute(context.Background(), "unknown_tool", json.RawMessage(`{}`))
	if err == nil {
		t.Error("expected error for unknown tool, got nil")
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────

// captureProvider records the limit passed to Search for limit-capping tests.
type captureProvider struct {
	onSearch func(query string, limit int)
}

func (c *captureProvider) Search(_ context.Context, query string, limit int) ([]websearch.SearchResult, error) {
	if c.onSearch != nil {
		c.onSearch(query, limit)
	}
	return nil, nil
}
