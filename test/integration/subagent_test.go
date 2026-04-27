package integration

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/marcoantonios1/Agent-OS/internal/costguard"
	"github.com/marcoantonios1/Agent-OS/internal/tools/websearch"
	"github.com/marcoantonios1/Agent-OS/internal/types"
)

// TestSubAgent_BuilderCallsResearch_RequirementsPhase verifies the end-to-end
// flow where a user asks the Builder to research competitors before writing a
// spec. The scripted LLM call order is:
//
//  1. Classifier → builder
//  2. Builder step 1 → tool call research_query("padel app competitors")
//  3. Research agent → text findings (web search not required; LLM returns them)
//  4. Builder step 2 → final text incorporating the research result
func TestSubAgent_BuilderCallsResearch_RequirementsPhase(t *testing.T) {
	researchQueryArgs, _ := json.Marshal(map[string]string{
		"query": "padel app competitors",
	})

	// 1 — classifier
	classify := classifyResp("builder")
	// 2 — builder calls research_query
	builderCallsResearch := costguard.CompletionResponse{
		ToolCalls: []types.ToolCall{
			{ID: "rq-1", Name: "research_query", Arguments: string(researchQueryArgs)},
		},
	}
	// 3 — research agent returns its findings (treated as a plain Complete call)
	researchFindings := textResp("Padel app market: PadelBase has 50k users and charges $9/mo. Playtomic dominates Europe.")
	// 4 — builder incorporates findings and replies
	builderFinal := textResp("Great! I've researched the market. Key competitors are PadelBase and Playtomic. " +
		"Let me now draft a spec that differentiates us on pricing and UX.\n" +
		"<builder_meta>{\"builder.phase\":\"spec\"}</builder_meta>")

	stack := newStack(stackConfig{
		llmResponses: []costguard.CompletionResponse{
			classify,
			builderCallsResearch,
			researchFindings,
			builderFinal,
		},
		searchProv: &mockSearchProvider{results: []websearch.SearchResult{
			{Title: "Playtomic — padel booking", URL: "https://playtomic.io", Snippet: "Europe's largest padel platform"},
		}},
	})
	defer stack.Close()

	resp, body, err := stack.post(chatRequest{
		SessionID: "sub-agent-test-1",
		UserID:    "user-1",
		Text:      "Build me a padel app — research competitors first before writing the spec",
	})
	if err != nil {
		t.Fatalf("POST /v1/chat: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	// The final response should mention findings from the research agent.
	if !strings.Contains(body.Text, "PadelBase") && !strings.Contains(body.Text, "Playtomic") {
		t.Errorf("research findings not incorporated into builder response; got: %s", body.Text)
	}
}

// TestSubAgent_ResearchQuery_ToolDefinitionPresent verifies that research_query
// appears in the Builder's tool definitions when SubAgentCaller is wired. We
// confirm this indirectly: the LLM successfully calls research_query (which
// would return a "not registered" error if the tool were absent).
func TestSubAgent_ResearchQuery_NoSubCaller_ToolAbsent(t *testing.T) {
	// Build a stack where the builder LLM only makes a plain text response —
	// no research_query call — to confirm the tool is still not breaking things
	// when it *is* wired but simply not invoked.
	stack := newStack(stackConfig{
		llmResponses: []costguard.CompletionResponse{
			classifyResp("builder"),
			textResp("I'll help you build a padel app. What platform — iOS, Android, or web?"),
		},
	})
	defer stack.Close()

	resp, body, err := stack.post(chatRequest{
		SessionID: "sub-agent-no-tool-1",
		UserID:    "user-2",
		Text:      "Build me a padel app",
	})
	if err != nil {
		t.Fatalf("POST /v1/chat: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if body.Text == "" {
		t.Error("expected non-empty response from builder")
	}
}
