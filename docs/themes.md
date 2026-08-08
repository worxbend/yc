# Themes

Every built-in palette, the nine roles each one fills, the custom-hex format, the
`ctrl+t` picker, and the contrast correction that keeps a theme legible instead
of merely pretty.

For the config keys, read [config.md](config.md). For where a palette is used in
the layout, read [architecture.md](architecture.md).

## Nine Roles, No More

A `theme.Palette` has exactly nine fields. Every widget in `yc` derives its
colors from those nine, and no widget carries a hex value of its own.

| Role | Used for |
| --- | --- |
| `Background` | the chat surface. The full-viewport canvas — and the terminal's own OSC 11 background — is a **darkened derivative** of it, so panes read as raised. |
| `Foreground` | body text |
| `Accent` | the status bar fill, the focused pane rail, selection markers, membership chips, tier 1 |
| `Muted` | timestamps, secondary detail, an unfocused frame, the deleted-message placeholder |
| `Border` | pane frames |
| `Surface` | framed pane bodies, sitting above `Background` |
| `Warning` | notices, the `[notice]` marker, the mid-stage quota meter, paid tiers 5–6 |
| `Error` | errors, moderation entries, the exhausted quota meter, paid tiers 9–11 |
| `Success` | connected state, gift chips, a healthy quota meter, paid tiers 3–4 |

Nine is a constraint the renderer works within rather than around. YouTube's own
Super Chat ladder runs eleven distinct tier colors; eleven hues do not exist in a
nine-role palette, and inventing them would break the theme contract. The ladder
therefore collapses onto six monotonic steps built from `Accent`, `Success`,
`Warning`, and `Error`, with `theme.Mix` blends between them. The exact amount is
printed on the chip either way — the color only has to convey "more than the last
one".

Values are hex strings, `#rrggbb` or `#rgb`. An empty or unparseable value
**degrades the decoration rather than failing the app**: a broken custom theme
costs you a gradient, not a session.

## Choosing A Theme

```sh
yc profile list                 # print every preset name
yc profile show                 # print the palette currently in force
yc profile set nord             # persist it to config.toml
yc chat --theme tokyo-night     # one run only; not written back
```

Or set it in `config.toml` / the environment:

```toml
theme_name = "catppuccin-mocha"
```

```sh
export YC_THEME_NAME=gruvbox
```

An unrecognized name falls back to the default palette **with a warning** rather
than failing startup:

```text
warning: unknown theme "hologram"; using the default palette.
Run `yc profile list` for available names.
```

`yc doctor` reports the same thing as a `theme` check line.

The default is `claude` (`theme.DefaultPaletteName`).

## The `ctrl+t` Picker

`ctrl+t` opens the theme picker. It is a **full-screen page**, not a strip docked
under the chat pane, and that is deliberate: a palette has to be judged on the
whole terminal — the chat surface, the panes, the status bar, the canvas behind
them — and a preview seen through a chat pane squeezed down to make room for the
picker tells you nothing about the theme you are choosing.

| Key | Does |
| --- | --- |
| `↑` / `↓` | move the selection, wrapping at both ends |
| `tab` | next entry |
| `home` / `end` | jump to the first or last entry |
| printable keys | filter the list by substring |
| `backspace` / `ctrl+u` | edit or clear the filter |
| `enter` | commit the highlighted theme |
| `esc` | cancel and restore the palette that was live when the picker opened |
| `ctrl+t` | close the picker |

**Moving the selection applies the palette immediately.** `View` re-derives the
terminal background from the live palette on every frame, so the terminal's own
background follows the preview too. `esc` restores what was on screen when the
picker opened rather than leaving the last previewed palette applied.

The picker opens **on the active theme**, not at the top of an alphabetical list,
because it previews live and starting somewhere else would repaint the terminal
before you asked it to.

Each row draws the name plus a seven-cell swatch strip in that preset's own
colors, sampled in a fixed order — accent, foreground, muted, success, warning,
error, border — so a swatch column means the same thing on every row and the list
doubles as a palette comparison without selecting each entry in turn. An unset
role draws `··` rather than a solid block: a block would paint in the terminal's
default foreground and read as a real color.

The theme the session started with is labelled `(active)`, so a live preview can
never be mistaken for the configured choice.

> **`enter` applies the theme for this run.** It updates the effective config in
> memory; it does not write `config.toml`. Use `yc profile set <name>` to persist
> a choice. The picker says as much rather than implying otherwise: the header
> reads `Select a theme — the preview applies for this run` and the footer reads
> `↑/↓ move · home/end jump · enter apply · esc cancel`.

Entries are the 58 preset names in sorted order, followed by `custom` last, so
the entry that needs configuration is not sitting in the middle of the list.

## The 58 Presets

Most take each scheme's well-known published colors; `yc`, `claude`, `codex`,
`btop`, and `mono` are authored for this project. Upstream schemes rarely name
all nine roles — usually `Border`, sometimes `Muted`, is missing — and where one
is absent the nearest published tone is reused, or a quiet neighbor is derived
from the scheme's own ramp, rather than a new hue being invented.

Light schemes map `Background` to the scheme's lightest base and `Surface` to the
next tone up the ramp, so panes keep reading as raised above the canvas in both
light and dark palettes.

The **Tone** column is derived from the relative luminance of `Background`; it is
descriptive, not a config value.

| Name | Tone | Background | Foreground | Accent | Muted | Border | Surface | Warning | Error | Success |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| `abyss` | dark | `#010a0c` | `#d6f5f2` | `#00e5cc` | `#5c8a8a` | `#0d3033` | `#041416` | `#ffcb47` | `#ff5f70` | `#4dffd2` |
| `amber-crt` | dark | `#0a0600` | `#ffd799` | `#ffab00` | `#9a7440` | `#3d2a08` | `#140d02` | `#ffd500` | `#ff6b4a` | `#b8e04a` |
| `arctic-neon` | dark | `#04080d` | `#e6f4ff` | `#4dd8ff` | `#6f8799` | `#183040` | `#0a1219` | `#ffd166` | `#ff6b81` | `#5df2b5` |
| `ayu-dark` | dark | `#0d1017` | `#bfbdb6` | `#e6b450` | `#7b8391` | `#1f242c` | `#131721` | `#ffb454` | `#d95757` | `#aad94c` |
| `ayu-mirage` | dark | `#1f2430` | `#cccac2` | `#ffcc66` | `#8a94a6` | `#33415e` | `#242936` | `#ffd173` | `#f28779` | `#d5ff80` |
| `blood-moon` | dark | `#0b0406` | `#f6e3e6` | `#ff3b52` | `#96707a` | `#3b1620` | `#160a0e` | `#ffab4a` | `#ff5c6c` | `#59d99d` |
| `btop` | dark | `#000000` | `#d3d3d3` | `#00ff00` | `#5a5a5a` | `#3a3a3a` | `#101010` | `#ffdd33` | `#ff3333` | `#00ff00` |
| `bullion` | dark | `#0a0803` | `#fff3d4` | `#ffcf33` | `#9c8a5e` | `#3a2f10` | `#141005` | `#ffe066` | `#ff6b52` | `#a8e05f` |
| `carbon` | dark | `#000000` | `#f0f0f0` | `#ff5f1f` | `#8c8c8c` | `#262626` | `#0d0d0d` | `#ffbf00` | `#ff3b30` | `#32d74b` |
| `catppuccin-frappe` | dark | `#303446` | `#c6d0f5` | `#ca9ee6` | `#a5adce` | `#51576d` | `#414559` | `#e5c890` | `#e78284` | `#a6d189` |
| `catppuccin-latte` | light | `#eff1f5` | `#4c4f69` | `#8839ef` | `#6c6f85` | `#ccd0da` | `#e6e9ef` | `#df8e1d` | `#d20f39` | `#40a02b` |
| `catppuccin-macchiato` | dark | `#24273a` | `#cad3f5` | `#c6a0f6` | `#a5adcb` | `#494d64` | `#363a4f` | `#eed49f` | `#ed8796` | `#a6da95` |
| `catppuccin-mocha` | dark | `#1e1e2e` | `#cdd6f4` | `#cba6f7` | `#a6adc8` | `#45475a` | `#313244` | `#f9e2af` | `#f38ba8` | `#a6e3a1` |
| `claude` | dark | `#1a1523` | `#f2ede4` | `#d97757` | `#948f9c` | `#4a4358` | `#241d30` | `#e0a72e` | `#e0685a` | `#7fbf8e` |
| `cobalt2` | dark | `#193549` | `#e1efff` | `#ffc600` | `#93b3cc` | `#234e6d` | `#1f4662` | `#ff9d00` | `#ff628c` | `#3ad900` |
| `codex` | dark | `#0d1117` | `#e6edf3` | `#3fb950` | `#8b949e` | `#30363d` | `#161b22` | `#d29922` | `#f85149` | `#3fb950` |
| `cyberpunk` | dark | `#050505` | `#f4f4e8` | `#fcee0a` | `#8a8a7a` | `#2e2e18` | `#0f0f0a` | `#ff9f1c` | `#ff003c` | `#00f0ff` |
| `deep-ocean` | dark | `#020914` | `#dcefff` | `#00d4ff` | `#5f7f99` | `#123049` | `#06141f` | `#ffc857` | `#ff5d73` | `#2ee6a8` |
| `dracula` | dark | `#282a36` | `#f8f8f2` | `#bd93f9` | `#6272a4` | `#44475a` | `#343746` | `#f1fa8c` | `#ff5555` | `#50fa7b` |
| `emerald-noir` | dark | `#020a07` | `#dcf6ea` | `#00d68f` | `#5f8c78` | `#0f3327` | `#06130e` | `#ffc94d` | `#ff5d6e` | `#3ff2a8` |
| `everforest` | dark | `#2d353b` | `#d3c6aa` | `#a7c080` | `#859289` | `#3d484d` | `#343f44` | `#dbbc7f` | `#e67e80` | `#83c092` |
| `github-light` | light | `#ffffff` | `#24292f` | `#0969da` | `#57606a` | `#d0d7de` | `#f6f8fa` | `#9a6700` | `#cf222e` | `#1a7f37` |
| `gruvbox` | dark | `#282828` | `#ebdbb2` | `#fe8019` | `#928374` | `#3c3836` | `#32302f` | `#fabd2f` | `#fb4934` | `#b8bb26` |
| `gruvbox-light` | light | `#fbf1c7` | `#3c3836` | `#af3a03` | `#7c6f64` | `#d5c4a1` | `#f2e5bc` | `#b57614` | `#9d0006` | `#79740e` |
| `horizon` | dark | `#1c1e26` | `#d5d8da` | `#e95678` | `#8a8daf` | `#2e303e` | `#232530` | `#fab795` | `#f43e5c` | `#29d398` |
| `hotline` | dark | `#0a0410` | `#ffe9f4` | `#ff2e88` | `#96738c` | `#3d1230` | `#16081d` | `#ffb627` | `#ff4d4d` | `#00e0c7` |
| `kanagawa` | dark | `#1f1f28` | `#dcd7ba` | `#7e9cd8` | `#727169` | `#2d4f67` | `#2a2a37` | `#e6c384` | `#e82424` | `#98bb6c` |
| `magma` | dark | `#0b0503` | `#ffe4d6` | `#ff3d00` | `#9c7263` | `#3d1a0d` | `#160a05` | `#ffab2e` | `#ff6347` | `#8fd97f` |
| `matrix` | dark | `#000000` | `#c8ffc8` | `#00ff41` | `#4f8f4f` | `#0f3d0f` | `#050f05` | `#b6ff00` | `#ff5f56` | `#00ff41` |
| `midnight-ember` | dark | `#0a0705` | `#ffe8d6` | `#ff7a33` | `#9c7f6b` | `#3a2618` | `#150e09` | `#ffc233` | `#ff5a4d` | `#7fd98a` |
| `mint-noir` | dark | `#040b09` | `#e0fff2` | `#5effc4` | `#6b9c8a` | `#12352a` | `#081511` | `#ffd75e` | `#ff6b7d` | `#5effc4` |
| `mono` | dark | `#000000` | `#ffffff` | `#ffffff` | `#808080` | `#808080` | `#1a1a1a` | `#c0c0c0` | `#ffffff` | `#ffffff` |
| `monokai` | dark | `#272822` | `#f8f8f2` | `#f92672` | `#75715e` | `#3e3d32` | `#3e3d32` | `#e6db74` | `#f92672` | `#a6e22e` |
| `neon-tokyo` | dark | `#07060d` | `#eae6ff` | `#ff2bd6` | `#7b7596` | `#2b2447` | `#100c1c` | `#ffc531` | `#ff4d6d` | `#3df5c4` |
| `night-owl` | dark | `#011627` | `#d6deeb` | `#82aaff` | `#7c93a8` | `#1d3b53` | `#0b2942` | `#ecc48d` | `#ef5350` | `#22da6e` |
| `nightfox` | dark | `#192330` | `#cdcecf` | `#719cd6` | `#71839b` | `#29394f` | `#212e3f` | `#dbc074` | `#c94f6d` | `#81b29a` |
| `nord` | dark | `#2e3440` | `#eceff4` | `#88c0d0` | `#4c566a` | `#3b4252` | `#3b4252` | `#ebcb8b` | `#bf616a` | `#a3be8c` |
| `obsidian` | dark | `#000000` | `#e8eaed` | `#cfd8dc` | `#7a8288` | `#22262a` | `#0b0d0f` | `#ffca28` | `#ff5252` | `#69f0ae` |
| `oceanic-next` | dark | `#1b2b34` | `#c0c5ce` | `#6699cc` | `#8b98a6` | `#4f5b66` | `#343d46` | `#fac863` | `#ec5f67` | `#99c794` |
| `one-dark` | dark | `#282c34` | `#abb2bf` | `#61afef` | `#5c6370` | `#3e4451` | `#21252b` | `#e5c07b` | `#e06c75` | `#98c379` |
| `orchid` | dark | `#08040a` | `#f7e4ff` | `#e56cf0` | `#8f6b99` | `#341542` | `#120818` | `#ffcc52` | `#ff5c8a` | `#52e0b8` |
| `palenight` | dark | `#292d3e` | `#a6accd` | `#c792ea` | `#676e95` | `#3a3f58` | `#32374d` | `#ffcb6b` | `#f07178` | `#c3e88d` |
| `plasma` | dark | `#06050f` | `#e6e3ff` | `#6c5cff` | `#75729c` | `#231f4a` | `#0d0b1c` | `#ffc93c` | `#ff5470` | `#3ce8b0` |
| `rose-pine` | dark | `#191724` | `#e0def4` | `#c4a7e7` | `#6e6a86` | `#403d52` | `#26233a` | `#f6c177` | `#eb6f92` | `#31748f` |
| `rose-pine-dawn` | light | `#faf4ed` | `#575279` | `#907aa9` | `#797593` | `#dfdad9` | `#fffaf3` | `#ea9d34` | `#b4637a` | `#286983` |
| `rose-pine-moon` | dark | `#232136` | `#e0def4` | `#c4a7e7` | `#6e6a86` | `#44415a` | `#2a273f` | `#f6c177` | `#eb6f92` | `#3e8fb0` |
| `ruby` | dark | `#0a0308` | `#ffe0ec` | `#f50057` | `#9c6b81` | `#3d0f28` | `#150610` | `#ffbf47` | `#ff5c7a` | `#4de3a8` |
| `sapphire` | dark | `#03070f` | `#dfeaff` | `#2979ff` | `#65799c` | `#122647` | `#070e1c` | `#ffc233` | `#ff5c7a` | `#31dba0` |
| `solarized-dark` | dark | `#002b36` | `#839496` | `#268bd2` | `#586e75` | `#073642` | `#073642` | `#b58900` | `#dc322f` | `#859900` |
| `solarized-light` | light | `#fdf6e3` | `#586e75` | `#268bd2` | `#93a1a1` | `#ded8c4` | `#f5efdd` | `#b58900` | `#dc322f` | `#859900` |
| `spectre` | dark | `#000000` | `#e8f6fa` | `#9fe8ff` | `#6b8592` | `#1c2b33` | `#080d10` | `#ffd98f` | `#ff8fa3` | `#8fffd6` |
| `synthwave-84` | dark | `#262335` | `#f0eff1` | `#ff7edb` | `#848bbd` | `#34294f` | `#2a2139` | `#fede5d` | `#fe4450` | `#72f1b8` |
| `tokyo-night` | dark | `#1a1b26` | `#c0caf5` | `#7aa2f7` | `#565f89` | `#414868` | `#24283b` | `#e0af68` | `#f7768e` | `#9ece6a` |
| `toxic` | dark | `#040703` | `#e8ffd9` | `#aaff00` | `#7f9a66` | `#1f3312` | `#0a1006` | `#ffe600` | `#ff4d3d` | `#39ff88` |
| `ultraviolet` | dark | `#08040f` | `#ece2ff` | `#a855ff` | `#7d6f9c` | `#2d1a4d` | `#110a1e` | `#ffcc4d` | `#ff4f81` | `#4de8b0` |
| `vaporwave` | dark | `#0d0618` | `#f2e9ff` | `#ff6ad5` | `#8d7fa8` | `#3a2a5c` | `#170c28` | `#ffd166` | `#ff5c8a` | `#61e8e1` |
| `yc` | dark | `#0b0708` | `#f4eef0` | `#ff2d46` | `#948a8d` | `#3a2429` | `#150e10` | `#ffb340` | `#ff7a6b` | `#5ce0a0` |
| `zenburn` | dark | `#3f3f3f` | `#dcdccc` | `#f0dfaf` | `#989890` | `#5f5f5f` | `#4f4f4f` | `#dfaf8f` | `#cc9393` | `#7f9f7f` |

Fifty-three dark, five light (`catppuccin-latte`, `github-light`,
`gruvbox-light`, `rose-pine-dawn`, `solarized-light`).

`yc` is the house palette: a near-black canvas under the platform red, chosen so
a Super Chat chip and an error state still read apart from the accent that paints
the status bar. `claude` is the default.

### Preset invariants

These are tested, not merely intended, for **every** preset:

- every role is present and parses as valid hex;
- `Foreground` meets 4.5:1 against both `Background` and `Surface`;
- `Accent` visibly stands out from its own `Background`;
- no two presets are identical;
- a hashed author identity color stays readable on every preset;
- `Success`, `Warning`, `Error`, and `Muted` stay **mutually distinguishable**
  when drawn on that preset's `Accent` — see
  [Contrast Correction](#contrast-correction).

## Custom Palettes

Set `theme_name = "custom"` and fill the nine role keys. They are read **only**
when the name is `custom`; an unset role falls back to no styling for that role
rather than to a preset's value.

```toml
theme_name = "custom"
theme_background = "#0b0708"
theme_foreground = "#f4eef0"
theme_accent     = "#ff2d46"
theme_muted      = "#948a8d"
theme_border     = "#3a2429"
theme_surface    = "#150e10"
theme_warning    = "#ffb340"
theme_error      = "#ff7a6b"
theme_success    = "#5ce0a0"
```

Environment equivalents, one per role:

```sh
YC_THEME_NAME=custom
YC_THEME_BACKGROUND YC_THEME_FOREGROUND YC_THEME_ACCENT
YC_THEME_MUTED      YC_THEME_BORDER     YC_THEME_SURFACE
YC_THEME_WARNING    YC_THEME_ERROR      YC_THEME_SUCCESS
```

Or write them from the command line, which validates each value and preserves
your comments and key ordering in the file:

```sh
yc profile set custom \
  --background '#0b0708' --foreground '#f4eef0' --accent '#ff2d46' \
  --muted '#948a8d' --border '#3a2429' --surface '#150e10' \
  --warning '#ffb340' --error '#ff7a6b' --success '#5ce0a0'
```

Format rules:

- `#rrggbb` or the three-digit `#rgb` shorthand.
- Case-insensitive.
- An unparseable value costs that role's decoration and nothing else. There is no
  path by which a bad hex string fails startup or panics a render.

`custom` also appears as the last entry in the `ctrl+t` picker, drawing `··` for
any role you have not filled in.

A good starting point is `yc profile show`, which prints the palette currently in
force role by role — copy it, change one value, and set `theme_name = "custom"`.

## Contrast Correction

`yc` never trusts a palette to be legible. Two different corrections run, and
they answer two different questions.

### `ContrastCorrectedForeground` — "is this exact color legible here?"

Used for body text and for chip labels. It returns:

1. the color unchanged, if it already meets **4.5:1** (WCAG AA) against the
   background it will be drawn on; else
2. the supplied fallback, if *that* passes; else
3. pure black or pure white, whichever wins against that background.

An unparseable foreground returns the fallback unchanged.

This is why a Super Chat chip is readable on every theme: the chip's ground is a
tier color derived from the palette, and its text is the background role
corrected against that ground.

### `ReadableOn` — "keep the signal, make it legible"

Discarding a color for a neutral is right for body text and **wrong for a
signal**. The status bar is filled with the palette's `Accent`, and against a
saturated accent every role color fails 4.5:1 — so `Success`, `Warning`, `Error`,
and `Muted` all collapsed onto the same neutral. The quota meter's three-stage
escalation, the `STRETCHED` cadence label, and the dropped-message counter
rendered identically to the ordinary text beside them, on every built-in theme.

**A meter that cannot turn red is not a meter.**

`ReadableOn` keeps the hue and moves only the lightness, away from the
background, until the ratio clears:

- A near-neutral color — chroma below `0.15` — has no signal to protect and falls
  straight through to the neutral answer. Chroma is used rather than HSL
  saturation because saturation lies at the ends of the lightness range: a cream
  white reports a saturation near 0.37 while carrying almost no color, and
  relighting it to reach 4.5:1 concentrates that trace of warmth into a visible
  brown.
- The direction is chosen by comparing what pure black and pure white would each
  achieve against the background — *away from the background* is not the same as
  *lighter on a dark bar*. Against a mid-tone accent, lightening cannot reach 4.5
  at any lightness (pure white manages roughly 3.5) while darkening reaches it
  easily.
- The walk is sixteen steps of 0.05, which covers the whole lightness range from
  either end, nudging saturation up slightly as it goes because a hue washes out
  toward either extreme and a washed-out warning is exactly what is being fixed.
- A hue that cannot be rescued at any lightness falls back to the neutral answer,
  because an illegible signal is worse than a legible non-signal.

Red still reads as red. It just reads.

### Author colors

YouTube supplies **no** author color. `theme.IdentityColor` hashes the channel ID
(falling back to the display name) with FNV-64a, derives a hue and a saturation
from the hash, then picks the first candidate lightness that meets 4.5:1 against
**every** background the name will be drawn on — the chat surface and the raised
pane surface both. Dark canvases get a light identity color and light canvases a
dark one.

The result is stable across sessions and machines with no mutable per-user color
state anywhere in the app, and it is tested to stay readable on every one of the
58 presets.

## Related Color Helpers

| Function | Does |
| --- | --- |
| `Darken(color, amount)` | moves a color toward black; how the app canvas is derived from `Background` |
| `Mix(base, overlay, amount)` | blends toward an overlay; how a row is tinted with a chatter's identity color while staying a background |
| `Gradient(start, end, steps)` | interpolates a ramp; an unparseable endpoint yields `steps` copies of `start` rather than an error |
| `SeamlessGradient(start, end, steps)` | a mirrored start→end→start ramp whose ends match, so a rotating phase shows no seam |

Gradients drive chrome animation only. A single shared ~10 fps frame tick feeds
every effect, and `animation_mode = "off"` stops the tick entirely — the palette
still applies, nothing moves.

## Terminal Background

`yc` sets the terminal's own background with OSC 11 to the derived canvas color,
so the area outside the drawn frame matches the theme instead of showing the
terminal's default. It restores the previous background with OSC 111 on exit.

This happens **only in interactive mode**. Piped output and test output carry no
escape codes at all, which is what lets the screenshots under
[assets/screenshots/](assets/screenshots/) be generated from real `View()` output.

Restoring the background on exit is a [manual check](manual-validation.md) — no
automated test can observe what a terminal emulator did with an escape sequence.

## How To Verify This Page

```sh
# the preset table, straight from source
sed -n '/^var presets = map\[string\]Palette{/,/^}/p' internal/theme/presets.go

# the name list the picker and `yc profile list` render
go run ./cmd/yc profile list

# every preset invariant asserted on this page
go test ./internal/theme -v
```

| Test | Guarantees |
| --- | --- |
| `TestPresetNamesListsEveryBuiltInInStableOrder` | the picker order is deterministic |
| `TestPresetPalettesFillEveryRoleWithValidHex` | no preset ships an empty or malformed role |
| `TestPresetForegroundsAreReadableOnBackgroundAndSurface` | 4.5:1 on both surfaces, every preset |
| `TestPresetAccentsStandOutFromTheirBackground` | an accent cannot vanish into its canvas |
| `TestPresetPalettesAreDistinct` | no two presets are the same palette under two names |
| `TestReadableOnKeepsRoleColorsDistinctOnEveryPresetAccent` | the status bar's signals stay tellable apart |
| `TestReadableOnLeavesNearNeutralsNeutral` | a gray is not relit into a color |
| `TestIdentityColorStaysReadableOnEveryPreset` | hashed author colors are legible on all 58 |
| `TestContrastCorrectedForegroundHandlesInvalidInput` | bad hex degrades, never fails |
| `TestGradientDegradesSafelyForInvalidCustomColors` | a broken custom theme costs decoration only |

**Status: ready.** Palette resolution, contrast correction, the picker's key
handling, and every invariant above are covered by credential-free tests, and the
picker is fully usable in `yc chat --mock`. **Manual:** how a palette actually
looks in your terminal, and whether OSC 11/111 background restore works in your
emulator, can only be confirmed by a human — see
[manual-validation.md](manual-validation.md).
