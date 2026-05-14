package whatsapp

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	watypes "go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	agenttypes "github.com/marcoantonios1/Agent-OS/internal/types"
	"github.com/marcoantonios1/Agent-OS/internal/voice"
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

// ── test helpers ──────────────────────────────────────────────────────────────

// stubDownloader satisfies mediaDownloader without a real WhatsApp connection.
type stubDownloader struct {
	data []byte
	err  error
}

func (s *stubDownloader) DownloadAny(_ context.Context, _ *waE2E.Message) ([]byte, error) {
	return s.data, s.err
}

// newTestHandler builds a Handler with no real client; safe for unit tests that
// only exercise helpers (processMedia, buildMsgParts, etc.).
func newTestHandler(d mediaDownloader) *Handler {
	return &Handler{
		downloader: d,
		allowedJID: "test@s.whatsapp.net",
		log:        nil, // logger() falls back to slog.Default()
	}
}

// buildTestPDF returns a minimal valid single-page PDF whose text layer
// contains the given string. Uses the same xref-offset logic as the
// attachments package tests so ledongthuc/pdf can parse it.
func buildTestPDF(t *testing.T, text string) []byte {
	t.Helper()
	stream := fmt.Sprintf("BT /F1 12 Tf 72 720 Td (%s) Tj ET\n", text)

	var buf bytes.Buffer
	w := func(s string) { buf.WriteString(s) }

	w("%PDF-1.4\n")

	offsets := make([]int, 0, 5)

	// 1: Catalog
	offsets = append(offsets, buf.Len())
	w("1 0 obj\n<</Type /Catalog /Pages 2 0 R>>\nendobj\n")

	// 2: Pages
	offsets = append(offsets, buf.Len())
	w("2 0 obj\n<</Type /Pages /Kids [3 0 R] /Count 1>>\nendobj\n")

	// 3: Page
	offsets = append(offsets, buf.Len())
	w("3 0 obj\n<</Type /Page /Parent 2 0 R /MediaBox [0 0 612 792]" +
		" /Contents 4 0 R /Resources <</Font <</F1 5 0 R>>>>>>\nendobj\n")

	// 4: Content stream
	offsets = append(offsets, buf.Len())
	w(fmt.Sprintf("4 0 obj\n<</Length %d>>\nstream\n%sendstream\nendobj\n", len(stream), stream))

	// 5: Font
	offsets = append(offsets, buf.Len())
	w("5 0 obj\n<</Type /Font /Subtype /Type1 /BaseFont /Helvetica>>\nendobj\n")

	xrefPos := buf.Len()
	nObjs := len(offsets) + 1
	w(fmt.Sprintf("xref\n0 %d\n", nObjs))
	w("0000000000 65535 f \n")
	for _, off := range offsets {
		w(fmt.Sprintf("%010d 00000 n \n", off))
	}
	w(fmt.Sprintf("trailer\n<</Size %d /Root 1 0 R>>\nstartxref\n%d\n", nObjs, xrefPos))
	w("%%EOF\n")

	return buf.Bytes()
}

// ── processMedia ──────────────────────────────────────────────────────────────

func TestProcessMedia_Nil(t *testing.T) {
	h := newTestHandler(&stubDownloader{})
	parts, err := h.processMedia(context.Background(), nil)
	if err != nil || parts != nil {
		t.Errorf("expected (nil, nil), got parts=%v err=%v", parts, err)
	}
}

func TestProcessMedia_TextOnly(t *testing.T) {
	h := newTestHandler(&stubDownloader{})
	msg := &waE2E.Message{Conversation: strPtr("Hello")}
	parts, err := h.processMedia(context.Background(), msg)
	if err != nil || parts != nil {
		t.Errorf("text-only message should return (nil, nil), got parts=%v err=%v", parts, err)
	}
}

func TestProcessMedia_ImageMessage(t *testing.T) {
	imgData := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10} // JPEG magic bytes
	h := newTestHandler(&stubDownloader{data: imgData})
	msg := &waE2E.Message{
		ImageMessage: &waE2E.ImageMessage{Mimetype: strPtr("image/jpeg")},
	}
	parts, err := h.processMedia(context.Background(), msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(parts) != 1 {
		t.Fatalf("expected 1 part, got %d", len(parts))
	}
	p := parts[0]
	if p.Type != "image" {
		t.Errorf("Type = %q, want %q", p.Type, "image")
	}
	if p.MimeType != "image/jpeg" {
		t.Errorf("MimeType = %q, want %q", p.MimeType, "image/jpeg")
	}
	if want := base64.StdEncoding.EncodeToString(imgData); p.ImageData != want {
		t.Errorf("ImageData mismatch")
	}
}

func TestProcessMedia_ImageMessage_DefaultMime(t *testing.T) {
	h := newTestHandler(&stubDownloader{data: []byte("img")})
	msg := &waE2E.Message{
		ImageMessage: &waE2E.ImageMessage{}, // no Mimetype field set
	}
	parts, err := h.processMedia(context.Background(), msg)
	if err != nil || len(parts) != 1 {
		t.Fatalf("expected 1 part, got parts=%v err=%v", parts, err)
	}
	if parts[0].MimeType != "image/jpeg" {
		t.Errorf("default MimeType = %q, want %q", parts[0].MimeType, "image/jpeg")
	}
}

func TestProcessMedia_ImageMessage_DownloadError(t *testing.T) {
	h := newTestHandler(&stubDownloader{err: errors.New("network error")})
	msg := &waE2E.Message{
		ImageMessage: &waE2E.ImageMessage{Mimetype: strPtr("image/png")},
	}
	parts, err := h.processMedia(context.Background(), msg)
	if err != nil {
		t.Errorf("download error should be swallowed, got err=%v", err)
	}
	if parts != nil {
		t.Errorf("expected nil parts on download failure, got %v", parts)
	}
}

func TestProcessMedia_PDFDocument(t *testing.T) {
	pdfBytes := buildTestPDF(t, "Invoice total")
	h := newTestHandler(&stubDownloader{data: pdfBytes})
	msg := &waE2E.Message{
		DocumentMessage: &waE2E.DocumentMessage{
			Mimetype: strPtr("application/pdf"),
			FileName: strPtr("invoice.pdf"),
		},
	}
	parts, err := h.processMedia(context.Background(), msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(parts) != 1 {
		t.Fatalf("expected 1 part, got %d", len(parts))
	}
	p := parts[0]
	if p.Type != "text" {
		t.Errorf("Type = %q, want %q", p.Type, "text")
	}
	if !strings.Contains(p.Text, "Invoice total") {
		t.Errorf("extracted text %q does not contain expected content", p.Text)
	}
	if p.Filename != "invoice.pdf" {
		t.Errorf("Filename = %q, want %q", p.Filename, "invoice.pdf")
	}
}

func TestProcessMedia_PDFDocument_DefaultFilename(t *testing.T) {
	pdfBytes := buildTestPDF(t, "content")
	h := newTestHandler(&stubDownloader{data: pdfBytes})
	msg := &waE2E.Message{
		DocumentMessage: &waE2E.DocumentMessage{
			Mimetype: strPtr("application/pdf"),
			// FileName not set
		},
	}
	parts, err := h.processMedia(context.Background(), msg)
	if err != nil || len(parts) != 1 {
		t.Fatalf("expected 1 part, got parts=%v err=%v", parts, err)
	}
	if parts[0].Filename != "document.pdf" {
		t.Errorf("Filename = %q, want %q", parts[0].Filename, "document.pdf")
	}
}

func TestProcessMedia_UnsupportedDocument(t *testing.T) {
	h := newTestHandler(&stubDownloader{})
	msg := &waE2E.Message{
		DocumentMessage: &waE2E.DocumentMessage{
			Mimetype: strPtr("application/zip"),
			FileName: strPtr("archive.zip"),
		},
	}
	_, err := h.processMedia(context.Background(), msg)
	if !errors.Is(err, errUnsupportedMediaType) {
		t.Errorf("expected errUnsupportedMediaType, got %v", err)
	}
}

// ── buildMsgParts ─────────────────────────────────────────────────────────────

func TestBuildMsgParts_NoAttachments(t *testing.T) {
	if got := buildMsgParts("hello", nil); got != nil {
		t.Errorf("expected nil when no attachments, got %v", got)
	}
}

func TestBuildMsgParts_TextAndImage(t *testing.T) {
	att := []agenttypes.ContentPart{{Type: "image", ImageData: "abc", MimeType: "image/png"}}
	parts := buildMsgParts("describe this", att)
	if len(parts) != 2 {
		t.Fatalf("expected 2 parts, got %d", len(parts))
	}
	if parts[0].Type != "text" || parts[0].Text != "describe this" {
		t.Errorf("first part should be text, got %+v", parts[0])
	}
	if parts[1].Type != "image" {
		t.Errorf("second part should be image, got %+v", parts[1])
	}
}

func TestBuildMsgParts_ImageOnlyNoText(t *testing.T) {
	att := []agenttypes.ContentPart{{Type: "image", ImageData: "abc", MimeType: "image/png"}}
	parts := buildMsgParts("", att)
	if len(parts) != 1 {
		t.Fatalf("expected 1 part (image only), got %d", len(parts))
	}
	if parts[0].Type != "image" {
		t.Errorf("expected image part, got %+v", parts[0])
	}
}

// ── voice test helpers ────────────────────────────────────────────────────────

// stubSender satisfies msgSender and records the text of every sent message.
type stubSender struct {
	mu   sync.Mutex
	sent []string
	err  error
}

func (s *stubSender) SendMessage(_ context.Context, _ watypes.JID, msg *waE2E.Message, _ ...whatsmeow.SendRequestExtra) (whatsmeow.SendResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if msg.Conversation != nil {
		s.sent = append(s.sent, *msg.Conversation)
	}
	return whatsmeow.SendResponse{}, s.err
}

// stubTranscriber is a controllable voice.Transcriber.
type stubTranscriber struct {
	text string
	err  error
}

func (t *stubTranscriber) Transcribe(_ context.Context, _ []byte, _ string) (string, error) {
	return t.text, t.err
}

// recordingDispatcher satisfies web.Dispatcher and records every routed message.
type recordingDispatcher struct {
	mu       sync.Mutex
	received []agenttypes.InboundMessage
}

func (d *recordingDispatcher) Route(_ context.Context, msg agenttypes.InboundMessage) (agenttypes.OutboundMessage, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.received = append(d.received, msg)
	return agenttypes.OutboundMessage{Text: "ok"}, nil
}

// makeAudioEvent builds a minimal *events.Message with an AudioMessage payload.
func makeAudioEvent(userJID string, mimeType string) *events.Message {
	jid := watypes.JID{User: userJID, Server: watypes.DefaultUserServer}
	return &events.Message{
		Info: watypes.MessageInfo{
			MessageSource: watypes.MessageSource{
				Chat:     jid,
				Sender:   jid,
				IsFromMe: false,
			},
		},
		Message: &waE2E.Message{
			AudioMessage: &waE2E.AudioMessage{
				Mimetype: strPtr(mimeType),
			},
		},
	}
}

// newVoiceHandler builds a Handler wired for voice tests.
func newVoiceHandler(d mediaDownloader, s *stubSender, tr voice.Transcriber, disp *recordingDispatcher) *Handler {
	return &Handler{
		downloader:  d,
		sender:      s,
		dispatcher:  disp,
		allowedJID:  "96170123456@s.whatsapp.net",
		transcriber: tr,
		log:         slog.Default(),
	}
}

// ── voice tests ───────────────────────────────────────────────────────────────

func TestVoice_TranscribedTextRoutedWithPrefix(t *testing.T) {
	dl := &stubDownloader{data: []byte("fake ogg")}
	sender := &stubSender{}
	tr := &stubTranscriber{text: "hello from voice"}
	disp := &recordingDispatcher{}

	h := newVoiceHandler(dl, sender, tr, disp)
	h.onMessage(makeAudioEvent("96170123456", "audio/ogg"))

	if len(disp.received) != 1 {
		t.Fatalf("expected 1 dispatched message, got %d", len(disp.received))
	}
	want := "[Voice message transcribed]: hello from voice"
	if disp.received[0].Text != want {
		t.Errorf("routed text = %q, want %q", disp.received[0].Text, want)
	}
	// The handler sends the agent's reply back via sender — exactly 1 call expected.
	if len(sender.sent) != 1 {
		t.Errorf("expected 1 reply (agent response), got %d: %v", len(sender.sent), sender.sent)
	}
}

func TestVoice_NoopTranscriber_SendsHelpfulReply(t *testing.T) {
	dl := &stubDownloader{data: []byte("fake ogg")}
	sender := &stubSender{}
	tr := &voice.NoopTranscriber{}
	disp := &recordingDispatcher{}

	h := newVoiceHandler(dl, sender, tr, disp)
	h.onMessage(makeAudioEvent("96170123456", "audio/ogg"))

	if len(disp.received) != 0 {
		t.Errorf("dispatcher should not be called for unsupported voice, got %d messages", len(disp.received))
	}
	if len(sender.sent) != 1 {
		t.Fatalf("expected 1 sent reply, got %d", len(sender.sent))
	}
	if !strings.Contains(sender.sent[0], "aren't supported") {
		t.Errorf("reply %q should tell user voice isn't supported", sender.sent[0])
	}
}

func TestVoice_TranscriptionError_NotifiesUser(t *testing.T) {
	dl := &stubDownloader{data: []byte("fake ogg")}
	sender := &stubSender{}
	tr := &stubTranscriber{err: errors.New("whisper unavailable")}
	disp := &recordingDispatcher{}

	h := newVoiceHandler(dl, sender, tr, disp)
	h.onMessage(makeAudioEvent("96170123456", "audio/ogg"))

	if len(disp.received) != 0 {
		t.Errorf("dispatcher should not be called on transcription error, got %d messages", len(disp.received))
	}
	if len(sender.sent) != 1 {
		t.Fatalf("expected 1 error reply, got %d", len(sender.sent))
	}
	if !strings.Contains(sender.sent[0], "couldn't transcribe") {
		t.Errorf("reply %q should tell user transcription failed", sender.sent[0])
	}
}

func TestVoice_DownloadError_NotifiesUser(t *testing.T) {
	dl := &stubDownloader{err: errors.New("network error")}
	sender := &stubSender{}
	tr := &stubTranscriber{text: "should not reach this"}
	disp := &recordingDispatcher{}

	h := newVoiceHandler(dl, sender, tr, disp)
	h.onMessage(makeAudioEvent("96170123456", "audio/ogg"))

	if len(disp.received) != 0 {
		t.Errorf("dispatcher should not be called on download error, got %d messages", len(disp.received))
	}
	if len(sender.sent) != 1 {
		t.Fatalf("expected 1 error reply, got %d", len(sender.sent))
	}
	if !strings.Contains(sender.sent[0], "couldn't download") {
		t.Errorf("reply %q should mention download failure", sender.sent[0])
	}
}

func TestVoice_NonWhitelistedSender_Dropped(t *testing.T) {
	dl := &stubDownloader{data: []byte("ogg")}
	sender := &stubSender{}
	tr := &stubTranscriber{text: "spy message"}
	disp := &recordingDispatcher{}

	h := newVoiceHandler(dl, sender, tr, disp)
	// Send from a different JID than allowedJID.
	h.onMessage(makeAudioEvent("999999999", "audio/ogg"))

	if len(disp.received) != 0 || len(sender.sent) != 0 {
		t.Error("non-whitelisted sender should be silently dropped")
	}
}
