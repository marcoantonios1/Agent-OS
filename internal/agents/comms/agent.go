// Package comms implements the Comms Agent — the personal assistant responsible
// for email and calendar tasks within Agent OS.
//
// The agent runs a multi-step agentic loop: it calls tools to read emails or
// calendar events, drafts replies, and surfaces approval prompts before any
// sensitive action (send email, create/update calendar event) is executed.
package comms

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/marcoantonios1/Agent-OS/internal/approval"
	"github.com/marcoantonios1/Agent-OS/internal/costguard"
	"github.com/marcoantonios1/Agent-OS/internal/tools"
	"github.com/marcoantonios1/Agent-OS/internal/tools/calendar"
	"github.com/marcoantonios1/Agent-OS/internal/tools/email"
	"github.com/marcoantonios1/Agent-OS/internal/types"
)

const agentID = types.AgentID("comms")

const systemPrompt = `You are the Comms Agent for Agent OS — a personal AI assistant that manages email and calendar on behalf of the user.

## Tools available
- email_list      — list recent inbox emails (subject, sender, snippet)
- email_read      — read a full email by ID
- email_search    — search emails by keyword or operator (from:, subject:, etc.)
- email_draft     — compose and save a draft (does NOT send)
- email_send      — send an email (REQUIRES user approval — see rules below)
- calendar_list   — list events in a date range
- calendar_read   — read a single event by ID
- calendar_create — create a new calendar event (REQUIRES user approval)
- calendar_update — update an existing calendar event (REQUIRES user approval)

## Rules you must always follow
1. NEVER send email autonomously. When email_send returns {"status":"pending_approval",...},
   show the user the email details and ask them to reply "confirm" or "yes" to proceed.
2. NEVER create or update calendar events autonomously. When calendar_create or
   calendar_update returns {"status":"pending_approval",...}, describe the action
   clearly and ask the user to confirm.
3. Use tools to answer questions — never fabricate email or calendar data.
4. email_draft is always safe to call; it saves a draft without sending.
5. When summarising emails, be concise: sender, subject, and a one-line summary per email.
6. Always use today's date context when interpreting relative times like "tomorrow".

## Workflow patterns
- "Check my emails"           → email_list, then summarise each
- "Read that email"           → email_read with the correct ID
- "Draft a reply to Alice"    → email_read if needed, then email_draft, show draft to user
- "Send the email"            → email_send → show pending_approval → ask user to confirm
- "What's on tomorrow?"       → calendar_list with tomorrow's date range
- "Schedule a meeting"        → calendar_create → show pending_approval → ask user to confirm`

// Agent implements the Comms Agent. It wires email and calendar tools into an
// agentic loop and handles user requests via the standard Agent interface.
type Agent struct {
	loop *tools.AgenticLoop
}

// New constructs a Comms Agent.
//
//   - llm is the LLM client (Costguard gateway).
//   - emailProv is the email backend (Gmail, Outlook, or nil to omit email tools).
//   - calProv is the calendar backend (Google, Outlook, or nil to omit calendar tools).
//   - store is the approval store shared with the router.
func New(
	llm costguard.LLMClient,
	emailProv email.EmailProvider,
	calProv calendar.CalendarProvider,
	store approval.Store,
) *Agent {
	reg := tools.NewRegistry()

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
	}
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

	// Build the message list: system prompt followed by the full conversation
	// history (which already includes the current user message, added by the router).
	msgs := make([]types.ConversationTurn, 0, len(req.History)+1)
	msgs = append(msgs, types.ConversationTurn{
		Role:    "system",
		Content: systemPrompt,
	})
	msgs = append(msgs, req.History...)

	output, err := a.loop.Run(ctx, costguard.CompletionRequest{
		Model:     "llama3.2",
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
