package inbox

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/marcoantonios1/Agent-OS/internal/costguard"
	"github.com/marcoantonios1/Agent-OS/internal/memory/episodic"
	"github.com/marcoantonios1/Agent-OS/internal/sessions"
	"github.com/marcoantonios1/Agent-OS/internal/tools/email"
	"github.com/marcoantonios1/Agent-OS/internal/types"
)

// Notifier delivers a formatted notification to the user's channel.
// Implemented by each channel handler (WhatsApp, Discord, web, etc.)
// via the reminder.Notifier interface — use the same concrete types.
type Notifier interface {
	NotifyReminder(ctx context.Context, r *sessions.Reminder) error
}

// Config holds all runtime configuration for the inbox monitor.
type Config struct {
	// Interval between checks. Zero or negative disables the monitor.
	Interval time.Duration
	// Channel is the delivery channel (e.g. "whatsapp", "discord").
	Channel types.ChannelID
	// UserID is the target user — used for episodic memory lookup and notification delivery.
	UserID string
	// MaxEmails is the maximum number of recent emails to fetch per check.
	MaxEmails int
	// Model is the LLM model to use for triage (should be cheap — gemma4:27b or similar).
	Model string
}

// ConfigFromEnv reads Config from environment variables.
// Returns a config with Interval = 0 when INBOX_MONITOR_ENABLED is not "true".
func ConfigFromEnv() Config {
	if os.Getenv("INBOX_MONITOR_ENABLED") != "true" {
		return Config{}
	}
	cfg := Config{
		Interval:  15 * time.Minute,
		Channel:   types.ChannelID(os.Getenv("INBOX_MONITOR_CHANNEL")),
		UserID:    os.Getenv("INBOX_MONITOR_USER_ID"),
		MaxEmails: 20,
		Model:     os.Getenv("INBOX_MONITOR_MODEL"),
	}
	if v := os.Getenv("INBOX_MONITOR_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			cfg.Interval = d
		}
	}
	if v := os.Getenv("INBOX_MONITOR_MAX_EMAILS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.MaxEmails = n
		}
	}
	if cfg.Model == "" {
		cfg.Model = "gemma4:27b"
	}
	return cfg
}

// Monitor polls the inbox on a configurable interval and proactively notifies
// the user about important emails. It uses episodic memory to ensure each email
// is surfaced at most once.
type Monitor struct {
	cfg      Config
	email    email.EmailProvider
	llm      costguard.LLMClient
	episodic episodic.Store
	notifier Notifier
	log      *slog.Logger
}

// New returns a Monitor. Start Run() in a goroutine to begin polling.
func New(
	cfg Config,
	emailProv email.EmailProvider,
	llmClient costguard.LLMClient,
	episodicStore episodic.Store,
	notifier Notifier,
) *Monitor {
	return &Monitor{
		cfg:      cfg,
		email:    emailProv,
		llm:      llmClient,
		episodic: episodicStore,
		notifier: notifier,
		log:      slog.Default(),
	}
}

// Run starts the ticker loop and blocks until ctx is cancelled.
// It is a no-op when cfg.Interval <= 0 or cfg.UserID is empty.
func (m *Monitor) Run(ctx context.Context) {
	if m.cfg.Interval <= 0 || m.cfg.UserID == "" {
		m.log.Info("inbox monitor: disabled (no interval or user configured)")
		return
	}
	ticker := time.NewTicker(m.cfg.Interval)
	defer ticker.Stop()
	m.log.Info("inbox monitor: started",
		"interval", m.cfg.Interval,
		"channel", m.cfg.Channel,
		"user_id", m.cfg.UserID,
	)
	// Run once immediately so the user gets a check on startup.
	m.check(ctx)
	for {
		select {
		case <-ctx.Done():
			m.log.Info("inbox monitor: stopped")
			return
		case <-ticker.C:
			m.check(ctx)
		}
	}
}

// RunOnce performs exactly one check cycle. Exported for testing.
func (m *Monitor) RunOnce(ctx context.Context) {
	m.check(ctx)
}

// check performs one full inbox check cycle. All errors are logged and swallowed
// so the monitor always retries on the next tick.
func (m *Monitor) check(ctx context.Context) {
	m.log.InfoContext(ctx, "inbox monitor: checking inbox", "user_id", m.cfg.UserID)

	// 1. Fetch recent emails.
	emails, err := m.email.List(ctx, m.cfg.MaxEmails)
	if err != nil {
		m.log.WarnContext(ctx, "inbox monitor: email fetch failed", "error", err)
		return
	}
	if len(emails) == 0 {
		return
	}

	// 2. Filter out emails already notified via episodic memory.
	unseen := m.filterAlreadyNotified(ctx, emails)
	if len(unseen) == 0 {
		m.log.InfoContext(ctx, "inbox monitor: all emails already notified")
		return
	}

	// 3. Ask LLM which emails need attention.
	important, err := m.triageEmails(ctx, unseen)
	if err != nil {
		m.log.WarnContext(ctx, "inbox monitor: LLM triage failed", "error", err)
		return
	}
	if len(important) == 0 {
		m.log.InfoContext(ctx, "inbox monitor: no important emails found")
		// Still mark unseen emails as checked so we don't re-evaluate them every tick.
		m.markChecked(ctx, unseen)
		return
	}

	// 4. Format and deliver notification.
	notification := formatNotification(important)
	rem := &sessions.Reminder{
		ID:        uuid.NewString(),
		UserID:    m.cfg.UserID,
		SessionID: string(m.cfg.Channel) + ":" + m.cfg.UserID,
		ChannelID: m.cfg.Channel,
		Message:   notification,
	}
	if err := m.notifier.NotifyReminder(ctx, rem); err != nil {
		m.log.WarnContext(ctx, "inbox monitor: notification failed", "error", err)
		// Don't return — still persist memory so we don't re-notify on next tick.
	}
	m.log.InfoContext(ctx, "inbox monitor: notification sent", "count", len(important))

	// 5. Persist episodic memories for all surfaced emails.
	m.persistNotified(ctx, important)

	// 6. Mark remaining unseen (non-important) emails as checked too.
	importantIDs := make(map[string]bool, len(important))
	for _, e := range important {
		importantIDs[e.ID] = true
	}
	var nonImportant []email.EmailSummary
	for _, e := range unseen {
		if !importantIDs[e.ID] {
			nonImportant = append(nonImportant, e)
		}
	}
	m.markChecked(ctx, nonImportant)
}

// filterAlreadyNotified returns emails that have not yet been surfaced to the
// user. It searches episodic memory for each email's deduplication key.
// On search error, the email is included (prefer over-notifying to missing it).
func (m *Monitor) filterAlreadyNotified(ctx context.Context, emails []email.EmailSummary) []email.EmailSummary {
	var unseen []email.EmailSummary
	for _, e := range emails {
		key := deduplicationKey(e)
		// Search memory for this specific key — k=1, look for exact-ish match.
		memories, err := m.episodic.SearchByText(ctx, m.cfg.UserID, key, 1)
		if err != nil {
			m.log.WarnContext(ctx, "inbox monitor: episodic search failed", "error", err)
			unseen = append(unseen, e) // include on error
			continue
		}
		alreadyNotified := false
		for _, mem := range memories {
			// Distance < 0.2 indicates a near-exact match for this email's key.
			if mem.Distance < 0.2 && strings.Contains(mem.Content, e.ID) {
				alreadyNotified = true
				break
			}
		}
		if !alreadyNotified {
			unseen = append(unseen, e)
		}
	}
	return unseen
}

// triageEmails calls the LLM with the triage prompt and returns the subset of
// emails identified as important. IDs not found in emails are silently ignored.
func (m *Monitor) triageEmails(ctx context.Context, emails []email.EmailSummary) ([]email.EmailSummary, error) {
	emailList := formatEmailListForPrompt(emails)
	prompt := strings.ReplaceAll(triagePrompt, "{email_list}", emailList)

	resp, err := m.llm.Complete(ctx, costguard.CompletionRequest{
		Model: m.cfg.Model,
		Messages: []types.ConversationTurn{
			{Role: "user", Content: prompt},
		},
		MaxTokens: 256,
	})
	if err != nil {
		return nil, fmt.Errorf("triage: llm: %w", err)
	}

	raw := strings.TrimSpace(resp.Content)
	// Strip markdown fences if present.
	if strings.HasPrefix(raw, "```") {
		if idx := strings.Index(raw, "\n"); idx >= 0 {
			raw = raw[idx+1:]
		}
		if idx := strings.LastIndex(raw, "```"); idx >= 0 {
			raw = raw[:idx]
		}
		raw = strings.TrimSpace(raw)
	}

	var importantIDs []string
	if err := json.Unmarshal([]byte(raw), &importantIDs); err != nil {
		return nil, fmt.Errorf("triage: parse json %q: %w", raw, err)
	}
	if len(importantIDs) == 0 {
		return nil, nil
	}

	// Build a lookup map for O(1) membership test.
	idSet := make(map[string]bool, len(importantIDs))
	for _, id := range importantIDs {
		idSet[id] = true
	}
	var important []email.EmailSummary
	for _, e := range emails {
		if idSet[e.ID] {
			important = append(important, e)
		}
	}
	return important, nil
}

// markChecked persists a low-importance memory for each email so the monitor
// doesn't re-evaluate it on the next tick.
func (m *Monitor) markChecked(ctx context.Context, emails []email.EmailSummary) {
	for _, e := range emails {
		m.saveMemory(ctx, fmt.Sprintf("inbox-checked:%s", e.ID),
			fmt.Sprintf("Checked email %s from %s: %s", e.ID, e.From, e.Subject), 0.1)
	}
}

// persistNotified saves a high-importance episodic memory for each surfaced email.
func (m *Monitor) persistNotified(ctx context.Context, emails []email.EmailSummary) {
	for _, e := range emails {
		content := fmt.Sprintf("Notified user about email %s from %s: %s", e.ID, e.From, e.Subject)
		m.saveMemory(ctx, fmt.Sprintf("inbox-notified:%s", e.ID), content, 0.7)
	}
}

// saveMemory is a best-effort episodic memory save. Errors are logged, never
// surfaced — a failed memory save must not stop the monitor.
func (m *Monitor) saveMemory(ctx context.Context, _, content string, importance float64) {
	mem := episodic.Memory{
		ID:         uuid.NewString(),
		UserID:     m.cfg.UserID,
		SessionID:  "inbox-monitor",
		Channel:    "inbox",
		Content:    content,
		Source:     "inbox_monitor",
		Importance: importance,
		CreatedAt:  time.Now().UTC(),
	}
	if err := m.episodic.SaveText(ctx, mem); err != nil {
		m.log.WarnContext(ctx, "inbox monitor: failed to save memory", "error", err)
	}
}

// deduplicationKey returns a string suitable for episodic memory search that
// uniquely identifies an email without relying on provider-specific IDs alone.
func deduplicationKey(e email.EmailSummary) string {
	return fmt.Sprintf("email %s from %s: %s", e.ID, e.From, e.Subject)
}

// formatEmailListForPrompt serialises a list of EmailSummary for the triage prompt.
func formatEmailListForPrompt(emails []email.EmailSummary) string {
	var sb strings.Builder
	for _, e := range emails {
		sb.WriteString(fmt.Sprintf("ID: %s | From: %s | Subject: %s | Date: %s | Snippet: %s\n",
			e.ID, e.From, e.Subject, e.Date.Format(time.RFC3339), e.Snippet))
	}
	return sb.String()
}

// formatNotification builds the user-facing notification message.
func formatNotification(emails []email.EmailSummary) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("📬 %d email", len(emails)))
	if len(emails) != 1 {
		sb.WriteString("s")
	}
	sb.WriteString(" need")
	if len(emails) == 1 {
		sb.WriteString("s")
	}
	sb.WriteString(" your attention:\n")
	for _, e := range emails {
		sb.WriteString(fmt.Sprintf("\n- %s — %q\n", e.From, e.Subject))
		if e.Snippet != "" {
			sb.WriteString(fmt.Sprintf("  → %s\n", truncate(e.Snippet, 80)))
		}
	}
	return sb.String()
}

// truncate trims s to at most n runes, appending "…" if trimmed.
func truncate(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "…"
}

const triagePrompt = `You are an email triage assistant. Given these emails, identify which ones genuinely need the user's attention right now.

Flag an email if it:
- Requires a decision or response within 48 hours
- Is from a person the user frequently communicates with
- Contains a deadline, meeting request, or action item
- Seems urgent based on subject or sender

Do NOT flag:
- Marketing emails or newsletters
- Automated notifications (GitHub, Jira, etc.) unless they contain failures
- Emails already replied to
- FYI emails that require no action

Return only a JSON array of the email IDs that need attention.
Return [] if nothing is important.

Emails:
{email_list}`
