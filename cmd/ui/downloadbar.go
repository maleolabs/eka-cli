package ui

import (
	"fmt"
	"time"

	"github.com/dustin/go-humanize"
)

// downloadBarInterval throttles the in-place redraws of the download
// bar: at most one frame per interval, so a fast local server cannot
// flood the terminal. It is a var (not a const) so the package tests
// can shrink it for deterministic throttle assertions.
var downloadBarInterval = 100 * time.Millisecond

// DownloadBar renders realtime download progress (used by `eka
// update`). It follows the spinner's TTY discipline exactly:
//
//   - On a TTY with color enabled it redraws one line in place via
//     "\r\x1b[K" (throttled to downloadBarInterval, first frame drawn
//     immediately):
//
//     ↓ eka-linux-amd64  ██████░░░░  3.2 MB / 12.4 MB (26%)
//
//   - With no Content-Length (total == 0) no bar is rendered — a bar
//     without a total would lie:
//
//     ↓ eka-linux-amd64  3.2 MB downloaded
//
//   - On a non-TTY — and on a TTY with color disabled — the
//     constructor prints one deterministic line and Set is a no-op:
//     no frames, no "\r", no erase codes in plain output.
//
// Finish prints the deterministic final line ("✓ downloaded <name>
// (<total>)") exactly once, on every environment; Abort ends the bar
// silently (no success line) when the download failed. The bar is
// driven from a single goroutine (the download loop calls Set per
// read chunk, then Finish or Abort once) — there is deliberately no
// lock.
type DownloadBar struct {
	s    *Style
	name string
	tot  int64

	done     int64
	lastDraw time.Time
	finished bool
}

// animated reports whether the in-place redraw may run: the writer
// must be a terminal and colors must be enabled, so no control
// sequences ever reach plain output.
func (b *DownloadBar) animated() bool {
	return b.s.TTY && b.s.Color
}

// NewDownloadBar starts the download progress for name with the given
// total byte count (0 or negative = unknown, indeterminate). On a
// non-TTY (or TTY without color) the deterministic "↓ downloading
// <name>..." line is printed immediately; on a TTY with color the
// first frame is drawn right away.
func NewDownloadBar(s *Style, name string, total int64) *DownloadBar {
	if total < 0 {
		total = 0 // negative Content-Length is an unknown total
	}
	b := &DownloadBar{s: s, name: name, tot: total}
	if !b.animated() {
		fmt.Fprintf(s.W, "%s downloading %s...\n", s.Progress(IconDown), s.Progress(name))
		return b
	}
	b.lastDraw = time.Now()
	b.draw()
	return b
}

// Set reports done bytes downloaded. On a TTY with color it redraws
// the progress line, throttled to downloadBarInterval (the first
// frame was already drawn by the constructor). No-op otherwise.
func (b *DownloadBar) Set(done int64) {
	if !b.animated() {
		return
	}
	if done < b.done {
		return // never move backwards
	}
	b.done = done
	if time.Since(b.lastDraw) < downloadBarInterval {
		return
	}
	b.lastDraw = time.Now()
	b.draw()
}

// Abort ends the bar WITHOUT the success line: the download failed.
// On a TTY the in-place progress line is erased (the refusal explains
// why); on a non-TTY it is a no-op — the deterministic start line was
// already printed by the constructor. Abort is idempotent.
func (b *DownloadBar) Abort() {
	if b.finished {
		return
	}
	b.finished = true
	if b.animated() {
		fmt.Fprintf(b.s.W, "\r\x1b[K%s", Margin)
	}
}

// Finish prints the deterministic final line ("✓ downloaded <name>
// (<total>)"). On a TTY with color it first erases the progress line
// (mirroring Spinner.Stop: margin embedded after the erase sequence).
// Finish is idempotent — the final line prints at most once.
func (b *DownloadBar) Finish() {
	if b.finished {
		return
	}
	b.finished = true
	line := fmt.Sprintf("%s downloaded %s (%s)\n",
		b.s.Success(IconDone), b.s.Progress(b.name), humanize.Bytes(uint64(b.tot)))
	if !b.animated() {
		fmt.Fprint(b.s.W, line)
		return
	}
	fmt.Fprintf(b.s.W, "\r\x1b[K%s%s", Margin, line)
}

// draw renders the current progress frame in place: the erase
// sequence, the embedded margin (the wrapped writer skips
// carriage-return redraws — see margin.go), the download arrow and
// name, the completion bar and the byte/percent readout. Without a
// total the bar would lie, so only the byte count is shown.
func (b *DownloadBar) draw() {
	done := humanize.Bytes(uint64(b.done))
	if b.tot <= 0 {
		fmt.Fprintf(b.s.W, "\r\x1b[K%s%s %s  %s downloaded",
			Margin, b.s.Progress(IconDown), b.s.Progress(b.name), done)
		return
	}
	total := humanize.Bytes(uint64(b.tot))
	pct := int(b.done * 100 / b.tot)
	fmt.Fprintf(b.s.W, "\r\x1b[K%s%s %s  %s  %s / %s (%d%%)",
		Margin, b.s.Progress(IconDown), b.s.Progress(b.name),
		ProgressBar(b.s, int(b.done), int(b.tot)), done, total, pct)
}
