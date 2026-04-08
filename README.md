# Agent-OS

A multi-agent AI system with a single entry point that routes requests to specialised agents (comms, builder, research) powered by Costguard.

## Architecture

```
Channels (web / discord / whatsApp / telegram)
              │
              ▼
         Router / App
              │
    ┌─────────┼─────────┐
    ▼         ▼         ▼
 Comms     Builder   Research
 Agent      Agent     Agent
              │
    ┌─────────┼─────────┐
    ▼         ▼         ▼
 Memory  Orchestration CostGuard
              │
    ┌─────────┼─────────┐
    ▼         ▼         ▼
 Email    Calendar   WebSearch
  Tool      Tool       Tool
```

## What's built

| # | Area | Status |
|---|---|---|
| 1 | Repository structure, Go module, no-op HTTP server | Done |
| 2 | Core types — `InboundMessage`, `OutboundMessage`, `AgentRequest`, `AgentResponse`, `Session` | Done |
| 3 | Costguard LLM client — `Complete`, `Stream`, retry with exponential backoff | Done |
| 4 | Session memory store — in-memory, TTL expiry, thread-safe, swappable interface | Done |
| 5 | Intent classifier — LLM-based, routes to `comms` / `builder` / `research` / `unknown` | Done |
| 6 | Router Agent — session load/create, classify, dispatch, history persistence | Done |
| 7 | Web channel — `POST /v1/chat`, `GET /healthz`, request ID, logging, panic recovery | Done |
| 8 | Tool framework — `Tool` interface, `ToolRegistry`, agentic loop (LLM → tool → repeat) | Done |
| 9 | Email tools — `email_list`, `email_read`, `email_search`, `email_draft` with Gmail + Outlook | Done |

## Quick start

```bash
# build
make build

# run the server (stub LLM if COSTGUARD_URL is not set)
make run

# unit tests
make test

# API smoke tests (auto-starts server if not running)
make test-api

# email tool tests (uses live Gmail or Outlook if credentials are set)
make test-email
```

## Configuration

Copy `.env.example` to `.env` and fill in the values you need:

```bash
cp .env.example .env
```

| Variable | Description |
|---|---|
| `COSTGUARD_URL` | Costguard gateway base URL |
| `COSTGUARD_API_KEY` | Costguard API key |
| `PORT` | HTTP server port (default `8080`) |
| `GMAIL_*` | Gmail credentials — see [docs/email-setup.md](docs/email-setup.md) |
| `OUTLOOK_*` | Outlook credentials — see [docs/email-setup.md](docs/email-setup.md) |

## API

### `POST /v1/chat`

```json
// request
{ "session_id": "abc", "user_id": "u1", "text": "Send Alice an email about the meeting" }

// response
{ "session_id": "abc", "text": "..." }
```

### `GET /healthz`

Returns `200 ok`.

## Project layout

```
cmd/
  agentos/        — main server entrypoint
  gmailauth/      — one-time Gmail OAuth2 setup
  outlookauth/    — one-time Outlook device code setup
  emailtest/      — manual email tool test harness
internal/
  types/          — shared message and session types
  costguard/      — LLM client interface + HTTP implementation
  sessions/       — SessionStore interface
  memory/         — in-memory SessionStore implementation
  router/         — intent classifier + Router Agent
  channels/web/   — HTTP handler (web chat channel)
  tools/          — Tool interface, ToolRegistry, agentic loop
  tools/email/    — email tools + EmailProvider interface
  tools/email/gmail/   — Gmail provider
  tools/email/outlook/ — Outlook provider
docs/
  email-setup.md  — Gmail and Outlook OAuth setup guide
scripts/
  test_api.sh     — HTTP API smoke tests
  test_email.sh   — email tool tests
```
