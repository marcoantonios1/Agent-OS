// Package web exposes the Router Agent over HTTP for the web chat channel.
package web

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/marcoantonios1/Agent-OS/internal/observability"
	"github.com/marcoantonios1/Agent-OS/internal/types"
)

type contextKey string

const reqIDKey contextKey = "request_id"

// Dispatcher is the interface the Handler uses to process inbound messages.
// *router.Router satisfies this interface.
type Dispatcher interface {
	Route(ctx context.Context, msg types.InboundMessage) (types.OutboundMessage, error)
}

// StreamDispatcher is the optional streaming extension of Dispatcher.
// When the dispatcher also implements this interface, POST /v1/chat/stream
// is enabled and returns a Server-Sent Events stream of tokens.
type StreamDispatcher interface {
	RouteStream(ctx context.Context, msg types.InboundMessage) (<-chan string, error)
}

// sseFrame is the JSON payload for each SSE data line.
type sseFrame struct {
	Delta     string `json:"delta"`
	Done      bool   `json:"done,omitempty"`
	SessionID string `json:"session_id,omitempty"`
}

// ReadinessChecker reports whether all external dependencies are reachable.
// *costguard.Client satisfies this interface. A nil ReadinessChecker makes
// /readyz always return 200.
type ReadinessChecker interface {
	Ping(ctx context.Context) error
}

// chatRequest is the JSON body for POST /v1/chat.
type chatRequest struct {
	SessionID string `json:"session_id"`
	UserID    string `json:"user_id"`
	Text      string `json:"text"`
}

// chatResponse is the JSON body returned by POST /v1/chat.
type chatResponse struct {
	SessionID string `json:"session_id"`
	Text      string `json:"text"`
}

// errorResponse is the JSON body returned on errors.
type errorResponse struct {
	Error string `json:"error"`
}

// Handler is the HTTP handler for the web chat channel.
type Handler struct {
	dispatcher Dispatcher
	readiness  ReadinessChecker // may be nil
	log        *slog.Logger
	handler    http.Handler
}

// NewHandler registers all routes and wraps them with middleware.
// checker is used by GET /readyz; pass nil to skip the external dependency check.
func NewHandler(d Dispatcher, checker ReadinessChecker) *Handler {
	h := &Handler{
		dispatcher: d,
		readiness:  checker,
		log:        slog.Default(),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat", h.chat)
	mux.HandleFunc("POST /v1/chat/stream", h.chatStream)
	mux.HandleFunc("GET /healthz", h.healthz)
	mux.HandleFunc("GET /readyz", h.readyz)

	// Middleware order (outermost first):
	//   recovery → requestID → logging → mux
	h.handler = recovery(requestIDMiddleware(h.loggingMiddleware(mux)))
	return h
}

// ServeHTTP implements http.Handler.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.handler.ServeHTTP(w, r)
}

// --- route handlers ---

func (h *Handler) chat(w http.ResponseWriter, r *http.Request) {
	var req chatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.SessionID == "" || req.UserID == "" || req.Text == "" {
		writeError(w, http.StatusBadRequest, "session_id, user_id, and text are required")
		return
	}

	start := time.Now()
	msg := types.InboundMessage{
		ID:        reqIDFromCtx(r.Context()),
		ChannelID: types.ChannelID("web"),
		UserID:    req.UserID,
		SessionID: req.SessionID,
		Text:      req.Text,
		Timestamp: start,
	}

	h.log.InfoContext(r.Context(), "channel_received",
		"session_id", req.SessionID,
		"user_id", req.UserID,
		"text_length", len(req.Text),
		"channel", "web",
	)

	out, err := h.dispatcher.Route(r.Context(), msg)
	if err != nil {
		h.log.ErrorContext(r.Context(), "router error",
			"session_id", req.SessionID, "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	h.log.InfoContext(r.Context(), "channel_response",
		"session_id", req.SessionID,
		"latency_ms", time.Since(start).Milliseconds(),
		"channel", "web",
	)

	writeJSON(w, http.StatusOK, chatResponse{
		SessionID: out.SessionID,
		Text:      out.Text,
	})
}

func (h *Handler) chatStream(w http.ResponseWriter, r *http.Request) {
	var req chatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.SessionID == "" || req.UserID == "" || req.Text == "" {
		writeError(w, http.StatusBadRequest, "session_id, user_id, and text are required")
		return
	}

	sd, ok := h.dispatcher.(StreamDispatcher)
	if !ok {
		writeError(w, http.StatusNotImplemented, "streaming not supported")
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported by server")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	start := time.Now()
	msg := types.InboundMessage{
		ID:        reqIDFromCtx(r.Context()),
		ChannelID: types.ChannelID("web"),
		UserID:    req.UserID,
		SessionID: req.SessionID,
		Text:      req.Text,
		Timestamp: start,
	}

	h.log.InfoContext(r.Context(), "channel_received",
		"session_id", req.SessionID, "user_id", req.UserID,
		"text_length", len(req.Text), "channel", "web-stream")

	chunks, err := sd.RouteStream(r.Context(), msg)
	if err != nil {
		h.log.ErrorContext(r.Context(), "router stream error",
			"session_id", req.SessionID, "error", err)
		writeSSEFrame(w, flusher, sseFrame{Delta: "internal error", Done: true, SessionID: req.SessionID})
		return
	}

	for chunk := range chunks {
		writeSSEFrame(w, flusher, sseFrame{Delta: chunk})
	}
	writeSSEFrame(w, flusher, sseFrame{Done: true, SessionID: req.SessionID})

	h.log.InfoContext(r.Context(), "channel_response",
		"session_id", req.SessionID,
		"latency_ms", time.Since(start).Milliseconds(),
		"channel", "web-stream")
}

func writeSSEFrame(w http.ResponseWriter, f http.Flusher, v sseFrame) {
	b, _ := json.Marshal(v)
	fmt.Fprintf(w, "data: %s\n\n", b) //nolint:errcheck
	f.Flush()
}

func (h *Handler) healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) readyz(w http.ResponseWriter, r *http.Request) {
	if h.readiness == nil {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := h.readiness.Ping(ctx); err != nil {
		h.log.WarnContext(r.Context(), "readiness check failed", "error", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"status": "unavailable",
			"reason": err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// --- middleware ---

// requestIDMiddleware injects a request ID into the context as both the local
// reqIDKey and as the observability trace_id, and sets it as a response header.
// It honours an incoming X-Request-ID header if present.
func requestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if id == "" {
			id = newRequestID()
		}
		w.Header().Set("X-Request-ID", id)
		ctx := context.WithValue(r.Context(), reqIDKey, id)
		ctx = observability.WithTraceID(ctx, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// loggingMiddleware logs method, path, status code, and duration for every request.
func (h *Handler) loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rw, r)
		h.log.InfoContext(r.Context(), "http request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rw.status,
			"duration_ms", time.Since(start).Milliseconds(),
			"request_id", reqIDFromCtx(r.Context()),
		)
	})
}

// recovery catches panics, logs them, and returns a 500.
func recovery(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if p := recover(); p != nil {
				slog.ErrorContext(r.Context(), "panic recovered",
					"panic", p, "request_id", reqIDFromCtx(r.Context()))
				writeError(w, http.StatusInternalServerError, "internal error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// --- helpers ---

// statusWriter wraps ResponseWriter to capture the status code for logging.
type statusWriter struct {
	http.ResponseWriter
	status  int
	written bool
}

func (sw *statusWriter) WriteHeader(status int) {
	if !sw.written {
		sw.status = status
		sw.written = true
		sw.ResponseWriter.WriteHeader(status)
	}
}

func (sw *statusWriter) Write(b []byte) (int, error) {
	if !sw.written {
		sw.WriteHeader(http.StatusOK)
	}
	return sw.ResponseWriter.Write(b)
}

func (sw *statusWriter) Flush() {
	if f, ok := sw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v) //nolint:errcheck
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, errorResponse{Error: msg})
}

func reqIDFromCtx(ctx context.Context) string {
	if id, ok := ctx.Value(reqIDKey).(string); ok {
		return id
	}
	return ""
}

func newRequestID() string {
	b := make([]byte, 8)
	rand.Read(b) //nolint:errcheck
	return hex.EncodeToString(b)
}
