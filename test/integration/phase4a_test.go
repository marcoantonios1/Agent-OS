package integration

// Phase 4a integration tests: end-to-end attachment pipeline.
//
//  1. Web channel — JPEG image → agent LLM call contains image ContentPart
//  2. Web channel — PDF → agent LLM call contains extracted-text ContentPart
//  3. Web channel — unsupported MIME type → 400
//  4. Web channel — 6 attachments (limit is 5) → 400
//  5. Discord path — InboundMessage.Parts built by fetchAttachments → image reaches LLM
//  6. Backward-compat — text-only request produces zero Parts in LLM call

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/marcoantonios1/Agent-OS/internal/costguard"
	"github.com/marcoantonios1/Agent-OS/internal/types"
)

// ── local request/response types ─────────────────────────────────────────────

type attachmentPayload struct {
	MimeType string `json:"mime_type"`
	Data     string `json:"data"` // base64-encoded bytes
}

type chatWithAttachmentsRequest struct {
	SessionID   string              `json:"session_id"`
	UserID      string              `json:"user_id"`
	Text        string              `json:"text"`
	Attachments []attachmentPayload `json:"attachments,omitempty"`
}

// ── helpers ───────────────────────────────────────────────────────────────────

// postChat marshals body as JSON, POSTs to /v1/chat, and returns both the
// raw response (for status-code checks) and the decoded chatResponse body.
// The HTTP body is fully consumed and closed before returning.
func (ts *testStack) postChat(t *testing.T, body any) (*http.Response, chatResponse) {
	t.Helper()
	b, _ := json.Marshal(body)
	resp, err := http.Post(ts.srv.URL+"/v1/chat", "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatalf("POST /v1/chat: %v", err)
	}
	defer resp.Body.Close()
	var out chatResponse
	json.NewDecoder(resp.Body).Decode(&out) //nolint:errcheck
	return resp, out
}

// agentCallMessages returns the Messages from the second LLM call (index 1).
// Index 0 is always the classifier; index 1 is the agent's first (and, in the
// blocking Route path, only) completion call. Returns nil when fewer than two
// calls have been recorded.
func agentCallMessages(llm *scriptedLLM) []types.ConversationTurn {
	llm.mu.Lock()
	defer llm.mu.Unlock()
	if len(llm.calls) < 2 {
		return nil
	}
	return llm.calls[1].Messages
}

// findImagePart returns the first image ContentPart found in any turn, or nil.
func findImagePart(msgs []types.ConversationTurn) *types.ContentPart {
	for _, m := range msgs {
		for i := range m.Parts {
			if m.Parts[i].Type == "image" {
				p := m.Parts[i]
				return &p
			}
		}
	}
	return nil
}

// findTextPartContaining returns the first text ContentPart whose Text contains
// needle, or nil if none matches.
func findTextPartContaining(msgs []types.ConversationTurn, needle string) *types.ContentPart {
	for _, m := range msgs {
		for i := range m.Parts {
			if m.Parts[i].Type == "text" && strings.Contains(m.Parts[i].Text, needle) {
				p := m.Parts[i]
				return &p
			}
		}
	}
	return nil
}

// buildIntegrationPDF returns a minimal valid single-page PDF whose text layer
// contains text. Uses the same xref-offset layout as attachments/pdf_test.go so
// ledongthuc/pdf can parse it without errors.
func buildIntegrationPDF(t *testing.T, text string) []byte {
	t.Helper()
	stream := fmt.Sprintf("BT /F1 12 Tf 72 720 Td (%s) Tj ET\n", text)

	var buf bytes.Buffer
	w := func(s string) { buf.WriteString(s) }

	w("%PDF-1.4\n")
	offsets := make([]int, 0, 5)

	offsets = append(offsets, buf.Len())
	w("1 0 obj\n<</Type /Catalog /Pages 2 0 R>>\nendobj\n")

	offsets = append(offsets, buf.Len())
	w("2 0 obj\n<</Type /Pages /Kids [3 0 R] /Count 1>>\nendobj\n")

	offsets = append(offsets, buf.Len())
	w("3 0 obj\n<</Type /Page /Parent 2 0 R /MediaBox [0 0 612 792]" +
		" /Contents 4 0 R /Resources <</Font <</F1 5 0 R>>>>>>\nendobj\n")

	offsets = append(offsets, buf.Len())
	w(fmt.Sprintf("4 0 obj\n<</Length %d>>\nstream\n%sendstream\nendobj\n", len(stream), stream))

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

// dumpParts logs all ContentParts from each LLM call turn — useful when an
// assertion fails and you need to see what the agent actually received.
func dumpParts(t *testing.T, msgs []types.ConversationTurn) {
	t.Helper()
	for _, m := range msgs {
		for _, p := range m.Parts {
			preview := p.Text
			if len(preview) > 80 {
				preview = preview[:80] + "…"
			}
			t.Logf("  part role=%s type=%s mime=%s text=%q", m.Role, p.Type, p.MimeType, preview)
		}
	}
}

// ── Test 1: JPEG image via the web channel ────────────────────────────────────

func TestPhase4a_WebChannel_ImageAttachment(t *testing.T) {
	// Six bytes that form a valid JPEG magic header — small enough that the
	// handler's 5 MiB size limit is never reached.
	imgData := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10}
	b64 := base64.StdEncoding.EncodeToString(imgData)

	stack := newStack(stackConfig{
		llmResponses: []costguard.CompletionResponse{
			classifyResp("comms"),
			textResp("I can see a JPEG image."),
		},
		emailProv: newMockEmail(nil, nil),
	})
	defer stack.Close()

	httpResp, body := stack.postChat(t, chatWithAttachmentsRequest{
		SessionID:   "p4a-img-1",
		UserID:      "user-1",
		Text:        "describe this image",
		Attachments: []attachmentPayload{{MimeType: "image/jpeg", Data: b64}},
	})
	if httpResp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", httpResp.StatusCode)
	}
	if body.Text != "I can see a JPEG image." {
		t.Errorf("response text = %q", body.Text)
	}

	msgs := agentCallMessages(stack.llm)
	if msgs == nil {
		t.Fatal("expected ≥2 LLM calls (classifier + agent); got fewer")
	}

	imgPart := findImagePart(msgs)
	if imgPart == nil {
		dumpParts(t, msgs)
		t.Fatal("agent LLM call does not contain an image ContentPart")
	}
	if imgPart.MimeType != "image/jpeg" {
		t.Errorf("MimeType = %q, want image/jpeg", imgPart.MimeType)
	}
	if imgPart.ImageData != b64 {
		t.Errorf("ImageData mismatch: got len=%d, want len=%d",
			len(imgPart.ImageData), len(b64))
	}
}

// ── Test 2: PDF document via the web channel ──────────────────────────────────

func TestPhase4a_WebChannel_PDFAttachment(t *testing.T) {
	const pdfText = "Quarterly revenue report"
	pdfData := buildIntegrationPDF(t, pdfText)
	b64 := base64.StdEncoding.EncodeToString(pdfData)

	stack := newStack(stackConfig{
		llmResponses: []costguard.CompletionResponse{
			classifyResp("comms"),
			textResp("The document discusses quarterly revenue."),
		},
		emailProv: newMockEmail(nil, nil),
	})
	defer stack.Close()

	httpResp, _ := stack.postChat(t, chatWithAttachmentsRequest{
		SessionID:   "p4a-pdf-1",
		UserID:      "user-1",
		Text:        "summarise this document",
		Attachments: []attachmentPayload{{MimeType: "application/pdf", Data: b64}},
	})
	if httpResp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", httpResp.StatusCode)
	}

	msgs := agentCallMessages(stack.llm)
	if msgs == nil {
		t.Fatal("expected ≥2 LLM calls")
	}

	txtPart := findTextPartContaining(msgs, pdfText)
	if txtPart == nil {
		dumpParts(t, msgs)
		t.Fatalf("agent LLM call does not contain a text ContentPart with %q", pdfText)
	}
}

// ── Test 3: unsupported MIME type → 400 ──────────────────────────────────────

func TestPhase4a_WebChannel_InvalidMimeType(t *testing.T) {
	stack := newStack(stackConfig{emailProv: newMockEmail(nil, nil)})
	defer stack.Close()

	httpResp, _ := stack.postChat(t, chatWithAttachmentsRequest{
		SessionID:   "p4a-badmime",
		UserID:      "user-1",
		Text:        "here is a file",
		Attachments: []attachmentPayload{{MimeType: "application/zip", Data: "YWJj"}},
	})
	if httpResp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for unsupported mime type", httpResp.StatusCode)
	}
}

// ── Test 4: too many attachments → 400 ───────────────────────────────────────

func TestPhase4a_WebChannel_TooManyAttachments(t *testing.T) {
	stack := newStack(stackConfig{emailProv: newMockEmail(nil, nil)})
	defer stack.Close()

	// maxAttachments is 5 — send 6 to trigger the limit.
	jpegB64 := base64.StdEncoding.EncodeToString([]byte{0xFF, 0xD8, 0xFF, 0xE0})
	atts := make([]attachmentPayload, 6)
	for i := range atts {
		atts[i] = attachmentPayload{MimeType: "image/jpeg", Data: jpegB64}
	}

	httpResp, _ := stack.postChat(t, chatWithAttachmentsRequest{
		SessionID:   "p4a-toomany",
		UserID:      "user-1",
		Text:        "images",
		Attachments: atts,
	})
	if httpResp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for too many attachments", httpResp.StatusCode)
	}
}

// ── Test 5: Discord path — Parts built by fetchAttachments reach the LLM ──────

// TestPhase4a_Discord_ImageAttachment validates the full pipeline from an
// InboundMessage.Parts (as the Discord handler produces after fetchAttachments)
// through the router to the agent's LLM call.
//
// The Discord handler's own URL-fetching logic is covered by its unit tests
// (internal/channels/discord/handler_test.go). This integration test verifies
// that once a ContentPart is placed on InboundMessage.Parts, it survives the
// router → agent → LLM pipeline intact.
func TestPhase4a_Discord_ImageAttachment(t *testing.T) {
	// PNG magic bytes — small, distinct from JPEG to verify MimeType is preserved.
	imgData := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}
	b64 := base64.StdEncoding.EncodeToString(imgData)

	stack := newStack(stackConfig{
		llmResponses: []costguard.CompletionResponse{
			classifyResp("comms"),
			textResp("That is a PNG screenshot."),
		},
		emailProv: newMockEmail(nil, nil),
	})
	defer stack.Close()

	// Construct the InboundMessage exactly as discord.Handler.onMessageCreate
	// would after calling fetchAttachments on a single PNG attachment.
	inbound := types.InboundMessage{
		ID:        "discord-msg-1",
		ChannelID: types.ChannelID("discord"),
		UserID:    "discord-user-1",
		SessionID: "discord:guild123:chan456:discord-user-1",
		Text:      "what is in this screenshot?",
		Timestamp: time.Now(),
		Parts: []types.ContentPart{
			{Type: "text", Text: "what is in this screenshot?"},
			{Type: "image", ImageData: b64, MimeType: "image/png", Filename: "screenshot.png"},
		},
	}

	out, err := stack.router.Route(context.Background(), inbound)
	if err != nil {
		t.Fatalf("Route error: %v", err)
	}
	if out.Text != "That is a PNG screenshot." {
		t.Errorf("output = %q", out.Text)
	}

	msgs := agentCallMessages(stack.llm)
	if msgs == nil {
		t.Fatal("expected ≥2 LLM calls (classifier + agent)")
	}

	imgPart := findImagePart(msgs)
	if imgPart == nil {
		dumpParts(t, msgs)
		t.Fatal("agent LLM call does not contain an image ContentPart")
	}
	if imgPart.MimeType != "image/png" {
		t.Errorf("MimeType = %q, want image/png", imgPart.MimeType)
	}
	if imgPart.Filename != "screenshot.png" {
		t.Errorf("Filename = %q, want screenshot.png", imgPart.Filename)
	}
	if imgPart.ImageData != b64 {
		t.Errorf("ImageData mismatch")
	}
}

// ── Test 6: text-only request produces zero Parts ─────────────────────────────

// TestPhase4a_MultipartTurn_BackwardCompat verifies that an ordinary text
// request (no attachments) reaches the agent with ConversationTurns whose
// Parts field is nil — identical to pre-attachment behaviour.
func TestPhase4a_MultipartTurn_BackwardCompat(t *testing.T) {
	stack := newStack(stackConfig{
		llmResponses: []costguard.CompletionResponse{
			classifyResp("comms"),
			textResp("Hello!"),
		},
		emailProv: newMockEmail(nil, nil),
	})
	defer stack.Close()

	_, body := stack.postChat(t, chatRequest{
		SessionID: "p4a-textonly",
		UserID:    "user-1",
		Text:      "just text, no attachments",
	})
	if body.Text != "Hello!" {
		t.Errorf("response = %q", body.Text)
	}

	msgs := agentCallMessages(stack.llm)
	if msgs == nil {
		t.Fatal("expected ≥2 LLM calls")
	}

	// No turn should carry Parts for a plain-text request.
	for _, m := range msgs {
		if len(m.Parts) > 0 {
			t.Errorf("text-only request produced Parts in role=%q turn: %v", m.Role, m.Parts)
		}
	}

	// The user turn must still carry the original text via Content.
	var found bool
	for _, m := range msgs {
		if m.Role == "user" && strings.Contains(m.Content, "just text") {
			found = true
		}
	}
	if !found {
		t.Error("user turn with original text not found in agent LLM call messages")
	}
}
