// Package builder implements the Builder Agent — an AI software engineer that
// guides the user through requirements → spec → task breakdown → code generation
// → review in a structured multi-turn workflow.
//
// # Phase model
//
// Each conversation session carries a builder.phase key in session metadata:
//
//	requirements → spec → tasks → codegen → review
//
// The LLM is told its current phase via the system prompt. When it wants to
// advance (or carry data forward), it embeds a JSON block in its response:
//
//	<builder_meta>{"builder.phase":"spec","builder.spec":"# Overview\n..."}</builder_meta>
//
// The agent strips this block before returning the visible reply and persists
// each key back into the session via SessionStore.SetMetadata.
package builder

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/marcoantonios1/Agent-OS/internal/costguard"
	"github.com/marcoantonios1/Agent-OS/internal/sessions"
	"github.com/marcoantonios1/Agent-OS/internal/tools"
	"github.com/marcoantonios1/Agent-OS/internal/tools/code"
	"github.com/marcoantonios1/Agent-OS/internal/types"
)

const agentID = types.AgentID("builder")

// Session metadata keys.
const (
	KeyPhase      = "builder.phase"
	KeySpec       = "builder.spec"
	KeyTasks      = "builder.tasks"
	KeyActiveTask = "builder.active_task"
)

// Phase values.
const (
	PhaseRequirements = "requirements"
	PhaseSpec         = "spec"
	PhaseTasks        = "tasks"
	PhaseCodegen      = "codegen"
	PhaseReview       = "review"
)

const metaOpen  = "<builder_meta>"
const metaClose = "</builder_meta>"

// ── system prompts ────────────────────────────────────────────────────────────

const basePrompt = `You are the Builder Agent for Agent OS — an expert AI software engineer.
You help users design and build software through a structured workflow.

## Workflow phases
1. requirements — gather context, ask clarifying questions
2. spec         — produce a structured markdown specification
3. tasks        — break the spec into a numbered implementation task list
4. codegen      — generate code for each task using file_write, validate with shell_run
5. review       — review the output, iterate or mark complete

## Phase transitions
When you are ready to advance to the next phase, OR when you need to store
structured data for later turns, embed a metadata block at the very end of
your response (after your visible reply):

<builder_meta>{"builder.phase":"<next_phase>","builder.spec":"...","builder.tasks":"..."}</builder_meta>

Only include keys that are changing. The block must be valid JSON.
Never show the <builder_meta> block to the user — it is stripped automatically.

## Rules
- Never skip phases. Always gather requirements before writing a spec.
- Always ask for user approval before advancing from spec → tasks and tasks → codegen.
- Use file_write + shell_run to generate and validate code.
- Keep responses concise and actionable.`

const requirementsPrompt = `
## Current phase: REQUIREMENTS
Your goal: understand what the user wants to build well enough to write a spec.

- Ask up to 5 targeted clarifying questions if the request is vague.
- Cover: target platform, key user flows, data model, integrations, constraints.
- When you have enough detail, write a brief summary of what you understood and
  ask the user to confirm before advancing.
- To advance to spec phase, end your response with:
  <builder_meta>{"builder.phase":"spec"}</builder_meta>`

const specPrompt = `
## Current phase: SPEC
Your goal: produce a structured specification document.

Write a markdown spec with these sections:
1. **Overview** — one paragraph summary
2. **Architecture** — tech stack, key components, data flow
3. **Data model** — main entities and relationships
4. **User flows** — numbered step-by-step flows for each feature
5. **Milestones** — 3-5 phased milestones
6. **Open questions** — anything still unclear

After presenting the spec, ask: "Does this look right? Reply 'yes' to continue to task breakdown."
When the user approves, advance by ending with:
<builder_meta>{"builder.phase":"tasks","builder.spec":"<full spec markdown>"}</builder_meta>`

const tasksPrompt = `
## Current phase: TASKS
Your goal: break the spec into a concrete, ordered implementation task list.

Produce a numbered list. Each task must have:
- A short title
- The file(s) it will create or modify
- A one-sentence description

Format as JSON array at the end so it can be parsed:
<builder_meta>{"builder.phase":"codegen","builder.tasks":"[{\"index\":0,\"title\":\"...\",\"files\":[\"...\"],\"description\":\"...\"}, ...]","builder.active_task":"0"}</builder_meta>

Before embedding the block, show the task list to the user in readable markdown
and ask: "Ready to start coding? Reply 'yes' to begin with task 1."`

const codegenPrompt = `
## Current phase: CODEGEN
Your goal: implement the active task using file_write and shell_run.

Steps for each task:
1. Write the file(s) with file_write.
2. Validate with shell_run (e.g. "go build ./..." or relevant lint/test command).
3. Report the result. Fix any errors before marking done.
4. When the task is complete, advance the active_task index.

When all tasks are done, transition to review:
<builder_meta>{"builder.phase":"review"}</builder_meta>

To move to the next task (index N):
<builder_meta>{"builder.active_task":"N"}</builder_meta>`

const reviewPrompt = `
## Current phase: REVIEW
Your goal: summarise what was built, highlight any remaining open items, and ask
whether the user wants to iterate or is satisfied.

If the user wants changes, reset the appropriate phase in your metadata block.`

// ── Agent ─────────────────────────────────────────────────────────────────────

// Agent implements the Builder Agent. It carries multi-turn state in session
// metadata and uses file/shell tools during code generation.
type Agent struct {
	loop     *tools.AgenticLoop
	sessions sessions.SessionStore
}

// New constructs a Builder Agent.
//
//   - llm is the LLM client (Costguard gateway).
//   - store is the session store — used to persist phase metadata across turns.
//   - cfg is the code tool configuration (sandbox directory, blocked commands, etc.).
func New(llm costguard.LLMClient, store sessions.SessionStore, cfg code.Config) *Agent {
	reg := tools.NewRegistry()
	reg.Register(code.NewReadTool(cfg))
	reg.Register(code.NewWriteTool(cfg))
	reg.Register(code.NewListTool(cfg))
	reg.Register(code.NewShellTool(cfg))

	return &Agent{
		loop: &tools.AgenticLoop{
			Client:   llm,
			Registry: reg,
		},
		sessions: store,
	}
}

// Handle processes a single user turn. It reads the current phase from session
// metadata, builds a phase-aware system prompt, runs the agentic loop, then
// parses and persists any metadata transitions the LLM produced.
func (a *Agent) Handle(ctx context.Context, req types.AgentRequest) (types.AgentResponse, error) {
	// Read up-to-date session metadata — req.Metadata may lag one turn behind
	// because the router loads the session before the agent saves its metadata.
	meta := req.Metadata
	if sess, err := a.sessions.Get(req.SessionID); err == nil {
		meta = sess.Metadata
	}

	phase := metaGet(meta, KeyPhase, PhaseRequirements)
	spec := metaGet(meta, KeySpec, "")
	tasks := metaGet(meta, KeyTasks, "")
	activeTask := metaGet(meta, KeyActiveTask, "0")

	prompt := buildSystemPrompt(phase, spec, tasks, activeTask)

	msgs := make([]types.ConversationTurn, 0, len(req.History)+1)
	msgs = append(msgs, types.ConversationTurn{Role: "system", Content: prompt})
	msgs = append(msgs, req.History...)

	raw, err := a.loop.Run(ctx, costguard.CompletionRequest{
		Model:     "claude-sonnet-4-6",
		Messages:  msgs,
		MaxTokens: 8192,
	})
	if err != nil {
		return types.AgentResponse{}, fmt.Errorf("builder agent: %w", err)
	}

	// Strip the metadata block from the visible output and persist each key.
	visible, newMeta := extractMeta(raw)
	for k, v := range newMeta {
		_ = a.sessions.SetMetadata(req.SessionID, k, v)
	}

	return types.AgentResponse{
		AgentID:  agentID,
		Output:   visible,
		Metadata: newMeta,
	}, nil
}

// ── helpers ───────────────────────────────────────────────────────────────────

func buildSystemPrompt(phase, spec, tasks, activeTask string) string {
	var sb strings.Builder
	sb.WriteString(basePrompt)

	switch phase {
	case PhaseSpec:
		sb.WriteString(specPrompt)
	case PhaseTasks:
		sb.WriteString(tasksPrompt)
		if spec != "" {
			sb.WriteString("\n\n## Spec to break down\n")
			sb.WriteString(spec)
		}
	case PhaseCodegen:
		sb.WriteString(codegenPrompt)
		if spec != "" {
			sb.WriteString("\n\n## Spec\n")
			sb.WriteString(spec)
		}
		if tasks != "" {
			sb.WriteString("\n\n## Task list\n")
			sb.WriteString(tasks)
		}
		sb.WriteString("\n\n## Active task index: ")
		sb.WriteString(activeTask)
	case PhaseReview:
		sb.WriteString(reviewPrompt)
	default:
		// requirements (default)
		sb.WriteString(requirementsPrompt)
	}

	return sb.String()
}

// extractMeta finds and removes the <builder_meta>{...}</builder_meta> block
// from raw LLM output. Returns the visible text and the parsed key/value map.
func extractMeta(raw string) (visible string, meta map[string]string) {
	start := strings.Index(raw, metaOpen)
	end := strings.Index(raw, metaClose)
	if start < 0 || end <= start {
		return raw, nil
	}

	jsonStr := raw[start+len(metaOpen) : end]
	after := raw[end+len(metaClose):]
	visible = strings.TrimSpace(raw[:start] + after)

	var parsed map[string]json.RawMessage
	if err := json.Unmarshal([]byte(jsonStr), &parsed); err != nil {
		// Malformed block — return raw output unchanged so nothing is lost.
		return raw, nil
	}

	meta = make(map[string]string, len(parsed))
	for k, v := range parsed {
		// Values are either JSON strings or other JSON (arrays, objects).
		// Try to unquote as a string first; fall back to raw JSON.
		var s string
		if err := json.Unmarshal(v, &s); err == nil {
			meta[k] = s
		} else {
			meta[k] = string(v)
		}
	}
	return visible, meta
}

// metaGet returns meta[key] or defaultVal if the key is absent or meta is nil.
func metaGet(meta map[string]string, key, defaultVal string) string {
	if meta == nil {
		return defaultVal
	}
	if v, ok := meta[key]; ok && v != "" {
		return v
	}
	return defaultVal
}
