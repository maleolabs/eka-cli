package ui

import (
	"fmt"
	"strings"
)

// Cards renders a block of compact boxed info cards (┌─┐ / │ / └─┘).
// By default one box per item, stacked vertically. With Grid(), the
// cards render as a horizontal grid: cells of uniform width arranged
// row-major (left to right, top to bottom) inside shared row boxes
// (┌─┬─┐ / │a│b│ / └─┴─┘), so many cards stay compact instead of
// growing the view vertically.
//
// The primitive knows nothing about domain data: it renders header and
// body lines exactly as given (truncated to the cell width when the
// grid is active). Colors are applied to text spans only (borders and
// body lines are dim on a TTY); non-TTY output is plain UTF-8.
type Cards struct {
	s          *Style
	cards      []card
	grid       bool
	gridBudget int
}

// card is one boxed info card.
type card struct {
	header string
	color  func(string) string
	body   []string
}

// Card grid bounds and the width budget. The budget approximates a
// typical terminal width; it is a fixed constant so grid layout is
// deterministic on every environment (TTY and non-TTY alike) — column
// count adapts to CONTENT, never to the terminal.
const (
	cardMinWidth = 24
	cardMaxWidth = 48
	cardBudget   = 100
	cardMaxCols  = 4
)

// CompactLayoutWidth is the terminal width below which display
// projections switch to their compact structural layouts: grid
// projections derive their column count from the window, the
// containers projection swaps its table for a card list, and the
// architecture/planning trees drop non-essential detail (edge
// annotations, long state text). A width of 0 (unknown — pipes, CI,
// tests) never triggers compact layout, keeping non-TTY output
// byte-identical.
const CompactLayoutWidth = 100

// NewCards starts an info-card block for the given style.
func NewCards(s *Style) *Cards { return &Cards{s: s} }

// Add appends one card: header text, the color function applied to the
// header (nil = plain), and the body lines (rendered dim, plain when
// colors are off).
func (c *Cards) Add(header string, color func(string) string, body []string) *Cards {
	c.cards = append(c.cards, card{header: header, color: color, body: body})
	return c
}

// Grid switches the block to horizontal grid rendering. The column
// count is derived from the content: cell width = the widest content
// line (clamped to [cardMinWidth, cardMaxWidth], truncated beyond),
// columns = clamp(cardBudget / cellWidth, 1, cardMaxCols). A block
// without cards renders nothing either way.
func (c *Cards) Grid() *Cards {
	c.grid = true
	return c
}

// GridWithWidth switches the block to horizontal grid rendering with
// the column count derived from the terminal width (adaptive layout):
// the same cell-width rule as Grid, but the horizontal budget is the
// given terminal width instead of the fixed cardBudget — a wider
// window shows more columns, a narrow one fewer, without truncating
// cell content. A termWidth of 0 (unknown — pipes, CI, tests) falls
// back to the fixed cardBudget, keeping non-TTY output byte-identical.
func (c *Cards) GridWithWidth(termWidth int) *Cards {
	c.grid = true
	if termWidth > 0 {
		c.gridBudget = termWidth
	}
	return c
}

// Render prints every card. Without Grid(), one box per item, stacked
// vertically, all boxes sharing the widest content line. With Grid(),
// cards render row-major inside shared row boxes. A block without
// cards renders nothing.
func (c *Cards) Render() {
	if len(c.cards) == 0 {
		return
	}
	if c.grid {
		c.renderGrid()
		return
	}
	s := c.s
	width := 0
	for _, cd := range c.cards {
		if w := displayWidth(cd.header); w > width {
			width = w
		}
		for _, line := range cd.body {
			if w := displayWidth(line); w > width {
				width = w
			}
		}
	}
	for _, cd := range c.cards {
		fmt.Fprintln(s.W, s.Dim("┌"+strings.Repeat("─", width+2)+"┐"))
		header := cd.header
		if cd.color != nil {
			header = cd.color(header)
		}
		fmt.Fprintln(s.W, s.Dim("│"), header+strings.Repeat(" ", width-displayWidth(cd.header)), s.Dim("│"))
		for _, line := range cd.body {
			fmt.Fprintln(s.W, s.Dim("│"), s.Dim(line)+strings.Repeat(" ", width-displayWidth(line)), s.Dim("│"))
		}
		fmt.Fprintln(s.W, s.Dim("└"+strings.Repeat("─", width+2)+"┘"))
	}
}

// cellWidth returns the uniform grid cell width: the widest content
// line (header or body) clamped to [cardMinWidth, cardMaxWidth].
func (c *Cards) cellWidth() int {
	width := 0
	for _, cd := range c.cards {
		if w := displayWidth(cd.header); w > width {
			width = w
		}
		for _, line := range cd.body {
			if w := displayWidth(line); w > width {
				width = w
			}
		}
	}
	if width < cardMinWidth {
		width = cardMinWidth
	}
	if width > cardMaxWidth {
		width = cardMaxWidth
	}
	return width
}

// renderGrid prints the cards as horizontal grid rows. Each card keeps
// its own box (like the vertical layout) but boxes sit side by side
// with a single-space gap, aligned top and bottom per row. All boxes of
// one row share the same width and height, so the row reads as one
// aligned band of cards. The last row renders only the remaining cards
// — it is never padded with blank filler boxes: an empty box reads as
// a data artifact (a phantom card), not as layout whitespace.
func (c *Cards) renderGrid() {
	s := c.s
	width := c.cellWidth()
	budget := c.gridBudget
	if budget == 0 {
		budget = cardBudget
	}
	// Each box occupies width+2 border cells plus one gap cell.
	cols := budget / (width + 3)
	if cols < 1 {
		cols = 1
	}
	if cols > cardMaxCols {
		cols = cardMaxCols
	}
	// Never pad a row with empty boxes beyond the card count.
	if cols > len(c.cards) {
		cols = len(c.cards)
	}

	// box renders one card box for the given line index (0 = top
	// border, 1..height = cell lines, height+1 = bottom border),
	// padded to the shared width.
	box := func(cd *card, line, height int) string {
		switch {
		case line == 0:
			return "┌" + strings.Repeat("─", width+2) + "┐"
		case line == height+1:
			return "└" + strings.Repeat("─", width+2) + "┘"
		}
		var text string
		switch {
		case line == 1:
			text = cd.header
		case line-2 < len(cd.body):
			text = cd.body[line-2]
		}
		// Padding is computed on the PLAIN display width and color is
		// applied to the padded string — coloring first would make
		// displayWidth count the ANSI escapes and overflow the cell.
		text = truncate(text, width)
		padded := text + strings.Repeat(" ", width-displayWidth(text))
		if line == 1 && cd.color != nil {
			padded = cd.color(padded)
		} else {
			padded = s.paint(ColorDim, padded)
		}
		return s.paint(ColorDim, "│ ") + padded + s.paint(ColorDim, " │")
	}

	for start := 0; start < len(c.cards); start += cols {
		row := c.cards[start:min(start+cols, len(c.cards))]
		height := 0
		for _, cd := range row {
			if h := 1 + len(cd.body); h > height {
				height = h
			}
		}
		for line := 0; line <= height+1; line++ {
			var sb strings.Builder
			for i := 0; i < len(row); i++ {
				if i > 0 {
					sb.WriteString(" ")
				}
				part := box(&row[i], line, height)
				if line == 0 || line == height+1 {
					part = s.paint(ColorDim, part)
				}
				sb.WriteString(part)
			}
			fmt.Fprintln(s.W, sb.String())
		}
	}
}
