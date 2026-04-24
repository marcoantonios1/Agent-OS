package discord

import (
	"context"
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"

	"github.com/marcoantonios1/Agent-OS/internal/sessions"
	"github.com/marcoantonios1/Agent-OS/internal/types"
)

// ── helpers ───────────────────────────────────────────────────────────────────

func makeReminder(sessionID, msg string) *sessions.Reminder {
	return &sessions.Reminder{ID: "r1", SessionID: sessionID, Message: msg}
}

// ── NotifyReminder: routing ───────────────────────────────────────────────────

func TestDiscordNotifier_SkipsNonDiscordSession(t *testing.T) {
	// session is nil — any attempt to use it would panic, proving it is not reached.
	h := &Handler{}
	for _, sid := range []string{
		"whatsapp:96170@s.whatsapp.net",
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

func TestDiscordNotifier_ErrorWhenNotConnected(t *testing.T) {
	h := &Handler{session: nil}
	err := h.NotifyReminder(context.Background(), makeReminder("discord:g:c:u", "ping"))
	if err == nil {
		t.Fatal("expected error when session is nil")
	}
}

func TestDiscordNotifier_MalformedSessionID(t *testing.T) {
	h := &Handler{session: &discordgo.Session{}}
	err := h.NotifyReminder(context.Background(), makeReminder("discord:only-two-parts", "ping"))
	if err == nil {
		t.Fatal("expected error for malformed session ID")
	}
}

// ── sessionKey format (used by NotifyReminder parser) ─────────────────────────

func TestDiscordNotifier_GuildSessionKeyHasChannelAtIndex2(t *testing.T) {
	sid := sessionKey("guild1", "chan42", "user9")
	parts := strings.SplitN(sid, ":", 4)
	if len(parts) != 4 {
		t.Fatalf("expected 4 parts, got %d: %q", len(parts), sid)
	}
	if parts[2] != "chan42" {
		t.Errorf("channelID part = %q, want %q", parts[2], "chan42")
	}
}

func TestDiscordNotifier_DMSessionKeyHasChannelAtIndex2(t *testing.T) {
	sid := sessionKey("", "dmchan7", "user9") // empty guildID → "dm"
	parts := strings.SplitN(sid, ":", 4)
	if len(parts) != 4 {
		t.Fatalf("expected 4 parts, got %d: %q", len(parts), sid)
	}
	if parts[1] != "dm" {
		t.Errorf("parts[1] = %q, want \"dm\"", parts[1])
	}
	if parts[2] != "dmchan7" {
		t.Errorf("channelID part = %q, want %q", parts[2], "dmchan7")
	}
}
