package ui

import "fmt"

// Summary renders the tree-style summary block used at the end of
// every successful command: a "Summary:" heading followed by
// "└── label: value" items. Rendering is a pure function of the added
// items, so output is deterministic on TTY and non-TTY alike.
type Summary struct {
	s     *Style
	items [][2]string
}

// NewSummary starts a summary block for the given style.
func NewSummary(s *Style) *Summary {
	return &Summary{s: s}
}

// Add appends one label/value item (label may be empty for a bare
// value line).
func (sm *Summary) Add(label, value string) *Summary {
	sm.items = append(sm.items, [2]string{label, value})
	return sm
}

// Render prints the summary block. It is preceded by a blank line so
// it always separates cleanly from the tree/report above it.
func (sm *Summary) Render() {
	s := sm.s
	fmt.Fprintln(s.W)
	fmt.Fprintln(s.W, s.Accent("Summary:"))
	for _, it := range sm.items {
		if it[0] == "" {
			fmt.Fprintf(s.W, "%s %s\n", TreeLast, it[1])
			continue
		}
		fmt.Fprintf(s.W, "%s %s: %s\n", TreeLast, s.Info(it[0]), it[1])
	}
}

// Bullets renders an optional titled bullet list (used by --verbose
// detail sections: per-unit lists, plan actions, warnings). Empty item
// lists render nothing, keeping default output concise.
func (s *Style) Bullets(title string, items []string) {
	if len(items) == 0 {
		return
	}
	fmt.Fprintln(s.W)
	fmt.Fprintln(s.W, s.Info(title))
	for _, it := range items {
		fmt.Fprintf(s.W, "  %s %s\n", IconBullet, it)
	}
}
