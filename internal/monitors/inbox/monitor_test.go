package inbox_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marcoantonios1/Agent-OS/internal/costguard"
	"github.com/marcoantonios1/Agent-OS/internal/memory/episodic"
	"github.com/marcoantonios1/Agent-OS/internal/monitors/inbox"
	"github.com/marcoantonios1/Agent-OS/internal/sessions"
	"github.com/marcoantonios1/Agent-OS/internal/tools/email"
)

// mockEmailProvider returns a fixed list of emails.
type mockEmailProvider struct {
	emails []email.EmailSummary
	err    error
}

func (m *mockEmailProvider) List(_ context.Context, _ int) ([]email.EmailSummary, error) {
	return m.emails, m.err
}
func (m *mockEmailProvider) Read(_ context.Context, _ string) (*email.Email, error) {
	return nil, nil
}
func (m *mockEmailProvider) Search(_ context.Context, _ string) ([]email.EmailSummary, error) {
	return nil, nil
}
func (m *mockEmailProvider) Draft(_ context.Context, _, _, _ string) (*email.Draft, error) {
	return nil, nil
}
func (m *mockEmailProvider) Send(_ context.Context, _, _, _ string) error { return nil }

// mockEpisodicStore returns scripted search results and records SaveText calls.
type mockEpisodicStore struct {
	mu        sync.Mutex
	saved     []episodic.Memory
	results   map[string][]episodic.Memory // keyed by query substring
	searchErr error
}

func (m *mockEpisodicStore) Save(_ context.Context, mem episodic.Memory, _ []float32) error {
	return nil
}
func (m *mockEpisodicStore) SaveText(_ context.Context, mem episodic.Memory) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.saved = append(m.saved, mem)
	return nil
}
func (m *mockEpisodicStore) Search(_ context.Context, _ string, _ []float32, _ int) ([]episodic.Memory, error) {
	return nil, nil
}
func (m *mockEpisodicStore) SearchByText(_ context.Context, _, query string, _ int) ([]episodic.Memory, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.searchErr != nil {
		return nil, m.searchErr
	}
	if m.results != nil {
		for key, mems := range m.results {
			if strings.Contains(query, key) {
				return mems, nil
			}
		}
	}
	return nil, nil
}
func (m *mockEpisodicStore) Recent(_ context.Context, _ string, _ int) ([]episodic.Memory, error) {
	return nil, nil
}
func (m *mockEpisodicStore) Delete(_ context.Context, _ string) error  { return nil }
func (m *mockEpisodicStore) Prune(_ context.Context, _ string, _ time.Duration) error {
	return nil
}
func (m *mockEpisodicStore) savedCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.saved)
}

// mockLLMInbox returns a scripted response.
type mockLLMInbox struct {
	content    string
	err        error
	lastPrompt string
}

func (m *mockLLMInbox) Complete(_ context.Context, req costguard.CompletionRequest) (costguard.CompletionResponse, error) {
	if len(req.Messages) > 0 {
		m.lastPrompt = req.Messages[len(req.Messages)-1].Content
	}
	return costguard.CompletionResponse{Content: m.content}, m.err
}
func (m *mockLLMInbox) Stream(_ context.Context, _ costguard.CompletionRequest) (<-chan costguard.StreamChunk, error) {
	return nil, nil
}
func (m *mockLLMInbox) Embed(_ context.Context, _ string) ([]float32, error) { return nil, nil }

// mockNotifier records delivered notifications.
type mockNotifier struct {
	mu   sync.Mutex
	msgs []*sessions.Reminder
}

func (n *mockNotifier) NotifyReminder(_ context.Context, r *sessions.Reminder) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.msgs = append(n.msgs, r)
	return nil
}
func (n *mockNotifier) count() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.msgs)
}
func (n *mockNotifier) last() *sessions.Reminder {
	n.mu.Lock()
	defer n.mu.Unlock()
	if len(n.msgs) == 0 {
		return nil
	}
	return n.msgs[len(n.msgs)-1]
}

// makeConfig returns a minimal Config for tests.
func makeConfig() inbox.Config {
	return inbox.Config{
		Interval:  time.Hour,
		Channel:   "discord",
		UserID:    "user1",
		MaxEmails: 20,
		Model:     "test-model",
	}
}

// TestInboxMonitor_SkipsSeenEmails: 5 emails, 2 already seen → only 3 triaged,
// 1 flagged important → 1 notification.
func TestInboxMonitor_SkipsSeenEmails(t *testing.T) {
	emails := []email.EmailSummary{
		{ID: "e1", From: "alice@example.com", Subject: "Q3 budget approval needed by Friday", Date: time.Now()},
		{ID: "e2", From: "ceo@example.com", Subject: "Quick call this week?", Date: time.Now()},
		{ID: "e3", From: "aws@amazon.com", Subject: "Action required: certificate expiring", Date: time.Now()},
		{ID: "e4", From: "news@acme.com", Subject: "Weekly digest", Date: time.Now()},
		{ID: "e5", From: "bot@github.com", Subject: "Build #42 passed", Date: time.Now()},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	seen := inbox.NewMemorySeenStore()
	_ = seen.MarkSeen(ctx, "user1", "e4")
	_ = seen.MarkSeen(ctx, "user1", "e5")

	llm := &mockLLMInbox{content: `["e1"]`}
	notifier := &mockNotifier{}
	mon := inbox.New(makeConfig(), &mockEmailProvider{emails: emails}, llm, &mockEpisodicStore{}, seen, notifier)
	mon.RunOnce(ctx)

	if notifier.count() != 1 {
		t.Fatalf("expected 1 notification, got %d", notifier.count())
	}
	if !strings.Contains(notifier.last().Message, "Q3 budget") {
		t.Errorf("notification should mention Q3 budget email, got: %s", notifier.last().Message)
	}
	if strings.Contains(llm.lastPrompt, "Weekly digest") || strings.Contains(llm.lastPrompt, "Build #42") {
		t.Errorf("seen emails should not be sent to triage, prompt: %s", llm.lastPrompt)
	}
}

// TestInboxMonitor_NoRenotifyOnNextTick: an email surfaced once is not
// surfaced again on the following check.
func TestInboxMonitor_NoRenotifyOnNextTick(t *testing.T) {
	emails := []email.EmailSummary{
		{ID: "e1", From: "alice@example.com", Subject: "Contract to sign today", Date: time.Now()},
		{ID: "e2", From: "promo@shop.com", Subject: "50% off", Date: time.Now()},
	}
	llm := &mockLLMInbox{content: `["e1"]`}
	notifier := &mockNotifier{}
	mon := inbox.New(makeConfig(), &mockEmailProvider{emails: emails}, llm, &mockEpisodicStore{}, nil, notifier)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	mon.RunOnce(ctx)
	mon.RunOnce(ctx)

	if notifier.count() != 1 {
		t.Fatalf("expected 1 notification across two ticks, got %d", notifier.count())
	}
}

// TestInboxMonitor_LLMError_NoNotification: LLM returns error → no notification, no panic.
func TestInboxMonitor_LLMError_NoNotification(t *testing.T) {
	emails := []email.EmailSummary{
		{ID: "e1", From: "alice@example.com", Subject: "Important proposal", Date: time.Now()},
	}
	store := &mockEpisodicStore{}
	llm := &mockLLMInbox{err: fmt.Errorf("LLM unavailable")}
	notifier := &mockNotifier{}

	mon := inbox.New(makeConfig(), &mockEmailProvider{emails: emails}, llm, store, nil, notifier)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	mon.RunOnce(ctx)

	if notifier.count() != 0 {
		t.Errorf("expected no notification on LLM error, got %d", notifier.count())
	}
}

// TestInboxMonitor_EmailProviderError_NoNotification: email provider error → no notification, no panic.
func TestInboxMonitor_EmailProviderError_NoNotification(t *testing.T) {
	store := &mockEpisodicStore{}
	llm := &mockLLMInbox{content: "[]"}
	notifier := &mockNotifier{}
	emailProv := &mockEmailProvider{err: fmt.Errorf("connection refused")}

	mon := inbox.New(makeConfig(), emailProv, llm, store, nil, notifier)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	mon.RunOnce(ctx)

	if notifier.count() != 0 {
		t.Errorf("expected no notification on provider error, got %d", notifier.count())
	}
}
