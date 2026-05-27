package costguard

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEmbed_HappyPath(t *testing.T) {
	var capturedPath string
	var capturedBody oaiEmbeddingRequest

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&capturedBody); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"data":[{"embedding":[0.1,0.2,0.3]}]}`)
	}))
	defer srv.Close()

	client := newTestClient(srv)
	got, err := client.Embed(context.Background(), "hello")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := []float32{0.1, 0.2, 0.3}
	if len(got) != len(want) {
		t.Fatalf("embedding length = %d, want %d", len(got), len(want))
	}
	for i, v := range want {
		if got[i] != v {
			t.Errorf("embedding[%d] = %v, want %v", i, got[i], v)
		}
	}

	if capturedPath != "/v1/embeddings" {
		t.Errorf("request path = %q, want %q", capturedPath, "/v1/embeddings")
	}
	if capturedBody.Input != "hello" {
		t.Errorf("request input = %q, want %q", capturedBody.Input, "hello")
	}
	if capturedBody.Model != "" {
		t.Errorf("request model = %q, want empty string", capturedBody.Model)
	}
}

func TestEmbed_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	client := newTestClient(srv)
	_, err := client.Embed(context.Background(), "hello")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error %q does not contain %q", err.Error(), "500")
	}
}
