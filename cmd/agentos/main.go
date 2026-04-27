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

	"github.com/marcoantonios1/Agent-OS/internal/agents/builder"
	"github.com/marcoantonios1/Agent-OS/internal/agents/comms"
	"github.com/marcoantonios1/Agent-OS/internal/agents/research"
	"github.com/marcoantonios1/Agent-OS/internal/agents/reviewer"
	"github.com/marcoantonios1/Agent-OS/internal/app"
	"github.com/marcoantonios1/Agent-OS/internal/approval"
	"github.com/marcoantonios1/Agent-OS/internal/channels/discord"
	"github.com/marcoantonios1/Agent-OS/internal/channels/web"
	"github.com/marcoantonios1/Agent-OS/internal/channels/whatsApp"
	"github.com/marcoantonios1/Agent-OS/internal/costguard"
	"github.com/marcoantonios1/Agent-OS/internal/memory"
	agentOAuth "github.com/marcoantonios1/Agent-OS/internal/oauth"
	"github.com/marcoantonios1/Agent-OS/internal/observability"
	"github.com/marcoantonios1/Agent-OS/internal/router"
	"github.com/marcoantonios1/Agent-OS/internal/sessions"
	"github.com/marcoantonios1/Agent-OS/internal/tools"
	"github.com/marcoantonios1/Agent-OS/internal/tools/calendar"
	calendarGoogle "github.com/marcoantonios1/Agent-OS/internal/tools/calendar/google"
	calendarOutlook "github.com/marcoantonios1/Agent-OS/internal/tools/calendar/outlook"
	"github.com/marcoantonios1/Agent-OS/internal/tools/code"
	"github.com/marcoantonios1/Agent-OS/internal/tools/email"
	emailGmail "github.com/marcoantonios1/Agent-OS/internal/tools/email/gmail"
	emailOutlook "github.com/marcoantonios1/Agent-OS/internal/tools/email/outlook"
	"github.com/marcoantonios1/Agent-OS/internal/tools/reminder"
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

	// User, project, and reminder stores: SQLite when SQLITE_PATH is set, in-memory otherwise.
	var projectStore sessions.ProjectStore
	var userStore sessions.UserStore
	var reminderStore sessions.ReminderStore
	if cfg.SQLiteConfigured() {
		db, err := memory.OpenDB(cfg.SQLitePath)
		if err != nil {
			slog.Error("sqlite: failed to open database", "path", cfg.SQLitePath, "error", err)
			os.Exit(1)
		}
		projectStore = memory.NewSQLiteProjectStore(db)
		userStore = memory.NewSQLiteUserStore(db)
		reminderStore = memory.NewSQLiteReminderStore(db)
		slog.Info("using SQLite persistence", "path", cfg.SQLitePath)
	} else {
		projectStore = memory.NewProjectStore()
		userStore = memory.NewUserStore()
		reminderStore = memory.NewReminderStore()
		slog.Warn("SQLITE_PATH not set — using in-memory stores (data lost on restart)")
	}

	approvals := approval.NewMemoryStore()

	llm := costguard.New(cfg.CostguardURL, cfg.CostguardAPIKey)
	classifier := router.NewLLMClassifier(llm, cfg.ClassifierModel)

	reminderWorker := reminder.NewWorker(reminderStore)

	builderCfg := newBuilderConfig(cfg)
	builderAgent := builder.New(llm, store, builderCfg, projectStore, cfg.BuilderModel)

	agents := map[router.Intent]router.Agent{
		router.IntentComms:     comms.New(llm, newEmailProvider(ctx, cfg), newCalendarProvider(ctx, cfg), approvals, userStore, reminderStore, cfg.CommsModel),
		router.IntentBuilder:   builderAgent,
		router.IntentResearch:  research.New(llm, newWebSearchRegistry(cfg), cfg.ResearchModel),
		router.IntentReviewer:  reviewer.New(llm, cfg.BuilderModel, builderCfg),
	}

	r := router.New(classifier, agents, store, approvals)
	r.Users = userStore
	builderAgent.SetSubAgentCaller(r)
	h := web.NewHandler(r, llm)

	// Web sessions have no persistent connection — register a no-op notifier
	// that logs a warning when a web reminder fires.
	reminderWorker.AddNotifier(web.ReminderNotifier{})

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

	go reminderWorker.Run(ctx)

	// Start Discord channel if configured.
	var discordHandler *discord.Handler
	if cfg.DiscordConfigured() {
		discordHandler = discord.New(r, cfg.DiscordBotToken, cfg.DiscordGuildID, cfg.DiscordPrefix)
		reminderWorker.AddNotifier(discordHandler)
		go func() {
			if err := discordHandler.Start(ctx); err != nil {
				slog.Error("discord channel error", "error", err)
			}
		}()
	} else {
		slog.Warn("DISCORD_BOT_TOKEN not set — Discord channel disabled")
	}

	// Start WhatsApp channel if configured.
	var whatsAppHandler *whatsapp.Handler
	if cfg.WhatsAppConfigured() {
		whatsAppHandler, err = whatsapp.New(r, cfg.WhatsAppStorePath, cfg.WhatsAppAllowedJID)
		if err != nil {
			slog.Error("whatsapp: setup failed", "error", err)
			os.Exit(1)
		}
		reminderWorker.AddNotifier(whatsAppHandler)
		go func() {
			if err := whatsAppHandler.Start(ctx); err != nil {
				slog.Error("whatsapp channel error", "error", err)
			}
		}()
	} else {
		slog.Warn("WHATSAPP_STORE_PATH not set — WhatsApp channel disabled")
	}

	// Block until SIGINT or SIGTERM.
	<-ctx.Done()
	stop()

	if discordHandler != nil {
		discordHandler.Stop()
	}
	if whatsAppHandler != nil {
		whatsAppHandler.Stop()
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

// newEmailProvider returns an EmailProvider for all configured backends.
// If both Google and Microsoft are configured a MultiProvider is returned with
// Google as the primary (writes go to Gmail). If only one is configured that
// provider is returned directly. Returns nil if neither is configured.
func newEmailProvider(ctx context.Context, cfg *app.Config) email.EmailProvider {
	var google, microsoft email.EmailProvider

	if cfg.GoogleConfigured() {
		persist := agentOAuth.EnvFilePersist(cfg.EnvFile, "GOOGLE_REFRESH_TOKEN")
		p, err := emailGmail.New(ctx, cfg.GoogleClientID, cfg.GoogleClientSecret, cfg.GoogleRefreshToken, persist)
		if err != nil {
			slog.Warn("Gmail provider unavailable", "error", err)
		} else {
			google = p
		}
	}
	if cfg.MicrosoftConfigured() {
		persist := agentOAuth.EnvFilePersist(cfg.EnvFile, "MICROSOFT_REFRESH_TOKEN")
		p, err := emailOutlook.New(ctx, cfg.MicrosoftClientID, "", cfg.MicrosoftRefreshToken, persist)
		if err != nil {
			slog.Warn("Outlook email provider unavailable", "error", err)
		} else {
			microsoft = p
		}
	}

	switch {
	case google != nil && microsoft != nil:
		slog.Info("email: using both Gmail and Outlook (Google primary)")
		return email.NewMultiProvider(google, google, microsoft)
	case google != nil:
		return google
	case microsoft != nil:
		return microsoft
	default:
		slog.Warn("No email provider configured — email tools disabled")
		return nil
	}
}

// newCalendarProvider returns a CalendarProvider for all configured backends.
// If both Google and Microsoft are configured a MultiProvider is returned with
// Google as the primary (writes go to Google Calendar). If only one is
// configured that provider is returned directly. Returns nil if neither is configured.
func newCalendarProvider(ctx context.Context, cfg *app.Config) calendar.CalendarProvider {
	var google, microsoft calendar.CalendarProvider

	if cfg.GoogleConfigured() {
		persist := agentOAuth.EnvFilePersist(cfg.EnvFile, "GOOGLE_REFRESH_TOKEN")
		p, err := calendarGoogle.New(ctx, cfg.GoogleClientID, cfg.GoogleClientSecret, cfg.GoogleRefreshToken, persist)
		if err != nil {
			slog.Warn("Google Calendar provider unavailable", "error", err)
		} else {
			google = p
		}
	}
	if cfg.MicrosoftConfigured() {
		persist := agentOAuth.EnvFilePersist(cfg.EnvFile, "MICROSOFT_REFRESH_TOKEN")
		p, err := calendarOutlook.New(ctx, cfg.MicrosoftClientID, cfg.MicrosoftRefreshToken, persist)
		if err != nil {
			slog.Warn("Outlook Calendar provider unavailable", "error", err)
		} else {
			microsoft = p
		}
	}

	switch {
	case google != nil && microsoft != nil:
		slog.Info("calendar: using both Google Calendar and Outlook (Google primary)")
		return calendar.NewMultiProvider(google, google, microsoft)
	case google != nil:
		return google
	case microsoft != nil:
		return microsoft
	default:
		slog.Warn("No calendar provider configured — calendar tools disabled")
		return nil
	}
}

// newBuilderConfig returns a code.Config for the Builder Agent sandbox.
func newBuilderConfig(cfg *app.Config) code.Config {
	os.MkdirAll(cfg.BuilderSandboxDir, 0o755) //nolint:errcheck
	return code.Config{SandboxDir: cfg.BuilderSandboxDir}
}
