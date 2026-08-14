package cmd

import (
	"fmt"
	"strings"

	"github.com/maleolabs/eka-cli/cmd/ui"
	"github.com/maleolabs/eka-core/contexts"
)

// This file implements the Context projection renderer: the human
// reading interface of the Context Engine (the default `eka context`
// output). It is a THIN renderer over the Context Object — it never
// builds context itself, never touches the Runtime, and renders a
// pure function of (object, style). Empty sections are omitted
// entirely; when the object carries no relationship sections at all
// (local depth), a calm "(no relationships)" line replaces the empty
// block.

// renderContext renders the Context Object as the human projection:
// the context header (subject, project, depth, domain, stratum,
// state, object hash), the classified sections, the strata landscape,
// the history and the summary. Deterministic plain output on non-TTY
// (the ui.Style handles color automatically); on a TTY the same
// structure carries colors.
func renderContext(s *ui.Style, obj *contexts.Object, projectID string) {
	ui.NewHeader(s, "Context").
		Add("Subject", obj.Focus.CanonicalForm).
		Add("Project", projectID).
		Add("Depth", obj.Depth).
		Add("Domain", obj.Focus.EngineeringDomain).
		Add("Stratum", fmt.Sprintf("%d", obj.Focus.Stratum)).
		Add("State", contextFocusState(&obj.Focus.StateVector)).
		Add("Object Hash", obj.Focus.ObjectHash).
		Pipeline("Context").
		Render()

	if obj.Summary.Sections == 0 {
		// No relationship sections at all (local depth): the calm
		// dim line replaces the empty block — never nothing.
		fmt.Fprintf(s.W, "\n%s\n", s.Dim("(no relationships)"))
	}

	// Constraints: the higher-authority units (strata above the
	// focus), each with its domain and stratum.
	if len(obj.Sections.Constraints) > 0 {
		contextSection(s, "Constrained by")
		for _, e := range obj.Sections.Constraints {
			fmt.Fprintf(s.W, "%s %s  %s\n", ui.IconBullet, e.LineForm,
				s.Dim(fmt.Sprintf("(%s, stratum %d)", e.Domain, e.Stratum)))
		}
	}

	// Dependencies, then downstream, then upstream — the one-hop
	// neighborhood, bullet lines with the role annotation.
	if len(obj.Sections.Dependencies) > 0 {
		contextSection(s, "Depends on")
		for _, e := range obj.Sections.Dependencies {
			contextEntryLine(s, e)
		}
	}
	if len(obj.Sections.Downstream) > 0 {
		contextSection(s, "Referenced by")
		for _, e := range obj.Sections.Downstream {
			contextEntryLine(s, e)
		}
	}
	if len(obj.Sections.Upstream) > 0 {
		contextSection(s, "References")
		for _, e := range obj.Sections.Upstream {
			contextEntryLine(s, e)
		}
	}

	// Decisions, planning and review — the type-token sections.
	if len(obj.Sections.Decisions) > 0 {
		contextSection(s, "Decisions")
		for _, e := range obj.Sections.Decisions {
			contextEntryLine(s, e)
		}
	}
	if len(obj.Sections.Planning) > 0 {
		contextSection(s, "Planning")
		for _, e := range obj.Sections.Planning {
			contextEntryLine(s, e)
		}
	}
	if len(obj.Sections.Review) > 0 {
		contextSection(s, "Review")
		for _, e := range obj.Sections.Review {
			contextEntryLine(s, e)
		}
	}

	// The strata landscape: per-stratum group headings with the
	// bulleted units (ascending stratum — 1 = highest authority).
	if len(obj.Strata) > 0 {
		for _, st := range obj.Strata {
			fmt.Fprintf(s.W, "\n%s\n", s.Accent(fmt.Sprintf("Stratum %d — %s", st.Stratum, st.Domain)))
			for _, e := range st.Units {
				fmt.Fprintf(s.W, "%s %s\n", ui.IconBullet, e.LineForm)
			}
		}
	}

	// History: the focus's instance-line timeline — each instance
	// with its short object hash and the state transitions.
	if len(obj.Sections.History) > 0 {
		contextSection(s, "History")
		for _, h := range obj.Sections.History {
			transitions := make([]string, 0, len(h.ChangeLog))
			for _, c := range h.ChangeLog {
				transitions = append(transitions, fmt.Sprintf("%s: %s -> %s", c.Domain, c.From, c.To))
			}
			line := fmt.Sprintf("%d %s", h.InstanceVersion, shortHash(h.ObjectHash))
			if len(transitions) > 0 {
				line += "  " + s.Dim(strings.Join(transitions, ", "))
			}
			fmt.Fprintf(s.W, "%s %s\n", ui.IconBullet, line)
		}
	}

	ui.NewSummary(s).
		Add("Units", fmt.Sprintf("%d", obj.Summary.Units)).
		Add("Sections", fmt.Sprintf("%d", obj.Summary.Sections)).
		Add("History", fmt.Sprintf("%d", obj.Summary.History)).
		Add("Depth", obj.Depth).
		Add("Next", "eka get "+obj.Focus.CanonicalForm).
		Render()
}

// contextSection renders one section heading of the context
// projection. The heading is separated from the block above it by a
// blank line.
func contextSection(s *ui.Style, title string) {
	fmt.Fprintf(s.W, "\n%s\n", s.Accent(title))
}

// contextEntryLine renders one section entry as a bullet line with
// the role annotation when the entry carries one.
func contextEntryLine(s *ui.Style, e contexts.Entry) {
	line := e.LineForm
	if e.Role != "" {
		line += "  " + s.Dim("("+e.Role+")")
	}
	fmt.Fprintf(s.W, "%s %s\n", ui.IconBullet, line)
}

// contextFocusState renders the primary state of the focus — the
// deterministic priority of the context projection: execution-state,
// container-state, note-state, planning-state, content-state ("" when
// none — the focus unit carries no owned state).
func contextFocusState(v *contexts.StateVector) string {
	switch {
	case v.ExecutionState != "":
		return v.ExecutionState
	case v.ContainerState != "":
		return v.ContainerState
	case v.NoteState != "":
		return v.NoteState
	case v.PlanningState != "":
		return v.PlanningState
	default:
		return v.ContentState
	}
}

// shortHash renders the 8-character prefix of an object hash (the
// stable short reference of the history lines; a hash shorter than 8
// bytes renders whole).
func shortHash(hash string) string {
	if len(hash) <= 8 {
		return hash
	}
	return hash[:8]
}
