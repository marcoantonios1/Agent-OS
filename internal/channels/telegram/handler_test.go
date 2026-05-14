package telegram

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"github.com/marcoantonios1/Agent-OS/internal/sessions"
	"github.com/marcoantonios1/Agent-OS/internal/types"
	"github.com/marcoantonios1/Agent-OS/internal/voice"
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

// ── voice test helpers ────────────────────────────────────────────────────────

// mockBot satisfies BotAPI for voice tests.
type mockBot struct {
	mu        sync.Mutex
	sent      []string // text of each message sent
	fileURL   string
	fileErr   error
}

func (m *mockBot) Send(c tgbotapi.Chattable) (tgbotapi.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if msg, ok := c.(tgbotapi.MessageConfig); ok {
		m.sent = append(m.sent, msg.Text)
	}
	if edit, ok := c.(tgbotapi.EditMessageTextConfig); ok {
		m.sent = append(m.sent, edit.Text)
	}
	return tgbotapi.Message{}, nil
}

func (m *mockBot) GetFileDirectURL(_ string) (string, error) {
	return m.fileURL, m.fileErr
}

func (m *mockBot) GetUpdatesChan(_ tgbotapi.UpdateConfig) tgbotapi.UpdatesChannel {
	return make(tgbotapi.UpdatesChannel)
}

func (m *mockBot) StopReceivingUpdates() {}

// stubTranscriber is a controllable voice.Transcriber.
type stubTranscriber struct {
	text string
	err  error
}

func (s *stubTranscriber) Transcribe(_ context.Context, _ []byte, _ string) (string, error) {
	return s.text, s.err
}

// recordingDispatcher satisfies web.Dispatcher and records every routed message.
type recordingDispatcher struct {
	mu       sync.Mutex
	received []types.InboundMessage
}

func (d *recordingDispatcher) Route(_ context.Context, msg types.InboundMessage) (types.OutboundMessage, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.received = append(d.received, msg)
	return types.OutboundMessage{Text: "agent reply"}, nil
}

// audioServer starts a test HTTP server that serves audioData and returns its URL.
func audioServer(t *testing.T, audioData []byte) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write(audioData) //nolint:errcheck
	}))
}

// voiceMessage builds a minimal tgbotapi.Message with a Voice payload.
func voiceMessage(userID, chatID int64, fileID, mimeType string) *tgbotapi.Message {
	return &tgbotapi.Message{
		MessageID: 1,
		From:      &tgbotapi.User{ID: userID},
		Chat:      &tgbotapi.Chat{ID: chatID},
		Voice:     &tgbotapi.Voice{FileID: fileID, MimeType: mimeType},
	}
}

// audioMessage builds a minimal tgbotapi.Message with an Audio payload.
func audioMessage(userID, chatID int64, fileID, mimeType string) *tgbotapi.Message {
	return &tgbotapi.Message{
		MessageID: 2,
		From:      &tgbotapi.User{ID: userID},
		Chat:      &tgbotapi.Chat{ID: chatID},
		Audio:     &tgbotapi.Audio{FileID: fileID, MimeType: mimeType},
	}
}

// newVoiceHandler builds a Handler wired for voice/audio tests.
func newVoiceHandler(bot *mockBot, tr voice.Transcriber, disp *recordingDispatcher, srv *httptest.Server) *Handler {
	h := &Handler{
		bot:         bot,
		username:    "testbot",
		dispatcher:  disp,
		allowedUID:  111,
		transcriber: tr,
		log:         slog.Default(),
		httpClient:  srv.Client(),
	}
	return h
}

// ── voice tests ───────────────────────────────────────────────────────────────

func TestVoice_TranscribedTextRoutedWithPrefix(t *testing.T) {
	srv := audioServer(t, []byte("fake ogg"))
	defer srv.Close()

	bot := &mockBot{fileURL: srv.URL}
	tr := &stubTranscriber{text: "hello from voice"}
	disp := &recordingDispatcher{}
	h := newVoiceHandler(bot, tr, disp, srv)

	h.handleMessage(context.Background(), voiceMessage(111, 100, "file1", "audio/ogg"))

	if len(disp.received) != 1 {
		t.Fatalf("expected 1 dispatched message, got %d", len(disp.received))
	}
	want := "[Voice message transcribed]: hello from voice"
	if disp.received[0].Text != want {
		t.Errorf("routed text = %q, want %q", disp.received[0].Text, want)
	}
}

func TestVoice_NoopTranscriber_SendsHelpfulReply(t *testing.T) {
	srv := audioServer(t, []byte("fake ogg"))
	defer srv.Close()

	bot := &mockBot{fileURL: srv.URL}
	tr := &voice.NoopTranscriber{}
	disp := &recordingDispatcher{}
	h := newVoiceHandler(bot, tr, disp, srv)

	h.handleMessage(context.Background(), voiceMessage(111, 100, "file1", "audio/ogg"))

	if len(disp.received) != 0 {
		t.Errorf("dispatcher should not be called when transcription unsupported, got %d messages", len(disp.received))
	}
	if len(bot.sent) != 1 {
		t.Fatalf("expected 1 reply, got %d", len(bot.sent))
	}
	if !strings.Contains(bot.sent[0], "aren't supported") {
		t.Errorf("reply %q should tell user voice isn't supported", bot.sent[0])
	}
}

func TestVoice_TranscriptionError_NotifiesUser(t *testing.T) {
	srv := audioServer(t, []byte("fake ogg"))
	defer srv.Close()

	bot := &mockBot{fileURL: srv.URL}
	tr := &stubTranscriber{err: errors.New("whisper unavailable")}
	disp := &recordingDispatcher{}
	h := newVoiceHandler(bot, tr, disp, srv)

	h.handleMessage(context.Background(), voiceMessage(111, 100, "file1", "audio/ogg"))

	if len(disp.received) != 0 {
		t.Errorf("dispatcher should not be called on transcription error, got %d messages", len(disp.received))
	}
	if len(bot.sent) != 1 {
		t.Fatalf("expected 1 error reply, got %d", len(bot.sent))
	}
	if !strings.Contains(bot.sent[0], "couldn't transcribe") {
		t.Errorf("reply %q should mention transcription failure", bot.sent[0])
	}
}

func TestVoice_FileURLError_NotifiesUser(t *testing.T) {
	srv := audioServer(t, nil)
	defer srv.Close()

	bot := &mockBot{fileErr: errors.New("file not found")}
	tr := &stubTranscriber{text: "should not reach"}
	disp := &recordingDispatcher{}
	h := newVoiceHandler(bot, tr, disp, srv)

	h.handleMessage(context.Background(), voiceMessage(111, 100, "file1", "audio/ogg"))

	if len(disp.received) != 0 {
		t.Errorf("dispatcher should not be called on URL error, got %d messages", len(disp.received))
	}
	if len(bot.sent) != 1 || !strings.Contains(bot.sent[0], "couldn't access") {
		t.Errorf("expected 'couldn't access' reply, got %v", bot.sent)
	}
}

func TestVoice_NonWhitelistedSender_Dropped(t *testing.T) {
	srv := audioServer(t, []byte("ogg"))
	defer srv.Close()

	bot := &mockBot{fileURL: srv.URL}
	tr := &stubTranscriber{text: "spy message"}
	disp := &recordingDispatcher{}
	h := newVoiceHandler(bot, tr, disp, srv)

	h.handleMessage(context.Background(), voiceMessage(999, 100, "file1", "audio/ogg"))

	if len(disp.received) != 0 || len(bot.sent) != 0 {
		t.Error("non-whitelisted sender should be silently dropped")
	}
}

func TestAudio_TranscribedTextRoutedWithPrefix(t *testing.T) {
	srv := audioServer(t, []byte("fake mp3"))
	defer srv.Close()

	bot := &mockBot{fileURL: srv.URL}
	tr := &stubTranscriber{text: "audio file content"}
	disp := &recordingDispatcher{}
	h := newVoiceHandler(bot, tr, disp, srv)

	h.handleMessage(context.Background(), audioMessage(111, 100, "file2", "audio/mpeg"))

	if len(disp.received) != 1 {
		t.Fatalf("expected 1 dispatched message, got %d", len(disp.received))
	}
	want := "[Voice message transcribed]: audio file content"
	if disp.received[0].Text != want {
		t.Errorf("routed text = %q, want %q", disp.received[0].Text, want)
	}
}

func TestAudio_DefaultMimeType(t *testing.T) {
	srv := audioServer(t, []byte("fake mp3"))
	defer srv.Close()

	var capturedMime string
	tr := &captureTranscriber{}
	bot := &mockBot{fileURL: srv.URL}
	disp := &recordingDispatcher{}
	h := newVoiceHandler(bot, tr, disp, srv)
	_ = capturedMime

	// Audio message with empty MimeType — handler should default to audio/mpeg.
	msg := audioMessage(111, 100, "file2", "")
	h.handleMessage(context.Background(), msg)

	if tr.capturedMime != "audio/mpeg" {
		t.Errorf("default mime for Audio = %q, want audio/mpeg", tr.capturedMime)
	}
}

func TestVoice_DefaultMimeType(t *testing.T) {
	srv := audioServer(t, []byte("fake ogg"))
	defer srv.Close()

	tr := &captureTranscriber{}
	bot := &mockBot{fileURL: srv.URL}
	disp := &recordingDispatcher{}
	h := newVoiceHandler(bot, tr, disp, srv)

	// Voice message with empty MimeType — handler should default to audio/ogg.
	msg := voiceMessage(111, 100, "file1", "")
	h.handleMessage(context.Background(), msg)

	if tr.capturedMime != "audio/ogg" {
		t.Errorf("default mime for Voice = %q, want audio/ogg", tr.capturedMime)
	}
}

// captureTranscriber records the mimeType it was called with.
type captureTranscriber struct {
	capturedMime string
}

func (c *captureTranscriber) Transcribe(_ context.Context, _ []byte, mimeType string) (string, error) {
	c.capturedMime = mimeType
	return "ok", nil
}
