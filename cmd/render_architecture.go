package cmd

import (
	"strconv"

	"github.com/maleolabs/eka-cli/cmd/ui"
	"github.com/maleolabs/eka-core/conformance"
	"github.com/maleolabs/eka-core/view"
)

// This file implements the Architecture projection renderer: the
// dependency tree answering "how is the system structured?" — rooted
// at the architecture description (arc-), with decisions,
// specifications, standards and vocabulary as subtrees and
// derives-from/depends-on edge annotations where the graph resolves
// them.

// renderArchitecture renders the Architecture projection as a
// dependency tree.
func renderArchitecture(s *ui.Style, g *view.Graph, p *view.ArchitectureProjection) {
	renderDomainHeader(s, g, "Architecture")
	if view.GroupTotal(p.Groups) == 0 {
		renderDomainEmpty(s, "Architecture")
		renderArchitectureInsights(s, p)
		return
	}
	descriptions := groupByName(p.Groups, "Architecture Descriptions")
	tree := ui.NewDependencyTree(s, "Architecture", nil)
	if len(descriptions) > 0 {
		// Root the tree at the architecture description when one
		// exists; otherwise the root is a plain "Architecture" node.
		arc := descriptions[0]
		tree = ui.NewDependencyTree(s, arc.Identity+artifactStateText(arc),
			contentStateColor(s, arc.ContentState))
	}
	for _, gr := range p.Groups {
		if gr.Name == "Architecture Descriptions" || len(gr.Artifacts) == 0 {
			continue
		}
		sub := tree.Add("", gr.Name, s.Info)
		for _, a := range gr.Artifacts {
			// Adaptive layout: on a narrow terminal the edge
			// annotation (derives-from/depends-on) is dropped whole —
			// the tree structure stays intact, the detail yields.
			// Non-TTY output (width 0) keeps the annotations.
			edge := ""
			if s.Width == 0 || s.Width >= ui.CompactLayoutWidth {
				edge = architectureEdge(g, a)
			}
			sub.Add(contentStateIcon(a.ContentState), a.Identity+artifactStateText(a),
				contentStateColor(s, a.ContentState)).Edge(edge)
		}
	}
	tree.Render()
	renderArchitectureInsights(s, p)
}

// architectureEdge returns the first resolvable derives-from (else
// depends-on) reference of an architecture unit in short form,
// e.g. "derives-from arc:feather-system"; "" when nothing resolves.
// The projection model carries no relationships, so the edge is read
// from the graph — read-only, never mutated.
func architectureEdge(g *view.Graph, a view.DomainArtifact) string {
	art := g.ByLineForm(a.Identity)
	if art == nil {
		return ""
	}
	for _, field := range []string{"derives-from", "depends-on"} {
		for _, raw := range g.Rels(art, field) {
			ref, err := conformance.ParseReference(raw, art.Identity.Namespace, art.Identity.Type)
			if err != nil || g.Resolve(ref) == nil {
				continue
			}
			return field + " " + shortRef(ref, art.Identity.Namespace)
		}
	}
	return ""
}

// renderArchitectureInsights renders the architecture summary:
// accepted decisions, open items (proposed/review) and superseded
// decisions.
func renderArchitectureInsights(s *ui.Style, p *view.ArchitectureProjection) {
	accepted, open, superseded := 0, 0, 0
	for _, a := range groupByName(p.Groups, "Decisions") {
		switch a.ContentState {
		case "accepted":
			accepted++
		case "proposed", "review":
			open++
		case "superseded":
			superseded++
		}
	}
	ui.NewSummary(s).
		Add("Accepted decisions", strconv.Itoa(accepted)).
		Add("Open items", strconv.Itoa(open)).
		Add("Superseded", strconv.Itoa(superseded)).
		Render()
}
