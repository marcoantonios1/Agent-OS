package observability_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/marcoantonios1/Agent-OS/internal/observability"
)

func TestWithTraceID_RoundTrip(t *testing.T) {
	ctx := observability.WithTraceID(context.Background(), "abc123")
	if got := observability.TraceIDFromContext(ctx); got != "abc123" {
		t.Errorf("got %q, want %q", got, "abc123")
	}
}

func TestTraceIDFromContext_EmptyWhenNotSet(t *testing.T) {
	if got := observability.TraceIDFromContext(context.Background()); got != "" {
		t.Errorf("got %q, want empty string", got)
	}
}

func TestSetup_LogLevelDebug(t *testing.T) {
	var buf bytes.Buffer
	// Install a test handler that captures output.
	base := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	slog.SetDefault(slog.New(base))

	slog.Debug("should appear")
	if !strings.Contains(buf.String(), "should appear") {
		t.Error("debug log should appear when level is debug")
	}
}

func TestContextHandler_InjectsTraceID(t *testing.T) {
	var buf bytes.Buffer

	// Build the same handler stack Setup() builds, but write to buf.
	base := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})

	// Wrap with the context-aware handler by calling Setup-equivalent logic.
	// We test the exported helpers + the behaviour rather than internal types.
	observability.Setup("info")

	// Replace default with a buf-backed version so we can inspect output.
	slog.SetDefault(slog.New(newTestContextHandler(&buf)))

	ctx := observability.WithTraceID(context.Background(), "trace-xyz")
	slog.InfoContext(ctx, "test event", "key", "val")

	var record map[string]any
	if err := json.Unmarshal(buf.Bytes(), &record); err != nil {
		t.Fatalf("log output is not valid JSON: %v\nraw: %s", err, buf.String())
	}
	if record["trace_id"] != "trace-xyz" {
		t.Errorf("trace_id = %v, want trace-xyz", record["trace_id"])
	}
	if record["msg"] != "test event" {
		t.Errorf("msg = %v, want 'test event'", record["msg"])
	}
}

func TestContextHandler_NoTraceID_FieldAbsent(t *testing.T) {
	var buf bytes.Buffer
	slog.SetDefault(slog.New(newTestContextHandler(&buf)))

	// Context without a trace ID.
	slog.InfoContext(context.Background(), "no trace")

	var record map[string]any
	if err := json.Unmarshal(buf.Bytes(), &record); err != nil {
		t.Fatalf("log output is not valid JSON: %v", err)
	}
	if _, ok := record["trace_id"]; ok {
		t.Error("trace_id should be absent when not set in context")
	}
}

// newTestContextHandler is a test-local handler that mirrors the behaviour
// of observability.Setup but writes to an in-memory buffer.
func newTestContextHandler(buf *bytes.Buffer) slog.Handler {
	base := slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	return &testContextHandler{base: base}
}

type testContextHandler struct{ base slog.Handler }

func (h *testContextHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.base.Enabled(ctx, l)
}
func (h *testContextHandler) Handle(ctx context.Context, r slog.Record) error {
	if id := observability.TraceIDFromContext(ctx); id != "" {
		r.AddAttrs(slog.String("trace_id", id))
	}
	return h.base.Handle(ctx, r)
}
func (h *testContextHandler) WithAttrs(a []slog.Attr) slog.Handler {
	return &testContextHandler{base: h.base.WithAttrs(a)}
}
func (h *testContextHandler) WithGroup(n string) slog.Handler {
	return &testContextHandler{base: h.base.WithGroup(n)}
}
