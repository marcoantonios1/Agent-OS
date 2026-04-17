package userprofile

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/marcoantonios1/Agent-OS/internal/memory"
	"github.com/marcoantonios1/Agent-OS/internal/sessions"
)

// ctxWithUser returns a context carrying the given user ID, as the router does.
func ctxWithUser(userID string) context.Context {
	return sessions.WithUserID(context.Background(), userID)
}

// ── ReadTool ──────────────────────────────────────────────────────────────────

func TestReadTool_ReturnsProfile(t *testing.T) {
	store := memory.NewUserStore()
	_ = store.SaveUser(&sessions.UserProfile{
		UserID:             "u1",
		Name:               "Alice",
		CommunicationStyle: "formal",
		Preferences:        map[string]string{"sign_off": "Best"},
	})

	tool := NewReadTool(store)
	out, err := tool.Execute(ctxWithUser("u1"), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var p sessions.UserProfile
	if err := json.Unmarshal([]byte(out), &p); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if p.Name != "Alice" {
		t.Errorf("Name: got %q", p.Name)
	}
	if p.Preferences["sign_off"] != "Best" {
		t.Errorf("Preferences[sign_off]: got %q", p.Preferences["sign_off"])
	}
}

func TestReadTool_UserNotFound_ReturnsEmptyProfile(t *testing.T) {
	store := memory.NewUserStore()
	tool := NewReadTool(store)

	out, err := tool.Execute(ctxWithUser("unknown"), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	// Should return a valid (empty) profile, not an error.
	var p sessions.UserProfile
	if jsonErr := json.Unmarshal([]byte(out), &p); jsonErr != nil {
		t.Fatalf("expected valid JSON for missing user, got: %s", out)
	}
	if p.UserID != "unknown" {
		t.Errorf("expected UserID 'unknown' in empty profile, got %q", p.UserID)
	}
}

func TestReadTool_NoUserIDInContext_ReturnsErrorJSON(t *testing.T) {
	store := memory.NewUserStore()
	tool := NewReadTool(store)

	out, err := tool.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if out == "" {
		t.Fatal("expected non-empty output")
	}
	// Should contain an error field, not panic.
	var m map[string]string
	_ = json.Unmarshal([]byte(out), &m)
	if m["error"] == "" {
		t.Errorf("expected error key in JSON, got: %s", out)
	}
}

func TestReadTool_Definition(t *testing.T) {
	store := memory.NewUserStore()
	tool := NewReadTool(store)
	def := tool.Definition()
	if def.Name != "user_profile_read" {
		t.Errorf("Name: got %q", def.Name)
	}
	if def.Description == "" {
		t.Error("Description should not be empty")
	}
}

// ── UpdateTool ────────────────────────────────────────────────────────────────

func TestUpdateTool_CreatesProfileOnFirstUpdate(t *testing.T) {
	store := memory.NewUserStore()
	tool := NewUpdateTool(store)

	input := `{"name":"Marco","communication_style":"concise","preferences":{"tone":"casual"}}`
	_, err := tool.Execute(ctxWithUser("u2"), json.RawMessage(input))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	p, err := store.GetUser("u2")
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if p.Name != "Marco" {
		t.Errorf("Name: got %q", p.Name)
	}
	if p.CommunicationStyle != "concise" {
		t.Errorf("CommunicationStyle: got %q", p.CommunicationStyle)
	}
	if p.Preferences["tone"] != "casual" {
		t.Errorf("Preferences[tone]: got %q", p.Preferences["tone"])
	}
}

func TestUpdateTool_MergesPreferences(t *testing.T) {
	store := memory.NewUserStore()
	_ = store.SaveUser(&sessions.UserProfile{
		UserID:      "u3",
		Preferences: map[string]string{"tone": "formal", "language": "en"},
	})

	tool := NewUpdateTool(store)
	_, err := tool.Execute(ctxWithUser("u3"), json.RawMessage(`{"preferences":{"tone":"casual","sign_off":"Cheers"}}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	p, _ := store.GetUser("u3")
	// "tone" overwritten, "language" preserved, "sign_off" added.
	if p.Preferences["tone"] != "casual" {
		t.Errorf("tone: got %q", p.Preferences["tone"])
	}
	if p.Preferences["language"] != "en" {
		t.Errorf("language should be preserved: got %q", p.Preferences["language"])
	}
	if p.Preferences["sign_off"] != "Cheers" {
		t.Errorf("sign_off: got %q", p.Preferences["sign_off"])
	}
}

func TestUpdateTool_AddContact(t *testing.T) {
	store := memory.NewUserStore()
	tool := NewUpdateTool(store)

	input := `{"add_contact":{"name":"Bob","email":"bob@example.com","notes":"colleague"}}`
	_, err := tool.Execute(ctxWithUser("u4"), json.RawMessage(input))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	p, _ := store.GetUser("u4")
	if len(p.RecurringContacts) != 1 {
		t.Fatalf("expected 1 contact, got %d", len(p.RecurringContacts))
	}
	c := p.RecurringContacts[0]
	if c.Name != "Bob" || c.Email != "bob@example.com" || c.Notes != "colleague" {
		t.Errorf("contact: %+v", c)
	}
}

func TestUpdateTool_AddContact_Appends(t *testing.T) {
	store := memory.NewUserStore()
	_ = store.SaveUser(&sessions.UserProfile{
		UserID:            "u5",
		RecurringContacts: []sessions.Contact{{Name: "Alice", Email: "alice@example.com"}},
	})
	tool := NewUpdateTool(store)

	_, _ = tool.Execute(ctxWithUser("u5"), json.RawMessage(`{"add_contact":{"name":"Bob","email":"bob@example.com"}}`))

	p, _ := store.GetUser("u5")
	if len(p.RecurringContacts) != 2 {
		t.Errorf("expected 2 contacts, got %d", len(p.RecurringContacts))
	}
}

func TestUpdateTool_NoUserIDInContext(t *testing.T) {
	store := memory.NewUserStore()
	tool := NewUpdateTool(store)

	out, err := tool.Execute(context.Background(), json.RawMessage(`{"name":"test"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var m map[string]string
	_ = json.Unmarshal([]byte(out), &m)
	if m["error"] == "" {
		t.Errorf("expected error key in JSON, got: %s", out)
	}
}

func TestUpdateTool_ReturnsOKStatus(t *testing.T) {
	store := memory.NewUserStore()
	tool := NewUpdateTool(store)

	out, err := tool.Execute(ctxWithUser("u6"), json.RawMessage(`{"name":"Test"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var m map[string]string
	if jsonErr := json.Unmarshal([]byte(out), &m); jsonErr != nil {
		t.Fatalf("unmarshal: %v, raw: %s", jsonErr, out)
	}
	if m["status"] != "ok" {
		t.Errorf("status: got %q", m["status"])
	}
}

func TestUpdateTool_Definition(t *testing.T) {
	store := memory.NewUserStore()
	tool := NewUpdateTool(store)
	def := tool.Definition()
	if def.Name != "user_profile_update" {
		t.Errorf("Name: got %q", def.Name)
	}
}

// ── Context helpers ───────────────────────────────────────────────────────────

func TestContextHelpers_RoundTrip(t *testing.T) {
	ctx := sessions.WithUserID(context.Background(), "user123")
	if got := sessions.UserIDFromContext(ctx); got != "user123" {
		t.Errorf("got %q, want %q", got, "user123")
	}
}

func TestContextHelpers_MissingReturnsEmpty(t *testing.T) {
	if got := sessions.UserIDFromContext(context.Background()); got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
}

// ErrUserNotFound sentinel is exported and wrappable.
func TestErrUserNotFound_IsCheckable(t *testing.T) {
	store := memory.NewUserStore()
	_, err := store.GetUser("nobody")
	if !errors.Is(err, sessions.ErrUserNotFound) {
		t.Errorf("expected errors.Is(err, ErrUserNotFound), got: %v", err)
	}
}
