# WhatsApp Channel Setup

Agent OS can receive and respond to WhatsApp messages via the [go.mau.fi/whatsmeow](https://go.mau.fi/whatsmeow) library — a pure-Go, CGO-free WhatsApp Web multi-device client. No separate process or Puppeteer dependency is required.

## How it works

The WhatsApp channel links Agent OS as a **companion device** to your existing WhatsApp account (the same mechanism as WhatsApp Web and WhatsApp Desktop). On first run a QR code is printed to the terminal; scan it once and the session is persisted to a local SQLite file — subsequent restarts reconnect automatically.

For security, the channel only processes messages from a single configured JID (`WHATSAPP_ALLOWED_JID`). All other senders are silently ignored.

## Prerequisites

- An active WhatsApp account on a phone.
- The phone must be online at least during initial pairing (subsequent sessions work while the phone is offline, as with other linked devices).

## Environment variables

| Variable | Required | Description |
|---|---|---|
| `WHATSAPP_STORE_PATH` | Yes (to enable) | Path to the SQLite file that stores the device pairing session (e.g. `whatsapp.db`). Setting this enables the WhatsApp channel. |
| `WHATSAPP_ALLOWED_JID` | Yes (when WhatsApp enabled) | The only WhatsApp JID that Agent OS will respond to. Format: `<phone>@s.whatsapp.net` (e.g. `96170123456@s.whatsapp.net`). |

Add both to your `.env` file:

```env
WHATSAPP_STORE_PATH=whatsapp.db
WHATSAPP_ALLOWED_JID=96170123456@s.whatsapp.net
```

## Finding your JID

Your JID is your phone number in international format (no `+`, no spaces) followed by `@s.whatsapp.net`.

Examples:
- Lebanon +961 70 123 456 → `96170123456@s.whatsapp.net`
- UK +44 7700 900 000 → `447700900000@s.whatsapp.net`
- US +1 415 555 0100 → `14155550100@s.whatsapp.net`

## First run — device pairing

1. Set both env vars and start Agent OS.
2. A QR code will appear in the terminal:
   ```
   Agent OS started port=9091
   whatsapp: scan the QR code below with WhatsApp → Linked Devices → Link a Device
   ████████████████
   ██ ▄▄▄▄▄ █▀█ █
   …
   ```
3. Open WhatsApp on your phone → **Linked Devices** → **Link a Device** → scan the QR.
4. Once pairing succeeds you'll see: `whatsapp: pairing successful — session saved to store`
5. The session is now persisted at `WHATSAPP_STORE_PATH`. Future restarts reconnect silently.

## Subsequent runs

Agent OS reconnects automatically. You do not need to re-scan the QR code unless the session expires (WhatsApp invalidates linked-device sessions after ~14 days of the phone being offline).

## Messaging

Send any text message from your WhatsApp account to the number Agent OS is linked to. Responses are sent back as a single message once the agent finishes (streaming is buffered since WhatsApp does not support live message editing).

Media messages (images, audio, stickers, reactions) are silently ignored — only text is routed.

## Unlinking

To unlink Agent OS from your WhatsApp account, go to WhatsApp on your phone → **Linked Devices** → select the device → **Log out**. Delete `WHATSAPP_STORE_PATH` to force a fresh pairing on the next start.
