package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"

	"github.com/marcoantonios1/Agent-OS/internal/agents/builder"
	"github.com/marcoantonios1/Agent-OS/internal/agents/comms"
	"github.com/marcoantonios1/Agent-OS/internal/approval"
	"github.com/marcoantonios1/Agent-OS/internal/tools/code"
	"github.com/marcoantonios1/Agent-OS/internal/channels/web"
	"github.com/marcoantonios1/Agent-OS/internal/costguard"
	"github.com/marcoantonios1/Agent-OS/internal/memory"
	"github.com/marcoantonios1/Agent-OS/internal/router"
	calendarGoogle "github.com/marcoantonios1/Agent-OS/internal/tools/calendar/google"
	calendarOutlook "github.com/marcoantonios1/Agent-OS/internal/tools/calendar/outlook"
	"github.com/marcoantonios1/Agent-OS/internal/tools/calendar"
	emailGmail "github.com/marcoantonios1/Agent-OS/internal/tools/email/gmail"
	emailOutlook "github.com/marcoantonios1/Agent-OS/internal/tools/email/outlook"
	"github.com/marcoantonios1/Agent-OS/internal/tools/email"
	"github.com/marcoantonios1/Agent-OS/internal/types"
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "9091"
	}

	ctx := context.Background()

	store := memory.NewStore()
	defer store.Close()

	approvals := approval.NewMemoryStore()

	llm := newLLMClient()
	classifier := router.NewLLMClassifier(llm)

	agents := map[router.Intent]router.Agent{
		router.IntentComms:    comms.New(llm, newEmailProvider(ctx), newCalendarProvider(ctx), approvals),
		router.IntentBuilder:  builder.New(llm, store, newBuilderConfig()),
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

// newEmailProvider returns a Gmail or Outlook EmailProvider based on which env
// vars are set, or nil if neither is configured.
func newEmailProvider(ctx context.Context) email.EmailProvider {
	if os.Getenv("GMAIL_CLIENT_ID") != "" {
		p, err := emailGmail.NewFromEnv(ctx)
		if err != nil {
			slog.Warn("Gmail provider unavailable", "error", err)
			return nil
		}
		return p
	}
	if os.Getenv("OUTLOOK_CLIENT_ID") != "" {
		p, err := emailOutlook.NewFromEnv(ctx)
		if err != nil {
			slog.Warn("Outlook email provider unavailable", "error", err)
			return nil
		}
		return p
	}
	slog.Warn("No email provider configured — email tools disabled")
	return nil
}

// newCalendarProvider returns a Google or Outlook CalendarProvider based on
// which env vars are set, or nil if neither is configured.
func newCalendarProvider(ctx context.Context) calendar.CalendarProvider {
	if os.Getenv("GOOGLE_CAL_CLIENT_ID") != "" {
		p, err := calendarGoogle.NewFromEnv(ctx)
		if err != nil {
			slog.Warn("Google Calendar provider unavailable", "error", err)
			return nil
		}
		return p
	}
	if os.Getenv("OUTLOOK_CLIENT_ID") != "" {
		p, err := calendarOutlook.NewFromEnv(ctx)
		if err != nil {
			slog.Warn("Outlook calendar provider unavailable", "error", err)
			return nil
		}
		return p
	}
	slog.Warn("No calendar provider configured — calendar tools disabled")
	return nil
}

// newBuilderConfig returns a code.Config for the Builder Agent sandbox.
// The sandbox directory defaults to a "workspace" folder next to the binary;
// override with BUILDER_SANDBOX_DIR.
func newBuilderConfig() code.Config {
	dir := os.Getenv("BUILDER_SANDBOX_DIR")
	if dir == "" {
		dir = "workspace"
	}
	os.MkdirAll(dir, 0o755) //nolint:errcheck
	return code.Config{SandboxDir: dir}
}

// placeholderAgent is used until real agent implementations are wired in.
type placeholderAgent struct{ name string }

func (p *placeholderAgent) Handle(_ context.Context, req types.AgentRequest) (types.AgentResponse, error) {
	return types.AgentResponse{
		AgentID: types.AgentID(p.name),
		Output:  "[" + p.name + " agent] received: " + req.Input,
	}, nil
}
