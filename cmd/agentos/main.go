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

	sqlite_vec "github.com/asg017/sqlite-vec-go-bindings/cgo"
	_ "github.com/mattn/go-sqlite3" // CGO sqlite3 driver — required for sqlite-vec linkage
	"github.com/marcoantonios1/Agent-OS/internal/agents/builder"
	"github.com/marcoantonios1/Agent-OS/skills/community"
	"github.com/marcoantonios1/Agent-OS/internal/agents/generic"
	"github.com/marcoantonios1/Agent-OS/internal/agents/profile"
	"github.com/marcoantonios1/Agent-OS/internal/agents/reviewer"
	"github.com/marcoantonios1/Agent-OS/internal/app"
	"github.com/marcoantonios1/Agent-OS/internal/approval"
	"github.com/marcoantonios1/Agent-OS/internal/channels/discord"
	"github.com/marcoantonios1/Agent-OS/internal/channels/imessage"
	"github.com/marcoantonios1/Agent-OS/internal/channels/slack"
	"github.com/marcoantonios1/Agent-OS/internal/channels/telegram"
	"github.com/marcoantonios1/Agent-OS/internal/channels/web"
	"github.com/marcoantonios1/Agent-OS/internal/voice"
	"github.com/marcoantonios1/Agent-OS/internal/heartbeat"
	"github.com/marcoantonios1/Agent-OS/internal/types"
	"github.com/marcoantonios1/Agent-OS/internal/channels/whatsApp"
	"github.com/marcoantonios1/Agent-OS/internal/costguard"
	"github.com/marcoantonios1/Agent-OS/internal/memory"
	"github.com/marcoantonios1/Agent-OS/internal/memory/episodic"
	agentOAuth "github.com/marcoantonios1/Agent-OS/internal/oauth"
	"github.com/marcoantonios1/Agent-OS/internal/observability"
	"github.com/marcoantonios1/Agent-OS/internal/router"
	"github.com/marcoantonios1/Agent-OS/internal/sessions"
	"github.com/marcoantonios1/Agent-OS/internal/skills"
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

	llm := costguard.New(cfg.CostguardURL, cfg.CostguardAPIKey)

	// User, project, reminder, and personality stores: SQLite when SQLITE_PATH is set, in-memory otherwise.
	var projectStore sessions.ProjectStore
	var userStore sessions.UserStore
	var reminderStore sessions.ReminderStore
	var personalityStore sessions.PersonalityStore
	var episodicExtractor *episodic.Extractor
	var episodicStoreForRouter *episodic.SQLiteStore
	sqlite_vec.Auto()
	if cfg.SQLiteConfigured() {
		db, err := memory.OpenDB(cfg.SQLitePath)
		if err != nil {
			slog.Error("sqlite: failed to open database", "path", cfg.SQLitePath, "error", err)
			os.Exit(1)
		}
		projectStore = memory.NewSQLiteProjectStore(db)
		userStore = memory.NewSQLiteUserStore(db)
		reminderStore = memory.NewSQLiteReminderStore(db)
		personalityStore = memory.NewSQLitePersonalityStore(db)

		embedFn := func(ctx context.Context, text string) ([]float32, error) {
			return llm.Embed(ctx, text)
		}
		episodicStore, err := episodic.NewSQLiteStore(db, embedFn)
		if err != nil {
			slog.Error("episodic: failed to create store", "error", err)
			os.Exit(1)
		}

		extractorModel := cfg.ClassifierModel
		if m := os.Getenv("EPISODIC_EXTRACTOR_MODEL"); m != "" {
			extractorModel = m
		}
		episodicExtractor = episodic.NewExtractor(llm, episodicStore, extractorModel)
		episodicStoreForRouter = episodicStore

		slog.Info("using SQLite persistence", "path", cfg.SQLitePath)
	} else {
		projectStore = memory.NewProjectStore()
		userStore = memory.NewUserStore()
		reminderStore = memory.NewReminderStore()
		personalityStore = memory.NewPersonalityStore()
		slog.Warn("SQLITE_PATH not set — using in-memory stores (data lost on restart)")
	}
	approvals := approval.NewMemoryStore()
	classifier := router.NewLLMClassifier(llm, cfg.ClassifierModel)

	reminderWorker := reminder.NewWorker(reminderStore)

	builderCfg := newBuilderConfig(cfg)

	// Build the global registry once. Every agent gets a subset of this registry.
	var episodicStoreIface episodic.Store
	if episodicStoreForRouter != nil {
		episodicStoreIface = episodicStoreForRouter
	}
	globalRegistry := skills.NewGlobalRegistry(
		newEmailProvider(ctx, cfg),
		newCalendarProvider(ctx, cfg),
		newSearchProvider(cfg),
		approvals,
		userStore,
		reminderStore,
		projectStore,
		store,
		builderCfg,
		episodicStoreIface,
	)
	community.RegisterAll(globalRegistry)

	builderReg, _ := globalRegistry.Subset([]string{
		"file_read", "file_write", "file_list", "shell_run",
		"project_list", "project_load",
	})
	reviewerReg, _ := globalRegistry.Subset([]string{"file_read", "file_list", "shell_run"})

	builderAgent := builder.New(llm, builderReg, store, projectStore, cfg.BuilderModel)
	if cfg.BuilderToolCallModel != "" {
		builderAgent.SetToolCallModel(cfg.BuilderToolCallModel)
		slog.Info("builder: tool-call model enabled", "tool_call_model", cfg.BuilderToolCallModel)
	}

	agents := map[router.Intent]router.Agent{
		router.IntentBuilder:  builderAgent,
		router.IntentReviewer: reviewer.New(llm, reviewerReg, cfg.BuilderModel),
	}

	// Load generic agents from the agents/ directory. Any intent not already
	// claimed by a built-in agent is registered here. Errors are logged but
	// never fatal — the system runs with whichever agents loaded successfully.
	genericAgents, err := generic.LoadAll("agents", llm, globalRegistry)
	if err != nil {
		slog.Warn("generic agents: failed to scan agents directory", "error", err)
	} else {
		for intent, agent := range genericAgents {
			if _, exists := agents[intent]; !exists {
				agents[intent] = agent
			}
		}
	}

	r := router.New(classifier, agents, store, approvals)
	r.Users = userStore
	r.BuilderNotifier = web.ReminderNotifier{} // web: logs only; Discord overrides below
	r.ProfileObserver = profile.New(llm, personalityStore, cfg.ProfileModel)
	r.EpisodicExtractor = episodicExtractor
	r.EpisodicStore = episodicStoreForRouter
	r.Personality = personalityStore
	r.CompactionLLM = llm
	r.CompactionModel = cfg.ClassifierModel
	r.CompactionThreshold = cfg.CompactionThreshold
	builderAgent.SetSubAgentCaller(r)
	reminderWorker.SetDispatcher(r)
	h := web.NewHandler(r, llm)
	h.PersonalityStore = personalityStore

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

	// Build transcriber — enabled only when VOICE_TRANSCRIPTION=enabled.
	var transcriber voice.Transcriber
	if cfg.VoiceTranscriptionEnabled() {
		transcriber = voice.NewCostguardTranscriber(cfg.CostguardURL, cfg.CostguardAPIKey)
		slog.Info("voice transcription enabled")
	} else {
		transcriber = &voice.NoopTranscriber{}
		slog.Info("VOICE_TRANSCRIPTION not set — voice messages will prompt users to type instead")
	}

	// Build synthesizer — enabled only when VOICE_TTS=enabled.
	var synthesizer voice.Synthesizer
	if cfg.VoiceTTSEnabled() {
		synthesizer = voice.NewCostguardSynthesizer(cfg.CostguardURL, cfg.CostguardAPIKey, cfg.VoiceTTSVoice, cfg.VoiceTTSFormat)
		slog.Info("voice TTS enabled", "voice", cfg.VoiceTTSVoice)
	} else {
		synthesizer = &voice.NoopSynthesizer{}
		slog.Info("VOICE_TTS not set — text-only responses on all channels")
	}

	// Start Discord channel if configured.
	var discordHandler *discord.Handler
	if cfg.DiscordConfigured() {
		discordHandler = discord.New(r, cfg.DiscordBotToken, cfg.DiscordGuildID, cfg.DiscordPrefix)
		reminderWorker.AddNotifier(discordHandler)
		r.BuilderNotifier = discordHandler // Discord can push progress mid-build
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
		whatsAppHandler, err = whatsapp.New(r, cfg.WhatsAppStorePath, cfg.WhatsAppAllowedJID, transcriber)
		if err != nil {
			slog.Error("whatsapp: setup failed", "error", err)
			os.Exit(1)
		}
		whatsAppHandler.SetSynthesizer(synthesizer)
		whatsAppHandler.SetVideoConfig(cfg.VideoMaxFrames, int64(cfg.VideoMaxSizeMB))
		reminderWorker.AddNotifier(whatsAppHandler)
		go func() {
			if err := whatsAppHandler.Start(ctx); err != nil {
				slog.Error("whatsapp channel error", "error", err)
			}
		}()
	} else {
		slog.Warn("WHATSAPP_STORE_PATH not set — WhatsApp channel disabled")
	}

	// Start Telegram channel if configured.
	var telegramHandler *telegram.Handler
	if cfg.TelegramConfigured() {
		telegramHandler, err = telegram.New(r, cfg.TelegramBotToken, cfg.TelegramAllowedUserID)
		if err != nil {
			slog.Error("telegram: setup failed", "error", err)
			os.Exit(1)
		}
		telegramHandler.SetTranscriber(transcriber)
		telegramHandler.SetSynthesizer(synthesizer)
		telegramHandler.SetVideoConfig(cfg.VideoMaxFrames, int64(cfg.VideoMaxSizeMB))
		reminderWorker.AddNotifier(telegramHandler)
		go func() {
			if err := telegramHandler.Start(ctx); err != nil {
				slog.Error("telegram channel error", "error", err)
			}
		}()
	} else {
		slog.Warn("TELEGRAM_BOT_TOKEN not set — Telegram channel disabled")
	}

	// Start Slack channel if configured.
	var slackHandler *slack.Handler
	if cfg.SlackConfigured() {
		slackHandler, err = slack.New(r, cfg.SlackBotToken, cfg.SlackAppToken, cfg.SlackAllowedUserID, transcriber, synthesizer)
		if err != nil {
			slog.Error("slack: setup failed", "error", err)
			os.Exit(1)
		}
		slackHandler.SetVideoConfig(cfg.VideoMaxFrames, int64(cfg.VideoMaxSizeMB))
		reminderWorker.AddNotifier(slackHandler)
		go func() {
			if err := slackHandler.Start(ctx); err != nil {
				slog.Error("slack channel error", "error", err)
			}
		}()
	} else {
		slog.Warn("SLACK_BOT_TOKEN not set — Slack channel disabled")
	}

	// Start iMessage channel if configured.
	var iMessageHandler *imessage.Handler
	if cfg.BlueBubblesConfigured() {
		iMessageHandler, err = imessage.New(imessage.Config{
			BaseURL:       cfg.BlueBubblesURL,
			Password:      cfg.BlueBubblesPassword,
			AllowedHandle: cfg.BlueBubblesAllowedHandle,
			WebhookPort:   cfg.BlueBubblesWebhookPort,
		}, r, transcriber, synthesizer)
		if err != nil {
			slog.Error("imessage: setup failed", "error", err)
			os.Exit(1)
		}
		reminderWorker.AddNotifier(iMessageHandler)
		go func() {
			if err := iMessageHandler.Start(ctx); err != nil {
				slog.Error("imessage channel error", "error", err)
			}
		}()
	} else {
		slog.Warn("BLUEBUBBLES_URL not set — iMessage channel disabled")
	}

	// Start heartbeat worker if HEARTBEAT_INTERVAL is configured.
	if cfg.HeartbeatConfigured() {
		hbWorker := heartbeat.New(heartbeat.Config{
			Interval:     cfg.HeartbeatInterval,
			UserID:       cfg.HeartbeatUserID,
			SessionID:    cfg.HeartbeatSessionID,
			Channel:      types.ChannelID(cfg.HeartbeatChannel),
			Prompt:       cfg.HeartbeatPrompt,
			WorkspaceDir: cfg.BuilderSandboxDir,
		}, r)
		if discordHandler != nil {
			hbWorker.AddNotifier(types.ChannelID("discord"), discordHandler)
		}
		if whatsAppHandler != nil {
			hbWorker.AddNotifier(types.ChannelID("whatsapp"), whatsAppHandler)
		}
		if telegramHandler != nil {
			hbWorker.AddNotifier(types.ChannelID("telegram"), telegramHandler)
		}
		if slackHandler != nil {
			hbWorker.AddNotifier(types.ChannelID("slack"), slackHandler)
		}
		if iMessageHandler != nil {
			hbWorker.AddNotifier(types.ChannelID("imessage"), iMessageHandler)
		}
		hbWorker.AddNotifier(types.ChannelID("web"), web.ReminderNotifier{})
		go hbWorker.Run(ctx)
	} else {
		slog.Info("HEARTBEAT_INTERVAL not set — heartbeat worker disabled")
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
	if telegramHandler != nil {
		telegramHandler.Stop()
	}
	if slackHandler != nil {
		slackHandler.Stop()
	}
	if iMessageHandler != nil {
		iMessageHandler.Stop()
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

// newSearchProvider returns a SearchProvider for the configured search backend.
// If SEARCH_API_KEY is not set a stub returning empty results is used and a
// warning is logged — agents that depend on web search will fall back to LLM
// knowledge only.
func newSearchProvider(cfg *app.Config) websearch.SearchProvider {
	if !cfg.SearchConfigured() {
		slog.Warn("SEARCH_API_KEY not set — web search tools disabled; research agent will use LLM knowledge only")
		return &stubSearchProvider{}
	}
	slog.Info("web search enabled", "provider", cfg.SearchProvider)
	return searchBrave.New(cfg.SearchAPIKey)
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
