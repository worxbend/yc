# Register Your Own Google Cloud Project

`yc` ships with **no built-in Google application and no API key**. It is designed
to run against *your own* Google Cloud project, so your quota, your consent
screen, and your token requests are yours and are never shared through a
vendor-hosted client.

That also means the 10,000-unit daily allowance in [quota.md](quota.md) is
*your* project's, and nobody else can spend it.

This guide covers creating that project and the **minimum configuration needed
to start**. For the credential model and precedence rules, read
[auth.md](auth.md) and [config.md](config.md); for symptoms and fixes, read
[troubleshooting.md](troubleshooting.md).

## Do You Even Need A Project?

Pick the smallest path that covers what you want to do:

| Goal | Needs | Minimum to start |
| --- | --- | --- |
| Try the UI, no Google account | nothing | `yc chat --mock` |
| **Read** a public live chat | project + **API key** | `YC_YOUTUBE_API_KEY` + a chat target |
| **Send** messages | project + **OAuth Desktop client** + `yc login` | `YC_GOOGLE_CLIENT_ID` |
| **Moderate** (delete / timeout / ban) | the same, **plus** owning or moderating that chat on YouTube | `YC_GOOGLE_CLIENT_ID` |

Moderation needs nothing extra from the Google Cloud console beyond the OAuth
client — the `youtube.force-ssl` scope that `yc login` already requests covers
it. What it additionally needs is a fact about YouTube, not about the project:
the signed-in channel must be the broadcast's owner or one of its moderators.
See [moderation.md](moderation.md).

The API-key path is genuinely first class in `yc`, not a degraded mode. It needs
no OAuth consent screen, no verification, no browser round trip, and no test
users — roughly two minutes of console work. Take it first if you only want to
read.

## 1. Create The Project

1. Sign in at <https://console.cloud.google.com/>.
2. Open the project picker in the top bar and click **New Project**.
3. Name it anything (`yc-<your-name>` is fine). No organization is required for
   a personal Google account.
4. Click **Create**, then make sure the new project is selected in the picker.

## 2. Enable YouTube Data API v3

Nothing works until this is on — the API answers `SERVICE_DISABLED` otherwise,
which `yc` reports as "not permitted".

1. Go to **APIs & Services → Library**.
2. Search for **YouTube Data API v3**.
3. Open it and click **Enable**.

You can confirm the daily allowance afterwards under **APIs & Services → Quotas**,
where the default is 10,000 units per day for **Queries per day**, plus a
separate 100-per-day allowance for search calls.

## 3a. The Read-Only Path: Create An API Key

1. Go to **APIs & Services → Credentials**.
2. Click **Create Credentials → API key**.
3. Copy the key. It starts with `AIza`.
4. Click **Edit API key** and restrict it — this is worth the extra minute:
   - **API restrictions → Restrict key →** YouTube Data API v3.
   - **Application restrictions →** leave as *None*. `yc` runs from a terminal,
     so there is no referrer, IP, or app package to bind to. Do not add an IP
     restriction unless you have a static one.

Give it to `yc`:

```sh
export YC_YOUTUBE_API_KEY="AIza…"
yc chat --video dQw4w9WgXcQ
```

An API key is an accepted caller identity for `liveChatMessages.list` on a
**public** live chat, and for nothing else `yc` does. In key-only mode `yc`:

- reads public live chats, with titles and viewer counts;
- **cannot** send, delete, or ban;
- **cannot** resolve your own channel or read your subscriptions.

The composer stays visible and says exactly that instead of disappearing.

> **Treat the key as a secret.** It is a bearer credential in a query string.
> `yc` redacts it from `yc config show`, `yc doctor`, debug logs, and error
> messages — including from `net/http` errors, which echo the request URL — but
> you are responsible for how you store it.

## 3b. The Full Path: Create An OAuth Desktop Client

Needed for `yc login`, for sending, and for moderation.

### Configure the consent screen (once)

1. Go to **APIs & Services → OAuth consent screen**.
2. Choose **External** unless you have a Google Workspace organization, then
   **Create**.
3. Fill in the app name, your user support email, and your developer contact
   email. Nothing else is required.
4. On **Scopes**, you may add
   `https://www.googleapis.com/auth/youtube.readonly` and
   `https://www.googleapis.com/auth/youtube.force-ssl`. `yc` requests them at
   authorization time regardless, so this step is documentation rather than
   enforcement.
5. On **Test users**, **add your own Google account**. This matters: while the
   app is in *Testing* status, only listed test users can authorize it, and
   refresh tokens issued to a testing app **expire after 7 days**. If you want
   tokens that last, publish the app (**Publish app** → *In production*).
   Publishing a project that only you use does not require Google verification
   as long as you are not requesting sensitive scopes for other people's data —
   but Google's verification policy is theirs to change, so check the console's
   own wording.

### Create the client

1. Go to **APIs & Services → Credentials**.
2. Click **Create Credentials → OAuth client ID**.
3. **Application type → Desktop app**. This is the one that matters. `yc` runs
   the installed-app flow with a loopback redirect; a *Web application* client
   would require a registered redirect URI and would reject the ephemeral port
   `yc` binds.
4. Name it and click **Create**.
5. Copy the **Client ID**. A Desktop client may also show a **Client secret** —
   see below.

### About the client secret

**It is optional.** An installed application cannot keep a secret, which is
exactly why the flow uses **PKCE S256** instead. `yc` authenticates with the code
verifier and does not need the secret.

Set `YC_GOOGLE_CLIENT_SECRET` only if your OAuth client was issued one *and*
Google rejects your refresh without it. Everything works without it in the normal
case, and one fewer secret on disk is one fewer secret to leak.

## 4. Give The Credentials To `yc`

Environment variables (canonical `YC_`-prefixed names, and unprefixed aliases,
both work):

```sh
export YC_GOOGLE_CLIENT_ID="…apps.googleusercontent.com"
# optional:
export YC_GOOGLE_CLIENT_SECRET="…"
# aliases: GOOGLE_CLIENT_ID / GOOGLE_CLIENT_SECRET
```

Or a private flat config file (find its path with `yc config path`). The client
ID is safe to store there; keep the secret and the API key in the environment or
the credential file instead:

```toml
google_client_id = "…apps.googleusercontent.com"
```

> `yc setup` writes non-secret values (client ID, your channel ID, default
> chats, theme, layout, avatar, mouse, animation) for you and can then hand off
> to login. It never writes the client secret, tokens, an authorization code,
> OAuth state, or the API key.

## 5. Log In

```sh
yc login
```

`yc` binds a loopback listener on `127.0.0.1` on an **ephemeral port**, opens
your browser to Google's consent prompt with PKCE and `access_type=offline`,
waits for the callback, exchanges the code, validates the token, and saves it
through the private credential store — **without printing it**. It requests:

| Scope | Enables |
| --- | --- |
| `https://www.googleapis.com/auth/youtube.readonly` | read live chat, broadcast metadata, your own channel, your subscriptions |
| `https://www.googleapis.com/auth/youtube.force-ssl` | send chat messages, delete messages, manage live chat bans |

`force-ssl` is the narrowest scope covering every write `yc` performs. `yc` never
requests the broad `.../auth/youtube` scope, though it honors one when Google
reports it as granted for a credential minted elsewhere.

Variations:

```sh
yc login --read-only              # readonly only; chat becomes read-only
yc login --dry-run                # explains everything, contacts nothing
yc login --write-default-config   # write a starter config.toml first, if none exists
yc login --timeout 2m             # bound the browser round trip
yc logout                         # delete saved credentials and revoke remotely
yc logout --keep-remote           # delete locally, leave the grant in place
```

> **Redirect URIs.** Desktop clients accept loopback redirects on any port, so
> there is nothing to register. `--redirect-uri` exists for the unusual case
> where you must pin a specific loopback address and port; the default of "bind
> 127.0.0.1 on whatever port is free" is what the installed-app flow wants.

> **Status: credentialed.** The login flow is implemented and unit-tested against
> fakes and `httptest`, and has **never been run against Google**. `--dry-run` is
> the only part covered by a credential-free smoke. See
> [manual-validation.md](manual-validation.md).

## 6. Start Chatting

```sh
yc chat --channel @somechannel
yc chat --video dQw4w9WgXcQ
yc doctor
```

`doctor` reports the effective config path, the credential store, the credential
mode and what it can do, the granted scopes when `yc` knows them, your resolved
channel, and the quota budget — without printing a token, a secret, or a key.

## Minimal Configurations, Summarized

**Mock — zero credentials, zero quota:**

```sh
yc chat --mock
```

**Read a public chat with an API key (no OAuth, no consent screen):**

```sh
export YC_YOUTUBE_API_KEY="AIza…"
yc chat --video dQw4w9WgXcQ
```

**Full experience with your own OAuth client (recommended):**

```sh
export YC_GOOGLE_CLIENT_ID="…apps.googleusercontent.com"
yc login
yc chat --channel @somechannel
```

## Credential Environment Variables Reference

| Purpose | Canonical | Alias |
| --- | --- | --- |
| OAuth client ID | `YC_GOOGLE_CLIENT_ID` | `GOOGLE_CLIENT_ID` |
| OAuth client secret (optional) | `YC_GOOGLE_CLIENT_SECRET` | `GOOGLE_CLIENT_SECRET` |
| Access token | `YC_GOOGLE_ACCESS_TOKEN` | `GOOGLE_ACCESS_TOKEN` |
| Refresh token | `YC_GOOGLE_REFRESH_TOKEN` | `GOOGLE_REFRESH_TOKEN` |
| Loopback redirect override | `YC_GOOGLE_REDIRECT_URL` | `GOOGLE_REDIRECT_URL` |
| API key | `YC_YOUTUBE_API_KEY` | — |
| Your own channel ID | `YC_YOUTUBE_CHANNEL_ID` | — |
| Default chats | `YC_DEFAULT_CHATS` | — |

When both a canonical `YC_`-prefixed name and its alias are set, the canonical
name wins. An empty value is ignored entirely rather than treated as "set to
empty", so a blank CI variable cannot mask a configured file value.

Never commit shell profiles, `.env` files, config files, credential files, or
logs that contain these values.

## Common Console Problems

**"YouTube Data API v3 has not been used in project … before or it is
disabled."** Step 2 was skipped, or you enabled it in a different project than
the credential belongs to.

**`API_KEY_INVALID` / `API_KEY_SERVICE_BLOCKED`.** The key is wrong, or its API
restriction does not include YouTube Data API v3.

**Consent screen says the app is unverified.** Expected while the project is in
*Testing*. Add yourself as a test user and continue past the warning, or publish
the app.

**Login works, then stops after a week.** Refresh tokens issued by an app in
*Testing* status expire after 7 days. Publish the app, or re-run `yc login`.

**Quota exhausted far sooner than expected.** Read [quota.md](quota.md). The
usual cause is polling at the server-advised cadence for a long stream; the
usual fix is letting `yc` stretch, or `--session-hours`.

**Everything is configured and `d`/`t`/`b` still refuse.** That is a YouTube
permission, not a console setting: the signed-in channel must own or moderate
that specific chat. [moderation.md](moderation.md) lists every refusal and its
fix, and [faq.md](faq.md) answers the questions this page raises.
