package ui

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"
	"testing"
)

// ansiRegex strips SGR escape sequences for plain-text assertions.
var ansiRegex = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func TestCardsLayout(t *testing.T) {
	s, buf := testStyle()
	NewCards(s).
		Add("✓ feather/vis:feather-vision", nil, []string{"approved · revision 1"}).
		Add("○ feather/req:comments", nil, []string{"draft", "note"}).
		Render()
	out := stripMargin(buf.String())

	// Header 1 is the widest line (29 cells incl. ✓ + space); the box
	// width is the max display width + 2 pads + 2 bars.
	width := 28 // "feather/vis:feather-vision" is the widest content (28 cells)
	want := fmt.Sprintf(
		"┌%s┐\n│ %s%s │\n│ %s%s │\n└%s┘\n"+
			"┌%s┐\n│ %s%s │\n│ %s%s │\n│ %s%s │\n└%s┘\n",
		strings.Repeat("─", width+2),
		"✓ feather/vis:feather-vision", "",
		"approved · revision 1", strings.Repeat(" ", width-len([]rune("approved · revision 1"))),
		strings.Repeat("─", width+2),
		strings.Repeat("─", width+2),
		"○ feather/req:comments", strings.Repeat(" ", width-len([]rune("○ feather/req:comments"))),
		"draft", strings.Repeat(" ", width-len([]rune("draft"))),
		"note", strings.Repeat(" ", width-len([]rune("note"))),
		strings.Repeat("─", width+2),
	)
	if got := stripMargin(buf.String()); got != want {
		t.Errorf("cards layout mismatch:\n got: %q\nwant: %q", got, want)
	}

	// Every content row spans the same display width (28 + 2 + 2).
	rows := 0
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if strings.HasPrefix(line, "│") {
			rows++
			if w := len([]rune(line)); w != 32 {
				t.Errorf("content row %q spans %d cells, want 32", line, w)
			}
		}
	}
	if rows != 5 {
		t.Errorf("content rows = %d, want 5 (2 headers + 3 bodies)", rows)
	}
}

func TestCardsEmptyBody(t *testing.T) {
	s, buf := testStyle()
	NewCards(s).Add("header", nil, nil).Render()
	out := buf.String()
	if !strings.Contains(out, "│ header │") {
		t.Errorf("header-only card must render:\n%s", out)
	}
}

func TestCardsNoCardsRendersNothing(t *testing.T) {
	s, buf := testStyle()
	NewCards(s).Render()
	if buf.Len() != 0 {
		t.Errorf("a cards block without cards must render nothing, got %q", buf.String())
	}
}

func TestCardsHeaderColored(t *testing.T) {
	var buf bytes.Buffer
	s := &Style{Color: true, W: &buf}
	NewCards(s).Add("header", s.Success, nil).Render()
	if !strings.Contains(buf.String(), "\x1b[38;5;114mheader\x1b[0m") {
		t.Errorf("header must be colored by its color function:\n%q", buf.String())
	}
}

// TestCardsGridColorsOnTTY: colored grid cells must not overflow —
// padding is computed on the plain display width, never on the ANSI-
// colored text (a colored-then-padded cell would panic with a negative
// Repeat count).
// TestCardsGridNoFiller: the last grid row renders only the remaining
// cards — it is never padded with blank filler boxes. A 5-card grid
// over 2 columns renders exactly 5 boxes (rows 2+2+1), never 6 with an
// empty phantom card.
func TestCardsGridNoFiller(t *testing.T) {
	var buf bytes.Buffer
	s := &Style{W: &buf}
	NewCards(s).
		Add("✓ feather/req:alpha", nil, []string{"draft"}).
		Add("✓ feather/req:beta", nil, []string{"draft"}).
		Add("✓ feather/req:gamma", nil, []string{"draft"}).
		Add("✓ feather/req:delta", nil, []string{"draft"}).
		Add("✓ feather/req:epsilon", nil, []string{"draft"}).
		GridWithWidth(62). // 62 / 31 = 2 columns
		Render()
	out := buf.String()

	// The widest line "✓ feather/req:epsilon" is 20 cells; the cell
	// width clamps to cardMinWidth 24, so each box is 24+2 wide and
	// every row band is 2 boxes + 1 gap = 51 cells.
	total := strings.Count(out, "┌")
	if total != 5 {
		t.Errorf("total boxes = %d, want 5 (no filler box)", total)
	}
	// The last row band holds exactly one box — never a blank second.
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	lastTop := ""
	for _, line := range lines {
		if strings.Contains(line, "┌") {
			lastTop = line
		}
	}
	if got := strings.Count(lastTop, "┌"); got != 1 {
		t.Errorf("last row boxes = %d, want 1 (no phantom card)", got)
	}
}

func TestCardsGridColorsOnTTY(t *testing.T) {
	var buf bytes.Buffer
	s := &Style{Color: true, W: &buf}
	NewCards(s).
		Add("✓ feather/req:comments-phase2", s.Warning, []string{"draft · revision 1"}).
		Add("✓ feather/req:publishing-core", s.Success, []string{"approved · revision 1"}).
		Grid().
		Render()
	out := buf.String()
	if !strings.Contains(out, "\x1b[") {
		t.Errorf("color-enabled cards must emit ANSI:\n%q", out)
	}
	// Every border element (top, bottom, and the side bars) is dim —
	// the grid must not mix plain white bars with dim borders.
	if !strings.Contains(out, "\x1b[38;5;245m│ \x1b[0m") {
		t.Errorf("side bars must be dim like the borders:\n%q", out)
	}
	// Separate boxes side by side with a single-space gap, top-aligned:
	// "┌…┐ ┌…┐" on the border line. The gap check runs on the
	// ANSI-stripped text — the borders are dim-wrapped on a color TTY.
	plain := ansiRegex.ReplaceAllString(out, "")
	if !strings.Contains(plain, "┐ ┌") {
		t.Errorf("grid boxes must be separated by a gap:\n%q", out)
	}
}

// TestCardsGridWithWidth: the adaptive grid derives its column count
// from the terminal width — a narrow window shows fewer columns, a
// wide one more (clamped to cardMaxCols), and an unknown width (0)
// falls back to the fixed cardBudget, keeping non-TTY output
// byte-identical.
func TestCardsGridWithWidth(t *testing.T) {
	render := func(w int) string {
		var buf bytes.Buffer
		s := &Style{W: &buf}
		NewCards(s).
			Add("✓ feather/req:alpha-long-name", nil, []string{"draft"}).
			Add("✓ feather/req:beta", nil, []string{"draft"}).
			Add("✓ feather/req:gamma", nil, []string{"draft"}).
			Add("✓ feather/req:delta", nil, []string{"draft"}).
			Add("✓ feather/req:epsilon", nil, []string{"draft"}).
			GridWithWidth(w).
			Render()
		return buf.String()
	}
	// The widest content line is "✓ feather/req:alpha-long-name"
	// (28 cells), so every cell is 28 wide; each box occupies
	// 28+2 border cells plus one gap cell.
	countCols := func(out string) int {
		first := strings.SplitN(out, "\n", 2)[0]
		return strings.Count(first, "┌")
	}
	// Narrow terminal: 40 / 31 = 1 column.
	if got := countCols(render(40)); got != 1 {
		t.Errorf("GridWithWidth(40) columns = %d, want 1", got)
	}
	// Wide terminal: 200 / 31 = 6, clamped to 4 columns.
	if got := countCols(render(200)); got != 4 {
		t.Errorf("GridWithWidth(200) columns = %d, want 4 (clamped)", got)
	}
	// Unknown width: fixed budget 100 / 31 = 3 columns (non-TTY).
	if got := countCols(render(0)); got != 3 {
		t.Errorf("GridWithWidth(0) columns = %d, want 3 (fixed budget)", got)
	}
}
