package reminder

import (
	"context"
	"log/slog"
	"time"

	"github.com/marcoantonios1/Agent-OS/internal/sessions"
)

// Notifier delivers a fired reminder to the user's channel.
// Implementations are provided by each channel handler (Discord, web, etc.).
type Notifier interface {
	NotifyReminder(ctx context.Context, r *sessions.Reminder) error
}

// Worker polls the ReminderStore on a fixed interval and fires due reminders
// via the registered Notifiers. It runs until ctx is cancelled.
type Worker struct {
	Store     sessions.ReminderStore
	Notifiers []Notifier
	Interval  time.Duration
}

// NewWorker returns a Worker with the default 30-second poll interval.
func NewWorker(store sessions.ReminderStore) *Worker {
	return &Worker{
		Store:    store,
		Interval: 30 * time.Second,
	}
}

// AddNotifier registers a Notifier to receive fired reminders.
func (w *Worker) AddNotifier(n Notifier) {
	w.Notifiers = append(w.Notifiers, n)
}

// Run starts the polling loop. Blocks until ctx is cancelled.
func (w *Worker) Run(ctx context.Context) {
	ticker := time.NewTicker(w.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case t := <-ticker.C:
			w.fire(ctx, t)
		}
	}
}

func (w *Worker) fire(ctx context.Context, now time.Time) {
	due, err := w.Store.Due(now)
	if err != nil {
		slog.ErrorContext(ctx, "reminder_worker: failed to query due reminders", "error", err)
		return
	}
	for _, r := range due {
		slog.InfoContext(ctx, "reminder_worker: firing reminder",
			"id", r.ID,
			"user_id", r.UserID,
			"channel_id", string(r.ChannelID),
		)
		for _, n := range w.Notifiers {
			if err := n.NotifyReminder(ctx, r); err != nil {
				slog.WarnContext(ctx, "reminder_worker: notifier error",
					"id", r.ID, "error", err)
			}
		}
	}
}
