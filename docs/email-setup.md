# Email Setup Guide

Agent OS supports two email providers out of the box: **Gmail** and **Outlook** (personal Microsoft accounts). Both are configured via environment variables in your `.env` file.

---

## Gmail (antoniosm384@gmail.com)

### 1. Create OAuth2 credentials

1. Go to [console.cloud.google.com](https://console.cloud.google.com)
2. Create a project → **APIs & Services → Library** → enable **Gmail API**
3. **OAuth consent screen** → External → fill in app name + your email → add scopes:
   - `https://www.googleapis.com/auth/gmail.readonly`
   - `https://www.googleapis.com/auth/gmail.compose`
   - Add your Gmail address as a test user
4. **Credentials → Create Credentials → OAuth client ID** → Desktop app
5. Copy the **Client ID** and **Client Secret**

### 2. Get your refresh token

```bash
GMAIL_CLIENT_ID=<id> GMAIL_CLIENT_SECRET=<secret> go run ./cmd/gmailauth/
```

Open the printed URL, sign in, paste the authorisation code back — your `GMAIL_REFRESH_TOKEN` is printed.

### 3. Add to `.env`

```env
GMAIL_CLIENT_ID=your-client-id
GMAIL_CLIENT_SECRET=your-client-secret
GMAIL_REFRESH_TOKEN=your-refresh-token
```

### 4. Test

```bash
make test-email
```

---

## Outlook (marco_antonios1@outlook.com)

Outlook uses the **device code flow** — no redirect URIs, no client secret, no copy-pasting codes.

### 1. Register an Azure app

1. Go to [portal.azure.com](https://portal.azure.com) → **Azure Active Directory → App registrations → New registration**
2. Name: `Agent OS` | Supported account types: **Personal Microsoft accounts only**
3. **API permissions → Add → Microsoft Graph → Delegated**:
   - `Mail.Read`
   - `Mail.ReadWrite`
4. **Authentication → Advanced settings → Allow public client flows → Yes** → Save

Copy the **Application (client) ID** from the Overview page.

### 2. Get your refresh token

```bash
OUTLOOK_CLIENT_ID=<your-app-id> go run ./cmd/outlookauth/
```

The tool prints a short code (e.g. `ABCD-1234`) and a URL. Open `https://microsoft.com/devicelogin`, enter the code, sign in with `marco_antonios1@outlook.com` — the terminal prints your `OUTLOOK_REFRESH_TOKEN` automatically.

### 3. Add to `.env`

```env
OUTLOOK_CLIENT_ID=your-app-id
OUTLOOK_REFRESH_TOKEN=your-refresh-token
```

No client secret needed.

### 4. Test

```bash
make test-email
```

---

## Provider priority

When running `make test-email`, the harness picks the provider in this order:

| Priority | Provider | Required env vars |
|---|---|---|
| 1 | Outlook (live) | `OUTLOOK_CLIENT_ID` + `OUTLOOK_REFRESH_TOKEN` |
| 2 | Gmail (live) | `GMAIL_CLIENT_ID` + `GMAIL_CLIENT_SECRET` + `GMAIL_REFRESH_TOKEN` |
| 3 | Stub (fake data) | none |

To force Gmail when both are configured, comment out the Outlook vars in `.env`.

---

## What the tools do

| Tool | Description | Sends email? |
|---|---|---|
| `email_list` | Lists recent inbox emails (subject, sender, date, snippet) | No |
| `email_read` | Reads full email by ID | No |
| `email_search` | Searches by query string (e.g. `from:alice subject:budget`) | No |
| `email_draft` | Composes a draft and saves it — does **not** send | No |

Sending is gated behind an explicit approval step (Issue #11) and is never triggered autonomously.
