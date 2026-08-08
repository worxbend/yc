# 0001: Poll `liveChatMessages.list` As The Chat Transport

## Status

Accepted.

## Context

`yc` needs live YouTube chat receive and send support from a terminal UI. Unlike
Twitch, YouTube exposes no chat socket a plain client can hold open:

- `liveChatMessages.list` is a paged REST endpoint that returns a batch of
  messages, a `nextPageToken`, and a `pollingIntervalMillis` telling the client
  how long to wait before asking again. It is the documented way to read live
  chat, it works with an API key for public chats as well as with OAuth, and it
  is the only method every YouTube live-chat client in the wild is built on.
- `liveChatMessages.streamList` is documented as a low-latency server-streaming
  alternative, but it is **absent from the REST discovery document** and is not
  reachable from a plain REST client today. Building on it would mean building on
  something we cannot call.
- The YouTube Live Streaming API has no WebSocket, no webhook, and no PubSub
  delivery for chat.

There is therefore exactly one transport, and the interesting question is not
*whether* to poll but *how often* — which is [0004](0004-pace-polling-against-an-estimated-quota-budget.md).

## Decision

Implement the chat transport as a poll loop over `liveChatMessages.list`, owned
by `youtube.Poller`, exposed to the UI through the app-facing `ChatClient`
contract rather than through any YouTube type.

The poller:

- Resolves a `ChatTarget` to a `liveChatId` through the cheapest ladder that can
  answer, before the first list call.
- **Primes** with one token-less request, which returns recent history at once.
  Those rows are emitted with `Historical` set so the UI can render backlog
  differently from live traffic.
- **Retains the page token.** An empty `nextPageToken` never clears the stored
  one: falling back to a token-less request re-delivers the whole backlog and
  charges for the privilege.
- Requests `maxResults=2000`. Quota is charged per call, not per item, so the
  largest documented page costs exactly what the smallest does and buys an order
  of magnitude more headroom.
- **Deduplicates** against an 8000-entry ring of recent message IDs, because a
  retained token, a retry, and a reconnect all re-deliver rows, and a client that
  prints the same Super Chat twice is one the viewer stops trusting.
- **Honors `pollingIntervalMillis` as an absolute floor** that no configuration,
  jitter, or backoff decay may go beneath.
- Emits messages, moderation events, room events, poll state, and connection
  state on five separate buffered channels.
- **Drops rather than blocks** when a consumer falls behind, and counts the
  drops, because blocking an emitter stalls the goroutine that owns the poll
  schedule and costs the whole session.
- **Parks on a terminal condition with its streams open.** The adapter above
  treats a closed stream as "retry this chat", and retrying an ended chat or an
  exhausted quota spends units to be told the same thing again. Closing is the
  user's decision, taken with `ctrl+r`.

Sending is `liveChatMessages.insert` behind a local token-bucket limiter that
declines *before* dispatch, because an insert costs an estimated 50 units whether
the API accepts it or not.

## Consequences

- `yc` gets real live chat with one well-supported endpoint and no undocumented
  dependency.
- Latency is bounded below by `pollingIntervalMillis` and, in practice, by the
  budget floor — typically tens of seconds rather than the sub-second delivery an
  IRC client gets. This is inherent to the API and must be surfaced honestly in
  the UI rather than hidden.
- Everything the transport does is metered, which makes quota a first-class UI
  concern rather than an operational detail.
- Chat has no parent-message field, so a reply is a display convention
  (`@DisplayName ` prefix), not a thread.
- If `streamList` ever ships in the discovery document, it can be substituted
  behind the same `Poller` type without touching the renderer or the app.

## Verification

- Unit-test the state machine on an injected clock with no wall-clock sleeps:
  priming, token retention across an empty `nextPageToken`, dedupe across a
  reconnect, the offline grace window, the reserve threshold, and every error
  class routing to its correct terminal or retry outcome.
- Unit-test that `NextInterval` never returns a value below the server floor for
  any combination of budget floor, config bounds, jitter, and backoff.
- Unit-test send-limiter burst and steady-state spacing.
- Drive the Bubble Tea model with `youtube.FakeChatClient`.
- Run `go test -race ./internal/youtube ./internal/app`.
