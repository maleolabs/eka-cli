package cmd

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/maleolabs/eka-cli/cmd/ui"
	"github.com/maleolabs/eka-core/view"
)

// This file implements the Planning projection renderer: the roadmap
// answering "what are we planning next?" — every plan instance as a
// milestone row at the same root level, with the scope definitions,
// epics and sub-plans deriving from it as branch rows beneath it
// (tree branches ├─ / └─), scope/epic lines without a plan as orphan
// rows, traceability as a footer note, and the insight summary.

// renderPlanning renders the Planning projection as a roadmap tree:
// plan milestone rows first, all at the same level (ordered by
// created date, oldest first), each followed by its scope/epic
// children and sub-plans as indented branches; then the orphan
// scope/epic lines (no plan); then the separator and the
// traceability footer.
func renderPlanning(s *ui.Style, g *view.Graph, p *view.PlanningProjection) {
	renderDomainHeader(s, g, "Planning")
	if p.Total() == 0 {
		renderDomainEmpty(s, "Planning")
		renderPlanningInsights(s, p)
		return
	}
	if len(p.Plans) == 0 {
		// No committed plan: the roadmap has no milestone yet.
		fmt.Fprintf(s.W, "%s\n", s.Dim("○ no plan yet — roadmap undefined"))
	} else {
		for _, group := range p.Plans {
			// Milestone row: every plan shares the same root level.
			fmt.Fprintf(s.W, "%s\n",
				planningStateColor(s, group.Plan.PlanningState)(
					planningStateIcon(group.Plan.PlanningState)+" "+group.Plan.Identity+planningArtifactState(s, group.Plan)))
			renderPlanBranches(s, group, "")
		}
	}
	// Orphan scp-/epc- lines (no plan derives them) follow every plan
	// group, before the traceability separator.
	for _, a := range p.Orphans {
		fmt.Fprintf(s.W, "%s\n",
			contentStateColor(s, a.ContentState)("▸ "+a.Identity+planningArtifactState(s, a)))
	}
	fmt.Fprintf(s.W, "%s\n", s.Dim(strings.Repeat("─", 24)))
	for _, tr := range p.Traceability {
		// Traceability is a Planning artifact like any other: it gets a
		// normal row (connected to the structure), with the
		// "traceability:" label for context.
		fmt.Fprintf(s.W, "%s\n",
			contentStateColor(s, tr.ContentState)("▸ traceability: "+tr.Identity+" ("+tr.ContentState+")"))
	}
	renderPlanningInsights(s, p)
}

// planningArtifactState renders the state detail of a planning row:
// the full state text on a wide terminal (or non-TTY), the compact
// content-state-only form on a narrow terminal — detail dropped
// whole, never truncated (adaptive layout).
func planningArtifactState(s *ui.Style, a view.DomainArtifact) string {
	if s.Width > 0 && s.Width < ui.CompactLayoutWidth {
		return compactStateText(a)
	}
	return artifactStateText(a)
}

// renderPlanBranches renders the branch rows under one plan: the
// scope/epic children and the sub-plans, each with a tree branch
// (├─ for every row but the last, └─ for the last). Sub-plan rows
// use the planning-state presentation; their own scope/epic children
// nest one level deeper, under the sub-plan's rail (│  — a trailing
// root branch) or indent (when the sub-plan is the last row).
// railPrefix continues the parent branch for nested grandchildren;
// it is "" for root plans and "│  "/"   " for sub-plan children.
func renderPlanBranches(s *ui.Style, group view.PlanGroup, railPrefix string) {
	branches := len(group.Children) + len(group.SubPlans)
	idx := 0
	for _, child := range group.Children {
		branch := "├─ "
		if idx == branches-1 {
			branch = "└─ "
		}
		idx++
		fmt.Fprintf(s.W, "%s%s\n", s.Dim(branch),
			contentStateColor(s, child.ContentState)("▸ "+child.Identity+planningArtifactState(s, child)))
	}
	for _, sub := range group.SubPlans {
		branch := "├─ "
		last := idx == branches-1
		if last {
			branch = "└─ "
		}
		idx++
		fmt.Fprintf(s.W, "%s%s\n", s.Dim(branch),
			planningStateColor(s, sub.Plan.PlanningState)(
				planningStateIcon(sub.Plan.PlanningState)+" "+sub.Plan.Identity+planningArtifactState(s, sub.Plan)))
		// The sub-plan's own children nest one level deeper.
		rail := "│  "
		if last {
			rail = "   "
		}
		for i, gc := range sub.Children {
			gbranch := "├─ "
			if i == len(sub.Children)-1 {
				gbranch = "└─ "
			}
			fmt.Fprintf(s.W, "%s%s\n", s.Dim(rail+gbranch),
				contentStateColor(s, gc.ContentState)("▸ "+gc.Identity+planningArtifactState(s, gc)))
		}
	}
}

// renderPlanningInsights renders the planning summary: committed
// (approved plans), exploring (draft epics), and the next milestone
// (the phase of the first approved plan in created-date order, "—"
// when none).
func renderPlanningInsights(s *ui.Style, p *view.PlanningProjection) {
	approved := 0
	for _, sc := range p.PlansByState {
		if sc.State == "approved" {
			approved = sc.Count
		}
	}
	draftEpics := 0
	for _, group := range p.Plans {
		for _, child := range group.Children {
			if child.Type == "epc" && child.ContentState == "draft" {
				draftEpics++
			}
		}
		for _, sub := range group.SubPlans {
			for _, child := range sub.Children {
				if child.Type == "epc" && child.ContentState == "draft" {
					draftEpics++
				}
			}
		}
	}
	for _, a := range p.Orphans {
		if a.Type == "epc" && a.ContentState == "draft" {
			draftEpics++
		}
	}
	next := "—"
	for _, group := range p.Plans {
		plan := group.Plan
		if plan.PlanningState != "approved" {
			continue
		}
		if plan.HasPhase {
			next = plan.Phase
		}
		break
	}
	ui.NewSummary(s).
		Add("Committed", strconv.Itoa(approved)).
		Add("Exploring", strconv.Itoa(draftEpics)).
		Add("Next milestone", next).
		Render()
}
