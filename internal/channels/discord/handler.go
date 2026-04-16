// Package discord exposes the Router Agent over the Discord gateway.
// It follows the same Dispatcher interface as the web channel so the router
// is completely unaware of the transport.
package discord

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"

	"github.com/marcoantonios1/Agent-OS/internal/channels/web"
	"github.com/marcoantonios1/Agent-OS/internal/types"
)

// maxMessageLen is Discord's per-message character limit.
const maxMessageLen = 2000

// Handler listens for Discord messages and routes them through the shared
// Dispatcher (router.Router). One Handler per bot token.
type Handler struct {
	dispatcher web.Dispatcher
	botToken   string
	guildID    string // optional — empty means all guilds
	log        *slog.Logger
	session    *discordgo.Session
}

// New creates a Handler. guildID may be empty to accept messages from any guild.
func New(dispatcher web.Dispatcher, botToken, guildID string) *Handler {
	return &Handler{
		dispatcher: dispatcher,
		botToken:   botToken,
		guildID:    guildID,
		log:        slog.Default(),
	}
}

// Start opens the Discord WebSocket connection and blocks until ctx is cancelled.
// It registers the MessageCreate handler before connecting so no events are missed.
func (h *Handler) Start(ctx context.Context) error {
	dg, err := discordgo.New("Bot " + h.botToken)
	if err != nil {
		return fmt.Errorf("discord: create session: %w", err)
	}
	h.session = dg

	// Only request the events we need.
	dg.Identify.Intents = discordgo.IntentsGuildMessages | discordgo.IntentsDirectMessages

	dg.AddHandler(h.onMessageCreate)

	if err := dg.Open(); err != nil {
		return fmt.Errorf("discord: open WebSocket: %w", err)
	}
	h.log.Info("discord channel started", "guild_id", h.guildID)

	// Block until the context is cancelled (SIGTERM / SIGINT).
	<-ctx.Done()
	return nil
}

// Stop closes the Discord WebSocket connection gracefully.
// Safe to call more than once.
func (h *Handler) Stop() {
	if h.session != nil {
		h.log.Info("discord channel stopping")
		h.session.Close() //nolint:errcheck
	}
}

// onMessageCreate is called by discordgo for every MessageCreate event.
func (h *Handler) onMessageCreate(s *discordgo.Session, m *discordgo.MessageCreate) {
	// Skip messages from bots (including our own).
	if m.Author == nil || m.Author.Bot {
		return
	}

	// If a guild filter is configured, ignore messages from other guilds.
	if h.guildID != "" && m.GuildID != h.guildID {
		return
	}

	// Ignore empty messages and messages that are just mentions.
	text := strings.TrimSpace(m.Content)
	if text == "" {
		return
	}

	// Build a stable session ID scoped to this user × channel × guild.
	// This gives each user their own conversation thread per channel.
	guildPart := m.GuildID
	if guildPart == "" {
		guildPart = "dm" // direct-message context
	}
	sessionID := fmt.Sprintf("discord:%s:%s:%s", guildPart, m.ChannelID, m.Author.ID)

	ctx := context.Background()
	h.log.InfoContext(ctx, "channel_received",
		"session_id", sessionID,
		"user_id", m.Author.ID,
		"channel_id", m.ChannelID,
		"guild_id", m.GuildID,
		"text_length", len(text),
		"channel", "discord",
	)

	start := time.Now()
	inbound := types.InboundMessage{
		ID:        m.ID,
		ChannelID: types.ChannelID("discord"),
		UserID:    m.Author.ID,
		SessionID: sessionID,
		Text:      text,
		Timestamp: start,
	}

	out, err := h.dispatcher.Route(ctx, inbound)
	if err != nil {
		h.log.ErrorContext(ctx, "discord route error",
			"session_id", sessionID, "error", err)
		s.ChannelMessageSend(m.ChannelID, "Sorry, something went wrong. Please try again.") //nolint:errcheck
		return
	}

	h.log.InfoContext(ctx, "channel_response",
		"session_id", sessionID,
		"latency_ms", time.Since(start).Milliseconds(),
		"channel", "discord",
	)

	// Discord has a 2,000-character limit — split long replies into chunks.
	for _, chunk := range splitMessage(out.Text, maxMessageLen) {
		if _, err := s.ChannelMessageSend(m.ChannelID, chunk); err != nil {
			h.log.WarnContext(ctx, "discord send error",
				"session_id", sessionID, "error", err)
		}
	}
}

// splitMessage breaks text into chunks of at most maxLen characters, splitting
// on newlines where possible so Markdown formatting is preserved.
func splitMessage(text string, maxLen int) []string {
	if len(text) <= maxLen {
		return []string{text}
	}

	var chunks []string
	for len(text) > 0 {
		if len(text) <= maxLen {
			chunks = append(chunks, text)
			break
		}

		// Try to split on a newline within the last quarter of the window.
		cutAt := maxLen
		if idx := strings.LastIndex(text[:maxLen], "\n"); idx > maxLen*3/4 {
			cutAt = idx + 1
		}

		chunks = append(chunks, text[:cutAt])
		text = text[cutAt:]
	}
	return chunks
}
