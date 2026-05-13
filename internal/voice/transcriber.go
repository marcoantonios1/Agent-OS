// Package voice provides audio transcription for Agent OS channel handlers.
// CostguardTranscriber calls Costguard's /v1/audio/transcriptions endpoint,
// which is compatible with the OpenAI Whisper API (multipart/form-data).
// NoopTranscriber is the fallback when transcription is not configured.
package voice

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"
)

// ErrNotSupported is returned by NoopTranscriber. Channel handlers should
// reply with a helpful message when they receive this error.
var ErrNotSupported = errors.New("voice transcription is not configured")

// Transcriber converts raw audio bytes to text.
type Transcriber interface {
	Transcribe(ctx context.Context, audio []byte, mimeType string) (string, error)
}

// mimeToExt maps supported audio MIME types to file extensions. The filename
// field in the multipart request tells Whisper which codec to use.
var mimeToExt = map[string]string{
	"audio/ogg":  ".ogg",
	"audio/mpeg": ".mp3",
	"audio/mp4":  ".mp4",
	"audio/wav":  ".wav",
	"audio/webm": ".webm",
	"audio/opus": ".ogg",
	"audio/aac":  ".aac",
}

// ── CostguardTranscriber ──────────────────────────────────────────────────────

// CostguardTranscriber calls Costguard's /v1/audio/transcriptions endpoint,
// which proxies to Whisper (or any OpenAI-compatible transcription backend).
type CostguardTranscriber struct {
	baseURL string
	apiKey  string
	client  *http.Client
}

// NewCostguardTranscriber returns a CostguardTranscriber pointed at baseURL.
// apiKey is forwarded as a Bearer token when non-empty.
func NewCostguardTranscriber(baseURL, apiKey string) *CostguardTranscriber {
	return &CostguardTranscriber{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		client:  http.DefaultClient,
	}
}

// transcribeResponse is the JSON body returned by the Whisper-compatible
// transcription endpoint.
type transcribeResponse struct {
	Text string `json:"text"`
}

// Transcribe sends audio bytes to Costguard's transcription endpoint and
// returns the recognised text. mimeType is used to pick the file extension
// in the multipart payload — Whisper uses the filename to select the decoder.
func (t *CostguardTranscriber) Transcribe(ctx context.Context, audio []byte, mimeType string) (string, error) {
	ext := mimeToExt[mimeType]
	if ext == "" {
		ext = ".ogg" // safe default for Telegram/WhatsApp voice notes
	}
	filename := "voice" + ext

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)

	// File field with explicit Content-Type so Whisper can decode the audio.
	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename="%s"`, filename))
	h.Set("Content-Type", mimeType)
	fw, err := mw.CreatePart(h)
	if err != nil {
		return "", fmt.Errorf("voice: create multipart part: %w", err)
	}
	if _, err := fw.Write(audio); err != nil {
		return "", fmt.Errorf("voice: write audio bytes: %w", err)
	}

	if err := mw.WriteField("model", "whisper-1"); err != nil {
		return "", fmt.Errorf("voice: write model field: %w", err)
	}
	mw.Close()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		t.baseURL+"/v1/audio/transcriptions", &body)
	if err != nil {
		return "", fmt.Errorf("voice: build request: %w", err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if t.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+t.apiKey)
	}

	resp, err := t.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("voice: POST transcription: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("voice: transcription API returned %d: %s", resp.StatusCode, bytes.TrimSpace(body))
	}

	var out transcribeResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("voice: decode response: %w", err)
	}
	return out.Text, nil
}

// ── NoopTranscriber ───────────────────────────────────────────────────────────

// NoopTranscriber always returns ErrNotSupported. It is the default when
// VOICE_TRANSCRIPTION is not set to "enabled".
type NoopTranscriber struct{}

// Transcribe always returns ErrNotSupported.
func (n *NoopTranscriber) Transcribe(_ context.Context, _ []byte, _ string) (string, error) {
	return "", ErrNotSupported
}
