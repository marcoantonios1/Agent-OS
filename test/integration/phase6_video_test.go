package integration

// Phase 6 integration tests: cross-channel video processing pipeline.
//
//  1. FramesExtracted_ReachLLM      — video bytes → ffmpeg → image ContentParts → dispatcher.
//  2. OversizedVideo_NotifiesUser   — size gate fires before ffmpeg; user sees friendly message.
//  3. FrameCount_RespectsMaxFrames  — ExtractFrames never exceeds configured cap.
//  4. FfmpegUnavailable_NotifiesUser — graceful degradation when ffmpeg is missing from PATH.

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"github.com/marcoantonios1/Agent-OS/internal/attachments"
	"github.com/marcoantonios1/Agent-OS/internal/channels/telegram"
	"github.com/marcoantonios1/Agent-OS/internal/types"
)

// ── test helpers ──────────────────────────────────────────────────────────────

// requireFFmpegVideo skips the test when ffmpeg is not in PATH.
func requireFFmpegVideo(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not found in PATH — skipping video integration test")
	}
}

// makeIntegrationVideo generates a small black MP4 via ffmpeg.
// Skips the test if ffmpeg cannot produce a valid file.
func makeIntegrationVideo(t *testing.T, durationSecs int) []byte {
	t.Helper()
	requireFFmpegVideo(t)

	tmp, err := os.CreateTemp("", "agentos-inttest-video-*.mp4")
	if err != nil {
		t.Fatalf("makeIntegrationVideo: create temp: %v", err)
	}
	tmp.Close()
	defer os.Remove(tmp.Name())

	cmd := exec.Command("ffmpeg",
		"-f", "lavfi",
		"-i", fmt.Sprintf("color=black:size=64x64:duration=%d", durationSecs),
		"-c:v", "libx264", "-pix_fmt", "yuv420p",
		"-t", strconv.Itoa(durationSecs),
		"-y", tmp.Name(),
	)
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Run(); err != nil {
		t.Skipf("makeIntegrationVideo: ffmpeg could not generate test video: %v", err)
	}

	data, err := os.ReadFile(tmp.Name())
	if err != nil {
		t.Fatalf("makeIntegrationVideo: read: %v", err)
	}
	return data
}

// videoHTTPServer starts an httptest.Server that returns videoData as video/mp4
// for every request, regardless of path.
func videoHTTPServer(t *testing.T, videoData []byte) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		w.Write(videoData) //nolint:errcheck
	}))
}

// videoTGBot wraps mockTGBot and overrides GetFileDirectURL to return a fixed URL.
// All other BotAPI methods delegate to the embedded *mockTGBot.
type videoTGBot struct {
	*mockTGBot
	fileURL string
}

func (m *videoTGBot) GetFileDirectURL(_ string) (string, error) {
	return m.fileURL, nil
}

var _ telegram.BotAPI = (*videoTGBot)(nil)

// tgMessageText extracts text from a tgbotapi.MessageConfig.
// Returns ("", false) for any other Chattable type.
func tgMessageText(c tgbotapi.Chattable) (string, bool) {
	mc, ok := c.(tgbotapi.MessageConfig)
	if !ok {
		return "", false
	}
	return mc.Text, true
}

// containsVideoReply returns true when any message in sent contains substr.
func containsVideoReply(sent []tgbotapi.Chattable, substr string) bool {
	for _, c := range sent {
		if text, ok := tgMessageText(c); ok && strings.Contains(text, substr) {
			return true
		}
	}
	return false
}

// ── Test 1: extracted frames reach dispatcher ─────────────────────────────────

// TestPhase6_Video_FramesExtracted_ReachLLM verifies that a video upload flows
// through the Telegram handler → ExtractFrames → VideoToContentParts and arrives
// at the dispatcher as an InboundMessage whose Parts contain:
//   - a leading text ContentPart
//   - one or more image ContentParts with valid non-empty base64 JPEG data
//
// No raw temp paths must appear in any ContentPart field.
func TestPhase6_Video_FramesExtracted_ReachLLM(t *testing.T) {
	requireFFmpegVideo(t)

	videoBytes := makeIntegrationVideo(t, 2)

	srv := videoHTTPServer(t, videoBytes)
	defer srv.Close()

	disp := &telegramRecordingDispatcher{}
	bot := &videoTGBot{
		mockTGBot: &mockTGBot{},
		fileURL:   srv.URL + "/video.mp4",
	}

	h := telegram.NewForTest(disp, bot, 111)
	h.SetHTTPClient(srv.Client())

	h.HandleMessage(context.Background(), &tgbotapi.Message{
		MessageID: 1,
		From:      &tgbotapi.User{ID: 111},
		Chat:      &tgbotapi.Chat{ID: 999},
		Video: &tgbotapi.Video{
			FileID:   "video-inttest-1",
			FileSize: len(videoBytes),
			MimeType: "video/mp4",
		},
	})

	disp.mu.Lock()
	msgs := disp.msgs
	disp.mu.Unlock()

	if len(msgs) != 1 {
		t.Fatalf("dispatcher called %d times, want 1", len(msgs))
	}

	parts := msgs[0].Parts
	if len(parts) == 0 {
		t.Fatal("InboundMessage.Parts is empty — no ContentParts reached the dispatcher")
	}

	// Leading part must be the text descriptor.
	if parts[0].Type != "text" {
		t.Errorf("parts[0].Type = %q, want 'text'", parts[0].Type)
	}

	// Collect image parts that follow.
	var imageParts []types.ContentPart
	for _, p := range parts[1:] {
		if p.Type == "image" {
			imageParts = append(imageParts, p)
		}
	}
	if len(imageParts) == 0 {
		t.Fatal("no image ContentParts found — video frames did not reach the dispatcher")
	}

	// Each image part must carry valid, non-empty base64 JPEG data.
	for i, p := range imageParts {
		if p.ImageData == "" {
			t.Errorf("imageParts[%d].ImageData is empty", i)
			continue
		}
		decoded, err := base64.StdEncoding.DecodeString(p.ImageData)
		if err != nil {
			t.Errorf("imageParts[%d]: invalid base64: %v", i, err)
			continue
		}
		if len(decoded) == 0 {
			t.Errorf("imageParts[%d]: decoded JPEG is empty", i)
		}
	}

	// Verify ordering: each part after index 0 must be an image.
	for i, p := range parts[1:] {
		if p.Type != "image" {
			t.Errorf("parts[%d].Type = %q after text intro, want 'image'", i+1, p.Type)
		}
	}

	// No raw temp file paths must leak into any ContentPart string field.
	for i, p := range parts {
		if strings.Contains(p.Text, "/tmp") || strings.Contains(p.Filename, "/tmp") ||
			strings.Contains(p.Text, os.TempDir()) || strings.Contains(p.Filename, os.TempDir()) {
			t.Errorf("parts[%d]: temp path leaked — text=%q filename=%q", i, p.Text, p.Filename)
		}
	}
}

// ── Test 2: oversized video is rejected before ffmpeg ─────────────────────────

// TestPhase6_Video_OversizedVideo_NotifiesUser verifies that the channel handler
// checks file size before invoking ffmpeg. When the video exceeds VIDEO_MAX_SIZE_MB:
//   - the user receives a "too large to analyse" message
//   - the dispatcher is never called
//   - ExtractFrames is never invoked (no ffmpeg child process)
func TestPhase6_Video_OversizedVideo_NotifiesUser(t *testing.T) {
	bot := &mockTGBot{}
	disp := &telegramRecordingDispatcher{}

	h := telegram.NewForTest(disp, bot, 111)
	h.SetVideoConfig(0, 1) // maxSizeMB = 1 MB; maxFrames unchanged

	// FileSize = 2 MB exceeds the 1 MB limit — size gate fires before any download.
	h.HandleMessage(context.Background(), &tgbotapi.Message{
		MessageID: 1,
		From:      &tgbotapi.User{ID: 111},
		Chat:      &tgbotapi.Chat{ID: 999},
		Video: &tgbotapi.Video{
			FileID:   "oversized-video",
			FileSize: 2 * 1024 * 1024,
			MimeType: "video/mp4",
		},
	})

	// Dispatcher must not have been called.
	disp.mu.Lock()
	dispatched := len(disp.msgs)
	disp.mu.Unlock()

	if dispatched != 0 {
		t.Errorf("dispatcher called %d times for oversized video, want 0", dispatched)
	}

	// User must receive a "too large" reply.
	bot.mu.Lock()
	sent := bot.sent
	bot.mu.Unlock()

	if len(sent) == 0 {
		t.Fatal("no reply sent to user for oversized video")
	}
	if !containsVideoReply(sent, "too large to analyse") {
		t.Errorf(`user reply must contain "too large to analyse"; got %d message(s)`, len(sent))
	}
}

// ── Test 3: max-frame cap is enforced by ExtractFrames ───────────────────────

// TestPhase6_Video_FrameCount_RespectsMaxFrames verifies that ExtractFrames and
// VideoToContentParts together never produce more image ContentParts than the
// configured maximum, and that the text intro ContentPart is always present and
// always leads the slice.
func TestPhase6_Video_FrameCount_RespectsMaxFrames(t *testing.T) {
	requireFFmpegVideo(t)

	videoBytes := makeIntegrationVideo(t, 4) // 4-second video yields multiple natural frames
	const maxFrames = 3

	frames, err := attachments.ExtractFrames(videoBytes, "video/mp4", maxFrames)
	if err != nil {
		t.Fatalf("ExtractFrames: %v", err)
	}
	if len(frames) > maxFrames {
		t.Errorf("ExtractFrames returned %d frames, want at most %d", len(frames), maxFrames)
	}
	if len(frames) == 0 {
		t.Fatal("ExtractFrames returned 0 frames")
	}

	parts := attachments.VideoToContentParts(frames, "test.mp4")
	if len(parts) == 0 {
		t.Fatal("VideoToContentParts returned no parts")
	}

	// Text intro must come first.
	if parts[0].Type != "text" {
		t.Errorf("parts[0].Type = %q, want 'text' (intro must lead)", parts[0].Type)
	}

	// All remaining parts must be images; count must not exceed maxFrames.
	var imageCount int
	for i, p := range parts[1:] {
		if p.Type != "image" {
			t.Errorf("parts[%d].Type = %q, want 'image'", i+1, p.Type)
		}
		imageCount++
	}
	if imageCount == 0 {
		t.Fatal("no image ContentParts — frame extraction produced no output")
	}
	if imageCount > maxFrames {
		t.Errorf("image ContentParts = %d, want at most %d", imageCount, maxFrames)
	}
}

// ── Test 4: ffmpeg missing → graceful user notification ──────────────────────

// TestPhase6_Video_FfmpegUnavailable_NotifiesUser verifies that when ffmpeg is
// absent from PATH the channel handler:
//   - sends "Video analysis isn't available on this server." to the user
//   - does not call the dispatcher
//   - does not panic or leak goroutines
func TestPhase6_Video_FfmpegUnavailable_NotifiesUser(t *testing.T) {
	// Dummy payload — content is irrelevant; ExtractFrames will fail before processing.
	dummyVideo := make([]byte, 512)

	srv := videoHTTPServer(t, dummyVideo)
	defer srv.Close()

	bot := &videoTGBot{
		mockTGBot: &mockTGBot{},
		fileURL:   srv.URL + "/video.mp4",
	}
	disp := &telegramRecordingDispatcher{}

	h := telegram.NewForTest(disp, bot, 111)
	h.SetHTTPClient(srv.Client())

	// Erase PATH so exec.LookPath("ffmpeg") fails inside ExtractFrames.
	t.Setenv("PATH", "")

	h.HandleMessage(context.Background(), &tgbotapi.Message{
		MessageID: 1,
		From:      &tgbotapi.User{ID: 111},
		Chat:      &tgbotapi.Chat{ID: 999},
		Video: &tgbotapi.Video{
			FileID:   "video-no-ffmpeg",
			FileSize: len(dummyVideo),
			MimeType: "video/mp4",
		},
	})

	// Dispatcher must not have been called.
	disp.mu.Lock()
	dispatched := len(disp.msgs)
	disp.mu.Unlock()

	if dispatched != 0 {
		t.Errorf("dispatcher called %d times when ffmpeg unavailable, want 0", dispatched)
	}

	// User must receive the availability message.
	bot.mu.Lock()
	sent := bot.sent
	bot.mu.Unlock()

	if len(sent) == 0 {
		t.Fatal("no reply sent to user when ffmpeg unavailable")
	}
	if !containsVideoReply(sent, "Video analysis isn't available") {
		t.Errorf(`user reply must contain "Video analysis isn't available"; got %d message(s)`, len(sent))
	}
}
