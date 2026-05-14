package voice_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/marcoantonios1/Agent-OS/internal/voice"
)

// ── CostguardTranscriber ──────────────────────────────────────────────────────

func TestCostguardTranscriber_Success(t *testing.T) {
	want := "this is the transcribed text"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", r.Method)
		}
		if !strings.HasSuffix(r.URL.Path, "/v1/audio/transcriptions") {
			t.Errorf("path = %q, want .../v1/audio/transcriptions", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"text": want}) //nolint:errcheck
	}))
	defer srv.Close()

	tr := voice.NewCostguardTranscriber(srv.URL, "")
	got, err := tr.Transcribe(context.Background(), []byte("fake ogg bytes"), "audio/ogg")
	if err != nil {
		t.Fatalf("Transcribe: %v", err)
	}
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestCostguardTranscriber_BearerTokenForwarded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth != "Bearer secret-key" {
			t.Errorf("Authorization = %q, want %q", auth, "Bearer secret-key")
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"text": "ok"}) //nolint:errcheck
	}))
	defer srv.Close()

	tr := voice.NewCostguardTranscriber(srv.URL, "secret-key")
	if _, err := tr.Transcribe(context.Background(), []byte("audio"), "audio/ogg"); err != nil {
		t.Fatalf("Transcribe: %v", err)
	}
}

func TestCostguardTranscriber_MultipartFormat(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ct := r.Header.Get("Content-Type")
		mediaType, params, err := mime.ParseMediaType(ct)
		if err != nil || mediaType != "multipart/form-data" {
			t.Errorf("Content-Type = %q, want multipart/form-data", ct)
		}

		mr := multipart.NewReader(r.Body, params["boundary"])
		fields := map[string]string{}
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatalf("read part: %v", err)
			}
			data, _ := io.ReadAll(part)
			fields[part.FormName()] = string(data)
		}

		if _, ok := fields["file"]; !ok {
			t.Error("multipart missing 'file' field")
		}
		if fields["model"] != "whisper-1" {
			t.Errorf("model field = %q, want whisper-1", fields["model"])
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"text": "ok"}) //nolint:errcheck
	}))
	defer srv.Close()

	tr := voice.NewCostguardTranscriber(srv.URL, "")
	if _, err := tr.Transcribe(context.Background(), []byte("fake audio"), "audio/ogg"); err != nil {
		t.Fatalf("Transcribe: %v", err)
	}
}

func TestCostguardTranscriber_MimeToFilename(t *testing.T) {
	cases := []struct {
		mimeType string
		wantExt  string
	}{
		{"audio/ogg", ".ogg"},
		{"audio/mpeg", ".mp3"},
		{"audio/wav", ".wav"},
		{"audio/webm", ".webm"},
		{"audio/mp4", ".mp4"},
		{"audio/unknown", ".ogg"}, // falls back to .ogg
	}

	for _, tc := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ct := r.Header.Get("Content-Type")
			_, params, _ := mime.ParseMediaType(ct)
			mr := multipart.NewReader(r.Body, params["boundary"])
			part, err := mr.NextPart()
			if err != nil {
				t.Errorf("read part: %v", err)
				return
			}
			filename := part.FileName()
			if !strings.HasSuffix(filename, tc.wantExt) {
				t.Errorf("mime %q: filename %q, want suffix %q", tc.mimeType, filename, tc.wantExt)
			}
			io.Copy(io.Discard, r.Body) //nolint:errcheck
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{"text": "ok"}) //nolint:errcheck
		}))

		tr := voice.NewCostguardTranscriber(srv.URL, "")
		tr.Transcribe(context.Background(), []byte("audio"), tc.mimeType) //nolint:errcheck
		srv.Close()
	}
}

func TestCostguardTranscriber_NonOKStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "model not found", http.StatusInternalServerError)
	}))
	defer srv.Close()

	tr := voice.NewCostguardTranscriber(srv.URL, "")
	_, err := tr.Transcribe(context.Background(), []byte("audio"), "audio/ogg")
	if err == nil {
		t.Fatal("Transcribe should return error on non-200 status")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error %q should contain status code 500", err.Error())
	}
}

func TestCostguardTranscriber_TrailingSlashStripped(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if r.URL.Path != "/v1/audio/transcriptions" {
			t.Errorf("path = %q, want /v1/audio/transcriptions", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"text": "ok"}) //nolint:errcheck
	}))
	defer srv.Close()

	// URL with trailing slash — should still hit the correct path.
	tr := voice.NewCostguardTranscriber(srv.URL+"/", "")
	if _, err := tr.Transcribe(context.Background(), []byte("a"), "audio/ogg"); err != nil {
		t.Fatalf("Transcribe: %v", err)
	}
	if !called {
		t.Error("handler was never called")
	}
}

// ── NoopTranscriber ───────────────────────────────────────────────────────────

func TestNoopTranscriber_ReturnsErrNotSupported(t *testing.T) {
	n := &voice.NoopTranscriber{}
	_, err := n.Transcribe(context.Background(), []byte("audio"), "audio/ogg")
	if !errors.Is(err, voice.ErrNotSupported) {
		t.Errorf("got %v, want ErrNotSupported", err)
	}
}

func TestNoopTranscriber_TextIsEmpty(t *testing.T) {
	n := &voice.NoopTranscriber{}
	text, _ := n.Transcribe(context.Background(), nil, "")
	if text != "" {
		t.Errorf("got %q, want empty string", text)
	}
}
