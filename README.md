# Agent OS

A multi-agent AI personal assistant that routes requests to specialised agents through a single entry point. Agents share session history, a persistent user profile, and a structured tool framework.

New agents can be added as plain folders — no Go code required. See [docs/adding-agents.md](docs/adding-agents.md).

## Architecture

```
Channels (Web · Discord · WhatsApp)
               │
               ▼
        Router / Classifier
               │
   ┌───────────┼─────────────────┐
   ▼           ▼           ▼     ▼  ...
Comms       Builder     Research  Generic agents
Agent        Agent       Agent    (agents/ folder)
   │           │           │           │
   ▼           ▼           ▼           ▼
Email       Code/File   WebSearch   Any declared
Calendar      Shell     WebFetch    skill subset
UserProfile  Project
Reminders   Load/List
```

**Request flow:** channel receives message → router classifies intent → one or more agents run their agentic loop (LLM ↔ tools) → response merged and returned → session history persisted.

**Generic agent layer:** agents defined as folders under `agents/` (an `agent.yaml` + `SYSTEM.md`) are loaded at startup with no code changes. The Comms, Research, Doctor, Companion, and Notes agents all use this mechanism. See [docs/adding-agents.md](docs/adding-agents.md) for a step-by-step guide.

## What's built

| # | Capability | Status |
|---|---|---|
| 1 | Repository structure, Go module, HTTP server skeleton | Done |
| 2 | Core types — `Session`, `AgentRequest/Response`, `ConversationTurn` | Done |
| 3 | Costguard LLM client — `Complete`, `Stream`, exponential backoff retry | Done |
| 4 | Session store — in-memory, TTL expiry, thread-safe | Done |
| 5 | Intent classifier — LLM-based, routes to `comms / builder / research / unknown` | Done |
| 6 | Router — session lifecycle, classify, dispatch, history persistence | Done |
| 7 | Web channel — `POST /v1/chat`, `GET /healthz`, `GET /readyz`, request ID, logging | Done |
| 8 | Tool framework — `Tool` interface, `ToolRegistry`, multi-step agentic loop | Done |
| 9 | Email tools — `email_list/read/search/draft/send` · Gmail + Outlook providers | Done |
| 10 | Calendar tools — `calendar_list/read/create/update` · Google + Outlook providers | Done |
| 11 | Approval gate — `email_send` and `calendar_create/update` require explicit confirmation | Done |
| 12 | Discord channel — bot with DM + prefix routing, progressive streaming edits | Done |
| 13 | Research Agent — `web_search`, `web_fetch` tools · Brave Search API | Done |
| 14 | Builder Agent — requirements → spec → tasks → codegen → review phase workflow | Done |
| 15 | User profile — `user_profile_read/update` tools, persisted preferences and contacts | Done |
| 16 | Project store — builder projects survive session expiry via `project_list/load` | Done |
| 17 | Context injection — user profile and project state injected into agent system prompts | Done |
| 18 | Streaming endpoint — `POST /v1/chat/stream` SSE, per-token delivery | Done |
| 19 | SQLite persistence — user profiles, projects, and reminders persist across restarts | Done |
| 20 | Reminder tool — `reminder_set/cancel/list`, background worker fires due reminders | Done |
| 21 | Docker — multi-stage `Dockerfile`, `docker-compose` with migration init container | Done |
| 22 | WhatsApp channel — WA Web gateway, DM routing, allowed-JID allowlist | Done |
| 23 | Dual email/calendar providers — Gmail + Outlook read in parallel; writes go to primary | Done |
| 24 | Context-aware reminders — `agent_prompt` field runs a Comms Agent call at fire time | Done |
| 25 | Reviewer Agent — code review workflow: reads workspace files, emits structured feedback | Done |
| 26 | Generic agent layer — add agents as `agent.yaml` + `SYSTEM.md` folders, zero Go code | Done |
| 27 | Doctor Agent — MedGemma-powered medical information assistant | Done |
| 28 | Companion Agent — personal conversational companion with user profile awareness | Done |
| 29 | Notes Agent — capture, find, and update markdown notes via file tools | Done |

## Quick start

### With Docker (recommended)

```bash
cp .env.example .env
# fill in COSTGUARD_URL and any optional credentials
docker compose up
```

`docker compose up` runs database migrations first, then starts the server on port `9091`. Data is persisted in named Docker volumes (`db_data`, `workspace`).

### Locally

```bash
cp .env.example .env
# fill in at minimum: COSTGUARD_URL, SQLITE_PATH

make migrate   # apply database migrations
make run       # start the server (ctrl+c to stop)
make test      # run all tests with race detector
```

## Configuration

Copy `.env.example` to `.env` and fill in the values you need. Environment variables always take precedence over the file.

### Server

| Variable | Required | Default | Description |
|---|---|---|---|
| `COSTGUARD_URL` | **Yes** | — | Costguard gateway base URL (e.g. `http://localhost:8080`) |
| `COSTGUARD_API_KEY` | No | — | Bearer token for the Costguard gateway |
| `PORT` | No | `9091` | TCP port the HTTP server listens on |
| `LOG_LEVEL` | No | `info` | Log level: `debug`, `info`, `warn`, `error` |
| `SESSION_TTL` | No | `24h` | Idle session expiry (e.g. `30m`, `12h`) |

### Persistence

| Variable | Required | Default | Description |
|---|---|---|---|
| `SQLITE_PATH` | No | — | Path to the SQLite database (e.g. `./data/agentos.db`). When unset, in-memory stores are used and all data is lost on restart. |

### Builder Agent

| Variable | Required | Default | Description |
|---|---|---|---|
| `BUILDER_SANDBOX_DIR` | No | `workspace` | Root directory for Builder Agent file and shell operations |

### Agent models

All model variables are optional. When unset, every agent defaults to `gemma4:26b`.

| Variable | Agent | Description |
|---|---|---|
| `COMMS_MODEL` | Comms | Model used for email, calendar, and reminder tasks |
| `BUILDER_MODEL` | Builder | Model used for code generation and project tasks |
| `RESEARCH_MODEL` | Research | Model used for web search and synthesis |
| `CLASSIFIER_MODEL` | Router | Model used to classify incoming message intent |

### Discord channel

| Variable | Required | Description |
|---|---|---|
| `DISCORD_BOT_TOKEN` | For Discord | Bot token — create one at [discord.com/developers](https://discord.com/developers/applications) |
| `DISCORD_GUILD_ID` | No | Restricts the bot to one server (recommended for personal use) |
| `DISCORD_PREFIX` | No | Require a prefix (e.g. `!ai`) in server channels; DMs are always routed |

If `DISCORD_BOT_TOKEN` is absent the server starts normally with only the web channel active.

### Research Agent

| Variable | Required | Default | Description |
|---|---|---|---|
| `SEARCH_API_KEY` | For live search | — | Brave Search API key — get one free at [brave.com/search/api](https://brave.com/search/api/) |
| `SEARCH_PROVIDER` | No | `brave` | Search backend (`brave` is the only supported provider) |

If `SEARCH_API_KEY` is absent the Research Agent still starts but uses LLM training knowledge only — no live web access.

### Google (Gmail + Google Calendar)

A single refresh token covers both services. Run the one-time setup tool:

```bash
go run ./cmd/tool/googleauth/
```

See [docs/email-setup.md](docs/email-setup.md) and [docs/calendar-setup.md](docs/calendar-setup.md) for full instructions.

| Variable | Required | Description |
|---|---|---|
| `GOOGLE_CLIENT_ID` | For Google | OAuth2 client ID |
| `GOOGLE_CLIENT_SECRET` | For Google | OAuth2 client secret |
| `GOOGLE_REFRESH_TOKEN` | For Google | Long-lived refresh token (Gmail + Calendar) |

### Microsoft (Outlook Mail + Outlook Calendar)

A single refresh token covers both services. Run the one-time setup tool:

```bash
go run ./cmd/tool/microsoftauth/
```

| Variable | Required | Description |
|---|---|---|
| `MICROSOFT_CLIENT_ID` | For Microsoft | Azure app client ID |
| `MICROSOFT_REFRESH_TOKEN` | For Microsoft | Long-lived refresh token (Outlook Mail + Calendar) |

### WhatsApp channel

WhatsApp uses the WA Web protocol via [whatsmeow](https://github.com/tulir/whatsmeow). Scan the QR code once on first run; the session is persisted automatically.

```bash
go run ./cmd/agentos/  # prints QR code to terminal on first run
```

See [docs/whatsapp-setup.md](docs/whatsapp-setup.md) for full setup instructions.

| Variable | Required | Default | Description |
|---|---|---|---|
| `WHATSAPP_STORE_PATH` | For WhatsApp | `./data/whatsapp.db` | Path to the WhatsApp session database |
| `WHATSAPP_ALLOWED_JID` | No | — | Comma-separated list of JIDs (phone numbers) allowed to send messages. Leave unset to allow all. |

If `WHATSAPP_STORE_PATH` is absent the WhatsApp channel is disabled and only web/Discord channels are active.

## API

### `POST /v1/chat`

Standard request/response.

```json
// request
{ "session_id": "abc", "user_id": "u1", "text": "Check my emails" }

// response
{ "session_id": "abc", "text": "You have 3 new emails..." }
```

### `POST /v1/chat/stream`

Server-Sent Events — tokens delivered as they arrive.

```bash
curl -N -X POST http://localhost:9091/v1/chat/stream \
  -H "Content-Type: application/json" \
  -d '{"session_id":"abc","user_id":"u1","text":"Summarise my inbox"}'
```

```
data: {"delta":"You "}
data: {"delta":"have "}
data: {"delta":"3 new emails."}
data: {"done":true}
```

### `GET /healthz`

Returns `200 ok` — liveness probe.

### `GET /readyz`

Returns `200 ok` when all readiness checks pass — readiness probe.

## Project layout

```
cmd/
  agentos/              — main server entrypoint
  migrate/              — standalone migration CLI (also used as Docker init container)
  tool/
    googleauth/         — one-time Google OAuth2 token setup
    microsoftauth/      — one-time Microsoft device code token setup
    emailtest/          — manual email tool smoke test
    calendartest/       — manual calendar tool smoke test
internal/
  types/                — shared message, session, and agent types
  costguard/            — LLM client (Complete + Stream) with retry
  sessions/             — SessionStore, UserStore, ProjectStore, ReminderStore interfaces
  memory/               — in-memory and SQLite implementations of all stores
  approval/             — approval gate for sensitive tool actions
  router/               — intent classifier, Router, compound dispatch
  channels/
    web/                — HTTP handler: /v1/chat, /v1/chat/stream, /healthz, /readyz
    discord/            — Discord gateway: DM + prefix routing, streaming edits
    whatsapp/           — WhatsApp Web gateway: QR-code auth, DM routing, allowed-JID filter
  agents/
    comms/              — Comms Agent (email + calendar + reminders + user profile)
    builder/            — Builder Agent (requirements → spec → tasks → codegen → review)
    research/           — Research Agent (web search + synthesis)
    reviewer/           — Reviewer Agent (code review: reads workspace, emits structured feedback)
    generic/            — loader: scans agents/ folders and registers them at startup
agents/
  comms/                — agent.yaml + SYSTEM.md (loaded by generic layer)
  research/             — agent.yaml + SYSTEM.md
  doctor/               — agent.yaml + SYSTEM.md (MedGemma medical assistant)
  companion/            — agent.yaml + SYSTEM.md (personal conversational companion)
  notes/                — agent.yaml + SYSTEM.md (markdown notes manager)
  tools/
    loop.go             — agentic loop (Complete for tool steps, Stream for final reply)
    email/              — email_list/read/search/draft/send + Gmail/Outlook providers
    calendar/           — calendar_list/read/create/update + Google/Outlook providers
    websearch/          — web_search, web_fetch + Brave provider
    userprofile/        — user_profile_read, user_profile_update
    project/            — project_list, project_load
    reminder/           — reminder_set, reminder_cancel, reminder_list + background worker
    code/               — file_read, file_write, file_list, shell_run (Builder sandbox)
  app/                  — config loading from .env + environment
  observability/        — structured logging setup
migrations/
  001_initial_schema.sql
  002_reminders_created_at.sql
  003_reminders_agent_prompt.sql
docs/
  adding-agents.md      — step-by-step guide: add a new agent with no Go code
  skills.md             — full list of built-in skills and what each one does
  email-setup.md
  calendar-setup.md
test/
  integration/          — full HTTP stack tests with mocked LLM and providers
    harness_test.go     — scriptedLLM, mock providers, newStack() helper
    phase3_test.go      — Phase 3 feature tests (WhatsApp, dual providers, reminders, Reviewer, Builder)
```
