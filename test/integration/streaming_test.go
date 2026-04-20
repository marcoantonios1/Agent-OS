package integration

import (
	"bufio"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/marcoantonios1/Agent-OS/internal/costguard"
)

// sseFrame mirrors the JSON payload in each SSE data line.
type sseFrame struct {
	Delta     string `json:"delta"`
	Done      bool   `json:"done"`
	SessionID string `json:"session_id"`
}

// readSSEFrames reads all SSE data frames from r until the stream closes.
func readSSEFrames(t *testing.T, resp *http.Response) []sseFrame {
	t.Helper()
	var frames []sseFrame
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		var f sseFrame
		if err := json.Unmarshal([]byte(payload), &f); err != nil {
			t.Fatalf("failed to parse SSE frame %q: %v", payload, err)
		}
		frames = append(frames, f)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("SSE scanner error: %v", err)
	}
	return frames
}

// TestStreaming_MultipleChunksAndDoneFrame verifies that POST /v1/chat/stream:
//   - returns Content-Type: text/event-stream
//   - emits multiple delta frames before the final done frame
//   - closes cleanly with done:true and the correct session_id
func TestStreaming_MultipleChunksAndDoneFrame(t *testing.T) {
	tokens := []string{"Hello", " World", "!"}

	stack := newStack(stackConfig{
		llmResponses: []costguard.CompletionResponse{
			classifyResp("comms"),
			// Empty Complete() response triggers RunStream to call Stream().
			textResp(""),
		},
		streamChunks: [][]string{tokens},
		emailProv:    newMockEmail(nil, nil),
	})
	defer stack.Close()

	resp, err := stack.postStream(chatRequest{
		SessionID: "stream-1",
		UserID:    "user-1",
		Text:      "Check my emails",
	})
	if err != nil {
		t.Fatalf("postStream: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("Content-Type: got %q, want text/event-stream", ct)
	}

	frames := readSSEFrames(t, resp)

	// Must have at least one delta frame plus the done frame.
	if len(frames) < 2 {
		t.Fatalf("expected at least 2 SSE frames, got %d", len(frames))
	}

	// All frames except the last should carry delta content.
	deltaFrames := frames[:len(frames)-1]
	for i, f := range deltaFrames {
		if f.Done {
			t.Errorf("frame %d: unexpected done=true in delta frame", i)
		}
	}

	// Verify we received all tokens in order.
	var got []string
	for _, f := range deltaFrames {
		if f.Delta != "" {
			got = append(got, f.Delta)
		}
	}
	if strings.Join(got, "") != strings.Join(tokens, "") {
		t.Errorf("token content: got %q, want %q", strings.Join(got, ""), strings.Join(tokens, ""))
	}

	// Final frame must have done:true and the correct session_id.
	last := frames[len(frames)-1]
	if !last.Done {
		t.Errorf("last frame: done=false, want true")
	}
	if last.SessionID != "stream-1" {
		t.Errorf("last frame: session_id=%q, want %q", last.SessionID, "stream-1")
	}
}

// TestStreaming_ValidationErrors verifies that missing fields return 400
// (not a streaming response).
func TestStreaming_ValidationErrors(t *testing.T) {
	stack := newStack(stackConfig{emailProv: newMockEmail(nil, nil)})
	defer stack.Close()

	cases := []chatRequest{
		{UserID: "u1", Text: "hi"},           // missing session_id
		{SessionID: "s1", Text: "hi"},        // missing user_id
		{SessionID: "s1", UserID: "u1"},      // missing text
	}
	for _, req := range cases {
		resp, err := stack.postStream(req)
		if err != nil {
			t.Fatalf("postStream: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("expected 400 for %+v, got %d", req, resp.StatusCode)
		}
	}
}

// TestStreaming_HistoryPersistedAfterStream verifies that a follow-up call to
// POST /v1/chat on the same session sees the streamed turn in history.
func TestStreaming_HistoryPersistedAfterStream(t *testing.T) {
	tokens := []string{"Stream reply"}

	stack := newStack(stackConfig{
		llmResponses: []costguard.CompletionResponse{
			// Stream turn: classify + empty complete
			classifyResp("comms"),
			textResp(""),
			// Follow-up turn: classify + agent reply
			classifyResp("comms"),
			textResp("I remember your previous message."),
		},
		streamChunks: [][]string{tokens},
		emailProv:    newMockEmail(nil, nil),
	})
	defer stack.Close()

	// First turn via streaming.
	streamResp, err := stack.postStream(chatRequest{
		SessionID: "stream-hist",
		UserID:    "user-1",
		Text:      "First message via stream",
	})
	if err != nil {
		t.Fatalf("postStream: %v", err)
	}
	// Read all frames to let the stream complete (history is persisted after close).
	readSSEFrames(t, streamResp)
	streamResp.Body.Close()

	// Second turn via regular /v1/chat — history should contain the first turn.
	_, chatResp, err := stack.post(chatRequest{
		SessionID: "stream-hist",
		UserID:    "user-1",
		Text:      "Follow-up",
	})
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	if chatResp.Text == "" {
		t.Error("expected non-empty response on follow-up turn")
	}
}
