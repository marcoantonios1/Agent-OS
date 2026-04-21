# Email Setup Guide

Agent OS supports two email providers out of the box: **Gmail** and **Outlook** (personal Microsoft accounts). Both are configured via environment variables in your `.env` file.

---

## Gmail

### 1. Create OAuth2 credentials

1. Go to [console.cloud.google.com](https://console.cloud.google.com)
2. Create a project → **APIs & Services → Library** → enable **Gmail API** and **Google Calendar API**
3. **OAuth consent screen** → External → fill in app name + your email → add scopes:
   - `https://www.googleapis.com/auth/gmail.readonly`
   - `https://www.googleapis.com/auth/gmail.compose`
   - `https://www.googleapis.com/auth/calendar.readonly`
   - `https://www.googleapis.com/auth/calendar.events`
   - Add your Gmail address as a test user
4. **Credentials → Create Credentials → OAuth client ID** → **Desktop app**
5. Copy the **Client ID** and **Client Secret**

> **Note:** Google's Desktop app type automatically allows `http://127.0.0.1` as a redirect URI, which is what the auth helper uses. No manual redirect URI configuration is needed.

### 2. Get your refresh token

```bash
GOOGLE_CLIENT_ID=<id> GOOGLE_CLIENT_SECRET=<secret> go run ./cmd/tool/googleauth/
```

Open the printed URL, sign in, paste the authorisation code back — your `GOOGLE_REFRESH_TOKEN` is printed. This single token covers both Gmail and Google Calendar.

### 3. Add to `.env`

```env
GOOGLE_CLIENT_ID=your-client-id
GOOGLE_CLIENT_SECRET=your-client-secret
GOOGLE_REFRESH_TOKEN=your-refresh-token
```

### 4. Test

```bash
make test-email
```

---

## Outlook

Outlook uses the **device code flow** — no redirect URIs, no client secret, no copy-pasting codes.

### 1. Register an Azure app

1. Go to [portal.azure.com](https://portal.azure.com) → **Azure Active Directory → App registrations → New registration**
2. Name: `Agent OS` | Supported account types: **Personal Microsoft accounts only**
3. **API permissions → Add → Microsoft Graph → Delegated**:
   - `Mail.Read`
   - `Mail.ReadWrite`
   - `Calendars.Read`
   - `Calendars.ReadWrite`
4. **Authentication → Advanced settings → Allow public client flows → Yes** → Save

Copy the **Application (client) ID** from the Overview page.

### 2. Get your refresh token

```bash
MICROSOFT_CLIENT_ID=<your-app-id> go run ./cmd/tool/microsoftauth/
```

The tool prints a short code (e.g. `ABCD-1234`) and a URL. Open `https://microsoft.com/devicelogin`, enter the code, sign in with `marco_antonios1@outlook.com` — the terminal prints your `MICROSOFT_REFRESH_TOKEN` automatically. This single token covers both Outlook Mail and Outlook Calendar.

### 3. Add to `.env`

```env
MICROSOFT_CLIENT_ID=your-app-id
MICROSOFT_REFRESH_TOKEN=your-refresh-token
```

No client secret needed.

### 4. Test

```bash
make test-email
```

---

## Provider priority

When running, the app picks the first provider with credentials set:

| Priority | Provider | Required env vars |
|---|---|---|
| 1 | Gmail | `GOOGLE_CLIENT_ID` + `GOOGLE_CLIENT_SECRET` + `GOOGLE_REFRESH_TOKEN` |
| 2 | Outlook | `MICROSOFT_CLIENT_ID` + `MICROSOFT_REFRESH_TOKEN` |

To force Outlook when both are configured, comment out the Google vars in `.env`.

---

## What the tools do

| Tool | Description | Sends email? |
|---|---|---|
| `email_list` | Lists recent inbox emails (subject, sender, date, snippet) | No |
| `email_read` | Reads full email by ID | No |
| `email_search` | Searches by query string (e.g. `from:alice subject:budget`) | No |
| `email_draft` | Composes a draft and saves it — does **not** send | No |

Sending is gated behind an explicit approval step and is never triggered autonomously.
