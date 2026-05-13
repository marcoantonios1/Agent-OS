package telegram

import (
	"context"
	"testing"

	"github.com/marcoantonios1/Agent-OS/internal/sessions"
)

// ── sessionKey ────────────────────────────────────────────────────────────────

func TestSessionKey(t *testing.T) {
	got := sessionKey(123456789, 987654321)
	want := "telegram:123456789:987654321"
	if got != want {
		t.Errorf("sessionKey = %q, want %q", got, want)
	}
}

func TestSessionKey_DMChat(t *testing.T) {
	// In DMs, chatID == userID — key must still be well-formed.
	got := sessionKey(42, 42)
	want := "telegram:42:42"
	if got != want {
		t.Errorf("sessionKey (DM) = %q, want %q", got, want)
	}
}

// ── splitMessage ──────────────────────────────────────────────────────────────

func TestSplitMessage_Short(t *testing.T) {
	parts := splitMessage("hello", 4096)
	if len(parts) != 1 || parts[0] != "hello" {
		t.Errorf("splitMessage(short) = %v, want [hello]", parts)
	}
}

func TestSplitMessage_ExactLimit(t *testing.T) {
	text := make([]byte, 4096)
	for i := range text {
		text[i] = 'a'
	}
	parts := splitMessage(string(text), 4096)
	if len(parts) != 1 {
		t.Errorf("splitMessage(exact) = %d parts, want 1", len(parts))
	}
}

func TestSplitMessage_OverLimit(t *testing.T) {
	// Build a 9000-char string; expect it split into 3 parts.
	text := make([]byte, 9000)
	for i := range text {
		text[i] = 'b'
	}
	parts := splitMessage(string(text), 4096)
	if len(parts) != 3 {
		t.Errorf("splitMessage(9000) = %d parts, want 3", len(parts))
	}
	total := 0
	for _, p := range parts {
		total += len(p)
	}
	if total != 9000 {
		t.Errorf("splitMessage total chars = %d, want 9000", total)
	}
}

func TestSplitMessage_PreservesNewlines(t *testing.T) {
	// 4000 'a's + newline + 200 'b's — newline is within the first window,
	// so the split should happen at the newline.
	a := make([]byte, 4000)
	for i := range a {
		a[i] = 'a'
	}
	b := make([]byte, 200)
	for i := range b {
		b[i] = 'b'
	}
	text := string(a) + "\n" + string(b)
	parts := splitMessage(text, 4096)
	if len(parts) != 2 {
		t.Fatalf("splitMessage(newline-split) = %d parts, want 2", len(parts))
	}
	// First part ends with the newline.
	if parts[0][len(parts[0])-1] != '\n' {
		t.Errorf("first part should end with newline, got %q…", parts[0][len(parts[0])-5:])
	}
}

// ── truncateForEdit ───────────────────────────────────────────────────────────

func TestTruncateForEdit_Short(t *testing.T) {
	got := truncateForEdit("hello")
	if got != "hello" {
		t.Errorf("truncateForEdit(short) = %q, want hello", got)
	}
}

func TestTruncateForEdit_Long(t *testing.T) {
	text := make([]byte, 5000)
	for i := range text {
		text[i] = 'x'
	}
	got := truncateForEdit(string(text))
	if len(got) > maxMessageLen {
		t.Errorf("truncateForEdit result len = %d, exceeds %d", len(got), maxMessageLen)
	}
	if got[len(got)-3:] != "…" {
		t.Errorf("truncateForEdit result should end with '…', got %q", got[len(got)-5:])
	}
}

// ── chatIDFromReminder ────────────────────────────────────────────────────────

func TestChatIDFromReminder_SessionID(t *testing.T) {
	r := &sessions.Reminder{
		ID:        "r1",
		SessionID: "telegram:111:222",
		UserID:    "111",
	}
	got, err := chatIDFromReminder(r)
	if err != nil {
		t.Fatalf("chatIDFromReminder: %v", err)
	}
	if got != 222 {
		t.Errorf("chatIDFromReminder = %d, want 222", got)
	}
}

func TestChatIDFromReminder_FallbackUserID(t *testing.T) {
	// SessionID doesn't have telegram prefix — should fall back to UserID.
	r := &sessions.Reminder{
		ID:        "r2",
		SessionID: "discord:server:channel:555",
		UserID:    "333",
	}
	got, err := chatIDFromReminder(r)
	if err != nil {
		t.Fatalf("chatIDFromReminder fallback: %v", err)
	}
	if got != 333 {
		t.Errorf("chatIDFromReminder fallback = %d, want 333", got)
	}
}

func TestChatIDFromReminder_NoValidID(t *testing.T) {
	r := &sessions.Reminder{
		ID:        "r3",
		SessionID: "notelegram",
		UserID:    "not-a-number",
	}
	_, err := chatIDFromReminder(r)
	if err == nil {
		t.Error("chatIDFromReminder should return error when no valid ID found")
	}
}

// ── NotifyReminder — channel mismatch silently ignored ───────────────────────

func TestNotifyReminder_NonTelegramReminder(t *testing.T) {
	// A reminder from a different channel (no telegram prefix, non-numeric UserID)
	// should be silently ignored (return nil) without panicking.
	h := &Handler{} // no bot set — would panic if it tried to send
	r := &sessions.Reminder{
		ID:        "r4",
		SessionID: "discord:g:c:u",
		UserID:    "not-a-number",
		Message:   "ping",
	}
	if err := h.NotifyReminder(context.Background(), r); err != nil {
		t.Errorf("NotifyReminder(non-telegram) = %v, want nil", err)
	}
}
