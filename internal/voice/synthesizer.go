package voice

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Synthesizer converts text to audio.
// Synthesize returns (nil, "", nil) when the implementation is a no-op — callers
// should treat a nil audio slice as "send text instead".
type Synthesizer interface {
	Synthesize(ctx context.Context, text string) (audio []byte, mimeType string, err error)
}

// CostguardSynthesizer posts text to a Costguard /v1/audio/speech endpoint
// (OpenAI TTS API compatible) and returns the raw audio bytes.
type CostguardSynthesizer struct {
	baseURL string
	apiKey  string
	voice   string
	client  *http.Client
}

// NewCostguardSynthesizer returns a Synthesizer backed by the Costguard gateway.
// voice is the voice name (e.g. "alloy", "nova", "shimmer"); defaults to "alloy"
// when empty.
func NewCostguardSynthesizer(baseURL, apiKey, voice string) *CostguardSynthesizer {
	if voice == "" {
		voice = "alloy"
	}
	return &CostguardSynthesizer{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		voice:   voice,
		client:  http.DefaultClient,
	}
}

// Synthesize sends text to the TTS endpoint and returns the audio bytes with the
// MIME type reported by the server (typically "audio/mpeg").
func (s *CostguardSynthesizer) Synthesize(ctx context.Context, text string) ([]byte, string, error) {
	body, err := json.Marshal(map[string]string{
		"model": "tts-1",
		"input": text,
		"voice": s.voice,
	})
	if err != nil {
		return nil, "", fmt.Errorf("tts: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		s.baseURL+"/v1/audio/speech", bytes.NewReader(body))
	if err != nil {
		return nil, "", fmt.Errorf("tts: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if s.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+s.apiKey)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("tts: request: %w", err)
	}
	data, readErr := io.ReadAll(resp.Body)
	resp.Body.Close()
	if readErr != nil {
		return nil, "", fmt.Errorf("tts: read body: %w", readErr)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("tts: server returned %d: %s", resp.StatusCode, string(data))
	}

	mimeType := resp.Header.Get("Content-Type")
	if mimeType == "" {
		mimeType = "audio/mpeg"
	}
	// Strip parameters ("audio/mpeg; charset=utf-8" → "audio/mpeg").
	if idx := strings.IndexByte(mimeType, ';'); idx >= 0 {
		mimeType = strings.TrimSpace(mimeType[:idx])
	}

	return data, mimeType, nil
}

// NoopSynthesizer is the default when VOICE_TTS is not enabled.
// It always returns (nil, "", nil); callers interpret nil audio as "send text".
type NoopSynthesizer struct{}

func (n *NoopSynthesizer) Synthesize(_ context.Context, _ string) ([]byte, string, error) {
	return nil, "", nil
}
