# Quota

This is the document that has no `twi` counterpart, and the constraint that
shapes every design decision in `yc`.

Twitch gives you an IRC socket. YouTube gives you `liveChatMessages.list` and a
daily unit allowance. A YouTube chat client is therefore a polling client, and a
polling client on a metered API is a budgeting client. Read this before a long
stream.

> **The number that matters most is an estimate.** Google publishes a cost for
> every Data API method `yc` calls *except* the live chat ones. Its cost table
> mentions **none** of `liveChat`, `liveBroadcasts`, `liveStreams`, or
> `superChatEvents` — and the reference pages for `liveChatMessages.list`,
> `liveChatMessages.insert`, and `liveChatBans.insert` carry no *Quota impact*
> line at all, despite the cost page's own prose claiming live-streaming methods
> incur the same costs as everything else. So the 5 units per poll that the
> whole budget rests on is community-observed, not documented. `yc` marks the
> estimates `est.` at every surface, and the Google Cloud Console is the only
> authority. If the Console disagrees with `yc`, the Console is right — and the
> fix is one config line, because the cost table is data.

## The Arithmetic

| | |
| --- | --- |
| Default daily allowance | **10,000 units** |
| Reset | **midnight America/Los_Angeles** |
| `liveChatMessages.list` | **5 units est.** per call |
| Polls the day buys | 10,000 ÷ 5 = **2,000** |
| Interval to last 24h | 86,400s ÷ 2,000 ≈ **43 seconds** |
| YouTube's advised cadence | typically **~5 seconds** |
| Day at the advised cadence | 10,000 ÷ 5 units ÷ (3,600 ÷ 5 polls/h) ≈ **2.8 hours** |

Polling at the cadence YouTube itself advertises exhausts the entire day in
under three hours. That is not a bug in YouTube's advice — `pollingIntervalMillis`
tells you how fast you *may* poll, not how fast you can *afford* to. Reconciling
the two is the client's job.

`yc doctor` prints the same arithmetic for your configured limit:

```text
[ok] quota budget: 10000 units/day at an estimated 5 per poll is about 2000 polls,
     or one poll every 43s to last a full day (all unit figures are estimates)
```

## Unit Costs

The built-in table, all config-overridable. The **Source** column says whether
the figure is Google's or a community estimate — the distinction is the whole
reason this table is data rather than constants.

| Endpoint | Cost | Source | Config key | When `yc` calls it |
| --- | --- | --- | --- | --- |
| `liveChatMessages.list` | 5 | *est.* | `quota_cost_list` | Every poll. This is the whole transport. |
| `liveChatMessages.insert` | 50 | *est.* | `quota_cost_insert` | Sending a message. |
| `liveChatMessages.delete` | 50 | *est.* | `quota_cost_delete` | Deleting a message (transport only — no UI yet). |
| `liveChatBans.insert` | 50 | *est.* | `quota_cost_bans_insert` | Ban or timeout (transport only — no UI yet). |
| `liveChatBans.delete` | 50 | *est.* | `quota_cost_bans_delete` | Lifting a ban (transport only — no UI yet). |
| `videos.list` | 1 | published | `quota_cost_videos_list` | Resolving a video ID or watch URL; broadcast metrics. |
| `channels.list` | 1 | published | `quota_cost_channels_list` | Resolving an `@handle` or channel ID; your own identity. |
| `subscriptions.list` | 1 | published | — | Your subscriptions, for the chat picker. |
| `videoCategories.list` | 1 | published | — | Category names on the Stream Info tab. |
| `videos.update` | 50 | published | — | Stream Info editing. Transport only — there is no editing UI. |
| `search.list` | 1 call | published | `quota_cost_search_list` | **Not wired up.** See below. |

The five estimates are not arbitrary. Google documents the shape even where it
withholds the numbers: *"a read operation that retrieves a list of resources
— channels, videos, playlists — usually costs 1 unit"* and *"a write operation
that creates, updates, or deletes a resource usually costs 50 units."* All four
live chat writes follow the 50-unit rule exactly. The read is the outlier: 5
rather than 1, and it is the one figure worth checking against your own Console.

An endpoint `yc` does not know costs 1 unit, so an unmetered call still moves
the ledger rather than being free by accident.

`yc doctor` prints the table actually in force, including how many entries your
config overrode:

```sh
yc doctor | grep 'cost table'
```

### The `search.list` bucket

Nearly every reference you will find says `search.list` costs **100 units** from
the main pool — a full 1% of the day for one call. **That figure is out of
date.** On **2026-06-01** Google's granular quota change moved `search.list` and
`videos.insert` into their own buckets. The current reference page reads:

> **Quota impact:** 100 calls per day. A call to this method has a quota cost of
> **1 unit in the Search Queries quota bucket**.

and the allocation is stated as *"100 `search.list` calls, 100 `videos.insert`
calls, and 10,000 units per day combined for all other endpoints."* So a new
project gets three independent allowances, not one. `yc` models it that way.

The practical consequence is the opposite of a rounding error: **the buckets
deplete independently.** Search can start returning 403 with 9,000 units still
untouched in the main pool, and — the part that matters for a chat client —
a search never eats into the budget that keeps chat polling. Folding it into
the main total, as the old 100-unit figure would, would have `yc` report ~20
chat polls' worth of headroom gone per search that never actually left.

It is still scarce and non-replenishing, so it stays opt-in. `yc` meters it in
its own bucket and never against the main pool, and shows it separately
everywhere (`search = 3/100 calls`).

Sources, all current as of 2026-08:

- [Quota costs table](https://developers.google.com/youtube/v3/determine_quota_cost)
- [`search.list` reference](https://developers.google.com/youtube/v3/docs/search/list) — the *Quota impact* line quoted above
- [Quota allocation](https://developers.google.com/youtube/v3/getting-started#quota) — the three-bucket default, and the read/write rules of thumb
- [Revision history](https://developers.google.com/youtube/v3/revision_history) — the 2026-06-01 granular-bucket entry, and the 2025-12-04 entry that cut `videos.insert` from ~1600 units

**Status: planned.** `youtube.SearchLiveVideo` and the `allow_search` config key
exist and are tested, but nothing calls them. Resolving "which video is this
channel live on right now" therefore only succeeds when `channels.list` already
reports a current broadcast. Pass the video ID or watch URL directly when it
does not.

## How `yc` Paces Itself

Every interval is computed by `NextInterval`:

```text
base     = max(serverFloor, budgetFloor, configMin)
base     = clamp(base, configMin, configMax)
floor    = max(configMin, serverFloor)          # absolute; nothing goes below it
interval = base * backoff, then jitter, then re-floored
```

| Input | Where it comes from | Effect |
| --- | --- | --- |
| `serverFloor` | `pollingIntervalMillis` in the last list response | **Inviolable.** No configuration, jitter, or decay produces an interval beneath it. |
| `budgetFloor` | `BudgetFloor(remaining, costPerPoll, horizon)` = `horizon / (remaining / cost)` | Stretches the cadence so the remaining units survive the horizon. |
| `configMin` | `poll_interval_floor_ms` (default 1000), or 5s in `economy` mode | A local minimum. It can only slow `yc` down. |
| `configMax` | `poll_interval_ceiling_ms` (0 = none) | Caps the stretch so a nearly-empty budget does not stall chat entirely. |
| `backoff` | `rateLimitExceeded` or 5xx | ×2 per failure, capped at 120s (rate limit) or 60s (transient); decays one step per success. |
| jitter | always | ±10% normally, **full jitter** while backing off, so restarts and multiple instances do not synchronize onto the same second. |

The status bar shows the effective interval next to `⟳`, and a mode label
appears only when something is *not* normal:

| Mode | Meaning |
| --- | --- |
| *(no label)* | Live. Polling at the server-advised cadence. The `⟳` interval already says everything. |
| `STRETCHED` | The budget floor exceeded the server floor. `yc` is deliberately polling slower than allowed so the day lasts. This is a decision, not a fault. |
| `BACKOFF` | The API asked `yc` to slow down. |
| `PAUSED` | The daily allowance or the reserve threshold was reached. |

The quota tab (`alt+3`) spells the same thing out with room to explain, and
`yc quota` prints it as text.

## The Reserve

Running out of read budget must not also take away the ability to send and
moderate — that is the half of the client a stream owner cannot do without.

`quota_reserve_percent` (default **10**) stops polling once remaining units drop
to that share of the allowance. Reads stop; sends and moderation keep working
against the units the reserve held back. The status line says so, and `ctrl+r`
overrides it if you would rather spend the rest on reading.

At 0 units remaining, polling stops unconditionally. `ErrQuotaExceeded` is never
retried: every attempt is charged, and the allowance does not come back until the
Pacific reset.

## Stretching The Budget

Ordered from least to most drastic.

**1. Prefer an explicit live chat ID.** `--live-chat-id` skips resolution
entirely and costs zero units. Every other target form costs one unit to resolve,
once per session.

**2. Ask for more rows per poll.** `yc` already does: it requests
`maxResults=2000`. The documented range is 200–2000 with a default of 500, and
quota is charged **per call, not per item** — so asking for 2000 costs exactly
what asking for 200 costs and buys an order of magnitude more headroom. This is
not configurable because there is no reason to want less.

**3. Narrow the horizon.** `--session-hours 4` (or `session_hours = 4`) budgets
to the next four hours instead of to the reset. You get real-time cadence
tonight and spend tomorrow's headroom to get it. This is the right knob for
"I am watching one stream and then going to bed."

**4. Economy mode.** `poll_interval_mode = "economy"` raises the local floor to
5 seconds regardless of what the budget would allow. Useful when you want a
predictable spend rather than a computed one.

**5. Cap the stretch.** `poll_interval_ceiling_ms` stops the budget floor from
growing past a point you find unusable — at the cost of the budget running out
before the reset.

**6. Manual only.** `poll_interval_mode = "off"` sets both floor and ceiling to
24 hours, which in practice means "poll once and then only when I press
`ctrl+r`."

**7. Raise the actual quota.** A Google Cloud project can request a quota
increase. If yours has one, set `daily_quota_units` to match, or use
`--follow-server-cadence` / `follow_server_cadence = true` to remove the budget
floor entirely and poll at whatever YouTube advises.

**8. Correct the cost table.** If the Cloud Console shows a different spend than
`yc` estimated, fix the constant:

```toml
quota_cost_list = 4
```

The table is data precisely so a corrected figure is a config line rather than a
release.

## The Ledger

`yc` keeps its own estimated ledger because the Cloud Console's own usage graph
lags and cannot be read from a terminal mid-stream.

- Every **dispatched** call is charged, including failures — Google charges at
  least one unit for an invalid request.
- The tally is keyed by **credential fingerprint** (a truncated SHA-256 of the
  client ID and channel ID) and by **Pacific day**, so two accounts on one
  machine do not share a meter and yesterday's spend never counts against today.
- It is persisted under the cache directory as
  `<cache>/yc/quota/<fingerprint>-<YYYY-MM-DD>.json`, mode `0600` in a `0700`
  directory, written atomically through a temp file and rename. Filename
  components are **validated, not sanitized**: a traversal-shaped fingerprint or
  day is refused.
- Rollover **reloads** the new day's persisted tally rather than carrying
  yesterday's forward. A stale total understates today's budget as badly as a
  zeroed one overstates it.
- `ResetAt` constructs the next calendar midnight in `America/Los_Angeles`
  rather than adding 24 hours, so a DST boundary moves the reset by an hour
  rather than by a day. The tz database is embedded in the binary.
- A ledger that cannot be read or written is a **degraded meter, not a broken
  startup**: the session still counts its own spend and `yc doctor` says the
  cache is not writable.

> **Known gap:** `FileLedgerStore.Prune` is implemented and tested but has no
> caller, so one small JSON record accumulates per credential per day. They are
> a few hundred bytes each; delete `<cache>/yc/quota/` freely when it bothers
> you, at the cost of forgetting today's tally.

## Reading The Meter

```sh
yc quota
```

```text
used = 1240/10000 units est.
remaining = 8760 units est. (88%)
search = 0/100 calls
mode = stretched (polling slower than allowed so the daily budget lasts)
resets = Sun, 09 Aug 2026 00:00:00 PDT (America/Los_Angeles)
effective_interval = 41.3s
server_floor = 5s
budget_floor = 41.2s
projected_exhaustion = 20h6m0s at the current cadence, est.

by endpoint (est. units):
  channels.list                1
  liveChatMessages.insert      150
  liveChatMessages.list        1085
  videos.list                  4
```

`yc quota` reads the persisted ledger and **spends nothing** — the command that
tells you how much budget is left cannot itself consume any.

The status bar carries the same meter live, colored from success through warning
to error as remaining units deplete, with the `est.` marker being the last thing
dropped before the numbers themselves when the terminal is narrow.

## Error Conditions Worth Knowing

| Condition | HTTP | `yc` reaction |
| --- | --- | --- |
| `quotaExceeded` / `dailyLimitExceeded` | 403 | Stop polling until the reset. **Never retried** — every attempt is charged. |
| `rateLimitExceeded` / `userRateLimitExceeded` | 403 | Backoff ladder, capped at 120s. |
| `liveChatEnded` | 400/403 | Clean terminal state. History stays on screen. |
| `liveChatDisabled` | 403 | Terminal. The broadcast has chat turned off. |
| `liveChatNotFound` | 404 | Terminal. |
| `forbidden` / `insufficientPermissions` | 403 | Terminal. The credential is valid but lacks the scope or role. |
| invalid credentials | 401 | Terminal at the poller; one refresh is attempted a layer above. |
| backend / internal error | 5xx | Backoff ladder, capped at 60s. |

Sending is additionally rate-limited **locally** — 3 in a burst, then one every
2 seconds — because an insert costs an estimated 50 units whether the API
accepts it or not. Discovering a rate limit by hitting it is the most expensive
possible way to find it.
