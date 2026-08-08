# Troubleshooting

Run `yc doctor` first. It is designed to report setup problems without printing
secrets, and thirteen of its fifteen checks work with no network at all.

If your question is "why does it work this way" rather than "why is it broken",
[faq.md](faq.md) is the other half of this page.

## `no YouTube credentials configured`

`yc chat` refuses **before opening a socket** and exits `2`. Pick a credential
source:

```sh
# read-only, no OAuth, no consent screen
export YC_YOUTUBE_API_KEY="AIza…"
go run ./cmd/yc chat --video dQw4w9WgXcQ

# full access
export YC_GOOGLE_CLIENT_ID="…apps.googleusercontent.com"
go run ./cmd/yc login
go run ./cmd/yc chat --channel @somechannel

# no credentials at all
go run ./cmd/yc chat --mock
```

See [register-google-app.md](register-google-app.md) to create either one.

## `yc login` succeeded but chat still fails with the old token

Environment and config values take precedence over saved credentials. `yc`
reports this explicitly:

```text
Saved login credentials are present but were not used because an environment or
config token takes precedence; unset YC_GOOGLE_ACCESS_TOKEN/GOOGLE_ACCESS_TOKEN
or remove google_access_token from config.toml, then retry.
```

`yc doctor` shows the same state as a warning on the credential-file line
(`… loaded but shadowed by an environment or config token`).

## `YouTube Data API v3 has not been used in project … or it is disabled`

Enable the API in the Google Cloud console under **APIs & Services → Library**,
in the same project the credential belongs to. `yc` reports the underlying
`SERVICE_DISABLED` as "not permitted".

## The composer is visible but says it cannot send

That is by design — `yc` disables controls with a reason instead of hiding them.
The reason names the fix:

| Reason | Fix |
| --- | --- |
| `API-key mode is read-only; run 'yc login' to send messages` | Log in. An API key can never send. |
| `missing OAuth scope …/youtube.force-ssl` | `yc login` again and approve it. `--read-only` requests only `readonly`. |
| `granted scopes are unknown for a token supplied through the environment or config file` | Not an error — `yc` is being optimistic. The API is the authority. Run `yc doctor`. |

## Pressing `d`, `t`, or `b` says moderation is unavailable

The keys stay bound even when they cannot be used, and the status bar names the
specific reason. Each one has a different fix:

| Status line | Fix |
| --- | --- |
| `no chat source` / `this chat source cannot moderate` | You are in `--mock` or a fake source. Moderation needs a live session. |
| `this session has no credential that can moderate; run yc login and grant youtube.force-ssl` | You are in API-key mode, or no credential was wired. An API key can never moderate. |
| `no chat open` | Open a chat first. |
| `moderation needs the youtube.force-ssl scope; run yc login again and approve it` | You logged in with `--read-only`. Re-run `yc login` and approve both scopes. |
| `you are not a moderator of this chat` | The signed-in channel has spoken in this chat and YouTube reported it as neither the owner nor a moderator. Get moderator status on the channel. |

And the refusals that are about the **selected message** rather than the
credential:

| Status line | Means |
| --- | --- |
| `select a message with j/k first` | Nothing is selected. |
| `that message is already removed` | The row is a tombstone or an earlier delete. |
| `waiting for YouTube to confirm that message before it can be deleted` | It is still a local echo of your own send and has no confirmed ID yet. |
| `no channel id for that author, so they cannot be banned` | The API sent no `channelId` for that author. |
| `that is your own channel` | You cannot ban yourself. |
| `this chat is not resolved yet, so bans have nowhere to go` | The `liveChatId` has not resolved. Wait, or press `ctrl+r`. |

If the confirmation prompt ends with `(your role in this chat is unknown; YouTube
decides)` or `(granted scopes unknown; YouTube decides)`, that is **not** an
error. `yc` is being optimistic on purpose — YouTube reports moderator status
only for authors who have spoken, and a token from the environment carries no
record of its scopes. The action will be attempted and the API is the authority.
See [moderation.md](moderation.md).

## A moderation action failed

```text
could not permanently ban Bob: <reason> (nothing was removed)
```

The `(nothing was removed)` is literal: `yc` blanks the rows optimistically the
moment you confirm, and a failure restores every one of them exactly. Your view
matches the broadcast again.

Common reasons behind that message: the credential lacks `force-ssl`; the account
is not actually a moderator of that chat; the message was already deleted by
somebody else; or the daily quota is gone. Each moderation call costs an
estimated 50 units — see [quota.md](quota.md).

There is no undo for a **successful** action. Lift a ban from YouTube Studio;
`yc` deliberately does not bind a key to `Unban`, because `liveChatBans` has no
`list` method and a ban ID is only knowable within the session that created it.

## Chat stopped and the status bar says `PAUSED`

The quota reserve tripped, or the daily allowance is gone.

```text
quota reserve reached (est.); sends still available, polling paused until 00:00 PDT.
Press ctrl+r to override
```

This is deliberate: reads stop so that sending and moderation still work. Press
`ctrl+r` to override and spend the rest on reading, or wait for the midnight
Pacific reset. `yc quota` shows exactly where the units went. Read
[quota.md](quota.md) before the next stream.

## `STRETCHED` in the status bar

Not a fault. It means the budget floor exceeded YouTube's advised cadence and
`yc` is deliberately polling slower than allowed so the day lasts. If you have a
raised project quota, `--follow-server-cadence` removes the budget floor. If you
only care about tonight, `--session-hours 4` narrows the horizon.

## Quota exhausted much sooner than expected

Likely causes, in order:

1. Polling at the server-advised cadence for hours — that is ~2.8 hours of
   budget. Let `yc` stretch, or use `--session-hours`.
2. Another client on the same Google Cloud project. The allowance is per project,
   not per machine, and `yc`'s ledger only counts calls `yc` made.
3. A wrong cost estimate. Google publishes no live-chat costs; compare the Cloud
   Console's usage graph against `yc quota` and correct `quota_cost_list`.
4. Repeated restarts. Each new session spends one resolution unit per chat unless
   you use `--live-chat-id`.

## `no active broadcast for this channel`

`channels.list` answered but the channel is not live, or its current broadcast is
not reported there. Pass the video ID or the watch URL directly:

```sh
yc chat --video dQw4w9WgXcQ
yc chat --chat 'https://www.youtube.com/watch?v=dQw4w9WgXcQ'
```

The `search.list` fallback that would find the broadcast from a channel alone is
implemented in the transport but **not wired up**, so there is no opt-in prompt
to accept yet.

## `this video has no active live chat`

The video is not a broadcast, or its chat has already ended. `liveChatId` only
exists while `liveStreamingDetails.activeLiveChatId` is populated. A VOD of a
past stream does not have one.

## The chat ends and `yc` does not reconnect

Terminal conditions — chat ended, chat disabled, chat not found, quota exhausted,
invalid credentials, insufficient permissions — **park with the streams open**
rather than closing them. Retrying an ended chat spends quota to be told the same
thing again, so reopening is your decision: press `ctrl+r`.

A broadcast going *offline* is different: chat outlives the stream, so `yc` keeps
polling through a 2-minute grace window and only closes if nothing new arrives.

## Messages appear twice / a Super Chat repeats

They should not. The poller keeps an 8000-ID dedupe ring precisely because a
retained page token, a retry, and a reconnect all re-deliver rows. A `ctrl+r`
reconnect normally restarts the poller **in place**, so the token, the resolved
target, and that ring all survive; only a transport that cannot restart itself is
torn down and rebuilt. If you see a duplicate, that is a bug worth reporting —
include the event kind and whether a reconnect had just happened.

## `dropped=N` in the status bar

The UI could not keep up and the poller discarded rows rather than blocking.
Blocking the emitter would stall the goroutine that owns the poll schedule and
cost the whole session, so the trade is deliberate — but the loss is shown at
every terminal width instead of being silent. A very fast chat on a slow terminal
is the usual cause. Lowering `scrollback_limit` and using `compact` layout both
reduce repaint cost.

## Login does not open a browser, or the callback never arrives

- The listener binds `127.0.0.1` on an ephemeral port. A firewall or a sandbox
  that blocks loopback binds will fail here.
- Headless environments have no browser to open. Run `yc login` on a machine with
  one, then copy the credential file, or export the tokens as environment
  variables.
- Desktop OAuth clients accept loopback redirects on any port, so there is
  normally nothing to register. If you pinned one with `--redirect-uri`, it must
  match exactly — scheme, host (`localhost` and `127.0.0.1` are different), port,
  path, and trailing slash.
- On non-Unix builds, `yc login` stops **before** opening the browser: there is
  no saved-credential backend, so a token would have nowhere to go.

## Login works, then stops after about a week

Refresh tokens issued by an OAuth app in *Testing* status expire after 7 days.
Publish the consent screen in the Google Cloud console, or re-run `yc login`.

## The session ended saying the sign-in expired

```text
videos.list: the sign-in expired and could not be renewed; run `yc login`
to sign in again (<scrubbed reason>)
```

A mid-session 401 is normally invisible: the transport exchanges the token once,
under a client-wide single-flight, and retries the request exactly once. You only
see this message when the **second** attempt was also rejected, which means the
refresh itself failed or produced a token Google still would not accept.

Causes, in order of likelihood:

1. The refresh token expired. An OAuth app in *Testing* status issues refresh
   tokens that die after **7 days**. Publish the consent screen, or re-run
   `yc login`.
2. The grant was revoked — at <https://myaccount.google.com/permissions>, or by
   a `yc logout` elsewhere.
3. There is no refresh token at all. An access token exported into the
   environment without one cannot be renewed; `yc doctor` warns about exactly
   this.
4. Your OAuth client was issued a client secret *and* Google is refusing the
   refresh without it. Set `YC_GOOGLE_CLIENT_SECRET`.

`yc doctor` distinguishes all four without printing anything.

Note what is **not** retried: 403, 429, 5xx, and a 401 on a key-only request. A
refresh cannot fix any of them, so they stay with the poller's backoff ladder.

## Credential file permission errors

On Unix the credential directory must be exactly `0700` and the file exactly
`0600`. Symlinks, directories, group- or world-accessible files, and files with
special mode bits are **rejected rather than repaired**. Fix the permissions or
move the unsafe file aside, then run `yc login` again:

```sh
chmod 700 ~/.config/yc
chmod 600 ~/.config/yc/credentials.json
```

## `saved credentials are not supported on this platform`

Non-Unix builds have no credential-file backend. Use environment variables or a
private flat config file there. `yc doctor` reports it as a warning and
`yc login` refuses before starting OAuth.

## Avatars, badges, and emoji render as text

Expected, and it is the only rendering mode. The live chat API supplies no badge
imagery and no per-message emote metadata, so there is nothing faithful to
render an image from. `yc` has no image decode, download, cache, or
terminal-graphics path at all. Authors show an `[XY]` initials chip (or nothing
with `avatar_mode = "off"`), badges show glyphs or compact labels, and emoji show
the native Unicode glyph. The full reasoning is in [faq.md](faq.md) and
[adr/0005-render-everything-as-text.md](adr/0005-render-everything-as-text.md).

## The quota tab is empty in mock mode

Correct. Mock and fake chat sources perform no API calls, so there is nothing to
account for, and the tab says so rather than showing zeros that look like a
working meter.

## Docker cannot reach credentials

Pass them at runtime. Never bake them into the image:

```sh
docker run --rm -it -e YC_YOUTUBE_API_KEY yc:local chat --video dQw4w9WgXcQ
```

The container runs as UID/GID `10001`, so a bind-mounted config directory must be
writable by that account. If Docker itself fails before the container starts,
check daemon access — Podman-equivalent checks are useful locally but do not
replace Docker evidence.

## Mock mode works but live mode fails

Mock uses no credentials, no network, and no quota. If mock works and live does
not, the difference is credentials, scopes, project configuration, quota, or
network. `yc doctor` separates those four:

```sh
yc doctor
```

## Debug logs

Enable them only when you need diagnostics:

```sh
yc chat --video dQw4w9WgXcQ --debug-log
yc doctor --debug-log
```

Logs redact every known credential shape, but they can still contain video IDs,
live chat IDs, channel names, display names, hostnames, counts, and timing.
Review a log before sharing it.
