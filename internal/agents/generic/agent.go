// Package generic implements GenericAgent — a configurable agent loaded from
// an agent.yaml + SYSTEM.md pair on disk. It delegates to a subset of the
// global ToolRegistry and optionally dispatches sub-tasks via call_agent.
package generic

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/marcoantonios1/Agent-OS/internal/costguard"
	"github.com/marcoantonios1/Agent-OS/internal/sessions"
	"github.com/marcoantonios1/Agent-OS/internal/tools"
	"github.com/marcoantonios1/Agent-OS/internal/types"
)

// Config holds the parsed configuration for a generic agent.
// SystemPrompt is loaded from SYSTEM.md; all other fields come from agent.yaml.
type Config struct {
	ID        string   `yaml:"id"`
	Model     string   `yaml:"model"`
	// ToolCallModel is an optional cheaper model for intermediate tool-call steps.
	// When set, the agentic loop uses this model for file/shell/search decisions and
	// reserves Model for the final synthesis step. Omit to use Model throughout.
	ToolCallModel string   `yaml:"tool_call_model"`
	MaxTokens     int      `yaml:"max_tokens"`
	Intents       []string `yaml:"intents"`
	Skills        []string `yaml:"skills"`
	SubAgents     []string `yaml:"sub_agents"`
	// SystemPrompt is populated by the loader from SYSTEM.md, not agent.yaml.
	SystemPrompt string `yaml:"-"`
}

// Agent is a general-purpose agent whose skills and system prompt are driven
// entirely by its on-disk configuration.
type Agent struct {
	cfg       Config
	llm       costguard.LLMClient
	globalReg *tools.ToolRegistry
}

// New constructs a generic Agent. globalRegistry is the full tool registry;
// it is subset to cfg.Skills on each Handle call.
func New(cfg Config, globalRegistry *tools.ToolRegistry, llm costguard.LLMClient) (*Agent, error) {
	if cfg.ID == "" {
		return nil, fmt.Errorf("generic agent: id must not be empty")
	}
	if cfg.Model == "" {
		return nil, fmt.Errorf("generic agent %q: model must not be empty", cfg.ID)
	}
	if cfg.SystemPrompt == "" {
		return nil, fmt.Errorf("generic agent %q: system prompt must not be empty (missing SYSTEM.md?)", cfg.ID)
	}
	return &Agent{cfg: cfg, llm: llm, globalReg: globalRegistry}, nil
}

// Handle processes a single user turn. It builds a per-call ToolRegistry from
// the globally registered skills, optionally adds call_agent, then runs the
// agentic loop. Follows the comms-agent message pattern: system + history
// (history already includes the current user turn, added by the router).
func (a *Agent) Handle(ctx context.Context, req types.AgentRequest) (types.AgentResponse, error) {
	slog.InfoContext(ctx, "generic_agent_start", "agent_id", a.cfg.ID, "session_id", req.SessionID)

	loop := a.buildLoop(ctx, req.SubCaller)
	msgs := a.buildMessages(req)

	output, err := loop.Run(ctx, costguard.CompletionRequest{
		Model:         a.cfg.Model,
		ToolCallModel: a.cfg.ToolCallModel,
		Messages:      msgs,
		MaxTokens:     a.maxTokens(),
	})
	if err != nil {
		return types.AgentResponse{}, fmt.Errorf("generic agent %q: %w", a.cfg.ID, err)
	}
	return types.AgentResponse{
		AgentID: types.AgentID(a.cfg.ID),
		Output:  output,
	}, nil
}

// HandleStream is the streaming variant of Handle.
func (a *Agent) HandleStream(ctx context.Context, req types.AgentRequest) (<-chan string, error) {
	loop := a.buildLoop(ctx, req.SubCaller)
	msgs := a.buildMessages(req)

	return loop.RunStream(ctx, costguard.CompletionRequest{
		Model:         a.cfg.Model,
		ToolCallModel: a.cfg.ToolCallModel,
		Messages:      msgs,
		MaxTokens:     a.maxTokens(),
	})
}

func (a *Agent) buildLoop(ctx context.Context, caller types.SubAgentCaller) *tools.AgenticLoop {
	reg, missing := a.globalReg.Subset(a.cfg.Skills)
	if len(missing) > 0 {
		slog.WarnContext(ctx, "generic agent: unknown skills", "agent_id", a.cfg.ID, "skills", missing)
	}
	if len(a.cfg.SubAgents) > 0 && caller != nil {
		reg.Register(newCallAgentTool(caller, a.cfg.SubAgents))
	}
	return &tools.AgenticLoop{Client: a.llm, Registry: reg}
}

func (a *Agent) buildMessages(req types.AgentRequest) []types.ConversationTurn {
	msgs := make([]types.ConversationTurn, 0, len(req.History)+1)
	msgs = append(msgs, types.ConversationTurn{Role: "system", Content: a.buildDynamicPrompt(req)})
	msgs = append(msgs, req.History...)
	return msgs
}

// buildDynamicPrompt appends two context blocks to the static SYSTEM.md content
// at call time so every generic agent has accurate situational awareness:
//
//  1. ## User context — user name, style, preferences, contacts (when the router
//     has injected a profile under req.Metadata["user.profile"]).
//  2. ## Current time — local RFC3339 timestamp and day of week, giving agents
//     that work with calendar or time-sensitive data the correct timezone offset.
func (a *Agent) buildDynamicPrompt(req types.AgentRequest) string {
	var sb strings.Builder
	sb.WriteString(a.cfg.SystemPrompt)

	if raw := req.Metadata["user.profile"]; raw != "" {
		var p sessions.UserProfile
		if err := json.Unmarshal([]byte(raw), &p); err == nil {
			sb.WriteString("\n\n## User context")
			if p.Name != "" {
				sb.WriteString("\nName: ")
				sb.WriteString(p.Name)
			}
			if p.CommunicationStyle != "" {
				sb.WriteString("\nCommunication style: ")
				sb.WriteString(p.CommunicationStyle)
			}
			if len(p.Preferences) > 0 {
				sb.WriteString("\nPreferences:")
				keys := make([]string, 0, len(p.Preferences))
				for k := range p.Preferences {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				for _, k := range keys {
					sb.WriteString("\n  - ")
					sb.WriteString(k)
					sb.WriteString(": ")
					sb.WriteString(p.Preferences[k])
				}
			}
			if len(p.RecurringContacts) > 0 {
				sb.WriteString("\nRecurring contacts:")
				for _, c := range p.RecurringContacts {
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
	}

	if raw := req.Metadata["user.personality"]; raw != "" {
		var p sessions.PersonalityProfile
		if err := json.Unmarshal([]byte(raw), &p); err == nil {
			sb.WriteString(sessions.FormatPersonalityContext(&p))
		}
	}

	if block := req.Metadata["user.episodic_memories"]; block != "" {
		sb.WriteString(block)
	}

	now := time.Now()
	sb.WriteString("\n\n## Current time\nLocal date/time (use this UTC offset for ALL calendar timestamps): ")
	sb.WriteString(now.Format(time.RFC3339))
	sb.WriteString("\nDay of week: ")
	sb.WriteString(now.Weekday().String())
	return sb.String()
}

func (a *Agent) maxTokens() int {
	if a.cfg.MaxTokens > 0 {
		return a.cfg.MaxTokens
	}
	return 4096
}

// ── call_agent tool ───────────────────────────────────────────────────────────

type callAgentTool struct {
	caller  types.SubAgentCaller
	allowed map[string]bool
}

type callAgentInput struct {
	AgentID string `json:"agent_id"`
	Prompt  string `json:"prompt"`
}

func newCallAgentTool(caller types.SubAgentCaller, subAgents []string) *callAgentTool {
	allowed := make(map[string]bool, len(subAgents))
	for _, id := range subAgents {
		allowed[id] = true
	}
	return &callAgentTool{caller: caller, allowed: allowed}
}

func (t *callAgentTool) Definition() costguard.ToolDefinition {
	return costguard.ToolDefinition{
		Name:        "call_agent",
		Description: "Delegate a sub-task to another agent and return its response.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"agent_id": map[string]any{
					"type":        "string",
					"description": "The ID of the agent to delegate to.",
				},
				"prompt": map[string]any{
					"type":        "string",
					"description": "The task or question for the sub-agent.",
				},
			},
			"required": []string{"agent_id", "prompt"},
		},
	}
}

func (t *callAgentTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var in callAgentInput
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("call_agent: invalid input: %w", err)
	}
	if in.AgentID == "" {
		return "", fmt.Errorf("call_agent: agent_id must not be empty")
	}
	if in.Prompt == "" {
		return "", fmt.Errorf("call_agent: prompt must not be empty")
	}
	if !t.allowed[in.AgentID] {
		return "", fmt.Errorf("call_agent: agent %q is not in the allowed sub-agents list", in.AgentID)
	}
	result, err := t.caller.Call(ctx, in.AgentID, in.Prompt)
	if err != nil {
		return "", fmt.Errorf("call_agent %q: %w", in.AgentID, err)
	}
	return result, nil
}
