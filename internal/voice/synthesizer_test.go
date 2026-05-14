package voice_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/marcoantonios1/Agent-OS/internal/voice"
)

func TestCostguardSynthesizer_Success(t *testing.T) {
	audioData := []byte("fake mp3 bytes")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "audio/mpeg")
		w.Write(audioData) //nolint:errcheck
	}))
	defer srv.Close()

	s := voice.NewCostguardSynthesizer(srv.URL, "", "alloy", "", "")
	got, mime, err := s.Synthesize(context.Background(), "hello world")
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if string(got) != string(audioData) {
		t.Errorf("audio = %q, want %q", got, audioData)
	}
	if mime != "audio/mpeg" {
		t.Errorf("mimeType = %q, want audio/mpeg", mime)
	}
}

func TestCostguardSynthesizer_VoiceParam(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		if body["voice"] != "nova" {
			t.Errorf("voice = %q, want nova", body["voice"])
		}
		if body["model"] != "tts-1" {
			t.Errorf("model = %q, want tts-1", body["model"])
		}
		if body["input"] != "test input" {
			t.Errorf("input = %q, want test input", body["input"])
		}
		w.Write([]byte("audio")) //nolint:errcheck
	}))
	defer srv.Close()

	s := voice.NewCostguardSynthesizer(srv.URL, "", "nova", "", "")
	if _, _, err := s.Synthesize(context.Background(), "test input"); err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
}

func TestCostguardSynthesizer_DefaultVoice(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body) //nolint:errcheck
		if body["voice"] != "alloy" {
			t.Errorf("default voice = %q, want alloy", body["voice"])
		}
		w.Write([]byte("audio")) //nolint:errcheck
	}))
	defer srv.Close()

	s := voice.NewCostguardSynthesizer(srv.URL, "", "", "") // empty voice → "alloy"
	if _, _, err := s.Synthesize(context.Background(), "hi"); err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
}

func TestCostguardSynthesizer_BearerTokenForwarded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth != "Bearer secret" {
			t.Errorf("Authorization = %q, want Bearer secret", auth)
		}
		w.Write([]byte("audio")) //nolint:errcheck
	}))
	defer srv.Close()

	s := voice.NewCostguardSynthesizer(srv.URL, "secret", "alloy", "", "")
	if _, _, err := s.Synthesize(context.Background(), "hi"); err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
}

func TestCostguardSynthesizer_NonOKStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "model not found", http.StatusInternalServerError)
	}))
	defer srv.Close()

	s := voice.NewCostguardSynthesizer(srv.URL, "", "alloy", "", "")
	_, _, err := s.Synthesize(context.Background(), "hi")
	if err == nil {
		t.Fatal("Synthesize should return error on non-200 status")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error %q should contain status 500", err.Error())
	}
}

func TestCostguardSynthesizer_TrailingSlashStripped(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if r.URL.Path != "/v1/audio/speech" {
			t.Errorf("path = %q, want /v1/audio/speech", r.URL.Path)
		}
		w.Write([]byte("audio")) //nolint:errcheck
	}))
	defer srv.Close()

	s := voice.NewCostguardSynthesizer(srv.URL+"/", "", "alloy", "", "")
	if _, _, err := s.Synthesize(context.Background(), "hi"); err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if !called {
		t.Error("handler was never called")
	}
}

func TestCostguardSynthesizer_ContentTypeStripsParams(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Server returns Content-Type with params; synthesizer should strip them.
		w.Header().Set("Content-Type", "audio/mpeg; charset=utf-8")
		w.Write([]byte("audio")) //nolint:errcheck
	}))
	defer srv.Close()

	s := voice.NewCostguardSynthesizer(srv.URL, "", "alloy", "", "")
	_, mime, err := s.Synthesize(context.Background(), "hi")
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if mime != "audio/mpeg" {
		t.Errorf("mimeType = %q, want audio/mpeg (params stripped)", mime)
	}
}

func TestCostguardSynthesizer_MissingContentType_DefaultsToMPEG(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Explicitly clear Content-Type before writing so the synthesizer
		// uses its own default. WriteHeader prevents auto-sniffing.
		w.Header()["Content-Type"] = nil
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("audio bytes")) //nolint:errcheck
	}))
	defer srv.Close()

	s := voice.NewCostguardSynthesizer(srv.URL, "", "alloy", "", "")
	_, mime, err := s.Synthesize(context.Background(), "hi")
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if mime != "audio/mpeg" {
		t.Errorf("mimeType = %q, want audio/mpeg", mime)
	}
}

// ── NoopSynthesizer ───────────────────────────────────────────────────────────

func TestNoopSynthesizer_ReturnsNil(t *testing.T) {
	n := &voice.NoopSynthesizer{}
	data, mime, err := n.Synthesize(context.Background(), "anything")
	if err != nil {
		t.Errorf("NoopSynthesizer.Synthesize err = %v, want nil", err)
	}
	if data != nil {
		t.Errorf("NoopSynthesizer.Synthesize data = %v, want nil", data)
	}
	if mime != "" {
		t.Errorf("NoopSynthesizer.Synthesize mimeType = %q, want empty", mime)
	}
}
