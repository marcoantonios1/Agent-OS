package reminder_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/marcoantonios1/Agent-OS/internal/sessions"
	"github.com/marcoantonios1/Agent-OS/internal/tools/reminder"
	"github.com/marcoantonios1/Agent-OS/internal/types"
)

// ── mocks ─────────────────────────────────────────────────────────────────────

type mockReminderStore struct {
	mu   sync.Mutex
	due  []*sessions.Reminder
}

func (m *mockReminderStore) Save(r *sessions.Reminder) error          { return nil }
func (m *mockReminderStore) Get(id string) (*sessions.Reminder, error) {
	return nil, sessions.ErrReminderNotFound
}
func (m *mockReminderStore) Delete(id string) error                         { return nil }
func (m *mockReminderStore) ListForUser(_ string) ([]*sessions.Reminder, error) { return nil, nil }
func (m *mockReminderStore) Due(_ time.Time) ([]*sessions.Reminder, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := m.due
	m.due = nil
	return out, nil
}

type mockNotifier struct {
	mu       sync.Mutex
	received []*sessions.Reminder
}

func (n *mockNotifier) NotifyReminder(_ context.Context, r *sessions.Reminder) error {
	n.mu.Lock()
	cp := *r
	n.received = append(n.received, &cp)
	n.mu.Unlock()
	return nil
}

type mockDispatcher struct {
	response string
	err      error
	calls    []types.InboundMessage
	mu       sync.Mutex
}

func (d *mockDispatcher) Route(_ context.Context, msg types.InboundMessage) (types.OutboundMessage, error) {
	d.mu.Lock()
	d.calls = append(d.calls, msg)
	d.mu.Unlock()
	return types.OutboundMessage{Text: d.response}, d.err
}

// ── helpers ───────────────────────────────────────────────────────────────────

func plainReminder() *sessions.Reminder {
	return &sessions.Reminder{
		ID:        "r1",
		UserID:    "u1",
		SessionID: "s1",
		ChannelID: "web",
		Message:   "plain text",
		FireAt:    time.Now().Add(-time.Second),
	}
}

func contextAwareReminder() *sessions.Reminder {
	r := plainReminder()
	r.AgentPrompt = "Search Alice's emails about the invoice and summarise."
	return r
}

// ── tests ─────────────────────────────────────────────────────────────────────

func TestWorker_PlainReminder_NotifiesDirectly(t *testing.T) {
	store := &mockReminderStore{due: []*sessions.Reminder{plainReminder()}}
	notifier := &mockNotifier{}
	dispatcher := &mockDispatcher{response: "enriched"}

	w := reminder.NewWorker(store)
	w.AddNotifier(notifier)
	w.SetDispatcher(dispatcher)

	w.FireNow(context.Background(), time.Now())

	if len(dispatcher.calls) != 0 {
		t.Errorf("dispatcher should not be called for plain reminder, got %d calls", len(dispatcher.calls))
	}
	if len(notifier.received) != 1 {
		t.Fatalf("expected 1 notification, got %d", len(notifier.received))
	}
	if notifier.received[0].Message != "plain text" {
		t.Errorf("message = %q, want %q", notifier.received[0].Message, "plain text")
	}
}

func TestWorker_ContextAwareReminder_DispatchesAndEnriches(t *testing.T) {
	store := &mockReminderStore{due: []*sessions.Reminder{contextAwareReminder()}}
	notifier := &mockNotifier{}
	dispatcher := &mockDispatcher{response: "Here's Alice's latest invoice email: ..."}

	w := reminder.NewWorker(store)
	w.AddNotifier(notifier)
	w.SetDispatcher(dispatcher)

	w.FireNow(context.Background(), time.Now())

	if len(dispatcher.calls) != 1 {
		t.Fatalf("expected 1 dispatcher call, got %d", len(dispatcher.calls))
	}
	if dispatcher.calls[0].Text != contextAwareReminder().AgentPrompt {
		t.Errorf("dispatcher input = %q, want %q", dispatcher.calls[0].Text, contextAwareReminder().AgentPrompt)
	}
	if len(notifier.received) != 1 {
		t.Fatalf("expected 1 notification, got %d", len(notifier.received))
	}
	if notifier.received[0].Message != "Here's Alice's latest invoice email: ..." {
		t.Errorf("message = %q, want enriched text", notifier.received[0].Message)
	}
}

func TestWorker_ContextAwareReminder_DispatcherError_FallsBackToPlain(t *testing.T) {
	store := &mockReminderStore{due: []*sessions.Reminder{contextAwareReminder()}}
	notifier := &mockNotifier{}
	dispatcher := &mockDispatcher{err: errors.New("LLM unavailable")}

	w := reminder.NewWorker(store)
	w.AddNotifier(notifier)
	w.SetDispatcher(dispatcher)

	w.FireNow(context.Background(), time.Now())

	if len(notifier.received) != 1 {
		t.Fatalf("expected 1 notification, got %d", len(notifier.received))
	}
	if notifier.received[0].Message != "plain text" {
		t.Errorf("fallback message = %q, want %q", notifier.received[0].Message, "plain text")
	}
}

func TestWorker_ContextAwareReminder_NoDispatcher_FallsBackToPlain(t *testing.T) {
	store := &mockReminderStore{due: []*sessions.Reminder{contextAwareReminder()}}
	notifier := &mockNotifier{}

	w := reminder.NewWorker(store)
	w.AddNotifier(notifier)
	// no dispatcher set

	w.FireNow(context.Background(), time.Now())

	if len(notifier.received) != 1 {
		t.Fatalf("expected 1 notification, got %d", len(notifier.received))
	}
	if notifier.received[0].Message != "plain text" {
		t.Errorf("message = %q, want %q", notifier.received[0].Message, "plain text")
	}
}

func TestWorker_DispatcherReceivesCorrectSessionContext(t *testing.T) {
	r := contextAwareReminder()
	r.SessionID = "session-abc"
	r.UserID = "user-xyz"
	r.ChannelID = "discord"

	store := &mockReminderStore{due: []*sessions.Reminder{r}}
	notifier := &mockNotifier{}
	dispatcher := &mockDispatcher{response: "ok"}

	w := reminder.NewWorker(store)
	w.AddNotifier(notifier)
	w.SetDispatcher(dispatcher)

	w.FireNow(context.Background(), time.Now())

	if len(dispatcher.calls) != 1 {
		t.Fatalf("expected 1 dispatcher call, got %d", len(dispatcher.calls))
	}
	call := dispatcher.calls[0]
	if call.SessionID != "session-abc" {
		t.Errorf("SessionID = %q, want %q", call.SessionID, "session-abc")
	}
	if call.UserID != "user-xyz" {
		t.Errorf("UserID = %q, want %q", call.UserID, "user-xyz")
	}
	if call.ChannelID != "discord" {
		t.Errorf("ChannelID = %q, want %q", call.ChannelID, "discord")
	}
}
