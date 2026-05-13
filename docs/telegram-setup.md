# Telegram setup

This guide gets Agent OS talking to you over Telegram in about five minutes.

---

## 1. Create a bot with @BotFather

1. Open Telegram and search for **@BotFather**.
2. Send `/newbot` and follow the prompts — choose a name and a username (must end in `bot`).
3. BotFather replies with your **bot token**, e.g. `7123456789:AAH…`
4. Copy it — you'll paste it into `.env` in a moment.

---

## 2. Find your numeric user ID

Agent OS only responds to one user (you). You need your **numeric** Telegram user ID:

1. Search for **@userinfobot** in Telegram.
2. Send it any message — it replies with your user ID, e.g. `Id: 987654321`.

---

## 3. Add the credentials to `.env`

```bash
TELEGRAM_BOT_TOKEN=7123456789:AAH…
TELEGRAM_ALLOWED_USER_ID=987654321
```

Agent OS refuses to start if `TELEGRAM_BOT_TOKEN` is set but `TELEGRAM_ALLOWED_USER_ID` is missing or zero.

---

## 4. Start the server

```bash
make run
```

On startup you should see:

```
INFO  telegram channel started  bot_username=YourBotName  allowed_uid=987654321
```

Open Telegram, find your bot, and send it a message.

---

## Supported message types

| Type | Behaviour |
|---|---|
| Text | Routed directly |
| Photo | Sent to the agent as a base64-encoded image |
| PDF document | Text extracted and sent to the agent |
| Voice | Replied with "not supported yet" |
| All others | Replied with "not supported yet" |

---

## Session isolation

Each unique `(user, chat)` pair gets its own session. DMs and group chats are tracked separately so history doesn't bleed between them.

---

## Reminders over Telegram

The reminder agent (`reminder_set`) works across all channels. Set a reminder from any channel and it will be delivered to you over Telegram when it fires, as long as Agent OS is running and the bot token is configured.

---

## Heartbeat

To receive the heartbeat over Telegram, set:

```bash
HEARTBEAT_CHANNEL=telegram
```

The heartbeat message is delivered to the chat where your `TELEGRAM_ALLOWED_USER_ID` is the user — for bots this is your DM chat.
