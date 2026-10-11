package ui

import (
	"math"
	"strconv"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/allisonhere/tideui"
)

// contrast is the WCAG 2 contrast ratio, the measure TideUI uses to decide
// whether to keep a theme's colour or swap in black or white.
func contrast(t *testing.T, a, b lipgloss.Color) float64 {
	t.Helper()
	lum := func(c lipgloss.Color) float64 {
		h := strings.TrimPrefix(string(c), "#")
		if len(h) != 6 {
			t.Fatalf("colour %q is not #rrggbb", c)
		}
		var ch [3]float64
		for i := range ch {
			v, err := strconv.ParseUint(h[2*i:2*i+2], 16, 8)
			if err != nil {
				t.Fatalf("colour %q: %v", c, err)
			}
			x := float64(v) / 255
			if x <= 0.04045 {
				ch[i] = x / 12.92
			} else {
				ch[i] = math.Pow((x+0.055)/1.055, 2.4)
			}
		}
		return 0.2126*ch[0] + 0.7152*ch[1] + 0.0722*ch[2]
	}
	la, lb := lum(a), lum(b)
	return (math.Max(la, lb) + 0.05) / (math.Min(la, lb) + 0.05)
}

// TestShoalThemesKeepTheirColoursLegible holds each of shoal's themes to the
// thresholds TideUI applies: below them it replaces the colour, and the
// table would lose the meaning shoal gives it.
func TestShoalThemesKeepTheirColoursLegible(t *testing.T) {
	for _, th := range shoalThemes {
		for _, f := range []struct {
			name string
			c    lipgloss.Color
		}{{"Bg", th.Bg}, {"Fg", th.Fg}, {"Border", th.Border}, {"BorderFocus", th.BorderFocus}, {"Selected", th.Selected},
			{"Unread", th.Unread}, {"Dimmed", th.Dimmed}, {"StatusBar", th.StatusBar}, {"StatusFg", th.StatusFg},
			{"Error", th.Error}, {"Overlay", th.Overlay}, {"OverlayBorder", th.OverlayBorder}} {
			if f.c == "" {
				t.Errorf("%s: %s is not set", th.Name, f.name)
			}
		}
		for _, c := range []struct {
			what string
			a, b lipgloss.Color
			min  float64
		}{
			{"text on the background", th.Fg, th.Bg, 4.5},
			{"stale and muted rows (Dimmed)", th.Dimmed, th.Bg, 3},
			{"received and done (Unread)", th.Unread, th.Bg, 3},
			{"not-answering rows, drawn in the status bar's error colour", th.Error, th.StatusBar, 4.5},
			{"not-answering rows on the background", th.Error, th.Bg, 3},
			{"status bar text", th.StatusFg, th.StatusBar, 4.5},
		} {
			if r := contrast(t, c.a, c.b); r < c.min {
				t.Errorf("%s: %s has contrast %.2f, below the %.1f TideUI keeps", th.Name, c.what, r, c.min)
			}
		}
		if !isDarkTheme(t, th) {
			t.Errorf("%s: shoal's themes are dark ones", th.Name)
		}
	}
}

func isDarkTheme(t *testing.T, th tideui.Theme) bool {
	return contrast(t, th.Bg, "#000000") < contrast(t, th.Bg, "#FFFFFF")
}

func TestThemeNamesAreUniqueAndFitThePicker(t *testing.T) {
	seen := map[string]bool{}
	for _, name := range ThemeNames() {
		if seen[name] {
			t.Errorf("two themes are called %s", name)
		}
		seen[name] = true
	}
	for _, th := range shoalThemes {
		// The picker is themePickerWidth wide, less its border and padding.
		if len(th.Name) > themePickerWidth-6 {
			t.Errorf("%s is too long for the picker", th.Name)
		}
	}
	if got, want := len(Themes()), len(tideui.BuiltinThemes)-len(droppedBuiltins)+len(shoalThemes); got != want {
		t.Errorf("Themes() = %d, want TideUI's %d less %d dropped, and shoal's %d", got, len(tideui.BuiltinThemes), len(droppedBuiltins), len(shoalThemes))
	}
	if len(shoalThemes) != 6 {
		t.Errorf("shoal offers %d themes of its own, want the six chosen", len(shoalThemes))
	}
}

func TestDroppedBuiltinsAreGoneEverywhere(t *testing.T) {
	for name := range droppedBuiltins {
		if _, ok := tideui.ThemeByName(name); !ok {
			t.Errorf("%s is not a TideUI theme, so there is nothing to drop; check the name", name)
		}
		if _, ok := ThemeByName(name); ok {
			t.Errorf("%s was dropped but can still be chosen", name)
		}
	}
}

func TestThemeByNameFindsShoalThemesAndReportsUnknownOnes(t *testing.T) {
	if th, ok := ThemeByName("kanagawa-wave"); !ok || th.Name != "kanagawa-wave" {
		t.Errorf("a shoal theme should be found: %v %s", ok, th.Name)
	}
	if th, ok := ThemeByName("nord"); !ok || th.Name != "nord" {
		t.Errorf("a TideUI theme should be found: %v %s", ok, th.Name)
	}
	if th, ok := ThemeByName("no-such-theme"); ok || th.Name != DefaultTheme {
		t.Errorf("an unknown theme should be reported and fall back to %s: %v %s", DefaultTheme, ok, th.Name)
	}
}

func TestARememberedThemeThatIsGoneIsSaid(t *testing.T) {
	a := newApp(Options{Theme: "a-theme-since-removed"})
	if a.theme != DefaultTheme {
		t.Errorf("theme = %s, want %s", a.theme, DefaultTheme)
	}
	if len(a.events) != 1 || !strings.Contains(a.events[0].Message, "a-theme-since-removed is not one shoal offers") {
		t.Errorf("the log should say why the colours changed: %+v", a.events)
	}
	if b := newApp(Options{Theme: "vesper"}); len(b.events) != 0 || b.theme != "vesper" {
		t.Errorf("a known shoal theme starts quietly: %s %+v", b.theme, b.events)
	}
}

func TestThePickerOffersShoalsThemesAndKeepsOne(t *testing.T) {
	a, _ := sized(t, 120, 40)
	saved := ""
	a.opts.SaveTheme = func(name string) error { saved = name; return nil }
	a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("t")})
	// The last theme in the list is shoal's last.
	for range Themes() {
		a.Update(tea.KeyMsg{Type: tea.KeyDown})
	}
	last := shoalThemes[len(shoalThemes)-1].Name
	if got := a.renderer.Styles.Theme.Name; got != last {
		t.Fatalf("previewing %s at the end of the list, want %s", got, last)
	}
	_, cmd := a.Update(tea.KeyMsg{Type: tea.KeyEnter})
	run(a, cmd)
	if a.theme != last || saved != last {
		t.Errorf("Enter should keep and remember %s: theme %s, saved %q", last, a.theme, saved)
	}
}
