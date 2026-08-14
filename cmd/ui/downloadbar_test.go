package ui

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// stripANSI removes SGR color codes from a TTY render so assertions
// can match the plain text.
func stripANSI(s string) string {
	var b strings.Builder
	in := false
	for _, r := range s {
		if r == '\x1b' {
			in = true
			continue
		}
		if in {
			if r == 'm' {
				in = false
			}
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// TestDownloadBarTTYThrottle: on a TTY the first frame draws
// immediately, a Set within the throttle window does not redraw, a
// Set after the window does, and Finish erases the line and prints
// the final state exactly once.
func TestDownloadBarTTYThrottle(t *testing.T) {
	var buf bytes.Buffer
	s := &Style{Color: true, TTY: true, W: &buf}
	bar := NewDownloadBar(s, "eka-linux-amd64", 100)
	bar.Set(50) // within the window: no extra frame
	bar.Finish()
	bar.Finish() // idempotent

	out := buf.String()
	if !strings.HasPrefix(out, "\r\x1b[K") {
		t.Errorf("first frame must draw immediately, got %q", out)
	}
	plain := stripANSI(out)
	if !strings.Contains(plain, "0 B / 100 B (0%)") {
		t.Errorf("first frame must show the initial progress, got %q", plain)
	}
	if strings.Contains(plain, "50 B / 100 B") {
		t.Errorf("a Set within the throttle window must not redraw, got %q", plain)
	}
	if !strings.Contains(plain, "✓ downloaded eka-linux-amd64 (100 B)\n") {
		t.Errorf("final state must be the success line, got %q", plain)
	}
	if strings.Count(plain, "✓") != 1 {
		t.Errorf("Finish must print the final line exactly once, got %q", plain)
	}

	// A Set after the throttle window redraws. The interval is a
	// package var so the test can shrink it deterministically.
	old := downloadBarInterval
	downloadBarInterval = time.Millisecond
	defer func() { downloadBarInterval = old }()
	var buf2 bytes.Buffer
	s2 := &Style{Color: true, TTY: true, W: &buf2}
	bar2 := NewDownloadBar(s2, "eka-linux-amd64", 100)
	time.Sleep(2 * time.Millisecond)
	bar2.Set(50)
	if !strings.Contains(stripANSI(buf2.String()), "50 B / 100 B (50%)") {
		t.Errorf("a Set after the throttle window must redraw, got %q", buf2.String())
	}
}

// TestDownloadBarNegativeTotal: a negative total (unknown
// Content-Length) is clamped to 0 — the indeterminate form renders no
// bar, and Finish prints "(0 B)" without panicking.
func TestDownloadBarNegativeTotal(t *testing.T) {
	var buf bytes.Buffer
	s := &Style{Color: true, TTY: true, W: &buf}
	bar := NewDownloadBar(s, "eka-linux-amd64", -1)
	bar.Set(1024)
	bar.Finish()

	out := buf.String()
	plain := stripANSI(out)
	if strings.Contains(plain, "EB") || strings.Contains(plain, "18446744073709551615") {
		t.Errorf("a negative total must not leak into the output, got %q", plain)
	}
	if !strings.Contains(plain, "0 B downloaded") {
		t.Errorf("indeterminate frames must show only the byte count, got %q", plain)
	}
	if !strings.Contains(plain, "✓ downloaded eka-linux-amd64 (0 B)\n") {
		t.Errorf("Finish must show the clamped total, got %q", plain)
	}
}

// TestDownloadBarAbort: Abort erases the progress line on a TTY
// without printing the success line, is a no-op on a non-TTY (the
// deterministic start line stands), and is idempotent.
func TestDownloadBarAbort(t *testing.T) {
	var buf bytes.Buffer
	s := &Style{Color: true, TTY: true, W: &buf}
	bar := NewDownloadBar(s, "eka-linux-amd64", 100)
	bar.Abort()
	bar.Abort() // idempotent
	out := buf.String()
	if strings.Contains(stripANSI(out), "✓") {
		t.Errorf("Abort must not print the success line, got %q", out)
	}
	// The erase sequence with the embedded margin (the wrapped writer
	// skips carriage-return redraws — see margin.go).
	if !strings.HasSuffix(out, "\r\x1b[K  ") {
		t.Errorf("Abort must erase the progress line on a TTY, got %q", out)
	}

	var buf2 bytes.Buffer
	s2 := &Style{Color: false, TTY: false, W: &buf2}
	bar2 := NewDownloadBar(s2, "eka-linux-amd64", 100)
	bar2.Abort()
	out2 := buf2.String()
	if out2 != "↓ downloading eka-linux-amd64...\n" {
		t.Errorf("non-TTY Abort must leave exactly the deterministic start line, got %q", out2)
	}
	if strings.Contains(out2, "\x1b") || strings.Contains(out2, "\r") {
		t.Errorf("non-TTY output must not contain control sequences, got %q", out2)
	}
}
