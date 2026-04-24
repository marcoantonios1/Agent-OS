package whatsapp

import (
	"context"
	"strings"
	"testing"

	"github.com/marcoantonios1/Agent-OS/internal/sessions"
	"github.com/marcoantonios1/Agent-OS/internal/types"
)

func TestWhatsAppNotifier_SkipsNonWhatsAppSession(t *testing.T) {
	// client is nil — any attempt to use it would panic, proving it is not reached.
	h := &Handler{}
	for _, sid := range []string{
		"discord:guild:chan:user",
		"web:abc",
		"",
	} {
		if err := h.NotifyReminder(context.Background(), &sessions.Reminder{
			SessionID: sid, ChannelID: types.ChannelID("other"),
		}); err != nil {
			t.Errorf("sid=%q: expected nil, got %v", sid, err)
		}
	}
}

func TestWhatsAppNotifier_InvalidJID(t *testing.T) {
	// ParseJID errors when the user part contains an unexpected number of dots
	// (AD-JID format requires exactly "user.agent:device@server").
	h := &Handler{}
	err := h.NotifyReminder(context.Background(), &sessions.Reminder{
		ID:        "r1",
		SessionID: "whatsapp:a.b.c@s.whatsapp.net",
		Message:   "ping",
	})
	if err == nil {
		t.Fatal("expected error for invalid JID, got nil")
	}
}

func TestWhatsAppNotifier_JIDExtractedFromSessionKey(t *testing.T) {
	jid := "96170855137@s.whatsapp.net"
	sid := sessionKey(jid)

	// Verify the parsing logic extracts the correct JID string.
	extracted := strings.TrimPrefix(sid, "whatsapp:")
	if extracted != jid {
		t.Errorf("extracted JID = %q, want %q", extracted, jid)
	}
}

func TestWhatsAppNotifier_SessionKeyFormat(t *testing.T) {
	sid := sessionKey("96170855137@s.whatsapp.net")
	if !strings.HasPrefix(sid, "whatsapp:") {
		t.Errorf("session key %q does not start with 'whatsapp:'", sid)
	}
	parts := strings.SplitN(sid, ":", 2)
	if len(parts) != 2 || parts[1] == "" {
		t.Errorf("expected 'whatsapp:<jid>', got %q", sid)
	}
}
