# Agent configuration

Every agent — built-in or user-defined — lives in its own subdirectory under
`agents/`. The directory name is the agent's ID. Two files are required:

```
agents/
  <id>/
    agent.yaml   ← metadata, model, routing intents, skill list
    SYSTEM.md    ← full system prompt (plain Markdown)
```

No Go code is needed. The runtime discovers agents at startup, validates their
configuration, and wires them into the router automatically.

---

## Folder layout

```
agents/
  comms/            ← built-in: email, calendar, reminders, general chat
    agent.yaml
    SYSTEM.md
  builder/          ← built-in: code generation and software projects
    agent.yaml
    SYSTEM.md
  research/         ← built-in: web search and fact-finding
    agent.yaml
    SYSTEM.md
  reviewer/         ← built-in: sub-agent only, code review
    agent.yaml
    SYSTEM.md
  doctor/           ← user-defined, zero Go code required
    agent.yaml
    SYSTEM.md
  companion/        ← user-defined, zero Go code required
    agent.yaml
    SYSTEM.md
```

---

## `agent.yaml` schema

```yaml
# ── Required ──────────────────────────────────────────────────────────────────

# Unique identifier.  Must match the directory name.  Used in session metadata
# and sub-agent call routing.  Lowercase letters, digits, and hyphens only.
id: doctor

# Costguard model string for the main agentic loop.
model: medgemma


# ── Optional ──────────────────────────────────────────────────────────────────

# Maximum tokens the model may emit per response.  Defaults to 4096.
max_tokens: 4096

# Cheaper model used only for tool-call selection steps (Phase 5 optimisation).
# When omitted, `model` is used for all steps.
tool_call_model: gemma4:27b


# ── Intent routing ─────────────────────────────────────────────────────────────
#
# The classifier returns one or more intent strings.  When an intent matches
# an entry in this list the router dispatches the message to this agent.
# An agent with no `intents` block is never dispatched directly; it can still
# be reached via sub_agents calls from other agents.
intents:
  - doctor
  - medical
  - health
  - symptoms


# ── Skills ────────────────────────────────────────────────────────────────────
#
# Built-in skills (tools) registered for this agent.  The model can only call
# skills listed here — all others are invisible to it.
# Full descriptions in docs/skills.md.
skills:
  - web_search
  - web_fetch
  - user_profile_read
  - reminder_set
  - reminder_list


# ── Sub-agent access ──────────────────────────────────────────────────────────
#
# Other agent IDs this agent is allowed to call via SubAgentCaller.
# Set to [] (or omit the key) to disable sub-agent calls from this agent.
sub_agents:
  - research
```

---

## Field reference

### Required fields

| Field | Type | Description |
|---|---|---|
| `id` | string | Unique agent identifier. Must match the directory name. Lowercase letters, digits, and hyphens only (`[a-z0-9-]+`). |
| `model` | string | Costguard model string (e.g. `gemma4:26b`, `claude-sonnet-4-6`). Must be non-empty. |

### Optional fields

| Field | Type | Default | Description |
|---|---|---|---|
| `max_tokens` | int | `4096` | Maximum tokens the model may emit per response turn. |
| `tool_call_model` | string | same as `model` | Cheaper model used for tool-call selection steps. Reduces cost when a large model is used for reasoning but a smaller one suffices for tool dispatch. |
| `intents` | `[]string` | `[]` | Classifier intent strings that route to this agent. Empty means the agent is sub-agent-only. |
| `skills` | `[]string` | `[]` | Built-in skills visible to the model. Unknown skill names cause a startup validation error. See [docs/skills.md](skills.md). |
| `sub_agents` | `[]string` | `[]` | Agent IDs this agent may call via `SubAgentCaller.Call`. |

---

## `SYSTEM.md`

The entire file content becomes the agent's system prompt. Plain Markdown is
supported — headers, lists, code blocks, bold/italic. The LLM receives the
text as-is without any transformation.

```markdown
# Doctor Agent

You are a medical information assistant.  Your role is to…

## What you can do
- Answer general health and symptom questions
- Search for up-to-date medical information
- Set follow-up reminders for the user

## What you must NOT do
- Diagnose conditions or prescribe medication
- Replace advice from a qualified medical professional
```

Keep the system prompt focused.  The router prepends session context
(user profile, current date/time) automatically; you do not need to include
those.

---

## Validation rules

The following are checked at startup.  A misconfigured agent prevents the
server from starting.

| Rule | Error |
|---|---|
| `id` matches the directory name | `agent "doctor": id must match directory name "doctor"` |
| `id` is unique across all agents | `agent "doctor": id already registered` |
| `id` matches `[a-z0-9-]+` | `agent "doctor!": id contains invalid characters` |
| `model` is non-empty | `agent "doctor": model must not be empty` |
| Every skill in `skills` is registered | `agent "doctor": unknown skill "web_crawl"` |
| Every agent in `sub_agents` exists | `agent "doctor": sub_agent "unknown" is not registered` |
| `SYSTEM.md` is present and non-empty | `agent "doctor": SYSTEM.md is missing or empty` |
| `max_tokens` > 0 when set | `agent "doctor": max_tokens must be > 0` |

---

## Classifier intents

The classifier is an LLM call that maps the user's message to one or more
intent strings.  Built-in intents (`comms`, `builder`, `research`) are seeded
from the system prompt.  User-defined intents are **automatically added** to
the classifier's intent list when the runtime loads an agent whose `intents`
field is non-empty — no manual classifier changes are needed.

The classifier prompt is rebuilt at startup to include all registered intents
and their agent descriptions (derived from the first `#` heading in
`SYSTEM.md`).

---

## Sub-agent calls

An agent can delegate a sub-task to another agent without going through the
full router cycle:

```
sub_agents: [research]
```

Inside the agentic loop the model issues a tool call named `call_agent` with
arguments `{agent: "research", prompt: "…"}`.  The result is returned as a
tool-result turn; it does not appear in the session history visible to the
user.

Sub-agent calls are depth-limited to **1** (no transitive chains).

---

## Example: user-defined companion agent

**`agents/companion/agent.yaml`**

```yaml
id: companion
model: gemma4:27b
max_tokens: 2048

intents:
  - chat
  - companion
  - talk

skills:
  - user_profile_read
  - reminder_set

sub_agents: []
```

**`agents/companion/SYSTEM.md`**

```markdown
# Companion Agent

You are a friendly, supportive conversational companion.
Your tone is warm, empathetic, and never clinical.

Keep responses concise — two to four sentences unless asked for more.
Remember the user's name and preferences from their profile.
```
