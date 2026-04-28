// Package comms implements the Comms Agent — the personal assistant responsible
// for email and calendar tasks within Agent OS.
//
// The agent runs a multi-step agentic loop: it calls tools to read emails or
// calendar events, drafts replies, and surfaces approval prompts before any
// sensitive action (send email, create/update calendar event) is executed.
package comms

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/marcoantonios1/Agent-OS/internal/approval"
	"github.com/marcoantonios1/Agent-OS/internal/costguard"
	"github.com/marcoantonios1/Agent-OS/internal/sessions"
	"github.com/marcoantonios1/Agent-OS/internal/tools"
	"github.com/marcoantonios1/Agent-OS/internal/tools/calendar"
	"github.com/marcoantonios1/Agent-OS/internal/tools/email"
	"github.com/marcoantonios1/Agent-OS/internal/tools/reminder"
	"github.com/marcoantonios1/Agent-OS/internal/tools/userprofile"
	"github.com/marcoantonios1/Agent-OS/internal/types"
)

const agentID = types.AgentID("comms")

const systemPromptBase = `You are the Comms Agent for Agent OS — a personal AI assistant that manages email and calendar on behalf of the user.

## Tools available
- user_profile_read   — retrieve the user's persistent profile (name, preferences, contacts, communication style)
- user_profile_update — update the user's preferences, communication style, or add a recurring contact
- email_list          — list recent inbox emails (subject, sender, snippet)
- email_read          — read a full email by ID
- email_search        — search emails by keyword or operator (from:, subject:, etc.)
- email_draft         — compose and save a draft (does NOT send)
- email_send          — send an email (REQUIRES user approval — see rules below)
- calendar_list       — list events in a date range
- calendar_read       — read a single event by ID
- calendar_create     — create a new calendar event (REQUIRES user approval)
- calendar_update     — update an existing calendar event (REQUIRES user approval)
- reminder_set        — schedule a follow-up reminder to be sent at a future time
- reminder_cancel     — cancel a previously scheduled reminder
- reminder_list       — list all pending reminders for the user

## Rules you must always follow
1. ALWAYS draft before sending. When the user asks you to send an email, call email_draft
   first, then show the user the FULL draft (To, Subject, and Body) and ask:
   "Does this look right? Reply 'send' to send it, or tell me what to change."
   Only call email_send after the user explicitly confirms. If they request changes,
   call email_draft again with the revised content and show the new draft.
2. NEVER send email autonomously. When email_send returns {"status":"pending_approval",...},
   remind the user what you are about to send and ask them to reply "yes" or "send".
3. NEVER create or update calendar events autonomously. When calendar_create or
   calendar_update returns {"status":"pending_approval",...}, describe the action
   clearly and ask the user to confirm.
4. Use tools to answer questions — never fabricate email or calendar data.
5. email_draft is always safe to call; it saves a draft without sending.
6. When summarising emails, be concise: sender, subject, and a one-line summary per email.
7. TIMEZONE RULE — critical for calendar events: the current local date/time and UTC offset
   are provided in the ## Current time section below. ALL RFC3339 timestamps you generate
   for calendar_create and calendar_update MUST use that same UTC offset (e.g. +02:00),
   never Z (UTC) unless the offset shown is +00:00. Wrong timezone = event appears at the
   wrong time in the user's calendar.
8. PERSONALISATION — Call user_profile_read at the start of tasks that involve drafting
   emails or calendar invites. Apply the user's preferences: use their preferred sign-off,
   match their communication style, and address contacts by their stored name. When the
   user states a lasting preference ("always sign off as Marco", "my timezone is UTC+3"),
   call user_profile_update to persist it for future conversations.
9. REMINDERS — When the user asks to be reminded about something, call reminder_set with
   a clear message and a relative or absolute time ("in 30 minutes", "in 2 hours",
   "tomorrow at 9am"). Confirm the scheduled time back to the user. Use reminder_list
   to show pending reminders and reminder_cancel to remove one.
   CONTEXT-AWARE REMINDERS — When the reminder involves following up on a specific person,
   thread, or topic (e.g. "remind me to follow up with Alice about the invoice", "remind me
   to check on the project proposal"), ALWAYS set agent_prompt to an instruction that will
   surface live context at fire time. Write it as a self-contained Comms Agent task, e.g.:
   "The user needs to follow up with Alice about an invoice. Search Alice's recent emails
   for the invoice thread and summarise the latest message with any outstanding action."
   The agent_prompt runs through the full Comms Agent at fire time — it can search emails,
   check the calendar, and compose a summary. Only omit agent_prompt for simple time-based
   reminders with no lookup needed (e.g. "remind me to take my medication in 1 hour").

## Workflow patterns
- "Check my emails"           → email_list, then summarise each
- "Read that email"           → email_read with the correct ID
- "Send an email to Alice"    → user_profile_read → email_draft → show full draft → wait
                                → email_send (only after user says yes/send)
- "Draft a reply to Alice"    → email_read if needed → email_draft → show draft to user
- "Change the subject"        → email_draft with updated fields → show new draft → wait
- "Send it" / "Yes" / "Send" → email_send (user has already seen the draft)
- "What's on tomorrow?"       → calendar_list with tomorrow's date range
- "Schedule a meeting"        → calendar_create → show pending_approval → ask user to confirm
- "Always sign off as Marco"  → user_profile_update with preferences: {"sign_off": "Marco"}
- "Remind me to follow up with X about Y" → reminder_set with message and agent_prompt set to a
                                            self-contained search task, e.g. "User needs to follow
                                            up with X about Y. Search X's recent emails for Y and
                                            summarise the latest thread."`

// buildSystemPrompt returns the system prompt with optional user context and
// the current local date/time injected.
func buildSystemPrompt(profile *sessions.UserProfile) string {
	var sb strings.Builder
	sb.WriteString(systemPromptBase)

	if profile != nil {
		sb.WriteString("\n\n## User context")
		if profile.Name != "" {
			sb.WriteString("\nName: ")
			sb.WriteString(profile.Name)
		}
		if profile.CommunicationStyle != "" {
			sb.WriteString("\nCommunication style: ")
			sb.WriteString(profile.CommunicationStyle)
		}
		if len(profile.Preferences) > 0 {
			sb.WriteString("\nPreferences:")
			keys := make([]string, 0, len(profile.Preferences))
			for k := range profile.Preferences {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				sb.WriteString("\n  - ")
				sb.WriteString(k)
				sb.WriteString(": ")
				sb.WriteString(profile.Preferences[k])
			}
		}
		if len(profile.RecurringContacts) > 0 {
			sb.WriteString("\nRecurring contacts:")
			for _, c := range profile.RecurringContacts {
				sb.WriteString("\n  - ")
				sb.WriteString(c.Name)
				sb.WriteString(" (")
				sb.WriteString(c.Email)
				sb.WriteString(")")
				if c.Notes != "" {
					sb.WriteString(" — ")
					sb.WriteString(c.Notes)
				}
			}
		}
	}

	now := time.Now()
	sb.WriteString("\n\n## Current time\nLocal date/time (use this UTC offset for ALL calendar timestamps): ")
	sb.WriteString(now.Format(time.RFC3339))
	sb.WriteString("\nDay of week: ")
	sb.WriteString(now.Weekday().String())
	return sb.String()
}

// Agent implements the Comms Agent. It wires email and calendar tools into an
// agentic loop and handles user requests via the standard Agent interface.
type Agent struct {
	loop  *tools.AgenticLoop
	model string
}

// New constructs a Comms Agent.
//
//   - llm is the LLM client (Costguard gateway).
//   - emailProv is the email backend (Gmail, Outlook, or nil to omit email tools).
//   - calProv is the calendar backend (Google, Outlook, or nil to omit calendar tools).
//   - store is the approval store shared with the router.
//   - users is the persistent user profile store (always required).
//   - reminders is the reminder store (always required).
func New(
	llm costguard.LLMClient,
	emailProv email.EmailProvider,
	calProv calendar.CalendarProvider,
	store approval.Store,
	users sessions.UserStore,
	reminders sessions.ReminderStore,
	model string,
) *Agent {
	reg := tools.NewRegistry()

	reg.Register(userprofile.NewReadTool(users))
	reg.Register(userprofile.NewUpdateTool(users))

	reg.Register(reminder.NewSetTool(reminders))
	reg.Register(reminder.NewCancelTool(reminders))
	reg.Register(reminder.NewListTool(reminders))

	if emailProv != nil {
		reg.Register(email.NewListTool(emailProv))
		reg.Register(email.NewReadTool(emailProv))
		reg.Register(email.NewSearchTool(emailProv))
		reg.Register(email.NewDraftTool(emailProv))
		reg.Register(email.NewSendTool(emailProv, store))
	}

	if calProv != nil {
		reg.Register(calendar.NewListTool(calProv))
		reg.Register(calendar.NewReadTool(calProv))
		reg.Register(calendar.NewCreateTool(calProv, store))
		reg.Register(calendar.NewUpdateTool(calProv, store))
	}

	return &Agent{
		loop: &tools.AgenticLoop{
			Client:   llm,
			Registry: reg,
		},
		model: model,
	}
}

// HandleStream is the streaming variant of Handle. Tool-call steps use
// Complete(); the final text reply is streamed token-by-token via Stream().
func (a *Agent) HandleStream(ctx context.Context, req types.AgentRequest) (<-chan string, error) {
	slog.InfoContext(ctx, "agent_start_stream", "agent_id", string(agentID), "session_id", req.SessionID)

	var profile *sessions.UserProfile
	if raw := req.Metadata["user.profile"]; raw != "" {
		var p sessions.UserProfile
		if err := json.Unmarshal([]byte(raw), &p); err == nil {
			profile = &p
		}
	}

	msgs := make([]types.ConversationTurn, 0, len(req.History)+1)
	msgs = append(msgs, types.ConversationTurn{Role: "system", Content: buildSystemPrompt(profile)})
	msgs = append(msgs, req.History...)

	return a.loop.RunStream(ctx, costguard.CompletionRequest{
		Model:     a.model,
		Messages:  msgs,
		MaxTokens: 4096,
	})
}

// Handle processes a single user turn. It prepends the system prompt to the
// conversation history and runs the agentic loop until the LLM produces a
// final text response.
func (a *Agent) Handle(ctx context.Context, req types.AgentRequest) (types.AgentResponse, error) {
	slog.InfoContext(ctx, "agent_start",
		"agent_id", string(agentID),
		"session_id", req.SessionID,
	)
	start := time.Now()

	// Parse user profile injected by the router (may be absent).
	var profile *sessions.UserProfile
	if raw := req.Metadata["user.profile"]; raw != "" {
		var p sessions.UserProfile
		if err := json.Unmarshal([]byte(raw), &p); err == nil {
			profile = &p
		}
	}

	// Build the message list: system prompt followed by the full conversation
	// history (which already includes the current user message, added by the router).
	msgs := make([]types.ConversationTurn, 0, len(req.History)+1)
	msgs = append(msgs, types.ConversationTurn{
		Role:    "system",
		Content: buildSystemPrompt(profile),
	})
	msgs = append(msgs, req.History...)

	output, err := a.loop.Run(ctx, costguard.CompletionRequest{
		Model:     a.model,
		Messages:  msgs,
		MaxTokens: 4096,
	})
	if err != nil {
		return types.AgentResponse{}, fmt.Errorf("comms agent: %w", err)
	}

	slog.InfoContext(ctx, "agent_complete",
		"agent_id", string(agentID),
		"session_id", req.SessionID,
		"latency_ms", time.Since(start).Milliseconds(),
	)

	return types.AgentResponse{
		AgentID: agentID,
		Output:  output,
	}, nil
}
