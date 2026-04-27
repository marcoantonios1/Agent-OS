package discord

import (
	"context"
	"fmt"
	"strings"

	"github.com/marcoantonios1/Agent-OS/internal/sessions"
)

// NotifyReminder implements reminder.Notifier. It delivers the reminder message
// to the Discord channel encoded in r.SessionID.
//
// Session ID format (set by sessionKey):
//
//	discord:{guildID}:{channelID}:{userID}
//	discord:dm:{channelID}:{userID}
//
// Only sessions whose ID starts with "discord:" are handled; all others are
// silently ignored so the worker can broadcast to all registered notifiers.
func (h *Handler) NotifyReminder(ctx context.Context, r *sessions.Reminder) error {
	if !strings.HasPrefix(r.SessionID, "discord:") {
		return nil
	}
	if h.session == nil {
		return fmt.Errorf("discord: not connected — cannot deliver reminder %s", r.ID)
	}

	// discord:{guildOrDM}:{channelID}:{userID}
	parts := strings.SplitN(r.SessionID, ":", 4)
	if len(parts) != 4 {
		return fmt.Errorf("discord: malformed session ID %q", r.SessionID)
	}
	channelID := parts[2]

	if _, err := h.session.ChannelMessageSend(channelID, r.Message); err != nil {
		return fmt.Errorf("discord: send reminder to channel %s: %w", channelID, err)
	}
	h.log.InfoContext(ctx, "discord: reminder delivered",
		"reminder_id", r.ID, "channel_id", channelID)
	return nil
}

// NotifyProgress implements types.ProgressNotifier. It delivers a builder
// progress update to the Discord channel encoded in sessionID.
// Non-discord session IDs are silently ignored.
func (h *Handler) NotifyProgress(ctx context.Context, sessionID, userID, text string) error {
	if !strings.HasPrefix(sessionID, "discord:") {
		return nil
	}
	if h.session == nil {
		return fmt.Errorf("discord: not connected — cannot deliver progress for session %s", sessionID)
	}

	// discord:{guildOrDM}:{channelID}:{userID}
	parts := strings.SplitN(sessionID, ":", 4)
	if len(parts) != 4 {
		return fmt.Errorf("discord: malformed session ID %q", sessionID)
	}
	channelID := parts[2]

	if _, err := h.session.ChannelMessageSend(channelID, text); err != nil {
		return fmt.Errorf("discord: send progress to channel %s: %w", channelID, err)
	}
	h.log.InfoContext(ctx, "discord: builder progress delivered",
		"session_id", sessionID, "channel_id", channelID, "text", text)
	return nil
}
