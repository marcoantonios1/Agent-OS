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
	prefix     string // optional command prefix (e.g. "!ai"); empty = no prefix filter
	log        *slog.Logger
	session    *discordgo.Session
	botUserID  string // populated after Start() — used to strip @mention prefix
}

// New creates a Handler.
//   - guildID may be empty to accept messages from any guild.
//   - prefix is an optional command prefix (e.g. "!ai"). When non-empty, only
//     messages that start with this prefix (or a bot @mention) are routed in
//     guild channels. DMs are always routed. Pass "" to route all messages.
func New(dispatcher web.Dispatcher, botToken, guildID, prefix string) *Handler {
	return &Handler{
		dispatcher: dispatcher,
		botToken:   botToken,
		guildID:    guildID,
		prefix:     prefix,
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

	// Capture the bot's own user ID so we can strip @mention prefixes.
	if dg.State != nil && dg.State.User != nil {
		h.botUserID = dg.State.User.ID
	}

	h.log.Info("discord channel started", "guild_id", h.guildID, "bot_user_id", h.botUserID)

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
	// DMs have an empty GuildID and always pass through.
	if h.guildID != "" && m.GuildID != "" && m.GuildID != h.guildID {
		return
	}

	isDM := m.GuildID == ""

	text, shouldRoute := preprocessText(m.Content, h.botUserID, h.prefix, isDM)
	if !shouldRoute {
		return
	}

	sid := sessionKey(m.GuildID, m.ChannelID, m.Author.ID)

	ctx := context.Background()
	h.log.InfoContext(ctx, "channel_received",
		"session_id", sid,
		"user_id", m.Author.ID,
		"channel_id", m.ChannelID,
		"guild_id", m.GuildID,
		"is_dm", isDM,
		"text_length", len(text),
		"channel", "discord",
	)

	start := time.Now()
	inbound := types.InboundMessage{
		ID:        m.ID,
		ChannelID: types.ChannelID("discord"),
		UserID:    m.Author.ID,
		SessionID: sid,
		Text:      text,
		Timestamp: start,
	}

	out, err := h.dispatcher.Route(ctx, inbound)
	if err != nil {
		h.log.ErrorContext(ctx, "discord route error",
			"session_id", sid, "error", err)
		s.ChannelMessageSend(m.ChannelID, "Sorry, something went wrong. Please try again.") //nolint:errcheck
		return
	}

	h.log.InfoContext(ctx, "channel_response",
		"session_id", sid,
		"latency_ms", time.Since(start).Milliseconds(),
		"channel", "discord",
	)

	// Discord has a 2,000-character limit — split long replies into chunks.
	for _, chunk := range splitMessage(out.Text, maxMessageLen) {
		if _, err := s.ChannelMessageSend(m.ChannelID, chunk); err != nil {
			h.log.WarnContext(ctx, "discord send error",
				"session_id", sid, "error", err)
		}
	}
}

// sessionKey returns a stable, unique session key for a user × channel × guild
// combination. This ensures each user has an independent conversation thread
// per channel — switching channels starts a fresh context.
//
// Format:
//
//	discord:{guildID}:{channelID}:{userID}   (server channel)
//	discord:dm:{channelID}:{userID}           (direct message)
func sessionKey(guildID, channelID, userID string) string {
	g := guildID
	if g == "" {
		g = "dm"
	}
	return fmt.Sprintf("discord:%s:%s:%s", g, channelID, userID)
}

// preprocessText cleans up inbound message text and reports whether it should
// be routed to the dispatcher.
//
// Rules:
//  1. Bot @mentions (<@botID> and <@!botID>) are always stripped from the text.
//  2. If prefix is non-empty, guild-channel messages must start with that
//     prefix (or a bot mention) to be routed — all other messages are ignored.
//  3. DM messages (isDM=true) are always routed regardless of prefix.
//  4. Empty text (after stripping) is never routed.
func preprocessText(text, botID, prefix string, isDM bool) (string, bool) {
	// Strip bot mention variants (<@ID> and <@!ID>).
	cleaned := stripMention(text, botID)

	if isDM {
		// DMs are always routed; no prefix required.
		cleaned = strings.TrimSpace(cleaned)
		return cleaned, cleaned != ""
	}

	if prefix != "" {
		// Guild channels: require either a bot mention or the command prefix.
		if mentionStripped := strings.TrimSpace(stripMention(text, botID)); mentionStripped != text || mentionStripped != cleaned {
			// A mention was present — accept this message.
			c := strings.TrimSpace(cleaned)
			return c, c != ""
		}
		if strings.HasPrefix(strings.TrimSpace(text), prefix) {
			c := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(text), prefix))
			return c, c != ""
		}
		// Neither mention nor prefix — ignore.
		return "", false
	}

	// No prefix configured — route everything but strip mention if present.
	c := strings.TrimSpace(cleaned)
	return c, c != ""
}

// stripMention removes a leading bot @mention from text.
// Handles both <@ID> (user mention) and <@!ID> (nickname mention).
func stripMention(text, botID string) string {
	if botID == "" {
		return text
	}
	for _, form := range []string{"<@!" + botID + ">", "<@" + botID + ">"} {
		if strings.HasPrefix(strings.TrimSpace(text), form) {
			return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(text), form))
		}
	}
	return text
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
