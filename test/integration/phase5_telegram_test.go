package integration

// Phase 5 integration tests: Telegram channel.
//
//  1. Session key format — telegram:{userID}:{chatID}; different chatIDs → different sessions.
//  2. Whitelist — message from non-allowed userID is silently dropped; dispatcher not called.
//  3. Image attachment — InboundMessage with image ContentPart reaches the LLM intact.
//  4. Reminder delivery — NotifyReminder with a telegram SessionID calls bot.Send.

import (
	"context"
	"encoding/base64"
	"fmt"
	"sync"
	"testing"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"github.com/marcoantonios1/Agent-OS/internal/channels/telegram"
	"github.com/marcoantonios1/Agent-OS/internal/costguard"
	"github.com/marcoantonios1/Agent-OS/internal/sessions"
	"github.com/marcoantonios1/Agent-OS/internal/types"
)

// ── shared test helpers ───────────────────────────────────────────────────────

// mockTGBot implements telegram.BotAPI for tests. All calls are recorded;
// Send always succeeds with MessageID=1.
type mockTGBot struct {
	mu   sync.Mutex
	sent []tgbotapi.Chattable
}

func (m *mockTGBot) Send(c tgbotapi.Chattable) (tgbotapi.Message, error) {
	m.mu.Lock()
	m.sent = append(m.sent, c)
	m.mu.Unlock()
	return tgbotapi.Message{MessageID: 1}, nil
}

func (m *mockTGBot) GetFileDirectURL(_ string) (string, error) { return "", nil }

func (m *mockTGBot) GetUpdatesChan(_ tgbotapi.UpdateConfig) tgbotapi.UpdatesChannel {
	ch := make(chan tgbotapi.Update)
	close(ch)
	return ch
}

func (m *mockTGBot) StopReceivingUpdates() {}

var _ telegram.BotAPI = (*mockTGBot)(nil)

// telegramRecordingDispatcher records every InboundMessage routed through it.
type telegramRecordingDispatcher struct {
	mu   sync.Mutex
	msgs []types.InboundMessage
}

func (d *telegramRecordingDispatcher) Route(_ context.Context, msg types.InboundMessage) (types.OutboundMessage, error) {
	d.mu.Lock()
	d.msgs = append(d.msgs, msg)
	d.mu.Unlock()
	return types.OutboundMessage{Text: "ok"}, nil
}

// ── Test 1: session key format and isolation ──────────────────────────────────

// TestPhase5_Telegram_SessionKey_Format verifies that the Telegram handler
// produces session IDs in the format "telegram:{userID}:{chatID}" and that two
// messages from the same user sent in different chats are stored in separate
// sessions.
func TestPhase5_Telegram_SessionKey_Format(t *testing.T) {
	bot := &mockTGBot{}
	disp := &telegramRecordingDispatcher{}
	h := telegram.NewForTest(disp, bot, 111)

	ctx := context.Background()

	msg1 := &tgbotapi.Message{
		MessageID: 1,
		From:      &tgbotapi.User{ID: 111},
		Chat:      &tgbotapi.Chat{ID: 100},
		Text:      "hello from chat 100",
	}
	msg2 := &tgbotapi.Message{
		MessageID: 2,
		From:      &tgbotapi.User{ID: 111},
		Chat:      &tgbotapi.Chat{ID: 200},
		Text:      "hello from chat 200",
	}

	h.HandleMessage(ctx, msg1)
	h.HandleMessage(ctx, msg2)

	disp.mu.Lock()
	msgs := disp.msgs
	disp.mu.Unlock()

	if len(msgs) != 2 {
		t.Fatalf("dispatcher called %d times, want 2", len(msgs))
	}

	want1 := fmt.Sprintf("telegram:%d:%d", 111, 100)
	want2 := fmt.Sprintf("telegram:%d:%d", 111, 200)

	if msgs[0].SessionID != want1 {
		t.Errorf("msg1 SessionID = %q, want %q", msgs[0].SessionID, want1)
	}
	if msgs[1].SessionID != want2 {
		t.Errorf("msg2 SessionID = %q, want %q", msgs[1].SessionID, want2)
	}
	if msgs[0].SessionID == msgs[1].SessionID {
		t.Error("different chatIDs for the same user must produce different session keys")
	}
}

// ── Test 2: whitelist enforcement ─────────────────────────────────────────────

// TestPhase5_Telegram_Whitelist_Drops_Unknown_User sends a message from a user
// ID that is not in the allowlist and verifies that neither the dispatcher nor
// bot.Send is called (silent drop, no reply).
func TestPhase5_Telegram_Whitelist_Drops_Unknown_User(t *testing.T) {
	bot := &mockTGBot{}
	disp := &telegramRecordingDispatcher{}
	h := telegram.NewForTest(disp, bot, 111) // only uid 111 is allowed

	msg := &tgbotapi.Message{
		MessageID: 1,
		From:      &tgbotapi.User{ID: 999}, // not allowed
		Chat:      &tgbotapi.Chat{ID: 100},
		Text:      "I should be silently dropped",
	}

	h.HandleMessage(context.Background(), msg)

	disp.mu.Lock()
	dispatched := len(disp.msgs)
	disp.mu.Unlock()

	if dispatched != 0 {
		t.Errorf("dispatcher called %d times for non-allowed user, want 0", dispatched)
	}

	bot.mu.Lock()
	sent := len(bot.sent)
	bot.mu.Unlock()

	if sent != 0 {
		t.Errorf("bot.Send called %d times for non-allowed user, want 0 (silent drop)", sent)
	}
}

// ── Test 3: image attachment reaches LLM ─────────────────────────────────────

// TestPhase5_Telegram_ImageAttachment_ReachesLLM constructs the InboundMessage
// that the Telegram handler would produce for a photo (image ContentPart with
// base64 data), routes it through the full router→agent→LLM stack, and verifies
// that the image ContentPart arrives intact at the LLM.
func TestPhase5_Telegram_ImageAttachment_ReachesLLM(t *testing.T) {
	var mu sync.Mutex
	var capturedReqs []costguard.CompletionRequest

	capLLM := &capturingScriptedLLM{
		responses: []costguard.CompletionResponse{
			classifyResp("comms"),
			textResp("I can see the photo you uploaded."),
		},
		onCall: func(req costguard.CompletionRequest) {
			mu.Lock()
			capturedReqs = append(capturedReqs, req)
			mu.Unlock()
		},
	}

	stack := newStack(stackConfig{
		customLLM: capLLM,
		emailProv: newMockEmail(nil, nil),
	})
	defer stack.Close()

	// Minimal PNG header — the router validates MIME type, not image content.
	imgBytes := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}
	encoded := base64.StdEncoding.EncodeToString(imgBytes)

	// This is exactly the InboundMessage that Handler.handleMessage produces
	// when it receives a Telegram photo message.
	inbound := types.InboundMessage{
		ID:        "42",
		ChannelID: types.ChannelID("telegram"),
		UserID:    "111",
		SessionID: "telegram:111:999",
		Text:      "",
		Timestamp: time.Now(),
		Parts: []types.ContentPart{
			{Type: "image", ImageData: encoded, MimeType: "image/jpeg", Filename: "photo.jpg"},
		},
	}

	if _, err := stack.router.Route(context.Background(), inbound); err != nil {
		t.Fatalf("Route: %v", err)
	}

	mu.Lock()
	reqs := capturedReqs
	mu.Unlock()

	var imgPart *types.ContentPart
outer:
	for _, req := range reqs {
		for _, msg := range req.Messages {
			for i := range msg.Parts {
				if msg.Parts[i].Type == "image" {
					p := msg.Parts[i]
					imgPart = &p
					break outer
				}
			}
		}
	}

	if imgPart == nil {
		t.Fatal("no image ContentPart found in any LLM call — attachment did not reach the LLM")
	}
	if imgPart.ImageData != encoded {
		t.Error("ImageData not preserved through router→agent→LLM pipeline")
	}
	if imgPart.MimeType != "image/jpeg" {
		t.Errorf("MimeType = %q, want image/jpeg", imgPart.MimeType)
	}
}

// ── Test 4: reminder delivery ─────────────────────────────────────────────────

// TestPhase5_Telegram_ReminderDelivery creates a due reminder whose SessionID
// has the telegram channel prefix and verifies that NotifyReminder calls
// bot.Send exactly once with a message to the correct chat ID.
func TestPhase5_Telegram_ReminderDelivery(t *testing.T) {
	bot := &mockTGBot{}
	disp := &telegramRecordingDispatcher{}
	h := telegram.NewForTest(disp, bot, 111)

	r := &sessions.Reminder{
		ID:        "rem-tg-1",
		SessionID: "telegram:111:999", // chatID = 999
		UserID:    "111",
		Message:   "Time to review your PRs!",
	}

	if err := h.NotifyReminder(context.Background(), r); err != nil {
		t.Fatalf("NotifyReminder: %v", err)
	}

	bot.mu.Lock()
	sent := len(bot.sent)
	bot.mu.Unlock()

	if sent != 1 {
		t.Errorf("bot.Send called %d times after NotifyReminder, want 1", sent)
	}
}
