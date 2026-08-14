package ui

import (
	"fmt"
	"strings"
)

// This file implements the output container: the uniform padding that
// keeps human output from sticking to the terminal corner. Every
// human-facing surface (the root landing, the help text, the command
// context headers) renders inside the container — a leading and a
// trailing blank line (vertical padding) and a uniform left margin
// (horizontal padding).

// Margin is the uniform left margin of the output container — the
// constant string of spaces applied by the container, the margin
// writer, the spinner redraws and the interactive menu.
const Margin = "  "

// Container renders a text block as if inside a padded frame: a
// leading and a trailing blank line (vertical padding) and a uniform
// left margin on every line (horizontal padding). Empty content
// renders as a single blank line. Deterministic on TTY and non-TTY —
// plain text, no control sequences. It writes through Raw: the block
// IS the container (the global margin of the style's wrapped writer
// must not double the padding).
func Container(s *Style, content string) {
	fmt.Fprintln(s.Raw())
	trimmed := strings.TrimRight(content, "\n")
	if trimmed == "" {
		fmt.Fprintln(s.Raw())
		return
	}
	for _, line := range strings.Split(trimmed, "\n") {
		fmt.Fprintf(s.Raw(), "%s%s\n", Margin, line)
	}
	fmt.Fprintln(s.Raw())
}
