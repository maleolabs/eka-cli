package cmd

import (
	"fmt"
	"strconv"

	"github.com/maleolabs/eka-cli/cmd/ui"
	"github.com/maleolabs/eka-core/conformance"
	"github.com/maleolabs/eka-core/view"
)

// This file implements the Operations projection renderer: the release
// summary answering "what has been delivered?" — one "Delivered" card
// per release record (with its derived-from plan) as the centerpiece,
// below it an activity timeline of the operations knowledge (run-
// items), and the insight summary.

// renderOperations renders the Operations projection as a release
// summary with an activity timeline.
func renderOperations(s *ui.Style, g *view.Graph, p *view.OperationsProjection) {
	renderDomainHeader(s, g, "Operations")
	if view.GroupTotal(p.Groups) == 0 {
		renderDomainEmpty(s, "Operations")
		renderOperationsInsights(s, p)
		return
	}
	if releases := groupByName(p.Groups, "Release Records"); len(releases) > 0 {
		fmt.Fprintf(s.W, "%s\n", s.Info("Release Records"))
		cards := ui.NewCards(s)
		for _, rel := range releases {
			body := []string{stateWord(rel)}
			if plan := releasePlan(g, rel); plan != "" {
				body = append(body, "derives-from "+plan)
			}
			cards.Add(contentStateIcon(rel.ContentState)+" "+rel.Identity,
				contentStateColor(s, rel.ContentState), body)
		}
		cards.GridWithWidth(s.Width).Render()
		fmt.Fprintln(s.W)
	}
	if runbooks := groupByName(p.Groups, "Runbooks"); len(runbooks) > 0 {
		fmt.Fprintf(s.W, "%s\n", s.Info("Runbooks"))
		tl := ui.NewTimeline(s)
		for _, run := range runbooks {
			tl.Add("▸", run.Identity+artifactStateText(run), contentStateColor(s, run.ContentState))
		}
		tl.Render()
	}
	renderOperationsInsights(s, p)
}

// releasePlan returns the first resolvable plan reference of a release
// record in short form ("plan:roadmap-v1", appending the instance
// version when the target is not the line's highest instance), or ""
// when none. The projection model carries no relationships, so the
// reference is read from the graph — read-only, never mutated.
func releasePlan(g *view.Graph, a view.DomainArtifact) string {
	art := g.ByLineForm(a.Identity)
	if art == nil {
		return ""
	}
	for _, raw := range g.Rels(art, "derives-from") {
		ref, err := conformance.ParseReference(raw, art.Identity.Namespace, art.Identity.Type)
		if err != nil || ref.Type != "plan" || g.Resolve(ref) == nil {
			continue
		}
		return shortRef(ref, art.Identity.Namespace)
	}
	return ""
}

// renderOperationsInsights renders the operations summary: releases
// delivered (approved release records) and runbooks maintained.
func renderOperationsInsights(s *ui.Style, p *view.OperationsProjection) {
	delivered := 0
	for _, a := range groupByName(p.Groups, "Release Records") {
		if a.ContentState == "approved" {
			delivered++
		}
	}
	runbooks := len(groupByName(p.Groups, "Runbooks"))
	ui.NewSummary(s).
		Add("Releases delivered", strconv.Itoa(delivered)).
		Add("Runbooks maintained", strconv.Itoa(runbooks)).
		Render()
}
