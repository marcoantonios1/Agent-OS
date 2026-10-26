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
// the user about important emails. It uses a SeenStore to ensure each email is
// triaged and surfaced at most once, and records surfaced emails in episodic
// memory so agents can recall them later.
type Monitor struct {
	cfg      Config
	email    email.EmailProvider
	llm      costguard.LLMClient
	episodic episodic.Store
	seen     SeenStore
	notifier Notifier
	log      *slog.Logger
}

// New returns a Monitor. Start Run() in a goroutine to begin polling.
// A nil seen store falls back to an in-memory one (not persisted across restarts).
func New(
	cfg Config,
	emailProv email.EmailProvider,
	llmClient costguard.LLMClient,
	episodicStore episodic.Store,
	seen SeenStore,
	notifier Notifier,
) *Monitor {
	if seen == nil {
		seen = NewMemorySeenStore()
	}
	return &Monitor{
		cfg:      cfg,
		email:    emailProv,
		llm:      llmClient,
		episodic: episodicStore,
		seen:     seen,
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

	// 2. Filter out emails already triaged on a previous tick.
	unseen := m.filterAlreadySeen(ctx, emails)
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

	// 5. Persist episodic memories for surfaced emails and mark everything
	// triaged this tick as seen.
	m.persistNotified(ctx, important)
	m.markChecked(ctx, unseen)
}

// filterAlreadySeen returns emails that have not been triaged before.
// On lookup error, the email is included (prefer over-notifying to missing it).
func (m *Monitor) filterAlreadySeen(ctx context.Context, emails []email.EmailSummary) []email.EmailSummary {
	var unseen []email.EmailSummary
	for _, e := range emails {
		seen, err := m.seen.IsSeen(ctx, m.cfg.UserID, e.ID)
		if err != nil {
			m.log.WarnContext(ctx, "inbox monitor: seen lookup failed", "error", err)
		}
		if !seen {
			unseen = append(unseen, e)
		}
	}
	return unseen
}

// triageEmails calls the LLM with the triage prompt and returns the subset of
// emails identified as important. IDs not found in emails are silently ignored.
//
// Emails are given short aliases (e1, e2, …) in the prompt so the LLM only
// needs to output small tokens rather than echoing long Outlook/Gmail IDs.
func (m *Monitor) triageEmails(ctx context.Context, emails []email.EmailSummary) ([]email.EmailSummary, error) {
	// Assign short aliases so the LLM outputs "e1" not a 150-char Outlook ID.
	aliased := make([]email.EmailSummary, len(emails))
	aliasToEmail := make(map[string]email.EmailSummary, len(emails))
	for i, e := range emails {
		alias := fmt.Sprintf("e%d", i+1)
		aliased[i] = e
		aliased[i].ID = alias
		aliasToEmail[alias] = e
	}

	emailList := formatEmailListForPrompt(aliased)
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
	if resp.Content == "" {
		return nil, fmt.Errorf("triage: llm returned empty content (model may be a thinking model that exhausted its token budget before outputting JSON — try a smaller non-thinking model)")
	}

	raw := resp.Content
	// Extract the first [...] array from anywhere in the response —
	// models often output reasoning text before the JSON.
	if start := strings.Index(raw, "["); start >= 0 {
		if end := strings.LastIndex(raw, "]"); end >= start {
			raw = raw[start : end+1]
		}
	}
	raw = strings.TrimSpace(raw)

	var importantAliases []string
	if err := json.Unmarshal([]byte(raw), &importantAliases); err != nil {
		return nil, fmt.Errorf("triage: parse json %q: %w", raw, err)
	}

	var important []email.EmailSummary
	for _, alias := range importantAliases {
		if e, ok := aliasToEmail[alias]; ok {
			important = append(important, e)
		}
	}
	return important, nil
}

// markChecked records each email in the seen store so the monitor doesn't
// re-evaluate it on the next tick.
func (m *Monitor) markChecked(ctx context.Context, emails []email.EmailSummary) {
	for _, e := range emails {
		if err := m.seen.MarkSeen(ctx, m.cfg.UserID, e.ID); err != nil {
			m.log.WarnContext(ctx, "inbox monitor: failed to mark email seen", "error", err)
		}
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

// formatEmailListForPrompt serialises a list of EmailSummary for the triage prompt.
// Date is omitted and snippets are truncated to keep prompt tokens small.
func formatEmailListForPrompt(emails []email.EmailSummary) string {
	var sb strings.Builder
	for _, e := range emails {
		snippet := truncate(e.Snippet, 60)
		sb.WriteString(fmt.Sprintf("ID:%s From:%s Subject:%s Snippet:%s\n",
			e.ID, e.From, e.Subject, snippet))
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

const triagePrompt = `You are an email triage assistant. Identify which emails need the user's attention.

Flag ONLY if a real person or service needs the user to act: a reply or decision within 48h, a deadline, a meeting invite or change, a bill/payment problem, a security alert the user did not trigger, or a failure.
Do NOT flag: newsletters, promotions, discounts, contests, marketing, shopping or delivery updates, one-time passwords / verification codes, daily agendas or digests, receipts, FYI-only emails.
When in doubt, do NOT flag. Most emails are not important.

Emails:
{email_list}

Output ONLY a raw JSON array of important email IDs. No explanation, no markdown, no preamble.
Example: ["id1","id2"]
If nothing is important: []`
