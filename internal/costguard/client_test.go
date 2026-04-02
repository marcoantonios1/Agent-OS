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

func TestComplete_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/complete" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("missing or wrong auth header")
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(apiResponse{
			Content: "world",
			Usage: struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
				TotalTokens      int `json:"total_tokens"`
			}{PromptTokens: 5, CompletionTokens: 3, TotalTokens: 8},
		})
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
			// First two attempts fail with 503.
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		// Third attempt succeeds.
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(apiResponse{Content: "ok on attempt 3"})
	}))
	defer srv.Close()

	client := newTestClient(srv)
	// Shorten backoff so the test runs quickly.
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

func TestStream_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/stream" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)

		chunks := []string{"hello", " world"}
		for i, c := range chunks {
			data, _ := json.Marshal(sseChunk{Content: c, Done: i == len(chunks)-1})
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
		fmt.Fprintf(w, "data: {\"content\":\"hi\",\"done\":false}\n\n")
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

// Compile-time check: *Client satisfies LLMClient.
var _ LLMClient = (*Client)(nil)
