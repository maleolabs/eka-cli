package cmd

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"github.com/maleolabs/eka-cli/cmd/ui"
	"github.com/maleolabs/eka-core/metadata"
	"github.com/maleolabs/eka-core/runtime"
	"github.com/maleolabs/eka-core/view"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// This file implements the `eka watch` command: the live version of
// `eka view`. The projection surface and the frame renderers are
// exactly the view ones — a watch frame of a synced repository is the
// one-shot view output plus the watching footer, byte for byte. Watch
// adds only the presentation loop: a polling interval, change
// detection (identical frames are not redrawn), a clear-screen on open
// and on exit, and SIGINT handling.
//
// The projection source is the EKA workspace canonical store: every
// cycle re-reads the project's units from the store, so a 'eka sync'
// run in another terminal is picked up without a restart. While the
// tree is not an EKA repository (no eka.yaml) or the repository is
// not registered the watch shows a calm refusal frame instead of the
// projection: it keeps polling and flips to the projection
// automatically once the tree carries eka.yaml and the repository is
// registered and synced. No Markdown is read at projection time. There
// is no fsnotify and no new dependency: the store is re-read on a
// timer.
//
// Watch is a live display: stdout must be a terminal. The terminal
// gate runs after argument validation, so usage errors stay
// deterministic (and testable) on any writer; the non-TTY error keeps
// piped/CI behavior explicit.

// flagWatchInterval is the polling interval flag of `eka watch`
// (seconds).
const flagWatchInterval = "interval"

// clearScreen is the ANSI clear-screen + cursor-home sequence: wipe the
// terminal and put the cursor at the top-left so every frame starts on
// a clean surface. Emitted before the first frame and after the loop
// (SIGINT), so the shell prompt restarts clean. TTY-only by
// construction: watch never runs without a terminal.
const clearScreen = "\x1b[2J\x1b[H"

// hideCursor/showCursor are the ANSI hide/show-cursor sequences: a
// live display must not carry a blinking cursor over the frame; the
// cursor is hidden after the initial clear and shown again on every
// exit path (interrupt and error), before the terminal is restored.
const (
	hideCursor = "\x1b[?25l"
	showCursor = "\x1b[?25h"
)

// crlfWriter converts bare line feeds to CRLF on the wire: watch runs
// the terminal in raw mode (OPOST off), where a bare \n no longer moves
// the cursor back to column 0 — the frame must reach the terminal as
// \r\n to render aligned. The frame bytes stay \n-only (deterministic,
// byte-pinned); the conversion happens only at the terminal write.
type crlfWriter struct {
	w io.Writer
}

// Write converts each \n not already preceded by \r into \r\n. It
// reports len(p) on success (the io.Writer contract counts p consumed,
// not bytes written) and the consumed prefix on error.
func (c *crlfWriter) Write(p []byte) (int, error) {
	last := 0
	for i := 0; i < len(p); i++ {
		if p[i] == '\n' && (i == 0 || p[i-1] != '\r') {
			if _, err := c.w.Write(p[last:i]); err != nil {
				return last, err
			}
			if _, err := io.WriteString(c.w, "\r\n"); err != nil {
				return i, err
			}
			last = i + 1
		}
	}
	if last < len(p) {
		if _, err := c.w.Write(p[last:]); err != nil {
			return last, err
		}
	}
	return len(p), nil
}

// newWatchCommand builds the `eka watch` command.
//
// Exit codes:
//
//	0  clean stop (Ctrl-C)
//	2  usage or internal error (unknown projection, missing ticket
//	   target, invalid interval, non-terminal stdout, workspace or
//	   store failure). Note: the not-an-EKA-repository and
//	   unregistered-repository states are rendered frames (like view's
//	   exit-1 refusals), never exits — watch keeps polling.
func newWatchCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "watch <projection> [target]",
		Short: "Watch a projection live, redrawn on change",
		Long: `Live projection of the Engineering Knowledge Model of the project
owning the repository rooted at the current directory: 'eka watch' is
'eka view' in a loop — the projection is re-rendered in place on a
polling interval and redrawn only when it changed (no flicker on
stable state).

On every cycle the projection is rebuilt from the EKA workspace
canonical store (default ~/.eka, or $EKA_HOME): the units of the whole
project are re-read from the store, so 'eka sync' in another terminal
is picked up without a restart. While the tree is not an EKA
repository (no eka.yaml) or the repository is not registered the watch
shows a calm refusal frame instead of the projection; it keeps polling
and flips to the projection automatically once the tree carries
eka.yaml and the repository is registered and synced. No Markdown is
read at projection time.

Projections (the same surface as 'eka view'):

  discovery    the Discovery domain: vis-, str-, req-, fnd- artifacts
               grouped by type with their content states
  architecture the Architecture domain: adr-, dec-, arc-, spec-, std-,
               gls- artifacts grouped by type with their content states
  planning     the Planning domain: scp-, epc-, plan-, trc- artifacts
               grouped by type with content state, planning state and
               phase context
  execution    the active execution container: its tickets with the
               status projected from their work items, and its work
               items grouped by execution state
  operations   the Operations domain: run-, rel- artifacts grouped by
               type with their content states
  ticket       one ticket's projected status, derived from the
               referenced work item's execution state

Aliases:

  sprint, wave resolve to the execution projection (identical output)

The target argument is required by the ticket projection only.

watch is a live display: stdout must be a terminal. For one-shot
output use 'eka view'.

Exit codes:
  0  clean stop (Ctrl-C)
  2  usage or internal error (unknown projection, missing ticket
     target, invalid interval, non-terminal stdout)`,
		Example: `  eka watch execution
  eka watch execution --interval 5
  eka watch ticket tkt-sto-alpha`,
		Args: cobra.MaximumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return fmt.Errorf("watch requires a projection — available projections: %s",
					view.HelpList())
			}
			name, target, err := parseProjectionArgs(args)
			if err != nil {
				return err
			}
			interval, err := cmd.Flags().GetInt(flagWatchInterval)
			if err != nil {
				return err
			}
			if interval < 1 {
				return fmt.Errorf("invalid interval %d: must be at least 1 second", interval)
			}
			// Terminal gate: watch is a live display. The deterministic
			// non-TTY error keeps piped/CI behavior explicit.
			if !ui.IsTTY(cmd.OutOrStdout()) {
				return fmt.Errorf("watch requires a terminal (stdout is not a TTY); use 'eka view' for one-shot output")
			}
			return runWatch(styleFor(cmd), name, target, interval)
		},
	}
	cmd.Flags().Int(flagWatchInterval, 2, "polling interval in seconds (minimum 1)")
	return cmd
}

// watchRawInput puts the terminal into raw input mode for the watch
// session: echo and canonical processing are disabled, so keystrokes
// are neither echoed onto the live display nor accepted as input —
// watch is read-only and never reads input. Returns nil when stdin is
// not a terminal (nothing to suppress; the SIGINT path still works).
func watchRawInput() func() {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return nil
	}
	state, err := term.MakeRaw(fd)
	if err != nil {
		return nil
	}
	return func() { term.Restore(fd, state) }
}

// watchCtrlC forwards the raw-mode Ctrl-C keypress (byte 0x03) to the
// interrupt channel — in raw mode ISIG is off, so the kernel no longer
// generates SIGINT for Ctrl-C. Every other byte is consumed and
// ignored (read-only display). Returns on reader EOF/error.
func watchCtrlC(r io.Reader, ch chan<- os.Signal) {
	var b [1]byte
	for {
		n, err := r.Read(b[:])
		if n > 0 && b[0] == 0x03 {
			ch <- os.Interrupt
		}
		if err != nil {
			return
		}
	}
}

// runWatch runs the live loop: clear the screen, render the first
// frame immediately (no initial delay), then re-render every interval
// seconds, redrawing the terminal only when the frame changed. SIGINT
// clears the screen and stops cleanly (exit 0); a single handler is
// enough — the loop is short and the cleanup is one write. The signal
// handler is installed only on the TTY path (this function is only
// reached past watch's terminal gate).
func runWatch(s *ui.Style, projection, target string, interval int) error {
	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt)
	defer signal.Stop(interrupt)

	// Watch is a read-only display: the terminal input is put into raw
	// mode (no echo, no accepted input); Ctrl-C arrives as a byte and is
	// forwarded to the interrupt channel. SIGINT (external kill) stays
	// handled. When stdin is not a terminal the raw mode is skipped and
	// SIGINT alone stops the watch.
	restore := watchRawInput()
	if restore != nil {
		defer restore()
		go watchCtrlC(os.Stdin, interrupt)
	}

	// Every exit path (interrupt or error) shows the cursor again before
	// the terminal is restored — the shell prompt must not inherit a
	// hidden cursor.
	defer func() {
		fmt.Fprint(s.Raw(), showCursor)
		flush(s.Raw())
	}()

	// The Runtime is the projection source. It is opened once; every
	// cycle re-reads the project's units, so a 'eka sync' run
	// (registration + seeding) is picked up without a restart.
	r, err := runtime.Ensure()
	if err != nil {
		return err // Exit 2: workspace resolution.
	}
	defer r.Close()

	out := &crlfWriter{w: s.W}

	writeClearScreen(s)
	fmt.Fprint(s.Raw(), hideCursor)
	flush(s.Raw())
	prev, err := renderWatchFrame(s, r, projection, target, interval)
	if err != nil {
		return err
	}
	io.Copy(out, bytes.NewReader(prev))
	flush(s.W)

	render := func() error {
		frame, err := renderWatchFrame(s, r, projection, target, interval)
		if err != nil {
			return err
		}
		if !frameChanged(prev, frame) {
			return nil
		}
		writeClearScreen(s)
		io.Copy(out, bytes.NewReader(frame))
		flush(s.W)
		prev = frame
		return nil
	}

	ticker := time.NewTicker(time.Duration(interval) * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-interrupt:
			writeClearScreen(s)
			return nil
		case <-ticker.C:
			if err := render(); err != nil {
				return err
			}
		}
	}
}

// renderWatchFrame runs one watch cycle into a fresh buffer: the
// project's canonical units are re-read from the Runtime, then either
// the projection frame — byte-identical to the one-shot view output
// plus the watching footer — or the unregistered-repository refusal
// frame. The frame is a pure function of (Runtime state, projection,
// target, interval): no clock, no timestamps, so identical states
// produce identical frames and the loop skips redraws by byte
// comparison. A store/registry failure is an error (exit 2); an
// unregistered repository is a rendered frame, never an exit.
//
// The base style is copied and its writer replaced with the buffer, so
// the frame carries exactly the color/TTY settings of the live stdout
// (a verified TTY for watch).
func renderWatchFrame(s *ui.Style, r *runtime.Runtime, projection, target string, interval int) ([]byte, error) {
	frame := *s
	var buf bytes.Buffer
	// The frame must match the one-shot view output byte-for-byte:
	// the buffer gets the same margin wrapping NewStyle applies.
	frame.W = ui.MarginWriter(&buf)

	abs, err := filepath.Abs(".")
	if err != nil {
		return nil, fmt.Errorf("watch failed: %w", err)
	}
	abs = filepath.Clean(abs)
	// The repository context gate (ADR-018), mirroring the view
	// prologue: without eka.yaml the tree is not an EKA repository and
	// the frame is the not-an-EKA-repository refusal; with metadata
	// but unregistered, the existing unregistered frame renders.
	_, _, hasMeta, err := metadata.Find(abs)
	if err != nil {
		return nil, fmt.Errorf("watch failed: %w", err)
	}
	repo, found, err := r.Workspace.FindRepo(abs)
	if err != nil {
		return nil, fmt.Errorf("watch failed: %w", err)
	}
	if !found {
		if hasMeta {
			renderWatchRefusal(&frame, abs, interval)
		} else {
			renderWatchNotEKA(&frame, abs, interval)
		}
		return buf.Bytes(), nil
	}
	units, err := r.Knowledge.UnitsByProject(repo.ProjectID)
	if err != nil {
		return nil, fmt.Errorf("watch failed: %w", err)
	}
	g := view.NewGraph(".", units)
	if numbers, nerr := r.Knowledge.NumbersByProject(repo.ProjectID); nerr == nil {
		g.AttachNumbers(numbers)
	}
	proj, err := view.Build(projection, g, target)
	if err != nil {
		return nil, fmt.Errorf("watch failed: %w", err)
	}
	renderView(&frame, g, proj, viewOptions{})
	// The watching footer is separated from the projection content by a
	// blank line — the same breathing room the refusal frames already
	// carry. The blank line stays truly blank (MarginWriter skips the
	// margin on blank lines).
	fmt.Fprintln(frame.W)
	renderWatchFooter(&frame, interval)
	return buf.Bytes(), nil
}

// renderWatchRefusal renders the unregistered-repository frame: the
// live error state of the watch when the current directory carries
// eka.yaml but is not registered in the EKA workspace. A calm
// red-tinted frame with the deterministic refusal message and the sync
// hint, ending with a dim note that the watch keeps polling (this is a
// rendered state, not an exit).
func renderWatchRefusal(s *ui.Style, abs string, interval int) {
	ui.NewHeader(s, "Repository").
		Add("Path", abs).
		Add("Knowledge", "EKA v"+standardVersion).
		Pipeline("View").
		Render()
	fmt.Fprintln(s.W)
	fmt.Fprintf(s.W, "%s\n", s.Error("Repository not registered in the EKA workspace."))
	fmt.Fprintf(s.W, "%s\n", s.Dim("Run 'eka sync' (auto-registers) or 'eka project register' first."))
	fmt.Fprintln(s.W)
	fmt.Fprintf(s.W, "%s\n", s.Dim("watching — run 'eka sync' to register the repository"))
	renderWatchFooter(s, interval)
}

// renderWatchNotEKA renders the not-an-EKA-repository frame (ADR-018):
// the live error state of the watch when the walk-up from the current
// directory finds no eka.yaml — the tree is not an EKA repository. The
// structure mirrors renderWatchRefusal: calm refusal frame, 'eka init'
// hint, and the dim note that the watch keeps polling.
func renderWatchNotEKA(s *ui.Style, abs string, interval int) {
	ui.NewHeader(s, "Repository").
		Add("Path", abs).
		Add("Knowledge", "EKA v"+standardVersion).
		Pipeline("View").
		Render()
	fmt.Fprintln(s.W)
	fmt.Fprintf(s.W, "%s\n", s.Error("Not an EKA repository (no eka.yaml)."))
	fmt.Fprintf(s.W, "%s\n", s.Dim("Run 'eka init' to create the repository marker."))
	fmt.Fprintln(s.W)
	fmt.Fprintf(s.W, "%s\n", s.Dim("watching — run 'eka init' to make this an EKA repository"))
	renderWatchFooter(s, interval)
}

// renderWatchFooter prints the one dim footer line shared by both
// frame kinds.
func renderWatchFooter(s *ui.Style, interval int) {
	fmt.Fprintf(s.W, "%s\n", s.Dim(fmt.Sprintf("watching — Ctrl-C to stop (interval %ds)", interval)))
}

// frameChanged reports whether the freshly rendered frame differs from
// the previous one. Identical frames are not redrawn: no clear-screen,
// no write, no flicker on stable state.
func frameChanged(prev, next []byte) bool {
	return !bytes.Equal(prev, next)
}

// writeClearScreen emits the clear-screen sequence and flushes. It is
// the single exit/entry point for terminal wiping (open and SIGINT
// paths both go through it).
func writeClearScreen(s *ui.Style) {
	// The clear+home sequence is terminal control, not text content:
	// it bypasses the container margin (Raw) so the wipe is exact.
	fmt.Fprint(s.Raw(), clearScreen)
	flush(s.Raw())
}

// flush pushes buffered output to the destination. os.File writes are
// unbuffered (no-op); buffered writers (*bufio.Writer) need the flush.
func flush(w io.Writer) {
	if bw, ok := w.(*bufio.Writer); ok {
		bw.Flush()
	}
}
