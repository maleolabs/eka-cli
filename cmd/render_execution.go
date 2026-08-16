package cmd

import (
	"fmt"
	"strconv"

	"github.com/maleolabs/eka-cli/cmd/ui"
	"github.com/maleolabs/eka-core/view"
)

// This file implements the Execution projection renderer: the active
// execution container as a kanban board — the primary question "what
// is currently being worked on?" answered in one glance. The board is
// the visual focal point; tickets are deliberately not rendered as a
// list (they duplicate the board state — tickets project the work
// items) and become one dim footer line.

// boardTitles maps the execution-state values to their kanban column
// titles. The board order is the fixed StateColumns order.
var boardTitles = map[string]string{
	"planned":     "Planned",
	"todo":        "Todo",
	"in-progress": "In Progress",
	"in-review":   "In Review",
	"done":        "Done",
	"canceled":    "Canceled",
}

// shortWorkItemID builds the short-id renderer for a work item set:
// the bare id ("draft-autosave"), or the "<type>:<id>" form when the
// id is ambiguous across work item types.
func shortWorkItemID(items []view.WorkItem) func(view.WorkItem) string {
	counts := make(map[string]int, len(items))
	for _, wi := range items {
		counts[wi.ID]++
	}
	return func(wi view.WorkItem) string {
		if counts[wi.ID] > 1 {
			return wi.Type + ":" + wi.ID
		}
		return wi.ID
	}
}

// boardTitle returns the display title of an execution-state column,
// or the raw state for values without a title (impossible behind the
// validation gate).
func boardTitle(state string) string {
	if t, ok := boardTitles[state]; ok {
		return t
	}
	return state
}

// renderExecution renders the Execution projection: the context
// header, the container line (with the multiple-active warning when
// the repository is in that invalid state), the six-column kanban
// board (canceled added, ADR-019), one footer line tying the board
// to its tickets (or the no-tickets warning when the active
// container has no tkt- membership), and the insight summary.
func renderExecution(s *ui.Style, g *view.Graph, p *view.ExecutionProjection) {
	container := "none"
	if p.Container != nil {
		container = p.Container.Identity
	}
	ui.NewHeader(s, "Execution").
		Add("Container", container).
		Add("Repository", g.Root()).
		Add("Knowledge", "EKA v"+standardVersion).
		Add("Domain", "Execution").
		Pipeline("View").
		Render()
	fmt.Fprintln(s.W)
	if p.MultipleActive {
		fmt.Fprintf(s.W, "%s\n", s.Warning("Multiple active containers — showing "+p.Container.Identity))
	}
	if p.Container == nil {
		// Empty projection: a calm line, still exit 0 with the summary.
		fmt.Fprintf(s.W, "%s\n", s.Dim("No active container."))
	} else {
		fmt.Fprintf(s.W, "%s\n", stateMark(s, p.Container.State)+" "+p.Container.Identity+
			"  "+s.Dim("("+p.Container.State+")"))
	}
	fmt.Fprintln(s.W)
	renderBoard(s, g, p.Columns)
	if p.Container != nil && len(p.Tickets) > 0 {
		fmt.Fprintln(s.W)
		fmt.Fprintf(s.W, "%s\n", s.Dim(plural(len(p.Tickets), "ticket", "tickets")+
			" project these work items"))
	} else if p.Container != nil {
		// Active container without tkt- membership: the work items are
		// invisible on this board (no ticket derives from the
		// container) — the same warning surface as the board's
		// unassigned line.
		fmt.Fprintln(s.W)
		fmt.Fprintf(s.W, "%s\n", s.Warning("Container has no tickets — work items are not linked to this container (create tkt- tickets deriving from it)"))
	}
	renderExecutionInsights(s, p)
}

// renderBoard renders the work board: the fixed six execution-state
// columns (canceled added, ADR-019; always the full set; empty
// columns show "—") with the short
// ids of their work items as cell labels, each tagged with its
// container context — the same tag rule as the board projection, so an
// item shared across containers is visible from the active container's
// board too.
func renderBoard(s *ui.Style, g *view.Graph, cols view.StateColumns) {
	board := ui.NewBoard(s)
	var all []view.WorkItem
	for _, col := range cols {
		all = append(all, col.WorkItems...)
	}
	short := shortWorkItemID(all)
	forms := make([]string, len(all))
	for i, wi := range all {
		forms[i] = wi.Identity
	}
	tag := containerTagRenderer(forms, g.ContainersForWorkItem)
	budget := ui.BoardItemBudget(s.Width, len(cols))
	for _, col := range cols {
		labels := make([]ui.Card, 0, len(col.WorkItems))
		for _, wi := range col.WorkItems {
			// The execution board keeps its container-tag layout (the
			// assignee tag belongs to the board projection, ADR-029).
			labels = append(labels, boardCard(short(wi), g.NumberLabel(wi.Identity), wi.Type, tag(wi.Identity), "", budget,
				stateColor(s, col.State), typeBadgeColor(s, wi.Type), s.Accent, wi.NotesCount))
		}
		board.AddCards(boardTitle(col.State), stateColor(s, col.State), labels)
	}
	board.Render()
}

// renderExecutionInsights renders the execution summary with meaningful
// insights instead of raw per-state counts: active work (in progress +
// in review), completed work, the review queue, and overall progress
// (bar + done/total with percent).
func renderExecutionInsights(s *ui.Style, p *view.ExecutionProjection) {
	inProgress := p.Columns.Count("in-progress")
	inReview := p.Columns.Count("in-review")
	done := p.Columns.Count("done")
	percent := "0%"
	if p.Total > 0 {
		percent = strconv.Itoa(done*100/p.Total) + "%"
	}
	progress := ui.ProgressBar(s, done, p.Total) + " " + fmt.Sprintf("%d/%d (%s)", done, p.Total, percent)
	ui.NewSummary(s).
		Add("Active Work", strconv.Itoa(inProgress+inReview)).
		Add("Completed Work", strconv.Itoa(done)).
		Add("Review Queue", strconv.Itoa(inReview)).
		Add("Overall Progress", progress).
		Render()
}
