# FINDINGS — cross-track handoffs and hot-zone locks

Agents: when you hit an issue that belongs to another track, record it under that track's
heading and move on. Do not fix out-of-scope issues.

## Hot-zone locks

Only one track may touch each zone at a time. Orchestrator grants/releases.

| Zone | Files | Held by |
|---|---|---|
| Render pipeline | `internal/render/*`, `internal/app/view.go`, `internal/app/panes.go` | Track A (sanitization), then C, then B |
| Message pipeline | `internal/youtube/poll.go`, `internal/app/live_chat.go`, `internal/app/chat.go` | Track C |
| Config schema | `internal/config/*` | Track D (after A/C merge) |

## Track A — Security
Done. See docs/swarm/SECURITY-AUDIT.md. No out-of-scope handoffs: nothing
belonging to another track surfaced during the audit. Render-pipeline lock can
be released to Track C (Track A touched internal/app/view.go's
sanitizeContextValue only; internal/render gained a fuzz test, no code change).

## Track B — UI/UX
(none yet)

## Track C — Performance
(none yet)

## Track D — Features
(none yet)

## Track E — Clean Code
(none yet)
