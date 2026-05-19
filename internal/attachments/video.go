package attachments

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/marcoantonios1/Agent-OS/internal/types"
)

// ErrFfmpegUnavailable is returned by ExtractFrames when ffmpeg is not found in PATH.
var ErrFfmpegUnavailable = errors.New("ffmpeg not found in PATH")

// supportedVideoExt maps supported video MIME types to file extensions for the
// temporary input file. The extension helps ffmpeg select the correct demuxer.
var supportedVideoExt = map[string]string{
	"video/mp4":        ".mp4",
	"video/mpeg":       ".mpeg",
	"video/quicktime":  ".mov",
	"video/webm":       ".webm",
	"video/3gpp":       ".3gp",
	"video/x-matroska": ".mkv",
}

// ExtractFrames extracts up to maxFrames evenly spaced JPEG screenshots from
// video bytes and returns them as base64-encoded strings in display order.
//
// Returns ErrFfmpegUnavailable when ffmpeg is not in PATH. Short videos may
// produce fewer than maxFrames frames — the caller should tolerate that.
//
// Security: exec.CommandContext is used exclusively; no user data is ever
// interpolated into shell strings.
func ExtractFrames(video []byte, mimeType string, maxFrames int) ([]string, error) {
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		return nil, ErrFfmpegUnavailable
	}

	ext, ok := supportedVideoExt[mimeType]
	if !ok {
		ext = ".mp4"
	}

	// Write video bytes to a temp file so ffmpeg can read it.
	tmpIn, err := os.CreateTemp("", "agentos-video-*"+ext)
	if err != nil {
		return nil, fmt.Errorf("create temp file: %w", err)
	}
	defer os.Remove(tmpIn.Name())

	if _, err := tmpIn.Write(video); err != nil {
		tmpIn.Close()
		return nil, fmt.Errorf("write temp file: %w", err)
	}
	if err := tmpIn.Close(); err != nil {
		return nil, fmt.Errorf("close temp file: %w", err)
	}

	// Create a temp directory to hold extracted JPEG frames.
	tmpDir, err := os.MkdirTemp("", "agentos-frames-*")
	if err != nil {
		return nil, fmt.Errorf("create frames dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Probe duration; fall back to a single frame at t=0 on any error.
	duration, _ := probeVideoDuration(ctx, tmpIn.Name())

	timestamps := evenTimestamps(duration, maxFrames)

	var frames []string
	for i, ts := range timestamps {
		outPath := filepath.Join(tmpDir, fmt.Sprintf("frame%04d.jpg", i))
		if extractErr := extractSingleFrame(ctx, ffmpegPath, tmpIn.Name(), ts, outPath); extractErr != nil {
			continue
		}
		data, readErr := os.ReadFile(outPath)
		if readErr != nil || len(data) == 0 {
			continue
		}
		frames = append(frames, base64.StdEncoding.EncodeToString(data))
		if len(frames) >= maxFrames {
			break
		}
	}

	return frames, nil
}

// VideoToContentParts converts a slice of base64-encoded JPEG frames into a
// multimodal ContentPart sequence ready for the LLM.
//
// The returned slice always starts with a descriptive text part followed by
// one image part per frame, preserving input order.
func VideoToContentParts(frames []string, filename string) []types.ContentPart {
	parts := make([]types.ContentPart, 0, 1+len(frames))
	parts = append(parts, types.ContentPart{
		Type: "text",
		Text: fmt.Sprintf("[Video: %s — %d frames extracted for analysis]", filename, len(frames)),
	})
	for i, frame := range frames {
		parts = append(parts, types.ContentPart{
			Type:      "image",
			MimeType:  "image/jpeg",
			Filename:  fmt.Sprintf("frame_%03d.jpg", i+1),
			ImageData: frame,
		})
	}
	return parts
}

// ── internal helpers ──────────────────────────────────────────────────────────

// probeVideoDuration uses ffprobe to get the video duration in seconds.
// Returns (0, err) when the duration cannot be determined.
func probeVideoDuration(ctx context.Context, path string) (float64, error) {
	ffprobePath, err := exec.LookPath("ffprobe")
	if err != nil {
		return 0, fmt.Errorf("ffprobe not in PATH: %w", err)
	}
	cmd := exec.CommandContext(ctx, ffprobePath,
		"-v", "error",
		"-show_entries", "format=duration",
		"-of", "csv=p=0",
		path,
	)
	out, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("ffprobe: %w", err)
	}
	s := strings.TrimSpace(string(out))
	if s == "" || s == "N/A" {
		return 0, fmt.Errorf("duration unavailable")
	}
	return strconv.ParseFloat(s, 64)
}

// evenTimestamps returns up to maxFrames evenly spaced timestamps within the
// video. When duration is 0 or unknown a single timestamp at t=0 is returned
// so the caller still gets at least one frame attempt.
func evenTimestamps(duration float64, maxFrames int) []float64 {
	if maxFrames <= 0 {
		return nil
	}
	if duration <= 0 {
		return []float64{0}
	}
	ts := make([]float64, maxFrames)
	step := duration / float64(maxFrames+1)
	for i := range ts {
		ts[i] = step * float64(i+1)
	}
	return ts
}

// extractSingleFrame runs ffmpeg to extract one JPEG frame at the given
// timestamp from inputPath, writing the result to outPath.
func extractSingleFrame(ctx context.Context, ffmpegPath, inputPath string, timestamp float64, outPath string) error {
	cmd := exec.CommandContext(ctx, ffmpegPath,
		"-ss", strconv.FormatFloat(timestamp, 'f', 3, 64),
		"-i", inputPath,
		"-frames:v", "1",
		"-q:v", "2",
		"-y",
		outPath,
	)
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run()
}
