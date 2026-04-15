package research_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/marcoantonios1/Agent-OS/internal/agents/research"
	"github.com/marcoantonios1/Agent-OS/internal/costguard"
	"github.com/marcoantonios1/Agent-OS/internal/tools/websearch"
	"github.com/marcoantonios1/Agent-OS/internal/types"
)

// ── mock LLM ──────────────────────────────────────────────────────────────────

type seqLLM struct {
	responses []costguard.CompletionResponse
	idx       int
	calls     []costguard.CompletionRequest
}

func (m *seqLLM) Complete(_ context.Context, req costguard.CompletionRequest) (costguard.CompletionResponse, error) {
	m.calls = append(m.calls, req)
	if m.idx >= len(m.responses) {
		return costguard.CompletionResponse{Content: "[no more responses]"}, nil
	}
	resp := m.responses[m.idx]
	m.idx++
	return resp, nil
}

func (m *seqLLM) Stream(_ context.Context, _ costguard.CompletionRequest) (<-chan costguard.StreamChunk, error) {
	ch := make(chan costguard.StreamChunk)
	close(ch)
	return ch, nil
}

func toolCall(id, name string, args any) costguard.CompletionResponse {
	b, _ := json.Marshal(args)
	return costguard.CompletionResponse{
		ToolCalls: []types.ToolCall{{ID: id, Name: name, Arguments: string(b)}},
	}
}

func textReply(text string) costguard.CompletionResponse {
	return costguard.CompletionResponse{Content: text}
}

// ── mock search provider ──────────────────────────────────────────────────────

type mockSearchProvider struct {
	results []websearch.SearchResult
}

func (m *mockSearchProvider) Search(_ context.Context, _ string, _ int) ([]websearch.SearchResult, error) {
	return m.results, nil
}

// ── helpers ───────────────────────────────────────────────────────────────────

func newRequest(sessionID, input string) types.AgentRequest {
	return types.AgentRequest{
		SessionID: sessionID,
		UserID:    "user-1",
		Intent:    "research",
		Input:     input,
		History:   []types.ConversationTurn{{Role: "user", Content: input}},
	}
}

func newAgent(llm *seqLLM, provider websearch.SearchProvider) *research.Agent {
	reg := websearch.NewWebSearchRegistry(provider)
	return research.New(llm, reg)
}

// ── tests ─────────────────────────────────────────────────────────────────────

// TestResearch_SingleSearch_TextResponse verifies the happy path:
// LLM calls web_search once, receives results, then returns a text answer.
func TestResearch_SingleSearch_TextResponse(t *testing.T) {
	provider := &mockSearchProvider{results: []websearch.SearchResult{
		{Title: "Brave Search API", URL: "https://brave.com/search/api/", Snippet: "Privacy-first search API."},
	}}

	llm := &seqLLM{responses: []costguard.CompletionResponse{
		toolCall("tc1", "web_search", map[string]any{"query": "brave search api pricing", "limit": 5}),
		textReply("## Findings\nBrave Search API offers a free tier with 2,000 queries/month.\n\n## Sources\n- [Brave Search API](https://brave.com/search/api/) — official pricing page\n\n## Caveats\nNone."),
	}}

	agent := newAgent(llm, provider)
	resp, err := agent.Handle(context.Background(), newRequest("sess-1", "What is the Brave Search API pricing?"))

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Output == "" {
		t.Fatal("expected non-empty response")
	}
	if len(llm.calls) != 2 {
		t.Errorf("expected 2 LLM calls (tool call + text reply), got %d", len(llm.calls))
	}
	if !strings.Contains(resp.Output, "Findings") {
		t.Errorf("output missing ## Findings section: %s", resp.Output)
	}
	if !strings.Contains(resp.Output, "Sources") {
		t.Errorf("output missing ## Sources section: %s", resp.Output)
	}
}

// TestResearch_MultiStep_SearchAndFetch verifies that the agent can call
// web_search followed by web_fetch (reading a full page) before synthesising.
func TestResearch_MultiStep_SearchAndFetch(t *testing.T) {
	// Serve a simple HTML page for the fetch step.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<html><body><h1>GraphQL vs REST</h1><p>GraphQL gives clients precise control over the data they fetch. REST is simpler and better supported.</p></body></html>`)) //nolint:errcheck
	}))
	defer srv.Close()

	provider := &mockSearchProvider{results: []websearch.SearchResult{
		{Title: "GraphQL vs REST", URL: srv.URL, Snippet: "A comparison of GraphQL and REST APIs."},
	}}

	llm := &seqLLM{responses: []costguard.CompletionResponse{
		// Step 1: search.
		toolCall("tc1", "web_search", map[string]any{"query": "GraphQL vs REST API differences"}),
		// Step 2: fetch the top result for full content.
		toolCall("tc2", "web_fetch", map[string]any{"url": srv.URL}),
		// Final answer.
		textReply("## Findings\nGraphQL lets clients request exactly the data they need. REST uses fixed endpoints.\n\n## Sources\n- [GraphQL vs REST](" + srv.URL + ") — detailed comparison\n\n## Caveats\nNone."),
	}}

	agent := newAgent(llm, provider)
	resp, err := agent.Handle(context.Background(), newRequest("sess-2", "What are the main differences between GraphQL and REST?"))

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(llm.calls) != 3 {
		t.Errorf("expected 3 LLM calls (search + fetch + text), got %d", len(llm.calls))
	}
	if !strings.Contains(resp.Output, "GraphQL") {
		t.Errorf("expected GraphQL in response, got: %s", resp.Output)
	}
	if !strings.Contains(resp.Output, "Sources") {
		t.Errorf("output missing ## Sources section: %s", resp.Output)
	}
}

// TestResearch_SystemPromptIsFirst verifies the system prompt is the very first
// message in every LLM call — the agent must not prepend history before it.
func TestResearch_SystemPromptIsFirst(t *testing.T) {
	llm := &seqLLM{responses: []costguard.CompletionResponse{
		textReply("## Findings\nTest answer.\n\n## Sources\n- None\n\n## Caveats\nNone."),
	}}

	agent := newAgent(llm, &mockSearchProvider{})
	agent.Handle(context.Background(), newRequest("sess-3", "What is the capital of France?")) //nolint:errcheck

	if len(llm.calls) == 0 {
		t.Fatal("expected at least one LLM call")
	}
	msgs := llm.calls[0].Messages
	if len(msgs) == 0 {
		t.Fatal("expected messages in first LLM call")
	}
	if msgs[0].Role != "system" {
		t.Errorf("first message must be system prompt, got role=%q", msgs[0].Role)
	}
	if !strings.Contains(msgs[0].Content, "Research Agent") {
		t.Errorf("system prompt does not mention Research Agent: %q", msgs[0].Content)
	}
}

// TestResearch_ToolsAreRegistered verifies that the registry exposes the
// web_search and web_fetch tool definitions on every LLM call.
func TestResearch_ToolsAreRegistered(t *testing.T) {
	llm := &seqLLM{responses: []costguard.CompletionResponse{
		textReply("## Findings\nAnswer.\n\n## Sources\n- None\n\n## Caveats\nNone."),
	}}

	agent := newAgent(llm, &mockSearchProvider{})
	agent.Handle(context.Background(), newRequest("sess-4", "Search for something")) //nolint:errcheck

	if len(llm.calls) == 0 {
		t.Fatal("expected at least one LLM call")
	}

	toolNames := make(map[string]bool)
	for _, td := range llm.calls[0].Tools {
		toolNames[td.Name] = true
	}
	if !toolNames["web_search"] {
		t.Error("web_search tool not registered in LLM call")
	}
	if !toolNames["web_fetch"] {
		t.Error("web_fetch tool not registered in LLM call")
	}
}

// TestResearch_AgentID verifies the response carries the correct agent ID.
func TestResearch_AgentID(t *testing.T) {
	llm := &seqLLM{responses: []costguard.CompletionResponse{
		textReply("## Findings\nAnswer.\n\n## Sources\n- None\n\n## Caveats\nNone."),
	}}

	agent := newAgent(llm, &mockSearchProvider{})
	resp, err := agent.Handle(context.Background(), newRequest("sess-5", "Question"))

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.AgentID != "research" {
		t.Errorf("expected AgentID=%q, got %q", "research", resp.AgentID)
	}
}

// TestResearch_ConversationHistory verifies that prior conversation turns are
// included in the LLM call so the agent has context for follow-up questions.
func TestResearch_ConversationHistory(t *testing.T) {
	llm := &seqLLM{responses: []costguard.CompletionResponse{
		textReply("## Findings\nFollow-up answer.\n\n## Sources\n- None\n\n## Caveats\nNone."),
	}}

	reg := websearch.NewWebSearchRegistry(&mockSearchProvider{})
	agent := research.New(llm, reg)

	req := types.AgentRequest{
		SessionID: "sess-6",
		UserID:    "user-1",
		Intent:    "research",
		Input:     "What about its pricing?",
		History: []types.ConversationTurn{
			{Role: "user", Content: "Tell me about the Brave Search API"},
			{Role: "assistant", Content: "Brave Search API is a privacy-first search API..."},
		},
	}

	agent.Handle(context.Background(), req) //nolint:errcheck

	if len(llm.calls) == 0 {
		t.Fatal("expected at least one LLM call")
	}

	msgs := llm.calls[0].Messages
	// Expect: system + 2 history turns + current user turn
	if len(msgs) < 4 {
		t.Errorf("expected at least 4 messages (system + history + current), got %d", len(msgs))
	}
}
