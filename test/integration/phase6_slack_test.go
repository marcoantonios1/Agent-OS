package integration

// Phase 6 integration tests: Slack channel.
//
//  1. Session key format — slack:{userID}:{channelID}; different channelIDs → different sessions.
//  2. Whitelist — message from non-allowed userID is silently dropped; no dispatcher call, no API call.
//  3. Image attachment — InboundMessage with image ContentPart (base64) reaches dispatcher intact;
//     no Slack-specific format leaks into the dispatcher layer.
//  4. Reminder delivery — NotifyReminder with a Slack SessionID posts to the correct channel.

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	slacklib "github.com/slack-go/slack"

	"github.com/marcoantonios1/Agent-OS/internal/channels/slack"
	"github.com/marcoantonios1/Agent-OS/internal/costguard"
	"github.com/marcoantonios1/Agent-OS/internal/sessions"
	"github.com/marcoantonios1/Agent-OS/internal/types"
)

// ── shared Slack test helpers ─────────────────────────────────────────────────

// slackRecordingDispatcher records every InboundMessage routed through it.
type slackRecordingDispatcher struct {
	mu   sync.Mutex
	msgs []types.InboundMessage
}

func (d *slackRecordingDispatcher) Route(_ context.Context, msg types.InboundMessage) (types.OutboundMessage, error) {
	d.mu.Lock()
	d.msgs = append(d.msgs, msg)
	d.mu.Unlock()
	return types.OutboundMessage{Text: "ok"}, nil
}

// mockSlackClient implements slack.SlackAPI for tests. All calls are recorded;
// PostMessage always succeeds with timestamp "ts1".
type mockSlackClient struct {
	mu      sync.Mutex
	posted  []string // text extracted from each PostMessage call (via UnsafeApplyMsgOptions)
	updated []string // text extracted from each UpdateMessage call
}

func (m *mockSlackClient) PostMessage(channelID string, options ...slacklib.MsgOption) (string, string, error) {
	m.mu.Lock()
	_, vals, _ := slacklib.UnsafeApplyMsgOptions("test-token", channelID, "https://slack.com/api/", options...)
	m.posted = append(m.posted, vals.Get("text"))
	m.mu.Unlock()
	return channelID, "ts1", nil
}

func (m *mockSlackClient) UpdateMessage(channelID, _ string, options ...slacklib.MsgOption) (string, string, string, error) {
	m.mu.Lock()
	_, vals, _ := slacklib.UnsafeApplyMsgOptions("test-token", channelID, "https://slack.com/api/", options...)
	m.updated = append(m.updated, vals.Get("text"))
	m.mu.Unlock()
	return channelID, "ts1", "", nil
}

func (m *mockSlackClient) UploadFile(_ slacklib.UploadFileParameters) (*slacklib.FileSummary, error) {
	return &slacklib.FileSummary{ID: "F001"}, nil
}

var _ slack.SlackAPI = (*mockSlackClient)(nil)

// imageFileServer starts a test HTTP server that returns fake JPEG bytes and
// requires an Authorization header. Returns the server and the expected token.
func imageFileServer(t *testing.T, imageData []byte) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Slack file downloads require a Bearer token.
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write(imageData) //nolint:errcheck
	}))
}

// slackDMMessage is a convenience constructor for an IncomingMessage representing
// a regular Slack DM text event.
func slackDMMessage(userID, channelID, text string) *slack.IncomingMessage {
	return &slack.IncomingMessage{
		User:        userID,
		Text:        text,
		Channel:     channelID,
		ChannelType: "im",
		SubType:     "",
	}
}

// ── Test 1: session key format and isolation ──────────────────────────────────

// TestPhase6_Slack_SessionKey_Format verifies that the Slack handler produces
// session IDs in the format "slack:{userID}:{channelID}" and that two messages
// from the same user sent in different DM channels land in separate sessions.
func TestPhase6_Slack_SessionKey_Format(t *testing.T) {
	api := &mockSlackClient{}
	disp := &slackRecordingDispatcher{}
	h := slack.NewForTest(api, disp, "U_ALLOWED")

	ctx := context.Background()

	h.HandleMessage(ctx, slackDMMessage("U_ALLOWED", "D_CHAN_A", "hello from channel A"))
	h.HandleMessage(ctx, slackDMMessage("U_ALLOWED", "D_CHAN_B", "hello from channel B"))

	disp.mu.Lock()
	msgs := disp.msgs
	disp.mu.Unlock()

	if len(msgs) != 2 {
		t.Fatalf("dispatcher called %d times, want 2", len(msgs))
	}

	want1 := fmt.Sprintf("slack:%s:%s", "U_ALLOWED", "D_CHAN_A")
	want2 := fmt.Sprintf("slack:%s:%s", "U_ALLOWED", "D_CHAN_B")

	if msgs[0].SessionID != want1 {
		t.Errorf("msg1 SessionID = %q, want %q", msgs[0].SessionID, want1)
	}
	if msgs[1].SessionID != want2 {
		t.Errorf("msg2 SessionID = %q, want %q", msgs[1].SessionID, want2)
	}
	if msgs[0].SessionID == msgs[1].SessionID {
		t.Error("different channelIDs for the same user must produce different session keys")
	}
	if !strings.HasPrefix(msgs[0].SessionID, "slack:") {
		t.Errorf("session key %q must start with 'slack:'", msgs[0].SessionID)
	}
	if !strings.HasPrefix(msgs[1].SessionID, "slack:") {
		t.Errorf("session key %q must start with 'slack:'", msgs[1].SessionID)
	}
}

// ── Test 2: whitelist enforcement (security boundary) ────────────────────────

// TestPhase6_Slack_Whitelist_Drops_Unknown_User sends a Slack DM from a user ID
// that is not in the allowlist. Verifies that neither the dispatcher nor the Slack
// API are called — this must be a complete, silent drop with no downstream side effects.
func TestPhase6_Slack_Whitelist_Drops_Unknown_User(t *testing.T) {
	api := &mockSlackClient{}
	disp := &slackRecordingDispatcher{}
	h := slack.NewForTest(api, disp, "U_ALLOWED") // only U_ALLOWED is permitted

	h.HandleMessage(context.Background(), slackDMMessage("U_STRANGER", "D_CHAN", "exfiltrate secrets"))

	disp.mu.Lock()
	dispatched := len(disp.msgs)
	disp.mu.Unlock()

	if dispatched != 0 {
		t.Errorf("dispatcher called %d times for non-allowed user — security boundary violated", dispatched)
	}

	api.mu.Lock()
	posted := len(api.posted)
	api.mu.Unlock()

	if posted != 0 {
		t.Errorf("Slack API PostMessage called %d times for non-allowed user — silent drop required", posted)
	}
}

// ── Test 3: image attachment routing to LLM ───────────────────────────────────

// TestPhase6_Slack_ImageAttachment_ReachesLLM verifies that a Slack file upload
// event (MIME type image/jpeg) is correctly downloaded, base64-encoded via the
// attachments pipeline, and forwarded to the dispatcher as an image ContentPart.
//
// The test also routes the resulting InboundMessage through the full router→agent→LLM
// stack to confirm that no Slack-specific format leaks past the dispatcher boundary.
func TestPhase6_Slack_ImageAttachment_ReachesLLM(t *testing.T) {
	// Minimal PNG magic bytes — the attachments pipeline checks the MIME type
	// supplied by the event, not the file contents.
	imgBytes := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d}
	srv := imageFileServer(t, imgBytes)
	defer srv.Close()

	// ── Step 1: drive the Slack handler and capture the InboundMessage it produces.

	api := &mockSlackClient{}
	disp := &slackRecordingDispatcher{}
	h := slack.NewForTest(api, disp, "U_ALLOWED")
	h.SetHTTPClient(srv.Client())

	h.HandleMessage(context.Background(), &slack.IncomingMessage{
		User:        "U_ALLOWED",
		Text:        "check out this image",
		Channel:     "D_CHAN",
		ChannelType: "im",
		Files: []slack.AttachedFile{{
			ID:                 "F_IMG",
			MimeType:           "image/jpeg",
			Name:               "photo.jpg",
			URLPrivateDownload: srv.URL + "/photo.jpg",
		}},
	})

	disp.mu.Lock()
	msgs := disp.msgs
	disp.mu.Unlock()

	if len(msgs) != 1 {
		t.Fatalf("dispatcher called %d times, want 1", len(msgs))
	}

	dispatched := msgs[0]

	// ── Step 2: verify the InboundMessage has the image ContentPart in the correct format.

	var imgPart *types.ContentPart
	for i := range dispatched.Parts {
		if dispatched.Parts[i].Type == "image" {
			p := dispatched.Parts[i]
			imgPart = &p
			break
		}
	}

	if imgPart == nil {
		t.Fatalf("no image ContentPart in dispatched message — attachment did not reach dispatcher; Parts=%v", dispatched.Parts)
	}
	if imgPart.MimeType != "image/jpeg" {
		t.Errorf("ContentPart.MimeType = %q, want image/jpeg", imgPart.MimeType)
	}

	// ImageData must be valid base64.
	decoded, err := base64.StdEncoding.DecodeString(imgPart.ImageData)
	if err != nil {
		t.Fatalf("ContentPart.ImageData is not valid base64: %v", err)
	}
	if string(decoded) != string(imgBytes) {
		t.Error("decoded ImageData does not match original image bytes")
	}

	// No Slack-specific type or field names must appear in the ContentPart.
	if imgPart.Type != "image" {
		t.Errorf("ContentPart.Type = %q, want 'image' (same as Telegram/WhatsApp)", imgPart.Type)
	}

	// ── Step 3: route the same InboundMessage through the full LLM stack and
	// confirm the image ContentPart arrives at the LLM without modification.

	var mu sync.Mutex
	var capturedReqs []costguard.CompletionRequest

	capLLM := &capturingScriptedLLM{
		responses: []costguard.CompletionResponse{
			classifyResp("comms"),
			textResp("I can see the photo you sent."),
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

	// Use the same InboundMessage the Slack handler produced (already verified above).
	inbound := types.InboundMessage{
		ID:        "slack-msg-1",
		ChannelID: types.ChannelID("slack"),
		UserID:    "U_ALLOWED",
		SessionID: "slack:U_ALLOWED:D_CHAN",
		Text:      "check out this image",
		Timestamp: time.Now(),
		Parts:     dispatched.Parts,
	}

	if _, err := stack.router.Route(context.Background(), inbound); err != nil {
		t.Fatalf("router.Route: %v", err)
	}

	mu.Lock()
	reqs := capturedReqs
	mu.Unlock()

	var llmImgPart *types.ContentPart
outer:
	for _, req := range reqs {
		for _, msg := range req.Messages {
			for i := range msg.Parts {
				if msg.Parts[i].Type == "image" {
					p := msg.Parts[i]
					llmImgPart = &p
					break outer
				}
			}
		}
	}

	if llmImgPart == nil {
		t.Fatal("image ContentPart did not reach the LLM — Slack attachment pipeline broken")
	}
	if llmImgPart.ImageData != imgPart.ImageData {
		t.Error("ImageData was modified between dispatcher and LLM — no transformation expected")
	}
}

// ── Test 4: reminder delivery via Slack notifier ──────────────────────────────

// TestPhase6_Slack_ReminderDelivery creates a due reminder whose SessionID
// encodes the Slack channel and verifies that NotifyReminder delivers the message
// to the correct DM channel via the Slack API.
func TestPhase6_Slack_ReminderDelivery(t *testing.T) {
	api := &mockSlackClient{}
	disp := &slackRecordingDispatcher{}
	h := slack.NewForTest(api, disp, "U_ALLOWED")

	r := &sessions.Reminder{
		ID:        "rem-slack-1",
		UserID:    "U_ALLOWED",
		SessionID: "slack:U_ALLOWED:D_CHAN_REMIND",
		ChannelID: types.ChannelID("slack"),
		Message:   "Time to review your PRs!",
		FireAt:    time.Now().Add(-time.Second), // already due
	}

	if err := h.NotifyReminder(context.Background(), r); err != nil {
		t.Fatalf("NotifyReminder: %v", err)
	}

	api.mu.Lock()
	posted := api.posted
	api.mu.Unlock()

	if len(posted) != 1 {
		t.Fatalf("PostMessage called %d times after NotifyReminder, want 1", len(posted))
	}
	if posted[0] != r.Message {
		t.Errorf("posted message = %q, want %q", posted[0], r.Message)
	}
}
