package integration

// Phase 6 combined integration tests.
//
// These tests verify the full request→dispatch→reply path for three channels
// and the video processing pipeline, using the existing test infrastructure
// (mockSlackClient, fakeBlueBubbles, videoTGBot, etc.) from sibling test files.
//
//  1. TestPhase6_Slack_TextMessage_Routes             — DM → router → LLM → Slack reply
//  2. TestPhase6_Slack_Whitelist_Enforced             — unknown user → no LLM call, no reply
//  3. TestPhase6_iMessage_TextMessage_Routes          — webhook → dispatcher → BlueBubbles reply
//  4. TestPhase6_iMessage_Whitelist_Enforced          — wrong handle → no route, no reply
//  5. TestPhase6_Video_Pipeline_EndToEnd              — video → frames → image ContentParts → LLM
//  6. TestPhase6_Video_FfmpegUnavailable_GracefulDegradation — ffmpeg absent → user notified, text still routes

import (
	"context"
	"sync"
	"testing"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"github.com/marcoantonios1/Agent-OS/internal/channels/slack"
	"github.com/marcoantonios1/Agent-OS/internal/channels/telegram"
	"github.com/marcoantonios1/Agent-OS/internal/costguard"
	"github.com/marcoantonios1/Agent-OS/internal/types"
)

// ── Test 1: Slack text DM routes through the full stack ───────────────────────

// TestPhase6_Slack_TextMessage_Routes sends a plain text DM from the allowed
// Slack user, drives it through the full router→LLM stack, and verifies that:
//   - the LLM is called at least once (intent classifier + agent)
//   - the Slack API receives the agent's reply via PostMessage
func TestPhase6_Slack_TextMessage_Routes(t *testing.T) {
	var mu sync.Mutex
	var capturedReqs []costguard.CompletionRequest

	capLLM := &capturingScriptedLLM{
		responses: []costguard.CompletionResponse{
			classifyResp("companion"),
			textResp("Hello! How can I help you today?"),
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

	api := &mockSlackClient{}
	h := slack.NewForTest(api, stack.router, "U_ALLOWED")

	h.HandleMessage(context.Background(), slackDMMessage("U_ALLOWED", "D_CHAN", "hello agent"))

	mu.Lock()
	llmCalls := len(capturedReqs)
	mu.Unlock()

	if llmCalls == 0 {
		t.Fatal("LLM not called — text message did not route to agent")
	}

	api.mu.Lock()
	posted := api.posted
	api.mu.Unlock()

	if len(posted) == 0 {
		t.Fatal("Slack API PostMessage not called — no reply sent to user")
	}
}

// ── Test 2: Slack whitelist blocks unknown users ───────────────────────────────

// TestPhase6_Slack_Whitelist_Enforced sends a DM from a user ID that is not in
// the allowlist and verifies that neither the LLM nor the Slack API are called.
func TestPhase6_Slack_Whitelist_Enforced(t *testing.T) {
	var mu sync.Mutex
	llmCalls := 0

	capLLM := &capturingScriptedLLM{
		responses: nil,
		onCall: func(_ costguard.CompletionRequest) {
			mu.Lock()
			llmCalls++
			mu.Unlock()
		},
	}

	stack := newStack(stackConfig{
		customLLM: capLLM,
		emailProv: newMockEmail(nil, nil),
	})
	defer stack.Close()

	api := &mockSlackClient{}
	h := slack.NewForTest(api, stack.router, "U_ALLOWED")

	h.HandleMessage(context.Background(), slackDMMessage("U_STRANGER", "D_CHAN", "exfiltrate secrets"))

	mu.Lock()
	calls := llmCalls
	mu.Unlock()

	if calls != 0 {
		t.Errorf("LLM called %d times for non-allowed user, want 0", calls)
	}

	api.mu.Lock()
	posted := len(api.posted)
	api.mu.Unlock()

	if posted != 0 {
		t.Errorf("Slack PostMessage called %d times for non-allowed user, want 0", posted)
	}
}

// ── Test 3: iMessage text routes and reply sent to BlueBubbles ────────────────

// TestPhase6_iMessage_TextMessage_Routes posts a BlueBubbles "new-message"
// webhook and verifies the complete round trip:
//   - the dispatcher receives the message with the correct session key and text
//   - the handler sends the agent reply back via POST /api/v1/message/text
func TestPhase6_iMessage_TextMessage_Routes(t *testing.T) {
	const (
		allowedHandle = "+1234567890"
		chatGUID      = "iMessage;-;+1234567890"
		msgText       = "hello agent"
		agentReply    = "Hello! How can I help?"
	)

	fb := newFakeBlueBubbles(t, nil)
	disp := &iMessageRecordingDispatcher{response: agentReply}
	h := newTestIMessageHandler(fb, disp, allowedHandle)

	h.HandleWebhook(context.Background(), bbWebhookBody(allowedHandle, chatGUID, msgText, nil))

	// Dispatcher must have received the message with the correct fields.
	disp.mu.Lock()
	msgs := disp.msgs
	disp.mu.Unlock()

	if len(msgs) != 1 {
		t.Fatalf("dispatcher called %d times, want 1", len(msgs))
	}
	if msgs[0].Text != msgText {
		t.Errorf("Text=%q, want %q", msgs[0].Text, msgText)
	}
	wantSID := "imessage:" + allowedHandle + ":" + chatGUID
	if msgs[0].SessionID != wantSID {
		t.Errorf("SessionID=%q, want %q", msgs[0].SessionID, wantSID)
	}
	if msgs[0].ChannelID != types.ChannelID("imessage") {
		t.Errorf("ChannelID=%q, want \"imessage\"", msgs[0].ChannelID)
	}

	// Fake BlueBubbles must have received exactly one POST /api/v1/message/text.
	fb.mu.Lock()
	posts := fb.textPosts
	fb.mu.Unlock()

	if len(posts) != 1 {
		t.Fatalf("POST /api/v1/message/text called %d times, want 1", len(posts))
	}
	if posts[0].ChatGUID != chatGUID {
		t.Errorf("chatGuid=%q, want %q", posts[0].ChatGUID, chatGUID)
	}
	if posts[0].Message != agentReply {
		t.Errorf("reply message=%q, want %q", posts[0].Message, agentReply)
	}
	if posts[0].Password == "" {
		t.Error("password query param missing from POST /api/v1/message/text")
	}
}

// ── Test 4: iMessage whitelist blocks unknown handles ─────────────────────────

// TestPhase6_iMessage_Whitelist_Enforced sends a webhook from a handle that is
// not in the allowlist and verifies that neither the dispatcher nor the
// BlueBubbles text API are called.
func TestPhase6_iMessage_Whitelist_Enforced(t *testing.T) {
	fb := newFakeBlueBubbles(t, nil)
	disp := &iMessageRecordingDispatcher{response: "should not send"}
	h := newTestIMessageHandler(fb, disp, "+1234567890")

	h.HandleWebhook(context.Background(),
		bbWebhookBody("+9999999999", "iMessage;-;+9999999999", "exfiltrate secrets", nil))

	disp.mu.Lock()
	dispatched := len(disp.msgs)
	disp.mu.Unlock()

	if dispatched != 0 {
		t.Errorf("dispatcher called %d times for unknown handle, want 0", dispatched)
	}

	fb.mu.Lock()
	posted := len(fb.textPosts)
	fb.mu.Unlock()

	if posted != 0 {
		t.Errorf("POST /api/v1/message/text called %d times for unknown handle, want 0", posted)
	}
}

// ── Test 5: video pipeline end-to-end through the LLM stack ──────────────────

// TestPhase6_Video_Pipeline_EndToEnd drives a video upload through the Telegram
// handler to produce an InboundMessage, then routes it through the full
// router→agent→LLM stack and verifies that image ContentParts arrive at the LLM
// with valid base64 JPEG data in the correct position (after the text intro).
func TestPhase6_Video_Pipeline_EndToEnd(t *testing.T) {
	requireFFmpegVideo(t)

	videoBytes := makeIntegrationVideo(t, 2)

	// Capture all LLM requests to verify image ContentParts arrive intact.
	var mu sync.Mutex
	var capturedReqs []costguard.CompletionRequest

	capLLM := &capturingScriptedLLM{
		responses: []costguard.CompletionResponse{
			classifyResp("companion"),
			textResp("I can see the frames from your video."),
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

	// Step 1: drive the Telegram handler to produce the InboundMessage with video
	// ContentParts. A recording dispatcher captures it before it reaches the LLM.
	srv := videoHTTPServer(t, videoBytes)
	defer srv.Close()

	tgDisp := &telegramRecordingDispatcher{}
	bot := &videoTGBot{
		mockTGBot: &mockTGBot{},
		fileURL:   srv.URL + "/video.mp4",
	}

	tgH := telegram.NewForTest(tgDisp, bot, 111)
	tgH.SetHTTPClient(srv.Client())

	tgH.HandleMessage(context.Background(), &tgbotapi.Message{
		MessageID: 1,
		From:      &tgbotapi.User{ID: 111},
		Chat:      &tgbotapi.Chat{ID: 999},
		Video: &tgbotapi.Video{
			FileID:   "vid-endtoend",
			FileSize: len(videoBytes),
			MimeType: "video/mp4",
		},
	})

	tgDisp.mu.Lock()
	tgMsgs := tgDisp.msgs
	tgDisp.mu.Unlock()

	if len(tgMsgs) != 1 {
		t.Fatalf("Telegram handler produced %d dispatched messages, want 1", len(tgMsgs))
	}

	// Verify the InboundMessage already has the correct ContentPart structure
	// before routing to the LLM.
	inbound := tgMsgs[0]
	if len(inbound.Parts) == 0 {
		t.Fatal("InboundMessage.Parts is empty before LLM routing")
	}
	if inbound.Parts[0].Type != "text" {
		t.Errorf("Parts[0].Type=%q, want 'text' (video descriptor must lead)", inbound.Parts[0].Type)
	}

	// Step 2: route the same InboundMessage through the full LLM stack.
	if _, err := stack.router.Route(context.Background(), inbound); err != nil {
		t.Fatalf("router.Route: %v", err)
	}

	mu.Lock()
	reqs := capturedReqs
	mu.Unlock()

	// Find an image ContentPart in the LLM call history.
	var found *types.ContentPart
outer:
	for _, req := range reqs {
		for _, msg := range req.Messages {
			for i := range msg.Parts {
				if msg.Parts[i].Type == "image" {
					p := msg.Parts[i]
					found = &p
					break outer
				}
			}
		}
	}

	if found == nil {
		t.Fatal("no image ContentPart reached the LLM — video pipeline broken")
	}
	if found.ImageData == "" {
		t.Error("LLM received image ContentPart with empty ImageData")
	}
	if found.MimeType != "image/jpeg" {
		t.Errorf("LLM image ContentPart MimeType=%q, want image/jpeg", found.MimeType)
	}
}

// ── Test 6: ffmpeg absent → graceful degradation, other types unaffected ──────

// TestPhase6_Video_FfmpegUnavailable_GracefulDegradation verifies two things:
//  1. When ffmpeg is absent, a video upload fails gracefully: the user receives
//     "Video analysis isn't available on this server." and the dispatcher is not called.
//  2. A subsequent plain-text message still routes correctly — the failed video
//     attempt does not corrupt the handler state.
func TestPhase6_Video_FfmpegUnavailable_GracefulDegradation(t *testing.T) {
	dummyVideo := make([]byte, 512)

	srv := videoHTTPServer(t, dummyVideo)
	defer srv.Close()

	bot := &videoTGBot{
		mockTGBot: &mockTGBot{},
		fileURL:   srv.URL + "/video.mp4",
	}
	disp := &telegramRecordingDispatcher{}

	h := telegram.NewForTest(disp, bot, 111)
	h.SetHTTPClient(srv.Client())

	// Erase PATH so exec.LookPath("ffmpeg") fails inside ExtractFrames.
	// t.Setenv restores the original value when the test ends.
	t.Setenv("PATH", "")

	// ── Part A: video must fail gracefully ────────────────────────────────────

	h.HandleMessage(context.Background(), &tgbotapi.Message{
		MessageID: 1,
		From:      &tgbotapi.User{ID: 111},
		Chat:      &tgbotapi.Chat{ID: 999},
		Video: &tgbotapi.Video{
			FileID:   "video-no-ffmpeg",
			FileSize: len(dummyVideo),
			MimeType: "video/mp4",
		},
	})

	disp.mu.Lock()
	afterVideo := len(disp.msgs)
	disp.mu.Unlock()

	if afterVideo != 0 {
		t.Errorf("dispatcher called %d times when ffmpeg unavailable, want 0", afterVideo)
	}

	bot.mu.Lock()
	sentAfterVideo := make([]tgbotapi.Chattable, len(bot.sent))
	copy(sentAfterVideo, bot.sent)
	bot.mu.Unlock()

	if len(sentAfterVideo) == 0 {
		t.Fatal("no reply sent to user when ffmpeg unavailable")
	}
	if !containsVideoReply(sentAfterVideo, "Video analysis isn't available") {
		t.Errorf(`reply must contain "Video analysis isn't available"; got %d message(s)`, len(sentAfterVideo))
	}

	// ── Part B: text message still routes correctly ───────────────────────────

	h.HandleMessage(context.Background(), &tgbotapi.Message{
		MessageID: 2,
		From:      &tgbotapi.User{ID: 111},
		Chat:      &tgbotapi.Chat{ID: 999},
		Text:      "what is 2 + 2?",
	})

	disp.mu.Lock()
	afterText := len(disp.msgs)
	disp.mu.Unlock()

	if afterText != 1 {
		t.Errorf("after video failure, text message dispatched %d times, want 1", afterText)
	}
	if disp.msgs[0].Text != "what is 2 + 2?" {
		t.Errorf("text message Text=%q, want %q", disp.msgs[0].Text, "what is 2 + 2?")
	}
}
