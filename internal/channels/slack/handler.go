// Package slack exposes the Router Agent over Slack using Socket Mode.
// It follows the same Dispatcher interface as the web, Discord, Telegram, and
// WhatsApp channels so the router is completely unaware of the transport.
//
// Only direct messages (message.im events) from SLACK_ALLOWED_USER_ID are
// processed. All other events and senders are silently ignored.
package slack

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	slacklib "github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"

	"github.com/marcoantonios1/Agent-OS/internal/attachments"
	"github.com/marcoantonios1/Agent-OS/internal/channels/web"
	"github.com/marcoantonios1/Agent-OS/internal/sessions"
	"github.com/marcoantonios1/Agent-OS/internal/types"
	"github.com/marcoantonios1/Agent-OS/internal/voice"
)

// maxMessageLen is the Slack per-message character limit we enforce.
// Slack's actual limit is higher but 4000 chars keeps messages readable.
const maxMessageLen = 4000

// editInterval is how often the streaming placeholder is updated.
// 500 ms gives ~2 edits/sec — well within Slack's rate limits.
const editInterval = 500 * time.Millisecond

// SlackAPI is the subset of the Slack client used by the Handler.
// *slacklib.Client satisfies this interface directly; tests may supply a mock.
type SlackAPI interface {
	PostMessage(channelID string, options ...slacklib.MsgOption) (string, string, error)
	UpdateMessage(channelID, timestamp string, options ...slacklib.MsgOption) (string, string, string, error)
	UploadFile(params slacklib.UploadFileParameters) (*slacklib.FileSummary, error)
}

// AttachedFile holds Slack file metadata parsed from a message event payload.
// Exported so tests can construct IncomingMessage values directly.
type AttachedFile struct {
	ID                 string `json:"id"`
	MimeType           string `json:"mimetype"`
	Name               string `json:"name"`
	URLPrivate         string `json:"url_private"`
	URLPrivateDownload string `json:"url_private_download"`
	Size               int64  `json:"size"`
}

// IncomingMessage is the internal representation of a parsed Slack DM event.
// Exported so tests can inject events without a real socket connection.
type IncomingMessage struct {
	User        string
	Text        string
	Channel     string
	ChannelType string
	SubType     string
	BotID       string
	Files       []AttachedFile
}

// rawEventsPayload is used for JSON-decoding the raw socket payload to access
// fields (e.g. Files) not present in slackevents.MessageEvent.
type rawEventsPayload struct {
	Event struct {
		User        string         `json:"user"`
		Text        string         `json:"text"`
		Channel     string         `json:"channel"`
		ChannelType string         `json:"channel_type"`
		SubType     string         `json:"subtype"`
		BotID       string         `json:"bot_id"`
		Files       []AttachedFile `json:"files"`
	} `json:"event"`
}

// Handler listens for Slack Socket Mode events and routes DMs through the
// shared Dispatcher (router.Router). One Handler per bot token.
type Handler struct {
	api            SlackAPI
	socket         *socketmode.Client
	dispatcher     web.Dispatcher
	allowedUID     string
	botToken       string // used to authenticate file download requests
	transcriber    voice.Transcriber
	synthesizer    voice.Synthesizer
	log            *slog.Logger
	httpClient     *http.Client
	videoMaxFrames int   // max frames to extract from a video; default 8
	videoMaxSizeMB int64 // max video size in MB before rejection; default 50
}

// New creates a Handler, validates the bot token via AuthTest, and sets up the
// Socket Mode client. Returns an error if any required field is missing or the
// Slack API is unreachable.
func New(
	dispatcher web.Dispatcher,
	botToken, appToken, allowedUID string,
	tr voice.Transcriber,
	sy voice.Synthesizer,
) (*Handler, error) {
	if botToken == "" {
		return nil, fmt.Errorf("slack: bot token is required")
	}
	if appToken == "" {
		return nil, fmt.Errorf("slack: app token (xapp-...) is required for Socket Mode")
	}
	if allowedUID == "" {
		return nil, fmt.Errorf("slack: SLACK_ALLOWED_USER_ID is required — set it to your Slack member ID (find it in your Slack profile)")
	}

	client := slacklib.New(botToken, slacklib.OptionAppLevelToken(appToken))
	if _, err := client.AuthTest(); err != nil {
		return nil, fmt.Errorf("slack: auth test failed: %w", err)
	}

	socket := socketmode.New(client)

	return &Handler{
		api:            client,
		socket:         socket,
		dispatcher:     dispatcher,
		allowedUID:     allowedUID,
		botToken:       botToken,
		transcriber:    tr,
		synthesizer:    sy,
		log:            slog.Default(),
		httpClient:     http.DefaultClient,
		videoMaxFrames: 8,
		videoMaxSizeMB: 50,
	}, nil
}

// NewForTest creates a Handler for testing, bypassing Slack token validation.
// api is a mock implementation of SlackAPI.
func NewForTest(api SlackAPI, dispatcher web.Dispatcher, allowedUID string) *Handler {
	return &Handler{
		api:            api,
		dispatcher:     dispatcher,
		allowedUID:     allowedUID,
		botToken:       "xoxb-test",
		transcriber:    &voice.NoopTranscriber{},
		synthesizer:    &voice.NoopSynthesizer{},
		log:            slog.Default(),
		httpClient:     http.DefaultClient,
		videoMaxFrames: 8,
		videoMaxSizeMB: 50,
	}
}

// SetTranscriber replaces the handler's transcriber.
func (h *Handler) SetTranscriber(t voice.Transcriber) { h.transcriber = t }

// SetSynthesizer replaces the handler's synthesizer.
func (h *Handler) SetSynthesizer(s voice.Synthesizer) { h.synthesizer = s }

// SetVideoConfig overrides the video processing limits.
// maxFrames must be >= 1; maxSizeMB must be > 0. Zero values are ignored.
func (h *Handler) SetVideoConfig(maxFrames int, maxSizeMB int64) {
	if maxFrames > 0 {
		h.videoMaxFrames = maxFrames
	}
	if maxSizeMB > 0 {
		h.videoMaxSizeMB = maxSizeMB
	}
}

// SetHTTPClient replaces the handler's HTTP client. Used in tests to intercept
// file download requests without hitting real Slack servers.
func (h *Handler) SetHTTPClient(c *http.Client) { h.httpClient = c }

// HandleMessage exposes handleMessage for integration testing without going
// through the Socket Mode loop in Start().
func (h *Handler) HandleMessage(ctx context.Context, msg *IncomingMessage) {
	h.handleMessage(ctx, msg)
}

// Start connects to Slack via Socket Mode and blocks until ctx is cancelled.
func (h *Handler) Start(ctx context.Context) error {
	h.log.Info("slack channel starting", "allowed_uid", h.allowedUID)
	go h.processEvents(ctx)
	if err := h.socket.RunContext(ctx); err != nil {
		if errors.Is(err, context.Canceled) {
			return nil
		}
		return fmt.Errorf("slack: socket: %w", err)
	}
	return nil
}

// Stop logs channel shutdown. The actual stop happens when the context passed
// to Start() is cancelled — socketmode.RunContext handles that cleanly.
func (h *Handler) Stop() {
	h.log.Info("slack channel stopping")
}

// processEvents reads from the socket Events channel and dispatches each event.
func (h *Handler) processEvents(ctx context.Context) {
	for {
		select {
		case evt, ok := <-h.socket.Events:
			if !ok {
				return
			}
			h.handleSocketEvent(ctx, evt)
		case <-ctx.Done():
			return
		}
	}
}

// handleSocketEvent dispatches a single socket event to the appropriate handler.
func (h *Handler) handleSocketEvent(ctx context.Context, evt socketmode.Event) {
	switch evt.Type {
	case socketmode.EventTypeConnecting:
		h.log.Info("slack: socket connecting")
	case socketmode.EventTypeConnected:
		h.log.Info("slack: socket connected")
	case socketmode.EventTypeConnectionError:
		h.log.Warn("slack: socket connection error")
	case socketmode.EventTypeDisconnect:
		h.log.Info("slack: socket disconnected")
	case socketmode.EventTypeEventsAPI:
		eventsAPIEvent, ok := evt.Data.(slackevents.EventsAPIEvent)
		if !ok {
			return
		}
		if err := h.socket.Ack(*evt.Request); err != nil {
			h.log.Warn("slack: failed to ack event", "error", err)
		}
		if eventsAPIEvent.InnerEvent.Type != "message" {
			h.log.Debug("slack: ignoring non-message event", "type", eventsAPIEvent.InnerEvent.Type)
			return
		}
		// Parse from raw payload for complete field access (files are not in
		// slackevents.MessageEvent).
		if evt.Request == nil {
			return
		}
		var payload rawEventsPayload
		if err := json.Unmarshal(evt.Request.Payload, &payload); err != nil {
			h.log.Warn("slack: failed to parse event payload", "error", err)
			return
		}
		msg := &IncomingMessage{
			User:        payload.Event.User,
			Text:        payload.Event.Text,
			Channel:     payload.Event.Channel,
			ChannelType: payload.Event.ChannelType,
			SubType:     payload.Event.SubType,
			BotID:       payload.Event.BotID,
			Files:       payload.Event.Files,
		}
		go h.handleMessage(ctx, msg)
	default:
		// Silently ignore all other event types.
	}
}

// handleMessage processes one incoming Slack DM message.
func (h *Handler) handleMessage(ctx context.Context, msg *IncomingMessage) {
	// Only handle direct messages.
	if msg.ChannelType != "im" {
		h.log.Debug("slack: ignoring non-DM message", "channel_type", msg.ChannelType)
		return
	}
	// Ignore bot messages (our own replies and other bots).
	if msg.BotID != "" || msg.SubType == "bot_message" {
		return
	}
	// Only handle regular messages and file shares.
	if msg.SubType != "" && msg.SubType != "file_share" {
		h.log.Debug("slack: ignoring message subtype", "subtype", msg.SubType)
		return
	}
	// Silently drop messages from non-whitelisted users.
	if msg.User != h.allowedUID {
		return
	}

	sid := sessionKey(msg.User, msg.Channel)

	h.log.InfoContext(ctx, "channel_received",
		"session_id", sid,
		"user_id", msg.User,
		"channel_id", msg.Channel,
		"channel", "slack",
	)

	// Handle video attachments before the general file loop.
	for _, f := range msg.Files {
		if strings.HasPrefix(f.MimeType, "video/") {
			h.handleVideo(ctx, msg, sid, f)
			return
		}
	}

	// Handle audio attachments via transcription.
	for _, f := range msg.Files {
		if strings.HasPrefix(f.MimeType, "audio/") {
			h.handleAudio(ctx, msg, sid, f)
			return
		}
	}

	// Process non-audio file attachments.
	var parts []types.ContentPart
	for _, f := range msg.Files {
		fileParts, err := h.processFile(ctx, sid, f)
		if err != nil {
			h.log.WarnContext(ctx, "slack: failed to process attachment",
				"session_id", sid, "file_id", f.ID, "error", err)
			continue
		}
		parts = append(parts, fileParts...)
	}

	inbound := h.buildInbound(msg, sid, parts)
	h.routeAndRespond(ctx, msg.Channel, sid, inbound, false)
}

// handleAudio downloads a Slack audio file, transcribes it, and routes the result.
func (h *Handler) handleAudio(ctx context.Context, msg *IncomingMessage, sid string, f AttachedFile) {
	h.log.InfoContext(ctx, "slack: audio message received",
		"session_id", sid, "mime", f.MimeType)

	data, err := h.downloadFile(ctx, f)
	if err != nil {
		h.log.WarnContext(ctx, "slack: failed to download audio",
			"session_id", sid, "file_id", f.ID, "error", err)
		h.sendText(msg.Channel, "Sorry, I couldn't download that voice message.")
		return
	}

	text, transcribeErr := h.transcriber.Transcribe(ctx, data, f.MimeType)
	if errors.Is(transcribeErr, voice.ErrNotSupported) {
		h.log.InfoContext(ctx, "slack: voice transcription disabled", "session_id", sid)
		h.sendText(msg.Channel, "Voice messages aren't supported yet — please type your message.")
		return
	}
	if transcribeErr != nil {
		h.log.WarnContext(ctx, "slack: transcription failed",
			"session_id", sid, "error", transcribeErr)
		h.sendText(msg.Channel, "Sorry, I couldn't transcribe that voice message — please type your message.")
		return
	}

	inbound := types.InboundMessage{
		ChannelID: types.ChannelID("slack"),
		UserID:    msg.User,
		SessionID: sid,
		Text:      fmt.Sprintf("[Voice message transcribed]: %s", text),
		Timestamp: time.Now(),
	}
	h.routeAndRespond(ctx, msg.Channel, sid, inbound, true)
}

// handleVideo downloads a Slack video file, extracts frames via ffmpeg, and
// routes the result alongside any caption text from the message.
func (h *Handler) handleVideo(ctx context.Context, msg *IncomingMessage, sid string, f AttachedFile) {
	h.log.InfoContext(ctx, "slack: video message received", "session_id", sid, "mime", f.MimeType)

	if h.videoMaxSizeMB > 0 && f.Size > 0 && f.Size > h.videoMaxSizeMB*1024*1024 {
		h.sendText(msg.Channel, fmt.Sprintf("That video is too large to analyse — max %dMB.", h.videoMaxSizeMB))
		return
	}

	data, err := h.downloadFile(ctx, f)
	if err != nil {
		h.log.WarnContext(ctx, "slack: failed to download video",
			"session_id", sid, "file_id", f.ID, "error", err)
		h.sendText(msg.Channel, "Sorry, I couldn't download that video.")
		return
	}

	if h.videoMaxSizeMB > 0 && int64(len(data)) > h.videoMaxSizeMB*1024*1024 {
		h.sendText(msg.Channel, fmt.Sprintf("That video is too large to analyse — max %dMB.", h.videoMaxSizeMB))
		return
	}

	mimeType := f.MimeType
	if mimeType == "" {
		mimeType = "video/mp4"
	}
	frames, extractErr := attachments.ExtractFrames(data, mimeType, h.videoMaxFrames)
	if errors.Is(extractErr, attachments.ErrFfmpegUnavailable) {
		h.sendText(msg.Channel, "Video analysis isn't available on this server.")
		return
	}
	if extractErr != nil {
		h.log.WarnContext(ctx, "slack: video frame extraction failed",
			"session_id", sid, "error", extractErr)
		h.sendText(msg.Channel, "Sorry, I couldn't process that video.")
		return
	}

	videoParts := attachments.VideoToContentParts(frames, f.Name)
	var parts []types.ContentPart
	if msg.Text != "" {
		parts = append(parts, types.ContentPart{Type: "text", Text: msg.Text})
	}
	parts = append(parts, videoParts...)

	inbound := types.InboundMessage{
		ChannelID: types.ChannelID("slack"),
		UserID:    msg.User,
		SessionID: sid,
		Text:      msg.Text,
		Timestamp: time.Now(),
		Parts:     parts,
	}
	h.routeAndRespond(ctx, msg.Channel, sid, inbound, false)
}

// processFile downloads and converts a single Slack file into ContentParts.
// Images are base64-encoded; PDFs have their text extracted.
// Unsupported types are silently ignored (nil, nil returned).
func (h *Handler) processFile(ctx context.Context, sid string, f AttachedFile) ([]types.ContentPart, error) {
	switch {
	case strings.HasPrefix(f.MimeType, "image/"):
		data, err := h.downloadFile(ctx, f)
		if err != nil {
			return nil, fmt.Errorf("download image: %w", err)
		}
		return []types.ContentPart{{
			Type:      "image",
			ImageData: base64.StdEncoding.EncodeToString(data),
			MimeType:  f.MimeType,
			Filename:  f.Name,
		}}, nil

	case f.MimeType == "application/pdf":
		data, err := h.downloadFile(ctx, f)
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
			Filename: f.Name,
		}}, nil

	default:
		// Unsupported attachment type — silently ignore.
		h.log.DebugContext(ctx, "slack: ignoring unsupported attachment type",
			"session_id", sid, "mime", f.MimeType)
		return nil, nil
	}
}

// downloadFile retrieves a Slack file by URL using the bot token for auth.
func (h *Handler) downloadFile(ctx context.Context, f AttachedFile) ([]byte, error) {
	url := f.URLPrivateDownload
	if url == "" {
		url = f.URLPrivate
	}
	if url == "" {
		return nil, fmt.Errorf("no download URL for file %q", f.ID)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+h.botToken)
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

// buildInbound constructs a types.InboundMessage from a Slack message.
func (h *Handler) buildInbound(msg *IncomingMessage, sid string, attParts []types.ContentPart) types.InboundMessage {
	var parts []types.ContentPart
	if len(attParts) > 0 {
		parts = make([]types.ContentPart, 0, 1+len(attParts))
		if msg.Text != "" {
			parts = append(parts, types.ContentPart{Type: "text", Text: msg.Text})
		}
		parts = append(parts, attParts...)
	}
	return types.InboundMessage{
		ChannelID: types.ChannelID("slack"),
		UserID:    msg.User,
		SessionID: sid,
		Text:      msg.Text,
		Timestamp: time.Now(),
		Parts:     parts,
	}
}

// routeAndRespond dispatches inbound via streaming when the dispatcher supports
// it, falling back to blocking Route() on error or when streaming is unavailable.
// wasVoice indicates the inbound was an audio file; when true the response may
// be synthesized back to audio.
func (h *Handler) routeAndRespond(
	ctx context.Context,
	channelID, sid string,
	inbound types.InboundMessage,
	wasVoice bool,
) {
	start := time.Now()
	if sd, ok := h.dispatcher.(web.StreamDispatcher); ok {
		chunks, err := sd.RouteStream(ctx, inbound)
		if err == nil {
			h.respondStreaming(ctx, channelID, sid, start, chunks, wasVoice)
			return
		}
		h.log.WarnContext(ctx, "slack: stream route failed, falling back to blocking",
			"session_id", sid, "error", err)
	}

	out, err := h.dispatcher.Route(ctx, inbound)
	if err != nil {
		h.log.ErrorContext(ctx, "slack: route error", "session_id", sid, "error", err)
		h.sendText(channelID, "Sorry, something went wrong. Please try again.")
		return
	}
	h.log.InfoContext(ctx, "channel_response",
		"session_id", sid, "latency_ms", time.Since(start).Milliseconds(), "channel", "slack")
	h.sendResponse(ctx, channelID, sid, out.Text, wasVoice)
}

// respondStreaming sends a "…" placeholder then edits it as tokens arrive,
// throttled to editInterval. The final edit delivers the complete text;
// messages over maxMessageLen chars are sent as additional messages.
// When wasVoice is true and TTS succeeds the placeholder is replaced and
// audio is uploaded instead.
func (h *Handler) respondStreaming(
	ctx context.Context,
	channelID, sid string,
	start time.Time,
	chunks <-chan string,
	wasVoice bool,
) {
	_, msgTS, err := h.api.PostMessage(channelID, slacklib.MsgOptionText("…", false))
	if err != nil {
		h.log.WarnContext(ctx, "slack: failed to post placeholder",
			"session_id", sid, "error", err)
		for range chunks {} //nolint:revive — drain so router goroutine can exit
		return
	}

	var sb strings.Builder
	ticker := time.NewTicker(editInterval)
	defer ticker.Stop()
	lastSent := ""

outerLoop:
	for {
		select {
		case chunk, ok := <-chunks:
			if !ok {
				break outerLoop
			}
			sb.WriteString(chunk)
		case <-ticker.C:
			current := truncateForEdit(sb.String())
			if current != "" && current != lastSent {
				h.editOrLog(ctx, channelID, msgTS, sid, current)
				lastSent = current
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

	if wasVoice {
		data, mime, synthErr := h.synthesizer.Synthesize(ctx, fullText)
		if synthErr != nil {
			h.log.WarnContext(ctx, "slack: TTS failed in streaming, sending text",
				"session_id", sid, "error", synthErr)
		} else if len(data) > 0 {
			h.editOrLog(ctx, channelID, msgTS, sid, "🎤")
			h.sendAudio(ctx, channelID, sid, data, mime)
			h.log.InfoContext(ctx, "channel_response",
				"session_id", sid, "latency_ms", time.Since(start).Milliseconds(), "channel", "slack-stream-tts")
			return
		}
	}

	parts := splitMessage(fullText, maxMessageLen)
	h.editOrLog(ctx, channelID, msgTS, sid, parts[0])
	for _, extra := range parts[1:] {
		h.sendText(channelID, extra)
	}

	h.log.InfoContext(ctx, "channel_response",
		"session_id", sid, "latency_ms", time.Since(start).Milliseconds(), "channel", "slack-stream")
}

// sendResponse sends the agent reply as audio when wasVoice is true and
// synthesis succeeds; otherwise sends plain text, splitting if needed.
func (h *Handler) sendResponse(ctx context.Context, channelID, sid, text string, wasVoice bool) {
	if wasVoice {
		data, mime, err := h.synthesizer.Synthesize(ctx, text)
		if err != nil {
			h.log.WarnContext(ctx, "slack: TTS failed, sending text",
				"session_id", sid, "error", err)
		} else if len(data) > 0 {
			h.sendAudio(ctx, channelID, sid, data, mime)
			return
		}
	}
	for _, part := range splitMessage(text, maxMessageLen) {
		h.sendText(channelID, part)
	}
}

// sendAudio uploads audio bytes to the Slack channel as a file.
func (h *Handler) sendAudio(ctx context.Context, channelID, sid string, data []byte, mimeType string) {
	filename := "response.ogg"
	if strings.Contains(mimeType, "mp3") || strings.Contains(mimeType, "mpeg") {
		filename = "response.mp3"
	}
	_, err := h.api.UploadFile(slacklib.UploadFileParameters{
		Channel:  channelID,
		Reader:   bytes.NewReader(data),
		FileSize: len(data),
		Filename: filename,
	})
	if err != nil {
		h.log.WarnContext(ctx, "slack: failed to upload audio", "session_id", sid, "error", err)
	}
	_ = ctx // ctx is threaded through but UploadFile does not accept it
}

// editOrLog edits a Slack message, logging a warning on failure.
func (h *Handler) editOrLog(ctx context.Context, channelID, msgTS, sid, text string) {
	if _, _, _, err := h.api.UpdateMessage(channelID, msgTS, slacklib.MsgOptionText(text, false)); err != nil {
		h.log.WarnContext(ctx, "slack: edit error", "session_id", sid, "error", err)
	}
}

// sendText sends a plain text message to the given channel, logging any error.
func (h *Handler) sendText(channelID, text string) {
	if _, _, err := h.api.PostMessage(channelID, slacklib.MsgOptionText(text, false)); err != nil {
		h.log.Warn("slack: send error", "channel_id", channelID, "error", err)
	}
}

// NotifyReminder implements reminder.Notifier. It resolves the target channel
// from r.SessionID (format "slack:{userID}:{channelID}") and posts the message.
// Reminders without a resolvable Slack channel are silently ignored.
func (h *Handler) NotifyReminder(ctx context.Context, r *sessions.Reminder) error {
	channelID, err := channelIDFromReminder(r)
	if err != nil {
		return nil // reminder is not for this channel — silently ignore
	}
	if _, _, err := h.api.PostMessage(channelID, slacklib.MsgOptionText(r.Message, false)); err != nil {
		return fmt.Errorf("slack: send reminder: %w", err)
	}
	h.log.InfoContext(ctx, "slack: reminder delivered",
		"reminder_id", r.ID, "channel_id", channelID)
	return nil
}

// channelIDFromReminder extracts the Slack channel ID from a Reminder.
// Expects r.SessionID in the format "slack:{userID}:{channelID}".
func channelIDFromReminder(r *sessions.Reminder) (string, error) {
	if strings.HasPrefix(r.SessionID, "slack:") {
		parts := strings.SplitN(r.SessionID, ":", 3)
		if len(parts) == 3 && parts[2] != "" {
			return parts[2], nil
		}
	}
	return "", fmt.Errorf("cannot resolve slack channel ID from reminder %q", r.ID)
}

// sessionKey returns a stable session key for a user × DM-channel combination.
//
// Format: slack:{userID}:{channelID}
func sessionKey(userID, channelID string) string {
	return fmt.Sprintf("slack:%s:%s", userID, channelID)
}

// truncateForEdit truncates text to fit within maxMessageLen, appending "…"
// and breaking at a word boundary when possible.
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
