// Package imessage exposes the Router Agent over iMessage via the BlueBubbles
// REST API. It follows the same Dispatcher interface as the web, Discord,
// Telegram, WhatsApp, and Slack channels so the router is completely unaware
// of the transport.
//
// Inbound messages arrive via webhook (BlueBubbles POSTs to Agent OS).
// Outbound messages are sent via the BlueBubbles HTTP API.
//
// Only messages from BLUEBUBBLES_ALLOWED_HANDLE are processed. All other
// senders are silently ignored.
package imessage

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/marcoantonios1/Agent-OS/internal/attachments"
	"github.com/marcoantonios1/Agent-OS/internal/channels/web"
	"github.com/marcoantonios1/Agent-OS/internal/sessions"
	"github.com/marcoantonios1/Agent-OS/internal/types"
	"github.com/marcoantonios1/Agent-OS/internal/voice"
)

// Config holds all runtime parameters for the iMessage channel.
type Config struct {
	// BaseURL is the base URL of the BlueBubbles server (e.g. "http://localhost:1234").
	BaseURL string

	// Password is the BlueBubbles server password.
	Password string

	// AllowedHandle is the iMessage handle (phone number or email) whose messages
	// are routed. All other senders are silently dropped.
	AllowedHandle string

	// WebhookPort is the local TCP port on which Agent OS listens for BlueBubbles
	// webhook events. Default: 18789.
	WebhookPort int
}

// Handler listens for BlueBubbles webhook events and routes iMessages through
// the shared Dispatcher (router.Router). One Handler per BlueBubbles server.
type Handler struct {
	cfg         Config
	dispatcher  web.Dispatcher
	transcriber voice.Transcriber
	synthesizer voice.Synthesizer
	log         *slog.Logger
	httpClient  *http.Client
	server      *http.Server
}

// New creates a Handler, validates connectivity by pinging BlueBubbles, and
// registers a webhook. Returns an error if the server is unreachable or the
// webhook registration fails.
func New(cfg Config, dispatcher web.Dispatcher, tr voice.Transcriber, sy voice.Synthesizer) (*Handler, error) {
	if cfg.BaseURL == "" {
		return nil, fmt.Errorf("imessage: BLUEBUBBLES_URL is required")
	}
	if cfg.Password == "" {
		return nil, fmt.Errorf("imessage: BLUEBUBBLES_PASSWORD is required")
	}
	if cfg.AllowedHandle == "" {
		return nil, fmt.Errorf("imessage: BLUEBUBBLES_ALLOWED_HANDLE is required — set it to your iMessage phone number or email")
	}
	if cfg.WebhookPort == 0 {
		cfg.WebhookPort = 18789
	}

	h := &Handler{
		cfg:         cfg,
		dispatcher:  dispatcher,
		transcriber: tr,
		synthesizer: sy,
		log:         slog.Default(),
		httpClient:  http.DefaultClient,
	}
	return h, nil
}

// NewForTest creates a Handler for testing, bypassing BlueBubbles connectivity
// checks. The HTTP client can be replaced via SetHTTPClient for test server intercept.
func NewForTest(cfg Config, dispatcher web.Dispatcher) *Handler {
	if cfg.WebhookPort == 0 {
		cfg.WebhookPort = 18789
	}
	return &Handler{
		cfg:         cfg,
		dispatcher:  dispatcher,
		transcriber: &voice.NoopTranscriber{},
		synthesizer: &voice.NoopSynthesizer{},
		log:         slog.Default(),
		httpClient:  http.DefaultClient,
	}
}

// SetTranscriber replaces the handler's transcriber.
func (h *Handler) SetTranscriber(t voice.Transcriber) { h.transcriber = t }

// SetSynthesizer replaces the handler's synthesizer.
func (h *Handler) SetSynthesizer(s voice.Synthesizer) { h.synthesizer = s }

// SetHTTPClient replaces the handler's HTTP client. Used in tests to intercept
// BlueBubbles API calls without hitting a real server.
func (h *Handler) SetHTTPClient(c *http.Client) { h.httpClient = c }

// HandleWebhook exposes onWebhook for integration testing without starting an
// HTTP server.
func (h *Handler) HandleWebhook(ctx context.Context, body []byte) {
	h.onWebhook(ctx, body)
}

// Start pings BlueBubbles, registers the webhook, and begins listening for
// inbound events. Blocks until ctx is cancelled, then deregisters the webhook
// and shuts down the HTTP server gracefully.
func (h *Handler) Start(ctx context.Context) error {
	h.log.Info("imessage channel starting",
		"base_url", h.cfg.BaseURL,
		"allowed_handle", h.cfg.AllowedHandle,
		"webhook_port", h.cfg.WebhookPort)

	if err := h.ping(ctx); err != nil {
		return fmt.Errorf("imessage: BlueBubbles ping failed: %w", err)
	}

	webhookURL := fmt.Sprintf("http://localhost:%d/webhook", h.cfg.WebhookPort)
	if err := h.registerWebhook(ctx, webhookURL); err != nil {
		return fmt.Errorf("imessage: webhook registration failed: %w", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/webhook", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(r.Body)
		r.Body.Close()
		if err != nil {
			h.log.Warn("imessage: failed to read webhook body", "error", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
		go h.onWebhook(context.Background(), body)
	})

	h.server = &http.Server{
		Addr:    fmt.Sprintf(":%d", h.cfg.WebhookPort),
		Handler: mux,
	}

	listener, err := net.Listen("tcp", h.server.Addr)
	if err != nil {
		return fmt.Errorf("imessage: listen on port %d: %w", h.cfg.WebhookPort, err)
	}

	go func() {
		if err := h.server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			h.log.Error("imessage: webhook server error", "error", err)
		}
	}()

	h.log.Info("imessage channel ready", "webhook_url", webhookURL)

	<-ctx.Done()

	h.log.Info("imessage channel stopping")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Best-effort deregister — log but don't block shutdown.
	if deregErr := h.deregisterWebhook(shutdownCtx, webhookURL); deregErr != nil {
		h.log.Warn("imessage: webhook deregistration failed", "error", deregErr)
	}

	if err := h.server.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("imessage: shutdown: %w", err)
	}
	return nil
}

// Stop is a no-op — shutdown happens when the context passed to Start() is
// cancelled. Provided for symmetry with other channel handlers.
func (h *Handler) Stop() {
	h.log.Info("imessage channel stop requested")
}

// onWebhook processes a single BlueBubbles webhook event body.
func (h *Handler) onWebhook(ctx context.Context, body []byte) {
	var event webhookEvent
	if err := json.Unmarshal(body, &event); err != nil {
		h.log.Warn("imessage: failed to parse webhook event", "error", err)
		return
	}

	if event.Type != "new-message" {
		return
	}
	if event.Data.IsFromMe {
		return
	}

	// Whitelist check.
	if event.Data.Handle.Address != h.cfg.AllowedHandle {
		return
	}

	chatGUID := ""
	if len(event.Data.Chats) > 0 {
		chatGUID = event.Data.Chats[0].GUID
	}
	if chatGUID == "" {
		h.log.Warn("imessage: message has no chat GUID — dropping", "event_type", event.Type)
		return
	}

	sid := sessionKey(event.Data.Handle.Address, chatGUID)

	h.log.InfoContext(ctx, "channel_received",
		"session_id", sid,
		"user_id", event.Data.Handle.Address,
		"channel", "imessage",
	)

	// Handle audio attachments via transcription.
	for _, att := range event.Data.Attachments {
		if strings.HasPrefix(att.MimeType, "audio/") {
			h.handleAudio(ctx, event.Data.Handle.Address, chatGUID, sid, att)
			return
		}
	}

	// Process non-audio attachments.
	var parts []types.ContentPart
	for _, att := range event.Data.Attachments {
		fileParts, err := h.processAttachment(ctx, sid, att)
		if err != nil {
			h.log.WarnContext(ctx, "imessage: failed to process attachment",
				"session_id", sid, "guid", att.GUID, "error", err)
			continue
		}
		parts = append(parts, fileParts...)
	}

	inbound := h.buildInbound(event.Data.Handle.Address, chatGUID, sid, event.Data.Text, parts)
	h.routeAndRespond(ctx, chatGUID, sid, inbound, false)
}

// handleAudio downloads an audio attachment, transcribes it, and routes the result.
func (h *Handler) handleAudio(ctx context.Context, handle, chatGUID, sid string, att attachment) {
	h.log.InfoContext(ctx, "imessage: audio message received", "session_id", sid, "mime", att.MimeType)

	data, err := h.downloadAttachment(ctx, att.GUID)
	if err != nil {
		h.log.WarnContext(ctx, "imessage: failed to download audio",
			"session_id", sid, "guid", att.GUID, "error", err)
		h.sendText(ctx, chatGUID, "Sorry, I couldn't download that voice message.")
		return
	}

	text, transcribeErr := h.transcriber.Transcribe(ctx, data, att.MimeType)
	if errors.Is(transcribeErr, voice.ErrNotSupported) {
		h.log.InfoContext(ctx, "imessage: voice transcription disabled", "session_id", sid)
		h.sendText(ctx, chatGUID, "Voice messages aren't supported yet — please type your message.")
		return
	}
	if transcribeErr != nil {
		h.log.WarnContext(ctx, "imessage: transcription failed",
			"session_id", sid, "error", transcribeErr)
		h.sendText(ctx, chatGUID, "Sorry, I couldn't transcribe that voice message — please type your message.")
		return
	}

	inbound := types.InboundMessage{
		ChannelID: types.ChannelID("imessage"),
		UserID:    handle,
		SessionID: sid,
		Text:      fmt.Sprintf("[Voice message transcribed]: %s", text),
		Timestamp: time.Now(),
	}
	h.routeAndRespond(ctx, chatGUID, sid, inbound, true)
}

// processAttachment downloads and converts a single BlueBubbles attachment into
// ContentParts. Images are base64-encoded; PDFs have their text extracted.
// Unsupported types are silently ignored.
func (h *Handler) processAttachment(ctx context.Context, sid string, att attachment) ([]types.ContentPart, error) {
	switch {
	case strings.HasPrefix(att.MimeType, "image/"):
		data, err := h.downloadAttachment(ctx, att.GUID)
		if err != nil {
			return nil, fmt.Errorf("download image: %w", err)
		}
		return []types.ContentPart{{
			Type:      "image",
			ImageData: base64.StdEncoding.EncodeToString(data),
			MimeType:  att.MimeType,
			Filename:  att.TransferName,
		}}, nil

	case att.MimeType == "application/pdf":
		data, err := h.downloadAttachment(ctx, att.GUID)
		if err != nil {
			return nil, fmt.Errorf("download PDF: %w", err)
		}
		text, err := attachments.ExtractPDFText(data)
		if err != nil {
			return nil, fmt.Errorf("extract PDF: %w", err)
		}
		return []types.ContentPart{{
			Type:     "text",
			Text:     text,
			Filename: att.TransferName,
		}}, nil

	default:
		h.log.DebugContext(ctx, "imessage: ignoring unsupported attachment type",
			"session_id", sid, "mime", att.MimeType)
		return nil, nil
	}
}

// downloadAttachment fetches attachment bytes from the BlueBubbles server.
func (h *Handler) downloadAttachment(ctx context.Context, guid string) ([]byte, error) {
	url := fmt.Sprintf("%s/api/v1/attachment/%s/download?password=%s",
		strings.TrimRight(h.cfg.BaseURL, "/"), guid, h.cfg.Password)
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
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("server returned %d for attachment %q", resp.StatusCode, guid)
	}
	return data, nil
}

// buildInbound constructs a types.InboundMessage from an iMessage event.
func (h *Handler) buildInbound(handle, chatGUID, sid, text string, attParts []types.ContentPart) types.InboundMessage {
	var parts []types.ContentPart
	if len(attParts) > 0 {
		parts = make([]types.ContentPart, 0, 1+len(attParts))
		if text != "" {
			parts = append(parts, types.ContentPart{Type: "text", Text: text})
		}
		parts = append(parts, attParts...)
	}
	return types.InboundMessage{
		ChannelID: types.ChannelID("imessage"),
		UserID:    handle,
		SessionID: sid,
		Text:      text,
		Timestamp: time.Now(),
		Parts:     parts,
	}
}

// routeAndRespond dispatches inbound via streaming when the dispatcher supports
// it, falling back to blocking Route() on error or when streaming is unavailable.
// wasVoice indicates the inbound was an audio file; when true the response may
// be synthesized back to audio.
//
// BlueBubbles does not support live message editing, so streaming mode buffers
// all chunks and sends the complete text as a single message on completion.
func (h *Handler) routeAndRespond(
	ctx context.Context,
	chatGUID, sid string,
	inbound types.InboundMessage,
	wasVoice bool,
) {
	start := time.Now()
	if sd, ok := h.dispatcher.(web.StreamDispatcher); ok {
		chunks, err := sd.RouteStream(ctx, inbound)
		if err == nil {
			h.respondBuffered(ctx, chatGUID, sid, start, chunks, wasVoice)
			return
		}
		h.log.WarnContext(ctx, "imessage: stream route failed, falling back to blocking",
			"session_id", sid, "error", err)
	}

	out, err := h.dispatcher.Route(ctx, inbound)
	if err != nil {
		h.log.ErrorContext(ctx, "imessage: route error", "session_id", sid, "error", err)
		h.sendText(ctx, chatGUID, "Sorry, something went wrong. Please try again.")
		return
	}
	h.log.InfoContext(ctx, "channel_response",
		"session_id", sid, "latency_ms", time.Since(start).Milliseconds(), "channel", "imessage")
	h.sendResponse(ctx, chatGUID, sid, out.Text, wasVoice)
}

// respondBuffered collects all stream chunks then sends the complete text (or
// audio) in a single message. BlueBubbles has no edit-message API.
func (h *Handler) respondBuffered(
	ctx context.Context,
	chatGUID, sid string,
	start time.Time,
	chunks <-chan string,
	wasVoice bool,
) {
	var sb strings.Builder
	for {
		select {
		case chunk, ok := <-chunks:
			if !ok {
				goto done
			}
			sb.WriteString(chunk)
		case <-ctx.Done():
			for range chunks {} //nolint:revive — drain so router goroutine can exit
			return
		}
	}
done:
	fullText := sb.String()
	if fullText == "" {
		fullText = "(no response)"
	}
	h.log.InfoContext(ctx, "channel_response",
		"session_id", sid, "latency_ms", time.Since(start).Milliseconds(), "channel", "imessage-stream")
	h.sendResponse(ctx, chatGUID, sid, fullText, wasVoice)
}

// sendResponse sends the agent reply as audio when wasVoice is true and
// synthesis succeeds; otherwise sends plain text.
func (h *Handler) sendResponse(ctx context.Context, chatGUID, sid, text string, wasVoice bool) {
	if wasVoice {
		data, mime, err := h.synthesizer.Synthesize(ctx, text)
		if err != nil {
			h.log.WarnContext(ctx, "imessage: TTS failed, sending text",
				"session_id", sid, "error", err)
		} else if len(data) > 0 {
			if voiceErr := h.sendVoiceMemo(ctx, chatGUID, data, mime); voiceErr != nil {
				h.log.WarnContext(ctx, "imessage: voice memo send failed, sending text",
					"session_id", sid, "error", voiceErr)
			} else {
				return
			}
		}
	}
	if err := h.sendText(ctx, chatGUID, text); err != nil {
		h.log.ErrorContext(ctx, "imessage: send text error", "session_id", sid, "error", err)
	}
}

// sendText posts a plain text message to the given iMessage chat.
func (h *Handler) sendText(ctx context.Context, chatGUID, text string) error {
	body := map[string]string{
		"chatGuid": chatGUID,
		"tempGuid": uuid.New().String(),
		"message":  text,
	}
	return h.bbPost(ctx, "/api/v1/message/text", body)
}

// sendVoiceMemo uploads audio bytes as a voice note attachment to BlueBubbles.
func (h *Handler) sendVoiceMemo(ctx context.Context, chatGUID string, data []byte, mimeType string) error {
	filename := "response.ogg"
	if strings.Contains(mimeType, "mp3") || strings.Contains(mimeType, "mpeg") {
		filename = "response.mp3"
	}

	url := fmt.Sprintf("%s/api/v1/attachment/upload?password=%s",
		strings.TrimRight(h.cfg.BaseURL, "/"), h.cfg.Password)

	payload := map[string]interface{}{
		"chatGuid":    chatGUID,
		"attachment":  base64.StdEncoding.EncodeToString(data),
		"name":        filename,
		"isVoice":     true,
	}
	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal voice memo payload: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := h.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("send voice memo: %w", err)
	}
	io.Copy(io.Discard, resp.Body) //nolint:errcheck
	resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("BlueBubbles returned %d for voice upload", resp.StatusCode)
	}
	return nil
}

// NotifyReminder implements reminder.Notifier. It resolves the target chat
// from r.SessionID (format "imessage:{handle}:{chatGUID}") and sends the message.
// Reminders without a resolvable iMessage chat are silently ignored.
func (h *Handler) NotifyReminder(ctx context.Context, r *sessions.Reminder) error {
	chatGUID, err := chatGUIDFromReminder(r)
	if err != nil {
		return nil // not for this channel — silently ignore
	}
	if err := h.sendText(ctx, chatGUID, r.Message); err != nil {
		return fmt.Errorf("imessage: send reminder: %w", err)
	}
	h.log.InfoContext(ctx, "imessage: reminder delivered",
		"reminder_id", r.ID, "chat_guid", chatGUID)
	return nil
}

// chatGUIDFromReminder extracts the BlueBubbles chat GUID from a Reminder.
// Expects r.SessionID in the format "imessage:{handle}:{chatGUID}".
func chatGUIDFromReminder(r *sessions.Reminder) (string, error) {
	if strings.HasPrefix(r.SessionID, "imessage:") {
		parts := strings.SplitN(r.SessionID, ":", 3)
		if len(parts) == 3 && parts[2] != "" {
			return parts[2], nil
		}
	}
	return "", fmt.Errorf("cannot resolve imessage chat GUID from reminder %q", r.ID)
}

// sessionKey returns a stable session key for a handle × chat-GUID combination.
//
// Format: imessage:{handle}:{chatGUID}
func sessionKey(handle, chatGUID string) string {
	return fmt.Sprintf("imessage:%s:%s", handle, chatGUID)
}

// ── BlueBubbles helpers ───────────────────────────────────────────────────────

// ping calls GET /api/v1/ping to verify that the BlueBubbles server is reachable.
func (h *Handler) ping(ctx context.Context) error {
	url := fmt.Sprintf("%s/api/v1/ping?password=%s",
		strings.TrimRight(h.cfg.BaseURL, "/"), h.cfg.Password)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	resp, err := h.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("ping: %w", err)
	}
	io.Copy(io.Discard, resp.Body) //nolint:errcheck
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected ping status: %d", resp.StatusCode)
	}
	return nil
}

// registerWebhook calls POST /api/v1/webhook to register the local HTTP server
// as a listener for "new-message" events.
func (h *Handler) registerWebhook(ctx context.Context, webhookURL string) error {
	return h.bbPost(ctx, "/api/v1/webhook", map[string]interface{}{
		"url":    webhookURL,
		"events": []string{"new-message"},
	})
}

// deregisterWebhook removes the webhook registration from BlueBubbles.
func (h *Handler) deregisterWebhook(ctx context.Context, webhookURL string) error {
	url := fmt.Sprintf("%s/api/v1/webhook?password=%s",
		strings.TrimRight(h.cfg.BaseURL, "/"), h.cfg.Password)
	body := map[string]string{"url": webhookURL}
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal body: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := h.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("deregister: %w", err)
	}
	io.Copy(io.Discard, resp.Body) //nolint:errcheck
	resp.Body.Close()
	return nil
}

// bbPost marshals body as JSON and POSTs it to the BlueBubbles API endpoint,
// appending the password as a query parameter.
func (h *Handler) bbPost(ctx context.Context, path string, body interface{}) error {
	url := fmt.Sprintf("%s%s?password=%s",
		strings.TrimRight(h.cfg.BaseURL, "/"), path, h.cfg.Password)
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal body: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := h.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("post %s: %w", path, err)
	}
	io.Copy(io.Discard, resp.Body) //nolint:errcheck
	resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("BlueBubbles returned %d for %s", resp.StatusCode, path)
	}
	return nil
}

// ── webhook event types ───────────────────────────────────────────────────────

type webhookEvent struct {
	Type string       `json:"type"`
	Data messageData  `json:"data"`
}

type messageData struct {
	GUID        string       `json:"guid"`
	Text        string       `json:"text"`
	IsFromMe    bool         `json:"isFromMe"`
	Handle      handleData   `json:"handle"`
	Chats       []chatData   `json:"chats"`
	Attachments []attachment `json:"attachments"`
}

type handleData struct {
	Address string `json:"address"`
}

type chatData struct {
	GUID string `json:"guid"`
}

type attachment struct {
	GUID         string `json:"guid"`
	MimeType     string `json:"mimeType"`
	TransferName string `json:"transferName"`
}
