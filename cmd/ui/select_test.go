package ui

import (
	"bytes"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// The selection menu is a bubbletea model; tests drive Update/View
// directly (the standard bubbletea testing pattern — no PTY needed).

// items builds a MenuItem list whose Title and Value both equal the
// given labels (the common case).
func items(labels ...string) []MenuItem {
	out := make([]MenuItem, len(labels))
	for i, l := range labels {
		out[i] = MenuItem{Title: l, Value: l}
	}
	return out
}

// key builds a KeyMsg by its string form (the same string the model
// switches on).
func key(s string) tea.KeyMsg {
	switch s {
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "ctrl+c":
		return tea.KeyMsg{Type: tea.KeyCtrlC}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}
}

// TestSelectModelInitial: the menu starts on the seeded index and the
// view carries the prompt, the item titles, the cursor and the hint.
func TestSelectModelInitial(t *testing.T) {
	m := selectModel{prompt: "choose:", items: items("alpha", "beta", "gamma"), selectedIndex: 1}
	view := m.View()
	for _, want := range []string{"choose:", "  alpha", "> beta", "  gamma", "Use ↑/↓ (or j/k) to navigate"} {
		if !strings.Contains(view, want) {
			t.Errorf("view must contain %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "> alpha") || strings.Contains(view, "> gamma") {
		t.Errorf("cursor must sit on the seeded item beta:\n%s", view)
	}
}

// TestSelectModelRendersTitleNotValue: the menu renders Titles; the
// returned selection is the Value (display and meaning may differ).
func TestSelectModelRendersTitleNotValue(t *testing.T) {
	m := selectModel{prompt: "choose:", items: []MenuItem{
		{Title: "align identity to acme", Value: "align"},
		{Title: "abort", Value: "abort"},
	}}
	view := m.View()
	if !strings.Contains(view, "> align identity to acme") {
		t.Errorf("view must render the item Title:\n%s", view)
	}
	if strings.Contains(view, "align\"") || strings.Contains(view, "\"align\"") {
		t.Errorf("view must never render the item Value:\n%s", view)
	}
	if m.items[0].Value != "align" {
		t.Errorf("the Value is what Select returns on selection")
	}
}

// TestSelectModelNavigation: down/up move the selection; j/k work like
// the arrows.
func TestSelectModelNavigation(t *testing.T) {
	m := selectModel{items: items("alpha", "beta", "gamma")}
	for _, step := range []string{"down", "down", "up", "j"} {
		updated, cmd := m.Update(key(step))
		var ok bool
		m, ok = updated.(selectModel)
		if !ok {
			t.Fatalf("Update returned a non-selectModel")
		}
		if cmd != nil {
			t.Fatalf("navigation must not quit, got cmd %v", cmd)
		}
	}
	if m.selectedIndex != 2 {
		t.Errorf("selectedIndex = %d, want 2 (alpha -> beta -> gamma -> beta -> gamma)", m.selectedIndex)
	}
	if m.aborted {
		t.Error("navigation must not abort")
	}
}

// TestSelectModelWraparound: navigation wraps around (the m2apps menu
// behavior): up at the first item moves to the last, down at the last
// moves to the first.
func TestSelectModelWraparound(t *testing.T) {
	m := selectModel{items: items("alpha", "beta", "gamma")}
	updated, _ := m.Update(key("up"))
	m = updated.(selectModel)
	if m.selectedIndex != 2 {
		t.Errorf("up at the first item must wrap to the last, got %d", m.selectedIndex)
	}
	updated, _ = m.Update(key("down"))
	m = updated.(selectModel)
	if m.selectedIndex != 0 {
		t.Errorf("down at the last item must wrap to the first, got %d", m.selectedIndex)
	}
}

// TestSelectModelEnter: Enter quits with the current selection kept.
func TestSelectModelEnter(t *testing.T) {
	m := selectModel{items: items("alpha", "beta", "gamma"), selectedIndex: 2}
	updated, cmd := m.Update(key("enter"))
	m = updated.(selectModel)
	if cmd == nil {
		t.Errorf("enter must quit, got nil cmd")
	}
	if m.selectedIndex != 2 || m.aborted {
		t.Errorf("enter must keep the selection and not abort: idx=%d aborted=%v", m.selectedIndex, m.aborted)
	}
	if !m.confirmed {
		t.Error("enter must set confirmed")
	}
}

// TestSelectModelConfirmedView: after confirmation the final frame
// renders the selected item with the check mark and WITHOUT the usage
// tip.
func TestSelectModelConfirmedView(t *testing.T) {
	colored := &Style{Color: true, TTY: true, W: &bytes.Buffer{}}
	m := selectModel{s: colored, items: items("alpha", "beta", "gamma"), selectedIndex: 1, confirmed: true}
	view := m.View()
	if !strings.Contains(view, colored.Accent(IconDone)+" "+colored.Accent("beta")) {
		t.Errorf("confirmed view must render the check mark in accent color:\n%q", view)
	}
	if strings.Contains(view, "Use ↑/↓") {
		t.Errorf("confirmed view must NOT contain the usage tip:\n%q", view)
	}
	if strings.Contains(view, "> beta") {
		t.Errorf("confirmed view must NOT contain the arrow cursor:\n%q", view)
	}

	// Plain (s = nil): no ANSI escapes, check mark rendered literally.
	m = selectModel{items: items("alpha", "beta", "gamma"), selectedIndex: 1, confirmed: true}
	view = m.View()
	if !strings.Contains(view, IconDone+" beta") {
		t.Errorf("confirmed plain view must contain the check mark:\n%q", view)
	}
	if strings.Contains(view, "Use ↑/↓") {
		t.Errorf("confirmed plain view must NOT contain the usage tip:\n%q", view)
	}
	if strings.Contains(view, "\x1b[") {
		t.Errorf("confirmed plain view must not carry ANSI escapes:\n%q", view)
	}
}

// TestSelectModelAbort: Esc, q and Ctrl-C cancel (aborted set + quit) —
// the abort path never confirms the default.
func TestSelectModelAbort(t *testing.T) {
	for _, k := range []string{"esc", "q", "ctrl+c"} {
		m := selectModel{items: items("alpha", "beta"), selectedIndex: 0}
		updated, cmd := m.Update(key(k))
		m = updated.(selectModel)
		if cmd == nil {
			t.Errorf("%s must quit, got nil cmd", k)
		}
		if !m.aborted {
			t.Errorf("%s must set aborted", k)
		}
	}
}

// TestSelectModelStyledView: with a color-enabled style, the ACTIVE
// item is rendered in the primary (accent) color and the unselected
// items are dimmed; with colors disabled the view stays plain. The
// usage tip is dimmed when a style exists.
func TestSelectModelStyledView(t *testing.T) {
	colored := &Style{Color: true, TTY: true, W: &bytes.Buffer{}}
	m := selectModel{s: colored, prompt: "choose:", items: items("alpha", "beta"), selectedIndex: 1}
	view := m.View()
	if !strings.Contains(view, colored.Accent(">")+" "+colored.Accent("beta")) {
		t.Errorf("active item must be accent-colored:\n%q", view)
	}
	if !strings.Contains(view, "  "+colored.Dim("alpha")) {
		t.Errorf("unselected item must be dimmed:\n%q", view)
	}
	if !strings.Contains(view, colored.Dim("Use ↑/↓ (or j/k) to navigate, Enter to select, Esc/q/Ctrl-C to abort.")) {
		t.Errorf("tip must be dimmed when a colored style exists:\n%q", view)
	}

	// Colors disabled: identical to the plain rendering.
	plain := &Style{Color: false, TTY: true, W: &bytes.Buffer{}}
	m = selectModel{s: plain, prompt: "choose:", items: items("alpha", "beta"), selectedIndex: 1}
	view = m.View()
	if !strings.Contains(view, "> beta") || !strings.Contains(view, "  alpha") {
		t.Errorf("colors-disabled view must stay plain:\n%q", view)
	}
	if strings.Contains(view, "\x1b[") {
		t.Errorf("colors-disabled view must not carry ANSI escapes:\n%q", view)
	}
	if !strings.Contains(view, "Use ↑/↓ (or j/k) to navigate, Enter to select, Esc/q/Ctrl-C to abort.") {
		t.Errorf("tip must appear plain when colors are disabled:\n%q", view)
	}
}
