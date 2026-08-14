package ui

import (
	"errors"
	"fmt"
	"io"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// This file implements the reusable interactive arrow-selected menu,
// following the m2apps interactive menu pattern
// (charmbracelet/bubbletea): a state model rendered by the bubbletea
// runtime, which handles raw-mode input, terminal capability detection
// and in-place redrawing — the same stack the m2apps CLI ships. No
// hand-rolled escape-sequence rendering. It is currently used by the
// ADR-020 namespace-alignment flow (`eka sync` / `eka project
// register`) and is the single interactive-selection primitive of the
// CLI.

// MenuItem is one selectable entry of the menu: Title is what is
// rendered, Value is what Select returns on selection (the m2apps
// menu pattern — display and meaning may differ).
type MenuItem struct {
	Title string
	Value string
}

// ErrCancelled reports that the user cancelled the selection
// (Esc / q / Ctrl-C). Callers map it to their deterministic abort
// path.
var ErrCancelled = errors.New("selection cancelled")

// Select renders an interactive arrow-selected menu and returns the
// VALUE of the selected item. The active option is rendered in the
// primary (accent) color and the unselected options dimmed (plain text
// when the style has colors disabled); the usage tip is dimmed while
// navigating. After confirmation (Enter) the final frame renders the
// selected item with the check mark and WITHOUT the usage tip. A cancel
// (Esc / q / Ctrl-C) returns ErrCancelled — deterministic, never the
// default; an empty item list also returns ErrCancelled; an EOF before
// any key returns the default item's value. Errors from the program run
// are returned unwrapped.
//
// Callers must invoke Select only when stdin AND stdout are real
// terminals (the cmd layer gates on both before wiring the
// confirmation); a non-TTY run refuses deterministically instead.
func Select(s *Style, stdin io.Reader, stdout io.Writer, prompt string, items []MenuItem, defaultIdx int) (string, error) {
	if len(items) == 0 {
		return "", ErrCancelled
	}
	idx := defaultIdx
	if idx < 0 {
		idx = 0
	}
	if idx >= len(items) {
		idx = len(items) - 1
	}

	model := selectModel{s: s, prompt: prompt, items: items, selectedIndex: idx}
	program := tea.NewProgram(model, tea.WithInput(stdin), tea.WithOutput(stdout))
	final, err := program.Run()
	if err != nil {
		return "", fmt.Errorf("interactive selection failed: %w", err)
	}
	state, ok := final.(selectModel)
	if !ok {
		return "", fmt.Errorf("interactive selection failed: invalid program state")
	}
	if state.aborted {
		return "", ErrCancelled
	}
	return state.items[state.selectedIndex].Value, nil
}

// selectModel is the bubbletea state model of the selection menu
// (m2apps menuModel pattern): wraparound navigation, Enter selects,
// Esc/q/Ctrl-C cancels. s is optional styling (nil renders plain —
// the test path). confirmed is set when Enter is pressed so the final
// frame renders the check mark and omits the usage tip.
type selectModel struct {
	prompt        string
	items         []MenuItem
	selectedIndex int
	aborted       bool
	confirmed     bool
	s             *Style
}

// Init starts the model with no commands.
func (m selectModel) Init() tea.Cmd { return nil }

// Update handles the navigation keys. Navigation wraps around (up at
// the first option moves to the last, down at the last moves to the
// first — the m2apps menu behavior).
func (m selectModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch typed := msg.(type) {
	case tea.KeyMsg:
		switch typed.String() {
		case "esc", "ctrl+c", "q":
			m.aborted = true
			return m, tea.Quit
		case "up", "k":
			if m.selectedIndex == 0 {
				m.selectedIndex = len(m.items) - 1
			} else {
				m.selectedIndex--
			}
		case "down", "j":
			m.selectedIndex++
			if m.selectedIndex >= len(m.items) {
				m.selectedIndex = 0
			}
		case "enter":
			m.confirmed = true
			return m, tea.Quit
		}
	}
	return m, nil
}

// View renders the menu frame: the prompt, the items with a ">"
// cursor on the active one, and the usage hint (dimmed when a style
// is present) — indented by the container margin (blank lines stay
// blank). After confirmation (Enter) the final frame renders the
// selected item with the check mark and WITHOUT the usage tip. The
// active item is rendered in the primary (accent) color and the
// unselected items dimmed (plain when the style is nil or colors are
// disabled).
func (m selectModel) View() string {
	var buf strings.Builder
	fmt.Fprintf(&buf, "%s\n\n", Margin+m.prompt)
	for i, item := range m.items {
		marker := ">"
		if m.confirmed {
			marker = IconDone
		}
		switch {
		case m.s != nil && m.s.Color:
			if i == m.selectedIndex {
				fmt.Fprintf(&buf, "%s%s %s\n", Margin, m.s.Accent(marker), m.s.Accent(item.Title))
			} else {
				fmt.Fprintf(&buf, "%s  %s\n", Margin, m.s.Dim(item.Title))
			}
		default:
			if i == m.selectedIndex {
				fmt.Fprintf(&buf, "%s%s %s\n", Margin, marker, item.Title)
			} else {
				fmt.Fprintf(&buf, "%s  %s\n", Margin, item.Title)
			}
		}
	}
	if !m.confirmed {
		tip := "Use ↑/↓ (or j/k) to navigate, Enter to select, Esc/q/Ctrl-C to abort."
		if m.s != nil {
			tip = m.s.Dim(tip)
		}
		buf.WriteString("\n" + Margin + tip + "\n")
	}
	return buf.String()
}
