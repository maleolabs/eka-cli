// Package ui provides the presentation primitives of the EKA CLI.
//
// It is a subpackage of cmd and deliberately contains no business logic:
// it only knows how to render bytes to a writer, given a Style. Every
// renderer in this package is a pure function of (data, style) — the only
// time-dependent output is the TTY-only spinner animation, and even that
// ends in a deterministic final state.
//
// Determinism contract (see also cmd/root.go):
//
//   - Non-TTY output (pipes, CI, tests) is plain text plus UTF-8 icons:
//     no ANSI escapes, no "\r", no spinner frames, byte-identical across
//     runs.
//   - TTY output carries the same structure, plus colors and in-place
//     tree redraws.
//
// The single decision point is Style: it is created once per command
// execution from the writer and the --verbose flag, and every renderer
// takes it explicitly. There is no global state and no ambient
// terminal detection.
package ui

import (
	"io"
	"os"

	"golang.org/x/term"
)

// Style carries the presentation context for one command execution:
// whether the writer is a terminal, whether colors are enabled, whether
// --verbose was given, and the destination writer.
type Style struct {
	// Color reports whether ANSI SGR colors may be emitted.
	Color bool
	// TTY reports whether the writer is a real terminal (in-place tree
	// redraws and the spinner animation are only safe on a TTY).
	TTY bool
	// Verbose mirrors the presentation-only --verbose flag: it adds
	// detail lines (per-unit lists, plan actions) to command output.
	Verbose bool
	// W is the destination writer for all HUMAN command output — a
	// line-indenting writer applying the container's uniform left
	// margin. Machine output (eka get JSON) and self-padded blocks
	// use Raw() instead.
	W io.Writer
	// Width is the terminal display width in cells when W is a TTY and
	// the size could be queried; 0 when unknown (pipes, CI, tests) —
	// renderers then fall back to their fixed widths, keeping non-TTY
	// output byte-identical.
	Width int

	// raw is the unwrapped writer W wraps (see Raw).
	raw io.Writer
}

// IsTTY reports whether w is a terminal: an *os.File whose underlying
// fd answers true to term.IsTerminal. Any other writer (bytes.Buffer,
// strings.Reader, a pipe, a regular file) is not a terminal.
func IsTTY(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(int(f.Fd()))
}

// terminalWidth returns the display width of w when it is a terminal,
// or 0 when the size cannot be queried.
func terminalWidth(w io.Writer) int {
	f, ok := w.(*os.File)
	if !ok {
		return 0
	}
	width, _, err := term.GetSize(int(f.Fd()))
	if err != nil {
		return 0
	}
	return width
}

// colorEnabled reports whether colors should be emitted on w: the
// writer must be a terminal, NO_COLOR must be unset, and TERM must not
// be "dumb". The NO_COLOR convention (https://no-color.org) is honored
// for the standard environment variables only.
func colorEnabled(w io.Writer) bool {
	if !IsTTY(w) {
		return false
	}
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	if os.Getenv("TERM") == "dumb" {
		return false
	}
	return true
}

// NewStyle builds the Style for one command execution against w.
// Verbose is the presentation-only --verbose flag value.
func NewStyle(w io.Writer, verbose bool) *Style {
	return &Style{
		Color:   colorEnabled(w),
		TTY:     IsTTY(w),
		Verbose: verbose,
		W:       newMarginWriter(w),
		Width:   terminalWidth(w),
		raw:     w,
	}
}

// Raw returns the UNWRAPPED writer: machine output (eka get JSON) and
// self-padded blocks (the landing container, the interactive menu)
// bypass the global left margin through it. Human output must go
// through W. Styles constructed directly (tests) carry no wrapper —
// Raw falls back to W.
func (s *Style) Raw() io.Writer {
	if s.raw != nil {
		return s.raw
	}
	return s.W
}
