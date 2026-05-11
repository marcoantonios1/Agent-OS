# Adding a new agent

Agent OS uses a file-based agent loader. Adding a new agent requires no Go code — create a folder under `agents/` with two files and restart the server.

## Quickstart

```
agents/
  my-agent/
    agent.yaml   ← id, model, intents, skills
    SYSTEM.md    ← the agent's system prompt
```

That's it. On startup, `generic.LoadAll` scans the `agents/` directory, loads every valid folder, and registers each agent's declared intents with the router.

---

## Step 1 — Create the folder

```bash
mkdir agents/my-agent
```

The folder name is only used for logging. The agent's public identity comes from the `id` field in `agent.yaml`.

---

## Step 2 — Write `agent.yaml`

```yaml
id: my-agent          # unique identifier, lowercase, hyphens ok
model: gemma4:26b     # any model string Costguard accepts
max_tokens: 4096      # optional, defaults to 4096
intents:              # one or more routing keywords (see below)
  - my-agent
  - my-keyword
skills:               # subset of built-in skills the agent may use
  - web_search
  - web_fetch
```

### `intents`

The router matches incoming messages to agents by intent. The classifier LLM returns one or more intent strings; the router looks them up in the agents map.

- At least one intent must match a keyword the classifier knows about (see [Updating the classifier](#updating-the-classifier) below).
- An agent can declare multiple intents — all of them point to the same agent instance.
- Two agents must not declare the same intent; the last one loaded wins.

### `skills`

Skills are the tools the agent can call. The full list of available skills is documented in [docs/skills.md](skills.md). Declare only the skills your agent actually needs — the agent's registry is a subset of the global registry, so undeclared skills are not visible to the LLM.

| Skill | What it does |
|---|---|
| `web_search` | Search the web via Brave Search API |
| `web_fetch` | Fetch and read a URL |
| `email_list` | List inbox emails |
| `email_read` | Read a full email by ID |
| `email_search` | Search emails by keyword |
| `email_draft` | Compose a draft (does not send) |
| `email_send` | Send an email (requires user approval) |
| `calendar_list` | List calendar events in a date range |
| `calendar_read` | Read a single event by ID |
| `calendar_create` | Create a calendar event (requires user approval) |
| `calendar_update` | Update a calendar event (requires user approval) |
| `reminder_set` | Schedule a reminder |
| `reminder_cancel` | Cancel a reminder |
| `reminder_list` | List pending reminders |
| `user_profile_read` | Read the user's persistent profile |
| `user_profile_update` | Update the user's profile |
| `file_read` | Read a file from the Builder sandbox |
| `file_write` | Write a file to the Builder sandbox |
| `file_list` | List files in the Builder sandbox |
| `shell_run` | Run a shell command in the Builder sandbox |
| `project_list` | List Builder projects |
| `project_load` | Load a Builder project into session |

---

## Step 3 — Write `SYSTEM.md`

`SYSTEM.md` is the agent's static system prompt. Write it in plain markdown.

Two sections are automatically appended at runtime — you do not need to include them:

- `## User context` — the user's name, communication style, preferences, and recurring contacts (injected when a user profile exists).
- `## Current time` — the local date, time, and UTC offset (always injected).

A minimal example:

```markdown
You are a helpful assistant specialised in X.

## Rules
- Always do Y
- Never do Z

## How to work
1. Call web_search first — never answer factual questions from memory alone.
2. Synthesise results into a clear, structured response.
```

Tips:
- Be explicit about what tools the agent should call and when.
- Include workflow patterns ("When the user asks X → do Y") for predictable behaviour.
- Keep the prompt focused — a narrow, well-specified agent outperforms a vague general one.

---

## Step 3b — Write `SOUL.md` (optional)

`SOUL.md` sits alongside `SYSTEM.md` and is appended to the system prompt after it. It is purely optional — omit it and the agent behaves exactly as the `SYSTEM.md` dictates.

**Use it to define character and tone separately from capability.** The idea is that `SYSTEM.md` answers _what the agent does and how_, while `SOUL.md` answers _who the agent is_ — its voice, rhythm, personality, and the things it avoids. Separating the two makes both easier to edit and reason about.

```
agents/my-agent/
  agent.yaml
  SYSTEM.md    ← what the agent does, which tools it calls, workflow rules
  SOUL.md      ← tone, character, how it sounds — optional
```

A minimal example:

```markdown
You speak directly and avoid corporate phrasing.
Keep responses short unless depth is asked for.
Never use phrases like "Certainly!" or "Of course!".
```

A more detailed example (see `agents/companion/SOUL.md` for the full version):

```markdown
intelligence:
  - sharp but never performative
  - thinks in consequences and tradeoffs

tone:
  - grounded, subtly philosophical
  - dry humour, low frequency, high accuracy
  - never becomes a motivational speaker

communication_rules:
  - avoid fake positivity
  - avoid therapy language unless necessary
  - brevity is acceptable — not every response needs to be long
```

`SOUL.md` content is not validated — write it however reads naturally to the model you are using. Plain instructions, YAML, bullet lists, or prose all work. The file is simply appended as text.

**When to add one:**
- Your agent needs a distinctive voice that is consistent across many different topics
- You want to tune tone (e.g. dry vs warm) without touching the capability instructions
- You are building a companion or persona-style agent where character matters

**When to skip it:**
- Task-only agents (finance, research, notes) rarely need one — a neutral professional tone from `SYSTEM.md` is fine

---

## Step 4 — Update the classifier

The classifier LLM only routes to intents it knows about. Open `internal/router/classifier.go` and add an entry for your new intent in the `systemPrompt` constant:

```go
- "my-agent" – One-sentence description of when to route here.
               Examples: "User asks about X", "User wants to do Y"
```

Also add a few-shot example at the bottom of the prompt:

```go
{"intents": ["my-agent"]}
```

This is the only Go file you need to touch when adding a new agent.

---

## Step 5 — Rebuild and restart

```bash
# Docker
docker compose up --build -d

# Local
make run
```

On startup you should see:

```
INFO  generic.LoadAll: loaded agent  id=my-agent  intents=[my-agent my-keyword]
```

If the folder is skipped, a `WARN generic.LoadAll: skipping agent` line will show the reason (missing file, invalid YAML, empty system prompt, etc.).

---

## Dynamic context (automatic)

Every agent loaded via `agent.yaml` automatically receives three context blocks appended to its system prompt at call time — you do not need to add anything to your files:

```
## User context
Name: Marco
Communication style: direct
Preferences:
  - sign_off: Marco

## User personality (inferred — treat as guidance, not rules)
- Communication style: direct (confidence: 0.8)
- Response length preference: brief (confidence: 0.7)
- Technical depth: high (confidence: 0.9)

## Current time
Local date/time (use this UTC offset for ALL calendar timestamps): 2026-05-06T16:00:00+03:00
Day of week: Wednesday
```

**User context** — the user's name, communication style, preferences, and recurring contacts from their profile (set via `user_profile_update` or learned over time).

**User personality** — behavioural signals observed by the background personality observer. After each conversation the observer extracts signals (response length preference, technical depth, communication style, topic interests, and more) and persists them in SQLite. A signal only appears here once its confidence reaches 0.6 — meaning it has been consistently observed across multiple separate conversations. All your agents share these signals automatically, so they all adapt to the user's style without any per-agent code.

**Current time** — the local date, time, and UTC offset. Always present. Reference it in calendar-related rules: "use the UTC offset from ## Current time for all calendar timestamps."

---

## Sub-agents (optional)

An agent can delegate sub-tasks to other agents by declaring them in `agent.yaml`:

```yaml
sub_agents:
  - research
  - notes
```

This gives the agent access to a `call_agent` tool:

```json
{
  "tool": "call_agent",
  "agent_id": "research",
  "prompt": "Find recent papers on LLM memory"
}
```

Only agents listed in `sub_agents` can be called — the tool rejects any other `agent_id`.

---

## Full example — Finance Agent

```
agents/finance/agent.yaml
agents/finance/SYSTEM.md
```

**`agent.yaml`**

```yaml
id: finance
model: gemma4:26b
max_tokens: 4096
intents:
  - finance
  - budget
  - expense
  - invoice
skills:
  - web_search
  - web_fetch
  - email_search
  - email_read
```

**`SYSTEM.md`**

```markdown
You are a finance assistant. You help the user track expenses, review invoices, and research financial topics.

## Rules
- Never give investment advice
- Always cite sources for market data
- For invoice-related tasks, search the user's email first

## Workflow patterns
- "How much did I spend on X?" → email_search for receipts → summarise amounts
- "What's the current EUR/USD rate?" → web_search → report with source
- "Find the invoice from Acme" → email_search "invoice acme" → email_read → summarise
```

**`classifier.go` addition**

```go
- "finance" – Questions about budgets, expenses, invoices, or financial research.
              Examples: "How much did I spend last month?", "Find the invoice from Acme",
                        "What's the EUR/USD rate?"
```

Restart the server — the Finance Agent is live.
