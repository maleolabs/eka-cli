package cmd

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/maleolabs/eka-cli/cmd/ui"
	"github.com/maleolabs/eka-core/conformance"
	"github.com/maleolabs/eka-core/view"
)

// This file holds the small shared helpers of the view renderer layer:
// the context header and empty-domain line used by the domain
// projections, the group lookup, the state icons, and the graph
// lookups that complement the projection model (the model carries no
// revision and no relationships — the renderers read those from the
// graph without ever mutating it).

// renderDomainHeader prints the standard context header shared by the
// domain projections (Planning, Architecture, Discovery, Operations).
func renderDomainHeader(s *ui.Style, g *view.Graph, domain string) {
	ui.NewHeader(s, domain).
		Add("Repository", g.Root()).
		Add("Knowledge", "EKA v"+standardVersion).
		Add("Domain", domain).
		Pipeline("View").
		Render()
	fmt.Fprintln(s.W)
}

// renderDomainEmpty prints the calm empty-domain line.
func renderDomainEmpty(s *ui.Style, domain string) {
	fmt.Fprintf(s.W, "%s\n", s.Dim("No "+domain+" artifacts."))
}

// fmtDim prints one dim line.
func fmtDim(s *ui.Style, text string) {
	fmt.Fprintf(s.W, "%s\n", s.Dim(text))
}

// groupByName returns the artifact lines of the named group, or nil
// when the group is absent.
func groupByName(groups []view.Group, name string) []view.DomainArtifact {
	for _, gr := range groups {
		if gr.Name == name {
			return gr.Artifacts
		}
	}
	return nil
}

// artifactStateText renders the compact state part of a domain artifact
// line: "(content-state, planning-state X, phase Y)" in fixed order,
// omitting absent parts. "" when the artifact carries no state values.
func artifactStateText(a view.DomainArtifact) string {
	var parts []string
	if a.HasContentState {
		parts = append(parts, a.ContentState)
	}
	if a.HasPlanningState {
		parts = append(parts, "planning-state "+a.PlanningState)
	}
	if a.HasPhase {
		parts = append(parts, "phase "+a.Phase)
	}
	if len(parts) == 0 {
		return ""
	}
	return "  (" + strings.Join(parts, ", ") + ")"
}

// compactStateText renders the state detail of a domain artifact in
// the narrow-terminal layout: the content state alone, whole — the
// planning-state and phase detail is dropped, never truncated. The
// content state is the leading, most distinguishing word.
func compactStateText(a view.DomainArtifact) string {
	if a.HasContentState {
		return "  (" + a.ContentState + ")"
	}
	return ""
}

// stateWord renders the content-state word of a domain artifact,
// "unknown" when the projection carries none.
func stateWord(a view.DomainArtifact) string {
	if a.HasContentState {
		return a.ContentState
	}
	return "unknown"
}

// contentStateIcon returns the icon of a content-state value: ✓
// accepted/approved, • in review/proposed/superseded, ○ draft. Icons
// decorate; the state word carries the meaning.
func contentStateIcon(state string) string {
	switch state {
	case "draft":
		return "○"
	case "accepted", "approved":
		return ui.IconDone
	default:
		return ui.IconBullet
	}
}

// planningStateIcon returns the icon of a planning-state value: ✓
// approved, ○ draft, • immutable.
func planningStateIcon(state string) string {
	switch state {
	case "approved":
		return ui.IconDone
	case "draft":
		return "○"
	default:
		return ui.IconBullet
	}
}

// revisionOf returns the unit line's revision from the graph (the
// projection model carries no revision), or "" when the line does not
// resolve or carries no revision.
func revisionOf(g *view.Graph, a view.DomainArtifact) string {
	art := g.ByLineForm(a.Identity)
	if art == nil || art.Revision <= 0 {
		return ""
	}
	return strconv.Itoa(art.Revision)
}

// shortRef renders a parsed reference in short form: "<type>:<id>" with
// the instance-version suffix when present, prefixed with the
// namespace when it differs from the referrer's.
func shortRef(ref conformance.Reference, defNamespace string) string {
	s := ref.Type + ":" + ref.ID
	if ref.HasVersion {
		s += ":" + strconv.Itoa(ref.Version)
	}
	if ref.Namespace != "" && ref.Namespace != defNamespace {
		s = ref.Namespace + "/" + s
	}
	return s
}
