package ui

import (
	"bytes"
	"io"
)

// This file implements the global horizontal padding of the output
// container: a line-indenting writer that prefixes every rendered line
// with the uniform left margin, so human output never sticks to the
// terminal's left edge. The margin is an OUTPUT-LAYER concern — the
// renderers stay margin-agnostic; NewStyle wraps the human-output
// writer with it, and Style.Raw exposes the unwrapped writer for
// machine output (eka get JSON) and self-padded blocks (the landing
// container, the interactive menu).
//
// Blank lines stay truly blank (no trailing margin) and carriage-
// return redraws (the spinner's in-place animation) are left to the
// writer that embeds its own margin after the erase sequence.

// Margin is the uniform left margin of the output container, in
// columns.
// marginWriter indents every non-blank line of the wrapped writer with
// Margin spaces. Writes are line-steppable: the margin is inserted at
// the start of a write (when the previous write ended on a fresh
// line) and after every newline (except before another newline — a
// blank line). A write that starts with a carriage return is a
// redraw: the margin is skipped and the caller embeds its own.
type marginWriter struct {
	w           io.Writer
	margin      string
	atLineStart bool
}

// newMarginWriter wraps w with the line-indenting margin.
func newMarginWriter(w io.Writer) io.Writer {
	return &marginWriter{w: w, margin: Margin, atLineStart: true}
}

// MarginWriter wraps w with the container's line-indenting margin —
// the same wrapping NewStyle applies to human output. It is the
// escape hatch for renderers that build their output into a private
// buffer and must match the view path byte-for-byte (the watch
// frame).
func MarginWriter(w io.Writer) io.Writer { return newMarginWriter(w) }

// Write indents every non-blank line. It returns the number of bytes
// of p consumed (matching io.Writer semantics — the margin bytes are
// NOT counted: a Write must never report more than len(p), or
// io.Copy panics with "invalid Write count").
func (m *marginWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	written := 0
	i := 0
	if m.atLineStart && p[0] != '\r' && p[0] != '\n' {
		if _, err := io.WriteString(m.w, m.margin); err != nil {
			return 0, err
		}
		m.atLineStart = false
	} else if m.atLineStart {
		m.atLineStart = false
	}
	for i < len(p) {
		j := bytes.IndexByte(p[i:], '\n')
		if j < 0 {
			if _, err := m.w.Write(p[i:]); err != nil {
				return written, err
			}
			written += len(p) - i
			return written, nil
		}
		if _, err := m.w.Write(p[i : i+j+1]); err != nil {
			return written, err
		}
		written += j + 1
		i += j + 1
		if i >= len(p) {
			m.atLineStart = true
			return written, nil
		}
		if p[i] == '\n' {
			continue // a blank line: no margin on it
		}
		if _, err := io.WriteString(m.w, m.margin); err != nil {
			return written, err
		}
	}
	return written, nil
}
