// Package app provides application-level bootstrapping for Agent OS:
// configuration loading, validation, and provider wiring.
//
// # Usage
//
//	cfg, err := app.Load(".env")
//	if err != nil {
//	    log.Fatal(err)
//	}
//
// Load reads from an optional .env file first, then from real environment
// variables (which always take precedence). It validates required fields and
// returns a descriptive error if any are missing.
//
// No code below main should call os.Getenv directly. Always pass a *Config.
package app

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// Config holds all runtime configuration for Agent OS. Every field maps 1-to-1
// with an environment variable documented in README.md.
type Config struct {
	// ── Server ────────────────────────────────────────────────────────────────

	// Port is the TCP port the HTTP server listens on.
	// Env: PORT (default: "9091")
	Port string

	// LogLevel sets the minimum slog log level.
	// Env: LOG_LEVEL (default: "info"). Valid: debug, info, warn, error.
	LogLevel string

	// ── Costguard LLM gateway ─────────────────────────────────────────────────

	// CostguardURL is the base URL of the Costguard gateway. Required.
	// Env: COSTGUARD_URL
	CostguardURL string

	// CostguardAPIKey is the optional bearer token for the Costguard gateway.
	// Env: COSTGUARD_API_KEY
	CostguardAPIKey string
	CommsModel      string
	BuilderModel    string
	ResearchModel   string
	ClassifierModel string

	// ── Google (Gmail + Calendar) ─────────────────────────────────────────────

	// A single OAuth2 client and refresh token covers both Gmail and Google
	// Calendar — obtained via: go run ./cmd/tool/googleauth/
	// Env: GOOGLE_CLIENT_ID, GOOGLE_CLIENT_SECRET, GOOGLE_REFRESH_TOKEN
	GoogleClientID     string
	GoogleClientSecret string
	GoogleRefreshToken string

	// ── Microsoft (Outlook Mail + Calendar) ───────────────────────────────────

	// A single OAuth2 client and refresh token covers both Outlook Mail and
	// Outlook Calendar — obtained via: go run ./cmd/tool/microsoftauth/
	// Env: MICROSOFT_CLIENT_ID, MICROSOFT_REFRESH_TOKEN
	MicrosoftClientID     string
	MicrosoftRefreshToken string

	// ── Discord channel ───────────────────────────────────────────────────────

	// DiscordBotToken is the bot token for the Discord gateway.
	// Env: DISCORD_BOT_TOKEN (required to enable Discord)
	DiscordBotToken string

	// DiscordGuildID restricts the bot to a single guild (server).
	// Env: DISCORD_GUILD_ID (optional — empty means all guilds)
	DiscordGuildID string

	// DiscordPrefix is an optional command prefix for guild channels (e.g. "!ai").
	// When set, only messages starting with this prefix (or a bot @mention) are
	// routed in server channels. DMs are always routed. Default: "" (no filter).
	// Env: DISCORD_PREFIX
	DiscordPrefix string

	// ── Research Agent ────────────────────────────────────────────────────────

	// SearchAPIKey is the API key for the web search provider.
	// Env: SEARCH_API_KEY
	SearchAPIKey string

	// SearchProvider selects the search backend.
	// Env: SEARCH_PROVIDER (default: "brave"). Valid: "brave".
	SearchProvider string

	// ── Builder Agent ─────────────────────────────────────────────────────────

	// BuilderSandboxDir is the root directory for all file and shell operations
	// performed by the Builder Agent.
	// Env: BUILDER_SANDBOX_DIR (default: "workspace")
	BuilderSandboxDir string

	// ── Session store ─────────────────────────────────────────────────────────

	// SessionTTL is how long idle sessions are kept in memory before expiry.
	// Env: SESSION_TTL (default: 24h). Accepts any value parseable by time.ParseDuration.
	SessionTTL time.Duration

	// ── WhatsApp channel ─────────────────────────────────────────────────────

	// WhatsAppStorePath is the path to the SQLite DB that stores the WhatsApp
	// device pairing session. Setting this enables the WhatsApp channel.
	// Env: WHATSAPP_STORE_PATH (default: "" — WhatsApp disabled)
	WhatsAppStorePath string

	// WhatsAppAllowedJID is the only WhatsApp JID that Agent OS will respond to.
	// Required when WHATSAPP_STORE_PATH is set; the server refuses to start if
	// this is empty and WHATSAPP_STORE_PATH is configured.
	// Env: WHATSAPP_ALLOWED_JID (e.g. "96170123456@s.whatsapp.net")
	WhatsAppAllowedJID string

	// ── Persistence ───────────────────────────────────────────────────────────

	// SQLitePath is the file path for the SQLite database used to persist user
	// profiles and project state. When empty, in-memory stores are used and data
	// is lost on restart (acceptable for local development without any config).
	// Env: SQLITE_PATH (default: "" — in-memory)
	SQLitePath string
}

// Load reads configuration from the given .env file (if it exists) and then
// from actual environment variables, which take precedence over the file.
// Returns a descriptive error if any required field is missing.
func Load(envFile string) (*Config, error) {
	if err := loadDotEnv(envFile); err != nil {
		return nil, fmt.Errorf("config: load env file %q: %w", envFile, err)
	}

	cfg := &Config{
		Port:     envOr("PORT", "9091"),
		LogLevel: envOr("LOG_LEVEL", "info"),

		CostguardURL:    os.Getenv("COSTGUARD_URL"),
		CostguardAPIKey: os.Getenv("COSTGUARD_API_KEY"),
		CommsModel:      envOr("COMMS_MODEL", "gemma4:26b"),
		BuilderModel:    envOr("BUILDER_MODEL", "gemma4:26b"),
		ResearchModel:   envOr("RESEARCH_MODEL", "gemma4:26b"),
		ClassifierModel: envOr("CLASSIFIER_MODEL", "gemma4:26b"),

		GoogleClientID:     os.Getenv("GOOGLE_CLIENT_ID"),
		GoogleClientSecret: os.Getenv("GOOGLE_CLIENT_SECRET"),
		GoogleRefreshToken: os.Getenv("GOOGLE_REFRESH_TOKEN"),

		MicrosoftClientID:     os.Getenv("MICROSOFT_CLIENT_ID"),
		MicrosoftRefreshToken: os.Getenv("MICROSOFT_REFRESH_TOKEN"),

		DiscordBotToken: os.Getenv("DISCORD_BOT_TOKEN"),
		DiscordGuildID:  os.Getenv("DISCORD_GUILD_ID"),
		DiscordPrefix:   os.Getenv("DISCORD_PREFIX"),

		SearchAPIKey:   os.Getenv("SEARCH_API_KEY"),
		SearchProvider: envOr("SEARCH_PROVIDER", "brave"),

		BuilderSandboxDir: envOr("BUILDER_SANDBOX_DIR", "workspace"),
		SessionTTL:        envDuration("SESSION_TTL", 24*time.Hour),

		WhatsAppStorePath:  os.Getenv("WHATSAPP_STORE_PATH"),
		WhatsAppAllowedJID: os.Getenv("WHATSAPP_ALLOWED_JID"),

		SQLitePath: os.Getenv("SQLITE_PATH"),
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// validate returns a descriptive error for any required field that is missing.
func (c *Config) validate() error {
	var missing []string
	if c.CostguardURL == "" {
		missing = append(missing, "COSTGUARD_URL is required — set it to your Costguard gateway base URL (e.g. http://localhost:8080)")
	}
	if c.WhatsAppStorePath != "" && c.WhatsAppAllowedJID == "" {
		missing = append(missing, "WHATSAPP_ALLOWED_JID is required when WhatsApp is enabled — set it to your personal number's JID (e.g. 96170123456@s.whatsapp.net)")
	}
	if len(missing) > 0 {
		return errors.New("config: " + strings.Join(missing, "; "))
	}
	return nil
}

// SQLiteConfigured reports whether a SQLite path is set.
// When false the server falls back to in-memory stores.
func (c *Config) SQLiteConfigured() bool {
	return c.SQLitePath != ""
}

// DiscordConfigured reports whether a Discord bot token is present.
func (c *Config) DiscordConfigured() bool {
	return c.DiscordBotToken != ""
}

// WhatsAppConfigured reports whether a WhatsApp store path is set.
func (c *Config) WhatsAppConfigured() bool {
	return c.WhatsAppStorePath != ""
}

// SearchConfigured reports whether a search API key is present.
func (c *Config) SearchConfigured() bool {
	return c.SearchAPIKey != ""
}

// GoogleConfigured reports whether all Google OAuth2 credentials are present.
// A single token covers both Gmail and Google Calendar.
func (c *Config) GoogleConfigured() bool {
	return c.GoogleClientID != "" && c.GoogleClientSecret != "" && c.GoogleRefreshToken != ""
}

// MicrosoftConfigured reports whether the Microsoft OAuth2 credentials are present.
// A single token covers both Outlook Mail and Outlook Calendar.
func (c *Config) MicrosoftConfigured() bool {
	return c.MicrosoftClientID != "" && c.MicrosoftRefreshToken != ""
}

// ── helpers ───────────────────────────────────────────────────────────────────

func envOr(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}

func envDuration(key string, defaultVal time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return defaultVal
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return defaultVal
	}
	return d
}

// loadDotEnv reads KEY=VALUE pairs from the file at path and registers each as
// an environment variable via os.Setenv. Lines that are already set in the
// process environment are skipped so that real env vars always win.
//
// Supported syntax:
//   - Blank lines and lines starting with # are ignored.
//   - Values may be quoted with " or '; the quotes are stripped.
//   - Inline comments are NOT supported (# after a value is part of the value).
//
// If the file does not exist, loadDotEnv returns nil without error.
func loadDotEnv(path string) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // .env is optional
		}
		return err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)

		// Strip surrounding single or double quotes.
		if len(value) >= 2 {
			if (value[0] == '"' && value[len(value)-1] == '"') ||
				(value[0] == '\'' && value[len(value)-1] == '\'') {
				value = value[1 : len(value)-1]
			}
		}

		// Real env vars win — only set if not already present.
		if os.Getenv(key) == "" {
			os.Setenv(key, value) //nolint:errcheck
		}
	}
	return scanner.Err()
}
