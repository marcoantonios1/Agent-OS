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

	"github.com/marcoantonios1/Agent-OS/internal/channels/web"
	agenttypes "github.com/marcoantonios1/Agent-OS/internal/types"
)

// Handler listens for WhatsApp messages and routes them through the shared
// Dispatcher. Only messages from allowedJID are processed; all others are
// silently dropped.
type Handler struct {
	client     *whatsmeow.Client
	dispatcher web.Dispatcher
	allowedJID string // only respond to this JID; validated non-empty by New
	log        *slog.Logger
}

// New creates a Handler.
//   - dispatcher is the router (satisfies web.Dispatcher and optionally web.StreamDispatcher).
//   - storePath is the path to the SQLite DB that persists the device pairing session.
//   - allowedJID is the only sender whose messages will be processed
//     (e.g. "+96170123456@s.whatsapp.net"). Must be non-empty.
func New(dispatcher web.Dispatcher, storePath, allowedJID string) (*Handler, error) {
	if allowedJID == "" {
		return nil, fmt.Errorf("whatsapp: WHATSAPP_ALLOWED_JID is required when WhatsApp is enabled — set it to your personal number's JID")
	}

	ctx := context.Background()
	container, err := sqlstore.New(ctx, "sqlite", "file:"+storePath+"?_foreign_keys=on", waLog.Noop)
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
		client:     client,
		dispatcher: dispatcher,
		allowedJID: allowedJID,
		log:        slog.Default(),
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

	senderJID := normaliseJID(evt.Info.Sender)
	if senderJID != h.allowedJID {
		h.log.Info("whatsapp: ignored message from non-whitelisted sender", "jid", senderJID)
		return
	}

	text := extractText(evt.Message)
	if text == "" {
		return // media, sticker, reaction, etc. — silently ignore
	}

	ctx := context.Background()
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
	}

	h.routeAndRespond(ctx, evt.Info.Chat, inbound, sid, start)
}

// routeAndRespond dispatches inbound via RouteStream when the dispatcher
// supports it, falling back to blocking Route() on error or when unavailable.
func (h *Handler) routeAndRespond(
	ctx context.Context,
	chat watypes.JID,
	inbound agenttypes.InboundMessage,
	sid string,
	start time.Time,
) {
	if sd, ok := h.dispatcher.(web.StreamDispatcher); ok {
		chunks, err := sd.RouteStream(ctx, inbound)
		if err == nil {
			h.respondStreaming(ctx, chat, sid, start, chunks)
			return
		}
		h.log.WarnContext(ctx, "whatsapp: stream route failed, falling back to blocking",
			"session_id", sid, "error", err)
	}

	out, err := h.dispatcher.Route(ctx, inbound)
	if err != nil {
		h.log.ErrorContext(ctx, "whatsapp: route error", "session_id", sid, "error", err)
		h.send(ctx, chat, "Sorry, something went wrong. Please try again.")
		return
	}

	h.log.InfoContext(ctx, "channel_response",
		"session_id", sid,
		"latency_ms", time.Since(start).Milliseconds(),
		"channel", "whatsapp",
	)
	h.send(ctx, chat, out.Text)
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
	h.send(ctx, chat, text)
}

// send delivers a text message to the given WhatsApp JID.
func (h *Handler) send(ctx context.Context, to watypes.JID, text string) {
	_, err := h.client.SendMessage(ctx, to, &waE2E.Message{
		Conversation: strPtr(text),
	})
	if err != nil {
		h.log.ErrorContext(ctx, "whatsapp: send error", "to", to, "error", err)
	}
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
	return ""
}

func strPtr(s string) *string { return &s }
