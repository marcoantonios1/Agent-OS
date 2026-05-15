# Skills reference

Skills are tools the model can call during its agentic loop.  Each skill maps
to a registered Go function in the tool registry.  An agent only has access to
skills listed in its `agent.yaml` — all others are invisible to the model.

Specify skills by their **skill name** (the `Name` field from the tool
definition) in the `skills` array:

```yaml
skills:
  - web_search
  - web_fetch
  - reminder_set
```

---

## Table of contents

- [Web](#web) — `web_search`, `web_fetch`
- [Email](#email) — `email_list`, `email_read`, `email_search`, `email_draft`, `email_send`
- [Calendar](#calendar) — `calendar_list`, `calendar_read`, `calendar_create`, `calendar_update`
- [Reminders](#reminders) — `reminder_set`, `reminder_list`, `reminder_cancel`
- [User profile](#user-profile) — `user_profile_read`, `user_profile_update`
- [File system](#file-system-builder-sandbox) — `file_read`, `file_write`, `file_list`, `shell_run`
- [Project management](#project-management) — `project_list`, `project_load`
- [Skill availability by agent](#skill-availability-by-built-in-agent)
- [Community skills](#community-skills)

---

## Web

### `web_search`

Search the web for current information.

- **Returns:** list of results — title, URL, and a short snippet per result.
- **Use when:** the model needs up-to-date facts, news, or external data not in the conversation.
- **Agents that use it:** `research`, `builder`

### `web_fetch`

Fetch a URL and return its readable text content.  HTML tags are stripped.

- **Returns:** plain-text body of the page.
- **Use when:** following up a `web_search` result to read the full content of a page, or fetching a known URL directly.
- **Agents that use it:** `research`, `builder`

---

## Email

> Email skills require a configured email provider (Gmail or Outlook).
> See [docs/email-setup.md](email-setup.md).

### `email_list`

List recent emails.

- **Returns:** subject, sender, date, and a short snippet for each message.
- **Input:** `limit` (int, optional) — number of messages to return.

### `email_read`

Read the full content of an email by its ID.

- **Returns:** full email body, headers, and any quoted threads.
- **Input:** `id` (string) — message ID returned by `email_list` or `email_search`.

### `email_search`

Search emails by a query string (sender, subject keywords, date ranges).

- **Returns:** same format as `email_list`.
- **Input:** `query` (string) — e.g. `"from:alice subject:invoice"`.

### `email_draft`

Compose an email draft.  **Does not send.**

- **Returns:** the draft content for the model to show the user before sending.
- **Input:** `to`, `subject`, `body` (all strings).
- **Approval:** none — drafting is non-destructive.

### `email_send`

Send an email.  **Requires explicit user confirmation.**

- **Behaviour:** the first call registers a pending approval and returns a
  `pending_approval` response.  The model must show the draft to the user and
  ask for confirmation.  Once the user confirms, the model calls this tool
  again and the email is sent.
- **Input:** `to`, `subject`, `body` (all strings).
- **Approval required:** yes — destructive action (sends an external message).

---

## Calendar

> Calendar skills require a configured calendar provider (Google Calendar or
> Outlook).  See [docs/calendar-setup.md](calendar-setup.md).

### `calendar_list`

List calendar events for a date range.

- **Returns:** event ID, title, start/end time, and location for each event.
- **Input:** `from`, `to` (strings, ISO-8601 or natural language like `"today"`).

### `calendar_read`

Read the full details of a calendar event by its ID.

- **Returns:** all event fields including description, attendees, and recurrence rule.
- **Input:** `id` (string) — event ID returned by `calendar_list`.

### `calendar_create`

Create a new calendar event.

- **Returns:** the created event with its assigned ID.
- **Input:** `title`, `start`, `end` (required); `description`, `location` (optional).

### `calendar_update`

Update an existing calendar event.  Only provided fields are changed.

- **Returns:** the updated event.
- **Input:** `id` (required); `title`, `start`, `end`, `description`, `location` (all optional).

---

## Reminders

### `reminder_set`

Schedule a follow-up reminder for the user.

- **Returns:** confirmation with the scheduled fire time.
- **Input:**
  - `message` (string) — text delivered to the user when the reminder fires.
  - `when` (string) — natural language (`"in 30 minutes"`, `"tomorrow at 9am"`) or ISO-8601.
  - `agent_prompt` (string, optional) — a prompt run by the Comms Agent at fire time to surface context-dependent information (e.g. `"Search Alice's recent emails and summarise the invoice thread."`).

### `reminder_list`

List all pending reminders for the current user, ordered by fire time ascending.

- **Returns:** reminder ID, message, and scheduled fire time for each entry.

### `reminder_cancel`

Cancel a previously scheduled reminder by its ID.

- **Returns:** confirmation.
- **Input:** `id` (string) — reminder ID returned by `reminder_list`.

---

## User profile

### `user_profile_read`

Retrieve the current user's persistent profile.

- **Returns:** name, communication style, tone preferences, timezone, recurring contacts, and any custom fields stored by previous sessions.
- **Use when:** starting a personalised task (drafting an email in the user's voice, scheduling at the right time, addressing the right people).

### `user_profile_update`

Update the current user's profile.  All fields are optional — only provided fields are changed.

- **Use when:** the user mentions a preference, name, timezone, or contact that should be remembered.
- **Input:** `name`, `tone`, `timezone`, `sign_off`, `contacts` (all optional).

---

## File system (builder sandbox)

> These skills operate inside an isolated sandbox directory.  Paths are
> relative to the sandbox root.  Absolute paths and `..` traversal are
> rejected.  Network access and destructive shell commands are blocked.

### `file_read`

Read the contents of a file.

- **Returns:** full file content as a string.
- **Input:** `path` (string, relative to sandbox root).

### `file_write`

Write or overwrite a file with the given content.  Parent directories are
created automatically.

- **Input:** `path` (string), `content` (string).

### `file_list`

List files and directories inside a directory.

- **Returns:** names, types (file/directory), and sizes.
- **Input:** `path` (string) — use `"."` or `""` for the sandbox root.

### `shell_run`

Run a shell command inside the sandbox directory.  Captures stdout and stderr.
A timeout is enforced.  Destructive commands (`rm -rf`, `git push`, etc.) and
network access are blocked.

- **Returns:** combined stdout + stderr output and exit code.
- **Input:** `command` (string) — e.g. `"go build ./..."`, `"go test ./..."`.
- **Typical use:** compile, test, lint, format code.

---

## Project management

### `project_list`

List all Builder Agent projects for the current user.

- **Returns:** project IDs, names, current phase, and last-updated timestamps.
- **Use before:** `project_load` to find the right project ID.

### `project_load`

Load an existing project into the current session so the agent can resume work.

- **Returns:** project metadata and the saved phase state.
- **Input:** `id` (string) — project ID from `project_list`.

---

## Skill availability by built-in agent

| Skill | `comms` | `builder` | `research` | `reviewer` |
|---|:---:|:---:|:---:|:---:|
| `web_search` | | ✓ | ✓ | |
| `web_fetch` | | ✓ | ✓ | |
| `email_list` | ✓ | | | |
| `email_read` | ✓ | | | |
| `email_search` | ✓ | | | |
| `email_draft` | ✓ | | | |
| `email_send` | ✓ | | | |
| `calendar_list` | ✓ | | | |
| `calendar_read` | ✓ | | | |
| `calendar_create` | ✓ | | | |
| `calendar_update` | ✓ | | | |
| `reminder_set` | ✓ | | | |
| `reminder_list` | ✓ | | | |
| `reminder_cancel` | ✓ | | | |
| `user_profile_read` | ✓ | | | |
| `user_profile_update` | ✓ | | | |
| `file_read` | | ✓ | | ✓ |
| `file_write` | | ✓ | | |
| `file_list` | | ✓ | | ✓ |
| `shell_run` | | ✓ | | ✓ |
| `project_list` | | ✓ | | |
| `project_load` | | ✓ | | |

User-defined agents may combine any subset of these skills regardless of the
built-in defaults.

---

## Community skills

Community skills extend the built-in registry without touching core code.  They live in `skills/community/` and are registered in `skills/community/register.go`.

### Ready-to-use examples

Three example skills ship in `skills/community/examples/`.  Copy any of them into `skills/community/` to activate:

| Skill name | Description | Credentials |
|---|---|---|
| `weather` | Current temperature, conditions, humidity, and wind for any city | None — uses Open-Meteo (free) |
| `stock_price` | Current price and daily change for a stock ticker (e.g. `AAPL`) | `ALPHA_VANTAGE_KEY` env var — [free tier](https://www.alphavantage.co/support/#api-key) |
| `url_shorten` | Shorten a long URL via is.gd | None — free, no key |

Quick-start for the no-key `weather` example:

```bash
cp -r skills/community/examples/weather skills/community/weather
```

Then in `skills/community/register.go`:

```go
import "github.com/marcoantonios1/Agent-OS/skills/community/weather"

func RegisterAll(reg *tools.ToolRegistry) {
    reg.Register(weather.New())
}
```

Add `weather` to the agent's `agent.yaml` skills list and restart.

### Writing your own skill

See [docs/contributing-skills.md](contributing-skills.md) for a full walkthrough: interface definition, registration, nil-safe API-key constructors, and testing patterns.
