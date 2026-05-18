# iMessage Setup (via BlueBubbles)

Agent OS integrates with iMessage through [BlueBubbles](https://bluebubbles.app) — an open-source server that exposes iMessage over a REST API. BlueBubbles must run on the same Mac where your iMessage account is signed in.

---

## Prerequisites

- A Mac with an active iMessage account (Apple ID signed in to Messages)
- BlueBubbles Server installed and running on that Mac
- Agent OS reachable from the Mac on the configured webhook port (default: 18789)

---

## Step 1 — Install BlueBubbles Server

1. Download BlueBubbles Server from [bluebubbles.app](https://bluebubbles.app)
2. Open the app and follow the setup wizard
3. Set a server password in **Preferences → Security → Server Password**
4. Note the server URL shown in the main window (e.g. `http://localhost:1234` or a ngrok URL if tunnelling)

---

## Step 2 — Find your iMessage handle

Your handle is the phone number or email address associated with your iMessage account. Use international format for phone numbers.

Examples:
- `+15551234567`
- `yourname@icloud.com`

You can verify the handle by opening **Messages → Settings → iMessage** on your Mac.

---

## Step 3 — Configure Agent OS

Add these variables to your `.env` file:

| Variable | Description |
|----------|-------------|
| `BLUEBUBBLES_URL` | Base URL of the BlueBubbles server (e.g. `http://localhost:1234`) |
| `BLUEBUBBLES_PASSWORD` | The server password you set in BlueBubbles preferences |
| `BLUEBUBBLES_ALLOWED_HANDLE` | Your iMessage handle — only messages from this address are served |
| `BLUEBUBBLES_WEBHOOK_PORT` | Local TCP port Agent OS listens on for inbound events (default: `18789`) |

```env
BLUEBUBBLES_URL=http://localhost:1234
BLUEBUBBLES_PASSWORD=your-server-password
BLUEBUBBLES_ALLOWED_HANDLE=+15551234567
BLUEBUBBLES_WEBHOOK_PORT=18789
```

---

## Step 4 — Network configuration

BlueBubbles must be able to POST events to Agent OS at:

```
http://localhost:{BLUEBUBBLES_WEBHOOK_PORT}/webhook
```

**Same machine:** If Agent OS runs on the same Mac as BlueBubbles, `localhost` works with no extra configuration.

**Docker:** If Agent OS runs in Docker on the same Mac, use `http://host.docker.internal:18789/webhook` in BlueBubbles webhook settings. No code change needed — Agent OS registers the webhook automatically using `localhost`, but you may override this by setting the webhook URL directly in BlueBubbles if auto-registration doesn't match your network topology.

**Remote server:** If Agent OS runs on a different machine, expose port `BLUEBUBBLES_WEBHOOK_PORT` and use the server's IP or hostname when configuring BlueBubbles. Agent OS registers the webhook using `http://localhost:{port}` by default — if your setup requires a different webhook URL, open a GitHub issue.

---

## How it works

On startup Agent OS:

1. Calls `GET /api/v1/ping` to verify BlueBubbles is reachable — startup fails immediately if not
2. Calls `POST /api/v1/webhook` to register itself as a listener for `new-message` events
3. Starts an HTTP server on `BLUEBUBBLES_WEBHOOK_PORT` at `/webhook`
4. On shutdown, deregisters the webhook and gracefully closes the HTTP server

Inbound flow:
- BlueBubbles POSTs a `new-message` event to Agent OS
- Agent OS filters to messages from `BLUEBUBBLES_ALLOWED_HANDLE` only
- Text, images, and PDFs are passed to the router
- Audio attachments are transcribed (when `VOICE_TRANSCRIPTION=enabled`)

Outbound flow:
- Text replies are sent via `POST /api/v1/message/text`
- Voice replies (when `VOICE_TTS=enabled`) are sent via `POST /api/v1/attachment/upload`

Session key format: `imessage:{handle}:{chatGUID}` — stable across restarts.

---

## Troubleshooting

**Startup fails with "BlueBubbles ping failed"**
- Verify `BLUEBUBBLES_URL` is correct and the BlueBubbles app is running
- Check that the password matches what is set in BlueBubbles preferences

**No messages received**
- Confirm BlueBubbles can reach Agent OS on the webhook port (try `curl http://localhost:18789/webhook` from the Mac)
- Check BlueBubbles → Settings → Webhooks to confirm the webhook is registered

**Messages from wrong address not filtered**
- Verify `BLUEBUBBLES_ALLOWED_HANDLE` matches exactly what BlueBubbles reports — check the address field in incoming message logs (`LOG_LEVEL=debug`)

**Voice messages not transcribed**
- Set `VOICE_TRANSCRIPTION=enabled` and ensure `COSTGUARD_URL` points to a Whisper-compatible endpoint
