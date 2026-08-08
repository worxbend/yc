# Key Bindings

Every binding `yc` handles, grouped by the context that owns it. The chat-view
table below is **generated from `internal/app/keymap.go`**, which is the same
table the help footer, the expanded help overlay (`?`), and the command palette
(`ctrl+p`) all render from. There is no second list to keep in sync — see
[How To Verify This Page](#how-to-verify-this-page).

For the layout the keys act on, read [quickstart.md](quickstart.md). For the
config values some of these keys cycle, read [config.md](config.md).

## How Input Is Dispatched

The order matters, and it is the order `shellModel.handleKey` checks in. A key is
consumed by the first context that claims it:

```text
  ctrl+c                      -> quit, from anywhere, always first
  splash still animating      -> any key skips it, and nothing else happens
  alt+<digit>                 -> switch tab, from any focus
  moderation prompt armed     -> the duration field or the confirmation owns it
  ctrl+p / ctrl+e / ctrl+t    -> open or close an overlay, from any focus
  ctrl+g / ctrl+b / ctrl+y / ctrl+n -> display cycles, from any focus
  an overlay is open          -> the overlay owns the keyboard
  the mention strip is showing-> it claims tab / up / down / esc, and only then
  a leader chord is pending   -> the next key completes or cancels the chord
  space (outside composer)    -> arms the leader chord
  the sidebar has focus       -> sidebar navigation
  everything else             -> normal mode, or literal text in the composer
```

Two consequences are worth stating out loud:

- **The global toggles work from inside an overlay**, because they are how you
  get back out of one.
- **A pending `space` leader is consumed before any normal-mode binding**, so a
  chord can never be mistaken for a bare key. An unbound second key cancels the
  chord rather than trapping you in a mode.

## The Leader Chord

`space` outside the composer starts a two-keystroke chord, in the AstroNvim
style. It is armed only outside the composer, where `space` is literal text.

| Chord | Does |
| --- | --- |
| `space e` | Show/hide the chats sidebar |
| `space c` | Open the chat picker |
| `space x` | Close the active chat |
| `space a` | Show/hide the activity column |
| `space i` | Toggle the inspect panel on the selected message |

A chord is exactly two keystrokes. Any other second key clears the pending state
and does nothing, so a stray `space` never leaves the shell in a mode you have to
escape from.

`space e` and `space a` set an explicit show/hide choice that outranks the
automatic width-based decision in both directions: a narrow terminal cannot
un-hide a pane you hid, and a wide one cannot hide a pane you pinned.

## Chat View

Generated from `keyBindings` in `internal/app/keymap.go`, in the order the
expanded help renders it.

| Group | Keys | Does |
| --- | --- | --- |
| **Chat** | | |
| | `i/o/a` | compose |
| | `esc` | back to chat |
| | `j/k` | select message |
| | `pgup/pgdn` | scroll |
| | `r` | reply |
| | `K` | inspect |
| | `ctrl+e` | emoji picker |
| | `@ then tab` | complete a mention |
| **Moderation** | | |
| | `d` | delete selected message (asks first) |
| | `t` | time out author (asks for a duration) |
| | `b` | ban author (asks first) |
| **Chats** | | |
| | `space e` | chats sidebar |
| | `space c` | open chat |
| | `space x` | close chat |
| | `space a` | activity column |
| | `space i` | inspect |
| | `[/]` | switch chat |
| **View** | | |
| | `1-4` | filters |
| | `0` | reset filters |
| | `alt+1/2/3` | tabs |
| | `tab` | focus chat/composer/chats |
| | `?` | help |
| | `</>` | resize pane |
| | `=` | reset pane sizes |
| **Display** | | |
| | `ctrl+t` | theme |
| | `ctrl+g` | layout |
| | `ctrl+b` | badges |
| | `ctrl+y` | emoji highlight |
| | `ctrl+n` | full names |
| **Session** | | |
| | `ctrl+p` | commands |
| | `ctrl+r` | reconnect |
| | `ctrl+l` | clear (asks first) |
| | `q` | quit |
| | `ctrl+c` | quit |

### Notes on individual bindings

- **`i` / `o` / `a`** all move focus to the composer. `vim`'s three insert keys
  differ by where they open a line; `yc`'s composer always appends, so the three
  are equivalent and exist for muscle memory.
- **`q`** quits only while the chat pane has focus. In the composer it is the
  letter `q`. `ctrl+c` quits from anywhere, including mid-overlay and
  mid-confirmation.
- **`K`** and **`space i`** both toggle the inspect panel. Opening it with no
  message selected selects the most recent one first.
- **`pgup` / `pgdn`** scroll by one viewport height. **`home`** jumps to the
  oldest retained row and **`end`** to the newest, outside the composer only.
  Neither is in the table above because neither is listed in `keyBindings`;
  they are handled directly by `handleKey`.
- **`</>` and `=`** work from the chat pane and from the sidebar, because the
  sidebar is the one pane whose own width they adjust. `=` clears both the
  sidebar and activity width overrides at once.
- **`ctrl+l`** asks first: the first press arms, the second clears. **Any other
  key disarms it**, so a stray press cannot sit waiting for an unrelated
  keystroke to fire it later. Clearing discards the retained backlog for that
  chat and cannot be undone.
- **`ctrl+r`** reconnects. It is also the override for a quota-reserve pause and
  the way to reopen a chat that parked on a terminal condition — see
  [quota.md](quota.md) and [troubleshooting.md](troubleshooting.md).

### Tabs

`alt+1` / `alt+2` / `alt+3` select **Chat**, **Stream Info**, and **Quota**.
The modifier is `alt` rather than `ctrl` because Bubble Tea — and most
terminals — cannot distinguish `ctrl+1` from a plain `1`. `alt+<digit>` is the
combination terminals do report as a distinct key.

Tab switching is handled before overlays and before moderation, so it always
works.

### Filters

`1`–`4` toggle local view filters on the active chat; `0` resets them. Filters
decide what is drawn, never what is retained, so toggling one back off restores
the full history with no refetch and no quota spend.

| Key | Filter | Shows |
| --- | --- | --- |
| `1` | mentions | messages that mention you |
| `2` | roles | owner, moderator, and member messages |
| `3` | events | Super Chats, stickers, gifts, memberships |
| `4` | notices | room-state and moderation notices |

The role filter tests owner/moderator/member because those are the only roles
the YouTube live chat API reports. There is no VIP, founder, or staff equivalent.

## Composer

Focus reaches the composer with `i`/`o`/`a`, with `tab`, or by committing an
emoji from the picker.

| Key | Does |
| --- | --- |
| printable keys, `space` | insert text |
| `backspace` | delete one **grapheme cluster**, never one byte |
| `ctrl+u` | clear the draft |
| `enter` | queue the message for sending |
| `esc` | leave the composer, keeping the draft intact |
| `@` then `tab` | accept the highlighted mention completion |

While the composer has focus, `?`, `q`, `j`, `k`, `d`, `t`, `b`, and the digits
are ordinary characters. The global `ctrl` toggles and `alt+<digit>` still work.

`esc` from the composer always returns to the chat view first; it does not close
an inspect panel or cancel a reply in the same keystroke. From the chat view,
`esc` then unwinds one step at a time: inspect panel, then armed reply, then the
selection.

### Mention completion

Typing `@` followed by a prefix pops a completion strip above the composer. It
claims `tab`, `up`, `down`, and `esc` **only while it is showing something** —
claiming them unconditionally would break the bindings those keys carry
everywhere else in the composer.

The candidate list is the chat's own roster, so it can only complete somebody who
has spoken since the session started.

## Overlays

Four overlays share one key handler, which is what keeps them behaving
identically under keys you have already learned. They are mutually exclusive by
construction: opening one closes any other, so `esc` always closes the thing you
can see.

| Overlay | Opens with |
| --- | --- |
| Command palette | `ctrl+p` |
| Theme picker | `ctrl+t` |
| Chat picker | `space c` |
| Emoji picker | `ctrl+e` |

| Key | Does |
| --- | --- |
| printable keys, `space` | type into the filter query |
| `backspace` | delete one grapheme from the query |
| `ctrl+u` | clear the query |
| `up` | previous item (wraps) |
| `down`, `tab` | next item (wraps) |
| `home` / `end` | first / last item |
| `enter` | commit the selection |
| `esc` | close without committing |
| `ctrl+p`/`ctrl+t`/`ctrl+e` | close this overlay, or swap to that one |

What `enter` commits differs by overlay: the theme picker applies and remembers
the palette, the chat picker opens the chat, the emoji picker inserts the
grapheme into the draft and moves focus to the composer, and the command palette
runs the command.

The theme picker is the only overlay that **previews**: moving the selection
applies the palette immediately, including the terminal's own background, and
`esc` restores the palette that was live when it opened. See
[themes.md](themes.md).

## Chats Sidebar

Reachable with `tab` (focus cycles chat → composer → chats) and toggled with
`space e`.

| Key | Does |
| --- | --- |
| `j` / `down` | next chat |
| `k` / `up` | previous chat |
| `l` / `right` / `enter` | activate the selected chat |
| `h` / `left` / `esc` | back to the chat pane |
| `x` / `d` / `delete` | close the selected chat |
| `i` / `o` / `a` | jump straight to the composer |
| `<` / `>` / `=` | resize the sidebar |

`d` closes a chat here rather than deleting a message: the moderation keys
require the chat pane to have focus, so the two meanings can never collide.

## Moderation Keys

`d`, `t`, and `b` act on the message the chat cursor has selected. They are live
only when **all** of these hold: the Chat tab is active, the chat pane has focus,
no overlay is open, and no leader chord is pending.

| Key | Action | Confirmation |
| --- | --- | --- |
| `d` | delete the selected message | press `d` again, or `enter` |
| `t` | time the author out | duration prompt, then press `t` again, or `enter` |
| `b` | ban the author permanently | press `b` again, or `enter` |

- `esc` cancels an armed confirmation or an open duration prompt.
- **Any other key disarms** the confirmation and then does its own job — the same
  rule `ctrl+l` follows. Pressing `t` while a ban is armed disarms the ban and
  opens the timeout prompt.
- The **duration prompt is modal**: while it is open, every printable key is part
  of the duration and nothing else fires.

The keys stay bound and documented even when the credential cannot use them, and
answer with a reason instead of doing nothing. Full treatment, including the
capability ladder and what the API can and cannot do:
[moderation.md](moderation.md).

## Mouse

`enable_mouse` is on by default; `--no-mouse` disables it for one run.

| Input | Does |
| --- | --- |
| wheel up | scroll back |
| wheel down | scroll forward |

That is the whole mouse surface. There is no click-to-select, no drag-to-resize,
and no hit-testing: the model owns scrolling, and hit-testing panes would belong
to the view, which is the only layer that knows where anything was drawn. Mouse
input is ignored entirely while an overlay is open.

## Discovering Keys In The App

Three surfaces, all generated from the same table:

- **The footer** is a one-line compact form, present at every terminal width.
- **`?`** expands it into the grouped legend. On short terminals the groups are
  dropped from the bottom up rather than truncated mid-line.
- **`ctrl+p`** is the command palette: every display key has an entry there,
  because a command palette exists precisely so a binding is not discoverable
  only by already knowing it.

## How To Verify This Page

The chat-view table is a rendering of `keyBindings`. To check it has not drifted,
print the source table and compare:

```sh
grep -o '{Keys: "[^"]*", Description: "[^"]*"' internal/app/keymap.go
```

The invariants behind the table are enforced by tests rather than by review:

```sh
go test ./internal/app -run 'Keymap|Help|CommandPalette|Ctrl|Moderation|EmojiPicker' -v
```

| Test | Guarantees |
| --- | --- |
| `TestEveryHandledCtrlKeyIsDocumented` | a `ctrl` key the update loop handles cannot be missing from the table |
| `TestExpandedHelpCoversEveryBinding` | every table entry reaches the `?` overlay |
| `TestCompactFooterOnlyNamesDocumentedKeys` | the footer cannot advertise a key that no longer exists |
| `TestEveryKeyGroupHasALabel` | a new group cannot render as a blank heading |
| `TestCommandPaletteReachesEveryDisplayKey` | every display toggle has a palette entry |
| `TestCommandPaletteShortcutsAreDocumentedKeys` | a palette row cannot name a key the keymap dropped |
| `TestModerationKeysAreDocumented` | `d`/`t`/`b` stay in the table |
| `TestModerationKeysStayDocumentedWhenTheyAreDisabled` | help does not hide keys the credential cannot use |
| `TestHelpFooterComesFromTheKeymap` | the footer is generated, not hand-maintained |

This history is why the table exists at all: in `twi`, `ctrl+e` opened the emote
picker dozens of times a session and appeared in none of the three surfaces,
discoverable only by reading the README.

**Status: ready.** Every binding on this page is exercised by tests and by
`yc chat --mock`, which needs no credentials. The moderation keys are ready as
*keys* — arming, confirming, cancelling, and every disabled explanation are
tested — but the requests they issue are **credentialed** and have never been run
against Google. See [manual-validation.md](manual-validation.md).
