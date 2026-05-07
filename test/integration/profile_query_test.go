package integration

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/marcoantonios1/Agent-OS/internal/agents/generic"
	"github.com/marcoantonios1/Agent-OS/internal/approval"
	"github.com/marcoantonios1/Agent-OS/internal/channels/web"
	"github.com/marcoantonios1/Agent-OS/internal/costguard"
	"github.com/marcoantonios1/Agent-OS/internal/memory"
	"github.com/marcoantonios1/Agent-OS/internal/router"
	"github.com/marcoantonios1/Agent-OS/internal/sessions"
	"github.com/marcoantonios1/Agent-OS/internal/tools"
	"github.com/marcoantonios1/Agent-OS/internal/tools/userprofile"
)

// newProfileQueryStack builds a minimal test stack wired for profile_query tests.
// agentDir is the path to the agents/profile_query folder (relative to the test
// package's working directory).
func newProfileQueryStack(t *testing.T, agentDir string, llm costguard.LLMClient, personalityStore sessions.PersonalityStore, userStore sessions.UserStore) *testStack {
	t.Helper()

	globalReg := tools.NewRegistry()
	globalReg.Register(userprofile.NewReadTool(userStore))

	profileQueryAgent, err := generic.Load(agentDir, llm, globalReg)
	if err != nil {
		t.Fatalf("load profile_query agent from %q: %v", agentDir, err)
	}

	agents := map[router.Intent]router.Agent{
		router.Intent("profile_query"): profileQueryAgent,
	}

	store := memory.NewStore()
	approvals := approval.NewMemoryStore()
	classifier := router.NewLLMClassifier(llm, "claude-sonnet-4-6")

	r := router.New(classifier, agents, store, approvals)
	r.Users = userStore
	r.Personality = personalityStore

	srv := httptest.NewServer(web.NewHandler(r, nil))
	return &testStack{srv: srv, store: store}
}

// TestProfileQuery_RoutesToProfileQueryAgent verifies that "What do you know
// about me?" is classified as profile_query, the personality signals are injected
// into the agent's system prompt, and the response is non-empty.
func TestProfileQuery_RoutesToProfileQueryAgent(t *testing.T) {
	personalityStore := memory.NewPersonalityStore()
	_ = personalityStore.SavePersonality(&sessions.PersonalityProfile{
		UserID: "user-query",
		Signals: []sessions.PersonalitySignal{
			{Key: sessions.SignalResponseLength, Value: "brief", Confidence: 0.8, Count: 8, LastSeen: time.Now()},
			{Key: sessions.SignalTechnicalDepth, Value: "high", Confidence: 0.75, Count: 7, LastSeen: time.Now()},
		},
	})

	var capturedSystemPrompts []string
	llm := &capturingScriptedLLM{
		responses: []costguard.CompletionResponse{
			classifyResp("profile_query"),
			textResp("Based on our conversations, I've noticed you prefer brief responses and have a high technical depth. Does that sound right?"),
		},
		onCall: func(req costguard.CompletionRequest) {
			for _, msg := range req.Messages {
				if msg.Role == "system" {
					capturedSystemPrompts = append(capturedSystemPrompts, msg.Content)
				}
			}
		},
	}

	userStore := memory.NewUserStore()
	ts := newProfileQueryStack(t, "../../agents/profile_query", llm, personalityStore, userStore)
	defer ts.Close()

	_, resp, err := ts.post(chatRequest{
		SessionID: "sess-profile-query",
		UserID:    "user-query",
		Text:      "What do you know about me?",
	})
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	if resp.Text == "" {
		t.Fatal("expected non-empty response from profile_query agent")
	}

	// Find the agent's system prompt (contains SYSTEM.md content).
	var agentPrompt string
	for _, p := range capturedSystemPrompts {
		if strings.Contains(p, "Profile Query Agent") || strings.Contains(p, "personality profile") {
			agentPrompt = p
			break
		}
	}
	if agentPrompt == "" {
		t.Fatalf("profile_query agent system prompt not captured; got %d system messages", len(capturedSystemPrompts))
	}

	if !strings.Contains(agentPrompt, "## User personality") {
		t.Errorf("system prompt missing personality block; excerpt:\n%s",
			agentPrompt[:min(len(agentPrompt), 800)])
	}
	if !strings.Contains(agentPrompt, "brief") {
		t.Errorf("system prompt should contain signal value 'brief'")
	}
	if !strings.Contains(agentPrompt, "high") {
		t.Errorf("system prompt should contain signal value 'high'")
	}
}

// TestProfileQuery_NoSignals_GracefulAbsence verifies that when no personality
// signals exist the profile_query agent still returns a valid response.
func TestProfileQuery_NoSignals_GracefulAbsence(t *testing.T) {
	personalityStore := memory.NewPersonalityStore() // empty

	llm := &capturingScriptedLLM{
		responses: []costguard.CompletionResponse{
			classifyResp("profile_query"),
			textResp("I haven't built up a picture of you yet — the more we talk, the better I'll know your preferences."),
		},
		onCall: func(_ costguard.CompletionRequest) {},
	}

	userStore := memory.NewUserStore()
	ts := newProfileQueryStack(t, "../../agents/profile_query", llm, personalityStore, userStore)
	defer ts.Close()

	_, resp, err := ts.post(chatRequest{
		SessionID: "sess-profile-no-signals",
		UserID:    "user-no-signals",
		Text:      "What have you learned about me?",
	})
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	if resp.Text == "" {
		t.Fatal("expected non-empty response even when no signals exist")
	}
}
