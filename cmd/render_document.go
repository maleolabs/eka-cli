package cmd

import (
	"fmt"
	"strings"

	"github.com/maleolabs/eka-cli/cmd/ui"
	"github.com/maleolabs/eka-core/view"
)

// This file implements the Document projection renderer: the detail
// view of ONE canonical document of any type. Rendering is type-aware
// (show-ish, theme-consistent):
//
//   - Top-level documents (Discovery: vis-/str-/req-/fnd-, Architecture:
//     adr-/dec-/arc-/spec-/std-/gls-) render in the LICENSE-TEXT style:
//     a formal uppercase heading, a dim canonical identity line, a
//     rule, and the content sections as uppercase-titled indented
//     paragraphs — the professional document look of a license block.
//   - Work items (sto-/ts-/bug-/td-/ch-/spk-/tkt-) render as the
//     execution card: the state-colored status line first (the ticket
//     card precedent), then the content sections.
//   - Planning and Operations documents use the license style with
//     their classification rows (dimension/domain/phase).

// renderDocument dispatches on the document class.
func renderDocument(s *ui.Style, d *view.Document) {
	if d.IsWorkItem {
		renderDocumentWorkItem(s, d)
		return
	}
	renderDocumentLicense(s, d)
}

// licenseRule is the fixed rule width of the license-style block.
const licenseRule = 64

// renderDocumentLicense renders a top-level (or planning/operations)
// document in the license-text style: uppercase heading, dim identity
// line, rule, classification rows, then the content sections as
// uppercase-titled indented paragraphs.
func renderDocumentLicense(s *ui.Style, d *view.Document) {
	ui.NewHeader(s, strings.ToUpper(d.Type)+" · "+strings.ToUpper(d.ID)).
		Add("Identity", d.Identity).
		Add("Repository", ".").
		Add("Knowledge", "EKA v"+standardVersion).
		Pipeline("View").
		Render()

	// The canonical identity line, dimmed — the document's formal
	// byline, with the issue number when the line carries one.
	meta := d.Identity
	if d.Number > 0 {
		meta = meta + "  " + fmt.Sprintf("#%d", d.Number)
	}
	if len(d.States) > 0 {
		meta += " · " + strings.Join(d.States, " · ")
	}
	fmt.Fprintf(s.W, "\n%s\n", s.Dim(meta))
	fmt.Fprintln(s.W, s.Dim(strings.Repeat("─", licenseRule)))

	// Classification rows (license preamble style).
	var rows [][2]string
	if d.Dimension != "" {
		rows = append(rows, [2]string{"Dimension", d.Dimension})
	}
	if d.Domain != "" {
		rows = append(rows, [2]string{"Domain", d.Domain})
	}
	if d.Phase != "" {
		rows = append(rows, [2]string{"Phase", d.Phase})
	}
	if len(rows) > 0 {
		width := 0
		for _, r := range rows {
			if len(r[0]) > width {
				width = len(r[0])
			}
		}
		for _, r := range rows {
			fmt.Fprintf(s.W, "%s\n", s.Dim(fmt.Sprintf("%-*s   %s", width, r[0], r[1])))
		}
	}

	// The content sections: uppercase section titles, indented
	// paragraphs — the license block body.
	for _, sec := range d.Content {
		fmt.Fprintf(s.W, "\n%s\n", s.Accent(strings.ToUpper(sec.Key)))
		for _, line := range strings.Split(sec.Value, "\n") {
			fmt.Fprintf(s.W, "  %s\n", line)
		}
	}

	if len(d.Relationships) > 0 {
		fmt.Fprintf(s.W, "\n%s\n", s.Dim(strings.Join(d.Relationships, " · ")))
	}

	ui.NewSummary(s).
		Add("Identity", d.Identity).
		Add("Instance", fmt.Sprintf("v%d", d.InstanceVersion)).
		Add("Type", d.Type).
		Add("State", primaryStateLabel(s, d)).
		Render()
}

// renderDocumentWorkItem renders a work item as the execution card:
// the state-colored status line first (the ticket card precedent),
// then the content sections.
func renderDocumentWorkItem(s *ui.Style, d *view.Document) {
	identity := d.Identity
	if d.Number > 0 {
		identity = identity + "  " + s.Accent(fmt.Sprintf("#%d", d.Number))
	}
	ui.NewHeader(s, "Work Item").
		Add("Item", identity).
		Add("Repository", ".").
		Add("Knowledge", "EKA v"+standardVersion).
		Add("Domain", "Execution").
		Pipeline("View").
		Render()

	status := "unresolved"
	if d.HasState {
		status = d.State
	}
	fmt.Fprintf(s.W, "\n%s  %s\n", s.Accent("Status"),
		stateColor(s, status)(stateIcon(status)+" "+status))

	var rows [][2]string
	if d.Dimension != "" {
		rows = append(rows, [2]string{"Dimension", d.Dimension})
	}
	for _, rel := range d.Relationships {
		rows = append(rows, [2]string{strings.ToUpper(strings.SplitN(rel, " ", 2)[0]), rel})
	}
	if len(rows) > 0 {
		width := 0
		for _, r := range rows {
			if len(r[0]) > width {
				width = len(r[0])
			}
		}
		body := make([]string, 0, len(rows))
		for _, r := range rows {
			body = append(body, fmt.Sprintf("%-*s   %s", width, r[0], r[1]))
		}
		ui.NewCards(s).Add(d.Identity, stateColor(s, status), body).Render()
	}

	for _, sec := range d.Content {
		fmt.Fprintf(s.W, "\n%s\n", s.Accent(strings.ToUpper(sec.Key)))
		for _, line := range strings.Split(sec.Value, "\n") {
			fmt.Fprintf(s.W, "  %s\n", line)
		}
	}

	ui.NewSummary(s).
		Add("Identity", d.Identity).
		Add("Instance", fmt.Sprintf("v%d", d.InstanceVersion)).
		Add("Type", d.Type).
		Add("Status", primaryStateLabel(s, d)).
		Render()
}

// primaryStateLabel renders the primary state with its color, or a
// dash when the document owns no state.
func primaryStateLabel(s *ui.Style, d *view.Document) string {
	if !d.HasState {
		return "—"
	}
	return stateColor(s, d.State)(d.State)
}
