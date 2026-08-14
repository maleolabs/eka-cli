package ui

import (
	"fmt"
	"sync"
	"time"
)

// spinnerInterval is the animation period for the TTY spinner.
const spinnerInterval = 100 * time.Millisecond

// Spinner renders a contextual loading state. Every spinner MUST carry
// a message ("Discovering repository...", "Loading Engineering
// Knowledge...") — a bare spinner is never acceptable.
//
// On a TTY with color enabled it prints the message with an animated
// Braille frame via "\r" on a private goroutine. On a non-TTY — and on
// a TTY with color disabled — it prints the message once,
// deterministically, and the animation methods are no-ops: no frames,
// no "\r", no erase codes in plain output.
//
// The animation can be PAUSED and RESUMED around an interactive prompt:
//
//	sp := NewSpinner(s, "Working...")  // starts (or prints once)
//	sp.Halt()                          // silent pause — no final line
//	... interactive prompt ...
//	sp.Start()                         // resume the animation
//	sp.Stop()                          // final: halt + print "✓ <message>"
//
// Stop is idempotent: the final line prints at most once, no matter
// how many times Halt/Start/Stop were called.
type Spinner struct {
	s   *Style
	msg string

	mu        sync.Mutex
	stop      chan struct{} // nil while halted; captured by the animator
	wg        sync.WaitGroup
	finalDone bool // the final "✓" line was printed
}

// animated reports whether the frame animation may run: the writer must
// be a terminal and colors must be enabled, so no control sequences
// ever reach plain output.
func (sp *Spinner) animated() bool {
	return sp.s.TTY && sp.s.Color
}

// NewSpinner starts the spinner. On a non-TTY (or TTY without color)
// the deterministic "message" line is printed immediately; on a TTY
// with color the animation starts and the first frame is drawn right
// away.
func NewSpinner(s *Style, message string) *Spinner {
	sp := &Spinner{s: s, msg: message}
	if !sp.animated() {
		fmt.Fprintln(s.W, message)
		return sp
	}
	sp.Start()
	return sp
}

// Start begins — or resumes — the frame animation. No-op when the
// spinner is not animated or already running. A halted spinner resumes
// on a fresh channel; the animator captures the channel at Start time,
// so a concurrent Halt/Start cycle can never stop the wrong animator.
func (sp *Spinner) Start() {
	if !sp.animated() {
		return
	}
	sp.mu.Lock()
	defer sp.mu.Unlock()
	if sp.stop != nil {
		return // already running
	}
	sp.stop = make(chan struct{})
	sp.wg.Add(1)
	go sp.animate(sp.stop)
}

// Halt pauses the animation WITHOUT printing the final line: the
// spinner may be resumed with Start. It is the pause used before an
// interactive prompt (the menu must render cleanly, no animation
// overwriting it). No-op when not running or not animated.
func (sp *Spinner) Halt() {
	if !sp.animated() {
		return
	}
	sp.mu.Lock()
	ch := sp.stop
	sp.stop = nil
	sp.mu.Unlock()
	if ch == nil {
		return
	}
	close(ch)
	sp.wg.Wait()
}

// Stop halts the animation and prints the deterministic final line
// ("✓ <message>") on a TTY with color. Otherwise it is a no-op: the
// start line was already deterministic. Stop is idempotent — the final
// line prints at most once, no matter how many Halt/Start/Stop cycles
// preceded it.
func (sp *Spinner) Stop() {
	if !sp.animated() {
		return
	}
	sp.Halt()
	sp.mu.Lock()
	defer sp.mu.Unlock()
	if sp.finalDone {
		return
	}
	sp.finalDone = true
	// The margin is embedded after the erase sequence (the wrapped
	// writer skips carriage-return redraws — see margin.go).
	fmt.Fprintf(sp.s.W, "\r\x1b[K%s%s %s\n",
		Margin, sp.s.Success(IconDone), sp.s.Progress(sp.msg))
}

// animate cycles the spinner frames on the spinner's own line until
// stop is closed. The frame index starts at the first frame so the
// first render is immediate. The channel is captured at Start time —
// the loop never re-reads sp.stop, so a later Halt/Start cycle cannot
// interfere.
func (sp *Spinner) animate(stop chan struct{}) {
	defer sp.wg.Done()
	frame := 0
	draw := func() {
		fmt.Fprintf(sp.s.W, "\r\x1b[K%s%s %s",
			Margin,
			sp.s.Progress(SpinnerFrames[frame%len(SpinnerFrames)]), sp.msg)
	}
	draw()
	ticker := time.NewTicker(spinnerInterval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			frame++
			draw()
		}
	}
}
