package cmd

import (
	"fmt"

	"github.com/maleolabs/eka-cli/cmd/ui"
	"github.com/maleolabs/eka-core/view"
)

// This file implements the Containers projection renderer: every
// execution container line of the repository — active and completed —
// as an aligned table (name, plan, items/tickets, started/ended,
// status). The CLI layer applies the retrieval filters (--active,
// --container) and the page window before the render; the renderer
// reads the full totals from the untouched projection and renders the
// filter note, the optional pagination footer and the insight line.

// renderContainers renders the Containers projection: the context
// header, the optional filter note, the aligned table, and the footer
// lines. filterNote is the applied retrieval filter ("active only",
// "container <id>", "") — a dim line when set. footer is the
// pre-rendered pagination line ("Page X/Y · containers A–B of T") —
// "" when no page window was applied.
func renderContainers(s *ui.Style, g *view.Graph, p *view.ContainersProjection, filterNote, footer string) {
	ui.NewHeader(s, "Containers").
		Add("Repository", g.Root()).
		Add("Knowledge", "EKA v"+standardVersion).
		Add("Domain", "Execution").
		Pipeline("View").
		Render()
	fmt.Fprintln(s.W)
	if filterNote != "" {
		fmt.Fprintf(s.W, "%s\n", s.Dim(filterNote))
		fmt.Fprintln(s.W)
	}
	if p.Total == 0 {
		// Empty projection: a calm line, still exit 0.
		fmt.Fprintf(s.W, "%s\n", s.Dim("No containers."))
		return
	}
	// Adaptive layout: on a narrow terminal the aligned table gives
	// way to a stacked card list — the same information, restructured
	// instead of truncated. Non-TTY output (width 0) keeps the table.
	if s.Width > 0 && s.Width < ui.CompactLayoutWidth {
		renderContainersCards(s, p)
	} else {
		renderContainersTable(s, p)
	}
	fmt.Fprintln(s.W)
	if footer != "" {
		fmt.Fprintf(s.W, "%s\n", s.Dim(footer))
	}
	// The insight line: population and active containers.
	fmt.Fprintf(s.W, "%s\n", s.Dim(plural(p.Total, "container", "containers")+
		" · "+plural(p.Active, "active", "active")))
}

// renderContainersCards renders the containers projection as a
// stacked card list — the narrow-terminal layout of the containers
// table. Every container becomes one boxed card: the header carries
// the state icon + id in the state color, the body carries the plan,
// the items/tickets counts and the started/ended dates. No
// information is dropped or truncated — the structure changes, the
// data stays complete.
func renderContainersCards(s *ui.Style, p *view.ContainersProjection) {
	cards := ui.NewCards(s)
	for _, c := range p.Containers {
		plan := c.Plan
		started := c.StartedAt
		ended := c.EndedAt
		if plan == "" {
			plan = "-"
		}
		if started == "" {
			started = "-"
		}
		if ended == "" {
			ended = "-"
		}
		body := []string{
			"plan: " + plan,
			fmt.Sprintf("items/tickets: %d/%d", c.Items, c.Tickets),
			"started: " + started,
			"ended: " + ended,
		}
		// The header is passed PLAIN with its color function separate —
		// coloring it before Add would make displayWidth count the ANSI
		// escapes and blow the card box (leaking header).
		cards.Add(stateIcon(c.State)+" "+c.ID,
			containerStateColor(s, c.State), body)
	}
	cards.Render()
}

// renderContainersTable renders the containers table: NAME | PLAN |
// ITEMS/TICKETS | STARTED | ENDED | STATUS.
func renderContainersTable(s *ui.Style, p *view.ContainersProjection) {
	table := ui.NewTable(s, "NAME", "PLAN", "ITEMS/TICKETS", "STARTED", "ENDED", "STATUS")
	for _, c := range p.Containers {
		plan := c.Plan
		started := c.StartedAt
		ended := c.EndedAt
		if plan == "" {
			plan = "-"
		}
		if started == "" {
			started = "-"
		}
		if ended == "" {
			ended = "-"
		}
		status := containerStateColor(s, c.State)(stateIcon(c.State)) + " " + c.State
		table.AddRow(
			[]string{c.ID, plan, fmt.Sprintf("%d/%d", c.Items, c.Tickets), started, ended, status},
			[]func(string) string{
				nil,
				func(t string) string {
					if c.Plan == "" {
						return s.Dim(t)
					}
					return t
				},
				nil,
				func(t string) string {
					if c.StartedAt == "" {
						return s.Dim(t)
					}
					return t
				},
				func(t string) string {
					if c.EndedAt == "" {
						return s.Dim(t)
					}
					return t
				},
				nil,
			},
		)
	}
	table.Render()
}

// containerStateColor returns the presentation color of a container
// state value: active info, completed success, planned dim (born
// planned, waiting for activation), everything else dim.
func containerStateColor(s *ui.Style, state string) func(string) string {
	switch state {
	case "active":
		return s.Info
	case "completed":
		return s.Success
	case "planned":
		return s.Dim
	default:
		return s.Dim
	}
}
