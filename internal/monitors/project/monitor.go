package projectmonitor

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/marcoantonios1/Agent-OS/internal/memory/episodic"
	"github.com/marcoantonios1/Agent-OS/internal/sessions"
	"github.com/marcoantonios1/Agent-OS/internal/types"
)

const (
	PhaseRequirements = "requirements"
	PhaseSpec         = "spec"
	PhaseTasks        = "tasks"
	PhaseCodegen      = "codegen"
	PhaseReview       = "review"
	PhaseDone         = "done"
	PhaseBlocked      = "blocked"
)

// activePhases is the set of phases where a project needs monitoring.
// Projects in PhaseDone are excluded — they're finished.
var activePhases = map[string]bool{
	PhaseRequirements: true,
	PhaseSpec:         true,
	PhaseTasks:        true,
	PhaseCodegen:      true,
	PhaseReview:       true,
	PhaseBlocked:      true,
}

// deadlineApproachWindow is how far out a milestone deadline must be to
// trigger a deadline alert.
const deadlineApproachWindow = 48 * time.Hour

// Notifier delivers a notification to the user's channel.
// Defined locally to avoid an import cycle with the reminder package.
// Structurally identical to reminder.Notifier.
type Notifier interface {
	NotifyReminder(ctx context.Context, r *sessions.Reminder) error
}

// Config holds all runtime configuration for the project monitor.
type Config struct {
	Interval   time.Duration // how often to check (default 6h)
	StaleAfter time.Duration // idle time before flagging stale (default 3 days)
	Channel    types.ChannelID
	UserID     string
}

// ConfigFromEnv reads Config from environment variables.
// Returns Config with Interval = 0 when PROJECT_MONITOR_ENABLED != "true".
func ConfigFromEnv() Config {
	if os.Getenv("PROJECT_MONITOR_ENABLED") != "true" {
		return Config{}
	}
	cfg := Config{
		Interval:   6 * time.Hour,
		StaleAfter: 3 * 24 * time.Hour,
		Channel:    types.ChannelID(os.Getenv("PROJECT_MONITOR_CHANNEL")),
		UserID:     os.Getenv("PROJECT_MONITOR_USER_ID"),
	}
	if v := os.Getenv("PROJECT_MONITOR_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			cfg.Interval = d
		}
	}
	if v := os.Getenv("PROJECT_MONITOR_STALE_AFTER"); v != "" {
		// Support "3d" shorthand as 3*24*time.Hour.
		v = strings.ReplaceAll(v, "d", "*24h")
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			cfg.StaleAfter = d
		}
	}
	return cfg
}

// Monitor polls the project store on a configurable interval and sends
// proactive notifications about stale, blocked, or deadline-approaching projects.
type Monitor struct {
	cfg      Config
	projects sessions.ProjectStore
	episodic episodic.Store
	notifier Notifier
	log      *slog.Logger
}

// New returns a Monitor. Call Run in a goroutine to start polling.
func New(
	cfg Config,
	projectStore sessions.ProjectStore,
	episodicStore episodic.Store,
	notifier Notifier,
) *Monitor {
	return &Monitor{
		cfg:      cfg,
		projects: projectStore,
		episodic: episodicStore,
		notifier: notifier,
		log:      slog.Default(),
	}
}

// Run starts the ticker loop and blocks until ctx is cancelled.
// No-op when cfg.Interval <= 0 or cfg.UserID is empty.
func (m *Monitor) Run(ctx context.Context) {
	if m.cfg.Interval <= 0 || m.cfg.UserID == "" {
		m.log.Info("project monitor: disabled")
		return
	}
	ticker := time.NewTicker(m.cfg.Interval)
	defer ticker.Stop()
	m.log.Info("project monitor: started",
		"interval", m.cfg.Interval,
		"stale_after", m.cfg.StaleAfter,
		"channel", m.cfg.Channel,
		"user_id", m.cfg.UserID,
	)
	m.check(ctx)
	for {
		select {
		case <-ctx.Done():
			m.log.Info("project monitor: stopped")
			return
		case <-ticker.C:
			m.check(ctx)
		}
	}
}

// RunOnce runs exactly one check cycle. Exported for testing.
func (m *Monitor) RunOnce(ctx context.Context) {
	m.check(ctx)
}

func (m *Monitor) check(ctx context.Context) {
	summaries, err := m.projects.ListProjects(m.cfg.UserID)
	if err != nil {
		m.log.WarnContext(ctx, "project monitor: list failed", "error", err)
		return
	}
	if len(summaries) == 0 {
		return
	}

	for _, summary := range summaries {
		if !activePhases[summary.Phase] {
			continue
		}
		project, err := m.projects.GetProject(summary.ID)
		if err != nil {
			m.log.WarnContext(ctx, "project monitor: get failed",
				"project_id", summary.ID, "error", err)
			continue
		}
		m.evaluateProject(ctx, project)
	}
}

// evaluateProject checks a single project for all monitor conditions and fires
// notifications as needed. Each condition is checked and deduplicated independently.
func (m *Monitor) evaluateProject(ctx context.Context, p *sessions.Project) {
	m.checkStale(ctx, p)
	m.checkBlocked(ctx, p)
	m.checkDeadlines(ctx, p)
}

// checkStale fires a stale notification if the project has been idle for longer
// than cfg.StaleAfter and hasn't already been notified for this staleness window.
func (m *Monitor) checkStale(ctx context.Context, p *sessions.Project) {
	idleFor := time.Since(p.UpdatedAt)
	if idleFor < m.cfg.StaleAfter {
		return
	}

	// Dedup key encodes project ID + the staleness window so re-notifications
	// fire after the project is updated and goes stale again.
	windowDays := int(m.cfg.StaleAfter.Hours() / 24)
	dedupKey := fmt.Sprintf("project-stale:%s:window%d", p.ID, windowDays)

	if m.alreadyNotified(ctx, dedupKey, p.ID) {
		return
	}

	days := int(idleFor.Hours() / 24)
	msg := formatStaleNotification(p, days)
	m.sendNotification(ctx, p.ID, msg)
	m.saveMemory(ctx, fmt.Sprintf("Sent stale notification for project %s (%s), idle %d days",
		p.ID, p.Name, days), 0.6)
}

// checkBlocked fires a notification when the project is in BLOCKED phase and
// hasn't been notified about this block yet.
func (m *Monitor) checkBlocked(ctx context.Context, p *sessions.Project) {
	if p.Phase != PhaseBlocked {
		return
	}
	dedupKey := fmt.Sprintf("project-blocked:%s", p.ID)
	if m.alreadyNotified(ctx, dedupKey, p.ID) {
		return
	}

	msg := formatBlockedNotification(p)
	m.sendNotification(ctx, p.ID, msg)
	m.saveMemory(ctx, fmt.Sprintf("Sent blocked notification for project %s (%s)",
		p.ID, p.Name), 0.6)
}

// checkDeadlines scans the project's Milestones slice for entries that look like
// deadlines and fires an alert when one is within deadlineApproachWindow.
//
// Milestone format expected: "Milestone name — 2026-07-01" (date suffix after " — ").
// Milestones without a parseable date suffix are silently skipped.
func (m *Monitor) checkDeadlines(ctx context.Context, p *sessions.Project) {
	now := time.Now()
	for _, milestone := range p.Milestones {
		date, name, ok := parseMilestoneDate(milestone)
		if !ok {
			continue
		}
		timeUntil := date.Sub(now)
		if timeUntil < 0 || timeUntil > deadlineApproachWindow {
			continue
		}

		dedupKey := fmt.Sprintf("project-deadline:%s:%s", p.ID, name)
		if m.alreadyNotified(ctx, dedupKey, p.ID) {
			continue
		}

		hours := int(timeUntil.Hours())
		msg := formatDeadlineNotification(p, name, hours)
		m.sendNotification(ctx, p.ID, msg)
		m.saveMemory(ctx, fmt.Sprintf("Sent deadline notification for project %s (%s) milestone %q in %dh",
			p.ID, p.Name, name, hours), 0.7)
	}
}

// parseMilestoneDate parses a milestone string of the form "Name — 2026-07-01".
// Returns the date, the name part, and true on success.
func parseMilestoneDate(milestone string) (time.Time, string, bool) {
	const sep = " — "
	idx := strings.LastIndex(milestone, sep)
	if idx < 0 {
		return time.Time{}, "", false
	}
	name := strings.TrimSpace(milestone[:idx])
	dateStr := strings.TrimSpace(milestone[idx+len(sep):])
	t, err := time.Parse("2006-01-02", dateStr)
	if err != nil {
		return time.Time{}, "", false
	}
	return t, name, true
}

// alreadyNotified returns true if episodic memory already contains a recent
// record for this dedup key. On search error, returns false (prefer notifying).
func (m *Monitor) alreadyNotified(ctx context.Context, dedupKey, projectID string) bool {
	memories, err := m.episodic.SearchByText(ctx, m.cfg.UserID, dedupKey, 1)
	if err != nil {
		m.log.WarnContext(ctx, "project monitor: episodic search failed", "error", err)
		return false
	}
	for _, mem := range memories {
		// Distance < 0.15 AND content contains projectID = near-exact match.
		if mem.Distance < 0.15 && strings.Contains(mem.Content, projectID) {
			return true
		}
	}
	return false
}

func (m *Monitor) sendNotification(ctx context.Context, projectID, msg string) {
	reminder := &sessions.Reminder{
		ID:        uuid.NewString(),
		UserID:    m.cfg.UserID,
		SessionID: string(m.cfg.Channel) + ":" + m.cfg.UserID,
		ChannelID: m.cfg.Channel,
		Message:   msg,
	}
	if err := m.notifier.NotifyReminder(ctx, reminder); err != nil {
		m.log.WarnContext(ctx, "project monitor: notification failed",
			"project_id", projectID, "error", err)
	}
}

func (m *Monitor) saveMemory(ctx context.Context, content string, importance float64) {
	mem := episodic.Memory{
		ID:         uuid.NewString(),
		UserID:     m.cfg.UserID,
		SessionID:  "project-monitor",
		Channel:    "project",
		Content:    content,
		Source:     "project_monitor",
		Importance: importance,
		CreatedAt:  time.Now().UTC(),
	}
	if err := m.episodic.SaveText(ctx, mem); err != nil {
		m.log.WarnContext(ctx, "project monitor: failed to save memory", "error", err)
	}
}

func formatStaleNotification(p *sessions.Project, idleDays int) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("⚠️ Project %q hasn't had activity in %d day", p.Name, idleDays))
	if idleDays != 1 {
		sb.WriteString("s")
	}
	sb.WriteString(fmt.Sprintf("\n\nCurrent phase: %s", strings.ToUpper(p.Phase)))
	taskSummary := summariseTasks(p.Tasks)
	if taskSummary != "" {
		sb.WriteString(fmt.Sprintf(" (%s)", taskSummary))
	}
	sb.WriteString(fmt.Sprintf("\n\nReady to continue? Just say \"continue %s\"", p.Name))
	return sb.String()
}

func formatBlockedNotification(p *sessions.Project) string {
	return fmt.Sprintf(
		"🚧 Project %q is blocked and waiting for input.\n\nSay \"unblock %s\" to provide what's needed.",
		p.Name, p.Name,
	)
}

func formatDeadlineNotification(p *sessions.Project, milestoneName string, hoursUntil int) string {
	return fmt.Sprintf(
		"⏰ Project %q has a deadline approaching:\n\nMilestone: %s\nDue in: ~%d hours\n\nCurrent phase: %s",
		p.Name, milestoneName, hoursUntil, strings.ToUpper(p.Phase),
	)
}

// summariseTasks parses the tasks JSON field and returns a brief completion
// summary like "3 of 12 tasks complete". Returns empty string on parse failure.
func summariseTasks(tasksJSON string) string {
	if tasksJSON == "" || tasksJSON == "[]" || tasksJSON == "null" {
		return ""
	}
	var tasks []map[string]any
	if err := json.Unmarshal([]byte(tasksJSON), &tasks); err != nil {
		return ""
	}
	if len(tasks) == 0 {
		return ""
	}
	done := 0
	for _, t := range tasks {
		if v, ok := t["done"]; ok {
			if b, ok := v.(bool); ok && b {
				done++
			}
		} else if v, ok := t["status"]; ok {
			if s, ok := v.(string); ok && s == "done" {
				done++
			}
		}
	}
	return fmt.Sprintf("%d of %d tasks complete", done, len(tasks))
}
