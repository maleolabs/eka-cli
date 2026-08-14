package ui

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Board is the kanban presentation primitive: a fixed grid of box-drawn
// columns (┌─┬─┐ / ├─┼─┤ / └─┴─┘ / │) rendered side by side with
// aligned rows. Each column owns its width — max(header with count,
// longest card line, 8), capped at the adaptive column width, overflow
// truncated with "…" — and renders its item cards in order; an empty
// column shows "—" in its first item row.
//
// An item is a Card: several display lines, each with its own color
// function. A card renders as a vertical block (the "▸ " marker on its
// first line, the following lines indented to the text column), and a
// row's height is the tallest card in that row — shorter cards and
// empty columns pad with blank lines. Consecutive rows are separated by
// a blank gap row, so cards stay visually distinct.
//
// Rendering is a pure function of the added columns, so output is
// deterministic on TTY and non-TTY alike.
//
// The primitive knows nothing about domain data: it renders titles and
// item labels exactly as given. Colors are applied to text spans only
// (borders are dim on a TTY); non-TTY output is plain UTF-8.
type Board struct {
	s       *Style
	columns []boardColumn
}

// Segment is one colored span of a card line: its text plus its own
// color function (nil = the column's default color).
type Segment struct {
	Text string
	// Color colors this segment only; nil falls back to the column
	// color.
	Color func(string) string
}

// CardLine is one display line of a card: the colored segments that
// make it up. Segment colors compose left to right, so a line can mix
// e.g. a type badge and the container tag in different colors.
type CardLine []Segment

// Card is a multi-line item label. The first line carries the "▸ "
// marker; following lines render indented to the text column.
type Card []CardLine

// boardColumn is one column of the board.
type boardColumn struct {
	title string
	color func(string) string
	items []Card
}

// Board width bounds: columns never narrow below boardMinWidth and
// never exceed boardMaxWidth (longer content is truncated with "…").
// On a TTY the columns adapt to the terminal width (BoardColumnWidth);
// with an unknown width (pipes, CI, tests) every column renders at the
// fixed maximum, keeping non-TTY output byte-identical.
const (
	boardMinWidth = 8
	boardMaxWidth = 32
)

// BoardColumnWidth returns the column width the board renders for a
// terminal of the given display width and column count. A termWidth of
// 0 (unknown — pipes, CI, tests) falls back to the fixed maximum. The
// result is clamped to [boardMinWidth, boardMaxWidth]: very narrow
// terminals keep the minimum width (the grid overflows — unavoidable
// without wrapping), wide terminals never exceed the cap.
func BoardColumnWidth(termWidth, columns int) int {
	if termWidth <= 0 || columns <= 0 {
		return boardMaxWidth
	}
	// Grid overhead: one border cell per column boundary (columns+1)
	// plus the two-cell padding of every column.
	overhead := (columns + 1) + columns*2
	w := (termWidth - overhead) / columns
	if w < boardMinWidth {
		w = boardMinWidth
	}
	if w > boardMaxWidth {
		w = boardMaxWidth
	}
	return w
}

// BoardItemBudget returns the display budget a single item label may
// use inside one board column: BoardColumnWidth minus the "▸ " prefix.
// Renderers that compose item labels (e.g. a label plus a context tag
// that must stay visible) truncate against this budget so the board
// primitive never re-truncates their tag away.
func BoardItemBudget(termWidth, columns int) int {
	return BoardColumnWidth(termWidth, columns) - displayWidth(itemPrefix)
}

// itemPrefix marks each item label on the board so list membership is
// readable at a glance; it is part of the cell content and counts
// toward the column width.
const itemPrefix = "▸ " // two display cells

// NewBoard starts a kanban board for the given style.
func NewBoard(s *Style) *Board { return &Board{s: s} }

// AddColumn appends one column of single-line items: title, the color
// function applied to the header text and item labels (nil = plain),
// and the item labels in display order. Multi-line labels ("a\nb")
// split into card lines; use AddCards for per-line colors.
func (b *Board) AddColumn(title string, color func(string) string, items []string) *Board {
	cards := make([]Card, len(items))
	for i, it := range items {
		lines := strings.Split(it, "\n")
		card := make(Card, len(lines))
		for j, line := range lines {
			card[j] = CardLine{{Text: line}}
		}
		cards[i] = card
	}
	return b.AddCards(title, color, cards)
}

// AddCards appends one column of item cards: title, the color function
// applied to the header text and to card lines without their own color
// (nil = plain), and the cards in display order.
func (b *Board) AddCards(title string, color func(string) string, cards []Card) *Board {
	b.columns = append(b.columns, boardColumn{title: title, color: color, items: cards})
	return b
}

// Render prints the board: top border, header row (title + count),
// separator, item rows (one row per maximum column item count; empty
// cells stay blank), bottom border. A board without columns renders
// nothing.
func (b *Board) Render() {
	if len(b.columns) == 0 {
		return
	}
	s := b.s
	colWidth := BoardColumnWidth(s.Width, len(b.columns))
	widths := make([]int, len(b.columns))
	headers := make([]string, len(b.columns))
	for i, c := range b.columns {
		headers[i] = fmt.Sprintf("%s (%d)", c.title, len(c.items))
		w := len(headers[i])
		for _, card := range c.items {
			for _, line := range card {
				// The first line carries the "▸ " prefix inside its cell.
				if lineWidth(line)+displayWidth(itemPrefix) > w {
					w = lineWidth(line) + displayWidth(itemPrefix)
				}
			}
		}
		if w < boardMinWidth {
			w = boardMinWidth
		}
		if w > colWidth {
			w = colWidth
		}
		widths[i] = w
	}

	// Top border: ┌───┬───┐
	var top strings.Builder
	for i, w := range widths {
		if i == 0 {
			top.WriteString("┌")
		} else {
			top.WriteString("┬")
		}
		top.WriteString(strings.Repeat("─", w+2))
	}
	top.WriteString("┐")
	fmt.Fprintln(s.W, s.Dim(top.String()))

	// Header row.
	fmt.Fprintln(s.W, boardRow(s, widths, func(i int) []Segment {
		return []Segment{{Text: headers[i], Color: b.columns[i].color}}
	}))

	// Separator: ├───┼───┤
	var mid strings.Builder
	for i, w := range widths {
		if i == 0 {
			mid.WriteString("├")
		} else {
			mid.WriteString("┼")
		}
		mid.WriteString(strings.Repeat("─", w+2))
	}
	mid.WriteString("┤")
	fmt.Fprintln(s.W, s.Dim(mid.String()))

	// Item rows. At least one row renders, so an all-empty board still
	// shows its "—" markers. Each row is as tall as its tallest card;
	// shorter cards pad with blank lines, and a blank gap row separates
	// consecutive rows so cards stay visually distinct.
	rows := 1
	for _, c := range b.columns {
		if len(c.items) > rows {
			rows = len(c.items)
		}
	}
	for r := 0; r < rows; r++ {
		height := 1
		for _, c := range b.columns {
			if r < len(c.items) {
				if n := len(c.items[r]); n > height {
					height = n
				}
			}
		}
		for h := 0; h < height; h++ {
			fmt.Fprintln(s.W, boardRow(s, widths, func(i int) []Segment {
				c := b.columns[i]
				if r >= len(c.items) {
					// An empty column signals its emptiness with "—" in
					// the first item row; later rows stay blank.
					if len(c.items) == 0 && r == 0 {
						return []Segment{{Text: "—", Color: c.color}}
					}
					return nil
				}
				card := c.items[r]
				if h >= len(card) {
					// A shorter card pads with a blank line.
					return nil
				}
				// Resolve segment colors: nil falls back to the column
				// color (the execution-state presentation).
				line := card[h]
				segs := make([]Segment, 0, len(line)+1)
				for _, seg := range line {
					segColor := seg.Color
					if segColor == nil {
						segColor = c.color
					}
					segs = append(segs, Segment{Text: seg.Text, Color: segColor})
				}
				// The "▸ " marker belongs to the card's first line (it
				// takes the first segment's color); following lines
				// indent to the text column.
				if h == 0 {
					return append([]Segment{{Text: itemPrefix, Color: segs[0].Color}}, segs...)
				}
				return append([]Segment{{Text: strings.Repeat(" ", displayWidth(itemPrefix))}}, segs...)
			}))
		}
		if r < rows-1 {
			// Row gap: a blank separator row across all columns.
			fmt.Fprintln(s.W, boardRow(s, widths, func(int) []Segment {
				return nil
			}))
		}
	}

	// Bottom border: └───┴───┘
	var bottom strings.Builder
	for i, w := range widths {
		if i == 0 {
			bottom.WriteString("└")
		} else {
			bottom.WriteString("┴")
		}
		bottom.WriteString(strings.Repeat("─", w+2))
	}
	bottom.WriteString("┘")
	fmt.Fprintln(s.W, s.Dim(bottom.String()))
}

// truncate shortens text to the display width, appending "…" when it
// does not fit. Titles and ids are ASCII by the reference grammar;
// cells may carry the "▸ " prefix and the "…" ellipsis — the only
// multi-byte glyphs the UI emits — so truncation operates on runes
// (display cells), never on bytes.
func truncate(text string, width int) string {
	if displayWidth(text) <= width {
		return text
	}
	runes := []rune(text)
	return string(runes[:width-1]) + "…"
}

// truncateSegments shortens a card line to the display width, cutting
// from the last segment and appending "…" when it does not fit. The
// line's own segments are preserved: color composition survives
// truncation. Operates on runes (display cells), never on bytes.
func truncateSegments(segs []Segment, width int) []Segment {
	total := 0
	for _, s := range segs {
		total += displayWidth(s.Text)
	}
	if total <= width {
		return segs
	}
	out := make([]Segment, 0, len(segs))
	remaining := width
	for _, s := range segs {
		sw := displayWidth(s.Text)
		if sw < remaining {
			out = append(out, s)
			remaining -= sw
			continue
		}
		if sw == remaining {
			out = append(out, s)
			return out
		}
		// This segment overflows: cut inside it, keep the ellipsis in
		// the same color, drop everything after.
		runes := []rune(s.Text)
		if remaining > 1 {
			out = append(out, Segment{Text: string(runes[:remaining-1]) + "…", Color: s.Color})
		} else {
			out = append(out, Segment{Text: "…", Color: s.Color})
		}
		return out
	}
	return out
}

// displayWidth returns the terminal display width of text. Every glyph
// the UI emits (box drawing, ✓, ○, ▸, •, ·, —, …) occupies exactly one
// cell, so the display width is the rune count; byte length would
// over-count the multi-byte glyphs.
func displayWidth(text string) int {
	return utf8.RuneCountInString(text)
}

// lineWidth returns the display width of a card line: the sum of its
// segments (plain text — colors never affect layout).
func lineWidth(line CardLine) int {
	w := 0
	for _, s := range line {
		w += displayWidth(s.Text)
	}
	return w
}

// boardRow renders one grid row: "│ <cell> │ ... │" with each cell
// padded to its column width. The cell content is a colored segment
// list (already truncated); the bars and padding are dim on a TTY.
// Padding is computed on the plain display width, so colored and
// truncated cells stay aligned.
func boardRow(s *Style, widths []int, cell func(int) []Segment) string {
	var sb strings.Builder
	for i, w := range widths {
		segs := truncateSegments(cell(i), w)
		sb.WriteString(s.paint(ColorDim, "│ "))
		written := 0
		for _, seg := range segs {
			text := seg.Text
			if seg.Color != nil {
				text = seg.Color(text)
			}
			sb.WriteString(text)
			written += displayWidth(seg.Text)
		}
		sb.WriteString(strings.Repeat(" ", w-written))
		sb.WriteString(s.paint(ColorDim, " "))
	}
	sb.WriteString(s.paint(ColorDim, "│"))
	return sb.String()
}
