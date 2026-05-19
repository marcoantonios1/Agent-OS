// Package whatsapp exposes the Router Agent over the WhatsApp gateway using
// the go.mau.fi/whatsmeow library (pure Go, no separate process required).
// It follows the same Dispatcher interface as the Discord and web channels so
// the router is completely channel-agnostic.
//
// On first run the handler prints a QR code to the terminal for device pairing.
// On subsequent runs it restores the session from the SQLite store at storePath.
package whatsapp

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/mdp/qrterminal/v3"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/store/sqlstore"
	watypes "go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	_ "modernc.org/sqlite" // registers the "sqlite" driver; safe to import in multiple packages

	"github.com/marcoantonios1/Agent-OS/internal/attachments"
	"github.com/marcoantonios1/Agent-OS/internal/channels/web"
	agenttypes "github.com/marcoantonios1/Agent-OS/internal/types"
	"github.com/marcoantonios1/Agent-OS/internal/voice"
)

var (
	errUnsupportedMediaType = errors.New("unsupported media type")
	errVideoTooLarge        = errors.New("video exceeds configured size limit")
	errFfmpegMissing        = errors.New("ffmpeg not available for video analysis")
	errVideoProcessFailed   = errors.New("video frame extraction failed")
)

// mediaDownloader abstracts whatsmeow.Client.DownloadAny for testing.
type mediaDownloader interface {
	DownloadAny(ctx context.Context, msg *waE2E.Message) ([]byte, error)
}

// msgSender abstracts the WhatsApp client methods needed to send messages and
// upload media, allowing both to be mocked in tests.
type msgSender interface {
	SendMessage(ctx context.Context, to watypes.JID, message *waE2E.Message, extra ...whatsmeow.SendRequestExtra) (whatsmeow.SendResponse, error)
	Upload(ctx context.Context, plaintext []byte, appInfo whatsmeow.MediaType) (whatsmeow.UploadResponse, error)
}

// Handler listens for WhatsApp messages and routes them through the shared
// Dispatcher. Only messages from allowedJID are processed; all others are
// silently dropped.
type Handler struct {
	client         *whatsmeow.Client
	downloader     mediaDownloader // defaults to client; overridden in tests
	sender         msgSender       // defaults to client; overridden in tests
	dispatcher     web.Dispatcher
	allowedJID     string // only respond to this JID; validated non-empty by New
	transcriber    voice.Transcriber
	synthesizer    voice.Synthesizer
	log            *slog.Logger
	videoMaxFrames int   // max frames to extract from a video; default 8
	videoMaxSizeMB int64 // max video size in MB before rejection; default 50
}

// SetSynthesizer replaces the handler's synthesizer. When set and the inbound
// message was a voice message, the agent response is synthesized back to audio.
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

// New creates a Handler.
//   - dispatcher is the router (satisfies web.Dispatcher and optionally web.StreamDispatcher).
//   - storePath is the path to the SQLite DB that persists the device pairing session.
//   - allowedJID is the only sender whose messages will be processed
//     (e.g. "+96170123456@s.whatsapp.net"). Must be non-empty.
//   - transcriber converts voice messages to text; use &voice.NoopTranscriber{} to disable.
func New(dispatcher web.Dispatcher, storePath, allowedJID string, transcriber voice.Transcriber) (*Handler, error) {
	if allowedJID == "" {
		return nil, fmt.Errorf("whatsapp: WHATSAPP_ALLOWED_JID is required when WhatsApp is enabled — set it to your personal number's JID")
	}

	ctx := context.Background()
	container, err := sqlstore.New(ctx, "sqlite", "file:"+storePath+"?_pragma=foreign_keys(ON)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)", waLog.Noop)
	if err != nil {
		return nil, fmt.Errorf("whatsapp: open session store %q: %w", storePath, err)
	}

	deviceStore, err := container.GetFirstDevice(ctx)
	if err != nil {
		return nil, fmt.Errorf("whatsapp: get device from store: %w", err)
	}

	client := whatsmeow.NewClient(deviceStore, waLog.Noop)
	client.EnableAutoReconnect = true

	h := &Handler{
		client:         client,
		downloader:     client,
		sender:         client,
		dispatcher:     dispatcher,
		allowedJID:     allowedJID,
		transcriber:    transcriber,
		synthesizer:    &voice.NoopSynthesizer{},
		log:            slog.Default(),
		videoMaxFrames: 8,
		videoMaxSizeMB: 50,
	}
	client.AddEventHandler(h.onEvent)
	return h, nil
}

// Start connects to WhatsApp. On first run it prints a QR code to stdout for
// scanning with the WhatsApp mobile app. On subsequent runs it restores the
// pairing session from storePath without any user interaction.
// Blocks until ctx is cancelled.
func (h *Handler) Start(ctx context.Context) error {
	if h.client.Store.ID == nil {
		// First run — device not yet paired; show QR code.
		qrChan, err := h.client.GetQRChannel(ctx)
		if err != nil {
			return fmt.Errorf("whatsapp: get QR channel: %w", err)
		}
		if err := h.client.Connect(); err != nil {
			return fmt.Errorf("whatsapp: connect for pairing: %w", err)
		}
		h.log.Info("whatsapp: scan the QR code below with WhatsApp → Linked Devices → Link a Device")
		for item := range qrChan {
			switch item.Event {
			case whatsmeow.QRChannelEventCode:
				qrterminal.GenerateHalfBlock(item.Code, qrterminal.L, os.Stdout)
				h.log.Info("whatsapp: new QR code shown — expires soon, scan quickly")
			case whatsmeow.QRChannelSuccess.Event:
				h.log.Info("whatsapp: pairing successful — session saved to store")
			case whatsmeow.QRChannelEventError:
				return fmt.Errorf("whatsapp: pairing failed: %w", item.Error)
			default:
				h.log.Warn("whatsapp: pairing ended", "event", item.Event)
			}
		}
	} else {
		// Session already exists — reconnect without user interaction.
		if err := h.client.Connect(); err != nil {
			return fmt.Errorf("whatsapp: connect: %w", err)
		}
		h.log.Info("whatsapp channel started", "allowed_jid", h.allowedJID)
	}

	<-ctx.Done()
	return nil
}

// Stop disconnects from WhatsApp gracefully. Safe to call multiple times.
func (h *Handler) Stop() {
	h.log.Info("whatsapp channel stopping")
	h.client.Disconnect()
}

// onEvent is the raw whatsmeow event handler. Only *events.Message is handled.
func (h *Handler) onEvent(rawEvt any) {
	evt, ok := rawEvt.(*events.Message)
	if !ok {
		return
	}
	h.onMessage(evt)
}

// onMessage processes a single inbound WhatsApp message.
func (h *Handler) onMessage(evt *events.Message) {
	// Skip echoes of our own outgoing messages.
	if evt.Info.IsFromMe {
		return
	}
	// Only handle direct messages; silently skip group chats.
	if evt.Info.Chat.Server == watypes.GroupServer {
		return
	}

	ctx := context.Background()
	chat := evt.Info.Chat

	senderJID := normaliseJID(evt.Info.Sender)
	// WhatsApp now delivers messages with LID (@lid) JIDs instead of phone JIDs.
	// Resolve to the canonical phone JID before checking the allowlist.
	if evt.Info.Sender.Server == watypes.HiddenUserServer {
		if pn, err := h.client.Store.GetAltJID(ctx, evt.Info.Sender.ToNonAD()); err == nil && !pn.IsEmpty() {
			senderJID = pn.String()
		}
	}
	if senderJID != h.allowedJID {
		h.log.Info("whatsapp: ignored message from non-whitelisted sender", "jid", senderJID)
		return
	}

	text := extractText(evt.Message)
	wasVoice := false

	// Handle voice/audio messages via transcription.
	if audio := evt.Message.GetAudioMessage(); audio != nil {
		wasVoice = true
		h.log.InfoContext(ctx, "whatsapp: audio message received", "from", senderJID)
		if h.transcriber == nil {
			h.log.WarnContext(ctx, "whatsapp: no transcriber configured — prompting user to type")
			h.send(ctx, chat, "Voice messages aren't supported yet — please type your message.") //nolint:errcheck
			return
		}
		data, err := h.downloader.DownloadAny(ctx, evt.Message)
		if err != nil {
			h.logger().WarnContext(ctx, "whatsapp: audio download failed", "error", err)
			h.send(ctx, chat, "Sorry, I couldn't download that voice message.") //nolint:errcheck
			return
		}
		mime := audio.GetMimetype()
		if mime == "" {
			mime = "audio/ogg"
		}
		transcribed, err := h.transcriber.Transcribe(ctx, data, mime)
		if errors.Is(err, voice.ErrNotSupported) {
			h.log.InfoContext(ctx, "whatsapp: voice transcription disabled (VOICE_TRANSCRIPTION not set to 'enabled')")
			h.send(ctx, chat, "Voice messages aren't supported yet — please type your message.") //nolint:errcheck
			return
		}
		if err != nil {
			h.logger().WarnContext(ctx, "whatsapp: transcription failed", "error", err)
			h.send(ctx, chat, "Sorry, I couldn't transcribe that voice message — please type your message.") //nolint:errcheck
			return
		}
		text = fmt.Sprintf("[Voice message transcribed]: %s", transcribed)
	}

	attParts, err := h.processMedia(ctx, evt.Message)
	switch {
	case errors.Is(err, errUnsupportedMediaType):
		h.send(ctx, chat, "I can read images and PDFs — other file types aren't supported yet") //nolint:errcheck
		return
	case errors.Is(err, errVideoTooLarge):
		h.send(ctx, chat, fmt.Sprintf("That video is too large to analyse — max %dMB.", h.videoMaxSizeMB)) //nolint:errcheck
		return
	case errors.Is(err, errFfmpegMissing):
		h.send(ctx, chat, "Video analysis isn't available on this server.") //nolint:errcheck
		return
	case errors.Is(err, errVideoProcessFailed):
		h.send(ctx, chat, "Sorry, I couldn't process that video.") //nolint:errcheck
		return
	}

	if text == "" && len(attParts) == 0 {
		return // sticker, reaction, etc. — silently ignore
	}

	sid := sessionKey(senderJID)

	h.log.InfoContext(ctx, "channel_received",
		"session_id", sid,
		"user_id", senderJID,
		"text_length", len(text),
		"channel", "whatsapp",
	)

	start := time.Now()
	inbound := agenttypes.InboundMessage{
		ChannelID: agenttypes.ChannelID("whatsapp"),
		UserID:    senderJID,
		SessionID: sid,
		Text:      text,
		Timestamp: start,
		Parts:     buildMsgParts(text, attParts),
	}

	h.routeAndRespond(ctx, chat, inbound, sid, start, wasVoice)
}

// routeAndRespond dispatches inbound via RouteStream when the dispatcher
// supports it, falling back to blocking Route() on error or when unavailable.
// wasVoice indicates the inbound message was audio; when true the response is
// synthesized back to audio if a Synthesizer is configured.
func (h *Handler) routeAndRespond(
	ctx context.Context,
	chat watypes.JID,
	inbound agenttypes.InboundMessage,
	sid string,
	start time.Time,
	wasVoice bool,
) {
	if sd, ok := h.dispatcher.(web.StreamDispatcher); ok {
		chunks, err := sd.RouteStream(ctx, inbound)
		if err == nil {
			h.respondStreaming(ctx, chat, sid, start, chunks, wasVoice)
			return
		}
		h.log.WarnContext(ctx, "whatsapp: stream route failed, falling back to blocking",
			"session_id", sid, "error", err)
	}

	out, err := h.dispatcher.Route(ctx, inbound)
	if err != nil {
		h.log.ErrorContext(ctx, "whatsapp: route error", "session_id", sid, "error", err)
		h.send(ctx, chat, "Sorry, something went wrong. Please try again.") //nolint:errcheck
		return
	}

	h.log.InfoContext(ctx, "channel_response",
		"session_id", sid,
		"latency_ms", time.Since(start).Milliseconds(),
		"channel", "whatsapp",
	)
	h.sendResponse(ctx, chat, sid, out.Text, wasVoice)
}

// respondStreaming collects all chunks from the channel and sends a single
// WhatsApp message once the stream is complete. WhatsApp does not support
// live message editing the way Discord does, so we buffer everything first.
func (h *Handler) respondStreaming(
	ctx context.Context,
	chat watypes.JID,
	sid string,
	start time.Time,
	chunks <-chan string,
	wasVoice bool,
) {
	var sb strings.Builder
	for chunk := range chunks {
		sb.WriteString(chunk)
	}
	text := sb.String()
	if text == "" {
		text = "(no response)"
	}

	h.log.InfoContext(ctx, "channel_response",
		"session_id", sid,
		"latency_ms", time.Since(start).Milliseconds(),
		"channel", "whatsapp-stream",
	)
	h.sendResponse(ctx, chat, sid, text, wasVoice)
}

// sendResponse sends the agent reply as audio when the original message was a
// voice message and synthesis succeeds; otherwise sends plain text.
func (h *Handler) sendResponse(ctx context.Context, to watypes.JID, sid, text string, wasVoice bool) {
	if wasVoice {
		data, mime, err := h.synthesizer.Synthesize(ctx, text)
		if err != nil {
			h.log.WarnContext(ctx, "whatsapp: TTS failed, sending text",
				"session_id", sid, "error", err)
		} else if len(data) > 0 {
			h.log.InfoContext(ctx, "whatsapp: sending TTS audio response",
				"session_id", sid, "mime", mime, "bytes", len(data))
			if sendErr := h.sendAudio(ctx, to, data, mime); sendErr != nil {
				h.log.WarnContext(ctx, "whatsapp: audio send failed, falling back to text",
					"session_id", sid, "error", sendErr)
			} else {
				h.log.InfoContext(ctx, "whatsapp: TTS audio sent successfully", "session_id", sid)
				return
			}
		}
	}
	h.send(ctx, to, text) //nolint:errcheck
}

// sendAudio uploads audio data to WhatsApp servers and sends it as a voice note.
func (h *Handler) sendAudio(ctx context.Context, to watypes.JID, data []byte, mimeType string) error {
	// WhatsApp mobile requires exactly "audio/ogg; codecs=opus" to render PTT
	// voice notes. TTS endpoints may return "audio/opus" or "audio/ogg" for the
	// same OGG/Opus container — normalise here so mobile always plays correctly.
	if strings.Contains(mimeType, "opus") || strings.Contains(mimeType, "ogg") {
		mimeType = "audio/ogg; codecs=opus"
	}
	uploaded, err := h.sender.Upload(ctx, data, whatsmeow.MediaAudio)
	if err != nil {
		return fmt.Errorf("whatsapp: upload audio: %w", err)
	}
	fileLen := uploaded.FileLength
	ptt := true
	_, err = h.sender.SendMessage(ctx, to, &waE2E.Message{
		AudioMessage: &waE2E.AudioMessage{
			URL:           &uploaded.URL,
			DirectPath:    &uploaded.DirectPath,
			MediaKey:      uploaded.MediaKey,
			FileEncSHA256: uploaded.FileEncSHA256,
			FileSHA256:    uploaded.FileSHA256,
			FileLength:    &fileLen,
			Mimetype:      &mimeType,
			PTT:           &ptt,
		},
	})
	return err
}

// send delivers a text message to the given WhatsApp JID.
func (h *Handler) send(ctx context.Context, to watypes.JID, text string) error {
	_, err := h.sender.SendMessage(ctx, to, &waE2E.Message{
		Conversation: strPtr(text),
	})
	if err != nil {
		h.logger().ErrorContext(ctx, "whatsapp: send error", "to", to, "error", err)
		return fmt.Errorf("whatsapp: send to %s: %w", to, err)
	}
	return nil
}

func (h *Handler) logger() *slog.Logger {
	if h.log != nil {
		return h.log
	}
	return slog.Default()
}

// ── helpers ───────────────────────────────────────────────────────────────────

// sessionKey returns the stable session identifier for a WhatsApp sender.
// Format: whatsapp:{jid}
func sessionKey(jid string) string {
	return "whatsapp:" + jid
}

// normaliseJID converts a whatsmeow JID to the canonical string used for
// allowedJID comparison and session keys. ToNonAD strips the agent/device
// suffix so +96170123456@s.whatsapp.net always compares equal regardless of
// which linked device sent the message.
func normaliseJID(jid watypes.JID) string {
	return jid.ToNonAD().String()
}

// extractText returns the plain text from a WhatsApp message proto, or ""
// for media, stickers, reactions, and other non-text message types.
// Captions on image, video, and document messages are treated as the message text.
func extractText(msg *waE2E.Message) string {
	if msg == nil {
		return ""
	}
	if text := msg.GetConversation(); text != "" {
		return text
	}
	if ext := msg.GetExtendedTextMessage(); ext != nil {
		return ext.GetText()
	}
	if img := msg.GetImageMessage(); img != nil {
		return img.GetCaption()
	}
	if vid := msg.GetVideoMessage(); vid != nil {
		return vid.GetCaption()
	}
	if doc := msg.GetDocumentMessage(); doc != nil {
		return doc.GetCaption()
	}
	return ""
}

// processMedia detects image and document sub-messages, downloads the encrypted
// media via whatsmeow, and returns ContentParts ready for the LLM.
// Returns (nil, errUnsupportedMediaType) for recognised-but-unsupported doc
// formats; the caller is responsible for sending the user a reply in that case.
// Download errors are logged and treated as "no media" so text still routes.
func (h *Handler) processMedia(ctx context.Context, msg *waE2E.Message) ([]agenttypes.ContentPart, error) {
	if msg == nil {
		return nil, nil
	}

	if img := msg.GetImageMessage(); img != nil {
		data, err := h.downloader.DownloadAny(ctx, msg)
		if err != nil {
			h.logger().WarnContext(ctx, "whatsapp: image download failed", "error", err)
			return nil, nil
		}
		mime := img.GetMimetype()
		if mime == "" {
			mime = "image/jpeg"
		}
		return []agenttypes.ContentPart{{
			Type:      "image",
			ImageData: base64.StdEncoding.EncodeToString(data),
			MimeType:  mime,
		}}, nil
	}

	if doc := msg.GetDocumentMessage(); doc != nil {
		if doc.GetMimetype() != "application/pdf" {
			return nil, errUnsupportedMediaType
		}
		data, err := h.downloader.DownloadAny(ctx, msg)
		if err != nil {
			h.logger().WarnContext(ctx, "whatsapp: PDF download failed", "error", err)
			return nil, nil
		}
		text, err := attachments.ExtractPDFText(data)
		if err != nil {
			h.logger().WarnContext(ctx, "whatsapp: PDF extraction failed", "error", err)
			return nil, errUnsupportedMediaType
		}
		filename := doc.GetFileName()
		if filename == "" {
			filename = "document.pdf"
		}
		return []agenttypes.ContentPart{{
			Type:     "text",
			Text:     text,
			Filename: filename,
		}}, nil
	}

	if vid := msg.GetVideoMessage(); vid != nil {
		sizeBytes := int64(vid.GetFileLength())
		if sizeBytes > 0 && h.videoMaxSizeMB > 0 && sizeBytes > h.videoMaxSizeMB*1024*1024 {
			return nil, errVideoTooLarge
		}
		data, err := h.downloader.DownloadAny(ctx, msg)
		if err != nil {
			h.logger().WarnContext(ctx, "whatsapp: video download failed", "error", err)
			return nil, nil
		}
		if h.videoMaxSizeMB > 0 && int64(len(data)) > h.videoMaxSizeMB*1024*1024 {
			return nil, errVideoTooLarge
		}
		mimeType := vid.GetMimetype()
		if mimeType == "" || !strings.HasPrefix(mimeType, "video/") {
			mimeType = "video/mp4"
		}
		frames, extractErr := attachments.ExtractFrames(data, mimeType, h.videoMaxFrames)
		if errors.Is(extractErr, attachments.ErrFfmpegUnavailable) {
			return nil, errFfmpegMissing
		}
		if extractErr != nil {
			return nil, errVideoProcessFailed
		}
		return attachments.VideoToContentParts(frames, "video.mp4"), nil
	}

	return nil, nil
}

// buildMsgParts prepends a text part when text is non-empty and returns nil
// when there are no attachment parts (text-only messages don't need Parts set).
func buildMsgParts(text string, attParts []agenttypes.ContentPart) []agenttypes.ContentPart {
	if len(attParts) == 0 {
		return nil
	}
	parts := make([]agenttypes.ContentPart, 0, 1+len(attParts))
	if text != "" {
		parts = append(parts, agenttypes.ContentPart{Type: "text", Text: text})
	}
	return append(parts, attParts...)
}

func strPtr(s string) *string { return &s }
