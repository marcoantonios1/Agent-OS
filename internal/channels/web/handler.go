// Package web exposes the Router Agent over HTTP for the web chat channel.
package web

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
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
	log        *slog.Logger
	handler    http.Handler
}

// NewHandler registers all routes and wraps them with middleware.
func NewHandler(d Dispatcher) *Handler {
	h := &Handler{
		dispatcher: d,
		log:        slog.Default(),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat", h.chat)
	mux.HandleFunc("GET /healthz", h.healthz)

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

	msg := types.InboundMessage{
		ID:        reqIDFromCtx(r.Context()),
		ChannelID: types.ChannelID("web"),
		UserID:    req.UserID,
		SessionID: req.SessionID,
		Text:      req.Text,
		Timestamp: time.Now(),
	}

	out, err := h.dispatcher.Route(r.Context(), msg)
	if err != nil {
		h.log.ErrorContext(r.Context(), "router error",
			"session_id", req.SessionID, "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeJSON(w, http.StatusOK, chatResponse{
		SessionID: out.SessionID,
		Text:      out.Text,
	})
}

func (h *Handler) healthz(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ok")) //nolint:errcheck
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
