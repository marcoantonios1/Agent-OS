package web

import (
	"context"
	"log/slog"
	"strings"

	"github.com/marcoantonios1/Agent-OS/internal/sessions"
)

// ReminderNotifier is the web-channel implementation of reminder.Notifier.
// Web sessions have no persistent connection, so delivery is not possible;
// the notifier logs a warning and returns nil so the worker continues.
type ReminderNotifier struct{}

// NotifyReminder implements reminder.Notifier for web sessions.
// Web sessions (session IDs that do not start with "discord:" or "whatsapp:")
// cannot receive push notifications — the HTTP connection is already closed by
// the time a reminder fires. A warning is logged for observability.
func (ReminderNotifier) NotifyReminder(ctx context.Context, r *sessions.Reminder) error {
	if strings.HasPrefix(r.SessionID, "discord:") || strings.HasPrefix(r.SessionID, "whatsapp:") {
		return nil // handled by the channel-specific notifier
	}
	slog.WarnContext(ctx, "web: reminder fired but web sessions have no persistent connection — delivery skipped",
		"reminder_id", r.ID,
		"user_id", r.UserID,
		"session_id", r.SessionID,
	)
	return nil
}

// NotifyProgress implements types.ProgressNotifier for web sessions.
// Web sessions have no persistent connection for push; progress is logged only.
func (ReminderNotifier) NotifyProgress(ctx context.Context, sessionID, userID, text string) error {
	if strings.HasPrefix(sessionID, "discord:") || strings.HasPrefix(sessionID, "whatsapp:") {
		return nil // handled by the channel-specific notifier
	}
	slog.InfoContext(ctx, "web: builder progress (no push available)",
		"session_id", sessionID, "user_id", userID, "text", text)
	return nil
}
