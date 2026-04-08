# Calendar Setup Guide

Agent OS supports two calendar backends: **Google Calendar** and **Outlook Calendar**.
You only need to set up the provider(s) you want to use.

## Provider priority

When multiple providers are configured, the runtime picks the first match:

| Priority | Provider          | Required env vars                                                  |
|----------|-------------------|--------------------------------------------------------------------|
| 1        | Outlook Calendar  | `OUTLOOK_CAL_CLIENT_ID` + `OUTLOOK_CAL_REFRESH_TOKEN`             |
| 2        | Google Calendar   | `GOOGLE_CAL_CLIENT_ID` + `GOOGLE_CAL_CLIENT_SECRET` + `GOOGLE_CAL_REFRESH_TOKEN` |
| fallback | Stub (no live API) | _(none set)_                                                      |

---

## Google Calendar

### 1. Create an OAuth2 client in Google Cloud Console

1. Go to [console.cloud.google.com](https://console.cloud.google.com) and select or create a project.
2. Enable the **Google Calendar API** under *APIs & Services → Library*.
3. Go to *APIs & Services → Credentials → Create Credentials → OAuth client ID*.
4. Choose **Desktop app** as the application type.
5. Copy the **Client ID** and **Client secret**.

### 2. Run the auth helper

```bash
GOOGLE_CAL_CLIENT_ID=<your-client-id> \
GOOGLE_CAL_CLIENT_SECRET=<your-client-secret> \
go run ./cmd/googlecalauth/
```

Follow the on-screen instructions:
- Open the URL in your browser.
- Sign in with your Google account and click **Allow**.
- Copy the authorisation code and paste it in the terminal.

The helper prints three lines — add them to your `.env` file:

```
GOOGLE_CAL_CLIENT_ID=<your-client-id>
GOOGLE_CAL_CLIENT_SECRET=<your-client-secret>
GOOGLE_CAL_REFRESH_TOKEN=<printed-token>
```

### 3. Verify

```bash
make test-calendar
```

The output should show `Provider: Google Calendar (live)`.

---

## Outlook Calendar

Outlook uses the **device code flow** — no redirect URIs or local servers needed.
This works with personal Microsoft accounts (`@outlook.com`, `@hotmail.com`).

### 1. Register an app in Azure

1. Go to [portal.azure.com](https://portal.azure.com) → *Azure Active Directory → App registrations → New registration*.
2. Name the app (e.g. `Agent OS Calendar`).
3. Under *Supported account types*, select **Personal Microsoft accounts only**.
4. Leave the redirect URI blank.
5. Copy the **Application (client) ID**.
6. Under *Authentication*, enable **Allow public client flows** → Yes.

### 2. Run the auth helper

```bash
OUTLOOK_CAL_CLIENT_ID=<your-client-id> go run ./cmd/outlookcalauth/
```

Follow the on-screen instructions:
- Open `https://microsoft.com/devicelogin` in your browser.
- Enter the short code shown in the terminal.
- Sign in with your Microsoft account and click **Accept**.

The helper prints two lines — add them to your `.env` file:

```
OUTLOOK_CAL_CLIENT_ID=<your-client-id>
OUTLOOK_CAL_REFRESH_TOKEN=<printed-token>
```

No client secret is needed — the device code flow works without one.

### 3. Verify

```bash
make test-calendar
```

The output should show `Provider: Outlook Calendar (live)`.

---

## Shared notes

- Refresh tokens are long-lived but can expire if unused for 90 days (Microsoft) or if you revoke access.
- Re-run the relevant auth helper to obtain a fresh token.
- The `calendar_create` tool requires explicit `approved: true` in the tool input — the agent must confirm with you before creating any event.
- Keep your `.env` file out of version control (it is already in `.gitignore`).
