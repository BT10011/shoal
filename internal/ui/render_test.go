package ui

import (
	"math/rand"
	"strings"
	"testing"
	"time"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"

	"github.com/BT10011/shoal/internal/model"
)

// widthsDiffer measures s with both width models directly, so these tests do
// not lean on the code they check.
func widthsDiffer(s string) bool {
	return ansi.StringWidth(s) != runewidth.StringWidth(s)
}

// firstBadPrefix returns the shortest prefix of s, cut at a rune, that the
// two models measure differently. The whole string counts as a prefix. A
// string is safe for ansi.Truncate to cut anywhere only if there is none.
func firstBadPrefix(s string) (string, bool) {
	rs := []rune(s)
	for i := 1; i <= len(rs); i++ {
		if p := string(rs[:i]); widthsDiffer(p) {
			return p, true
		}
	}
	return "", false
}

// A canary as much as a test: these are ordinary names in several scripts, and
// if a dependency bump ever makes the two models disagree about one, this
// fails and someone decides whether that script is worth losing on screen.
func TestDisplaySafeLeavesTextTheModelsAgreeOnAlone(t *testing.T) {
	for _, s := range []string{
		"", "172.16.10.20", "office-nas.local", "00:00:5e:00:53:01", "a ~ z {ok}?",
		"café", "Ноутбук", "日本語のプリンター", "한국어", "क\u093e", "\U0001F44D",
	} {
		if got := displaySafe(s); got != s {
			t.Errorf("displaySafe(%q) = %q, want it unchanged", s, got)
		}
	}
}

func TestDisplaySafeDoesNotAllocateForPlainASCII(t *testing.T) {
	if n := testing.AllocsPerRun(100, func() { displaySafe("office-nas.local") }); n != 0 {
		t.Errorf("printable ASCII allocated %v times per call, want 0", n)
	}
}

func TestDisplaySafeSpellsAFlagAsItsCountryCode(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"stage-laptop \U0001F1EB\U0001F1F7", "stage-laptop FR"},
		{"\U0001F1EC\U0001F1E7\U0001F1EF\U0001F1F5", "GBJP"},
		{"\U0001F1E6", "A"},
	} {
		got := displaySafe(c.in)
		if got != c.want {
			t.Errorf("displaySafe(%+q) = %q, want %q", c.in, got, c.want)
		}
		if widthsDiffer(got) {
			t.Errorf("%q still measures differently under the two models", got)
		}
	}
}

func TestDisplaySafeDropsSkinTonesButKeepsTheEmoji(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"\U0001F44D\U0001F3FD", "\U0001F44D"},
		{"a\U0001F3FF", "a"},
		// A joined sequence falls apart into its parts.
		{"\U0001F468\u200d\U0001F469\u200d\U0001F467", "\U0001F468\U0001F469\U0001F467"},
	} {
		if got := displaySafe(c.in); got != c.want {
			t.Errorf("displaySafe(%+q) = %+q, want %+q", c.in, got, c.want)
		}
	}
}

func TestDisplaySafeReplacesARuneTheModelsDisagreeAbout(t *testing.T) {
	// Recent emoji and symbols such as the Yijing hexagrams are what one
	// table calls wide and the other does not. Which of these is disputed
	// depends on the library versions built in, so use the first that is.
	var r rune
	for _, c := range []rune{0x1FAE9, 0x1FAC6, 0x1FA89, 0x4DC0, 0x2630, 0x1D300} {
		if widthsDiffer(string(c)) {
			r = c
			break
		}
	}
	if r == 0 {
		t.Skip("the two width models agree about every candidate in the versions built in")
	}
	for _, c := range []struct{ in, want string }{
		{"x" + string(r) + "y", "x?y"},
		{string(r) + string(r) + "z", "?z"}, // a run of them is one
		{string(r) + "\ufe0f" + string(r), "?"},
	} {
		if got := displaySafe(c.in); got != c.want {
			t.Errorf("displaySafe(%+q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestDisplaySafeRepairsWhatOnlyShowsInASequence(t *testing.T) {
	// A spacing mark after a Latin letter: x/ansi gives it a cell of its own
	// and go-runewidth attaches it to the letter. After a letter of its own
	// script the two agree, which TestDisplaySafeLeavesTextTheModelsAgreeOnAlone
	// holds on to.
	in := "a\u093e"
	if !widthsDiffer(in) {
		t.Skip("the two width models agree about it in the versions built in")
	}
	if got := displaySafe(in); got != "a?" {
		t.Errorf("displaySafe(%+q) = %+q, want %+q", in, got, "a?")
	}
}

// TestDisplaySafeMakesTheModelsAgreeOnEveryRune is the exhaustive check behind
// the unit cases above: whatever single rune a name carries, what is drawn
// measures the same either way.
func TestDisplaySafeMakesTheModelsAgreeOnEveryRune(t *testing.T) {
	bad := 0
	for r := rune(0x20); r <= unicode.MaxRune; r++ {
		if r >= 0xD800 && r <= 0xDFFF {
			continue // not a rune, and a Go string cannot hold one
		}
		if p, isBad := firstBadPrefix(displaySafe(string(r))); isBad {
			t.Errorf("U+%04X is drawn as %+q, which measures %d and %d", r, p, ansi.StringWidth(p), runewidth.StringWidth(p))
			if bad++; bad >= 10 {
				t.Fatal("too many failures")
			}
		}
	}
}

// Clusters form where a rune attaches to the one before it, so pair every rune
// that can attach with each kind of base, in both orders and around one.
func TestDisplaySafeMakesTheModelsAgreeOnPairs(t *testing.T) {
	var joiners []rune
	for r := rune(0x20); r <= unicode.MaxRune; r++ {
		switch {
		case unicode.Is(unicode.Mc, r),
			r >= toneFirst && r <= toneLast,
			r >= flagFirst && r <= flagLast,
			r >= 0x1100 && r <= 0x11FF, r >= 0xA960 && r <= 0xA97F, r >= 0xD7B0 && r <= 0xD7FF, // Hangul jamo
			r == 0x0E33, r == 0x0EB3, // Thai and Lao sara am, spacing marks that are letters
			r >= 0xFE00 && r <= 0xFE0F:
			joiners = append(joiners, r)
		}
	}
	bases := []rune{'a', ' ', '?', '1', '日', '가', 'क', '\u0e2a', 'ب', 'é', '\U0001F600', '❤', '\U0001F3F4'}
	bad := 0
	for _, j := range joiners {
		for _, b := range bases {
			for _, in := range []string{string(b) + string(j), string(j) + string(b), string(b) + string(j) + string(b)} {
				if p, isBad := firstBadPrefix(displaySafe(in)); isBad {
					t.Errorf("%+q is drawn so that %+q measures %d and %d", in, p, ansi.StringWidth(p), runewidth.StringWidth(p))
					if bad++; bad >= 10 {
						t.Fatal("too many failures")
					}
				}
			}
		}
	}
}

// Random text drawn from the scripts and symbol blocks that cause trouble, and
// from everywhere. The seed is fixed so a failure reproduces.
func TestDisplaySafeMakesTheModelsAgreeOnRandomText(t *testing.T) {
	ranges := [][2]rune{
		{0x20, 0x24F},      // Latin
		{0x300, 0x36F},     // combining marks
		{0x590, 0x5FF},     // Hebrew
		{0x600, 0x77F},     // Arabic
		{0x900, 0xDFF},     // Indic scripts: spacing marks, vowel signs, viramas
		{0xE00, 0xE7F},     // Thai
		{0x1000, 0x109F},   // Myanmar
		{0x1100, 0x11FF},   // Hangul jamo
		{0x1780, 0x17FF},   // Khmer
		{0x200B, 0x200F},   // zero-width and direction marks
		{0x2000, 0x2BFF},   // punctuation and symbols
		{0x4E00, 0x9FFF},   // CJK
		{0xAC00, 0xD7A3},   // Hangul syllables
		{0xFE00, 0xFE0F},   // variation selectors
		{0x1D100, 0x1D1FF}, // musical symbols
		{0x1F1E6, 0x1F1FF}, // regional indicators
		{0x1F300, 0x1FAFF}, // emoji
		{0x1F3FB, 0x1F3FF}, // skin tones
		{0xE0000, 0xE01EF}, // tags and variation selectors
		{0x20, 0x10FFFF},   // anywhere
	}
	n := 50000
	if testing.Short() {
		n = 10000
	}
	rnd := rand.New(rand.NewSource(20260921))
	bad := 0
	for i := 0; i < n; i++ {
		var in []rune
		for j := 1 + rnd.Intn(12); j > 0; j-- {
			rg := ranges[rnd.Intn(len(ranges))]
			in = append(in, rg[0]+rune(rnd.Intn(int(rg[1]-rg[0])+1)))
		}
		if p, isBad := firstBadPrefix(displaySafe(string(in))); isBad {
			t.Errorf("%+q is drawn so that %+q measures %d and %d", string(in), p, ansi.StringWidth(p), runewidth.StringWidth(p))
			if bad++; bad >= 10 {
				t.Fatal("too many failures")
			}
		}
	}
}

// TestNamesWithFlagsAndEmojiCannotBreakTheLayout is the whole-screen version
// of the cases above: names of the kind people give their phones, drawn in
// every pane, must leave every line the width of the terminal by both models.
func TestNamesWithFlagsAndEmojiCannotBreakTheLayout(t *testing.T) {
	names := []string{
		"stage-phone \U0001F1E6\U0001F1FA",                    // a flag
		"lab-tablet \U0001FAE9 \U0001F44D\U0001F3FD",          // a recent emoji and a skin tone
		"\u0e2a\u0e27\u0e31\u0e2a\u0e14\u0e35-printer",        // Thai with vowel and tone marks
		"a\u093e-projector " + "\U0001F468\u200d\U0001F469",   // a spacing mark after a Latin letter, a joined pair
		"\U0001F1EC\U0001F1E7\U0001F1EF\U0001F1F5 flags-only", // two flags
	}
	a, st := sized(t, 120, 40)
	for i, name := range names {
		key := "mac-flag-" + string(rune('a'+i))
		if err := st.Apply(obs(key, model.FieldHostname, name, "mdns", 0.9)); err != nil {
			t.Fatal(err)
		}
	}
	a.Update(Batch{Devices: st.Devices(), At: t0.Add(6 * time.Second)})
	// Look at each of the names in the details pane too, not only the table.
	for i := 0; i < len(names)+1; i++ {
		lines := strings.Split(a.View(), "\n")
		if len(lines) != 40 {
			t.Fatalf("frame has %d lines, want 40", len(lines))
		}
		for n, l := range lines {
			plain := ansi.Strip(l)
			if got := ansi.StringWidth(l); got != 120 {
				t.Errorf("selection %d, line %d: x/ansi=%d: %q", i, n, got, plain)
			}
			if got := runewidth.StringWidth(plain); got != 120 {
				t.Errorf("selection %d, line %d: go-runewidth=%d: %q", i, n, got, plain)
			}
		}
		a.Update(tea.KeyMsg{Type: tea.KeyDown})
	}
}
