// Package skills provides NewGlobalRegistry, which wires every built-in tool
// into a single ToolRegistry ready for injection into agents.
// Provider arguments are nil-safe: tools that depend on an absent provider are
// simply not registered.
package skills

import (
	"github.com/marcoantonios1/Agent-OS/internal/approval"
	"github.com/marcoantonios1/Agent-OS/internal/sessions"
	"github.com/marcoantonios1/Agent-OS/internal/tools"
	"github.com/marcoantonios1/Agent-OS/internal/tools/calendar"
	"github.com/marcoantonios1/Agent-OS/internal/tools/code"
	"github.com/marcoantonios1/Agent-OS/internal/tools/email"
	"github.com/marcoantonios1/Agent-OS/internal/tools/project"
	"github.com/marcoantonios1/Agent-OS/internal/tools/reminder"
	"github.com/marcoantonios1/Agent-OS/internal/tools/userprofile"
	"github.com/marcoantonios1/Agent-OS/internal/tools/websearch"
)

// NewGlobalRegistry creates a ToolRegistry populated with every built-in skill.
//
// Always-registered skills (require only the store arguments):
//   - user_profile_read, user_profile_update
//   - reminder_set, reminder_cancel, reminder_list
//   - project_list, project_load
//   - file_read, file_write, file_list, shell_run
//
// Conditionally registered (only when the provider is non-nil):
//   - email_list, email_read, email_search, email_draft, email_send (emailProv)
//   - calendar_list, calendar_read, calendar_create, calendar_update (calProv)
//   - web_search, web_fetch (searchProv)
func NewGlobalRegistry(
	emailProv email.EmailProvider,
	calProv calendar.CalendarProvider,
	searchProv websearch.SearchProvider,
	approvals approval.Store,
	users sessions.UserStore,
	reminders sessions.ReminderStore,
	projects sessions.ProjectStore,
	sessionStore sessions.SessionStore,
	sandboxCfg code.Config,
) *tools.ToolRegistry {
	reg := tools.NewRegistry()

	// ── always-on: user profile ───────────────────────────────────────────────
	reg.Register(userprofile.NewReadTool(users))
	reg.Register(userprofile.NewUpdateTool(users))

	// ── always-on: reminders ──────────────────────────────────────────────────
	reg.Register(reminder.NewSetTool(reminders))
	reg.Register(reminder.NewCancelTool(reminders))
	reg.Register(reminder.NewListTool(reminders))

	// ── always-on: projects ───────────────────────────────────────────────────
	reg.Register(project.NewListTool(projects, sessionStore))
	reg.Register(project.NewLoadTool(projects, sessionStore))

	// ── always-on: file / shell ───────────────────────────────────────────────
	reg.Register(code.NewReadTool(sandboxCfg))
	reg.Register(code.NewWriteTool(sandboxCfg))
	reg.Register(code.NewListTool(sandboxCfg))
	reg.Register(code.NewShellTool(sandboxCfg))

	// ── conditional: email ────────────────────────────────────────────────────
	if emailProv != nil {
		reg.Register(email.NewListTool(emailProv))
		reg.Register(email.NewReadTool(emailProv))
		reg.Register(email.NewSearchTool(emailProv))
		reg.Register(email.NewDraftTool(emailProv))
		reg.Register(email.NewSendTool(emailProv, approvals))
	}

	// ── conditional: calendar ─────────────────────────────────────────────────
	if calProv != nil {
		reg.Register(calendar.NewListTool(calProv))
		reg.Register(calendar.NewReadTool(calProv))
		reg.Register(calendar.NewCreateTool(calProv, approvals))
		reg.Register(calendar.NewUpdateTool(calProv, approvals))
	}

	// ── conditional: web search / fetch ───────────────────────────────────────
	if searchProv != nil {
		searchReg := websearch.NewWebSearchRegistry(searchProv)
		reg.MergeFrom(searchReg)
	}

	return reg
}
