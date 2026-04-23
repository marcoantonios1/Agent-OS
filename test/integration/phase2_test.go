package integration

// Phase 2 end-to-end tests covering the four net-new capabilities:
//   1. Research agent produces structured findings with sources.
//   2. Research + Builder compound request runs both agents in the stated order.
//   3. User profile saved in one session is injected into the system prompt
//      for a second session with the same user ID (cross-session memory).
//   4. A builder project saved in a previous session can be resumed via
//      project_load in a fresh session, starting at the persisted phase.

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/marcoantonios1/Agent-OS/internal/costguard"
	"github.com/marcoantonios1/Agent-OS/internal/memory"
	"github.com/marcoantonios1/Agent-OS/internal/sessions"
	"github.com/marcoantonios1/Agent-OS/internal/tools/websearch"
)

// ── Test 1: Research — structured findings with sources ────────────────────────

// TestPhase2_Research_StructuredFindings verifies the research workflow end-to-end:
// classifier routes to the research agent, which calls web_search and returns a
// response structured with ## Findings, ## Sources, and ## Caveats sections.
func TestPhase2_Research_StructuredFindings(t *testing.T) {
	searchProv := &mockSearchProvider{results: []websearch.SearchResult{
		{Title: "AutoGPT", URL: "https://autogpt.net", Snippet: "Open-source autonomous AI agent framework built on GPT-4."},
		{Title: "LangChain", URL: "https://langchain.com", Snippet: "Framework for building LLM-powered applications and agents."},
		{Title: "CrewAI", URL: "https://crewai.io", Snippet: "Multi-agent collaborative AI framework."},
	}}

	// Call order:
	//   1. Classifier → ["research"]
	//   2. Research agent step 1 → web_search tool call
	//   3. Research agent step 2 → structured findings text
	stack := newStack(stackConfig{
		llmResponses: []costguard.CompletionResponse{
			classifyResp("research"),
			toolCallResp("tc-1", "web_search", `{"query":"Agent OS competitors autonomous AI agent frameworks","limit":5}`),
			textResp("## Findings\n" +
				"The primary competitors in the autonomous AI agent space are AutoGPT, LangChain, and CrewAI. " +
				"AutoGPT is the most popular open-source option; LangChain dominates the developer framework market; " +
				"CrewAI focuses on multi-agent collaboration.\n\n" +
				"## Sources\n" +
				"- [AutoGPT](https://autogpt.net) — open-source autonomous agent\n" +
				"- [LangChain](https://langchain.com) — LLM application framework\n" +
				"- [CrewAI](https://crewai.io) — multi-agent collaboration\n\n" +
				"## Caveats\n" +
				"The AI agent space evolves rapidly; capabilities and market position may have changed since the search results were fetched."),
		},
		searchProv: searchProv,
	})
	defer stack.Close()

	resp, body, err := stack.post(chatRequest{
		SessionID: "p2-research-1",
		UserID:    "user-p2-r",
		Text:      "Research competitors for Agent OS",
	})
	if err != nil {
		t.Fatalf("POST /v1/chat: %v", err)
	}

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}

	// Response must contain all three structured sections.
	for _, section := range []string{"Findings", "Sources", "Caveats"} {
		if !strings.Contains(body.Text, section) {
			t.Errorf("response missing ## %s section: %q", section, body.Text)
		}
	}

	// At least one competitor should appear by name.
	namedCompetitors := []string{"AutoGPT", "LangChain", "CrewAI"}
	found := false
	for _, name := range namedCompetitors {
		if strings.Contains(body.Text, name) {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("response should name at least one competitor from %v, got: %q", namedCompetitors, body.Text)
	}

	// Sources section must contain at least one markdown link.
	if !strings.Contains(body.Text, "](http") {
		t.Errorf("sources section should contain at least one markdown URL link, got: %q", body.Text)
	}

	// Session history must be persisted.
	sess, err := stack.store.Get("p2-research-1")
	if err != nil {
		t.Fatalf("session not found after request: %v", err)
	}
	if len(sess.History) < 2 {
		t.Errorf("session history length = %d, want >= 2 (user + assistant)", len(sess.History))
	}

	// 1 (classifier) + 2 (research: tool call + text reply) = 3 LLM calls.
	if got := stack.llm.callCount(); got != 3 {
		t.Errorf("LLM call count = %d, want 3", got)
	}
}

// ── Test 2: Research + Builder compound ────────────────────────────────────────

// TestPhase2_ResearchAndBuilder_OrderedResponse verifies the compound
// ["research", "builder"] workflow: the response contains a [research] section
// with structured findings followed by a [builder] section with clarifying
// questions, separated by the compound divider, in that order.
func TestPhase2_ResearchAndBuilder_OrderedResponse(t *testing.T) {
	searchProv := &mockSearchProvider{results: []websearch.SearchResult{
		{Title: "Playtomic", URL: "https://playtomic.io", Snippet: "Leading multi-sport booking platform with padel support."},
		{Title: "PadelZone", URL: "https://padelzone.app", Snippet: "Padel-specific match-making and booking app."},
	}}

	// Call order:
	//   1. Classifier → ["research", "builder"]
	//   2. Research agent step 1 → web_search
	//   3. Research agent step 2 → structured findings
	//   4. Builder agent step 1 → clarifying questions
	stack := newStack(stackConfig{
		llmResponses: []costguard.CompletionResponse{
			classifyResp("research", "builder"),
			toolCallResp("tc-1", "web_search", `{"query":"padel app competitors market analysis","limit":5}`),
			textResp("## Findings\n" +
				"Playtomic is the dominant padel/sports booking platform in Europe. " +
				"PadelZone occupies a niche padel-only segment with match-making focus.\n\n" +
				"## Sources\n" +
				"- [Playtomic](https://playtomic.io) — market leader\n" +
				"- [PadelZone](https://padelzone.app) — padel niche\n\n" +
				"## Caveats\nData based on web presence; no financial data available."),
			textResp("Great — let's build your padel app! A few questions before I write the spec:\n\n" +
				"1. Target platform: iOS, Android, or Web?\n" +
				"2. Core feature: court booking, match-making, or both?\n" +
				"3. Do you need club management tools, or is this player-facing only?"),
		},
		searchProv: searchProv,
	})
	defer stack.Close()

	resp, body, err := stack.post(chatRequest{
		SessionID: "p2-mixed-1",
		UserID:    "user-p2-mix",
		Text:      "Research padel apps then build me one",
	})
	if err != nil {
		t.Fatalf("POST /v1/chat: %v", err)
	}

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}

	// Both agent labels must be present.
	if !strings.Contains(body.Text, "[research]") {
		t.Errorf("response missing [research] label: %q", body.Text)
	}
	if !strings.Contains(body.Text, "[builder]") {
		t.Errorf("response missing [builder] label: %q", body.Text)
	}

	// Sections must be separated by the compound divider.
	if !strings.Contains(body.Text, "---") {
		t.Errorf("compound response must contain --- separator: %q", body.Text)
	}

	// [research] must come before [builder] — matches user's stated order.
	researchIdx := strings.Index(body.Text, "[research]")
	builderIdx := strings.Index(body.Text, "[builder]")
	if researchIdx < 0 || builderIdx < 0 {
		t.Fatalf("expected both labels, got: %q", body.Text)
	}
	if researchIdx >= builderIdx {
		t.Errorf("[research] (idx %d) must appear before [builder] (idx %d)", researchIdx, builderIdx)
	}

	// Research section must contain structured findings.
	for _, section := range []string{"Findings", "Sources"} {
		if !strings.Contains(body.Text, section) {
			t.Errorf("research section missing ## %s: %q", section, body.Text)
		}
	}

	// Builder section must contain clarifying questions.
	if strings.Count(body.Text, "?") < 2 {
		t.Errorf("builder section should have >= 2 clarifying questions: %q", body.Text)
	}

	// 1 (classifier) + 2 (research) + 1 (builder) = 4 LLM calls.
	if got := stack.llm.callCount(); got != 4 {
		t.Errorf("LLM call count = %d, want 4", got)
	}
}

// ── Test 3: Memory — cross-session profile context ─────────────────────────────

// TestPhase2_Memory_ProfilePersistedAcrossSessions verifies that a user profile
// written to the UserStore in one logical session is injected into the Comms
// Agent's system prompt in a second session that shares the same user ID.
//
// This simulates the real use case: the user configures preferences once
// ("always address me as Marco, formal tone"), and subsequent sessions
// automatically pick up that context.
func TestPhase2_Memory_ProfilePersistedAcrossSessions(t *testing.T) {
	// Pre-seed the user store — this models what user_profile_update would have
	// written at the end of a prior session.
	userStore := memory.NewUserStore()
	if err := userStore.SaveUser(&sessions.UserProfile{
		UserID:             "user-p2-mem",
		Name:               "Marco",
		CommunicationStyle: "formal",
		Preferences: map[string]string{
			"timezone": "UTC+3",
			"sign_off": "Marco",
		},
		RecurringContacts: []sessions.Contact{
			{Name: "Alice", Email: "alice@example.com", Notes: "business partner"},
		},
	}); err != nil {
		t.Fatalf("SaveUser: %v", err)
	}

	// Capture the system prompt sent to the agent in the NEW session.
	var capturedPrompts []string
	capLLM := &capturingScriptedLLM{
		responses: []costguard.CompletionResponse{
			classifyResp("comms"),
			textResp("Hi Marco, you have no new emails."),
		},
		onCall: func(req costguard.CompletionRequest) {
			for _, msg := range req.Messages {
				if msg.Role == "system" {
					capturedPrompts = append(capturedPrompts, msg.Content)
				}
			}
		},
	}

	stack := newStack(stackConfig{
		customLLM: capLLM,
		userStore: userStore,
		emailProv: newMockEmail(nil, nil),
	})
	defer stack.Close()

	// This is a brand new session — different session ID from any prior session.
	resp, body, err := stack.post(chatRequest{
		SessionID: "p2-mem-session-2", // new session
		UserID:    "user-p2-mem",      // same user ID as the pre-seeded profile
		Text:      "Check my emails",
	})
	if err != nil {
		t.Fatalf("POST /v1/chat: %v", err)
	}

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if body.Text == "" {
		t.Error("expected non-empty response")
	}

	// The classifier now uses system role too, so find the comms agent prompt.
	var commsPrompt string
	for _, p := range capturedPrompts {
		if strings.Contains(p, "Comms Agent") {
			commsPrompt = p
			break
		}
	}
	if commsPrompt == "" {
		t.Fatalf("no comms system prompt captured; got %d system messages", len(capturedPrompts))
	}

	// The system prompt must contain the profile data from the previous session.
	if !strings.Contains(commsPrompt, "Marco") {
		t.Errorf("system prompt must contain user name 'Marco'; got:\n%.500s", commsPrompt)
	}
	if !strings.Contains(commsPrompt, "formal") {
		t.Errorf("system prompt must contain communication style 'formal'")
	}
	if !strings.Contains(commsPrompt, "UTC+3") {
		t.Errorf("system prompt must contain timezone preference 'UTC+3'")
	}
	if !strings.Contains(commsPrompt, "alice@example.com") {
		t.Errorf("system prompt must contain recurring contact email")
	}
}

// ── Test 4: Project resume — new session loads project at saved phase ──────────

// TestPhase2_ProjectResume_NewSessionLoadsPhase verifies the project resume
// workflow: a project previously saved in the ProjectStore (simulating session
// expiry) can be loaded in a fresh session via project_list + project_load,
// and the new session's metadata correctly reflects the saved phase and project ID.
func TestPhase2_ProjectResume_NewSessionLoadsPhase(t *testing.T) {
	// Pre-seed the project store — simulates a project built in a prior session
	// that has since expired, leaving only the ProjectStore record.
	projectStore := memory.NewProjectStore()
	if err := projectStore.SaveProject(&sessions.Project{
		ID:        "proj-padel-resume",
		UserID:    "user-p2-proj",
		Name:      "Padel App",
		Phase:     "spec",
		Spec:      "# Overview\nA padel court booking and match-making app for clubs.\n\n## Features\n- Court availability calendar\n- Match-making by skill level\n- Club management dashboard",
		CreatedAt: time.Now().Add(-24 * time.Hour),
	}); err != nil {
		t.Fatalf("SaveProject: %v", err)
	}

	// Call order in the NEW session:
	//   1. Classifier → ["builder"]
	//   2. Builder step 1 → project_list tool call
	//   3. Builder step 2 → project_load tool call (using the ID from the list)
	//   4. Builder step 3 → final text confirming the project was loaded
	stack := newStack(stackConfig{
		llmResponses: []costguard.CompletionResponse{
			classifyResp("builder"),
			toolCallResp("tc-1", "project_list", `{}`),
			toolCallResp("tc-2", "project_load", `{"project_id":"proj-padel-resume"}`),
			textResp("I've loaded your **Padel App** project. You're currently in the **spec** phase. Here's the spec so far:\n\n# Overview\nA padel court booking and match-making app for clubs.\n\nWould you like to continue to the tasks phase, or revise the spec first?"),
		},
		projectStore: projectStore,
		sandboxDir:   t.TempDir(),
	})
	defer stack.Close()

	resp, body, err := stack.post(chatRequest{
		SessionID: "p2-resume-session-1", // fresh session — no prior history
		UserID:    "user-p2-proj",
		Text:      "Resume my padel app project",
	})
	if err != nil {
		t.Fatalf("POST /v1/chat: %v", err)
	}

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}

	// The response must reference the loaded project.
	if !strings.Contains(body.Text, "Padel App") && !strings.Contains(body.Text, "padel") {
		t.Errorf("response should reference the project name, got: %q", body.Text)
	}
	if !strings.Contains(body.Text, "spec") {
		t.Errorf("response should mention the current phase 'spec', got: %q", body.Text)
	}

	// The session metadata must reflect the loaded project state.
	sess, err := stack.store.Get("p2-resume-session-1")
	if err != nil {
		t.Fatalf("session not found after request: %v", err)
	}

	if got := sess.Metadata["builder.project_id"]; got != "proj-padel-resume" {
		t.Errorf("builder.project_id = %q, want %q", got, "proj-padel-resume")
	}
	if got := sess.Metadata["builder.phase"]; got != "spec" {
		t.Errorf("builder.phase = %q, want %q", got, "spec")
	}
	if sess.Metadata["builder.spec"] == "" {
		t.Error("builder.spec should be non-empty after project_load")
	}

	// 1 (classifier) + 3 (builder: project_list + project_load + text) = 4 LLM calls.
	if got := stack.llm.callCount(); got != 4 {
		t.Errorf("LLM call count = %d, want 4", got)
	}
}
