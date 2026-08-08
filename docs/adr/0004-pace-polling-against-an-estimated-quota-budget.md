# 0004: Pace Polling Against An Estimated Quota Budget

## Status

Accepted.

## Context

[0001](0001-poll-live-chat-messages-list.md) settles that `yc` polls. This ADR
settles how often, and it is the decision the whole product hangs on.

The arithmetic:

- A default Google Cloud project gets **10,000 quota units per day**, resetting
  at midnight `America/Los_Angeles`.
- `liveChatMessages.list` costs an **estimated 5 units** per call.
- That is **2,000 polls a day**, or one poll every **~43 seconds** to last until
  the reset.
- YouTube's own `pollingIntervalMillis` typically advises **~5 seconds**, which
  exhausts the entire day in **under three hours**.

So the server's advice and the user's budget disagree by roughly an order of
magnitude, and reconciling them is the client's job. A client that simply obeys
`pollingIntervalMillis` dies mid-stream; a client that ignores it violates the
API's terms.

The estimate is the uncomfortable part. Google's published quota cost table lists
**no** `liveChat`, `liveBroadcasts`, `liveStreams`, or `superChatEvents` method at
all, despite the same page's prose claiming live-streaming methods are listed,
and the reference pages for the live-chat methods carry no *Quota impact* line
either. Every live-chat unit figure in circulation is community-observed or
inferred from Google's published rule of thumb — 1 unit for a list read, 50 for a
write. Every *other* endpoint `yc` calls does have a published cost, and `yc`
matches it; see [../quota.md](../quota.md).

## Decision

Meter every call, budget the remaining units across the remaining time, and let
the budget raise — never lower — the poll interval.

**The ledger.** `QuotaLedger` charges every *dispatched* call, including
failures, because Google charges at least one unit for an invalid request. It is
keyed by credential fingerprint and Pacific day, persisted under the cache
directory, and reloads the new day's tally on rollover rather than carrying
yesterday's forward. `ResetAt` constructs the next calendar midnight in
`America/Los_Angeles` rather than adding 24 hours, so a DST boundary moves the
reset by an hour instead of by a day. `search.list` spends its own 100-call
bucket and never the main pool.

**The cadence.**

```text
base     = max(serverFloor, budgetFloor, configMin)
base     = clamp(base, configMin, configMax)
floor    = max(configMin, serverFloor)
interval = base * backoff, jittered, then re-floored
```

with `budgetFloor = horizon / (remainingUnits / costPerPoll)`.

Four invariants:

1. **`pollingIntervalMillis` is inviolable.** Nothing — configuration, jitter,
   backoff decay, `--follow-server-cadence` — produces an interval beneath it.
   The budget can only slow `yc` down.
2. **Jitter always.** ±10% normally, full jitter while backing off, so several
   instances or one restarted in a loop do not synchronize onto the same second.
3. **An exhausted quota is never retried.** Every attempt is charged and the
   allowance does not return until the reset.
4. **A reserve is held back.** At `quota_reserve_percent` remaining (default 10),
   polling stops so that sending and moderation still work — that is the half of
   the client a stream owner cannot do without. `ctrl+r` overrides.

**The honesty rule.** The cost table is **data**, config-overridable per method,
and every figure `yc` renders carries an explicit `est.` marker. In the status
bar the marker is the *last* thing dropped as width runs out, before the numbers
themselves. The Cloud Console is named as the authority wherever the estimate
appears.

**Escape hatches**, because a one-size cadence is wrong for someone:
`--session-hours N` narrows the horizon to tonight, `economy` mode fixes a 5s
floor, `poll_interval_ceiling_ms` caps the stretch, `poll_interval_mode = "off"`
makes refresh manual, and `--follow-server-cadence` removes the budget floor for
a project with a raised quota.

## Consequences

- A default project can watch a chat all day. That is the whole point.
- Latency is worse than an IRC client's by design, and visibly so: the effective
  interval is on screen at all times, next to the meter that explains it.
- `STRETCHED` needs explaining, because it reads like a fault. The status bar
  therefore renders **no label at all** for the healthy case — the `⟳` interval
  already says it — and a label appears precisely when something is not normal.
- The whole system rests on an estimate that could be wrong. The mitigations are
  that it is overridable in one config line, printed by `yc doctor` so a wrong
  constant is visible rather than merely wrong, and never presented as a fact.
- A ledger that cannot be persisted is a degraded meter, not a broken startup —
  but a restart with an unwritable cache does hand the user a false sense of
  budget, so `yc doctor` warns about it explicitly.

## Verification

- Unit-test `BudgetFloor` arithmetic, including zero remaining units and a zero
  horizon.
- Unit-test `NextInterval` over a matrix of server floor, budget floor, config
  bounds, and backoff, asserting the result is never below the server floor and
  never above the ceiling.
- Unit-test ledger charging on failure paths, per-endpoint tallying, and the
  separate search bucket.
- Unit-test Pacific-day rollover across a DST boundary in both directions, using
  an injected clock.
- Unit-test that two credential fingerprints keep independent tallies and that a
  traversal-shaped fingerprint or day is refused rather than sanitized.
- Unit-test that the reserve threshold pauses polling and that `ErrQuotaExceeded`
  produces no retry.
- Compare `yc quota` against the Cloud Console over a real multi-hour stream.
  **This has not been done** — see [../manual-validation.md](../manual-validation.md).
