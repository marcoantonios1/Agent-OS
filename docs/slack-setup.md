# Slack Channel Setup

This guide walks through creating a Slack app, enabling Socket Mode, and configuring Agent OS to respond to your direct messages on Slack.

## How it works

Agent OS uses Slack's **Socket Mode** — a persistent WebSocket connection that lets the bot receive events without a public HTTPS endpoint. You only need the bot token and an app-level token; no web server or ngrok tunnel is required.

The bot responds only to **direct messages (DMs)** from the Slack user ID configured in `SLACK_ALLOWED_USER_ID`. All other messages and channels are silently ignored.

---

## 1. Create the Slack App

1. Go to [api.slack.com/apps](https://api.slack.com/apps) and sign in to your workspace.
2. Click **Create New App** → **From scratch**.
3. Give the app a name (e.g. *Agent OS*) and select your workspace.
4. Click **Create App**.

---

## 2. Enable Socket Mode

1. In the left sidebar, click **Socket Mode**.
2. Toggle **Enable Socket Mode** to on.
3. You will be prompted to create an **App-Level Token**:
   - Token name: `socket-mode` (or any label you like)
   - Scope: **`connections:write`**
4. Click **Generate** and copy the token — it starts with `xapp-`.
   This is your `SLACK_APP_TOKEN`.

---

## 3. Subscribe to Bot Events

1. In the left sidebar, click **Event Subscriptions**.
2. Toggle **Enable Events** to on.
3. Under **Subscribe to bot events**, click **Add Bot User Event** and add:
   - `message.im`
4. Click **Save Changes**.

---

## 4. Add Bot Token Scopes

1. In the left sidebar, click **OAuth & Permissions**.
2. Under **Bot Token Scopes**, click **Add an OAuth Scope** and add:
   - `chat:write` — post messages
   - `im:write` — open DM channels
   - `im:history` — read DM history (required for `message.im` events)
   - `files:read` — access uploaded files (images, audio, PDFs)
   - `files:write` — upload synthesized audio responses

---

## 5. Install the App to Your Workspace

1. In the left sidebar, click **OAuth & Permissions**.
2. Click **Install to Workspace** and approve the permissions.
3. Copy the **Bot User OAuth Token** — it starts with `xoxb-`.
   This is your `SLACK_BOT_TOKEN`.

---

## 6. Find Your Slack User ID

Your user ID is the `SLACK_ALLOWED_USER_ID`. Agent OS only responds to messages from this ID.

**In the Slack desktop or web app:**

1. Click your profile picture or name.
2. Click **Profile**.
3. Click the three-dot menu (**···**) in the top right of your profile.
4. Click **Copy member ID**.

The ID looks like `U0123456789`.

---

## 7. Set Environment Variables

Add the following to your `.env` file:

```env
SLACK_BOT_TOKEN=xoxb-...          # Bot OAuth token from step 5
SLACK_APP_TOKEN=xapp-...          # App-level token from step 2
SLACK_ALLOWED_USER_ID=U0123456789 # Your member ID from step 6
```

**Startup behavior:**

| Condition | Result |
|-----------|--------|
| `SLACK_BOT_TOKEN` not set | Server starts normally; Slack disabled |
| `SLACK_BOT_TOKEN` set, `SLACK_APP_TOKEN` missing | Startup fails with clear error |
| `SLACK_BOT_TOKEN` set, `SLACK_ALLOWED_USER_ID` missing | Startup fails with clear error |
| All three set | Slack channel enabled |

---

## 8. Local Testing

No public URL is required. Socket Mode works from localhost:

```bash
# Start the server
go run ./cmd/agentos/

# Or with Docker Compose
docker compose up
```

Watch the logs for:
```
INFO slack channel starting allowed_uid=U0123456789
INFO slack: socket connecting
INFO slack: socket connected
```

Then open Slack, find your bot in **Apps** (or search by name), and send it a direct message.

---

## Supported Message Types

| Type | Behaviour |
|------|-----------|
| Text message | Routed to the agent; streamed reply sent back |
| Image upload (JPEG, PNG, WebP, GIF) | Forwarded to LLM as a vision attachment |
| PDF upload | Text extracted and included in the prompt |
| Audio/voice upload | Transcribed via Whisper; reply synthesized as audio if `VOICE_TTS=enabled` |
| Other file types | Silently ignored |

---

## Reminders

When a reminder fires for a session that originated in Slack, the message is delivered to the same DM channel. Session IDs are stored as `slack:{userID}:{channelID}` and are stable across restarts.

---

## Troubleshooting

**Bot does not respond to messages**
- Confirm `SLACK_ALLOWED_USER_ID` matches your Slack member ID exactly.
- Check that the event subscription includes `message.im`.
- Check server logs for `slack: ignoring` lines.

**`invalid_auth` on startup**
- The bot token may be expired or revoked. Re-install the app to the workspace to generate a fresh token.

**Socket disconnects frequently**
- This is normal — Socket Mode reconnects automatically. Look for `slack: socket connected` in the logs after each reconnect.

**File downloads fail**
- Ensure the `files:read` scope is added to the bot token and the app is re-installed after adding it.
