package skills_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/marcoantonios1/Agent-OS/internal/approval"
	"github.com/marcoantonios1/Agent-OS/internal/costguard"
	"github.com/marcoantonios1/Agent-OS/internal/memory"
	"github.com/marcoantonios1/Agent-OS/internal/skills"
	"github.com/marcoantonios1/Agent-OS/internal/tools"
	"github.com/marcoantonios1/Agent-OS/internal/tools/calendar"
	"github.com/marcoantonios1/Agent-OS/internal/tools/code"
	"github.com/marcoantonios1/Agent-OS/internal/tools/email"
	"github.com/marcoantonios1/Agent-OS/internal/tools/websearch"
)

// ── provider stubs ────────────────────────────────────────────────────────────

// stubEmailProvider is a no-op implementation of email.EmailProvider.
type stubEmailProvider struct{}

func (s *stubEmailProvider) List(_ context.Context, _ int) ([]email.EmailSummary, error) {
	return nil, nil
}
func (s *stubEmailProvider) Read(_ context.Context, _ string) (*email.Email, error) {
	return nil, nil
}
func (s *stubEmailProvider) Search(_ context.Context, _ string) ([]email.EmailSummary, error) {
	return nil, nil
}
func (s *stubEmailProvider) Draft(_ context.Context, _, _, _ string) (*email.Draft, error) {
	return nil, nil
}
func (s *stubEmailProvider) Send(_ context.Context, _, _, _ string) error { return nil }

// stubCalendarProvider is a no-op implementation of calendar.CalendarProvider.
type stubCalendarProvider struct{}

func (s *stubCalendarProvider) List(_ context.Context, _, _ time.Time) ([]calendar.Event, error) {
	return nil, nil
}
func (s *stubCalendarProvider) Read(_ context.Context, _ string) (*calendar.Event, error) {
	return nil, nil
}
func (s *stubCalendarProvider) Create(_ context.Context, _ calendar.CreateEventInput) (*calendar.Event, error) {
	return nil, nil
}
func (s *stubCalendarProvider) Update(_ context.Context, _ calendar.UpdateEventInput) (*calendar.Event, error) {
	return nil, nil
}

// stubSearchProvider is a no-op implementation of websearch.SearchProvider.
type stubSearchProvider struct{}

func (s *stubSearchProvider) Search(_ context.Context, _ string, _ int) ([]websearch.SearchResult, error) {
	return nil, nil
}

// ── helpers ───────────────────────────────────────────────────────────────────

// defNames converts a Definitions slice into a name→bool lookup map.
func defNames(defs []costguard.ToolDefinition) map[string]bool {
	m := make(map[string]bool, len(defs))
	for _, d := range defs {
		m[d.Name] = true
	}
	return m
}

// buildRegistry constructs a NewGlobalRegistry with the provided (possibly nil)
// external providers and fresh in-memory stores.
func buildRegistry(
	ep email.EmailProvider,
	cp calendar.CalendarProvider,
	sp websearch.SearchProvider,
) *tools.ToolRegistry {
	return skills.NewGlobalRegistry(
		ep,
		cp,
		sp,
		approval.NewMemoryStore(),
		memory.NewUserStore(),
		memory.NewReminderStore(),
		memory.NewProjectStore(),
		memory.NewStore(),
		code.Config{SandboxDir: "/tmp"},
	)
}

// alwaysOnSkills is the set of tool names that must be present regardless of
// which providers are supplied.
var alwaysOnSkills = []string{
	"user_profile_read",
	"user_profile_update",
	"reminder_set",
	"reminder_cancel",
	"reminder_list",
	"project_list",
	"project_load",
	"file_read",
	"file_write",
	"file_list",
	"shell_run",
}

// ── tests ─────────────────────────────────────────────────────────────────────

func TestNewGlobalRegistry_AllProviders(t *testing.T) {
	reg := buildRegistry(&stubEmailProvider{}, &stubCalendarProvider{}, &stubSearchProvider{})
	got := defNames(reg.Definitions())

	// Always-on skills.
	for _, name := range alwaysOnSkills {
		if !got[name] {
			t.Errorf("expected always-on skill %q to be registered", name)
		}
	}

	// Email skills.
	for _, name := range []string{"email_list", "email_read", "email_search", "email_draft", "email_send"} {
		if !got[name] {
			t.Errorf("expected email skill %q to be registered when email provider is set", name)
		}
	}

	// Calendar skills.
	for _, name := range []string{"calendar_list", "calendar_read", "calendar_create", "calendar_update"} {
		if !got[name] {
			t.Errorf("expected calendar skill %q to be registered when calendar provider is set", name)
		}
	}

	// Web search / fetch.
	for _, name := range []string{"web_search", "web_fetch"} {
		if !got[name] {
			t.Errorf("expected web skill %q to be registered when search provider is set", name)
		}
	}
}

func TestNewGlobalRegistry_NilEmailProvider(t *testing.T) {
	reg := buildRegistry(nil, &stubCalendarProvider{}, &stubSearchProvider{})
	got := defNames(reg.Definitions())

	// Email tools must be absent.
	for _, name := range []string{"email_list", "email_read", "email_search", "email_draft", "email_send"} {
		if got[name] {
			t.Errorf("expected email skill %q to be absent when email provider is nil", name)
		}
	}

	// Always-on skills must still be present.
	for _, name := range alwaysOnSkills {
		if !got[name] {
			t.Errorf("expected always-on skill %q to be present even with nil email provider", name)
		}
	}

	// Calendar and web skills should still be registered.
	if !got["calendar_list"] {
		t.Error("expected calendar_list to be present")
	}
	if !got["web_search"] {
		t.Error("expected web_search to be present")
	}
}

func TestNewGlobalRegistry_NilCalendarProvider(t *testing.T) {
	reg := buildRegistry(&stubEmailProvider{}, nil, &stubSearchProvider{})
	got := defNames(reg.Definitions())

	// Calendar tools must be absent.
	for _, name := range []string{"calendar_list", "calendar_read", "calendar_create", "calendar_update"} {
		if got[name] {
			t.Errorf("expected calendar skill %q to be absent when calendar provider is nil", name)
		}
	}

	// Always-on skills must still be present.
	for _, name := range alwaysOnSkills {
		if !got[name] {
			t.Errorf("expected always-on skill %q to be present even with nil calendar provider", name)
		}
	}

	// Email and web skills should still be registered.
	if !got["email_list"] {
		t.Error("expected email_list to be present")
	}
	if !got["web_search"] {
		t.Error("expected web_search to be present")
	}
}

func TestNewGlobalRegistry_NilSearchProvider(t *testing.T) {
	reg := buildRegistry(&stubEmailProvider{}, &stubCalendarProvider{}, nil)
	got := defNames(reg.Definitions())

	// Web tools must be absent.
	for _, name := range []string{"web_search", "web_fetch"} {
		if got[name] {
			t.Errorf("expected web skill %q to be absent when search provider is nil", name)
		}
	}

	// Always-on skills must still be present.
	for _, name := range alwaysOnSkills {
		if !got[name] {
			t.Errorf("expected always-on skill %q to be present even with nil search provider", name)
		}
	}

	// Email and calendar skills should still be registered.
	if !got["email_list"] {
		t.Error("expected email_list to be present")
	}
	if !got["calendar_list"] {
		t.Error("expected calendar_list to be present")
	}
}

func TestNewGlobalRegistry_AllNilProviders(t *testing.T) {
	reg := buildRegistry(nil, nil, nil)
	got := defNames(reg.Definitions())

	// Provider-dependent tools must be absent.
	for _, name := range []string{
		"email_list", "email_read", "email_search", "email_draft", "email_send",
		"calendar_list", "calendar_read", "calendar_create", "calendar_update",
		"web_search", "web_fetch",
	} {
		if got[name] {
			t.Errorf("expected skill %q to be absent when all providers are nil", name)
		}
	}

	// Always-on skills must still be present.
	for _, name := range alwaysOnSkills {
		if !got[name] {
			t.Errorf("expected always-on skill %q to be present even when all providers are nil", name)
		}
	}
}

// ── TestMergeFrom ─────────────────────────────────────────────────────────────

// stubTool is a minimal tools.Tool for use in MergeFrom tests.
type stubTool struct{ name string }

func (s *stubTool) Definition() costguard.ToolDefinition {
	return costguard.ToolDefinition{Name: s.name, Description: "stub"}
}
func (s *stubTool) Execute(_ context.Context, _ json.RawMessage) (string, error) { return "", nil }

func TestMergeFrom(t *testing.T) {
	dst := tools.NewRegistry()
	dst.Register(&stubTool{name: "tool_a"})

	src := tools.NewRegistry()
	src.Register(&stubTool{name: "tool_b"})
	src.Register(&stubTool{name: "tool_c"})

	dst.MergeFrom(src)

	got := defNames(dst.Definitions())

	for _, name := range []string{"tool_a", "tool_b", "tool_c"} {
		if !got[name] {
			t.Errorf("expected %q to be present after MergeFrom", name)
		}
	}
}

func TestMergeFrom_NilOther(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(&stubTool{name: "tool_x"})

	// Should not panic.
	reg.MergeFrom(nil)

	got := defNames(reg.Definitions())
	if !got["tool_x"] {
		t.Error("existing tool should survive MergeFrom(nil)")
	}
	if len(got) != 1 {
		t.Errorf("expected exactly 1 tool after MergeFrom(nil), got %d", len(got))
	}
}

func TestMergeFrom_OverwritesExisting(t *testing.T) {
	dst := tools.NewRegistry()
	dst.Register(&stubTool{name: "tool_shared"})

	src := tools.NewRegistry()
	src.Register(&stubTool{name: "tool_shared"})

	dst.MergeFrom(src)

	got := defNames(dst.Definitions())
	if !got["tool_shared"] {
		t.Error("expected tool_shared to be present after MergeFrom")
	}
	if len(got) != 1 {
		t.Errorf("expected exactly 1 tool after merging same name, got %d", len(got))
	}
}
