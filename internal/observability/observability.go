// Package observability provides structured logging setup and trace ID
// propagation for Agent OS.
//
// # Trace ID
//
// Every inbound request is assigned a trace ID at the HTTP boundary. Inject it
// into the context with WithTraceID; retrieve it with TraceIDFromContext.
//
// # Automatic trace_id injection
//
// After calling Setup, every slog.InfoContext / slog.WarnContext / etc. call
// that receives a context carrying a trace ID will automatically include
// trace_id in the log record — no manual "trace_id", id argument needed at
// each callsite.
//
// # Log level
//
// Call Setup(os.Getenv("LOG_LEVEL")) at program start. Valid values:
// "debug", "info" (default), "warn", "error". Case-insensitive.
package observability

import (
	"context"
	"log/slog"
	"os"
	"strings"
)

type ctxKey string

const traceIDKey ctxKey = "trace_id"

// WithTraceID attaches a trace ID to ctx. The ID will be included in every
// slog call that uses this context after Setup has been called.
func WithTraceID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, traceIDKey, id)
}

// TraceIDFromContext retrieves the trace ID from ctx, or "" if not set.
func TraceIDFromContext(ctx context.Context) string {
	if id, ok := ctx.Value(traceIDKey).(string); ok {
		return id
	}
	return ""
}

// Setup configures the global slog default with JSON output, the given log
// level, and a handler that automatically appends trace_id from context.
// Call once at program start before any logging occurs.
func Setup(levelStr string) {
	var level slog.Level
	switch strings.ToLower(strings.TrimSpace(levelStr)) {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}

	base := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})
	slog.SetDefault(slog.New(&contextHandler{base: base}))
}

// contextHandler wraps any slog.Handler to inject trace_id from context into
// every log record transparently.
type contextHandler struct {
	base slog.Handler
}

func (h *contextHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.base.Enabled(ctx, level)
}

func (h *contextHandler) Handle(ctx context.Context, r slog.Record) error {
	if id := TraceIDFromContext(ctx); id != "" {
		r.AddAttrs(slog.String("trace_id", id))
	}
	return h.base.Handle(ctx, r)
}

func (h *contextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &contextHandler{base: h.base.WithAttrs(attrs)}
}

func (h *contextHandler) WithGroup(name string) slog.Handler {
	return &contextHandler{base: h.base.WithGroup(name)}
}
