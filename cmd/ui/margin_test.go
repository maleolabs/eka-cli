package ui

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

// TestMarginWriterIndentsLines: every non-blank line gets the margin,
// blank lines stay truly blank, and multi-line writes are handled.
func TestMarginWriterIndentsLines(t *testing.T) {
	var buf bytes.Buffer
	w := newMarginWriter(&buf)
	w.Write([]byte("one\n"))
	w.Write([]byte("two\n\nthree\n"))
	w.Write([]byte("four"))
	want := "  one\n  two\n\n  three\n  four"
	if buf.String() != want {
		t.Fatalf("margin writer:\ngot  %q\nwant %q", buf.String(), want)
	}
}

// TestMarginWriterMidLineSegments: a write continuing a line (no
// trailing newline) does not re-indent the next write.
func TestMarginWriterMidLineSegments(t *testing.T) {
	var buf bytes.Buffer
	w := newMarginWriter(&buf)
	w.Write([]byte("hello "))
	w.Write([]byte("world\n"))
	want := "  hello world\n"
	if buf.String() != want {
		t.Fatalf("mid-line segments:\ngot  %q\nwant %q", buf.String(), want)
	}
}

// TestMarginWriterCarriageReturnRedraw: a \r-prefixed write is a
// redraw — no margin is inserted (the caller embeds its own); the
// state does not leak into the next write.
func TestMarginWriterCarriageReturnRedraw(t *testing.T) {
	var buf bytes.Buffer
	w := newMarginWriter(&buf)
	w.Write([]byte("done\n"))
	w.Write([]byte("\r\x1b[Kframe"))
	w.Write([]byte("\n"))
	want := "  done\n\r\x1b[Kframe\n"
	if buf.String() != want {
		t.Fatalf("redraw handling:\ngot  %q\nwant %q", buf.String(), want)
	}
}

// TestMarginWriterBlankFirstLine: a write starting with a newline
// (blank line) gets no margin on that line.
func TestMarginWriterBlankFirstLine(t *testing.T) {
	var buf bytes.Buffer
	w := newMarginWriter(&buf)
	w.Write([]byte("\ncontent\n"))
	want := "\n  content\n"
	if buf.String() != want {
		t.Fatalf("blank first line:\ngot  %q\nwant %q", buf.String(), want)
	}
}

// TestMarginWriterAtLineStart: a fresh writer indents the first line.
func TestMarginWriterAtLineStart(t *testing.T) {
	var buf bytes.Buffer
	w := newMarginWriter(&buf)
	w.Write([]byte("first\n"))
	if buf.String() != "  first\n" {
		t.Fatalf("first line: got %q, want %q", buf.String(), "  first\n")
	}
}

// TestMarginWriterMatchesContainerMargin: the writer's margin is the
// same constant the container uses — the two padding systems agree.
func TestMarginWriterMatchesContainerMargin(t *testing.T) {
	if strings.Repeat(" ", 2) != Margin {
		t.Fatalf("Margin = %q, want two spaces", Margin)
	}
}

// TestMarginWriterWriteCount: the reported write count is exactly the
// consumed p bytes (never the margin bytes) — the io.Writer contract
// io.Copy relies on ("invalid Write count" panic regression).
func TestMarginWriterWriteCount(t *testing.T) {
	var buf bytes.Buffer
	w := newMarginWriter(&buf)
	// Multi-line write where the OLD implementation reported
	// len(p) + margin bytes (the watch-frame panic).
	p := []byte("hello\nworld\n")
	n, err := w.Write(p)
	if err != nil {
		t.Fatal(err)
	}
	if n != len(p) {
		t.Errorf("Write returned %d for %d bytes (must never exceed len(p))", n, len(p))
	}
	// io.Copy over the margin writer must not panic.
	var out bytes.Buffer
	cw := newMarginWriter(&out)
	frame := []byte("line one\nline two\n\nline three")
	written, err := io.Copy(cw, bytes.NewReader(frame))
	if err != nil {
		t.Fatalf("io.Copy over the margin writer failed: %v", err)
	}
	if written != int64(len(frame)) {
		t.Errorf("io.Copy wrote %d, want %d (the margin bytes are not counted)", written, len(frame))
	}
	if !strings.Contains(out.String(), "  line one\n  line two\n\n  line three") {
		t.Errorf("margin output = %q", out.String())
	}
}
