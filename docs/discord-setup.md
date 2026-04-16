# Discord Setup Guide

Agent OS can receive messages and reply via Discord — both in server channels and direct messages. The Discord channel uses the same router as the web channel, so all three agents (Comms, Builder, Research) are available.

---

## 1. Create a Discord Application

1. Go to [discord.com/developers/applications](https://discord.com/developers/applications)
2. Click **New Application** → give it a name (e.g. `Agent OS`)
3. Go to the **Bot** tab → click **Add Bot**
4. Under **Privileged Gateway Intents**, enable **Message Content Intent** — without this the bot cannot read message text
5. Click **Reset Token** → copy the token (you will only see it once)

---

## 2. Add credentials to `.env`

```env
DISCORD_BOT_TOKEN=your-bot-token

# Optional — limits the bot to one server (recommended for personal use).
# Leave empty to accept messages from all servers and DMs.
DISCORD_GUILD_ID=your-guild-id
```

If `DISCORD_BOT_TOKEN` is absent, Agent OS starts normally with only the web channel active (a warning is logged).

---

## 3. Invite the bot to your server

1. In the developer portal, go to **OAuth2 → URL Generator**
2. Scopes: select `bot`
3. Bot permissions: select `Send Messages`, `Read Message History`, `View Channels`
4. Copy the generated URL, open it in a browser, select your server, click **Authorize**

---

## 4. Start Agent OS

```bash
go run ./cmd/agentos/
```

You should see this log line when the Discord gateway connects:

```
INFO discord channel started guild_id=<your-guild-id>
```

---

## 5. Talk to the bot

### In a server channel

Once the bot is in your server, mention it or type in any channel it has access to.

### Via direct message

**The bot will not appear in Discord's search** until it shares a server with you. Once it has joined your server via the invite link above:

- Click the bot's name in the member list → **Message**
- Or open: `https://discord.com/users/<APPLICATION_ID>` in a browser while logged into Discord

The **Application ID** is on the **General Information** tab of your app in the developer portal (it is not the same as the bot token).

> **Note:** If `DISCORD_GUILD_ID` is set in `.env`, only messages from that server are processed. Direct messages (which have no guild ID) are always allowed regardless of this setting.

---

## Session model

Each user gets their own conversation session per channel:

| Context | Session ID format |
|---|---|
| Server channel | `discord:{guildID}:{channelID}:{userID}` |
| Direct message | `discord:dm:{channelID}:{userID}` |

This means each user has independent conversation history — switching channels starts a fresh context.

---

## Long replies

Discord has a 2,000-character limit per message. Agent OS automatically splits longer replies into multiple messages, breaking on newlines where possible to preserve Markdown formatting.

---

## Approval flow

When the Comms agent asks for confirmation before sending an email or creating a calendar event, just reply with any of the confirmation words in the same channel:

```
confirm  /  yes  /  approve  /  ok  /  sure  /  proceed  /  go ahead
```

---

## Smoke test

Run the manual smoke test to verify the full flow end-to-end:

```bash
DISCORD_BOT_TOKEN=<token> \
DISCORD_CHANNEL_ID=<channel-id> \
./scripts/test_discord.sh
```

The script:
1. Validates the bot token against the Discord API
2. Starts the Agent OS server
3. Sends a research question and waits for a reply
4. Sends a draft-email request and waits for the approval prompt
5. Sends SIGTERM and confirms the bot disconnects cleanly

---

## Troubleshooting

| Symptom | Likely cause | Fix |
|---|---|---|
| Bot does not come online | Invalid token | Re-generate the token in the developer portal |
| Bot online but does not reply | Message Content Intent disabled | Enable it in the **Bot** tab → Privileged Gateway Intents |
| Bot replies in server but not DMs | `DISCORD_GUILD_ID` set and filtering DMs | Leave `DISCORD_GUILD_ID` empty, or ensure the fix described above is applied |
| Can't find bot in Discord search | No mutual server | Invite the bot to a server first, then DM it from there |
| Replies are split across messages | Normal behaviour for responses > 2,000 chars | No action needed |
