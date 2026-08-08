# FAQ

Real questions, answered against the code. Where an answer is a guess, it says
so.

For symptoms with a fix attached, read [troubleshooting.md](troubleshooting.md).
For the numbers, read [quota.md](quota.md).

## Why does `yc` poll instead of using a WebSocket?

Because there is no socket to use.

YouTube exposes no chat socket a plain client can hold open. There is no
WebSocket, no webhook, and no PubSub delivery for live chat anywhere in the
YouTube Live Streaming API. The documented way to read live chat is
`liveChatMessages.list`: a paged REST endpoint that returns a batch of messages,
a `nextPageToken`, and a `pollingIntervalMillis` telling you how long to wait
before asking again.

`liveChatMessages.streamList` is documented as a low-latency server-streaming
alternative, but it is **absent from the REST discovery document** and is not
reachable from a plain REST client today. Building on it would mean building on
something we cannot call.

So there is exactly one transport, and the interesting question is not *whether*
to poll but *how often* — which is the whole of [quota.md](quota.md).

This is the single largest difference from `twi`, the author's Twitch client,
which owns an IRC socket that pushes. `yc` owns a loop that pulls, on a schedule
it has to justify.

Full reasoning: [adr/0001-poll-live-chat-messages-list.md](adr/0001-poll-live-chat-messages-list.md).

## Why does quota matter so much? It is just a chat client.

Because the allowance is small, shared, and does not come back until midnight
Pacific.

A Google Cloud project gets **10,000 units per day** by default. `yc` estimates
`liveChatMessages.list` at 5 units, which is 2,000 polls — one every **~43
seconds** if you want the day to last until the reset. YouTube's own
`pollingIntervalMillis` typically advises **~5 seconds**, which exhausts the
entire day in **under three hours**.

The server's advice and your budget therefore disagree by roughly an order of
magnitude, and reconciling them is the client's job. A client that simply obeys
`pollingIntervalMillis` dies mid-stream; a client that ignores it violates the
API's terms.

So `yc` meters every call it makes, budgets the remaining units across the
remaining time, and deliberately polls slower than allowed when it has to. The
status bar says `STRETCHED` when that is happening. That is the feature, not a
fault.

Two more reasons it is not merely a performance concern:

- The allowance is **per project, not per machine**. Another client, a script, or
  a second `yc` on the same credentials spends the same pool.
- It is **non-replenishing until the reset**. A bug that burns it is a
  denial-of-service against your own project, which is why
  [../SECURITY.md](../SECURITY.md) treats runaway polling as a security issue.

## Why are there no images, avatars, or emotes?

Because the data is not there.

`liveChatMessages.list` returns, per message: a `profileImageUrl`, four author
booleans, and text. There is **no badge imagery** — the role is four booleans,
not an image set. There is **no per-message emote metadata at all**: YouTube
channel emotes arrive inside the message text as `:shortcode:`, and the API
supplies no position map, no image URL, and no emote ID. Standard emoji arrive as
ordinary Unicode.

So the only asset with an image behind it is the avatar. Rendering it would mean
an HTTP download per author, an on-disk image cache, decode and resize, a
terminal capability probe, a graphics protocol implementation, placeholder cells
that reserve stable width so a late arrival does not reflow your scrollback — and
a fallback to text anyway. All of that for a one-to-two-cell thumbnail, on a
client whose defining constraint is that it must not spend resources it does not
have to.

What you get instead:

- **Avatars** — an `[XY]` initials chip in that author's stable color, or nothing
  with `avatar_mode = "off"`.
- **Badges** — a single-cell Unicode glyph (`◉` owner, `⚔` moderator, `★` member,
  `✓` verified) or a compact `[mod]`-style label, or nothing.
- **Emoji** — the native Unicode glyph, optionally on a tinted chip.
- **Channel emotes** — the literal `:shortcode:` as a fixed-width token chip,
  because that is genuinely all the API sends.

There is no image decode, download, cache, terminal capability probe, or graphics
protocol path anywhere in `yc`. This is **out of scope**, not "planned".

Full reasoning: [adr/0005-render-everything-as-text.md](adr/0005-render-everything-as-text.md).

## Can I use just an API key?

Yes, and it is a genuinely first-class path — not a degraded mode.

An API key is an accepted caller identity for `liveChatMessages.list` on a
**public** live chat. It takes about two minutes of console work: no OAuth
consent screen, no verification, no browser round trip, no test users.

```sh
export YC_YOUTUBE_API_KEY="AIza…"
yc chat --video dQw4w9WgXcQ
```

In key-only mode you **can** read public live chats, with titles and viewer
counts. You **cannot** send, delete, ban, resolve your own channel, or read your
subscriptions — the API accepts a key for none of those.

The composer stays visible and says exactly that, because a disabled control that
explains itself teaches you what to fix and a missing one does not:

```text
API-key mode is read-only; run `yc login` to send messages
```

Setup: [register-google-app.md](register-google-app.md). Model:
[auth.md](auth.md).

## Do I have to create my own Google Cloud project?

Yes. `yc` ships with **no built-in Google application and no API key**.

That is deliberate: your quota, your consent screen, and your token requests stay
yours, and nobody else can spend the 10,000 units you were allocated. A shared
vendor-hosted client would mean a shared quota pool and a shared blast radius.

## Why is my chat not found?

Four different failures produce something that looks like "not found", and `yc`
distinguishes them:

| Message | Means |
| --- | --- |
| `this video has no active live chat` | The video is not a broadcast, or its chat has ended. `liveChatId` only exists while `liveStreamingDetails.activeLiveChatId` is populated — a VOD of a past stream does not have one. |
| `no active broadcast for this channel` | `channels.list` answered, but the channel is not live, or its current broadcast is not reported there. Pass the video ID or the watch URL directly. |
| `chat disabled` | The broadcaster turned live chat off. |
| `chat ended` | The chat existed and is now closed. History stays on screen. |

The most common cause is passing a channel when you meant a video. `yc` resolves
a channel to its *current* broadcast through `channels.list`, and that field is
not always populated even for a channel that is live. The `search.list` fallback
that would find it is implemented in the transport and **not wired up**, because
`search.list` spends from a separate 100-call-per-day bucket and should not be
reached without explicit consent.

The reliable form is the watch URL or the video ID:

```sh
yc chat --video dQw4w9WgXcQ
yc chat --chat 'https://www.youtube.com/watch?v=dQw4w9WgXcQ'
```

If you already know the live chat ID, `--live-chat-id` skips resolution entirely
and costs **zero** quota.

## What happens when the quota runs out?

Two different things, at two different thresholds.

**At the reserve (10% remaining by default), polling pauses:**

```text
quota reserve reached (est.); sends still available, polling paused until 00:00 PDT.
Press ctrl+r to override
```

Reads stop **so that sending and moderation still work**. Your history stays on
screen; the session does not end. `ctrl+r` overrides and spends the rest on
reading.

**At zero, the API answers `quotaExceeded` and `yc` stops and never retries.**
Every attempt is charged, and the allowance does not come back until the reset —
so retrying spends units to be told the same thing again. The session parks with
its streams open rather than closing them; reopening is your decision, taken with
`ctrl+r`.

The reset is **the next calendar midnight in `America/Los_Angeles`**, constructed
as a real calendar boundary rather than "now + 24 hours", so a DST change moves it
by an hour instead of by a day.

Before the next stream, `yc quota` shows exactly where the units went, per
endpoint.

## Does it work for unlisted or members-only chats?

**Members-only chats:** only with OAuth, and only if the signed-in account is
actually a member (or the owner or a moderator). An API key carries no account
identity, so it cannot be a member of anything.

**Unlisted broadcasts:** `yc` does not special-case them. It passes the video ID
to `videos.list` and the resulting `liveChatId` to `liveChatMessages.list`, and
whether Google answers depends on Google's own access rules for that video and
that credential. **Neither outcome has been verified against the live API** — no
credentialed YouTube path in `yc` ever has. If it works for you, it is because
Google allowed it, not because `yc` did anything special.

**Private broadcasts:** the same, with OAuth and an account that has access.

The honest summary is the capability table in [auth.md](auth.md): an API key
reads public live chat and nothing else; OAuth does whatever the signed-in
account is allowed to do.

## Why does everything say "estimated"?

Because Google publishes no per-method quota cost for **any** live-chat method.

The published cost table lists none of them, and the reference pages for
`liveChatMessages.list`, `liveChatMessages.insert`, and `liveChatBans.insert`
carry no *Quota impact* line at all. What Google does publish is a rule of thumb:
a list read "usually costs 1 unit", a write "usually costs 50 units". All four
live-chat writes `yc` performs fit the write rule exactly. The read is the
outlier — `yc` uses **5**, and it is the one figure the entire budget rests on.

Everything else `yc` calls (`videos.list`, `channels.list`,
`subscriptions.list`, `videoCategories.list`, `videos.update`, `search.list`) has
a **published** cost and `yc` matches it.

So the cost table is **data, not constants**: a corrected figure is a config line
rather than a release.

```toml
quota_cost_list = 1   # if your Console usage graph says otherwise
```

Compare `yc quota` against the Cloud Console's usage graph over a session — that
is the measurement that would settle it, and it has not been made.

## Can I open more than one chat at once?

Yes. Chats are opened with repeated `--chat` flags, a comma list, `default_chats`
in config, or `space c` at runtime.

```sh
yc chat --chat A --chat B
yc chat --chats A,B
```

Each chat keeps its own history, unread count, composer draft, reply context,
scroll position, and filters; `[` and `]` switch between them and `space e` opens
the sidebar. Chats opened at runtime are session-only and are not written back to
config.

Be aware that **each open chat is its own poll loop and its own quota spend**.
Two chats halve the wall-clock time your allowance lasts, and `yc` budgets
accordingly.

## Does it run on macOS or Windows?

You can build from source on any platform Go supports, but:

- Published release binaries are **`linux/amd64` and `linux/arm64` only**. There
  is no macOS build, no Windows build, no snap, and no package-manager manifest.
- **Saved credentials are Unix-only.** The credential file store is a hardened
  Unix implementation with exact `0700`/`0600` modes, symlink rejection, and
  no-follow opens. Non-Unix builds have no saved-credential backend at all: `yc
  chat`, `yc config show`, and `yc doctor` still work with environment variables
  or a private config file, and `yc login` refuses **before** starting OAuth,
  because a token would have nowhere to go.

See [install.md](install.md) and [docker.md](docker.md).

## Why does the terminal show `dropped=N`?

The UI could not keep up and the poller discarded rows instead of blocking.

Blocking the emitter would stall the goroutine that owns the poll schedule and
cost the whole session, so the trade is deliberate — but the loss is shown at
**every** terminal width rather than being silent. A very fast chat on a slow
terminal is the usual cause. A lower `scrollback_limit` and the `compact` layout
both cut repaint cost.

## Why did chat stop and not reconnect itself?

Terminal conditions — chat ended, chat disabled, chat not found, quota exhausted,
invalid credentials, insufficient permissions — **park with the streams open**
rather than closing them. Retrying an ended chat, or an exhausted quota, spends
units to be told the same thing again, so reopening is your decision: press
`ctrl+r`.

A broadcast going *offline* is different. Chat outlives the stream, so `yc` keeps
polling through a 2-minute grace window and only closes if nothing new arrives.

Transient failures do not stop anything: a `5xx` backs off up to 60s, a rate
limit up to 120s, and one success decays the ladder a single step rather than
clearing it, so a flapping connection does not slam straight back to full
cadence.

## Does a session survive my token expiring?

Yes, in two layers.

A background loop refreshes **5 minutes before the known expiry**, so the token
is normally replaced during a quiet moment rather than mid-poll. If a request is
rejected with a 401 anyway, the transport exchanges the token once — under a
client-wide single-flight, so eight concurrent 401s produce one exchange — and
retries that one request exactly once. A second rejection is terminal and tells
you to run `yc login`.

Refresh tokens issued while your OAuth app is in *Testing* status expire after
**7 days**, regardless of any of that. Publish the consent screen, or re-run
`yc login`. See [auth.md](auth.md).

## Can I read a chat without logging in *and* without an API key?

Only the mock:

```sh
yc chat --mock
```

That drives the entire UI — every layout, every event kind, the theme picker,
filters, help, the activity column — from a deterministic local source with no
network, no credentials, and no quota. It is how most of this documentation was
verified.

The quota tab is empty in mock mode, and says so, rather than showing zeros that
would look like a working meter.

## Where do I find a video ID?

Any of these work as a chat target, unmodified:

```text
dQw4w9WgXcQ
https://www.youtube.com/watch?v=dQw4w9WgXcQ
https://youtu.be/dQw4w9WgXcQ
https://www.youtube.com/live/dQw4w9WgXcQ
https://www.youtube.com/shorts/dQw4w9WgXcQ
@somechannel
UC…  (a channel ID)
```

Nothing is stripped — the leading `@` of a handle is meaningful. Classification
happens in `youtube.ParseChatTarget`, not in the config parser, so a config load
can never fail on a chat name.

## Is my token safe in a debug log?

Tokens, refresh tokens, client secrets, API keys, authorization codes, OAuth
state, PKCE verifiers, authorization URLs, and `Authorization:` header values are
all redacted, and that is enforced by tests that scan output for obviously fake
markers.

**But a debug log is still sensitive.** It can contain video IDs, live chat IDs,
channel names, display names, message IDs, hostnames, counts, and timing. Review
one before sharing it. See [security.md](security.md).

## Why is there no moderation for slow mode, pinning, or members-only?

No API exists for any of them. The entire live-chat moderation surface of the
YouTube Data API v3 is: delete one message, ban a channel, timeout a channel,
lift a ban by its ID. That is all `yc` could ever offer.
[moderation.md](moderation.md) lists what is and is not possible, and why `Unban`
is deliberately not bound to a key.

## Something on this page looks wrong

Every claim here is meant to be checkable against the source or against a test.
If one is not, that is a documentation bug worth reporting — see
[../CONTRIBUTING.md](../CONTRIBUTING.md).

Claims about **credentialed** behavior — anything that requires Google to answer
— are implemented and unit-tested against fakes, and have never been run against
Google. [manual-validation.md](manual-validation.md) is the only place such
evidence may be recorded, and it currently records none.
