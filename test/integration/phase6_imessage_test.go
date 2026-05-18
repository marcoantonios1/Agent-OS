package integration

// Phase 6 integration tests: iMessage channel (BlueBubbles gateway).
//
//  1. Text message routing — HandleWebhook correctly parses a "new-message" event
//     and routes it to the dispatcher with the correct session key and text.
//  2. Whitelist enforcement — messages from non-allowed handles are silently dropped;
//     neither the dispatcher nor the BlueBubbles API are called.
//  3. Image attachment → dispatcher — image bytes are downloaded from the fake
//     BlueBubbles server, base64-encoded, and reach the dispatcher as an image
//     ContentPart with no raw URLs leaking through.
//  4. Outbound text — the dispatcher's reply triggers exactly one POST to
//     /api/v1/message/text with the correct chatGuid, message body, and password.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/marcoantonios1/Agent-OS/internal/channels/imessage"
	"github.com/marcoantonios1/Agent-OS/internal/types"
)

// ── shared iMessage test helpers ──────────────────────────────────────────────

// iMessageRecordingDispatcher records every InboundMessage routed through it and
// returns a configurable text response.
type iMessageRecordingDispatcher struct {
	mu       sync.Mutex
	msgs     []types.InboundMessage
	response string // text returned by Route; empty string by default
}

func (d *iMessageRecordingDispatcher) Route(_ context.Context, msg types.InboundMessage) (types.OutboundMessage, error) {
	d.mu.Lock()
	d.msgs = append(d.msgs, msg)
	d.mu.Unlock()
	return types.OutboundMessage{Text: d.response}, nil
}

// bbTextPost holds the decoded body of a POST /api/v1/message/text request.
type bbTextPost struct {
	ChatGUID string `json:"chatGuid"`
	TempGUID string `json:"tempGuid"`
	Message  string `json:"message"`
	Password string // captured from the URL query parameter
}

// fakeBlueBubbles is a minimal httptest.Server implementing the BlueBubbles API
// endpoints used by the iMessage handler. All outbound calls are recorded.
type fakeBlueBubbles struct {
	mu        sync.Mutex
	imgData   []byte
	textPosts []bbTextPost
	srv       *httptest.Server
}

func newFakeBlueBubbles(t *testing.T, imgData []byte) *fakeBlueBubbles {
	t.Helper()
	fb := &fakeBlueBubbles{imgData: imgData}

	mux := http.NewServeMux()

	// GET /api/v1/ping — always OK.
	mux.HandleFunc("/api/v1/ping", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	// POST /api/v1/webhook — acknowledge webhook registration.
	mux.HandleFunc("/api/v1/webhook", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	// GET /api/v1/attachment/{guid}/download — serve the test image fixture.
	mux.HandleFunc("/api/v1/attachment/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write(fb.imgData) //nolint:errcheck
	})

	// POST /api/v1/message/text — record outbound message requests.
	mux.HandleFunc("/api/v1/message/text", func(w http.ResponseWriter, r *http.Request) {
		var post bbTextPost
		json.NewDecoder(r.Body).Decode(&post) //nolint:errcheck
		r.Body.Close()
		post.Password = r.URL.Query().Get("password")
		fb.mu.Lock()
		fb.textPosts = append(fb.textPosts, post)
		fb.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	})

	fb.srv = httptest.NewServer(mux)
	t.Cleanup(fb.srv.Close)
	return fb
}

// bbWebhookBody builds a BlueBubbles "new-message" webhook JSON payload.
// atts is a list of attachment objects (each a map of JSON field → value).
func bbWebhookBody(handle, chatGUID, text string, atts []map[string]string) []byte {
	attachments := make([]map[string]string, 0, len(atts))
	attachments = append(attachments, atts...)
	payload := map[string]interface{}{
		"type": "new-message",
		"data": map[string]interface{}{
			"guid":        "msg-1",
			"text":        text,
			"isFromMe":    false,
			"handle":      map[string]string{"address": handle},
			"chats":       []map[string]string{{"guid": chatGUID}},
			"attachments": attachments,
		},
	}
	b, _ := json.Marshal(payload)
	return b
}

// newTestIMessageHandler creates a Handler wired to the fake server.
// No real network calls are made; the HTTP client is redirected to the fake server.
func newTestIMessageHandler(fb *fakeBlueBubbles, disp *iMessageRecordingDispatcher, allowedHandle string) *imessage.Handler {
	h := imessage.NewForTest(imessage.Config{
		BaseURL:       fb.srv.URL,
		Password:      "test-pass",
		AllowedHandle: allowedHandle,
	}, disp)
	h.SetHTTPClient(fb.srv.Client())
	return h
}

// ── Test 1: text message routing and session key format ───────────────────────

// TestPhase6_iMessage_WebhookParsing_TextMessage verifies that a well-formed
// "new-message" webhook from an allowed handle is routed to the dispatcher with
// the correct session key ("imessage:{handle}:{chatGUID}") and unmodified text.
func TestPhase6_iMessage_WebhookParsing_TextMessage(t *testing.T) {
	const (
		allowedHandle = "+1234567890"
		chatGUID      = "iMessage;-;+1234567890"
		msgText       = "hello agent"
	)

	fb := newFakeBlueBubbles(t, nil)
	disp := &iMessageRecordingDispatcher{}
	h := newTestIMessageHandler(fb, disp, allowedHandle)

	h.HandleWebhook(context.Background(), bbWebhookBody(allowedHandle, chatGUID, msgText, nil))

	disp.mu.Lock()
	msgs := disp.msgs
	disp.mu.Unlock()

	if len(msgs) != 1 {
		t.Fatalf("dispatcher called %d times, want 1", len(msgs))
	}

	wantSID := "imessage:" + allowedHandle + ":" + chatGUID
	if msgs[0].SessionID != wantSID {
		t.Errorf("SessionID=%q, want %q", msgs[0].SessionID, wantSID)
	}
	if msgs[0].Text != msgText {
		t.Errorf("Text=%q, want %q", msgs[0].Text, msgText)
	}
	if msgs[0].ChannelID != types.ChannelID("imessage") {
		t.Errorf("ChannelID=%q, want \"imessage\"", msgs[0].ChannelID)
	}
	if !strings.HasPrefix(msgs[0].SessionID, "imessage:") {
		t.Errorf("session key %q must start with 'imessage:'", msgs[0].SessionID)
	}
	if msgs[0].UserID != allowedHandle {
		t.Errorf("UserID=%q, want %q", msgs[0].UserID, allowedHandle)
	}
}

// ── Test 2: whitelist enforcement ────────────────────────────────────────────

// TestPhase6_iMessage_Whitelist_Drops_Unknown_Handle sends a webhook from a
// handle that is not in the allowlist. Verifies that neither the dispatcher nor
// the BlueBubbles text API are called — a complete, silent drop with no side effects.
func TestPhase6_iMessage_Whitelist_Drops_Unknown_Handle(t *testing.T) {
	fb := newFakeBlueBubbles(t, nil)
	disp := &iMessageRecordingDispatcher{}
	h := newTestIMessageHandler(fb, disp, "+1234567890") // only this handle is allowed

	h.HandleWebhook(context.Background(),
		bbWebhookBody("+9999999999", "iMessage;-;+9999999999", "exfiltrate secrets", nil))

	disp.mu.Lock()
	dispatched := len(disp.msgs)
	disp.mu.Unlock()

	if dispatched != 0 {
		t.Errorf("dispatcher called %d times for unknown handle — whitelist violated", dispatched)
	}

	fb.mu.Lock()
	posted := len(fb.textPosts)
	fb.mu.Unlock()

	if posted != 0 {
		t.Errorf("sendText called %d times for unknown handle — silent drop required", posted)
	}
}

// ── Test 3: image attachment → dispatcher ────────────────────────────────────

// TestPhase6_iMessage_ImageAttachment_ReachesLLM verifies that a webhook with an
// image attachment causes the handler to download the file from BlueBubbles,
// base64-encode it, and forward it to the dispatcher as an image ContentPart.
// No raw BlueBubbles URLs may appear in the ContentPart.
func TestPhase6_iMessage_ImageAttachment_ReachesLLM(t *testing.T) {
	// Minimal PNG magic bytes — the attachment pipeline checks the MIME type
	// from the event, not the file contents.
	imgBytes := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d}

	fb := newFakeBlueBubbles(t, imgBytes)
	disp := &iMessageRecordingDispatcher{}
	h := newTestIMessageHandler(fb, disp, "+1234567890")

	atts := []map[string]string{
		{"guid": "img-123", "mimeType": "image/jpeg", "transferName": "photo.jpg"},
	}
	h.HandleWebhook(context.Background(),
		bbWebhookBody("+1234567890", "iMessage;-;+1234567890", "check this image", atts))

	disp.mu.Lock()
	msgs := disp.msgs
	disp.mu.Unlock()

	if len(msgs) != 1 {
		t.Fatalf("dispatcher called %d times, want 1", len(msgs))
	}

	// Locate the image ContentPart.
	var imgPart *types.ContentPart
	for i := range msgs[0].Parts {
		if msgs[0].Parts[i].Type == "image" {
			p := msgs[0].Parts[i]
			imgPart = &p
			break
		}
	}

	if imgPart == nil {
		t.Fatalf("no image ContentPart in dispatched message — attachment did not reach dispatcher; Parts=%v", msgs[0].Parts)
	}
	if imgPart.MimeType != "image/jpeg" {
		t.Errorf("ContentPart.MimeType=%q, want image/jpeg", imgPart.MimeType)
	}

	// ImageData must be valid base64 encoding of the original bytes.
	decoded, err := base64.StdEncoding.DecodeString(imgPart.ImageData)
	if err != nil {
		t.Fatalf("ContentPart.ImageData is not valid base64: %v", err)
	}
	if !bytes.Equal(decoded, imgBytes) {
		t.Error("decoded ImageData does not match original image bytes")
	}

	// No raw BlueBubbles URLs may leak into the ContentPart.
	if strings.Contains(imgPart.ImageData, fb.srv.URL) {
		t.Error("raw BlueBubbles URL leaked into ContentPart.ImageData")
	}
	if imgPart.Type != "image" {
		t.Errorf("ContentPart.Type=%q, want 'image' (channel-agnostic)", imgPart.Type)
	}
}

// ── Test 4: outbound text via BlueBubbles API ─────────────────────────────────

// TestPhase6_iMessage_SendText_CallsBlueBubblesAPI verifies that after the
// dispatcher produces a reply the handler calls POST /api/v1/message/text exactly
// once with the correct chatGuid, message text, and password query parameter.
func TestPhase6_iMessage_SendText_CallsBlueBubblesAPI(t *testing.T) {
	const (
		allowedHandle = "+1234567890"
		chatGUID      = "iMessage;-;+1234567890"
		agentReply    = "Hello back"
	)

	fb := newFakeBlueBubbles(t, nil)
	disp := &iMessageRecordingDispatcher{response: agentReply}
	h := newTestIMessageHandler(fb, disp, allowedHandle)

	h.HandleWebhook(context.Background(),
		bbWebhookBody(allowedHandle, chatGUID, "hi", nil))

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
		t.Errorf("message=%q, want %q", posts[0].Message, agentReply)
	}
	if posts[0].Password == "" {
		t.Error("password query param missing from POST /api/v1/message/text")
	}
}
