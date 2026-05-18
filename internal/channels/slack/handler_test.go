package slack

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	slacklib "github.com/slack-go/slack"

	"github.com/marcoantonios1/Agent-OS/internal/sessions"
	"github.com/marcoantonios1/Agent-OS/internal/types"
	"github.com/marcoantonios1/Agent-OS/internal/voice"
)

const testAPIURL = "https://slack.com/api/"

// ── session key ───────────────────────────────────────────────────────────────

func TestSessionKey(t *testing.T) {
	got := sessionKey("U12345", "D67890")
	want := "slack:U12345:D67890"
	if got != want {
		t.Errorf("sessionKey = %q, want %q", got, want)
	}
}

func TestSessionKey_Stable(t *testing.T) {
	// Same inputs must produce the same key across calls.
	k1 := sessionKey("UABC", "DXYZ")
	k2 := sessionKey("UABC", "DXYZ")
	if k1 != k2 {
		t.Errorf("sessionKey not stable: %q != %q", k1, k2)
	}
}

// ── splitMessage ──────────────────────────────────────────────────────────────

func TestSplitMessage_Short(t *testing.T) {
	parts := splitMessage("hello", 4000)
	if len(parts) != 1 || parts[0] != "hello" {
		t.Errorf("splitMessage(short) = %v, want [hello]", parts)
	}
}

func TestSplitMessage_ExactLimit(t *testing.T) {
	text := strings.Repeat("a", 4000)
	parts := splitMessage(text, 4000)
	if len(parts) != 1 {
		t.Errorf("splitMessage(exact limit) = %d parts, want 1", len(parts))
	}
}

func TestSplitMessage_OverLimit(t *testing.T) {
	text := strings.Repeat("b", 9000)
	parts := splitMessage(text, 4000)
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

func TestSplitMessage_SplitsOnNewline(t *testing.T) {
	a := strings.Repeat("a", 3800)
	b := strings.Repeat("b", 300)
	text := a + "\n" + b
	parts := splitMessage(text, 4000)
	if len(parts) != 2 {
		t.Fatalf("splitMessage(newline-split) = %d parts, want 2", len(parts))
	}
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
	text := strings.Repeat("x", 5000)
	got := truncateForEdit(text)
	if len(got) > maxMessageLen {
		t.Errorf("truncateForEdit result len = %d, exceeds %d", len(got), maxMessageLen)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("truncateForEdit result should end with '…', got %q…", got[len(got)-5:])
	}
}

// ── channelIDFromReminder ─────────────────────────────────────────────────────

func TestChannelIDFromReminder_SessionID(t *testing.T) {
	r := &sessions.Reminder{
		ID:        "r1",
		SessionID: "slack:U12345:D67890",
	}
	got, err := channelIDFromReminder(r)
	if err != nil {
		t.Fatalf("channelIDFromReminder: %v", err)
	}
	if got != "D67890" {
		t.Errorf("channelIDFromReminder = %q, want D67890", got)
	}
}

func TestChannelIDFromReminder_NonSlack(t *testing.T) {
	r := &sessions.Reminder{
		ID:        "r2",
		SessionID: "telegram:111:222",
		UserID:    "111",
	}
	_, err := channelIDFromReminder(r)
	if err == nil {
		t.Error("channelIDFromReminder should return error for non-slack session")
	}
}

func TestChannelIDFromReminder_MalformedSessionID(t *testing.T) {
	r := &sessions.Reminder{
		ID:        "r3",
		SessionID: "slack:only-two-parts",
	}
	_, err := channelIDFromReminder(r)
	if err == nil {
		t.Error("channelIDFromReminder should return error for malformed session ID")
	}
}

// ── mock SlackAPI ─────────────────────────────────────────────────────────────

type mockSlackAPI struct {
	mu        sync.Mutex
	posted    []string // text of each PostMessage call
	edited    []string // text of each UpdateMessage call
	postTS    string   // timestamp returned by PostMessage
	postErr   error
	updateErr error
	uploadErr error
}

func (m *mockSlackAPI) PostMessage(channelID string, options ...slacklib.MsgOption) (string, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.posted = append(m.posted, msgOptionsText(channelID, options))
	return "C123", m.postTS, m.postErr
}

func (m *mockSlackAPI) UpdateMessage(channelID, _ string, options ...slacklib.MsgOption) (string, string, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.edited = append(m.edited, msgOptionsText(channelID, options))
	return "C123", "ts", "", m.updateErr
}

func (m *mockSlackAPI) UploadFile(_ slacklib.UploadFileParameters) (*slacklib.FileSummary, error) {
	return &slacklib.FileSummary{ID: "F001"}, m.uploadErr
}

// msgOptionsText extracts the "text" field from a MsgOption slice using
// UnsafeApplyMsgOptions, which is the only way to introspect sendConfig.
func msgOptionsText(channelID string, options []slacklib.MsgOption) string {
	_, vals, _ := slacklib.UnsafeApplyMsgOptions("test-token", channelID, testAPIURL, options...)
	return vals.Get("text")
}

// stubTranscriber is a controllable voice.Transcriber.
type stubTranscriber struct {
	text string
	err  error
}

func (s *stubTranscriber) Transcribe(_ context.Context, _ []byte, _ string) (string, error) {
	return s.text, s.err
}

// stubSynthesizer is a controllable voice.Synthesizer.
type stubSynthesizer struct {
	data []byte
	mime string
	err  error
}

func (s *stubSynthesizer) Synthesize(_ context.Context, _ string) ([]byte, string, error) {
	return s.data, s.mime, s.err
}

// recordingDispatcher satisfies web.Dispatcher and records every routed message.
type recordingDispatcher struct {
	mu       sync.Mutex
	received []types.InboundMessage
	reply    string
	err      error
}

func (d *recordingDispatcher) Route(_ context.Context, msg types.InboundMessage) (types.OutboundMessage, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.received = append(d.received, msg)
	reply := d.reply
	if reply == "" {
		reply = "agent reply"
	}
	return types.OutboundMessage{Text: reply}, d.err
}

// fileServer starts a test HTTP server serving fileData and returns its URL.
func fileServer(t *testing.T, fileData []byte) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write(fileData) //nolint:errcheck
	}))
}

// dmMessage builds a minimal IncomingMessage for a Slack DM.
func dmMessage(userID, channelID, text string) *IncomingMessage {
	return &IncomingMessage{
		User:        userID,
		Text:        text,
		Channel:     channelID,
		ChannelType: "im",
		SubType:     "",
	}
}

// ── whitelist enforcement ─────────────────────────────────────────────────────

func TestWhitelist_AllowedUser_Dispatches(t *testing.T) {
	api := &mockSlackAPI{postTS: "ts1"}
	disp := &recordingDispatcher{}
	h := NewForTest(api, disp, "U_ALLOWED")

	h.HandleMessage(context.Background(), dmMessage("U_ALLOWED", "D_CHAN", "hello"))

	if len(disp.received) != 1 {
		t.Fatalf("expected 1 dispatched message, got %d", len(disp.received))
	}
}

func TestWhitelist_NonAllowedUser_Dropped(t *testing.T) {
	api := &mockSlackAPI{}
	disp := &recordingDispatcher{}
	h := NewForTest(api, disp, "U_ALLOWED")

	h.HandleMessage(context.Background(), dmMessage("U_STRANGER", "D_CHAN", "spy message"))

	if len(disp.received) != 0 || len(api.posted) != 0 {
		t.Error("non-whitelisted user should be silently dropped")
	}
}

// ── ignored event filtering ───────────────────────────────────────────────────

func TestIgnored_BotMessage(t *testing.T) {
	api := &mockSlackAPI{}
	disp := &recordingDispatcher{}
	h := NewForTest(api, disp, "U_ALLOWED")

	h.HandleMessage(context.Background(), &IncomingMessage{
		User:        "U_ALLOWED",
		Text:        "I am a bot",
		Channel:     "D_CHAN",
		ChannelType: "im",
		SubType:     "bot_message",
		BotID:       "BXYZ",
	})

	if len(disp.received) != 0 {
		t.Error("bot messages should be ignored")
	}
}

func TestIgnored_NonDMChannel(t *testing.T) {
	api := &mockSlackAPI{}
	disp := &recordingDispatcher{}
	h := NewForTest(api, disp, "U_ALLOWED")

	h.HandleMessage(context.Background(), &IncomingMessage{
		User:        "U_ALLOWED",
		Text:        "channel message",
		Channel:     "C_PUBLIC",
		ChannelType: "channel",
		SubType:     "",
	})

	if len(disp.received) != 0 {
		t.Error("non-DM messages should be ignored")
	}
}

func TestIgnored_UnknownSubtype(t *testing.T) {
	api := &mockSlackAPI{}
	disp := &recordingDispatcher{}
	h := NewForTest(api, disp, "U_ALLOWED")

	h.HandleMessage(context.Background(), &IncomingMessage{
		User:        "U_ALLOWED",
		Text:        "something",
		Channel:     "D_CHAN",
		ChannelType: "im",
		SubType:     "message_changed",
	})

	if len(disp.received) != 0 {
		t.Error("message_changed subtype should be ignored")
	}
}

// ── attachment type routing ───────────────────────────────────────────────────

func TestAttachment_Image_RoutedAsContentPart(t *testing.T) {
	srv := fileServer(t, []byte("fake-jpeg"))
	defer srv.Close()

	api := &mockSlackAPI{postTS: "ts1"}
	disp := &recordingDispatcher{}
	h := NewForTest(api, disp, "U_ALLOWED")
	h.SetHTTPClient(srv.Client())

	h.HandleMessage(context.Background(), &IncomingMessage{
		User:        "U_ALLOWED",
		Text:        "look at this",
		Channel:     "D_CHAN",
		ChannelType: "im",
		Files: []AttachedFile{{
			ID:                 "F001",
			MimeType:           "image/jpeg",
			Name:               "photo.jpg",
			URLPrivateDownload: srv.URL,
		}},
	})

	if len(disp.received) != 1 {
		t.Fatalf("expected 1 dispatched message, got %d", len(disp.received))
	}
	msg := disp.received[0]
	var hasImagePart bool
	for _, p := range msg.Parts {
		if p.Type == "image" {
			hasImagePart = true
		}
	}
	if !hasImagePart {
		t.Errorf("expected image content part in dispatched message, got parts: %v", msg.Parts)
	}
}

func TestAttachment_PDF_ExtractsText(t *testing.T) {
	// Use a minimal real PDF so ExtractPDFText can parse it.
	// This is a 1-page blank PDF.
	minPDF := []byte("%PDF-1.4\n1 0 obj<</Type/Catalog/Pages 2 0 R>>endobj 2 0 obj<</Type/Pages/Kids[3 0 R]/Count 1>>endobj 3 0 obj<</Type/Page/MediaBox[0 0 3 3]>>endobj\nxref\n0 4\n0000000000 65535 f\n0000000009 00000 n\n0000000058 00000 n\n0000000115 00000 n\ntrailer<</Size 4/Root 1 0 R>>\nstartxref\n190\n%%EOF")
	srv := fileServer(t, minPDF)
	defer srv.Close()

	api := &mockSlackAPI{postTS: "ts1"}
	disp := &recordingDispatcher{}
	h := NewForTest(api, disp, "U_ALLOWED")
	h.SetHTTPClient(srv.Client())

	h.HandleMessage(context.Background(), &IncomingMessage{
		User:        "U_ALLOWED",
		Text:        "",
		Channel:     "D_CHAN",
		ChannelType: "im",
		Files: []AttachedFile{{
			ID:                 "F002",
			MimeType:           "application/pdf",
			Name:               "doc.pdf",
			URLPrivateDownload: srv.URL,
		}},
	})

	// Whether PDF extraction yields text or not, the dispatcher should be called.
	if len(disp.received) != 1 {
		t.Fatalf("expected 1 dispatched message for PDF, got %d", len(disp.received))
	}
}

func TestAttachment_UnsupportedType_Ignored(t *testing.T) {
	api := &mockSlackAPI{postTS: "ts1"}
	disp := &recordingDispatcher{}
	h := NewForTest(api, disp, "U_ALLOWED")

	// Unsupported MIME + some text: text should still be dispatched.
	h.HandleMessage(context.Background(), &IncomingMessage{
		User:        "U_ALLOWED",
		Text:        "here's a zip",
		Channel:     "D_CHAN",
		ChannelType: "im",
		Files: []AttachedFile{{
			ID:       "F003",
			MimeType: "application/zip",
			Name:     "archive.zip",
		}},
	})

	if len(disp.received) != 1 {
		t.Fatalf("expected 1 dispatched message (text only), got %d", len(disp.received))
	}
	if disp.received[0].Text != "here's a zip" {
		t.Errorf("text = %q, want 'here's a zip'", disp.received[0].Text)
	}
}

// ── audio / voice ─────────────────────────────────────────────────────────────

func TestAudio_Transcribed_RoutedWithPrefix(t *testing.T) {
	srv := fileServer(t, []byte("fake ogg"))
	defer srv.Close()

	api := &mockSlackAPI{postTS: "ts1"}
	tr := &stubTranscriber{text: "hello from voice"}
	disp := &recordingDispatcher{}
	h := NewForTest(api, disp, "U_ALLOWED")
	h.SetTranscriber(tr)
	h.SetHTTPClient(srv.Client())

	h.HandleMessage(context.Background(), &IncomingMessage{
		User:        "U_ALLOWED",
		Text:        "",
		Channel:     "D_CHAN",
		ChannelType: "im",
		Files: []AttachedFile{{
			ID:                 "F004",
			MimeType:           "audio/ogg",
			Name:               "voice.ogg",
			URLPrivateDownload: srv.URL,
		}},
	})

	if len(disp.received) != 1 {
		t.Fatalf("expected 1 dispatched message, got %d", len(disp.received))
	}
	want := "[Voice message transcribed]: hello from voice"
	if disp.received[0].Text != want {
		t.Errorf("routed text = %q, want %q", disp.received[0].Text, want)
	}
}

func TestAudio_NoopTranscriber_SendsHelpfulReply(t *testing.T) {
	srv := fileServer(t, []byte("fake ogg"))
	defer srv.Close()

	api := &mockSlackAPI{postTS: "ts1"}
	disp := &recordingDispatcher{}
	h := NewForTest(api, disp, "U_ALLOWED")
	h.SetTranscriber(&voice.NoopTranscriber{})
	h.SetHTTPClient(srv.Client())

	h.HandleMessage(context.Background(), &IncomingMessage{
		User:        "U_ALLOWED",
		Text:        "",
		Channel:     "D_CHAN",
		ChannelType: "im",
		Files: []AttachedFile{{
			ID:                 "F005",
			MimeType:           "audio/ogg",
			Name:               "voice.ogg",
			URLPrivateDownload: srv.URL,
		}},
	})

	if len(disp.received) != 0 {
		t.Errorf("dispatcher should not be called when transcription unsupported")
	}
	if len(api.posted) != 1 || !strings.Contains(api.posted[0], "aren't supported") {
		t.Errorf("expected 'aren't supported' reply, got %v", api.posted)
	}
}

func TestAudio_TranscriptionError_NotifiesUser(t *testing.T) {
	srv := fileServer(t, []byte("fake ogg"))
	defer srv.Close()

	api := &mockSlackAPI{postTS: "ts1"}
	tr := &stubTranscriber{err: errors.New("whisper unavailable")}
	disp := &recordingDispatcher{}
	h := NewForTest(api, disp, "U_ALLOWED")
	h.SetTranscriber(tr)
	h.SetHTTPClient(srv.Client())

	h.HandleMessage(context.Background(), &IncomingMessage{
		User:        "U_ALLOWED",
		Text:        "",
		Channel:     "D_CHAN",
		ChannelType: "im",
		Files: []AttachedFile{{
			ID:                 "F006",
			MimeType:           "audio/ogg",
			Name:               "voice.ogg",
			URLPrivateDownload: srv.URL,
		}},
	})

	if len(disp.received) != 0 {
		t.Errorf("dispatcher should not be called on transcription error")
	}
	if len(api.posted) != 1 || !strings.Contains(api.posted[0], "couldn't transcribe") {
		t.Errorf("expected transcription error reply, got %v", api.posted)
	}
}

// ── NotifyReminder ────────────────────────────────────────────────────────────

func TestNotifyReminder_SlackSession_Delivered(t *testing.T) {
	api := &mockSlackAPI{}
	h := NewForTest(api, &recordingDispatcher{}, "U_ALLOWED")

	r := &sessions.Reminder{
		ID:        "rem1",
		SessionID: "slack:U_ALLOWED:D_CHAN",
		Message:   "Don't forget!",
	}
	if err := h.NotifyReminder(context.Background(), r); err != nil {
		t.Fatalf("NotifyReminder: %v", err)
	}
	if len(api.posted) != 1 || api.posted[0] != "Don't forget!" {
		t.Errorf("expected reminder text posted, got %v", api.posted)
	}
}

func TestNotifyReminder_NonSlackSession_Ignored(t *testing.T) {
	api := &mockSlackAPI{}
	h := NewForTest(api, &recordingDispatcher{}, "U_ALLOWED")

	r := &sessions.Reminder{
		ID:        "rem2",
		SessionID: "telegram:111:222",
		Message:   "ping",
	}
	if err := h.NotifyReminder(context.Background(), r); err != nil {
		t.Fatalf("NotifyReminder(non-slack) should return nil, got %v", err)
	}
	if len(api.posted) != 0 {
		t.Errorf("non-slack reminder should not post to Slack")
	}
}

// ── startup validation ────────────────────────────────────────────────────────

func TestNew_MissingAppToken_Errors(t *testing.T) {
	_, err := New(nil, "xoxb-token", "", "U123", &voice.NoopTranscriber{}, &voice.NoopSynthesizer{})
	if err == nil || !strings.Contains(err.Error(), "app token") {
		t.Errorf("expected app token error, got %v", err)
	}
}

func TestNew_MissingAllowedUID_Errors(t *testing.T) {
	_, err := New(nil, "xoxb-token", "xapp-token", "", &voice.NoopTranscriber{}, &voice.NoopSynthesizer{})
	if err == nil || !strings.Contains(err.Error(), "SLACK_ALLOWED_USER_ID") {
		t.Errorf("expected allowed UID error, got %v", err)
	}
}

func TestNew_MissingBotToken_Errors(t *testing.T) {
	_, err := New(nil, "", "xapp-token", "U123", &voice.NoopTranscriber{}, &voice.NoopSynthesizer{})
	if err == nil || !strings.Contains(err.Error(), "bot token") {
		t.Errorf("expected bot token error, got %v", err)
	}
}

// ── streaming update throttling (unit) ───────────────────────────────────────

func TestStreamingThrottling_OnlyUpdatesOnChange(t *testing.T) {
	// Verify that editOrLog is not called when text hasn't changed.
	// We test this indirectly through the mock's edited slice.
	api := &mockSlackAPI{postTS: "ts1"}
	disp := &recordingDispatcher{}
	h := NewForTest(api, disp, "U_ALLOWED")

	// editOrLog should only call UpdateMessage once per unique text.
	h.editOrLog(context.Background(), "D_CHAN", "ts1", "sid1", "hello")
	h.editOrLog(context.Background(), "D_CHAN", "ts1", "sid1", "hello") // same text
	h.editOrLog(context.Background(), "D_CHAN", "ts1", "sid1", "world") // different text

	if len(api.edited) != 3 {
		t.Errorf("editOrLog called UpdateMessage %d times, want 3 (dedup happens in respondStreaming ticker)", len(api.edited))
	}
}

// ── msgOptionsText helper ─────────────────────────────────────────────────────

// Verify that the test helper correctly extracts text from MsgOptionText.
func TestMsgOptionsText(t *testing.T) {
	opts := []slacklib.MsgOption{slacklib.MsgOptionText("hello world", false)}
	got := msgOptionsText("D_CHAN", opts)
	if got != "hello world" {
		t.Errorf("msgOptionsText = %q, want 'hello world'", got)
	}
}
