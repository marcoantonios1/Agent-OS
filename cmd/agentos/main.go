package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"

	"github.com/marcoantonios1/Agent-OS/internal/approval"
	"github.com/marcoantonios1/Agent-OS/internal/channels/web"
	"github.com/marcoantonios1/Agent-OS/internal/costguard"
	"github.com/marcoantonios1/Agent-OS/internal/memory"
	"github.com/marcoantonios1/Agent-OS/internal/router"
	"github.com/marcoantonios1/Agent-OS/internal/types"
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	store := memory.NewStore()
	defer store.Close()

	approvals := approval.NewMemoryStore()

	llm := newLLMClient()
	classifier := router.NewLLMClassifier(llm)

	agents := map[router.Intent]router.Agent{
		router.IntentComms:    &placeholderAgent{name: "comms"},
		router.IntentBuilder:  &placeholderAgent{name: "builder"},
		router.IntentResearch: &placeholderAgent{name: "research"},
	}

	r := router.New(classifier, agents, store, approvals)
	h := web.NewHandler(r)

	slog.Info("Agent OS starting", "port", port)
	if err := http.ListenAndServe(":"+port, h); err != nil {
		slog.Error("server error", "error", err)
		os.Exit(1)
	}
}

// newLLMClient returns a Costguard client when COSTGUARD_URL is set, or a
// stub that returns a placeholder so the server starts without configuration.
func newLLMClient() costguard.LLMClient {
	if os.Getenv("COSTGUARD_URL") != "" {
		return costguard.NewFromEnv()
	}
	slog.Warn("COSTGUARD_URL not set — using stub LLM client")
	return &stubLLMClient{}
}

// stubLLMClient is used for local development when Costguard is not configured.
// It classifies every message as "comms" so the server starts and responds.
type stubLLMClient struct{}

func (s *stubLLMClient) Complete(_ context.Context, _ costguard.CompletionRequest) (costguard.CompletionResponse, error) {
	return costguard.CompletionResponse{Content: `{"intent":"comms"}`}, nil
}

func (s *stubLLMClient) Stream(_ context.Context, _ costguard.CompletionRequest) (<-chan costguard.StreamChunk, error) {
	ch := make(chan costguard.StreamChunk)
	close(ch)
	return ch, nil
}

// placeholderAgent is used until real agent implementations are wired in.
type placeholderAgent struct{ name string }

func (p *placeholderAgent) Handle(_ context.Context, req types.AgentRequest) (types.AgentResponse, error) {
	return types.AgentResponse{
		AgentID: types.AgentID(p.name),
		Output:  "[" + p.name + " agent] received: " + req.Input,
	}, nil
}
