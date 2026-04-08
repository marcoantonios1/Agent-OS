package email_test

import (
	"context"
	"errors"
	"testing"

	"github.com/marcoantonios1/Agent-OS/internal/approval"
	"github.com/marcoantonios1/Agent-OS/internal/tools/email"
)

// ── mock provider (send-capable) ──────────────────────────────────────────────

type mockSendProvider struct {
	sendErr   error
	sendCalls []sendCall
}

type sendCall struct{ to, subject, body string }

func (m *mockSendProvider) List(_ context.Context, _ int) ([]email.EmailSummary, error) {
	return nil, nil
}
func (m *mockSendProvider) Read(_ context.Context, _ string) (*email.Email, error) { return nil, nil }
func (m *mockSendProvider) Search(_ context.Context, _ string) ([]email.EmailSummary, error) {
	return nil, nil
}
func (m *mockSendProvider) Draft(_ context.Context, to, subject, body string) (*email.Draft, error) {
	return &email.Draft{To: to, Subject: subject, Body: body}, nil
}
func (m *mockSendProvider) Send(_ context.Context, to, subject, body string) error {
	if m.sendErr != nil {
		return m.sendErr
	}
	m.sendCalls = append(m.sendCalls, sendCall{to, subject, body})
	return nil
}

// ── email_send tests ──────────────────────────────────────────────────────────

func TestSendTool_Definition(t *testing.T) {
	def := email.NewSendTool(&mockSendProvider{}, approval.NewMemoryStore()).Definition()
	if def.Name != "email_send" {
		t.Errorf("name = %q, want email_send", def.Name)
	}
}

func TestSendTool_ReturnsPendingWithoutApproval(t *testing.T) {
	store := approval.NewMemoryStore()
	p := &mockSendProvider{}
	ctx := approval.WithSessionID(context.Background(), "sess-send")
	result, err := email.NewSendTool(p, store).Execute(ctx, mustMarshal(t, map[string]string{
		"to":      "alice@example.com",
		"subject": "Hello",
		"body":    "Hi Alice!",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	m := decodeMap(t, result)
	if m["status"] != "pending_approval" {
		t.Errorf("expected pending_approval, got %v", m["status"])
	}
	if m["action_id"] == "" {
		t.Error("expected non-empty action_id")
	}
	if len(p.sendCalls) != 0 {
		t.Errorf("Send must not be called without approval, got %d call(s)", len(p.sendCalls))
	}
}

func TestSendTool_ActionIDIsDeterministic(t *testing.T) {
	store := approval.NewMemoryStore()
	p := &mockSendProvider{}
	ctx := approval.WithSessionID(context.Background(), "sess-det")
	input := mustMarshal(t, map[string]string{
		"to": "alice@example.com", "subject": "Hello", "body": "Hi",
	})
	r1, _ := email.NewSendTool(p, store).Execute(ctx, input)
	r2, _ := email.NewSendTool(p, store).Execute(ctx, input)
	if decodeMap(t, r1)["action_id"] != decodeMap(t, r2)["action_id"] {
		t.Error("action_id must be deterministic for identical inputs")
	}
}

func TestSendTool_SendsAfterApproval(t *testing.T) {
	store := approval.NewMemoryStore()
	p := &mockSendProvider{}
	const sess = "sess-approved"
	input := mustMarshal(t, map[string]string{
		"to": "alice@example.com", "subject": "Hello", "body": "Hi Alice!",
	})
	ctx := approval.WithSessionID(context.Background(), sess)

	// First call → pending.
	r, _ := email.NewSendTool(p, store).Execute(ctx, input)
	actionID := decodeMap(t, r)["action_id"].(string)

	// Simulate router granting approval.
	store.Grant(sess, actionID)

	// Second call → sends.
	result, err := email.NewSendTool(p, store).Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error after approval: %v", err)
	}
	m := decodeMap(t, result)
	if m["status"] != "sent" {
		t.Errorf("expected status=sent, got %v", m["status"])
	}
	if len(p.sendCalls) != 1 {
		t.Errorf("expected 1 Send call, got %d", len(p.sendCalls))
	}
	if p.sendCalls[0].to != "alice@example.com" {
		t.Errorf("wrong recipient: %s", p.sendCalls[0].to)
	}
}

func TestSendTool_ApprovalConsumedAfterUse(t *testing.T) {
	store := approval.NewMemoryStore()
	p := &mockSendProvider{}
	const sess = "sess-consume"
	input := mustMarshal(t, map[string]string{
		"to": "alice@example.com", "subject": "Hello", "body": "Hi",
	})
	ctx := approval.WithSessionID(context.Background(), sess)

	r, _ := email.NewSendTool(p, store).Execute(ctx, input)
	store.Grant(sess, decodeMap(t, r)["action_id"].(string))
	email.NewSendTool(p, store).Execute(ctx, input) //nolint:errcheck — send succeeds

	// Third call must require a fresh approval.
	result, err := email.NewSendTool(p, store).Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decodeMap(t, result)["status"] != "pending_approval" {
		t.Error("approval should be consumed after use — expected pending on third call")
	}
	if len(p.sendCalls) != 1 {
		t.Errorf("expected exactly 1 Send call, got %d", len(p.sendCalls))
	}
}

func TestSendTool_MissingTo(t *testing.T) {
	store := approval.NewMemoryStore()
	_, err := email.NewSendTool(&mockSendProvider{}, store).Execute(
		approval.WithSessionID(context.Background(), "sess"),
		mustMarshal(t, map[string]string{"subject": "Hello", "body": "Hi"}))
	if err == nil {
		t.Fatal("expected error for missing to")
	}
}

func TestSendTool_MissingSubject(t *testing.T) {
	store := approval.NewMemoryStore()
	_, err := email.NewSendTool(&mockSendProvider{}, store).Execute(
		approval.WithSessionID(context.Background(), "sess"),
		mustMarshal(t, map[string]string{"to": "alice@example.com", "body": "Hi"}))
	if err == nil {
		t.Fatal("expected error for missing subject")
	}
}

func TestSendTool_MissingBody(t *testing.T) {
	store := approval.NewMemoryStore()
	_, err := email.NewSendTool(&mockSendProvider{}, store).Execute(
		approval.WithSessionID(context.Background(), "sess"),
		mustMarshal(t, map[string]string{"to": "alice@example.com", "subject": "Hello"}))
	if err == nil {
		t.Fatal("expected error for missing body")
	}
}

func TestSendTool_ProviderError(t *testing.T) {
	store := approval.NewMemoryStore()
	p := &mockSendProvider{sendErr: errors.New("SMTP failure")}
	const sess = "sess-smtp-err"
	input := mustMarshal(t, map[string]string{
		"to": "alice@example.com", "subject": "Hello", "body": "Hi",
	})
	ctx := approval.WithSessionID(context.Background(), sess)

	r, _ := email.NewSendTool(p, store).Execute(ctx, input)
	store.Grant(sess, decodeMap(t, r)["action_id"].(string))

	_, err := email.NewSendTool(p, store).Execute(ctx, input)
	if err == nil {
		t.Fatal("expected provider error")
	}
}

func TestSendTool_InvalidJSON(t *testing.T) {
	store := approval.NewMemoryStore()
	_, err := email.NewSendTool(&mockSendProvider{}, store).Execute(
		approval.WithSessionID(context.Background(), "sess"), []byte(`{bad`))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}
