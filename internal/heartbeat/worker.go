// Package heartbeat provides a background worker that dispatches a prompt to
// the router on a configurable interval and delivers the response to the user
// via a registered channel notifier.
package heartbeat

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/marcoantonios1/Agent-OS/internal/sessions"
	"github.com/marcoantonios1/Agent-OS/internal/tools/reminder"
	"github.com/marcoantonios1/Agent-OS/internal/types"
)

// Config holds all runtime configuration for the heartbeat worker.
type Config struct {
	// Interval between ticks. Must be > 0 to start the worker.
	Interval time.Duration
	// UserID is sent as the InboundMessage.UserID on every tick.
	UserID string
	// SessionID is a dedicated session that keeps heartbeat history separate
	// from the user's main conversation sessions.
	SessionID string
	// Channel determines which notifier receives the response.
	Channel types.ChannelID
	// Prompt is the fallback text sent to the router when no HEARTBEAT.md exists.
	Prompt string
	// WorkspaceDir is the directory scanned for HEARTBEAT.md. Optional.
	WorkspaceDir string
}

// Worker dispatches a prompt to the router on a configurable interval and
// delivers the response via the notifier registered for its configured channel.
type Worker struct {
	cfg        Config
	dispatcher reminder.Dispatcher
	mu         sync.RWMutex
	notifiers  map[types.ChannelID]reminder.Notifier
	log        *slog.Logger
}

// New returns a Worker. The worker does not start until Run is called.
func New(cfg Config, dispatcher reminder.Dispatcher) *Worker {
	return &Worker{
		cfg:        cfg,
		dispatcher: dispatcher,
		notifiers:  make(map[types.ChannelID]reminder.Notifier),
		log:        slog.Default(),
	}
}

// AddNotifier registers a Notifier for the given channel. Safe to call after
// Run has started.
func (w *Worker) AddNotifier(channelID types.ChannelID, n reminder.Notifier) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.notifiers[channelID] = n
}

// Run starts the ticker loop and blocks until ctx is cancelled.
func (w *Worker) Run(ctx context.Context) {
	ticker := time.NewTicker(w.cfg.Interval)
	defer ticker.Stop()
	w.log.Info("heartbeat worker started",
		"interval", w.cfg.Interval,
		"channel", w.cfg.Channel,
		"user_id", w.cfg.UserID,
	)
	for {
		select {
		case <-ctx.Done():
			w.log.Info("heartbeat worker stopped")
			return
		case <-ticker.C:
			w.tick(ctx)
		}
	}
}

func (w *Worker) tick(ctx context.Context) {
	prompt := w.loadPrompt()
	w.log.InfoContext(ctx, "heartbeat tick", "session_id", w.cfg.SessionID)

	out, err := w.dispatcher.Route(ctx, types.InboundMessage{
		ChannelID: w.cfg.Channel,
		UserID:    w.cfg.UserID,
		SessionID: w.cfg.SessionID,
		Text:      prompt,
		Timestamp: time.Now(),
	})
	if err != nil {
		w.log.WarnContext(ctx, "heartbeat: route failed", "error", err)
		return
	}

	w.mu.RLock()
	n, ok := w.notifiers[w.cfg.Channel]
	w.mu.RUnlock()
	if !ok {
		w.log.WarnContext(ctx, "heartbeat: no notifier registered", "channel", w.cfg.Channel)
		return
	}

	r := &sessions.Reminder{
		UserID:    w.cfg.UserID,
		SessionID: w.cfg.SessionID,
		ChannelID: w.cfg.Channel,
		Message:   out.Text,
	}
	if err := n.NotifyReminder(ctx, r); err != nil {
		w.log.WarnContext(ctx, "heartbeat: notifier failed", "channel", w.cfg.Channel, "error", err)
	}
}

const defaultPrompt = "Check my emails for anything urgent and summarize my calendar for today."

// loadPrompt resolves the prompt for this tick using the following priority:
//  1. {WorkspaceDir}/HEARTBEAT.md — re-read on every tick, no restart needed.
//  2. cfg.Prompt (HEARTBEAT_PROMPT env var) — if set and non-empty.
//  3. defaultPrompt — hardcoded fallback so the heartbeat is always useful.
//
// File-not-found is silently ignored. Any other read error (e.g. permissions)
// is logged as a warning before falling through to the next level.
func (w *Worker) loadPrompt() string {
	if w.cfg.WorkspaceDir != "" {
		path := filepath.Join(w.cfg.WorkspaceDir, "HEARTBEAT.md")
		data, err := os.ReadFile(path)
		switch {
		case err == nil:
			if s := strings.TrimSpace(string(data)); s != "" {
				return s
			}
			// empty file — fall through
		case os.IsNotExist(err):
			// expected when the file hasn't been created yet — silent
		default:
			w.log.Warn("heartbeat: could not read HEARTBEAT.md, using fallback",
				"path", path, "error", err)
		}
	}
	if w.cfg.Prompt != "" {
		return w.cfg.Prompt
	}
	return defaultPrompt
}
