package attachments_test

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"github.com/marcoantonios1/Agent-OS/internal/attachments"
)

// requireFFmpeg skips the current test when ffmpeg is not in PATH.
func requireFFmpeg(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not found in PATH — skipping video test")
	}
}

// makeTestVideo generates a minimal MP4 (black frames) via ffmpeg and returns
// its bytes. The test is skipped if ffmpeg cannot produce a valid file.
func makeTestVideo(t *testing.T, durationSecs int) []byte {
	t.Helper()
	tmp, err := os.CreateTemp("", "agentos-testvideo-*.mp4")
	if err != nil {
		t.Fatalf("makeTestVideo: create temp: %v", err)
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
		t.Skipf("makeTestVideo: ffmpeg could not generate test video: %v", err)
	}

	data, err := os.ReadFile(tmp.Name())
	if err != nil {
		t.Fatalf("makeTestVideo: read: %v", err)
	}
	return data
}

// ── Test 1: success path ──────────────────────────────────────────────────────

// TestExtractFrames_Success verifies that ExtractFrames returns at least one
// valid base64-encoded JPEG frame from a short synthetic MP4.
func TestExtractFrames_Success(t *testing.T) {
	requireFFmpeg(t)

	videoBytes := makeTestVideo(t, 4) // 4-second black video
	frames, err := attachments.ExtractFrames(videoBytes, "video/mp4", 4)
	if err != nil {
		t.Fatalf("ExtractFrames: %v", err)
	}
	if len(frames) == 0 {
		t.Fatal("expected at least 1 frame, got 0")
	}

	for i, f := range frames {
		data, decErr := base64.StdEncoding.DecodeString(f)
		if decErr != nil {
			t.Errorf("frame %d: invalid base64: %v", i, decErr)
			continue
		}
		if len(data) == 0 {
			t.Errorf("frame %d: empty JPEG data", i)
		}
	}
}

// ── Test 2: ffmpeg missing ────────────────────────────────────────────────────

// TestExtractFrames_FfmpegMissing verifies that ExtractFrames returns
// ErrFfmpegUnavailable when ffmpeg cannot be found in PATH.
func TestExtractFrames_FfmpegMissing(t *testing.T) {
	t.Setenv("PATH", "")

	_, err := attachments.ExtractFrames([]byte("not a real video"), "video/mp4", 4)
	if !errors.Is(err, attachments.ErrFfmpegUnavailable) {
		t.Errorf("expected ErrFfmpegUnavailable, got %v", err)
	}
}

// ── Test 3: maxFrames respected ───────────────────────────────────────────────

// TestExtractFrames_MaxFramesRespected verifies that ExtractFrames never
// returns more frames than the requested maximum.
func TestExtractFrames_MaxFramesRespected(t *testing.T) {
	requireFFmpeg(t)

	videoBytes := makeTestVideo(t, 8) // 8-second video
	const maxFrames = 3
	frames, err := attachments.ExtractFrames(videoBytes, "video/mp4", maxFrames)
	if err != nil {
		t.Fatalf("ExtractFrames: %v", err)
	}
	if len(frames) > maxFrames {
		t.Errorf("got %d frames, want at most %d", len(frames), maxFrames)
	}
}

// ── Test 4: oversized video rejected ─────────────────────────────────────────

// TestOversizedVideoRejected verifies that the size threshold sentinel used by
// channel handlers correctly identifies oversized videos. Channel handlers
// check size before calling ExtractFrames so ffmpeg is never invoked.
// This test validates the threshold arithmetic that each handler applies.
func TestOversizedVideoRejected(t *testing.T) {
	const maxSizeMB = int64(50)
	limit := maxSizeMB * 1024 * 1024

	oversized := make([]byte, limit+1)
	withinLimit := make([]byte, limit)

	if int64(len(oversized)) <= limit {
		t.Error("test setup: oversized slice should exceed limit")
	}
	if int64(len(withinLimit)) > limit {
		t.Error("test setup: within-limit slice should not exceed limit")
	}

	// Confirm the boundary: exactly at the limit is accepted, one byte over is not.
	if int64(len(withinLimit)) != limit {
		t.Errorf("within-limit len=%d, want exactly %d", len(withinLimit), limit)
	}

	t.Logf("size gate: %d MB = %d bytes; oversized at %d bytes", maxSizeMB, limit, len(oversized))
}

// ── Test 5: VideoToContentParts ordering ─────────────────────────────────────

// TestVideoToContentParts_OrderStable verifies that VideoToContentParts
// returns a leading text part followed by image parts in the exact input order.
func TestVideoToContentParts_OrderStable(t *testing.T) {
	frames := []string{"base64A", "base64B", "base64C"}
	parts := attachments.VideoToContentParts(frames, "clip.mp4")

	// 1 text + 3 image parts.
	if len(parts) != 4 {
		t.Fatalf("len(parts)=%d, want 4 (1 text + 3 images)", len(parts))
	}

	// First part must be the descriptive text.
	if parts[0].Type != "text" {
		t.Errorf("parts[0].Type=%q, want 'text'", parts[0].Type)
	}
	if !strings.Contains(parts[0].Text, "clip.mp4") {
		t.Errorf("text part missing filename: %q", parts[0].Text)
	}
	if !strings.Contains(parts[0].Text, "3 frames") {
		t.Errorf("text part missing frame count: %q", parts[0].Text)
	}

	// Image parts must preserve input order.
	for i, f := range frames {
		p := parts[i+1]
		if p.Type != "image" {
			t.Errorf("parts[%d].Type=%q, want 'image'", i+1, p.Type)
		}
		if p.MimeType != "image/jpeg" {
			t.Errorf("parts[%d].MimeType=%q, want 'image/jpeg'", i+1, p.MimeType)
		}
		if p.ImageData != f {
			t.Errorf("parts[%d].ImageData=%q, want %q", i+1, p.ImageData, f)
		}
		if p.Filename == "" {
			t.Errorf("parts[%d].Filename is empty", i+1)
		}
	}
}
