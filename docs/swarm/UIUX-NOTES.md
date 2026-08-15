# Track B (UI/UX) — Verification and Change Notes

Branch: `swarm/uiux`. Note: `docs/swarm/RECON.md` and `docs/swarm/FINDINGS.md`
did not exist in this worktree when the track started; the verification below
was done directly against the code and tests. Out-of-scope observations are in
`docs/swarm/FINDINGS.md` (created by this track).

## Verification pass (no changes needed)

| Item | Result |
| --- | --- |
| CJK / emoji ZWJ width under wrap | **Already satisfactory.** `internal/render/grapheme_test.go` exhaustively pins grapheme-cluster wrapping: wide CJK, ZWJ family emoji, combining marks, flag sequences, and the "wide cluster refused rather than split" edge at a one-cell budget. The composer caps drafts on cluster boundaries (`capComposerGraphemes`) and backspace deletes whole clusters. No new pinning test needed. |
| Graceful resize | **Already satisfactory.** `tea.WindowSizeMsg` re-renders in-flight reveals at the new width (`refreshActiveRevealRows`) and re-clamps scroll; the layout solver (`view.go: layout()`) degrades by dropping decoration in priority order down to a one-row terminal. `frame_layout_golden_test.go` pins frames at multiple sizes. |
| NO_COLOR / 16-color degradation | **Satisfied by the rendering stack.** Colors are only ever emitted through lipgloss, whose termenv backend detects `NO_COLOR`, `TERM`, and the color profile and downsamples truecolor to 256/16/none automatically. Non-TTY output additionally strips all ANSI (`runModel`'s `ansi.Strip` path). No repo-local `NO_COLOR` code exists and none is needed; a test would only re-test termenv. |
| Sticky behavior when scrolled up | **Already satisfactory, plus gap filled.** Scroll offset is measured from the bottom, so trimming and appends never shift the page being read; arrivals while scrolled away append statically (pinned by `TestScrolledAwayMessagesAppendStatically`). Gap: nothing announced those off-screen arrivals — fixed, see below. |
| Empty / error states | **Already satisfactory.** No-chat empty state (`emptyStateRows`) names the keys to open one; chat-ended and stream-offline are normal end states with history kept; quota exhaustion pauses polling with a status-line explanation and an estimated reset time (`internal/youtube/poll.go`); disabled composer states carry a reason instead of hiding. |

## Changes

### 1. Vim-style scroll jumps — `cdd9486`
Before: only pgup/pgdn/home/end scrolled; ctrl+d/ctrl+u dead outside the
composer. After: `g`/`G` jump to oldest/newest, `ctrl+d`/`ctrl+u` half-page
scroll in the chat view; composer `ctrl+u` (clear line) unchanged. Offsets go
through the existing clamp; scroll position semantics untouched.

### 2. Sticky "N new" indicator — `0da680a`
Before: messages arriving while scrolled up were invisible until the user
happened to scroll down. After: per-chat `newBelow` counter; while non-zero and
scrolled up, the bottom viewport row becomes a `↓ N new · G to jump to newest`
strip (accent role, contrast-corrected foreground — no hardcoded colors).
Clears in `clampScroll` when the offset returns to zero by any route.
Historical backlog never feeds it.

### 3. Incremental `/` search — `47fd683`
Before: no way to find text in the scrollback. After: `/` opens a modal query
line surfaced on the status bar; each edit jumps the browsing cursor to the
newest match and scrolls it into view (offset computed from the real rendered
row blocks, so exact under wrapping); `enter` commits, `n`/`N` walk
older/newer without wrapping, `esc` clears (one step of the existing unwind
chain). Matches thicken their gutter rail in the warning role; the cursor row
keeps the accent rail. Reuses the existing browsing cursor and the
selectable-message list, so deleted rows can never resurface removed words.

### 4. `y` copies the selected message — `aee1471`
Before: only terminal mouse selection, which fights the alt screen. After: `y`
copies the selected (or newest) message's sanitized plain text via OSC 52.
The sequence rides inside the rendered frame — the same mechanism as the OSC
11 background — never a direct write racing the renderer, and never a
shell-out. Payload retired by a sequence-numbered timer; piped/test output
stays escape-free; status bar confirms the copy.

### 5. Docs — `335ee66`
`docs/keybindings.md` regenerated table rows plus beginner-level notes for all
new bindings, the search dispatch-order entry, and the OSC 52 terminal
requirement. README already links to that page; no second list was added.

All new bindings are rows in the `keyBindings` table (help overlay, palette,
and `keymap_coverage_test.go` all key off it). Every change ships with tests:
`search_test.go`, `navigation_test.go`, `clipboard_test.go`.

## Test status

`go build ./... && go vet ./... && go test ./...` green before every commit,
including the golden frame and screenshot suites (unchanged — no fixture
regeneration was needed because no default-state frame changed).
