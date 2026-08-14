package cmd

import (
	"fmt"
	"strconv"

	"github.com/maleolabs/eka-cli/cmd/ui"
	"github.com/maleolabs/eka-core/view"
)

// This file implements the Discovery projection renderer: information
// cards answering "what are we building?" — one compact boxed card per
// artifact under its group heading. The card header carries the state
// icon + identity (colored by state, drafts dim with ○); the body
// carries the state word and revision. Draft artifacts stay visually
// distinct in every environment.

// renderDiscovery renders the Discovery projection as information
// cards.
func renderDiscovery(s *ui.Style, g *view.Graph, p *view.DiscoveryProjection) {
	renderDomainHeader(s, g, "Discovery")
	if view.GroupTotal(p.Groups) == 0 {
		renderDomainEmpty(s, "Discovery")
		renderDiscoveryInsights(s, p)
		return
	}
	for i, gr := range p.Groups {
		if len(gr.Artifacts) == 0 {
			continue
		}
		fmt.Fprintf(s.W, "%s\n", s.Info(gr.Name))
		cards := ui.NewCards(s)
		for _, a := range gr.Artifacts {
			body := stateWord(a)
			if rev := revisionOf(g, a); rev != "" {
				body += " · revision " + rev
			}
			cards.Add(contentStateIcon(a.ContentState)+" "+a.Identity,
				contentStateColor(s, a.ContentState), []string{body})
		}
		cards.GridWithWidth(s.Width).Render()
		if i < len(p.Groups)-1 {
			fmt.Fprintln(s.W)
		}
	}
	renderDiscoveryInsights(s, p)
}

// renderDiscoveryInsights renders the discovery summary: committed
// direction (approved artifacts) and exploring (drafts).
func renderDiscoveryInsights(s *ui.Style, p *view.DiscoveryProjection) {
	approved, drafts := 0, 0
	for _, gr := range p.Groups {
		for _, a := range gr.Artifacts {
			switch a.ContentState {
			case "approved":
				approved++
			case "draft":
				drafts++
			}
		}
	}
	ui.NewSummary(s).
		Add("Committed direction", strconv.Itoa(approved)).
		Add("Exploring", strconv.Itoa(drafts)).
		Render()
}
