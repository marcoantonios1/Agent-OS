package whatsapp

import (
	"context"
	"fmt"
	"strings"

	watypes "go.mau.fi/whatsmeow/types"

	"github.com/marcoantonios1/Agent-OS/internal/sessions"
)

// NotifyReminder implements reminder.Notifier. It resolves the target JID from
// r.UserID first, then falls back to parsing it from r.SessionID (legacy format
// "whatsapp:{jid}"). Reminders with no resolvable WhatsApp JID are silently
// ignored so the worker can broadcast to all registered notifiers.
func (h *Handler) NotifyReminder(ctx context.Context, r *sessions.Reminder) error {
	// Prefer UserID — set directly by the heartbeat worker and reminder tools.
	jidStr := r.UserID
	if jidStr == "" {
		// Legacy fallback: session ID encoded as "whatsapp:{jid}".
		if !strings.HasPrefix(r.SessionID, "whatsapp:") {
			return nil
		}
		jidStr = strings.TrimPrefix(r.SessionID, "whatsapp:")
	}

	jid, err := watypes.ParseJID(jidStr)
	if err != nil {
		return fmt.Errorf("whatsapp: parse JID %q: %w", jidStr, err)
	}

	if err := h.send(ctx, jid, r.Message); err != nil {
		return err
	}
	h.log.InfoContext(ctx, "whatsapp: reminder delivered",
		"reminder_id", r.ID, "jid", jidStr)
	return nil
}
