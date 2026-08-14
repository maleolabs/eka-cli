package ui

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Table is the minimal aligned table primitive: a header line (accent
// colored) with a dim unicode line underline (IconLine), then the
// rows with per-cell optional colors. Column widths are the maximum
// rune length of the header and the cells; cells pad right with two
// spaces between columns. An empty table renders nothing (the caller
// prints the empty line). Padding is computed on the plain text, so
// colored cells stay aligned on a TTY.
type Table struct {
	s         *Style
	headers   []string
	rows      [][]string
	rowColors [][]func(string) string
}

// NewTable starts an aligned table for the given style and headers.
func NewTable(s *Style, headers ...string) *Table {
	return &Table{s: s, headers: headers}
}

// AddRow appends one row of cells. colors may be nil or partial; a nil
// color function renders the cell plain.
func (t *Table) AddRow(cells []string, colors []func(string) string) *Table {
	t.rows = append(t.rows, cells)
	t.rowColors = append(t.rowColors, colors)
	return t
}

// Render prints the table: the accent header line, a dim unicode line
// underline (only when at least one row exists) and the rows. An
// empty table renders nothing.
func (t *Table) Render() {
	if len(t.rows) == 0 {
		return
	}
	s := t.s
	widths := make([]int, len(t.headers))
	for i, h := range t.headers {
		widths[i] = utf8.RuneCountInString(h)
	}
	for _, row := range t.rows {
		for i, cell := range row {
			if i < len(widths) && utf8.RuneCountInString(cell) > widths[i] {
				widths[i] = utf8.RuneCountInString(cell)
			}
		}
	}
	// Header line (accent) and the dim line underline. The header text
	// is padded BEFORE coloring so the alignment survives ANSI on a
	// TTY. The underline repeats the unicode box-drawing line
	// (IconLine), consistent with the tree connectors — the design
	// system never emits primitive ASCII lines.
	var header strings.Builder
	for i, h := range t.headers {
		if i > 0 {
			header.WriteString("  ")
		}
		header.WriteString(fmt.Sprintf("%-*s", widths[i], h))
	}
	fmt.Fprintln(s.W, s.Accent(header.String()))
	total := 0
	for i, w := range widths {
		if i > 0 {
			total += 2
		}
		total += w
	}
	fmt.Fprintln(s.W, s.Dim(strings.Repeat(IconLine, total)))
	for ri, row := range t.rows {
		var line strings.Builder
		for i, cell := range row {
			if i > 0 {
				line.WriteString("  ")
			}
			text := fmt.Sprintf("%-*s", widths[i], cell)
			if ri < len(t.rowColors) && i < len(t.rowColors[ri]) && t.rowColors[ri][i] != nil {
				text = t.rowColors[ri][i](text)
			}
			line.WriteString(text)
		}
		fmt.Fprintln(s.W, line.String())
	}
}
