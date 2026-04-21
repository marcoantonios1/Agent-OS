package reminder_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/marcoantonios1/Agent-OS/internal/memory"
	"github.com/marcoantonios1/Agent-OS/internal/sessions"
	"github.com/marcoantonios1/Agent-OS/internal/tools/reminder"
)

func ctxWithUser(userID string) context.Context {
	return sessions.WithUserID(context.Background(), userID)
}

func newStore() sessions.ReminderStore {
	return memory.NewReminderStore()
}

// ── parseWhen (via SetTool) ───────────────────────────────────────────────────

func TestSetTool_RelativeMinutes(t *testing.T) {
	store := newStore()
	tool := reminder.NewSetTool(store)

	before := time.Now()
	out := mustExec(t, tool, ctxWithUser("u1"), map[string]any{
		"message": "check in",
		"when":    "in 30 minutes",
	})
	after := time.Now()

	var result struct {
		Status string `json:"status"`
		ID     string `json:"id"`
		FireAt string `json:"fire_at"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if result.Status != "ok" {
		t.Errorf("status: got %q, want ok", result.Status)
	}
	if result.ID == "" {
		t.Error("expected non-empty ID")
	}
	fireAt, err := time.Parse(time.RFC3339, result.FireAt)
	if err != nil {
		t.Fatalf("parse fire_at: %v", err)
	}
	want := before.Add(30 * time.Minute)
	// Allow 2s tolerance for test execution time.
	if fireAt.Before(want.Add(-2*time.Second)) || fireAt.After(after.Add(30*time.Minute+2*time.Second)) {
		t.Errorf("fire_at %v not in expected range [%v, %v]", fireAt, want, after.Add(30*time.Minute))
	}
}

func TestSetTool_RelativeHours(t *testing.T) {
	store := newStore()
	tool := reminder.NewSetTool(store)

	before := time.Now()
	out := mustExec(t, tool, ctxWithUser("u1"), map[string]any{
		"message": "follow up",
		"when":    "in 2 hours",
	})
	fireAt := extractFireAt(t, out)
	if fireAt.Before(before.Add(2*time.Hour - 2*time.Second)) {
		t.Errorf("fire_at %v too early", fireAt)
	}
}

func TestSetTool_AbsoluteRFC3339(t *testing.T) {
	store := newStore()
	tool := reminder.NewSetTool(store)

	target := "2099-06-15T10:00:00Z"
	out := mustExec(t, tool, ctxWithUser("u1"), map[string]any{
		"message": "far future",
		"when":    target,
	})
	fireAt := extractFireAt(t, out)
	want, _ := time.Parse(time.RFC3339, target)
	if !fireAt.Equal(want) {
		t.Errorf("fire_at: got %v, want %v", fireAt, want)
	}
}

func TestSetTool_InvalidWhen(t *testing.T) {
	store := newStore()
	tool := reminder.NewSetTool(store)

	out, err := tool.Execute(ctxWithUser("u1"), toJSON(t, map[string]any{
		"message": "test",
		"when":    "next thursday",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var result map[string]string
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if result["error"] == "" {
		t.Error("expected error field in result")
	}
}

func TestSetTool_NoUserID(t *testing.T) {
	store := newStore()
	tool := reminder.NewSetTool(store)

	out, err := tool.Execute(context.Background(), toJSON(t, map[string]any{
		"message": "test",
		"when":    "in 1 hour",
	}))
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]string
	json.Unmarshal([]byte(out), &result) //nolint:errcheck
	if result["error"] == "" {
		t.Error("expected error for missing user ID")
	}
}

// ── CancelTool ────────────────────────────────────────────────────────────────

func TestCancelTool_CancelsOwned(t *testing.T) {
	store := newStore()
	setOut := mustExec(t, reminder.NewSetTool(store), ctxWithUser("u1"), map[string]any{
		"message": "to cancel",
		"when":    "in 1 hour",
	})

	var setResult struct{ ID string `json:"id"` }
	json.Unmarshal([]byte(setOut), &setResult) //nolint:errcheck

	cancelOut := mustExec(t, reminder.NewCancelTool(store), ctxWithUser("u1"), map[string]any{
		"id": setResult.ID,
	})

	var cancelResult map[string]string
	json.Unmarshal([]byte(cancelOut), &cancelResult) //nolint:errcheck
	if cancelResult["status"] != "cancelled" {
		t.Errorf("status: got %q, want cancelled", cancelResult["status"])
	}
}

func TestCancelTool_CannotCancelOtherUser(t *testing.T) {
	store := newStore()
	setOut := mustExec(t, reminder.NewSetTool(store), ctxWithUser("u1"), map[string]any{
		"message": "private",
		"when":    "in 1 hour",
	})
	var setResult struct{ ID string `json:"id"` }
	json.Unmarshal([]byte(setOut), &setResult) //nolint:errcheck

	out := mustExec(t, reminder.NewCancelTool(store), ctxWithUser("u2"), map[string]any{
		"id": setResult.ID,
	})
	var result map[string]string
	json.Unmarshal([]byte(out), &result) //nolint:errcheck
	if result["error"] == "" {
		t.Error("expected error when cancelling another user's reminder")
	}
}

// ── ListTool ──────────────────────────────────────────────────────────────────

func TestListTool_ReturnsUserReminders(t *testing.T) {
	store := newStore()
	setTool := reminder.NewSetTool(store)

	mustExec(t, setTool, ctxWithUser("u1"), map[string]any{"message": "first", "when": "in 1 hour"})
	mustExec(t, setTool, ctxWithUser("u1"), map[string]any{"message": "second", "when": "in 2 hours"})
	mustExec(t, setTool, ctxWithUser("u2"), map[string]any{"message": "other user", "when": "in 1 hour"})

	out := mustExec(t, reminder.NewListTool(store), ctxWithUser("u1"), nil)

	var list []struct {
		ID      string `json:"id"`
		Message string `json:"message"`
		FireAt  string `json:"fire_at"`
	}
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(list) != 2 {
		t.Errorf("expected 2 reminders for u1, got %d", len(list))
	}
	// Ordered by fire_at ascending: first before second.
	if len(list) == 2 && list[0].Message != "first" {
		t.Errorf("expected 'first' first, got %q", list[0].Message)
	}
}

func TestListTool_EmptyList(t *testing.T) {
	out := mustExec(t, reminder.NewListTool(newStore()), ctxWithUser("u1"), nil)
	var list []any
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("expected empty list, got %d items", len(list))
	}
}

// ── Worker ────────────────────────────────────────────────────────────────────

func TestWorker_FiresDueReminders(t *testing.T) {
	store := memory.NewReminderStore()

	past := time.Now().Add(-1 * time.Second)
	r := &sessions.Reminder{
		ID:      "r1",
		UserID:  "u1",
		Message: "fire me",
		FireAt:  past,
	}
	if err := store.Save(r); err != nil {
		t.Fatal(err)
	}

	var fired []*sessions.Reminder
	n := &captureNotifier{&fired}

	w := reminder.NewWorker(store)
	w.AddNotifier(n)
	w.Interval = 10 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	w.Run(ctx)

	if len(fired) != 1 || fired[0].ID != "r1" {
		t.Errorf("expected r1 to fire, got %v", fired)
	}
}

type captureNotifier struct {
	fired *[]*sessions.Reminder
}

func (c *captureNotifier) NotifyReminder(_ context.Context, r *sessions.Reminder) error {
	*c.fired = append(*c.fired, r)
	return nil
}

// ── helpers ───────────────────────────────────────────────────────────────────

type executor interface {
	Execute(ctx context.Context, input json.RawMessage) (string, error)
}

func mustExec(t *testing.T, tool executor, ctx context.Context, input map[string]any) string {
	t.Helper()
	var raw json.RawMessage
	if input == nil {
		raw = json.RawMessage(`{}`)
	} else {
		raw = toJSON(t, input)
	}
	out, err := tool.Execute(ctx, raw)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	return out
}

func toJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func extractFireAt(t *testing.T, out string) time.Time {
	t.Helper()
	var result struct {
		FireAt string `json:"fire_at"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	fireAt, err := time.Parse(time.RFC3339, result.FireAt)
	if err != nil {
		t.Fatalf("parse fire_at %q: %v", result.FireAt, err)
	}
	return fireAt
}
