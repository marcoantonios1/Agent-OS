// Package telegram exposes the Router Agent over the Telegram Bot API.
// It follows the same Dispatcher interface as the web and Discord channels so
// the router is completely unaware of the transport.
package telegram

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"github.com/marcoantonios1/Agent-OS/internal/attachments"
	"github.com/marcoantonios1/Agent-OS/internal/channels/web"
	"github.com/marcoantonios1/Agent-OS/internal/sessions"
	"github.com/marcoantonios1/Agent-OS/internal/types"
	"github.com/marcoantonios1/Agent-OS/internal/voice"
)

// maxMessageLen is Telegram's per-message character limit.
const maxMessageLen = 4096

// editInterval is how often the in-progress message is edited while streaming.
// 500 ms gives ~2 edits/sec — well within Telegram's rate limits.
const editInterval = 500 * time.Millisecond

// BotAPI is the minimal interface the Handler needs from the Telegram bot
// library. *tgbotapi.BotAPI satisfies this interface directly; tests may
// supply a mock.
type BotAPI interface {
	Send(c tgbotapi.Chattable) (tgbotapi.Message, error)
	GetFileDirectURL(fileID string) (string, error)
	GetUpdatesChan(config tgbotapi.UpdateConfig) tgbotapi.UpdatesChannel
	StopReceivingUpdates()
}

// Handler listens for Telegram updates and routes them through the shared
// Dispatcher (router.Router). One Handler per bot token.
type Handler struct {
	bot        BotAPI
	username   string // bot's own username, set by New() from Self.UserName
	dispatcher web.Dispatcher
	allowedUID int64  // silently drop messages from any other user ID
	transcriber voice.Transcriber
	log        *slog.Logger
	httpClient *http.Client
}

// SetTranscriber replaces the handler's transcriber. Call after New() to enable
// voice-to-text; omitting it leaves the NoopTranscriber default in place.
func (h *Handler) SetTranscriber(t voice.Transcriber) { h.transcriber = t }

// New creates a Handler and validates the bot token by calling GetMe.
// Returns an error if the token is invalid or the Telegram API is unreachable.
func New(dispatcher web.Dispatcher, token string, allowedUID int64) (*Handler, error) {
	bot, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		return nil, fmt.Errorf("telegram: create bot: %w", err)
	}
	return &Handler{
		bot:         bot,
		username:    bot.Self.UserName,
		dispatcher:  dispatcher,
		allowedUID:  allowedUID,
		transcriber: &voice.NoopTranscriber{},
		log:         slog.Default(),
		httpClient:  http.DefaultClient,
	}, nil
}

// NewForTest creates a Handler for testing, bypassing Telegram token
// validation. bot is a mock implementation of BotAPI.
func NewForTest(dispatcher web.Dispatcher, bot BotAPI, allowedUID int64) *Handler {
	return &Handler{
		bot:         bot,
		username:    "testbot",
		dispatcher:  dispatcher,
		allowedUID:  allowedUID,
		transcriber: &voice.NoopTranscriber{},
		log:         slog.Default(),
		httpClient:  http.DefaultClient,
	}
}

// HandleMessage exposes handleMessage for integration testing without going
// through the long-polling loop in Start().
func (h *Handler) HandleMessage(ctx context.Context, msg *tgbotapi.Message) {
	h.handleMessage(ctx, msg)
}

// Start begins long-polling for updates and blocks until ctx is cancelled.
func (h *Handler) Start(ctx context.Context) error {
	cfg := tgbotapi.NewUpdate(0)
	cfg.Timeout = 60
	updates := h.bot.GetUpdatesChan(cfg)

	h.log.Info("telegram channel started",
		"bot_username", h.username,
		"allowed_uid", h.allowedUID,
	)

	for {
		select {
		case update, ok := <-updates:
			if !ok {
				return nil
			}
			if update.Message == nil {
				continue
			}
			go h.handleMessage(ctx, update.Message)
		case <-ctx.Done():
			h.bot.StopReceivingUpdates()
			return nil
		}
	}
}

// Stop halts the long-polling loop. Safe to call more than once.
func (h *Handler) Stop() {
	h.log.Info("telegram channel stopping")
	h.bot.StopReceivingUpdates()
}

// handleMessage processes one incoming Telegram message.
func (h *Handler) handleMessage(ctx context.Context, msg *tgbotapi.Message) {
	// Silently drop messages from non-whitelisted users.
	if msg.From == nil || msg.From.ID != h.allowedUID {
		return
	}

	sid := sessionKey(msg.From.ID, msg.Chat.ID)

	h.log.InfoContext(ctx, "channel_received",
		"session_id", sid,
		"user_id", msg.From.ID,
		"chat_id", msg.Chat.ID,
		"channel", "telegram",
	)

	switch {
	case msg.Voice != nil:
		mimeType := "audio/ogg"
		if msg.Voice.MimeType != "" {
			mimeType = msg.Voice.MimeType
		}
		h.handleAudio(ctx, msg, sid, msg.Voice.FileID, mimeType)

	case msg.Audio != nil:
		mimeType := "audio/mpeg"
		if msg.Audio.MimeType != "" {
			mimeType = msg.Audio.MimeType
		}
		h.handleAudio(ctx, msg, sid, msg.Audio.FileID, mimeType)

	case msg.Photo != nil:
		// Telegram provides multiple photo sizes; the last one is the largest.
		largest := msg.Photo[len(msg.Photo)-1]
		parts, err := h.downloadFile(ctx, largest.FileID, "image/jpeg", "photo.jpg")
		if err != nil {
			h.log.WarnContext(ctx, "telegram: failed to download photo",
				"session_id", sid, "error", err)
			h.sendText(msg.Chat.ID, "Sorry, I couldn't download your photo.")
			return
		}
		inbound := h.buildInbound(msg, sid, parts)
		h.routeAndRespond(ctx, msg.Chat.ID, sid, inbound)

	case msg.Document != nil:
		doc := msg.Document
		if doc.MimeType != "application/pdf" {
			h.sendText(msg.Chat.ID,
				"I can read images and PDFs — other file types aren't supported yet.")
			return
		}
		parts, err := h.downloadFile(ctx, doc.FileID, doc.MimeType, doc.FileName)
		if err != nil {
			h.log.WarnContext(ctx, "telegram: failed to download document",
				"session_id", sid, "error", err)
			h.sendText(msg.Chat.ID, "Sorry, I couldn't read that document.")
			return
		}
		inbound := h.buildInbound(msg, sid, parts)
		h.routeAndRespond(ctx, msg.Chat.ID, sid, inbound)

	case msg.Text != "":
		inbound := h.buildInbound(msg, sid, nil)
		h.routeAndRespond(ctx, msg.Chat.ID, sid, inbound)

	default:
		h.sendText(msg.Chat.ID,
			"I can handle text, photos, and PDF documents. Other message types aren't supported yet.")
	}
}

// handleAudio downloads a Telegram audio file, transcribes it, and routes the
// result. Used for both Voice messages (OGG) and Audio file uploads.
func (h *Handler) handleAudio(ctx context.Context, msg *tgbotapi.Message, sid, fileID, mimeType string) {
	h.log.InfoContext(ctx, "telegram: audio message received",
		"session_id", sid, "mime", mimeType)

	url, err := h.bot.GetFileDirectURL(fileID)
	if err != nil {
		h.log.WarnContext(ctx, "telegram: failed to get audio URL",
			"session_id", sid, "error", err)
		h.sendText(msg.Chat.ID, "Sorry, I couldn't access that voice message.")
		return
	}
	audioBytes, err := h.fetchRaw(ctx, url)
	if err != nil {
		h.log.WarnContext(ctx, "telegram: failed to download audio",
			"session_id", sid, "error", err)
		h.sendText(msg.Chat.ID, "Sorry, I couldn't download that voice message.")
		return
	}
	text, transcribeErr := h.transcriber.Transcribe(ctx, audioBytes, mimeType)
	if errors.Is(transcribeErr, voice.ErrNotSupported) {
		h.log.InfoContext(ctx, "telegram: voice transcription disabled (VOICE_TRANSCRIPTION not set to 'enabled')",
			"session_id", sid)
		h.sendText(msg.Chat.ID,
			"Voice messages aren't supported yet — please type your message.")
		return
	}
	if transcribeErr != nil {
		h.log.WarnContext(ctx, "telegram: transcription failed",
			"session_id", sid, "error", transcribeErr)
		h.sendText(msg.Chat.ID,
			"Sorry, I couldn't transcribe that voice message — please type your message.")
		return
	}
	inbound := types.InboundMessage{
		ID:        strconv.Itoa(msg.MessageID),
		ChannelID: types.ChannelID("telegram"),
		UserID:    strconv.FormatInt(msg.From.ID, 10),
		SessionID: sid,
		Text:      fmt.Sprintf("[Voice message transcribed]: %s", text),
		Timestamp: time.Now(),
	}
	h.routeAndRespond(ctx, msg.Chat.ID, sid, inbound)
}

// buildInbound constructs a types.InboundMessage from a Telegram message.
func (h *Handler) buildInbound(msg *tgbotapi.Message, sid string, attParts []types.ContentPart) types.InboundMessage {
	var parts []types.ContentPart
	if len(attParts) > 0 {
		parts = make([]types.ContentPart, 0, 1+len(attParts))
		if msg.Text != "" {
			parts = append(parts, types.ContentPart{Type: "text", Text: msg.Text})
		}
		parts = append(parts, attParts...)
	}
	return types.InboundMessage{
		ID:        strconv.Itoa(msg.MessageID),
		ChannelID: types.ChannelID("telegram"),
		UserID:    strconv.FormatInt(msg.From.ID, 10),
		SessionID: sid,
		Text:      msg.Text,
		Timestamp: time.Now(),
		Parts:     parts,
	}
}

// routeAndRespond dispatches inbound via streaming when the dispatcher supports
// it, falling back to blocking Route() on error or when streaming is unavailable.
func (h *Handler) routeAndRespond(ctx context.Context, chatID int64, sid string, inbound types.InboundMessage) {
	start := time.Now()
	if sd, ok := h.dispatcher.(web.StreamDispatcher); ok {
		chunks, err := sd.RouteStream(ctx, inbound)
		if err == nil {
			h.respondStreaming(ctx, chatID, sid, start, chunks)
			return
		}
		h.log.WarnContext(ctx, "telegram: stream route failed, falling back to blocking",
			"session_id", sid, "error", err)
	}

	out, err := h.dispatcher.Route(ctx, inbound)
	if err != nil {
		h.log.ErrorContext(ctx, "telegram route error", "session_id", sid, "error", err)
		h.sendText(chatID, "Sorry, something went wrong. Please try again.")
		return
	}
	h.log.InfoContext(ctx, "channel_response",
		"session_id", sid, "latency_ms", time.Since(start).Milliseconds(), "channel", "telegram")
	for _, part := range splitMessage(out.Text, maxMessageLen) {
		h.sendText(chatID, part)
	}
}

// respondStreaming sends a "…" placeholder then edits it as tokens arrive,
// throttled to editInterval. A final edit delivers the complete text; messages
// over 4 096 chars are sent as additional messages.
func (h *Handler) respondStreaming(
	ctx context.Context,
	chatID int64,
	sid string,
	start time.Time,
	chunks <-chan string,
) {
	placeholder, err := h.bot.Send(tgbotapi.NewMessage(chatID, "…"))
	if err != nil {
		h.log.WarnContext(ctx, "telegram: failed to post placeholder",
			"session_id", sid, "error", err)
		for range chunks {} //nolint:revive — drain so router goroutine can exit
		return
	}
	msgID := placeholder.MessageID

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
				h.editOrLog(ctx, chatID, msgID, sid, truncateForEdit(sb.String()))
			}
		case <-ctx.Done():
			for range chunks {} //nolint:revive
			return
		}
	}

	fullText := sb.String()
	if fullText == "" {
		fullText = "(no response)"
	}
	parts := splitMessage(fullText, maxMessageLen)
	h.editOrLog(ctx, chatID, msgID, sid, parts[0])
	for _, extra := range parts[1:] {
		h.sendText(chatID, extra)
	}

	h.log.InfoContext(ctx, "channel_response",
		"session_id", sid, "latency_ms", time.Since(start).Milliseconds(), "channel", "telegram-stream")
}

// editOrLog edits a Telegram message, logging a warning on failure.
func (h *Handler) editOrLog(ctx context.Context, chatID int64, msgID int, sid, text string) {
	edit := tgbotapi.NewEditMessageText(chatID, msgID, text)
	if _, err := h.bot.Send(edit); err != nil {
		h.log.WarnContext(ctx, "telegram edit error", "session_id", sid, "error", err)
	}
}

// sendText sends a plain text message to the given chat, logging any error.
func (h *Handler) sendText(chatID int64, text string) {
	if _, err := h.bot.Send(tgbotapi.NewMessage(chatID, text)); err != nil {
		h.log.Warn("telegram: send error", "chat_id", chatID, "error", err)
	}
}

// fetchRaw downloads raw bytes from url using the handler's HTTP client.
func (h *Handler) fetchRaw(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	resp, err := h.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download: %w", err)
	}
	data, readErr := io.ReadAll(resp.Body)
	resp.Body.Close()
	if readErr != nil {
		return nil, fmt.Errorf("read body: %w", readErr)
	}
	return data, nil
}

// downloadFile retrieves a Telegram file by ID and returns it as a ContentPart.
// For images it base64-encodes the raw bytes; for PDFs it extracts the text.
func (h *Handler) downloadFile(ctx context.Context, fileID, mimeType, filename string) ([]types.ContentPart, error) {
	url, err := h.bot.GetFileDirectURL(fileID)
	if err != nil {
		return nil, fmt.Errorf("get file URL: %w", err)
	}
	data, err := h.fetchRaw(ctx, url)
	if err != nil {
		return nil, err
	}

	switch {
	case strings.HasPrefix(mimeType, "image/"):
		return []types.ContentPart{{
			Type:      "image",
			ImageData: base64.StdEncoding.EncodeToString(data),
			MimeType:  mimeType,
			Filename:  filename,
		}}, nil

	case mimeType == "application/pdf":
		text, err := attachments.ExtractPDFText(data)
		if err != nil {
			return nil, fmt.Errorf("extract PDF: %w", err)
		}
		return []types.ContentPart{{
			Type:     "text",
			Text:     text,
			Filename: filename,
		}}, nil
	}

	return nil, fmt.Errorf("unsupported MIME type %q", mimeType)
}

// NotifyReminder implements reminder.Notifier. It resolves the target chat ID
// from r.SessionID (format "telegram:{userID}:{chatID}"), falling back to
// r.UserID interpreted as the chat ID (works for DM chats where chatID == userID).
func (h *Handler) NotifyReminder(ctx context.Context, r *sessions.Reminder) error {
	chatID, err := chatIDFromReminder(r)
	if err != nil {
		return nil // reminder is not for this channel — silently ignore
	}
	msg := tgbotapi.NewMessage(chatID, r.Message)
	if _, err := h.bot.Send(msg); err != nil {
		return fmt.Errorf("telegram: send reminder: %w", err)
	}
	h.log.InfoContext(ctx, "telegram: reminder delivered",
		"reminder_id", r.ID, "chat_id", chatID)
	return nil
}

// chatIDFromReminder extracts the Telegram chat ID from a Reminder.
// Tries r.SessionID ("telegram:{userID}:{chatID}") first, then r.UserID.
func chatIDFromReminder(r *sessions.Reminder) (int64, error) {
	if strings.HasPrefix(r.SessionID, "telegram:") {
		parts := strings.SplitN(r.SessionID, ":", 3)
		if len(parts) == 3 {
			if id, err := strconv.ParseInt(parts[2], 10, 64); err == nil {
				return id, nil
			}
		}
	}
	if r.UserID != "" {
		if id, err := strconv.ParseInt(r.UserID, 10, 64); err == nil {
			return id, nil
		}
	}
	return 0, fmt.Errorf("cannot resolve telegram chat ID from reminder %q", r.ID)
}

// sessionKey returns a stable session key for a user × chat combination.
//
// Format: telegram:{userID}:{chatID}
func sessionKey(userID, chatID int64) string {
	return fmt.Sprintf("telegram:%d:%d", userID, chatID)
}

// truncateForEdit truncates text to fit within Telegram's 4 096-char limit,
// appending "…" and breaking at a word boundary when possible.
func truncateForEdit(text string) string {
	const ellipsis = "…"
	if len(text) <= maxMessageLen {
		return text
	}
	cut := maxMessageLen - len(ellipsis)
	if idx := strings.LastIndexByte(text[:cut], ' '); idx > cut*3/4 {
		cut = idx
	}
	return text[:cut] + ellipsis
}

// splitMessage breaks text into chunks of at most maxLen characters, splitting
// on newlines where possible so formatting is preserved.
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
		cut := maxLen
		if idx := strings.LastIndex(text[:maxLen], "\n"); idx > maxLen*3/4 {
			cut = idx + 1
		}
		chunks = append(chunks, text[:cut])
		text = text[cut:]
	}
	return chunks
}
