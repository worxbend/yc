package theme

import "sort"

// presets holds the built-in named palettes. Most take each scheme's
// well-known published colors; yc, Claude, Codex, Btop, and Mono are authored
// for this project.
//
// Every palette fills nine roles, and upstream schemes rarely name all nine.
// Where a scheme has no color for a role - usually Border, sometimes Muted -
// the nearest published tone is reused or a quiet neighbor is derived from the
// scheme's own ramp rather than inventing a new hue.
//
// Light schemes map Background to the scheme's lightest base and Surface to
// the next one up the ramp. internal/app derives the application canvas by
// darkening Background, so a Surface at or above Background keeps panes
// reading as raised above the canvas in both light and dark palettes.
var presets = map[string]Palette{
	// yc is the YouTube-flavored house palette: a near-black canvas under
	// the platform red, so a Super Chat chip and an error state still read
	// apart from the accent that paints the status bar.
	"yc": {
		Background: "#0b0708",
		Foreground: "#f4eef0",
		Accent:     "#ff2d46",
		Muted:      "#948a8d",
		Border:     "#3a2429",
		Surface:    "#150e10",
		Warning:    "#ffb340",
		Error:      "#ff7a6b",
		Success:    "#5ce0a0",
	},

	"claude": {
		Background: "#1a1523",
		Foreground: "#f2ede4",
		Accent:     "#d97757",
		Muted:      "#948f9c",
		Border:     "#4a4358",
		Surface:    "#241d30",
		Warning:    "#e0a72e",
		Error:      "#e0685a",
		Success:    "#7fbf8e",
	},
	"codex": {
		Background: "#0d1117",
		Foreground: "#e6edf3",
		Accent:     "#3fb950",
		Muted:      "#8b949e",
		Border:     "#30363d",
		Surface:    "#161b22",
		Warning:    "#d29922",
		Error:      "#f85149",
		Success:    "#3fb950",
	},
	"btop": {
		Background: "#000000",
		Foreground: "#d3d3d3",
		Accent:     "#00ff00",
		Muted:      "#5a5a5a",
		Border:     "#3a3a3a",
		Surface:    "#101010",
		Warning:    "#ffdd33",
		Error:      "#ff3333",
		Success:    "#00ff00",
	},
	"nord": {
		Background: "#2e3440",
		Foreground: "#eceff4",
		Accent:     "#88c0d0",
		Muted:      "#4c566a",
		Border:     "#3b4252",
		Surface:    "#3b4252",
		Warning:    "#ebcb8b",
		Error:      "#bf616a",
		Success:    "#a3be8c",
	},
	"dracula": {
		Background: "#282a36",
		Foreground: "#f8f8f2",
		Accent:     "#bd93f9",
		Muted:      "#6272a4",
		Border:     "#44475a",
		Surface:    "#343746",
		Warning:    "#f1fa8c",
		Error:      "#ff5555",
		Success:    "#50fa7b",
	},
	"gruvbox": {
		Background: "#282828",
		Foreground: "#ebdbb2",
		Accent:     "#fe8019",
		Muted:      "#928374",
		Border:     "#3c3836",
		Surface:    "#32302f",
		Warning:    "#fabd2f",
		Error:      "#fb4934",
		Success:    "#b8bb26",
	},
	"solarized-dark": {
		Background: "#002b36",
		Foreground: "#839496",
		Accent:     "#268bd2",
		Muted:      "#586e75",
		Border:     "#073642",
		Surface:    "#073642",
		Warning:    "#b58900",
		Error:      "#dc322f",
		Success:    "#859900",
	},
	"monokai": {
		Background: "#272822",
		Foreground: "#f8f8f2",
		Accent:     "#f92672",
		Muted:      "#75715e",
		Border:     "#3e3d32",
		Surface:    "#3e3d32",
		Warning:    "#e6db74",
		Error:      "#f92672",
		Success:    "#a6e22e",
	},
	"one-dark": {
		Background: "#282c34",
		Foreground: "#abb2bf",
		Accent:     "#61afef",
		Muted:      "#5c6370",
		Border:     "#3e4451",
		Surface:    "#21252b",
		Warning:    "#e5c07b",
		Error:      "#e06c75",
		Success:    "#98c379",
	},
	"tokyo-night": {
		Background: "#1a1b26",
		Foreground: "#c0caf5",
		Accent:     "#7aa2f7",
		Muted:      "#565f89",
		Border:     "#414868",
		Surface:    "#24283b",
		Warning:    "#e0af68",
		Error:      "#f7768e",
		Success:    "#9ece6a",
	},
	"catppuccin-mocha": {
		Background: "#1e1e2e",
		Foreground: "#cdd6f4",
		Accent:     "#cba6f7",
		Muted:      "#a6adc8",
		Border:     "#45475a",
		Surface:    "#313244",
		Warning:    "#f9e2af",
		Error:      "#f38ba8",
		Success:    "#a6e3a1",
	},
	"rose-pine": {
		Background: "#191724",
		Foreground: "#e0def4",
		Accent:     "#c4a7e7",
		Muted:      "#6e6a86",
		Border:     "#403d52",
		Surface:    "#26233a",
		Warning:    "#f6c177",
		Error:      "#eb6f92",
		Success:    "#31748f",
	},
	"mono": {
		Background: "#000000",
		Foreground: "#ffffff",
		Accent:     "#ffffff",
		Muted:      "#808080",
		Border:     "#808080",
		Surface:    "#1a1a1a",
		Warning:    "#c0c0c0",
		Error:      "#ffffff",
		Success:    "#ffffff",
	},
	"catppuccin-macchiato": {
		Background: "#24273a",
		Foreground: "#cad3f5",
		Accent:     "#c6a0f6",
		Muted:      "#a5adcb",
		Border:     "#494d64",
		Surface:    "#363a4f",
		Warning:    "#eed49f",
		Error:      "#ed8796",
		Success:    "#a6da95",
	},
	"catppuccin-frappe": {
		Background: "#303446",
		Foreground: "#c6d0f5",
		Accent:     "#ca9ee6",
		Muted:      "#a5adce",
		Border:     "#51576d",
		Surface:    "#414559",
		Warning:    "#e5c890",
		Error:      "#e78284",
		Success:    "#a6d189",
	},
	"catppuccin-latte": {
		Background: "#eff1f5",
		Foreground: "#4c4f69",
		Accent:     "#8839ef",
		Muted:      "#6c6f85",
		Border:     "#ccd0da",
		Surface:    "#e6e9ef",
		Warning:    "#df8e1d",
		Error:      "#d20f39",
		Success:    "#40a02b",
	},
	"rose-pine-moon": {
		Background: "#232136",
		Foreground: "#e0def4",
		Accent:     "#c4a7e7",
		Muted:      "#6e6a86",
		Border:     "#44415a",
		Surface:    "#2a273f",
		Warning:    "#f6c177",
		Error:      "#eb6f92",
		Success:    "#3e8fb0",
	},
	"rose-pine-dawn": {
		Background: "#faf4ed",
		Foreground: "#575279",
		Accent:     "#907aa9",
		Muted:      "#797593",
		Border:     "#dfdad9",
		Surface:    "#fffaf3",
		Warning:    "#ea9d34",
		Error:      "#b4637a",
		Success:    "#286983",
	},
	"gruvbox-light": {
		Background: "#fbf1c7",
		Foreground: "#3c3836",
		Accent:     "#af3a03",
		Muted:      "#7c6f64",
		Border:     "#d5c4a1",
		Surface:    "#f2e5bc",
		Warning:    "#b57614",
		Error:      "#9d0006",
		Success:    "#79740e",
	},
	// Solarized Light's own panel tone (base2 #eee8d5) drops base01 body text
	// to 4.39 contrast, under the bar yc holds every other role to, so the
	// pane surface sits one step lighter on the same ramp.
	"solarized-light": {
		Background: "#fdf6e3",
		Foreground: "#586e75",
		Accent:     "#268bd2",
		Muted:      "#93a1a1",
		Border:     "#ded8c4",
		Surface:    "#f5efdd",
		Warning:    "#b58900",
		Error:      "#dc322f",
		Success:    "#859900",
	},
	"github-light": {
		Background: "#ffffff",
		Foreground: "#24292f",
		Accent:     "#0969da",
		Muted:      "#57606a",
		Border:     "#d0d7de",
		Surface:    "#f6f8fa",
		Warning:    "#9a6700",
		Error:      "#cf222e",
		Success:    "#1a7f37",
	},
	"everforest": {
		Background: "#2d353b",
		Foreground: "#d3c6aa",
		Accent:     "#a7c080",
		Muted:      "#859289",
		Border:     "#3d484d",
		Surface:    "#343f44",
		Warning:    "#dbbc7f",
		Error:      "#e67e80",
		Success:    "#83c092",
	},
	"kanagawa": {
		Background: "#1f1f28",
		Foreground: "#dcd7ba",
		Accent:     "#7e9cd8",
		Muted:      "#727169",
		Border:     "#2d4f67",
		Surface:    "#2a2a37",
		Warning:    "#e6c384",
		Error:      "#e82424",
		Success:    "#98bb6c",
	},
	"ayu-dark": {
		Background: "#0d1017",
		Foreground: "#bfbdb6",
		Accent:     "#e6b450",
		Muted:      "#7b8391",
		Border:     "#1f242c",
		Surface:    "#131721",
		Warning:    "#ffb454",
		Error:      "#d95757",
		Success:    "#aad94c",
	},
	"ayu-mirage": {
		Background: "#1f2430",
		Foreground: "#cccac2",
		Accent:     "#ffcc66",
		Muted:      "#8a94a6",
		Border:     "#33415e",
		Surface:    "#242936",
		Warning:    "#ffd173",
		Error:      "#f28779",
		Success:    "#d5ff80",
	},
	"night-owl": {
		Background: "#011627",
		Foreground: "#d6deeb",
		Accent:     "#82aaff",
		Muted:      "#7c93a8",
		Border:     "#1d3b53",
		Surface:    "#0b2942",
		Warning:    "#ecc48d",
		Error:      "#ef5350",
		Success:    "#22da6e",
	},
	"palenight": {
		Background: "#292d3e",
		Foreground: "#a6accd",
		Accent:     "#c792ea",
		Muted:      "#676e95",
		Border:     "#3a3f58",
		Surface:    "#32374d",
		Warning:    "#ffcb6b",
		Error:      "#f07178",
		Success:    "#c3e88d",
	},
	"synthwave-84": {
		Background: "#262335",
		Foreground: "#f0eff1",
		Accent:     "#ff7edb",
		Muted:      "#848bbd",
		Border:     "#34294f",
		Surface:    "#2a2139",
		Warning:    "#fede5d",
		Error:      "#fe4450",
		Success:    "#72f1b8",
	},
	"oceanic-next": {
		Background: "#1b2b34",
		Foreground: "#c0c5ce",
		Accent:     "#6699cc",
		Muted:      "#8b98a6",
		Border:     "#4f5b66",
		Surface:    "#343d46",
		Warning:    "#fac863",
		Error:      "#ec5f67",
		Success:    "#99c794",
	},
	"nightfox": {
		Background: "#192330",
		Foreground: "#cdcecf",
		Accent:     "#719cd6",
		Muted:      "#71839b",
		Border:     "#29394f",
		Surface:    "#212e3f",
		Warning:    "#dbc074",
		Error:      "#c94f6d",
		Success:    "#81b29a",
	},
	"zenburn": {
		Background: "#3f3f3f",
		Foreground: "#dcdccc",
		Accent:     "#f0dfaf",
		Muted:      "#989890",
		Border:     "#5f5f5f",
		Surface:    "#4f4f4f",
		Warning:    "#dfaf8f",
		Error:      "#cc9393",
		Success:    "#7f9f7f",
	},
	"cobalt2": {
		Background: "#193549",
		Foreground: "#e1efff",
		Accent:     "#ffc600",
		Muted:      "#93b3cc",
		Border:     "#234e6d",
		Surface:    "#1f4662",
		Warning:    "#ff9d00",
		Error:      "#ff628c",
		Success:    "#3ad900",
	},
	"horizon": {
		Background: "#1c1e26",
		Foreground: "#d5d8da",
		Accent:     "#e95678",
		Muted:      "#8a8daf",
		Border:     "#2e303e",
		Surface:    "#232530",
		Warning:    "#fab795",
		Error:      "#f43e5c",
		Success:    "#29d398",
	},

	// Vibrant dark presets. These are authored for yc rather than ported
	// from an upstream scheme: each starts from a near-black or true-black
	// canvas and carries one saturated accent, for streamers who want the
	// terminal to read as part of the overlay instead of disappearing into
	// it. Every one clears the same contrast bar as the ported schemes.
	"neon-tokyo": {
		// Near-black with hot magenta and mint, the after-dark end of the
		// Tokyo Night family rather than its slate blues.
		Background: "#07060d",
		Foreground: "#eae6ff",
		Accent:     "#ff2bd6",
		Muted:      "#7b7596",
		Border:     "#2b2447",
		Surface:    "#100c1c",
		Warning:    "#ffc531",
		Error:      "#ff4d6d",
		Success:    "#3df5c4",
	},
	"vaporwave": {
		// Deep violet-black under pink and cyan.
		Background: "#0d0618",
		Foreground: "#f2e9ff",
		Accent:     "#ff6ad5",
		Muted:      "#8d7fa8",
		Border:     "#3a2a5c",
		Surface:    "#170c28",
		Warning:    "#ffd166",
		Error:      "#ff5c8a",
		Success:    "#61e8e1",
	},
	"hotline": {
		// Black-violet with hot pink and teal.
		Background: "#0a0410",
		Foreground: "#ffe9f4",
		Accent:     "#ff2e88",
		Muted:      "#96738c",
		Border:     "#3d1230",
		Surface:    "#16081d",
		Warning:    "#ffb627",
		Error:      "#ff4d4d",
		Success:    "#00e0c7",
	},
	"ultraviolet": {
		// Black with electric purple.
		Background: "#08040f",
		Foreground: "#ece2ff",
		Accent:     "#a855ff",
		Muted:      "#7d6f9c",
		Border:     "#2d1a4d",
		Surface:    "#110a1e",
		Warning:    "#ffcc4d",
		Error:      "#ff4f81",
		Success:    "#4de8b0",
	},
	"cyberpunk": {
		// Black with warning-sign yellow and electric cyan.
		Background: "#050505",
		Foreground: "#f4f4e8",
		Accent:     "#fcee0a",
		Muted:      "#8a8a7a",
		Border:     "#2e2e18",
		Surface:    "#0f0f0a",
		Warning:    "#ff9f1c",
		Error:      "#ff003c",
		Success:    "#00f0ff",
	},
	"matrix": {
		// Pure black with phosphor green, one hue for accent and success
		// because the reference it comes from only had the one.
		Background: "#000000",
		Foreground: "#c8ffc8",
		Accent:     "#00ff41",
		Muted:      "#4f8f4f",
		Border:     "#0f3d0f",
		Surface:    "#050f05",
		Warning:    "#b6ff00",
		Error:      "#ff5f56",
		Success:    "#00ff41",
	},
	"toxic": {
		// Near-black with acid lime.
		Background: "#040703",
		Foreground: "#e8ffd9",
		Accent:     "#aaff00",
		Muted:      "#7f9a66",
		Border:     "#1f3312",
		Surface:    "#0a1006",
		Warning:    "#ffe600",
		Error:      "#ff4d3d",
		Success:    "#39ff88",
	},
	"amber-crt": {
		// Black with amber, after monochrome amber-phosphor terminals.
		// Error and success step off the hue so they stay distinguishable.
		Background: "#0a0600",
		Foreground: "#ffd799",
		Accent:     "#ffab00",
		Muted:      "#9a7440",
		Border:     "#3d2a08",
		Surface:    "#140d02",
		Warning:    "#ffd500",
		Error:      "#ff6b4a",
		Success:    "#b8e04a",
	},
	"midnight-ember": {
		// Black with ember orange, the warm counterpart to deep-ocean.
		Background: "#0a0705",
		Foreground: "#ffe8d6",
		Accent:     "#ff7a33",
		Muted:      "#9c7f6b",
		Border:     "#3a2618",
		Surface:    "#150e09",
		Warning:    "#ffc233",
		Error:      "#ff5a4d",
		Success:    "#7fd98a",
	},
	"blood-moon": {
		// Near-black with crimson; warmer and darker than Dracula's red.
		Background: "#0b0406",
		Foreground: "#f6e3e6",
		Accent:     "#ff3b52",
		Muted:      "#96707a",
		Border:     "#3b1620",
		Surface:    "#160a0e",
		Warning:    "#ffab4a",
		Error:      "#ff5c6c",
		Success:    "#59d99d",
	},
	"deep-ocean": {
		// Very dark blue with aqua, the deepest of the blue presets.
		Background: "#020914",
		Foreground: "#dcefff",
		Accent:     "#00d4ff",
		Muted:      "#5f7f99",
		Border:     "#123049",
		Surface:    "#06141f",
		Warning:    "#ffc857",
		Error:      "#ff5d73",
		Success:    "#2ee6a8",
	},
	"arctic-neon": {
		// Near-black with ice blue; the coolest of the neon presets.
		Background: "#04080d",
		Foreground: "#e6f4ff",
		Accent:     "#4dd8ff",
		Muted:      "#6f8799",
		Border:     "#183040",
		Surface:    "#0a1219",
		Warning:    "#ffd166",
		Error:      "#ff6b81",
		Success:    "#5df2b5",
	},
	"carbon": {
		// True black with a single orange accent and otherwise system
		// colors, for terminals where anything but pure black shows a seam.
		Background: "#000000",
		Foreground: "#f0f0f0",
		Accent:     "#ff5f1f",
		Muted:      "#8c8c8c",
		Border:     "#262626",
		Surface:    "#0d0d0d",
		Warning:    "#ffbf00",
		Error:      "#ff3b30",
		Success:    "#32d74b",
	},

	// Second batch of authored dark presets, in hues the first batch left
	// thin: blues and blue-violets, jewel greens, gold, and two low-chroma
	// looks for anyone who wants a black terminal without a neon accent.
	"plasma": {
		// Electric violet-blue on black.
		Background: "#06050f",
		Foreground: "#e6e3ff",
		Accent:     "#6c5cff",
		Muted:      "#75729c",
		Border:     "#231f4a",
		Surface:    "#0d0b1c",
		Warning:    "#ffc93c",
		Error:      "#ff5470",
		Success:    "#3ce8b0",
	},
	"sapphire": {
		// Bright blue on near-black; the saturated counterpart to the
		// slate blues the ported schemes favor.
		Background: "#03070f",
		Foreground: "#dfeaff",
		Accent:     "#2979ff",
		Muted:      "#65799c",
		Border:     "#122647",
		Surface:    "#070e1c",
		Warning:    "#ffc233",
		Error:      "#ff5c7a",
		Success:    "#31dba0",
	},
	"orchid": {
		// Pink-purple, between neon-tokyo's magenta and ultraviolet.
		Background: "#08040a",
		Foreground: "#f7e4ff",
		Accent:     "#e56cf0",
		Muted:      "#8f6b99",
		Border:     "#341542",
		Surface:    "#120818",
		Warning:    "#ffcc52",
		Error:      "#ff5c8a",
		Success:    "#52e0b8",
	},
	"ruby": {
		// Pink-red on black, cooler than blood-moon's crimson.
		Background: "#0a0308",
		Foreground: "#ffe0ec",
		Accent:     "#f50057",
		Muted:      "#9c6b81",
		Border:     "#3d0f28",
		Surface:    "#150610",
		Warning:    "#ffbf47",
		Error:      "#ff5c7a",
		Success:    "#4de3a8",
	},
	"magma": {
		// Lava red-orange, the hottest of the warm presets.
		Background: "#0b0503",
		Foreground: "#ffe4d6",
		Accent:     "#ff3d00",
		Muted:      "#9c7263",
		Border:     "#3d1a0d",
		Surface:    "#160a05",
		Warning:    "#ffab2e",
		Error:      "#ff6347",
		Success:    "#8fd97f",
	},
	"bullion": {
		// Metallic gold on black; warmer and less orange than amber-crt.
		Background: "#0a0803",
		Foreground: "#fff3d4",
		Accent:     "#ffcf33",
		Muted:      "#9c8a5e",
		Border:     "#3a2f10",
		Surface:    "#141005",
		Warning:    "#ffe066",
		Error:      "#ff6b52",
		Success:    "#a8e05f",
	},
	"emerald-noir": {
		// Deep emerald on black, a jewel tone rather than matrix's
		// phosphor or toxic's acid.
		Background: "#020a07",
		Foreground: "#dcf6ea",
		Accent:     "#00d68f",
		Muted:      "#5f8c78",
		Border:     "#0f3327",
		Surface:    "#06130e",
		Warning:    "#ffc94d",
		Error:      "#ff5d6e",
		Success:    "#3ff2a8",
	},
	"mint-noir": {
		// Bright mint on black. Accent and success share the hue
		// because a mint scheme has one signature color.
		Background: "#040b09",
		Foreground: "#e0fff2",
		Accent:     "#5effc4",
		Muted:      "#6b9c8a",
		Border:     "#12352a",
		Surface:    "#081511",
		Warning:    "#ffd75e",
		Error:      "#ff6b7d",
		Success:    "#5effc4",
	},
	"abyss": {
		// Turquoise on a black-teal canvas; deep-ocean's colder,
		// greener neighbor.
		Background: "#010a0c",
		Foreground: "#d6f5f2",
		Accent:     "#00e5cc",
		Muted:      "#5c8a8a",
		Border:     "#0d3033",
		Surface:    "#041416",
		Warning:    "#ffcb47",
		Error:      "#ff5f70",
		Success:    "#4dffd2",
	},
	"spectre": {
		// Pale cyan on true black; the accent is light rather than
		// saturated, for a colder read than the neon presets.
		Background: "#000000",
		Foreground: "#e8f6fa",
		Accent:     "#9fe8ff",
		Muted:      "#6b8592",
		Border:     "#1c2b33",
		Surface:    "#080d10",
		Warning:    "#ffd98f",
		Error:      "#ff8fa3",
		Success:    "#8fffd6",
	},
	"obsidian": {
		// True black with a silver accent and color kept for the roles
		// that carry meaning, for anyone who wants the chrome quiet and
		// only warnings and errors to speak.
		Background: "#000000",
		Foreground: "#e8eaed",
		Accent:     "#cfd8dc",
		Muted:      "#7a8288",
		Border:     "#22262a",
		Surface:    "#0b0d0f",
		Warning:    "#ffca28",
		Error:      "#ff5252",
		Success:    "#69f0ae",
	},
}

// Presets returns every built-in palette keyed by its config name.
//
// The set is platform-neutral and ports from twi with one addition: the
// YouTube-flavored "yc" palette. Callers must not mutate the returned map.
func Presets() map[string]Palette {
	return presets
}

// PresetNames returns every preset name in sorted order, for the theme page and
// `yc profile list`.
func PresetNames() []string {
	names := make([]string, 0, len(presets))
	for name := range presets {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
