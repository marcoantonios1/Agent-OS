package calendarmonitor

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/marcoantonios1/Agent-OS/internal/costguard"
	"github.com/marcoantonios1/Agent-OS/internal/memory/episodic"
	"github.com/marcoantonios1/Agent-OS/internal/sessions"
	"github.com/marcoantonios1/Agent-OS/internal/tools/calendar"
	"github.com/marcoantonios1/Agent-OS/internal/types"
)

// Notifier delivers a brief to the user's channel.
// Defined locally to avoid an import cycle with the reminder package.
// Structurally identical to reminder.Notifier — channel handlers satisfy both.
type Notifier interface {
	NotifyReminder(ctx context.Context, r *sessions.Reminder) error
}

// Config holds all runtime configuration for the calendar monitor.
type Config struct {
	Interval  time.Duration  // how often to check (default 5m; bounds reminder lateness)
	Lookahead time.Duration  // how far ahead to look (default 48h; must cover the night-before slot)
	Location  *time.Location // timezone for the night-before/morning/noon slots (default time.Local)
	Channel   types.ChannelID
	UserID    string
	Model     string
}

// ConfigFromEnv reads Config from environment variables.
// Returns a Config with Interval = 0 when CALENDAR_MONITOR_ENABLED != "true".
func ConfigFromEnv() Config {
	if os.Getenv("CALENDAR_MONITOR_ENABLED") != "true" {
		return Config{}
	}
	cfg := Config{
		Interval:  5 * time.Minute,
		Lookahead: 48 * time.Hour,
		Location:  time.Local,
		Channel:   types.ChannelID(os.Getenv("CALENDAR_MONITOR_CHANNEL")),
		UserID:    os.Getenv("CALENDAR_MONITOR_USER_ID"),
		Model:     os.Getenv("CALENDAR_MONITOR_MODEL"),
	}
	if v := os.Getenv("CALENDAR_MONITOR_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			cfg.Interval = d
		}
	}
	if v := os.Getenv("CALENDAR_MONITOR_LOOKAHEAD"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			cfg.Lookahead = d
		}
	}
	if v := os.Getenv("CALENDAR_MONITOR_TIMEZONE"); v != "" {
		if loc, err := time.LoadLocation(v); err == nil {
			cfg.Location = loc
		} else {
			slog.Warn("calendar monitor: invalid CALENDAR_MONITOR_TIMEZONE, using local time", "value", v, "error", err)
		}
	}
	if cfg.Model == "" {
		cfg.Model = "llama3.2:3b"
	}
	return cfg
}

// Monitor polls the calendar on a configurable interval and sends enriched
// event briefs at fixed points before each event (see ReminderSlots).
type Monitor struct {
	cfg      Config
	calendar calendar.CalendarProvider
	llm      costguard.LLMClient
	episodic episodic.Store
	sent     SentStore
	notifier Notifier
	log      *slog.Logger
}

// New returns a Monitor. Call Run in a goroutine to start polling.
// A nil sent store falls back to an in-memory one (not persisted across restarts).
func New(
	cfg Config,
	calProv calendar.CalendarProvider,
	llmClient costguard.LLMClient,
	episodicStore episodic.Store,
	sent SentStore,
	notifier Notifier,
) *Monitor {
	if cfg.Location == nil {
		cfg.Location = time.Local
	}
	if sent == nil {
		sent = NewMemorySentStore()
	}
	return &Monitor{
		cfg:      cfg,
		calendar: calProv,
		llm:      llmClient,
		episodic: episodicStore,
		sent:     sent,
		notifier: notifier,
		log:      slog.Default(),
	}
}

// Run starts the ticker loop and blocks until ctx is cancelled.
// No-op when cfg.Interval <= 0 or cfg.UserID is empty.
func (m *Monitor) Run(ctx context.Context) {
	if m.cfg.Interval <= 0 || m.cfg.UserID == "" {
		m.log.Info("calendar monitor: disabled (no interval or user configured)")
		return
	}
	ticker := time.NewTicker(m.cfg.Interval)
	defer ticker.Stop()
	m.log.Info("calendar monitor: started",
		"interval", m.cfg.Interval,
		"lookahead", m.cfg.Lookahead,
		"timezone", m.cfg.Location.String(),
		"channel", m.cfg.Channel,
		"user_id", m.cfg.UserID,
	)
	m.check(ctx, time.Now()) // run immediately on startup
	for {
		select {
		case <-ctx.Done():
			m.log.Info("calendar monitor: stopped")
			return
		case <-ticker.C:
			m.check(ctx, time.Now())
		}
	}
}

// RunOnce runs exactly one check cycle. Exported for testing.
func (m *Monitor) RunOnce(ctx context.Context) {
	m.check(ctx, time.Now())
}

// RunOnceAt runs one check cycle as if the current time were now. Exported for testing.
func (m *Monitor) RunOnceAt(ctx context.Context, now time.Time) {
	m.check(ctx, now)
}

// check performs one full calendar check cycle.
func (m *Monitor) check(ctx context.Context, now time.Time) {
	events, err := m.calendar.List(ctx, now, now.Add(m.cfg.Lookahead))
	if err != nil {
		m.log.WarnContext(ctx, "calendar monitor: list failed", "error", err)
		return
	}

	for _, ev := range events {
		if ev.AllDay || !ev.Start.After(now) {
			continue // all-day and already-started events don't need briefs
		}
		m.maybeBriefEvent(ctx, ev, now)
	}
}

// maybeBriefEvent sends a brief for ev if one of its reminder slots is due and
// hasn't been sent. When several slots are due at once (e.g. after downtime)
// only one brief goes out and all due slots are marked sent.
func (m *Monitor) maybeBriefEvent(ctx context.Context, ev calendar.Event, now time.Time) {
	var due []string
	for _, slot := range ReminderSlots(ev.Start, m.cfg.Location) {
		if slot.At.After(now) {
			break
		}
		due = append(due, reminderKey(ev, slot))
	}
	if len(due) == 0 {
		return
	}
	latest := due[len(due)-1]
	sent, err := m.sent.IsSent(ctx, m.cfg.UserID, latest)
	if err != nil {
		m.log.WarnContext(ctx, "calendar monitor: sent lookup failed", "event_id", ev.ID, "error", err)
		// Include on error — prefer over-notifying to missing an event.
	}
	if sent {
		return
	}

	// Search episodic memory for context about this event's attendees and title.
	contextBlock := m.buildContextBlock(ctx, ev)

	brief := formatBrief(ev, now, m.cfg.Location, contextBlock)
	reminder := &sessions.Reminder{
		ID:        uuid.NewString(),
		UserID:    m.cfg.UserID,
		SessionID: string(m.cfg.Channel) + ":" + m.cfg.UserID,
		ChannelID: m.cfg.Channel,
		Message:   brief,
	}
	if err := m.notifier.NotifyReminder(ctx, reminder); err != nil {
		m.log.WarnContext(ctx, "calendar monitor: notification failed", "event_id", ev.ID, "error", err)
		// Still mark as sent to avoid re-briefing every tick.
	}
	m.log.InfoContext(ctx, "calendar monitor: brief sent", "event", ev.Title, "start", ev.Start, "slot", latest)

	for _, key := range due {
		if err := m.sent.MarkSent(ctx, m.cfg.UserID, key); err != nil {
			m.log.WarnContext(ctx, "calendar monitor: failed to mark reminder sent", "error", err)
		}
	}

	// Record the brief in episodic memory so agents can recall it.
	m.saveMemory(ctx, fmt.Sprintf("Sent calendar brief for event %s (%s) at %s",
		ev.ID, ev.Title, ev.Start.Format(time.RFC3339)), 0.6)
}

// reminderKey identifies one slot of one occurrence of an event. Including the
// start time means a rescheduled event gets a fresh set of reminders.
func reminderKey(ev calendar.Event, slot Slot) string {
	return fmt.Sprintf("%s|%d|%s", ev.ID, ev.Start.Unix(), slot.Name)
}

// buildContextBlock searches episodic memory for context relevant to the event
// and formats it as a markdown section. Returns empty string when nothing found.
func (m *Monitor) buildContextBlock(ctx context.Context, ev calendar.Event) string {
	queryParts := []string{ev.Title}
	queryParts = append(queryParts, ev.Attendees...)
	query := strings.Join(queryParts, " ")

	memories, err := m.episodic.SearchByText(ctx, m.cfg.UserID, query, 5)
	if err != nil {
		m.log.WarnContext(ctx, "calendar monitor: context search failed", "error", err)
		return ""
	}

	// Only include memories below the relevance threshold and not brief-records.
	var relevant []episodic.Memory
	for _, mem := range memories {
		if mem.Distance < 0.6 && !strings.HasPrefix(mem.Content, "Sent calendar brief") {
			relevant = append(relevant, mem)
		}
	}
	if len(relevant) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("\n📝 Context from past conversations:")
	for _, mem := range relevant {
		sb.WriteString("\n- ")
		sb.WriteString(mem.Content)
	}
	return sb.String()
}

// saveMemory persists an episodic memory for this event. Best-effort — errors logged only.
func (m *Monitor) saveMemory(ctx context.Context, content string, importance float64) {
	mem := episodic.Memory{
		ID:         uuid.NewString(),
		UserID:     m.cfg.UserID,
		SessionID:  "calendar-monitor",
		Channel:    "calendar",
		Content:    content,
		Source:     "calendar_monitor",
		Importance: importance,
		CreatedAt:  time.Now().UTC(),
	}
	if err := m.episodic.SaveText(ctx, mem); err != nil {
		m.log.WarnContext(ctx, "calendar monitor: failed to save memory", "error", err)
	}
}

// formatBrief builds the user-facing notification message for an event.
func formatBrief(ev calendar.Event, now time.Time, loc *time.Location, contextBlock string) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("📅 %s\n🕒 %s\n", ev.Title, capitalize(formatWhen(ev.Start, now, loc))))

	if len(ev.Attendees) > 0 {
		sb.WriteString(fmt.Sprintf("\nAttendees: %s", strings.Join(ev.Attendees, ", ")))
	}

	dur := ev.End.Sub(ev.Start)
	sb.WriteString(fmt.Sprintf("\nDuration: %s", formatDuration(dur)))

	if ev.Location != "" {
		sb.WriteString(fmt.Sprintf("\nLocation: %s", ev.Location))
	}

	if contextBlock != "" {
		sb.WriteString("\n")
		sb.WriteString(contextBlock)
	}

	return sb.String()
}

// formatDuration converts a duration to a human-readable string.
func formatDuration(d time.Duration) string {
	minutes := int(d.Minutes())
	if minutes < 60 {
		return fmt.Sprintf("%d minute%s", minutes, pluralS(minutes))
	}
	hours := d.Hours()
	if hours == float64(int(hours)) {
		h := int(hours)
		return fmt.Sprintf("%d hour%s", h, pluralS(h))
	}
	return fmt.Sprintf("%.1f hours", hours)
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func pluralS(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
