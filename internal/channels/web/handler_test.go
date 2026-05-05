package web_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/marcoantonios1/Agent-OS/internal/approval"
	"github.com/marcoantonios1/Agent-OS/internal/channels/web"
	"github.com/marcoantonios1/Agent-OS/internal/memory"
	"github.com/marcoantonios1/Agent-OS/internal/router"
	"github.com/marcoantonios1/Agent-OS/internal/types"
)

// --- test doubles ---

type stubClassifier struct{ intent router.Intent }

func (s *stubClassifier) Classify(_ context.Context, _, _ string, _ []types.ConversationTurn) ([]router.Intent, error) {
	return []router.Intent{s.intent}, nil
}

type recordingAgent struct {
	output string
	calls  []types.AgentRequest
}

func (a *recordingAgent) Handle(_ context.Context, req types.AgentRequest) (types.AgentResponse, error) {
	a.calls = append(a.calls, req)
	return types.AgentResponse{AgentID: "stub", Output: a.output}, nil
}

type panicAgent struct{}

func (p *panicAgent) Handle(_ context.Context, _ types.AgentRequest) (types.AgentResponse, error) {
	panic("something went very wrong")
}

// --- builder ---

type testFixture struct {
	srv   *httptest.Server
	agent *recordingAgent
}

// stubReadiness is a test ReadinessChecker whose error is configurable.
type stubReadiness struct{ err error }

func (s *stubReadiness) Ping(_ context.Context) error { return s.err }

func newFixture(t *testing.T, intent router.Intent, agentOutput string) *testFixture {
	t.Helper()
	store := memory.NewStore()
	agent := &recordingAgent{output: agentOutput}
	r := router.New(
		&stubClassifier{intent: intent},
		map[router.Intent]router.Agent{intent: agent},
		store,
		approval.NewMemoryStore(),
	)
	srv := httptest.NewServer(web.NewHandler(r, nil))
	t.Cleanup(srv.Close)
	t.Cleanup(store.Close)
	return &testFixture{srv: srv, agent: agent}
}

func post(t *testing.T, srv *httptest.Server, body any) *http.Response {
	t.Helper()
	b, _ := json.Marshal(body)
	resp, err := http.Post(srv.URL+"/v1/chat", "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatalf("POST /v1/chat: %v", err)
	}
	return resp
}

func decodeChat(t *testing.T, resp *http.Response) map[string]string {
	t.Helper()
	var m map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	resp.Body.Close()
	return m
}

// --- tests ---

func TestHealthz(t *testing.T) {
	f := newFixture(t, router.IntentComms, "ok")
	resp, err := http.Get(f.srv.URL + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: got %d, want 200", resp.StatusCode)
	}
	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode /healthz body: %v", err)
	}
	if body["status"] != "ok" {
		t.Errorf(`status field: got %q, want "ok"`, body["status"])
	}
}

func TestReadyz_Available(t *testing.T) {
	store := memory.NewStore()
	defer store.Close()
	r := router.New(
		&stubClassifier{intent: router.IntentComms},
		map[router.Intent]router.Agent{},
		store,
		approval.NewMemoryStore(),
	)
	srv := httptest.NewServer(web.NewHandler(r, &stubReadiness{err: nil}))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/readyz")
	if err != nil {
		t.Fatalf("GET /readyz: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: got %d, want 200", resp.StatusCode)
	}
	var body map[string]string
	json.NewDecoder(resp.Body).Decode(&body) //nolint:errcheck
	if body["status"] != "ok" {
		t.Errorf(`status field: got %q, want "ok"`, body["status"])
	}
}

func TestReadyz_Unavailable(t *testing.T) {
	store := memory.NewStore()
	defer store.Close()
	r := router.New(
		&stubClassifier{intent: router.IntentComms},
		map[router.Intent]router.Agent{},
		store,
		approval.NewMemoryStore(),
	)
	pingErr := errors.New("connection refused")
	srv := httptest.NewServer(web.NewHandler(r, &stubReadiness{err: pingErr}))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/readyz")
	if err != nil {
		t.Fatalf("GET /readyz: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status: got %d, want 503", resp.StatusCode)
	}
	var body map[string]string
	json.NewDecoder(resp.Body).Decode(&body) //nolint:errcheck
	if body["status"] != "unavailable" {
		t.Errorf(`status field: got %q, want "unavailable"`, body["status"])
	}
	if body["reason"] == "" {
		t.Error("reason field should be non-empty on 503")
	}
}

func TestReadyz_NoChecker_AlwaysOK(t *testing.T) {
	f := newFixture(t, router.IntentComms, "ok")
	resp, err := http.Get(f.srv.URL + "/readyz")
	if err != nil {
		t.Fatalf("GET /readyz: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: got %d, want 200 when no checker configured", resp.StatusCode)
	}
}

func TestChat_Success(t *testing.T) {
	f := newFixture(t, router.IntentComms, "Hello from comms agent")

	resp := post(t, f.srv, map[string]string{
		"session_id": "s1",
		"user_id":    "u1",
		"text":       "Send an email to Alice",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("got status %d, want 200", resp.StatusCode)
	}
	body := decodeChat(t, resp)
	if body["text"] != "Hello from comms agent" {
		t.Errorf("got text %q, want %q", body["text"], "Hello from comms agent")
	}
	if body["session_id"] != "s1" {
		t.Errorf("got session_id %q, want %q", body["session_id"], "s1")
	}
}

func TestChat_RequestIDHeader(t *testing.T) {
	f := newFixture(t, router.IntentComms, "hi")

	resp := post(t, f.srv, map[string]string{
		"session_id": "s-rid",
		"user_id":    "u1",
		"text":       "hello",
	})
	defer resp.Body.Close()
	if resp.Header.Get("X-Request-ID") == "" {
		t.Error("expected X-Request-ID header to be set")
	}
}

func TestChat_HonoursIncomingRequestID(t *testing.T) {
	f := newFixture(t, router.IntentComms, "hi")

	b, _ := json.Marshal(map[string]string{"session_id": "s2", "user_id": "u1", "text": "hi"})
	req, _ := http.NewRequest(http.MethodPost, f.srv.URL+"/v1/chat", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-ID", "my-custom-id")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request error: %v", err)
	}
	defer resp.Body.Close()
	if got := resp.Header.Get("X-Request-ID"); got != "my-custom-id" {
		t.Errorf("got X-Request-ID %q, want %q", got, "my-custom-id")
	}
}

func TestChat_MultiTurn_HistoryPreserved(t *testing.T) {
	f := newFixture(t, router.IntentBuilder, "")
	sessionID := "multi-turn"

	f.agent.output = "first reply"
	post(t, f.srv, map[string]string{
		"session_id": sessionID,
		"user_id":    "u1",
		"text":       "first message",
	}).Body.Close()

	f.agent.output = "second reply"
	resp := post(t, f.srv, map[string]string{
		"session_id": sessionID,
		"user_id":    "u1",
		"text":       "second message",
	})
	body := decodeChat(t, resp)
	if body["text"] != "second reply" {
		t.Errorf("got %q, want %q", body["text"], "second reply")
	}

	// The agent must have been called twice total.
	if len(f.agent.calls) != 2 {
		t.Fatalf("got %d agent calls, want 2", len(f.agent.calls))
	}
	// On the second call the agent must see the first exchange in its history.
	// History passed to agent = [user:first, assistant:first reply, user:second]
	h := f.agent.calls[1].History
	if len(h) != 3 {
		t.Fatalf("second call history len = %d, want 3", len(h))
	}
	if h[0].Role != "user" || h[0].Content != "first message" {
		t.Errorf("h[0] = %+v", h[0])
	}
	if h[1].Role != "assistant" || h[1].Content != "first reply" {
		t.Errorf("h[1] = %+v", h[1])
	}
	if h[2].Role != "user" || h[2].Content != "second message" {
		t.Errorf("h[2] = %+v", h[2])
	}
}

// --- validation tests ---

func TestChat_MissingSessionID(t *testing.T) {
	f := newFixture(t, router.IntentComms, "ok")
	resp := post(t, f.srv, map[string]string{"user_id": "u1", "text": "hi"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("got %d, want 400", resp.StatusCode)
	}
}

func TestChat_MissingUserID(t *testing.T) {
	f := newFixture(t, router.IntentComms, "ok")
	resp := post(t, f.srv, map[string]string{"session_id": "s1", "text": "hi"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("got %d, want 400", resp.StatusCode)
	}
}

func TestChat_MissingText(t *testing.T) {
	f := newFixture(t, router.IntentComms, "ok")
	resp := post(t, f.srv, map[string]string{"session_id": "s1", "user_id": "u1"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("got %d, want 400", resp.StatusCode)
	}
}

func TestChat_InvalidJSON(t *testing.T) {
	f := newFixture(t, router.IntentComms, "ok")
	resp, err := http.Post(f.srv.URL+"/v1/chat", "application/json",
		strings.NewReader("{not valid json"))
	if err != nil {
		t.Fatalf("request error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("got %d, want 400", resp.StatusCode)
	}
}

func TestChat_ValidationErrorBody(t *testing.T) {
	f := newFixture(t, router.IntentComms, "ok")
	resp := post(t, f.srv, map[string]string{"user_id": "u1", "text": "hi"})
	var body map[string]string
	json.NewDecoder(resp.Body).Decode(&body)
	resp.Body.Close()
	if body["error"] == "" {
		t.Error("expected non-empty error field in 400 response")
	}
}

func TestChat_PanicRecovery(t *testing.T) {
	store := memory.NewStore()
	defer store.Close()

	r := router.New(
		&stubClassifier{intent: router.IntentComms},
		map[router.Intent]router.Agent{router.IntentComms: &panicAgent{}},
		store,
		approval.NewMemoryStore(),
	)
	srv := httptest.NewServer(web.NewHandler(r, nil))
	defer srv.Close()

	resp := post(t, srv, map[string]string{
		"session_id": "panic-sess",
		"user_id":    "u1",
		"text":       "trigger panic",
	})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("got %d, want 500 after panic", resp.StatusCode)
	}
}

func TestChat_UnknownIntent_NoError(t *testing.T) {
	store := memory.NewStore()
	defer store.Close()

	r := router.New(
		&stubClassifier{intent: router.IntentUnknown},
		map[router.Intent]router.Agent{},
		store,
		approval.NewMemoryStore(),
	)
	srv := httptest.NewServer(web.NewHandler(r, nil))
	defer srv.Close()

	resp := post(t, srv, map[string]string{
		"session_id": "s-unk",
		"user_id":    "u1",
		"text":       "🤔",
	})
	body := decodeChat(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("got %d, want 200", resp.StatusCode)
	}
	if body["text"] == "" {
		t.Error("expected fallback message, got empty text")
	}
}

// ── attachment tests ──────────────────────────────────────────────────────────

// buildTestPDF creates a minimal valid multi-page PDF for tests.
// pageStreams are raw PDF content-stream strings; use simple ASCII text only.
func buildTestPDF(pageStreams []string) []byte {
	var buf bytes.Buffer
	write := func(s string) { buf.WriteString(s) }

	write("%PDF-1.4\n")
	nPages := len(pageStreams)
	fontObj := 2*nPages + 3
	contentStart := nPages + 3

	offsets := make([]int, 0, 2+2*nPages+1)

	offsets = append(offsets, buf.Len())
	write("1 0 obj\n<</Type /Catalog /Pages 2 0 R>>\nendobj\n")

	kids := make([]string, nPages)
	for i := range pageStreams {
		kids[i] = fmt.Sprintf("%d 0 R", 3+i)
	}
	offsets = append(offsets, buf.Len())
	write(fmt.Sprintf("2 0 obj\n<</Type /Pages /Kids [%s] /Count %d>>\nendobj\n",
		strings.Join(kids, " "), nPages))

	for i := range pageStreams {
		offsets = append(offsets, buf.Len())
		write(fmt.Sprintf(
			"%d 0 obj\n<</Type /Page /Parent 2 0 R /MediaBox [0 0 612 792]"+
				" /Contents %d 0 R /Resources <</Font <</F1 %d 0 R>>>>>>\nendobj\n",
			3+i, contentStart+i, fontObj))
	}
	for i, stream := range pageStreams {
		offsets = append(offsets, buf.Len())
		write(fmt.Sprintf("%d 0 obj\n<</Length %d>>\nstream\n%sendstream\nendobj\n",
			contentStart+i, len(stream), stream))
	}
	offsets = append(offsets, buf.Len())
	write(fmt.Sprintf(
		"%d 0 obj\n<</Type /Font /Subtype /Type1 /BaseFont /Helvetica>>\nendobj\n", fontObj))

	xrefPos := buf.Len()
	nObjs := len(offsets) + 1
	write(fmt.Sprintf("xref\n0 %d\n", nObjs))
	write("0000000000 65535 f \n")
	for _, off := range offsets {
		write(fmt.Sprintf("%010d 00000 n \n", off))
	}
	write(fmt.Sprintf("trailer\n<</Size %d /Root 1 0 R>>\nstartxref\n%d\n", nObjs, xrefPos))
	write("%%EOF\n")
	return buf.Bytes()
}

func postWithAttachments(t *testing.T, srv *httptest.Server, body any) *http.Response {
	t.Helper()
	b, _ := json.Marshal(body)
	resp, err := http.Post(srv.URL+"/v1/chat", "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatalf("POST /v1/chat: %v", err)
	}
	return resp
}

func TestChat_ImageAttachment(t *testing.T) {
	f := newFixture(t, router.IntentResearch, "ok")

	// A minimal 1×1 red pixel PNG (valid image bytes).
	// Using arbitrary bytes is fine — the handler doesn't validate image content.
	imgBytes := []byte{
		0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, // PNG magic
		0x00, 0x00, 0x00, 0x01, // width = 1
	}
	encoded := base64.StdEncoding.EncodeToString(imgBytes)

	resp := postWithAttachments(t, f.srv, map[string]any{
		"session_id": "s-img",
		"user_id":    "u1",
		"text":       "What does this image show?",
		"attachments": []map[string]string{
			{"data": encoded, "mime_type": "image/png", "filename": "test.png"},
		},
	})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("got status %d, want 200", resp.StatusCode)
	}
	if len(f.agent.calls) == 0 {
		t.Fatal("agent was not called")
	}

	// The last history turn is the user turn with Parts set.
	req := f.agent.calls[0]
	userTurn := req.History[len(req.History)-1]
	if len(userTurn.Parts) == 0 {
		t.Fatal("expected Parts on user turn, got none")
	}
	// First part is the user's text, second is the image.
	if userTurn.Parts[0].Type != "text" {
		t.Errorf("parts[0].Type = %q, want text", userTurn.Parts[0].Type)
	}
	imgPart := userTurn.Parts[1]
	if imgPart.Type != "image" {
		t.Errorf("parts[1].Type = %q, want image", imgPart.Type)
	}
	if imgPart.MimeType != "image/png" {
		t.Errorf("parts[1].MimeType = %q, want image/png", imgPart.MimeType)
	}
	if imgPart.ImageData != encoded {
		t.Error("image data not preserved in ContentPart")
	}
}

func TestChat_PDFAttachment(t *testing.T) {
	f := newFixture(t, router.IntentResearch, "ok")

	pdfBytes := buildTestPDF([]string{"BT /F1 12 Tf 72 720 Td (Invoice total 100) Tj ET\n"})
	encoded := base64.StdEncoding.EncodeToString(pdfBytes)

	resp := postWithAttachments(t, f.srv, map[string]any{
		"session_id": "s-pdf",
		"user_id":    "u1",
		"text":       "Summarise this invoice",
		"attachments": []map[string]string{
			{"data": encoded, "mime_type": "application/pdf", "filename": "invoice.pdf"},
		},
	})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("got status %d, want 200", resp.StatusCode)
	}
	if len(f.agent.calls) == 0 {
		t.Fatal("agent was not called")
	}

	req := f.agent.calls[0]
	userTurn := req.History[len(req.History)-1]
	if len(userTurn.Parts) < 2 {
		t.Fatalf("expected at least 2 parts, got %d", len(userTurn.Parts))
	}
	pdfPart := userTurn.Parts[1]
	if pdfPart.Type != "text" {
		t.Errorf("pdf part type = %q, want text", pdfPart.Type)
	}
	if !strings.Contains(pdfPart.Text, "Invoice total 100") {
		t.Errorf("pdf part text %q does not contain expected content", pdfPart.Text)
	}
	if pdfPart.Filename != "invoice.pdf" {
		t.Errorf("filename = %q, want invoice.pdf", pdfPart.Filename)
	}
}

func TestChat_InvalidMimeType_400(t *testing.T) {
	f := newFixture(t, router.IntentResearch, "ok")

	resp := postWithAttachments(t, f.srv, map[string]any{
		"session_id": "s-mime",
		"user_id":    "u1",
		"text":       "hi",
		"attachments": []map[string]string{
			{"data": base64.StdEncoding.EncodeToString([]byte("data")), "mime_type": "application/zip"},
		},
	})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("got %d, want 400", resp.StatusCode)
	}
	var body map[string]string
	json.NewDecoder(resp.Body).Decode(&body) //nolint:errcheck
	if !strings.Contains(body["error"], "unsupported attachment type") {
		t.Errorf("error = %q, want 'unsupported attachment type: ...'", body["error"])
	}
}

func TestChat_TooManyAttachments_400(t *testing.T) {
	f := newFixture(t, router.IntentResearch, "ok")

	encoded := base64.StdEncoding.EncodeToString([]byte("x"))
	atts := make([]map[string]string, 6) // one over the limit of 5
	for i := range atts {
		atts[i] = map[string]string{"data": encoded, "mime_type": "image/png"}
	}

	resp := postWithAttachments(t, f.srv, map[string]any{
		"session_id":  "s-many",
		"user_id":     "u1",
		"text":        "hi",
		"attachments": atts,
	})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("got %d, want 400", resp.StatusCode)
	}
	var body map[string]string
	json.NewDecoder(resp.Body).Decode(&body) //nolint:errcheck
	if !strings.Contains(body["error"], "too many attachments") {
		t.Errorf("error = %q, want 'too many attachments'", body["error"])
	}
}

func TestChat_OversizedImage_400(t *testing.T) {
	f := newFixture(t, router.IntentResearch, "ok")

	// 5 MB + 1 byte decoded → exceeds maxImageBytes.
	big := make([]byte, 5*1024*1024+1)
	encoded := base64.StdEncoding.EncodeToString(big)

	resp := postWithAttachments(t, f.srv, map[string]any{
		"session_id": "s-big",
		"user_id":    "u1",
		"text":       "hi",
		"attachments": []map[string]string{
			{"data": encoded, "mime_type": "image/jpeg", "filename": "big.jpg"},
		},
	})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("got %d, want 400", resp.StatusCode)
	}
}

func TestChat_InvalidBase64_400(t *testing.T) {
	f := newFixture(t, router.IntentResearch, "ok")

	resp := postWithAttachments(t, f.srv, map[string]any{
		"session_id": "s-b64",
		"user_id":    "u1",
		"text":       "hi",
		"attachments": []map[string]string{
			{"data": "not!!valid!!base64", "mime_type": "image/png"},
		},
	})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("got %d, want 400", resp.StatusCode)
	}
}

func TestChat_NoAttachments_IdenticalBehavior(t *testing.T) {
	// Sending no attachments field must behave exactly like the pre-attachment handler.
	f := newFixture(t, router.IntentComms, "plain text reply")

	resp := post(t, f.srv, map[string]string{
		"session_id": "s-plain",
		"user_id":    "u1",
		"text":       "hello",
	})
	body := decodeChat(t, resp)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("got status %d, want 200", resp.StatusCode)
	}
	if body["text"] != "plain text reply" {
		t.Errorf("got %q, want plain text reply", body["text"])
	}
	req := f.agent.calls[0]
	userTurn := req.History[len(req.History)-1]
	if userTurn.Parts != nil {
		t.Error("Parts should be nil for a text-only request")
	}
}
