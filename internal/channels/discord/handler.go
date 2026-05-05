// Package discord exposes the Router Agent over the Discord gateway.
// It follows the same Dispatcher interface as the web channel so the router
// is completely unaware of the transport.
package discord

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"

	"github.com/marcoantonios1/Agent-OS/internal/attachments"
	"github.com/marcoantonios1/Agent-OS/internal/channels/web"
	"github.com/marcoantonios1/Agent-OS/internal/types"
)

// maxMessageLen is Discord's per-message character limit.
const maxMessageLen = 2000

// maxDiscordAttachments is the maximum number of attachments processed per message.
const maxDiscordAttachments = 5

// discordSupportedTypes lists the attachment MIME types the Discord handler processes.
var discordSupportedTypes = map[string]bool{
	"image/jpeg":      true,
	"image/png":       true,
	"image/webp":      true,
	"image/gif":       true,
	"application/pdf": true,
}

// extMimeTypes is a fallback map from lowercase file extension to MIME type,
// used when Discord does not provide a Content-Type on the attachment.
var extMimeTypes = map[string]string{
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".png":  "image/png",
	".webp": "image/webp",
	".gif":  "image/gif",
	".pdf":  "application/pdf",
}

// editInterval is how often the in-progress message is edited while streaming.
// 500 ms gives ~2 edits/sec — well within Discord's ~5 edits/sec rate limit.
const editInterval = 500 * time.Millisecond

// Handler listens for Discord messages and routes them through the shared
// Dispatcher (router.Router). One Handler per bot token.
type Handler struct {
	dispatcher web.Dispatcher
	botToken   string
	guildID    string // optional — empty means all guilds
	prefix     string // optional command prefix (e.g. "!ai"); empty = no prefix filter
	log        *slog.Logger
	session    *discordgo.Session
	botUserID  string       // populated after Start() — used to strip @mention prefix
	httpClient *http.Client // used to download attachments; defaults to http.DefaultClient
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
		httpClient: http.DefaultClient,
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

	ctx := context.Background()

	// For attachment-only messages (no text / stripped text is empty): still
	// route when the message passes the prefix/mention filter.
	if !shouldRoute && !passesFilter(m.Content, h.botUserID, h.prefix, isDM) {
		return
	}

	attParts := h.fetchAttachments(ctx, m.Attachments)

	// Nothing to route: no text and no usable attachments.
	if !shouldRoute && len(attParts) == 0 {
		return
	}

	sid := sessionKey(m.GuildID, m.ChannelID, m.Author.ID)

	h.log.InfoContext(ctx, "channel_received",
		"session_id", sid,
		"user_id", m.Author.ID,
		"channel_id", m.ChannelID,
		"guild_id", m.GuildID,
		"is_dm", isDM,
		"text_length", len(text),
		"attachments", len(attParts),
		"channel", "discord",
	)

	// Build the Parts slice when attachments are present.
	// The user's text is prepended as the first part so the LLM always sees
	// the question alongside the attachment content.
	var msgParts []types.ContentPart
	if len(attParts) > 0 {
		msgParts = make([]types.ContentPart, 0, 1+len(attParts))
		if text != "" {
			msgParts = append(msgParts, types.ContentPart{Type: "text", Text: text})
		}
		msgParts = append(msgParts, attParts...)
	}

	start := time.Now()
	inbound := types.InboundMessage{
		ID:        m.ID,
		ChannelID: types.ChannelID("discord"),
		UserID:    m.Author.ID,
		SessionID: sid,
		Text:      text,
		Timestamp: start,
		Parts:     msgParts,
	}

	h.routeAndRespond(s, ctx, m.ChannelID, inbound, sid, start)
}

// routeAndRespond dispatches inbound via streaming when the dispatcher supports
// it, falling back to blocking Route() on error or when streaming is unavailable.
func (h *Handler) routeAndRespond(
	s *discordgo.Session,
	ctx context.Context,
	channelID string,
	inbound types.InboundMessage,
	sid string,
	start time.Time,
) {
	if sd, ok := h.dispatcher.(web.StreamDispatcher); ok {
		chunks, err := sd.RouteStream(ctx, inbound)
		if err == nil {
			h.respondStreaming(s, ctx, channelID, sid, start, chunks)
			return
		}
		h.log.WarnContext(ctx, "discord: stream route failed, falling back to blocking",
			"session_id", sid, "error", err)
	}

	// Blocking fallback.
	out, err := h.dispatcher.Route(ctx, inbound)
	if err != nil {
		h.log.ErrorContext(ctx, "discord route error", "session_id", sid, "error", err)
		s.ChannelMessageSend(channelID, "Sorry, something went wrong. Please try again.") //nolint:errcheck
		return
	}
	h.log.InfoContext(ctx, "channel_response",
		"session_id", sid, "latency_ms", time.Since(start).Milliseconds(), "channel", "discord")
	for _, part := range splitMessage(out.Text, maxMessageLen) {
		if _, err := s.ChannelMessageSend(channelID, part); err != nil {
			h.log.WarnContext(ctx, "discord send error", "session_id", sid, "error", err)
		}
	}
}

// respondStreaming posts an initial "…" placeholder then edits it as tokens
// arrive, throttled to editInterval. A final edit delivers the complete text;
// overflow beyond 2 000 characters is sent as additional messages.
func (h *Handler) respondStreaming(
	s *discordgo.Session,
	ctx context.Context,
	channelID, sid string,
	start time.Time,
	chunks <-chan string,
) {
	placeholder, err := s.ChannelMessageSend(channelID, "…")
	if err != nil {
		h.log.WarnContext(ctx, "discord: failed to post placeholder",
			"session_id", sid, "error", err)
		for range chunks {} //nolint:revive — drain so router goroutine can exit
		return
	}
	msgID := placeholder.ID

	var sb strings.Builder
	ticker := time.NewTicker(editInterval)
	defer ticker.Stop()

outerLoop:
	for {
		select {
		case chunk, ok := <-chunks:
			if !ok {
				break outerLoop
			}
			sb.WriteString(chunk)
		case <-ticker.C:
			if sb.Len() > 0 {
				h.editOrLog(s, ctx, channelID, msgID, sid, truncateForEdit(sb.String()))
			}
		case <-ctx.Done():
			for range chunks {} //nolint:revive
			return
		}
	}

	// Final edit: full response, split across messages if needed.
	fullText := sb.String()
	if fullText == "" {
		fullText = "(no response)"
	}
	parts := splitMessage(fullText, maxMessageLen)
	h.editOrLog(s, ctx, channelID, msgID, sid, parts[0])
	for _, extra := range parts[1:] {
		if _, err := s.ChannelMessageSend(channelID, extra); err != nil {
			h.log.WarnContext(ctx, "discord send overflow error", "session_id", sid, "error", err)
		}
	}

	h.log.InfoContext(ctx, "channel_response",
		"session_id", sid, "latency_ms", time.Since(start).Milliseconds(), "channel", "discord-stream")
}

// editOrLog edits a Discord message, logging a warning on failure.
func (h *Handler) editOrLog(s *discordgo.Session, ctx context.Context, channelID, msgID, sid, text string) {
	if _, err := s.ChannelMessageEdit(channelID, msgID, text); err != nil {
		h.log.WarnContext(ctx, "discord edit error", "session_id", sid, "error", err)
	}
}

// truncateForEdit truncates text to fit within Discord's 2 000-char limit,
// appending "…" and breaking at a word boundary when possible.
// "…" is 3 bytes in UTF-8, so we reserve 3 bytes before the cut point.
func truncateForEdit(text string) string {
	const ellipsis = "…" // U+2026 — 3 bytes in UTF-8
	if len(text) <= maxMessageLen {
		return text
	}
	cut := maxMessageLen - len(ellipsis)
	if idx := strings.LastIndexByte(text[:cut], ' '); idx > cut*3/4 {
		cut = idx
	}
	return text[:cut] + ellipsis
}

// passesFilter reports whether a message should be considered for routing
// based on channel context alone — regardless of whether the text is empty.
// Used to decide if an attachment-only message should be routed.
func passesFilter(text, botID, prefix string, isDM bool) bool {
	if isDM || prefix == "" {
		return true
	}
	// Guild channel with prefix: accept if the message has a @mention or the prefix.
	cleaned := strings.TrimSpace(text)
	if stripped := strings.TrimSpace(stripMention(cleaned, botID)); stripped != cleaned {
		return true // had a @mention
	}
	return strings.HasPrefix(cleaned, prefix)
}

// fetchAttachments downloads and processes Discord message attachments into
// ContentParts. Unsupported types are silently skipped; HTTP errors are logged
// and the attachment is skipped. At most maxDiscordAttachments are processed.
func (h *Handler) fetchAttachments(ctx context.Context, atts []*discordgo.MessageAttachment) []types.ContentPart {
	if len(atts) == 0 {
		return nil
	}
	limit := len(atts)
	if limit > maxDiscordAttachments {
		limit = maxDiscordAttachments
	}

	var parts []types.ContentPart
	for _, att := range atts[:limit] {
		mimeType := strings.TrimSpace(strings.SplitN(att.ContentType, ";", 2)[0])
		if mimeType == "" {
			mimeType = extMimeTypes[strings.ToLower(path.Ext(att.Filename))]
		}
		if !discordSupportedTypes[mimeType] {
			if mimeType != "" {
				h.log.InfoContext(ctx, "discord: ignoring unsupported attachment",
					"filename", att.Filename, "mime_type", mimeType)
			}
			continue
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, att.URL, nil)
		if err != nil {
			h.log.WarnContext(ctx, "discord: failed to build download request",
				"filename", att.Filename, "error", err)
			continue
		}
		resp, err := h.httpClient.Do(req)
		if err != nil {
			h.log.WarnContext(ctx, "discord: failed to download attachment",
				"filename", att.Filename, "error", err)
			continue
		}
		data, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			h.log.WarnContext(ctx, "discord: failed to read attachment body",
				"filename", att.Filename, "error", readErr)
			continue
		}

		switch {
		case strings.HasPrefix(mimeType, "image/"):
			parts = append(parts, types.ContentPart{
				Type:      "image",
				ImageData: base64.StdEncoding.EncodeToString(data),
				MimeType:  mimeType,
				Filename:  att.Filename,
			})
		case mimeType == "application/pdf":
			extracted, err := attachments.ExtractPDFText(data)
			if err != nil {
				h.log.WarnContext(ctx, "discord: failed to extract PDF text",
					"filename", att.Filename, "error", err)
				continue
			}
			parts = append(parts, types.ContentPart{
				Type:     "text",
				Text:     extracted,
				Filename: att.Filename,
			})
		}
	}
	return parts
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
