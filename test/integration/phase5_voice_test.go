package integration

// Phase 5 voice pipeline integration tests.
//
// These tests exercise the full voice path through the Telegram channel handler:
//  1. Transcription_RoutesAsText   — transcribed text reaches the dispatcher with the correct prefix
//  2. Noop_SendsHelpfulReply       — NoopTranscriber triggers the "not supported" reply
//  3. TranscriptionError_Notifies  — transcription error sends a graceful error message
//  4. TTS_SynthesizesResponse      — synthesizer output is sent as a Telegram voice note

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"github.com/marcoantonios1/Agent-OS/internal/channels/telegram"
	"github.com/marcoantonios1/Agent-OS/internal/types"
	"github.com/marcoantonios1/Agent-OS/internal/voice"
)

// ── voice-specific mocks ──────────────────────────────────────────────────────

// voiceBot is a BotAPI whose GetFileDirectURL returns a caller-supplied URL,
// allowing tests to serve fake audio from a local httptest.Server.
type voiceBot struct {
	mu      sync.Mutex
	sent    []tgbotapi.Chattable
	fileURL string
}

func (b *voiceBot) Send(c tgbotapi.Chattable) (tgbotapi.Message, error) {
	b.mu.Lock()
	b.sent = append(b.sent, c)
	b.mu.Unlock()
	return tgbotapi.Message{MessageID: 1}, nil
}

func (b *voiceBot) GetFileDirectURL(_ string) (string, error) { return b.fileURL, nil }

func (b *voiceBot) GetUpdatesChan(_ tgbotapi.UpdateConfig) tgbotapi.UpdatesChannel {
	ch := make(chan tgbotapi.Update)
	close(ch)
	return ch
}

func (b *voiceBot) StopReceivingUpdates() {}

var _ telegram.BotAPI = (*voiceBot)(nil)

// fixedTranscriber ignores audio bytes and always returns the configured text/error.
type fixedTranscriber struct {
	text string
	err  error
}

func (t *fixedTranscriber) Transcribe(_ context.Context, _ []byte, _ string) (string, error) {
	return t.text, t.err
}

// fixedSynthesizer always returns the configured audio bytes/error.
type fixedSynthesizer struct {
	data []byte
	mime string
	err  error
}

func (s *fixedSynthesizer) Synthesize(_ context.Context, _ string) ([]byte, string, error) {
	return s.data, s.mime, s.err
}

// voiceRecordingDispatcher records every InboundMessage and returns a fixed reply.
type voiceRecordingDispatcher struct {
	mu    sync.Mutex
	msgs  []types.InboundMessage
	reply string
}

func (d *voiceRecordingDispatcher) Route(_ context.Context, msg types.InboundMessage) (types.OutboundMessage, error) {
	d.mu.Lock()
	d.msgs = append(d.msgs, msg)
	reply := d.reply
	d.mu.Unlock()
	if reply == "" {
		reply = "ok"
	}
	return types.OutboundMessage{Text: reply}, nil
}

// audioServer spins up an httptest.Server that serves dummy OGG audio bytes.
// The returned server must be closed by the caller.
func audioServer() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "audio/ogg")
		w.Write([]byte("fake ogg audio bytes")) //nolint:errcheck
	}))
}

// voiceMsg builds a *tgbotapi.Message carrying a Voice attachment.
func voiceMsg(userID, chatID int64, fileID string) *tgbotapi.Message {
	return &tgbotapi.Message{
		MessageID: 1,
		From:      &tgbotapi.User{ID: userID},
		Chat:      &tgbotapi.Chat{ID: chatID},
		Voice:     &tgbotapi.Voice{FileID: fileID, MimeType: "audio/ogg"},
	}
}

// ── Test 1 ────────────────────────────────────────────────────────────────────

// TestPhase5_Voice_Transcription_RoutesAsText verifies that a voice message
// whose transcription succeeds is forwarded to the dispatcher with the
// "[Voice message transcribed]: " prefix intact.
func TestPhase5_Voice_Transcription_RoutesAsText(t *testing.T) {
	srv := audioServer()
	defer srv.Close()

	bot := &voiceBot{fileURL: srv.URL + "/voice.ogg"}
	disp := &voiceRecordingDispatcher{}
	tr := &fixedTranscriber{text: "call the dentist"}

	h := telegram.NewForTest(disp, bot, 111)
	h.SetTranscriber(tr)
	h.SetHTTPClient(srv.Client())

	h.HandleMessage(context.Background(), voiceMsg(111, 100, "file-abc"))

	disp.mu.Lock()
	msgs := disp.msgs
	disp.mu.Unlock()

	if len(msgs) != 1 {
		t.Fatalf("dispatcher called %d times, want 1", len(msgs))
	}
	const want = "[Voice message transcribed]: call the dentist"
	if msgs[0].Text != want {
		t.Errorf("routed text = %q, want %q", msgs[0].Text, want)
	}
}

// ── Test 2 ────────────────────────────────────────────────────────────────────

// TestPhase5_Voice_Noop_SendsHelpfulReply verifies that a NoopTranscriber
// (ErrNotSupported) causes the handler to send a helpful "not supported" reply
// to the user without ever calling the dispatcher.
func TestPhase5_Voice_Noop_SendsHelpfulReply(t *testing.T) {
	srv := audioServer()
	defer srv.Close()

	bot := &voiceBot{fileURL: srv.URL + "/voice.ogg"}
	disp := &voiceRecordingDispatcher{}

	h := telegram.NewForTest(disp, bot, 111)
	// Default transcriber is already NoopTranscriber; be explicit for clarity.
	h.SetTranscriber(&voice.NoopTranscriber{})
	h.SetHTTPClient(srv.Client())

	h.HandleMessage(context.Background(), voiceMsg(111, 100, "file-abc"))

	disp.mu.Lock()
	dispatched := len(disp.msgs)
	disp.mu.Unlock()

	if dispatched != 0 {
		t.Errorf("dispatcher called %d times for unsupported voice, want 0", dispatched)
	}

	bot.mu.Lock()
	sent := bot.sent
	bot.mu.Unlock()

	if len(sent) != 1 {
		t.Fatalf("bot.Send called %d times, want 1", len(sent))
	}
	msg, ok := sent[0].(tgbotapi.MessageConfig)
	if !ok {
		t.Fatalf("expected MessageConfig, got %T", sent[0])
	}
	if !strings.Contains(msg.Text, "aren't supported") {
		t.Errorf("reply %q should tell user voice messages aren't supported", msg.Text)
	}
}

// ── Test 3 ────────────────────────────────────────────────────────────────────

// TestPhase5_Voice_TranscriptionError_NotifiesUser verifies that a transcription
// error (not ErrNotSupported) causes the handler to send a graceful error reply
// without panicking or calling the dispatcher.
func TestPhase5_Voice_TranscriptionError_NotifiesUser(t *testing.T) {
	srv := audioServer()
	defer srv.Close()

	bot := &voiceBot{fileURL: srv.URL + "/voice.ogg"}
	disp := &voiceRecordingDispatcher{}
	tr := &fixedTranscriber{err: errors.New("whisper unavailable")}

	h := telegram.NewForTest(disp, bot, 111)
	h.SetTranscriber(tr)
	h.SetHTTPClient(srv.Client())

	// Must not panic.
	h.HandleMessage(context.Background(), voiceMsg(111, 100, "file-abc"))

	disp.mu.Lock()
	dispatched := len(disp.msgs)
	disp.mu.Unlock()

	if dispatched != 0 {
		t.Errorf("dispatcher called %d times on transcription error, want 0", dispatched)
	}

	bot.mu.Lock()
	sent := bot.sent
	bot.mu.Unlock()

	if len(sent) != 1 {
		t.Fatalf("bot.Send called %d times, want 1", len(sent))
	}
	msg, ok := sent[0].(tgbotapi.MessageConfig)
	if !ok {
		t.Fatalf("expected MessageConfig, got %T", sent[0])
	}
	if !strings.Contains(msg.Text, "couldn't transcribe") {
		t.Errorf("reply %q should mention transcription failure", msg.Text)
	}
}

// ── Test 4 ────────────────────────────────────────────────────────────────────

// TestPhase5_Voice_TTS_SynthesizesResponse verifies that when TTS is configured
// and the inbound message was a voice note, the handler sends the synthesized
// audio back as a Telegram voice note (tgbotapi.VoiceConfig) rather than text.
func TestPhase5_Voice_TTS_SynthesizesResponse(t *testing.T) {
	srv := audioServer()
	defer srv.Close()

	bot := &voiceBot{fileURL: srv.URL + "/voice.ogg"}
	disp := &voiceRecordingDispatcher{reply: "Sure, I'll book the dentist for you."}
	tr := &fixedTranscriber{text: "call the dentist"}
	synth := &fixedSynthesizer{
		data: []byte("synthesized ogg audio"),
		mime: "audio/ogg",
	}

	h := telegram.NewForTest(disp, bot, 111)
	h.SetTranscriber(tr)
	h.SetSynthesizer(synth)
	h.SetHTTPClient(srv.Client())

	h.HandleMessage(context.Background(), voiceMsg(111, 100, "file-abc"))

	bot.mu.Lock()
	sent := bot.sent
	bot.mu.Unlock()

	// Exactly one message should be sent: the audio response.
	if len(sent) != 1 {
		t.Fatalf("bot.Send called %d times, want 1", len(sent))
	}
	if _, ok := sent[0].(tgbotapi.VoiceConfig); !ok {
		t.Errorf("expected VoiceConfig (audio response), got %T", sent[0])
	}
}
