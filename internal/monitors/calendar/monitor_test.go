package calendarmonitor_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marcoantonios1/Agent-OS/internal/costguard"
	"github.com/marcoantonios1/Agent-OS/internal/memory/episodic"
	calendarmonitor "github.com/marcoantonios1/Agent-OS/internal/monitors/calendar"
	"github.com/marcoantonios1/Agent-OS/internal/sessions"
	"github.com/marcoantonios1/Agent-OS/internal/tools/calendar"
	"github.com/marcoantonios1/Agent-OS/internal/types"
)

type mockCalendarProvider struct {
	events []calendar.Event
	err    error
}

func (m *mockCalendarProvider) List(_ context.Context, _, _ time.Time) ([]calendar.Event, error) {
	return m.events, m.err
}
func (m *mockCalendarProvider) Read(_ context.Context, _ string) (*calendar.Event, error) {
	return nil, nil
}
func (m *mockCalendarProvider) Create(_ context.Context, _ calendar.CreateEventInput) (*calendar.Event, error) {
	return nil, nil
}
func (m *mockCalendarProvider) Update(_ context.Context, _ calendar.UpdateEventInput) (*calendar.Event, error) {
	return nil, nil
}

type mockEpisodicCalStore struct {
	mu        sync.Mutex
	saved     []episodic.Memory
	results   map[string][]episodic.Memory
	searchErr error
}

func (m *mockEpisodicCalStore) Save(_ context.Context, mem episodic.Memory, _ []float32) error {
	return nil
}
func (m *mockEpisodicCalStore) SaveText(_ context.Context, mem episodic.Memory) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.saved = append(m.saved, mem)
	return nil
}
func (m *mockEpisodicCalStore) Search(_ context.Context, _ string, _ []float32, _ int) ([]episodic.Memory, error) {
	return nil, nil
}
func (m *mockEpisodicCalStore) SearchByText(_ context.Context, _, query string, _ int) ([]episodic.Memory, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.searchErr != nil {
		return nil, m.searchErr
	}
	for key, mems := range m.results {
		if strings.Contains(query, key) {
			return mems, nil
		}
	}
	return nil, nil
}
func (m *mockEpisodicCalStore) Recent(_ context.Context, _ string, _ int) ([]episodic.Memory, error) {
	return nil, nil
}
func (m *mockEpisodicCalStore) Delete(_ context.Context, _ string) error { return nil }
func (m *mockEpisodicCalStore) Prune(_ context.Context, _ string, _ time.Duration) error {
	return nil
}
func (m *mockEpisodicCalStore) savedCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.saved)
}

type mockLLMCal struct{}

func (m *mockLLMCal) Complete(_ context.Context, _ costguard.CompletionRequest) (costguard.CompletionResponse, error) {
	return costguard.CompletionResponse{Content: ""}, nil
}
func (m *mockLLMCal) Stream(_ context.Context, _ costguard.CompletionRequest) (<-chan costguard.StreamChunk, error) {
	return nil, nil
}
func (m *mockLLMCal) Embed(_ context.Context, _ string) ([]float32, error) { return nil, nil }

type mockCalNotifier struct {
	mu   sync.Mutex
	msgs []*sessions.Reminder
}

func (n *mockCalNotifier) NotifyReminder(_ context.Context, r *sessions.Reminder) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.msgs = append(n.msgs, r)
	return nil
}
func (n *mockCalNotifier) count() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.msgs)
}
func (n *mockCalNotifier) last() *sessions.Reminder {
	n.mu.Lock()
	defer n.mu.Unlock()
	if len(n.msgs) == 0 {
		return nil
	}
	return n.msgs[len(n.msgs)-1]
}

func makeCalConfig() calendarmonitor.Config {
	return calendarmonitor.Config{
		Interval:    time.Hour,
		Lookahead:   24 * time.Hour,
		BriefBefore: 30 * time.Minute,
		Channel:     types.ChannelID("discord"),
		UserID:      "user1",
		Model:       "test-model",
	}
}

// TestCalendarMonitor_EventInWindow_BriefSent: event in 25 minutes → brief sent and memory saved.
func TestCalendarMonitor_EventInWindow_BriefSent(t *testing.T) {
	now := time.Now()
	events := []calendar.Event{
		{
			ID:        "evt-1",
			Title:     "Q3 Budget Review",
			Start:     now.Add(25 * time.Minute),
			End:       now.Add(85 * time.Minute),
			Attendees: []string{"alice@example.com", "bob@example.com"},
		},
	}

	store := &mockEpisodicCalStore{}
	notifier := &mockCalNotifier{}
	mon := calendarmonitor.New(makeCalConfig(), &mockCalendarProvider{events: events}, &mockLLMCal{}, store, notifier)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	mon.RunOnce(ctx)

	if notifier.count() != 1 {
		t.Fatalf("expected 1 brief, got %d", notifier.count())
	}
	msg := notifier.last().Message
	if !strings.Contains(msg, "Q3 Budget Review") {
		t.Errorf("brief should contain event title, got: %s", msg)
	}
	if !strings.Contains(msg, "alice@example.com") {
		t.Errorf("brief should contain attendees, got: %s", msg)
	}
	if !strings.Contains(msg, "minute") {
		t.Errorf("brief should mention time until event, got: %s", msg)
	}
	if store.savedCount() == 0 {
		t.Error("expected episodic memory to be saved after briefing")
	}
}

// TestCalendarMonitor_AlreadyBriefed_Skipped: event already briefed → no notification sent.
func TestCalendarMonitor_AlreadyBriefed_Skipped(t *testing.T) {
	now := time.Now()
	events := []calendar.Event{
		{
			ID:    "evt-already",
			Title: "Team Standup",
			Start: now.Add(10 * time.Minute),
			End:   now.Add(40 * time.Minute),
		},
	}

	store := &mockEpisodicCalStore{
		results: map[string][]episodic.Memory{
			"evt-already": {
				{
					ID:        "mem-1",
					Content:   "Sent calendar brief for event evt-already (Team Standup) at 2026-06-02T10:00:00Z",
					Distance:  0.05,
					CreatedAt: time.Now().Add(-5 * time.Minute),
				},
			},
		},
	}
	notifier := &mockCalNotifier{}
	mon := calendarmonitor.New(makeCalConfig(), &mockCalendarProvider{events: events}, &mockLLMCal{}, store, notifier)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	mon.RunOnce(ctx)

	if notifier.count() != 0 {
		t.Errorf("expected 0 notifications for already-briefed event, got %d", notifier.count())
	}
}

// TestCalendarMonitor_EpisodicContext_IncludedInBrief: episodic memory about Alice → included in brief.
func TestCalendarMonitor_EpisodicContext_IncludedInBrief(t *testing.T) {
	now := time.Now()
	events := []calendar.Event{
		{
			ID:        "evt-alice",
			Title:     "Budget discussion",
			Start:     now.Add(20 * time.Minute),
			End:       now.Add(80 * time.Minute),
			Attendees: []string{"alice@example.com"},
		},
	}

	store := &mockEpisodicCalStore{
		results: map[string][]episodic.Memory{
			"alice": {
				{
					ID:        "mem-alice",
					Content:   "Alice tends to push back on line items over $50k",
					Distance:  0.3,
					CreatedAt: time.Now().Add(-7 * 24 * time.Hour),
				},
			},
		},
	}
	notifier := &mockCalNotifier{}
	mon := calendarmonitor.New(makeCalConfig(), &mockCalendarProvider{events: events}, &mockLLMCal{}, store, notifier)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	mon.RunOnce(ctx)

	if notifier.count() != 1 {
		t.Fatalf("expected 1 brief, got %d", notifier.count())
	}
	msg := notifier.last().Message
	if !strings.Contains(msg, "Alice tends to push back") {
		t.Errorf("brief should contain episodic context about Alice, got:\n%s", msg)
	}
	if !strings.Contains(msg, "Context from past conversations") {
		t.Errorf("brief should contain context section header, got:\n%s", msg)
	}
}

// TestCalendarMonitor_ProviderError_NoNotification: calendar provider error → no notification, no panic.
func TestCalendarMonitor_ProviderError_NoNotification(t *testing.T) {
	store := &mockEpisodicCalStore{}
	notifier := &mockCalNotifier{}
	calProv := &mockCalendarProvider{err: fmt.Errorf("google calendar: connection refused")}
	mon := calendarmonitor.New(makeCalConfig(), calProv, &mockLLMCal{}, store, notifier)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	mon.RunOnce(ctx)

	if notifier.count() != 0 {
		t.Errorf("expected no notifications on provider error, got %d", notifier.count())
	}
}
