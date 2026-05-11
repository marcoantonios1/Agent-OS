# Agent OS

A local-first, self-hosted AI personal assistant. One `make run` and you have an agent that manages your email, calendar, answers research questions, writes code, and checks in on you — running entirely on your own machine, talking to a model you choose.

No cloud AI API keys. No data leaving your infrastructure.

---

## How it works

You talk to Agent OS through any channel — web, Discord, or WhatsApp. The router classifies your intent and dispatches to the right agent. Agents run an agentic loop (LLM ↔ tools) and stream back a response. Session history, user preferences, and reminders all persist in a local SQLite database.

```
You (Web · Discord · WhatsApp)
           │
           ▼
    Router + Classifier
           │
   ┌───────┼───────────┬────────────┐
   ▼       ▼           ▼            ▼
 Comms  Builder    Research     Generic agents
  │       │           │         (agents/ folder)
  ▼       ▼           ▼              ▼
Email   Code       WebSearch    Any declared
Cal     Shell      WebFetch     skill subset
Profile Project
Reminder
```

**Adding a new agent:** create a folder under `agents/` with `agent.yaml` + `SYSTEM.md`. No Go code, no rebuild. The server picks it up on next start. See [docs/adding-agents.md](docs/adding-agents.md).

**Learns about you:** after every conversation the router runs a background observer that extracts behavioural signals — how you like responses, your technical depth, communication style, interests. Once a signal has enough confidence it's automatically injected into every agent's system prompt so they all adapt without you having to repeat yourself.

---

## Run it in three steps

```bash
# 1. Copy and edit config — only COSTGUARD_URL is required
cp .env.example .env

# 2. Start the server (Docker handles migrations, then starts the server)
make run

# 3. Talk to it
curl -X POST http://localhost:9091/v1/chat \
  -H "Content-Type: application/json" \
  -d '{"session_id":"s1","user_id":"u1","text":"Check my emails"}'
```

`make run` uses Docker Compose under the hood. It builds the image, runs database migrations, starts the server on port `9091`, and tails the logs. `ctrl+c` stops everything cleanly.

### Without Docker

```bash
cp .env.example .env          # set COSTGUARD_URL and SQLITE_PATH at minimum
make migrate                  # apply DB migrations once
make run                      # starts the local binary
```

---

## Costguard — the LLM gateway

Agent OS talks to your models through [Costguard](https://github.com/marcoantonios1/costguard), a lightweight local gateway that proxies requests to Ollama (or any OpenAI-compatible endpoint). Set `COSTGUARD_URL` to wherever Costguard is running.

```
COSTGUARD_URL=http://localhost:8080   # default if running locally
```

Run a model you have locally in Ollama and point Costguard at it. That's the whole setup — no Anthropic key, no OpenAI key, nothing external.

---

## Built-in agents

| Agent | What it does | Triggered by |
|---|---|---|
| **Comms** | Email (list/read/search/draft/send), calendar (list/read/create/update), reminders | "Check my emails", "Schedule a meeting", "Remind me to…" |
| **Research** | Web search + page fetch via Brave Search | "Search for…", "What's the latest on…" |
| **Builder** | Requirements → spec → tasks → code → review workflow | "Build me…", "Write a script that…" |
| **Reviewer** | Code review on workspace files | "Review my code" |
| **Doctor** | Medical information assistant (MedGemma) | "I have a headache", "What are symptoms of…" |
| **Companion** | Personal conversational companion, profile-aware | "Just chat", "How's my week looking?" |
| **Notes** | Capture, find, and update markdown notes | "Note that…", "Find my note about…" |
| **Profile Query** | Answers "what do you know about me?" from accumulated personality signals | "What do you know about me?" |
| **Support** | Help and how-to questions about Agent OS itself | "How do I add an agent?" |

### Adding your own agent

```bash
mkdir agents/chef
```

**`agents/chef/agent.yaml`**
```yaml
id: chef
model: gemma4:26b
intents:
  - chef
  - recipe
  - cooking
skills:
  - web_search
  - web_fetch
```

**`agents/chef/SYSTEM.md`**
```
You are a culinary assistant. Suggest recipes, explain techniques,
and find ingredient substitutions using web search when needed.
```

**`agents/chef/SOUL.md`** _(optional)_
```
You speak like a passionate home cook, not a professional chef.
Keep it conversational. Use ingredient quantities people can visualise
("a small handful", "two glugs of olive oil"). Never sound like a recipe book.
```

`SOUL.md` is appended to the system prompt after `SYSTEM.md`. Use it to define tone, character, and style separately from capability — so you can tweak how an agent talks without touching its instructions. The companion agent's `SOUL.md` is a good reference.

**Then register your intents with the classifier** — open `internal/router/classifier.go` and add an entry to the `systemPrompt` constant so the router knows when to route to your agent:

```go
- "chef"    – Questions about cooking, recipes, ingredients, or food preparation.
              Also use for "recipe" and "cooking" intents.
              Examples: "Find me a pasta recipe", "What can I make with chickpeas?",
                        "How do I caramelise onions?"
```

Without this step the router never picks your agent — it only knows about intents listed in the classifier prompt.

Restart and ask: _"Find me a recipe for sourdough bread"_ — the classifier routes to your new agent.

Full agent authoring reference: [docs/adding-agents.md](docs/adding-agents.md) · [docs/agent-config.md](docs/agent-config.md) · [docs/skills.md](docs/skills.md)

---

## Channels

### Web (always on)
```bash
# Standard request/response
curl -X POST http://localhost:9091/v1/chat \
  -H "Content-Type: application/json" \
  -d '{"session_id":"s1","user_id":"u1","text":"What is on my calendar today?"}'

# Streaming (SSE)
curl -N -X POST http://localhost:9091/v1/chat/stream \
  -H "Content-Type: application/json" \
  -d '{"session_id":"s1","user_id":"u1","text":"Summarise my inbox"}'
```

### Discord — [setup guide](docs/discord-setup.md)
Set `DISCORD_BOT_TOKEN` in `.env`. DMs are always routed; in server channels a configurable prefix is required.

### WhatsApp — [setup guide](docs/whatsapp-setup.md)
Set `WHATSAPP_STORE_PATH` in `.env`. On first run a QR code prints to the logs — scan it with WhatsApp → Linked Devices. The session persists automatically.

---

## Heartbeat

A background worker that ticks on a configurable interval, runs a prompt through the router, and delivers the response via Discord or WhatsApp. Useful for a daily briefing, inbox triage, or a morning calendar summary.

```bash
# Enable in .env:
HEARTBEAT_INTERVAL=1h
HEARTBEAT_CHANNEL=discord
HEARTBEAT_USER_ID=your-user-id
```

Edit what it checks without restarting:

```bash
make beat   # copies HEARTBEAT.md into the running container's workspace
```

---

## Learns about you

After every conversation (3+ turns) the router runs a background observer that reads the transcript and extracts behavioural signals without blocking your response. Signals are persisted in SQLite and accumulate confidence over time — the same signal needs to be observed roughly 6 times before it crosses the confidence threshold and starts being injected.

Once a signal is confident enough it's automatically appended to the system prompt of **every** agent, so they all adapt without you repeating yourself:

```
## User personality (inferred — treat as guidance, not rules)
- Communication style: direct (confidence: 0.8)
- Response length preference: brief (confidence: 0.7)
- Technical depth: high (confidence: 0.9)
- Topic interests: golang, systems-design, startups (confidence: 0.7)
```

Signals observed:

| Signal | What it captures | Example values |
|---|---|---|
| `response_length` | How long you like replies | `brief` · `detailed` · `verbose` |
| `technical_depth` | How deep to go technically | `low` · `medium` · `high` |
| `communication_style` | Formality you prefer | `formal` · `casual` · `direct` |
| `humor_tolerance` | Whether you appreciate jokes | `none` · `light` · `high` |
| `question_style` | How you ask questions | `asks_followup` · `assumes` · `guesses` |
| `working_hours` | When you tend to work | `morning` · `evening` · `night` · `mixed` |
| `urgency_pattern` | How time-sensitive your requests feel | `high` · `medium` · `low` |
| `topic_interests` | Recurring subjects | comma-separated topics |

To ask what it knows about you: _"What do you know about me?"_ — the Profile Query agent answers from the accumulated signals.

---

## Configuration

Copy `.env.example` to `.env`. Only `COSTGUARD_URL` is required — everything else is optional and has sensible defaults.

### Core

| Variable | Default | Description |
|---|---|---|
| `COSTGUARD_URL` | — | **Required.** LLM gateway URL |
| `COSTGUARD_API_KEY` | — | Bearer token for the gateway (optional) |
| `PORT` | `9091` | HTTP server port |
| `LOG_LEVEL` | `info` | `debug` / `info` / `warn` / `error` |
| `SQLITE_PATH` | — | Path to SQLite DB (e.g. `./data/agentos.db`). Unset = in-memory, data lost on restart. |
| `BUILDER_SANDBOX_DIR` | `workspace` | Root directory for Builder Agent file + shell operations |

### Models

All optional — defaults to `gemma4:26b`. Use a small fast model for the classifier and profile observer; they only emit short structured JSON.

| Variable | Agent |
|---|---|
| `COMMS_MODEL` | Comms |
| `BUILDER_MODEL` | Builder |
| `RESEARCH_MODEL` | Research |
| `CLASSIFIER_MODEL` | Router (try `llama3.2:3b`) |
| `PROFILE_MODEL` | Personality observer (try `llama3.2:3b`) |

### Email + Calendar

#### Google (Gmail + Google Calendar) — [full setup guide](docs/email-setup.md)
One refresh token covers both Gmail and Google Calendar. Run the setup tool once:
```bash
go run ./cmd/tool/googleauth/
```
Then set `GOOGLE_CLIENT_ID`, `GOOGLE_CLIENT_SECRET`, `GOOGLE_REFRESH_TOKEN`.

See [docs/calendar-setup.md](docs/calendar-setup.md) for Google Calendar-specific notes.

#### Microsoft (Outlook Mail + Calendar)
```bash
go run ./cmd/tool/microsoftauth/
```
Then set `MICROSOFT_CLIENT_ID`, `MICROSOFT_REFRESH_TOKEN`.

Both providers active? Agent OS reads from both in parallel and writes to the primary (Google first).

### Research (web search)
Get a free key at [brave.com/search/api](https://brave.com/search/api/) and set `SEARCH_API_KEY`. Without it the Research Agent answers from model knowledge only.

### Context compaction

Long sessions are automatically summarised when estimated token count exceeds the threshold (characters ÷ 4). The 10 most recent turns are always kept verbatim.

| Variable | Default | Description |
|---|---|---|
| `COMPACTION_THRESHOLD` | `6000` | Token count that triggers compaction. `0` = disabled. |

---

## Makefile

| Target | What it does |
|---|---|
| `make run` | `docker compose up --build` — builds, migrates, starts, tails logs |
| `make beat` | Copies `HEARTBEAT.md` into the running container's workspace |
| `make migrate` | Runs DB migrations locally (no Docker) |
| `make test` | `go test ./... -race` |
| `make build` | `go build ./...` |
| `make lint` | `go vet ./...` |

---

## Project layout

```
agents/                   ← folder-based agents (add yours here)
  comms/                  agent.yaml + SYSTEM.md
  research/
  doctor/
  companion/
  notes/
  profile_query/
  support/
cmd/
  agentos/                main server entrypoint
  migrate/                DB migration CLI (also used as Docker init container)
  tool/
    googleauth/           one-time Google OAuth2 setup
    microsoftauth/        one-time Microsoft device code setup
internal/
  router/                 intent classifier, session lifecycle, compound dispatch
  agents/
    builder/              requirements → spec → tasks → codegen → review
    reviewer/             code review: reads workspace, emits structured feedback
    generic/              folder-based loader (agents/ directory)
    profile/              background personality observer
  channels/
    web/                  HTTP: /v1/chat, /v1/chat/stream, /healthz, /readyz
    discord/              Discord gateway
    whatsApp/             WhatsApp Web gateway
  tools/
    email/                email_list/read/search/draft/send + Gmail/Outlook
    calendar/             calendar_list/read/create/update + Google/Outlook
    websearch/            web_search, web_fetch + Brave
    code/                 file_read/write/list, shell_run (Builder sandbox)
    reminder/             reminder_set/cancel/list + background worker
    userprofile/          user_profile_read/update
    project/              project_list/load
  skills/                 NewGlobalRegistry — wires all tools into one registry
  memory/                 SQLite + in-memory store implementations
  costguard/              LLM client (Complete + Stream) with backoff retry
  heartbeat/              background worker: ticks, runs prompt, delivers response
  sessions/               store interfaces (SessionStore, UserStore, …)
  approval/               confirmation gate for send/create/update actions
migrations/
docs/
  adding-agents.md        step-by-step: new agent with no Go code
  skills.md               full built-in skill reference
  email-setup.md
  calendar-setup.md
  whatsapp-setup.md
test/
  integration/            full HTTP stack tests with scripted LLM + mock providers
```

---

## API reference

### `POST /v1/chat`
```json
// request
{ "session_id": "s1", "user_id": "u1", "text": "Draft a reply to Alice" }

// response
{ "session_id": "s1", "text": "Here is a draft reply…" }
```

### `POST /v1/chat/stream`
Server-Sent Events, one token per frame.
```
data: {"delta":"Here "}
data: {"delta":"is "}
data: {"delta":"a draft…"}
data: {"done":true}
```

### `GET /healthz` — liveness probe (`200 ok`)
### `GET /readyz` — readiness probe (`200 ok` when ready)
