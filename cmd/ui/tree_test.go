package ui

import (
	"bytes"
	"strings"
	"testing"
)

// TestTreeTTYRedrawMargin: the in-place TTY redraw embeds the
// container margin after each erase sequence (carriage-return writes
// are skipped by the margin writer, so the tree carries its own).
func TestTreeTTYRedrawMargin(t *testing.T) {
	var buf bytes.Buffer
	s := NewStyle(&buf, false) // production wiring: W margin-wrapped
	s.TTY = true
	s.Color = true
	tree := NewTree(s, "Repository")
	n := tree.Add("[1/5] Discover")
	n.Done("scanned")
	tree.Finish()
	out := buf.String()
	if !strings.Contains(out, Margin+"\x1b[38;5;75mRepository") {
		t.Errorf("the root heading must render with the margin (via the wrapped writer):\n%q", out)
	}
	if !strings.Contains(out, "\r\x1b[K"+Margin+"\x1b[38;5;114m✓ [1/5] Discover") {
		t.Errorf("TTY redraw node lines must embed the margin:\n%q", out)
	}
}
