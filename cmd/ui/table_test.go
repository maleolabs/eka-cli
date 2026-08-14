package ui

import (
	"bytes"
	"strings"
	"testing"
)

// TestTableAlignment: the columns pad to the widest rune length of the
// header and the cells, two spaces between columns, header accent +
// dim dash underline. Output flows through the style's line-indenting
// margin writer (the standard human-output path), so every non-blank
// line carries the uniform left margin.
func TestTableAlignment(t *testing.T) {
	var buf bytes.Buffer
	s := NewStyle(&buf, false)
	NewTable(s, "NAME", "PLAN", "ITEMS").
		AddRow([]string{"wave-0", "-", "1"}, nil).
		AddRow([]string{"wave-1", "plan:roadmap", "5/8"}, nil).
		Render()
	// Column widths: NAME 6, PLAN 12, ITEMS 5 (widest rune length of
	// header and cells); two spaces between columns.
	want := "" +
		"  NAME    PLAN          ITEMS\n" +
		"  " + strings.Repeat("─", 27) + "\n" +
		"  wave-0  -             1    \n" +
		"  wave-1  plan:roadmap  5/8  \n"
	if buf.String() != want {
		t.Errorf("table output differs:\ngot:\n%q\nwant:\n%q", buf.String(), want)
	}
}

// TestTableEmptyRendersNothing: an empty table (no rows) renders
// nothing at all — the caller prints the empty line.
func TestTableEmptyRendersNothing(t *testing.T) {
	var buf bytes.Buffer
	s := NewStyle(&buf, false)
	NewTable(s, "NAME", "STATUS").Render()
	if buf.String() != "" {
		t.Errorf("empty table must render nothing, got %q", buf.String())
	}
}

// TestTableRuneWidth: the column widths are measured in runes, so
// multi-byte cells (icons, dashes) align with ASCII cells.
func TestTableRuneWidth(t *testing.T) {
	var buf bytes.Buffer
	s := NewStyle(&buf, false)
	NewTable(s, "STATUS").
		AddRow([]string{"• active"}, nil).
		AddRow([]string{"• completed"}, nil).
		Render()
	// Column width 11: "• completed" is the widest cell (runes, not
	// bytes).
	want := "" +
		"  STATUS     \n" +
		"  ───────────\n" +
		"  • active   \n" +
		"  • completed\n"
	if buf.String() != want {
		t.Errorf("table output differs:\ngot:\n%q\nwant:\n%q", buf.String(), want)
	}
}

// TestTableColors: per-cell colors apply to the padded cell; a nil
// color renders plain; on a non-TTY style colors are identity.
func TestTableColors(t *testing.T) {
	var buf bytes.Buffer
	s := NewStyle(&buf, false)
	NewTable(s, "A", "B").
		AddRow([]string{"plain", "dim"}, []func(string) string{nil, s.Dim}).
		Render()
	want := "" +
		"  A      B  \n" +
		"  ──────────\n" +
		"  plain  dim\n"
	if buf.String() != want {
		t.Errorf("table output differs:\ngot:\n%q\nwant:\n%q", buf.String(), want)
	}
	// On a colored style the cell carries ANSI (the padding survives
	// the escapes — it is computed on the plain text).
	var cb bytes.Buffer
	cs := &Style{Color: true, W: &cb}
	NewTable(cs, "A").
		AddRow([]string{"x"}, []func(string) string{cs.Dim}).
		Render()
	if !strings.Contains(cb.String(), "\x1b[") {
		t.Error("colored cell must carry ANSI on a colored style")
	}
}
