package cmd

import (
	"fmt"
	"sort"
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
//
// When no container is active the board is empty (0 items) — the
// projection is scoped to the active container only. Planned
// containers with queued work are invisible in that empty board, so
// this renderer surfaces the queued planned containers SUMMARY
// (name, plan, items/tickets, status=planned) plus a hint to use
// `eka view board/containers` or `eka transition ctr:<id> active`
// — the exact handoff before activation (sto:execution-view-planned-hint).
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
		// When planned containers exist the empty board hides queued
		// work — surface the queued summary directly.
		fmt.Fprintf(s.W, "%s\n", s.Dim("No active container."))
		if queued := plannedContainers(g); len(queued) > 0 {
			fmt.Fprintln(s.W)
			fmt.Fprintf(s.W, "%s\n", s.Info(fmt.Sprintf("Queued: %d planned container(s) — ready to activate", len(queued))))
			fmt.Fprintln(s.W)
			renderQueuedPlanned(s, g, queued)
			fmt.Fprintln(s.W)
			fmt.Fprintf(s.W, "%s\n", s.Dim("Use `eka view board` or `eka view containers` to inspect queued work, or `eka transition ctr:<id> active` to start execution."))
		}
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
	// When an active container exists the board shows only its work;
	// queued planned containers are still relevant — surface them as
	// an optional hint after the board without breaking the active
	// board (sto:execution-view-planned-hint AC 2).
	if p.Container != nil {
		if queued := plannedContainers(g); len(queued) > 0 {
			fmt.Fprintln(s.W)
			fmt.Fprintf(s.W, "%s\n", s.Info(fmt.Sprintf("Queued: %d planned container(s) — ready to activate", len(queued))))
			fmt.Fprintln(s.W)
			renderQueuedPlanned(s, g, queued)
			fmt.Fprintln(s.W)
			fmt.Fprintf(s.W, "%s\n", s.Dim("Use `eka view board` or `eka view containers` to inspect queued work."))
		}
	}
	renderExecutionInsights(s, p)
}

// plannedContainers returns the planned container details of the graph,
// ordered by created date ascending ("" first) then canonical identity
// — the same order as the containers projection — so queued work
// reads oldest first and stays deterministic.
func plannedContainers(g *view.Graph) []view.ContainerDetail {
	details := g.ContainersDetailed()
	var out []view.ContainerDetail
	for _, d := range details {
		if d.State == "planned" {
			out = append(out, d)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Created != out[j].Created {
			return out[i].Created < out[j].Created
		}
		return out[i].Identity < out[j].Identity
	})
	return out
}

// renderQueuedPlanned renders the queued planned containers summary:
// name, plan, items/tickets, status=planned. Adaptive layout mirrors
// the containers projection — table on wide terminals, stacked cards
// on narrow (CompactLayoutWidth).
func renderQueuedPlanned(s *ui.Style, g *view.Graph, planned []view.ContainerDetail) {
	if s.Width > 0 && s.Width < ui.CompactLayoutWidth {
		renderQueuedPlannedCards(s, g, planned)
		return
	}
	renderQueuedPlannedTable(s, g, planned)
}

func renderQueuedPlannedTable(s *ui.Style, g *view.Graph, planned []view.ContainerDetail) {
	table := ui.NewTable(s, "NAME", "PLAN", "ITEMS/TICKETS", "STATUS")
	for _, c := range planned {
		plan := c.Plan
		if plan == "" {
			plan = "-"
		}
		items := len(g.WorkItemsForContainer(c.Identity))
		tickets := len(g.TicketsForContainer(c.Identity))
		status := containerStateColor(s, c.State)(stateIcon(c.State)) + " " + c.State
		table.AddRow(
			[]string{c.ID, plan, fmt.Sprintf("%d/%d", items, tickets), status},
			[]func(string) string{
				nil,
				func(t string) string {
					if c.Plan == "" {
						return s.Dim(t)
					}
					return t
				},
				nil,
				nil,
			},
		)
	}
	table.Render()
}

func renderQueuedPlannedCards(s *ui.Style, g *view.Graph, planned []view.ContainerDetail) {
	cards := ui.NewCards(s)
	for _, c := range planned {
		plan := c.Plan
		if plan == "" {
			plan = "-"
		}
		items := len(g.WorkItemsForContainer(c.Identity))
		tickets := len(g.TicketsForContainer(c.Identity))
		body := []string{
			"plan: " + plan,
			fmt.Sprintf("items/tickets: %d/%d", items, tickets),
			"status: " + c.State,
		}
		cards.Add(stateIcon(c.State)+" "+c.ID, containerStateColor(s, c.State), body)
	}
	cards.Render()
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
