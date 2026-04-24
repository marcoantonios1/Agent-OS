package whatsapp

import (
	"context"
	"fmt"
	"strings"

	watypes "go.mau.fi/whatsmeow/types"

	"github.com/marcoantonios1/Agent-OS/internal/sessions"
)

// NotifyReminder implements reminder.Notifier. It delivers the reminder message
// to the WhatsApp JID encoded in r.SessionID.
//
// Session ID format (set by sessionKey):
//
//	whatsapp:{jid}   e.g. whatsapp:96170855137@s.whatsapp.net
//
// Only sessions whose ID starts with "whatsapp:" are handled; all others are
// silently ignored so the worker can broadcast to all registered notifiers.
func (h *Handler) NotifyReminder(ctx context.Context, r *sessions.Reminder) error {
	if !strings.HasPrefix(r.SessionID, "whatsapp:") {
		return nil
	}

	jidStr := strings.TrimPrefix(r.SessionID, "whatsapp:")
	jid, err := watypes.ParseJID(jidStr)
	if err != nil {
		return fmt.Errorf("whatsapp: parse JID from session %q: %w", r.SessionID, err)
	}

	if err := h.send(ctx, jid, r.Message); err != nil {
		return err
	}
	h.log.InfoContext(ctx, "whatsapp: reminder delivered",
		"reminder_id", r.ID, "jid", jidStr)
	return nil
}
