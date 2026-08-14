package ui

import (
	"bytes"
	"strings"
	"testing"
)

// testStyle builds a plain (non-TTY, no color) style over a buffer.
func testStyle() (*Style, *bytes.Buffer) {
	var buf bytes.Buffer
	return NewStyle(&buf, false), &buf
}

func TestBoardLayout(t *testing.T) {
	s, buf := testStyle()
	NewBoard(s).
		AddColumn("Todo", nil, []string{"alpha", "beta"}).
		AddColumn("Done", nil, []string{"gamma"}).
		Render()
	want := "" +
		"┌──────────┬──────────┐\n" +
		"│ Todo (2) │ Done (1) │\n" +
		"├──────────┼──────────┤\n" +
		"│ ▸ alpha  │ ▸ gamma  │\n" +
		"│          │          │\n" + // row gap
		"│ ▸ beta   │          │\n" +
		"└──────────┴──────────┘\n"
	if got := stripMargin(buf.String()); got != want {
		t.Errorf("board layout mismatch:\n got: %q\nwant: %q", got, want)
	}
}

// TestBoardColumnWidth: adaptive column width from the terminal width,
// clamped to [boardMinWidth, boardMaxWidth]; unknown width falls back
// to the fixed maximum (non-TTY determinism).
func TestBoardColumnWidth(t *testing.T) {
	cases := []struct {
		term, columns, want int
	}{
		{0, 5, 32},   // unknown (non-TTY): fixed maximum
		{200, 5, 32}, // wide terminal: capped at the maximum
		{100, 5, 16}, // (100-16)/5
		{80, 5, 12},  // (80-16)/5
		{40, 5, 8},   // narrow: clamped to the minimum
		{20, 5, 8},   // very narrow: minimum, grid overflows
	}
	for _, c := range cases {
		if got := BoardColumnWidth(c.term, c.columns); got != c.want {
			t.Errorf("BoardColumnWidth(%d, %d) = %d, want %d", c.term, c.columns, got, c.want)
		}
	}
	if got := BoardItemBudget(0, 5); got != 30 {
		t.Errorf("BoardItemBudget(0, 5) = %d, want 30", got)
	}
	if got := BoardItemBudget(80, 5); got != 10 {
		t.Errorf("BoardItemBudget(80, 5) = %d, want 10", got)
	}
}

// TestBoardAdaptiveWidth: on a styled TTY width the rendered grid fits
// the terminal; the fixed-width rendering is unchanged on unknown
// width.
func TestBoardAdaptiveWidth(t *testing.T) {
	// 80-cell terminal: columns of 12 → total grid 5*12 + 6 borders + 10 padding = 76.
	var buf bytes.Buffer
	s := NewStyle(&buf, false)
	s.Width = 80
	NewBoard(s).
		AddColumn("Planned", nil, []string{"markdown-syntax-highlighting"}).
		AddColumn("Todo", nil, nil).
		AddColumn("In Progress", nil, nil).
		AddColumn("In Review", nil, nil).
		AddColumn("Done", nil, nil).
		Render()
	out := buf.String()
	for _, want := range []string{
		"│ Planned (1)  │ Todo (0) │",
		"│ ▸ markdown-… │",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("adaptive board must contain %q:\n%s", want, out)
		}
	}
	for _, line := range strings.Split(strings.TrimSuffix(out, "\n"), "\n") {
		if len([]rune(line)) > 80 {
			t.Errorf("board line exceeds the terminal width (%d > 80): %q", len([]rune(line)), line)
		}
	}
}

// TestBoardCardBlocks: multi-line item labels render as vertical
// cards — the "▸ " marker on the first line, following lines indented
// to the text column; a row is as tall as its tallest card, shorter
// cards pad with blank lines.
func TestBoardCardBlocks(t *testing.T) {
	s, buf := testStyle()
	NewBoard(s).
		AddColumn("In Progress", nil, []string{"draft-autosave\nsto · wave-7"}).
		AddColumn("Done", nil, []string{"publish-post\nsto · wave-7", "empty-title\nch · wave-7"}).
		Render()
	want := "" +
		"┌──────────────────┬────────────────┐\n" +
		"│ In Progress (1)  │ Done (2)       │\n" +
		"├──────────────────┼────────────────┤\n" +
		"│ ▸ draft-autosave │ ▸ publish-post │\n" +
		"│   sto · wave-7   │   sto · wave-7 │\n" +
		"│                  │                │\n" + // row gap
		"│                  │ ▸ empty-title  │\n" +
		"│                  │   ch · wave-7  │\n" +
		"└──────────────────┴────────────────┘\n"
	if got := stripMargin(buf.String()); got != want {
		t.Errorf("card board layout mismatch:\n got: %q\nwant: %q", got, want)
	}
}

// TestBoardCardShorterRowPads: a single-line card in a row with a
// two-line card pads its second line blank, keeping the grid aligned.
func TestBoardCardShorterRowPads(t *testing.T) {
	s, buf := testStyle()
	NewBoard(s).
		AddColumn("Todo", nil, []string{"alpha\nsto · wave-7", "beta"}).
		Render()
	out := stripMargin(buf.String())
	if !strings.Contains(out, "│ ▸ alpha        │\n│   sto · wave-7 │\n│                │\n│ ▸ beta         │") {
		t.Errorf("cards must be separated by a blank gap row:\n%s", out)
	}
}

func TestBoardEmptyColumnShowsDash(t *testing.T) {
	s, buf := testStyle()
	NewBoard(s).
		AddColumn("Planned", nil, []string{"alpha"}).
		AddColumn("Todo", nil, nil).
		Render()
	out := buf.String()
	if !strings.Contains(out, "│ ▸ alpha") {
		t.Errorf("filled column must show its item:\n%s", out)
	}
	if !strings.Contains(out, "—") {
		t.Errorf("empty column must show its \"—\" marker:\n%s", out)
	}
	// The "—" appears exactly once (the empty column's first row).
	if got := strings.Count(out, "—"); got != 1 {
		t.Errorf("empty marker count = %d, want 1:\n%s", got, out)
	}
}

func TestBoardAllColumnsEmpty(t *testing.T) {
	s, buf := testStyle()
	NewBoard(s).
		AddColumn("Planned", nil, nil).
		AddColumn("Done", nil, nil).
		Render()
	out := buf.String()
	if got := strings.Count(out, "—"); got != 2 {
		t.Errorf("every empty column must show \"—\", got %d:\n%s", got, out)
	}
}

func TestBoardMinWidth(t *testing.T) {
	s, buf := testStyle()
	NewBoard(s).AddColumn("A", nil, nil).Render()
	// Min width 8: "│ " + 8 cells + " │".
	if !strings.Contains(buf.String(), "│ A (0)    │") {
		t.Errorf("column must not narrow below the min width:\n%s", buf.String())
	}
}

func TestBoardTruncationCappedAtMaxWidth(t *testing.T) {
	s, buf := testStyle()
	long := strings.Repeat("x", 40)
	NewBoard(s).AddColumn("C", nil, []string{long}).Render()
	out := buf.String()
	if strings.Contains(out, long) {
		t.Errorf("long item must be truncated:\n%s", out)
	}
	if !strings.Contains(out, "…") {
		t.Errorf("truncated item must carry the ellipsis:\n%s", out)
	}
	// Display width of the truncated cell: "▸ " + 29 x + ellipsis = 32.
	if !strings.Contains(out, "▸ xxxxxxxxxxxxxxxxxxxxxxxxxxxxx…") {
		t.Errorf("truncation must cap at 32 display cells:\n%s", out)
	}
}

func TestBoardNoColumnsRendersNothing(t *testing.T) {
	s, buf := testStyle()
	NewBoard(s).Render()
	if buf.Len() != 0 {
		t.Errorf("a board without columns must render nothing, got %q", buf.String())
	}
}

func TestBoardDeterministic(t *testing.T) {
	build := func() string {
		var buf bytes.Buffer
		s := NewStyle(&buf, false)
		NewBoard(s).AddColumn("T", nil, []string{"a"}).Render()
		return buf.String()
	}
	if build() != build() {
		t.Error("board output must be deterministic")
	}
}

func TestBoardColorsOnTTY(t *testing.T) {
	var buf bytes.Buffer
	s := &Style{Color: true, W: &buf}
	NewBoard(s).AddColumn("T", s.Success, []string{"a"}).Render()
	out := buf.String()
	if !strings.Contains(out, "\x1b[") {
		t.Errorf("color-enabled board must emit ANSI:\n%q", out)
	}
	if !strings.Contains(out, "\x1b[38;5;245m") {
		t.Errorf("borders must be dim:\n%q", out)
	}
}
