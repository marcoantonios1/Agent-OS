package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/marcoantonios1/Agent-OS/internal/app"
	"github.com/marcoantonios1/Agent-OS/internal/agents/builder"
	"github.com/marcoantonios1/Agent-OS/internal/agents/comms"
	"github.com/marcoantonios1/Agent-OS/internal/agents/research"
	"github.com/marcoantonios1/Agent-OS/internal/approval"
	"github.com/marcoantonios1/Agent-OS/internal/channels/discord"
	"github.com/marcoantonios1/Agent-OS/internal/channels/web"
	"github.com/marcoantonios1/Agent-OS/internal/costguard"
	"github.com/marcoantonios1/Agent-OS/internal/memory"
	"github.com/marcoantonios1/Agent-OS/internal/observability"
	"github.com/marcoantonios1/Agent-OS/internal/router"
	"github.com/marcoantonios1/Agent-OS/internal/tools/calendar"
	calendarGoogle "github.com/marcoantonios1/Agent-OS/internal/tools/calendar/google"
	calendarOutlook "github.com/marcoantonios1/Agent-OS/internal/tools/calendar/outlook"
	"github.com/marcoantonios1/Agent-OS/internal/tools"
	"github.com/marcoantonios1/Agent-OS/internal/tools/code"
	"github.com/marcoantonios1/Agent-OS/internal/tools/email"
	emailGmail "github.com/marcoantonios1/Agent-OS/internal/tools/email/gmail"
	emailOutlook "github.com/marcoantonios1/Agent-OS/internal/tools/email/outlook"
	"github.com/marcoantonios1/Agent-OS/internal/tools/websearch"
	searchBrave "github.com/marcoantonios1/Agent-OS/internal/tools/websearch/brave"
)

func main() {
	// Load and validate configuration from .env + environment variables.
	cfg, err := app.Load(".env")
	if err != nil {
		slog.Error("startup failed", "error", err)
		os.Exit(1)
	}

	observability.Setup(cfg.LogLevel)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	store := memory.NewStore()
	defer store.Close()
	projectStore := memory.NewProjectStore()

	approvals := approval.NewMemoryStore()

	llm := costguard.New(cfg.CostguardURL, cfg.CostguardAPIKey)
	classifier := router.NewLLMClassifier(llm)

	agents := map[router.Intent]router.Agent{
		router.IntentComms:    comms.New(llm, newEmailProvider(ctx, cfg), newCalendarProvider(ctx, cfg), approvals),
		router.IntentBuilder:  builder.New(llm, store, newBuilderConfig(cfg), projectStore),
		router.IntentResearch: research.New(llm, newWebSearchRegistry(cfg)),
	}

	r := router.New(classifier, agents, store, approvals)
	h := web.NewHandler(r, llm)

	srv := &http.Server{
		Addr:    ":" + cfg.Port,
		Handler: h,
	}

	go func() {
		slog.Info("Agent OS started", "port", cfg.Port)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("server error", "error", err)
			os.Exit(1)
		}
	}()

	// Start Discord channel if configured.
	var discordHandler *discord.Handler
	if cfg.DiscordConfigured() {
		discordHandler = discord.New(r, cfg.DiscordBotToken, cfg.DiscordGuildID, cfg.DiscordPrefix)
		go func() {
			if err := discordHandler.Start(ctx); err != nil {
				slog.Error("discord channel error", "error", err)
			}
		}()
	} else {
		slog.Warn("DISCORD_BOT_TOKEN not set — Discord channel disabled")
	}

	// Block until SIGINT or SIGTERM.
	<-ctx.Done()
	stop()

	if discordHandler != nil {
		discordHandler.Stop()
	}

	slog.Info("shutting down — draining in-flight requests")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("shutdown timeout exceeded", "error", err)
		os.Exit(1)
	}
	slog.Info("shutdown complete")
}

// newWebSearchRegistry builds a ToolRegistry containing web_search and web_fetch.
// If SEARCH_API_KEY is not set a stub provider is used and a warning is logged —
// the research agent still starts but has no live web access.
func newWebSearchRegistry(cfg *app.Config) *tools.ToolRegistry {
	if !cfg.SearchConfigured() {
		slog.Warn("SEARCH_API_KEY not set — web search tools disabled; research agent will use LLM knowledge only")
		return websearch.NewWebSearchRegistry(&stubSearchProvider{})
	}
	slog.Info("web search enabled", "provider", cfg.SearchProvider)
	return websearch.NewWebSearchRegistry(searchBrave.New(cfg.SearchAPIKey))
}

// stubSearchProvider is returned when no API key is configured.
// It always returns an empty result set so the LLM can still respond from
// its training knowledge without a hard failure.
type stubSearchProvider struct{}

func (s *stubSearchProvider) Search(_ context.Context, _ string, _ int) ([]websearch.SearchResult, error) {
	return []websearch.SearchResult{}, nil
}

// newEmailProvider returns a Gmail or Outlook EmailProvider based on which
// credentials are present in cfg, or nil if neither is configured.
func newEmailProvider(ctx context.Context, cfg *app.Config) email.EmailProvider {
	if cfg.GoogleConfigured() {
		p, err := emailGmail.New(ctx, cfg.GoogleClientID, cfg.GoogleClientSecret, cfg.GoogleRefreshToken)
		if err != nil {
			slog.Warn("Gmail provider unavailable", "error", err)
			return nil
		}
		return p
	}
	if cfg.MicrosoftConfigured() {
		p, err := emailOutlook.New(ctx, cfg.MicrosoftClientID, "", cfg.MicrosoftRefreshToken)
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
// which credentials are present in cfg, or nil if neither is configured.
func newCalendarProvider(ctx context.Context, cfg *app.Config) calendar.CalendarProvider {
	if cfg.GoogleConfigured() {
		p, err := calendarGoogle.New(ctx, cfg.GoogleClientID, cfg.GoogleClientSecret, cfg.GoogleRefreshToken)
		if err != nil {
			slog.Warn("Google Calendar provider unavailable", "error", err)
			return nil
		}
		return p
	}
	if cfg.MicrosoftConfigured() {
		p, err := calendarOutlook.New(ctx, cfg.MicrosoftClientID, cfg.MicrosoftRefreshToken)
		if err != nil {
			slog.Warn("Outlook Calendar provider unavailable", "error", err)
			return nil
		}
		return p
	}
	slog.Warn("No calendar provider configured — calendar tools disabled")
	return nil
}

// newBuilderConfig returns a code.Config for the Builder Agent sandbox.
func newBuilderConfig(cfg *app.Config) code.Config {
	os.MkdirAll(cfg.BuilderSandboxDir, 0o755) //nolint:errcheck
	return code.Config{SandboxDir: cfg.BuilderSandboxDir}
}
