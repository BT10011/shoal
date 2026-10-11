package ui

import "github.com/allisonhere/tideui"

// shoal's own themes, offered after TideUI's built-ins. Each one's colours
// come from its project's published palette, named beside it with its
// licence; where a field had no direct counterpart, the comment says what
// was used instead. Every theme must keep the colours shoal relies on
// legible (TestShoalThemesKeepTheirColoursLegible): TideUI swaps in black
// or white for a colour that falls short, which would make a not-answering
// row lose its red or a stale one stop looking dim.
//
// Chosen by the maintainer on 2026-10-11 from twelve previewed in the
// picker (PROJECT_PLAN.md, Release 2 item 4).
var shoalThemes = []tideui.Theme{
	// Kanagawa Wave, rebelot/kanagawa.nvim (MIT, Tommaso Laurenzi). Error is peachRed: the
	// palette's samuraiRed is too dark to read on the status bar.
	{Name: "kanagawa-wave", Bg: "#1F1F28", Fg: "#DCD7BA", Border: "#54546D", BorderFocus: "#7E9CD8",
		Selected: "#7E9CD8", Unread: "#98BB6C", Dimmed: "#727169", StatusBar: "#16161D", StatusFg: "#C8C093",
		Error: "#FF5D62", Overlay: "#2A2A37", OverlayBorder: "#7E9CD8"},
	// Ayu Mirage, ayu-theme/ayu-colors (MIT, Konstantin Pschera). The status bar text is the
	// editor's, since the palette's own ui.fg is too faint there.
	{Name: "ayu-mirage", Bg: "#242936", Fg: "#CCCAC2", Border: "#707A8C", BorderFocus: "#FFCC66",
		Selected: "#FFCC66", Unread: "#87D96C", Dimmed: "#707A8C", StatusBar: "#1F2430", StatusFg: "#CCCAC2",
		Error: "#FF6666", Overlay: "#1C212C", OverlayBorder: "#FFCC66"},
	// Oxocarbon, nyoom-engineering/oxocarbon.nvim (MIT, Riccardo Mazzarini); greys blended in
	// HSLuv as the palette does. Dimmed sits between base03 and base04,
	// since base03 is too dark to read; Error is base12, as base10 is on
	// the status bar.
	{Name: "oxocarbon", Bg: "#161616", Fg: "#F2F2F2", Border: "#393939", BorderFocus: "#78A9FF",
		Selected: "#78A9FF", Unread: "#42BE65", Dimmed: "#6F6F6F", StatusBar: "#262626", StatusFg: "#F2F2F2",
		Error: "#FF7EB6", Overlay: "#262626", OverlayBorder: "#78A9FF"},
	// Vesper, raunofreiberg/vesper (MIT, Rauno Freiberg).
	{Name: "vesper", Bg: "#101010", Fg: "#FFFFFF", Border: "#282828", BorderFocus: "#FFC799",
		Selected: "#FFC799", Unread: "#99FFE4", Dimmed: "#7E7E7E", StatusBar: "#161616", StatusFg: "#A0A0A0",
		Error: "#FF8080", Overlay: "#1C1C1C", OverlayBorder: "#FFC799"},
	// Moonfly, bluz71/vim-moonfly-colors (MIT, its authors).
	{Name: "moonfly", Bg: "#080808", Fg: "#C6C6C6", Border: "#323437", BorderFocus: "#80A0FF",
		Selected: "#80A0FF", Unread: "#8CC85F", Dimmed: "#808080", StatusBar: "#1C1C1C", StatusFg: "#C6C6C6",
		Error: "#FF5D5D", Overlay: "#262626", OverlayBorder: "#80A0FF"},
	// SynthWave '84, robb0wen/synthwave-vscode (MIT, Robb Owen). Error is its coral,
	// as its red is too dark on the status bar.
	{Name: "synthwave-84", Bg: "#262335", Fg: "#FFFFFF", Border: "#495495", BorderFocus: "#FF7EDB",
		Selected: "#FF7EDB", Unread: "#72F1B8", Dimmed: "#848BBD", StatusBar: "#241B2F", StatusFg: "#B6B1B1",
		Error: "#F97E72", Overlay: "#2A2139", OverlayBorder: "#FF7EDB"},
}

// droppedBuiltins are TideUI themes shoal does not offer, chosen by the
// maintainer on 2026-10-11 when the list grew.
var droppedBuiltins = map[string]bool{
	"catppuccin-frappe":       true,
	"catppuccin-macchiato":    true,
	"tokyo-night-day":         true,
	"rose-pine-dawn":          true,
	"lavender-fields-forever": true,
}

// Themes lists every theme shoal offers: TideUI's built-ins less the
// dropped ones, then shoal's.
func Themes() []tideui.Theme {
	var out []tideui.Theme
	for _, t := range tideui.BuiltinThemes {
		if !droppedBuiltins[t.Name] {
			out = append(out, t)
		}
	}
	return append(out, shoalThemes...)
}

// DefaultTheme is used when none is named, or the one named is unknown.
const DefaultTheme = "catppuccin-mocha"

// ThemeByName finds a theme in Themes. Unlike tideui.ThemeByName it
// reports an unknown name, so the caller can say so rather than quietly
// showing another theme.
func ThemeByName(name string) (tideui.Theme, bool) {
	for _, t := range Themes() {
		if t.Name == name {
			return t, true
		}
	}
	t, _ := tideui.ThemeByName(DefaultTheme)
	return t, false
}

// ThemeNames lists the names of every theme, in picker order.
func ThemeNames() []string {
	var out []string
	for _, t := range Themes() {
		out = append(out, t.Name)
	}
	return out
}
