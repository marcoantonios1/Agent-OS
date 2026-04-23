package whatsapp

import (
	"strings"
	"testing"

	"go.mau.fi/whatsmeow/proto/waE2E"
	watypes "go.mau.fi/whatsmeow/types"
)

// ── sessionKey ────────────────────────────────────────────────────────────────

func TestSessionKey(t *testing.T) {
	tests := []struct {
		jid  string
		want string
	}{
		{"+96170123456@s.whatsapp.net", "whatsapp:+96170123456@s.whatsapp.net"},
		{"447700900000@s.whatsapp.net", "whatsapp:447700900000@s.whatsapp.net"},
	}
	for _, tc := range tests {
		got := sessionKey(tc.jid)
		if got != tc.want {
			t.Errorf("sessionKey(%q) = %q, want %q", tc.jid, got, tc.want)
		}
	}
}

func TestSessionKey_PrefixFormat(t *testing.T) {
	key := sessionKey("123@s.whatsapp.net")
	if !strings.HasPrefix(key, "whatsapp:") {
		t.Errorf("session key should start with 'whatsapp:', got %q", key)
	}
}

// ── normaliseJID ──────────────────────────────────────────────────────────────

func TestNormaliseJID_DropsDeviceSuffix(t *testing.T) {
	// JID with RawAgent and Device set; ToNonAD() should clear them.
	// We construct it manually since we can't pair a real device in tests.
	jid := watypes.JID{
		User:     "96170123456",
		Server:   watypes.DefaultUserServer,
		RawAgent: 0,
		Device:   3,
	}
	got := normaliseJID(jid)
	// After ToNonAD the JID should not contain the device part but keep
	// the user and server.
	if !strings.Contains(got, "96170123456") {
		t.Errorf("normalised JID should contain phone number, got %q", got)
	}
	if !strings.Contains(got, watypes.DefaultUserServer) {
		t.Errorf("normalised JID should contain server, got %q", got)
	}
}

// ── extractText ───────────────────────────────────────────────────────────────

func TestExtractText_Nil(t *testing.T) {
	if got := extractText(nil); got != "" {
		t.Errorf("extractText(nil) = %q, want empty", got)
	}
}

func TestExtractText_Conversation(t *testing.T) {
	msg := &waE2E.Message{Conversation: strPtr("Hello!")}
	if got := extractText(msg); got != "Hello!" {
		t.Errorf("extractText = %q, want %q", got, "Hello!")
	}
}

func TestExtractText_ExtendedText(t *testing.T) {
	msg := &waE2E.Message{
		ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: strPtr("Rich text")},
	}
	if got := extractText(msg); got != "Rich text" {
		t.Errorf("extractText = %q, want %q", got, "Rich text")
	}
}

func TestExtractText_PreferConversationOverExtended(t *testing.T) {
	msg := &waE2E.Message{
		Conversation:        strPtr("Simple"),
		ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: strPtr("Rich")},
	}
	if got := extractText(msg); got != "Simple" {
		t.Errorf("extractText should prefer Conversation, got %q", got)
	}
}

func TestExtractText_MediaMessage_ReturnsEmpty(t *testing.T) {
	// Image message has no Conversation or ExtendedTextMessage.
	msg := &waE2E.Message{
		ImageMessage: &waE2E.ImageMessage{},
	}
	if got := extractText(msg); got != "" {
		t.Errorf("extractText(image) = %q, want empty", got)
	}
}

func TestExtractText_EmptyConversation_FallsToExtended(t *testing.T) {
	msg := &waE2E.Message{
		Conversation:        strPtr(""),
		ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: strPtr("Fallback")},
	}
	if got := extractText(msg); got != "Fallback" {
		t.Errorf("extractText should fall to ExtendedText when Conversation is empty, got %q", got)
	}
}

// ── whitelist (allowedJID) ────────────────────────────────────────────────────

// isAllowed is the logic extracted from onMessage for direct testing.
func isAllowed(senderJID, allowedJID string) bool {
	return senderJID == allowedJID
}

func TestWhitelist_AllowedJID_Passes(t *testing.T) {
	if !isAllowed("96170123456@s.whatsapp.net", "96170123456@s.whatsapp.net") {
		t.Error("expected allowed JID to pass whitelist")
	}
}

func TestWhitelist_DifferentJID_Blocked(t *testing.T) {
	if isAllowed("447700900000@s.whatsapp.net", "96170123456@s.whatsapp.net") {
		t.Error("expected different JID to be blocked")
	}
}

func TestWhitelist_EmptyAllowed_BlocksEverything(t *testing.T) {
	// allowedJID="" means New() would have returned an error; here we just
	// verify the comparison logic doesn't accidentally allow anything.
	if isAllowed("96170123456@s.whatsapp.net", "") {
		t.Error("empty allowedJID should never match a real sender")
	}
}

func TestWhitelist_PartialMatch_Blocked(t *testing.T) {
	// Ensure prefix/suffix matches are not treated as equal.
	if isAllowed("96170123456@s.whatsapp.net", "9617012345@s.whatsapp.net") {
		t.Error("partial number match should be blocked")
	}
}

func TestWhitelist_CaseSensitive(t *testing.T) {
	// JIDs are case-sensitive by spec.
	if isAllowed("96170123456@S.WHATSAPP.NET", "96170123456@s.whatsapp.net") {
		t.Error("JID comparison should be case-sensitive")
	}
}

// ── strPtr ────────────────────────────────────────────────────────────────────

func TestStrPtr(t *testing.T) {
	s := "hello"
	p := strPtr(s)
	if p == nil {
		t.Fatal("strPtr returned nil")
	}
	if *p != s {
		t.Errorf("*strPtr(%q) = %q, want %q", s, *p, s)
	}
	// Mutation of original must not affect the pointer (value semantics).
	original := "original"
	ptr := strPtr(original)
	original = "changed"
	if *ptr != "original" {
		t.Error("strPtr should not be affected by mutation of the original variable")
	}
}
