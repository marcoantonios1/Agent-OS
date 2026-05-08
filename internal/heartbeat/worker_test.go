package heartbeat_test

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/marcoantonios1/Agent-OS/internal/heartbeat"
	"github.com/marcoantonios1/Agent-OS/internal/sessions"
	"github.com/marcoantonios1/Agent-OS/internal/types"
)

// ── mocks ─────────────────────────────────────────────────────────────────────

type mockDispatcher struct {
	mu       sync.Mutex
	calls    []types.InboundMessage
	response string
}

func (m *mockDispatcher) Route(_ context.Context, msg types.InboundMessage) (types.OutboundMessage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, msg)
	return types.OutboundMessage{
		SessionID: msg.SessionID,
		UserID:    msg.UserID,
		Text:      m.response,
	}, nil
}

func (m *mockDispatcher) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}

func (m *mockDispatcher) lastCall() types.InboundMessage {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls[len(m.calls)-1]
}

type mockNotifier struct {
	mu        sync.Mutex
	delivered []*sessions.Reminder
}

func (n *mockNotifier) NotifyReminder(_ context.Context, r *sessions.Reminder) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.delivered = append(n.delivered, r)
	return nil
}

func (n *mockNotifier) count() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.delivered)
}

func (n *mockNotifier) last() *sessions.Reminder {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.delivered[len(n.delivered)-1]
}

// ── tests ─────────────────────────────────────────────────────────────────────

func TestWorker_DispatchesPromptOnTick(t *testing.T) {
	d := &mockDispatcher{response: "You have 2 urgent emails."}
	n := &mockNotifier{}

	cfg := heartbeat.Config{
		Interval:  20 * time.Millisecond,
		UserID:    "u1",
		SessionID: "heartbeat",
		Channel:   "discord",
		Prompt:    "Check my emails for anything urgent.",
	}
	w := heartbeat.New(cfg, d)
	w.AddNotifier("discord", n)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer cancel()
	go w.Run(ctx)
	<-ctx.Done()

	if d.callCount() == 0 {
		t.Fatal("expected Route to be called at least once")
	}
	last := d.lastCall()
	if last.Text != "Check my emails for anything urgent." {
		t.Errorf("unexpected prompt: %q", last.Text)
	}
	if last.UserID != "u1" {
		t.Errorf("unexpected user_id: %q", last.UserID)
	}
	if last.SessionID != "heartbeat" {
		t.Errorf("unexpected session_id: %q", last.SessionID)
	}
}

func TestWorker_NotifierReceivesResponseText(t *testing.T) {
	d := &mockDispatcher{response: "No urgent emails."}
	n := &mockNotifier{}

	cfg := heartbeat.Config{
		Interval:  20 * time.Millisecond,
		UserID:    "u1",
		SessionID: "heartbeat",
		Channel:   "discord",
		Prompt:    "Check emails.",
	}
	w := heartbeat.New(cfg, d)
	w.AddNotifier("discord", n)

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	go w.Run(ctx)
	<-ctx.Done()

	if n.count() == 0 {
		t.Fatal("expected NotifyReminder to be called at least once")
	}
	if got := n.last().Message; got != "No urgent emails." {
		t.Errorf("notifier received wrong message: %q", got)
	}
}

func TestWorker_StopsOnContextCancellation(t *testing.T) {
	d := &mockDispatcher{response: "ok"}
	n := &mockNotifier{}

	cfg := heartbeat.Config{
		Interval:  10 * time.Millisecond,
		UserID:    "u1",
		SessionID: "heartbeat",
		Channel:   "discord",
		Prompt:    "ping",
	}
	w := heartbeat.New(cfg, d)
	w.AddNotifier("discord", n)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		w.Run(ctx)
		close(done)
	}()

	// Let it tick a couple of times then cancel.
	time.Sleep(35 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("worker did not stop after context cancellation")
	}

	countAfterCancel := d.callCount()
	time.Sleep(30 * time.Millisecond)
	if d.callCount() != countAfterCancel {
		t.Error("worker kept dispatching after context was cancelled")
	}
}

func TestWorker_LoadsPromptFromHEARTBEATmd(t *testing.T) {
	dir := t.TempDir()
	promptFromFile := "Summarise my calendar for today."
	os.WriteFile(filepath.Join(dir, "HEARTBEAT.md"), []byte(promptFromFile), 0o644) //nolint:errcheck

	d := &mockDispatcher{response: "ok"}
	n := &mockNotifier{}

	cfg := heartbeat.Config{
		Interval:     20 * time.Millisecond,
		UserID:       "u1",
		SessionID:    "heartbeat",
		Channel:      "discord",
		Prompt:       "fallback prompt — should not be used",
		WorkspaceDir: dir,
	}
	w := heartbeat.New(cfg, d)
	w.AddNotifier("discord", n)

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	go w.Run(ctx)
	<-ctx.Done()

	if d.callCount() == 0 {
		t.Fatal("expected at least one dispatch")
	}
	if got := d.lastCall().Text; got != promptFromFile {
		t.Errorf("expected prompt from HEARTBEAT.md %q, got %q", promptFromFile, got)
	}
}

func TestWorker_FallsBackToConfigPromptWhenNoFile(t *testing.T) {
	d := &mockDispatcher{response: "ok"}
	n := &mockNotifier{}

	cfg := heartbeat.Config{
		Interval:     20 * time.Millisecond,
		UserID:       "u1",
		SessionID:    "heartbeat",
		Channel:      "discord",
		Prompt:       "config fallback prompt",
		WorkspaceDir: t.TempDir(), // dir exists but no HEARTBEAT.md
	}
	w := heartbeat.New(cfg, d)
	w.AddNotifier("discord", n)

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	go w.Run(ctx)
	<-ctx.Done()

	if d.callCount() == 0 {
		t.Fatal("expected at least one dispatch")
	}
	if got := d.lastCall().Text; got != "config fallback prompt" {
		t.Errorf("expected fallback prompt, got %q", got)
	}
}

func TestWorker_UsesHardcodedDefaultWhenNeitherFileNorEnvSet(t *testing.T) {
	d := &mockDispatcher{response: "ok"}
	n := &mockNotifier{}

	cfg := heartbeat.Config{
		Interval:     20 * time.Millisecond,
		UserID:       "u1",
		SessionID:    "heartbeat",
		Channel:      "discord",
		Prompt:       "", // no env var
		WorkspaceDir: t.TempDir(), // no HEARTBEAT.md
	}
	w := heartbeat.New(cfg, d)
	w.AddNotifier("discord", n)

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	go w.Run(ctx)
	<-ctx.Done()

	if d.callCount() == 0 {
		t.Fatal("expected at least one dispatch")
	}
	const want = "Check my emails for anything urgent and summarize my calendar for today."
	if got := d.lastCall().Text; got != want {
		t.Errorf("expected hardcoded default, got %q", got)
	}
}

func TestWorker_EmptyHEARTBEATmd_FallsBackToEnvPrompt(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "HEARTBEAT.md"), []byte("   \n\t  "), 0o644) //nolint:errcheck

	d := &mockDispatcher{response: "ok"}
	n := &mockNotifier{}

	cfg := heartbeat.Config{
		Interval:     20 * time.Millisecond,
		UserID:       "u1",
		SessionID:    "heartbeat",
		Channel:      "discord",
		Prompt:       "env prompt",
		WorkspaceDir: dir,
	}
	w := heartbeat.New(cfg, d)
	w.AddNotifier("discord", n)

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	go w.Run(ctx)
	<-ctx.Done()

	if d.callCount() == 0 {
		t.Fatal("expected at least one dispatch")
	}
	if got := d.lastCall().Text; got != "env prompt" {
		t.Errorf("empty HEARTBEAT.md should fall back to env prompt, got %q", got)
	}
}

func TestWorker_NoNotifier_DoesNotPanic(t *testing.T) {
	d := &mockDispatcher{response: "ok"}

	cfg := heartbeat.Config{
		Interval:  20 * time.Millisecond,
		UserID:    "u1",
		SessionID: "heartbeat",
		Channel:   "discord",
		Prompt:    "ping",
	}
	w := heartbeat.New(cfg, d)
	// intentionally no notifier registered

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	// should warn and continue, not panic
	go w.Run(ctx)
	<-ctx.Done()
}
