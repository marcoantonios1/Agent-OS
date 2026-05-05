package costguard

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/marcoantonios1/Agent-OS/internal/types"
)

// completionReq is a helper to build a minimal CompletionRequest for tests.
func completionReq() CompletionRequest {
	return CompletionRequest{
		Model: "test-model",
		Messages: []types.ConversationTurn{
			{Role: "user", Content: "hello"},
		},
		MaxTokens: 100,
	}
}

// newTestClient wires a Client to the given test server.
func newTestClient(srv *httptest.Server) *Client {
	return New(srv.URL, "test-key")
}

// oaiResponseWith builds a minimal OpenAI-format response with the given text content.
func oaiResponseWith(content string) oaiResponse {
	c := content
	return oaiResponse{
		Choices: []struct {
			Message oaiMessage `json:"message"`
		}{
			{Message: oaiMessage{Role: "assistant", Content: &c}},
		},
		Usage: struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		}{PromptTokens: 5, CompletionTokens: 3, TotalTokens: 8},
	}
}

func TestComplete_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("missing or wrong auth header")
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(oaiResponseWith("world"))
	}))
	defer srv.Close()

	client := newTestClient(srv)
	resp, err := client.Complete(context.Background(), completionReq())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "world" {
		t.Errorf("got content %q, want %q", resp.Content, "world")
	}
	if resp.Usage.TotalTokens != 8 {
		t.Errorf("got total tokens %d, want 8", resp.Usage.TotalTokens)
	}
}

func TestComplete_RetryOnServerError(t *testing.T) {
	var calls atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(oaiResponseWith("ok on attempt 3"))
	}))
	defer srv.Close()

	client := newTestClient(srv)
	client.httpClient.Timeout = 5 * time.Second

	resp, err := client.Complete(context.Background(), completionReq())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "ok on attempt 3" {
		t.Errorf("got %q, want %q", resp.Content, "ok on attempt 3")
	}
	if calls.Load() != 3 {
		t.Errorf("got %d calls, want 3", calls.Load())
	}
}

func TestComplete_AllRetriesExhausted(t *testing.T) {
	var calls atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	client := newTestClient(srv)

	_, err := client.Complete(context.Background(), completionReq())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if calls.Load() != maxAttempts {
		t.Errorf("got %d calls, want %d", calls.Load(), maxAttempts)
	}
}

func TestComplete_NonRetryableError(t *testing.T) {
	var calls atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadRequest) // 400 — not retryable
	}))
	defer srv.Close()

	client := newTestClient(srv)

	_, err := client.Complete(context.Background(), completionReq())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if calls.Load() != 1 {
		t.Errorf("got %d calls, want 1 (no retry on 400)", calls.Load())
	}
}

func TestComplete_ContextCancelled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	client := newTestClient(srv)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	_, err := client.Complete(ctx, completionReq())
	if err == nil {
		t.Fatal("expected context error, got nil")
	}
}

func TestComplete_ToolCallsDecoded(t *testing.T) {
	args := `{"limit":5}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := oaiResponse{
			Choices: []struct {
				Message oaiMessage `json:"message"`
			}{
				{Message: oaiMessage{
					Role: "assistant",
					ToolCalls: []oaiToolCall{{
						ID:   "call_1",
						Type: "function",
						Function: oaiFuncCall{Name: "email_list", Arguments: args},
					}},
				}},
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	client := newTestClient(srv)
	resp, err := client.Complete(context.Background(), completionReq())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("got %d tool calls, want 1", len(resp.ToolCalls))
	}
	if resp.ToolCalls[0].Name != "email_list" {
		t.Errorf("tool name = %q, want email_list", resp.ToolCalls[0].Name)
	}
	if resp.ToolCalls[0].Arguments != args {
		t.Errorf("arguments = %q, want %q", resp.ToolCalls[0].Arguments, args)
	}
}

func TestStream_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)

		chunks := []struct{ content, finish string }{
			{"hello", ""},
			{" world", "stop"},
		}
		for _, c := range chunks {
			data, _ := json.Marshal(oaiStreamDelta{
				Choices: []struct {
					Delta struct {
						Content   string        `json:"content"`
						ToolCalls []oaiToolCall `json:"tool_calls"`
					} `json:"delta"`
					FinishReason string `json:"finish_reason"`
				}{{
					Delta:        struct{ Content string `json:"content"`; ToolCalls []oaiToolCall `json:"tool_calls"` }{Content: c.content},
					FinishReason: c.finish,
				}},
			})
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}
	}))
	defer srv.Close()

	client := newTestClient(srv)
	ch, err := client.Stream(context.Background(), completionReq())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var got string
	for chunk := range ch {
		if chunk.Error != nil {
			t.Fatalf("stream error: %v", chunk.Error)
		}
		got += chunk.Content
	}
	if got != "hello world" {
		t.Errorf("got %q, want %q", got, "hello world")
	}
}

func TestStream_DoneMarker(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"},\"finish_reason\":\"\"}]}\n\n")
		flusher.Flush()
		fmt.Fprintf(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer srv.Close()

	client := newTestClient(srv)
	ch, err := client.Stream(context.Background(), completionReq())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var chunks []StreamChunk
	for c := range ch {
		chunks = append(chunks, c)
	}
	if len(chunks) != 2 {
		t.Fatalf("got %d chunks, want 2", len(chunks))
	}
	if chunks[0].Content != "hi" {
		t.Errorf("got content %q, want %q", chunks[0].Content, "hi")
	}
	if !chunks[1].Done {
		t.Error("last chunk should have Done=true")
	}
}

// ── toOAIMessage tests ────────────────────────────────────────────────────────

func TestToOAIMessage_TextOnly(t *testing.T) {
	turn := types.ConversationTurn{Role: "user", Content: "hello"}
	raw := toOAIMessage(turn)

	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["role"] != "user" {
		t.Errorf("role = %v, want user", got["role"])
	}
	if got["content"] != "hello" {
		t.Errorf("content = %v, want hello", got["content"])
	}
	if _, ok := got["parts"]; ok {
		t.Error("unexpected 'parts' key in text-only message")
	}
}

func TestToOAIMessage_VisionParts(t *testing.T) {
	turn := types.ConversationTurn{
		Role: "user",
		Parts: []types.ContentPart{
			{Type: "text", Text: "What does this show?"},
			{Type: "image", ImageData: "abc123", MimeType: "image/jpeg"},
		},
	}
	raw := toOAIMessage(turn)

	var got struct {
		Role    string `json:"role"`
		Content []struct {
			Type     string `json:"type"`
			Text     string `json:"text,omitempty"`
			ImageURL *struct {
				URL string `json:"url"`
			} `json:"image_url,omitempty"`
		} `json:"content"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Role != "user" {
		t.Errorf("role = %q, want user", got.Role)
	}
	if len(got.Content) != 2 {
		t.Fatalf("content parts = %d, want 2", len(got.Content))
	}
	if got.Content[0].Type != "text" || got.Content[0].Text != "What does this show?" {
		t.Errorf("part[0] = %+v", got.Content[0])
	}
	if got.Content[1].Type != "image_url" {
		t.Errorf("part[1].type = %q, want image_url", got.Content[1].Type)
	}
	wantURL := "data:image/jpeg;base64,abc123"
	if got.Content[1].ImageURL == nil || got.Content[1].ImageURL.URL != wantURL {
		t.Errorf("part[1].image_url.url = %v, want %q", got.Content[1].ImageURL, wantURL)
	}
}

func TestToOAIMessage_DocumentPart(t *testing.T) {
	turn := types.ConversationTurn{
		Role: "user",
		Parts: []types.ContentPart{
			{Type: "document", ImageData: "pdfbytes", MimeType: "application/pdf", Filename: "invoice.pdf"},
		},
	}
	raw := toOAIMessage(turn)

	var got struct {
		Content []struct {
			Type     string `json:"type"`
			ImageURL *struct {
				URL string `json:"url"`
			} `json:"image_url,omitempty"`
		} `json:"content"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Content) != 1 {
		t.Fatalf("content parts = %d, want 1", len(got.Content))
	}
	wantURL := "data:application/pdf;base64,pdfbytes"
	if got.Content[0].ImageURL == nil || got.Content[0].ImageURL.URL != wantURL {
		t.Errorf("url = %v, want %q", got.Content[0].ImageURL, wantURL)
	}
}

func TestToOAIMessage_PartsIgnoreContent(t *testing.T) {
	// When Parts is set, Content string must be ignored.
	turn := types.ConversationTurn{
		Role:    "user",
		Content: "this should be ignored",
		Parts:   []types.ContentPart{{Type: "text", Text: "from parts"}},
	}
	raw := toOAIMessage(turn)

	var got struct {
		Content []struct {
			Text string `json:"text,omitempty"`
		} `json:"content"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Content) != 1 || got.Content[0].Text != "from parts" {
		t.Errorf("expected single part with text 'from parts', got %+v", got.Content)
	}
}

// Compile-time check: *Client satisfies LLMClient.
var _ LLMClient = (*Client)(nil)
