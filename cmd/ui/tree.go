package ui

import "fmt"

// Tree is a progressive step renderer. Callers add steps with Add and
// mark them with Done/Fail/Running; Render redraws or emits the tree.
//
// On a TTY the tree is redrawn in place: the previous tree lines are
// cleared (cursor-up + clear-to-EOL) so the active step shows a spinner
// frame and completed steps show "✓".
//
// On a non-TTY the tree emits deterministic sequential lines as steps
// complete ("├── label" / "│   ✓ detail") with no redraw and no
// spinner, so piped/CI/test output is byte-identical across runs.
//
// The root heading is printed once at construction. Every node line is
// a pure function of the node state, so the final tree (after Finish)
// is deterministic on both TTY and non-TTY.
type Tree struct {
	s     *Style
	root  string
	nodes []*Node
	// nodeLines is the number of node lines currently on screen (TTY
	// redraw bookkeeping).
	nodeLines int
}

// Node is one step of the tree. Obtain it from Tree.Add and mutate its
// state; the tree re-renders on the next Render call.
type Node struct {
	tree    *Tree
	label   string
	state   nodeState
	detail  string
	emitted bool // non-TTY: the node's lines were already printed
}

// nodeState is the lifecycle of one step.
type nodeState int

const (
	statePending nodeState = iota
	stateRunning
	stateDone
	stateFailed
)

// NewTree creates a tree rooted at root and prints the root heading
// immediately (both TTY and non-TTY). An empty root prints nothing.
func NewTree(s *Style, root string) *Tree {
	t := &Tree{s: s, root: root}
	if root != "" {
		fmt.Fprintln(s.W, s.Accent(root))
	}
	return t
}

// Add appends a step to the tree and returns its handle.
func (t *Tree) Add(label string) *Node {
	n := &Node{tree: t, label: label}
	t.nodes = append(t.nodes, n)
	return n
}

// Done marks the step as completed with an optional detail string.
func (n *Node) Done(detail string) {
	n.state = stateDone
	n.detail = detail
}

// Fail marks the step as failed with an optional detail string. The
// rendered line carries the word "failed" — never color alone.
func (n *Node) Fail(detail string) {
	n.state = stateFailed
	n.detail = detail
}

// Running marks the step as in progress (spinner on TTY).
func (n *Node) Running() {
	n.state = stateRunning
}

// Render redraws (TTY with color) or emits newly completed lines
// (non-TTY, and TTY with color disabled — no erase codes may ever
// appear when color is off).
func (t *Tree) Render() {
	if t.s.TTY && t.s.Color {
		t.renderTTY()
	} else {
		t.renderPlain()
	}
}

// Finish renders the final tree. On a TTY with color it leaves the
// cursor on a fresh line after the tree; otherwise it emits any
// remaining completed lines. It is safe to call once, after the last
// state change.
func (t *Tree) Finish() {
	t.Render()
	if t.s.TTY && t.s.Color {
		fmt.Fprintln(t.s.W)
	}
}

// renderPlain emits the deterministic sequential non-TTY lines for
// every completed or failed node that has not been emitted yet.
func (t *Tree) renderPlain() {
	for _, n := range t.nodes {
		if n.emitted || (n.state != stateDone && n.state != stateFailed) {
			continue
		}
		n.emitted = true
		fmt.Fprintf(t.s.W, "%s %s\n", TreeBranch, n.label)
		if n.detail == "" {
			continue
		}
		if n.state == stateFailed {
			fmt.Fprintf(t.s.W, "%s%s%s\n", TreeVert, TreeSpace, t.s.Error("failed: "+n.detail))
		} else {
			fmt.Fprintf(t.s.W, "%s%s%s %s\n", TreeVert, TreeSpace, t.s.Success(IconDone), n.detail)
		}
	}
}

// renderTTY redraws the whole tree in place. Only called when TTY and
// color are both enabled, so erase sequences never leak into plain
// output. The redraw writes bypass the margin writer (Raw) and embed
// the container margin itself after each erase — exactly like the
// spinner redraws: carriage-return-prefixed writes are skipped by the
// margin writer, so the in-place lines must carry their own margin.
func (t *Tree) renderTTY() {
	lines := t.ttyLines()
	w := t.s.Raw()
	if t.nodeLines > 0 {
		fmt.Fprintf(w, "\x1b[%dA", t.nodeLines)
	}
	for i, line := range lines {
		if i > 0 {
			fmt.Fprint(w, "\n")
		}
		fmt.Fprintf(w, "\r\x1b[K%s%s", Margin, line)
	}
	if t.nodeLines > len(lines) {
		// The tree shrank (e.g. a running node finished): clear the
		// leftover lines below the new tree.
		for range t.nodeLines - len(lines) {
			fmt.Fprint(w, "\n\x1b[K")
		}
	}
	t.nodeLines = len(lines)
}

// ttyLines renders the current node lines for the in-place redraw. The
// icon and label are wrapped as one colored span so the whole line
// reads as a unit.
func (t *Tree) ttyLines() []string {
	lines := make([]string, 0, len(t.nodes))
	for _, n := range t.nodes {
		switch n.state {
		case stateRunning:
			lines = append(lines, t.s.Progress(SpinnerFrames[0]+" "+n.label))
		case stateDone:
			lines = append(lines, t.s.Success(IconDone+" "+n.label))
			if n.detail != "" {
				lines = append(lines, TreeVert+TreeSpace+t.s.Success(IconDone+" "+n.detail))
			}
		case stateFailed:
			lines = append(lines, t.s.Error(n.label))
			if n.detail != "" {
				lines = append(lines, TreeVert+TreeSpace+t.s.Error("failed: "+n.detail))
			}
		default:
			lines = append(lines, t.s.Dim(n.label))
		}
	}
	return lines
}
