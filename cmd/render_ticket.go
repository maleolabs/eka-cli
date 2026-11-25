package cmd

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/maleolabs/eka-cli/cmd/ui"
	"github.com/maleolabs/eka-core/exchange"
	"github.com/maleolabs/eka-core/view"
)

// This file implements the Ticket projection renderer: the ticket
// detail card. The projected status is the first focal point (colored,
// with its state icon); the ticket card then carries the work item,
// the container and the derives-from references as supporting rows.

// renderTicket renders one ticket projection as a detail card.
func renderTicket(s *ui.Style, g *view.Graph, p *view.TicketProjection, withNotes bool) {
	identity := p.Ticket.Identity
	if label := g.NumberLabel(p.Ticket.Identity); label != "" {
		identity = identity + "  " + s.Accent(label)
	}
	ui.NewHeader(s, "Ticket").
		Add("Ticket", identity).
		Add("Repository", g.Root()).
		Add("Knowledge", "EKA v"+standardVersion).
		Add("Domain", "Execution").
		Pipeline("View").
		Render()

	// The projected status leads: icon + state word as one colored
	// span, the first thing the eye lands on.
	fmt.Fprintf(s.W, "\n%s  %s\n", s.Accent("Projected Status"),
		stateColor(s, p.Projected)(stateIcon(p.Projected)+" "+p.Projected))

	workItem := "unresolved"
	if p.WorkItem != nil {
		workItem = p.WorkItem.Identity + " (" + p.WorkItem.State + ")"
	}
	container := "unresolved"
	if p.Container != nil {
		container = p.Container.Identity
	}
	derives := "—"
	if len(p.References) > 0 {
		derives = strings.Join(p.References, ", ")
	}
	rows := [][2]string{
		{"Work Item", workItem},
		{"Container", container},
		{"Derives From", derives},
	}
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
	ui.NewCards(s).
		Add(p.Ticket.Identity, stateColor(s, p.Projected), body).
		Render()

	workItemValue := "unresolved"
	if p.WorkItem != nil {
		workItemValue = p.WorkItem.Identity + " (" + p.WorkItem.State + ")"
	}
	ui.NewSummary(s).
		Add("Projected status", p.Projected).
		Add("Work item", workItemValue).
		Render()

	if withNotes {
		renderTicketNotes(s, p)
	}
}

// renderTicketNotes renders the notes (comments) section of the ticket
// projection (--with-note / --with-comments): one card per cmt- note
// discussing the ticket or its work item, with the note identity, its
// role badge, the note-state mark, the authoring metadata and the
// per-role content fields — professional comment UI, deterministic
// order (canonical identity).
func renderTicketNotes(s *ui.Style, p *view.TicketProjection) {
	// Section heading, clearly separated from the comment list: the
	// heading, the count line and a blank line frame the list.
	fmt.Fprintf(s.W, "\n%s\n", s.Accent("Notes"))
	fmt.Fprintf(s.W, "%s\n\n", s.Dim(fmt.Sprintf(
		"%d note(s) discussing %s%s",
		len(p.Notes), p.Ticket.Identity,
		func() string {
			if p.WorkItem != nil && p.WorkItem.Identity != p.Ticket.Identity {
				return " and its work item " + p.WorkItem.Identity
			}
			return ""
		}())))
	if len(p.Notes) == 0 {
		fmt.Fprintf(s.W, "  %s\n", s.Dim("no notes yet — 'eka note <target> --role <role>' to add one"))
		return
	}
	// Per-reviewer grouping for review trail (phase2-cli-render): group review notes by author,
	// render each reviewer's trail together with verdict badge and note-state mark.
	// Non-review notes and reviews without author fall back to flat order, but per-reviewer
	// groups are emitted first to satisfy the per-reviewer trail requirement.
	hasReview := false
	for _, n := range p.Notes {
		c := map[string]any{}
		if n.Note.Content.Representation == exchange.StructuredJSON {
			_ = json.Unmarshal(n.Note.ContentPayload, &c)
		}
		if role, _ := c["role"].(string); role == "review" {
			hasReview = true
			break
		}
	}
	if hasReview {
		// Group review notes by author name (author identity), preserving canonical order within group.
		type group struct {
			author string
			notes  []view.TicketNote
		}
		groups := []group{}
		index := map[string]int{}
		var others []view.TicketNote
		for _, n := range p.Notes {
			c := map[string]any{}
			if n.Note.Content.Representation == exchange.StructuredJSON {
				_ = json.Unmarshal(n.Note.ContentPayload, &c)
			}
			role, _ := c["role"].(string)
			if role != "review" {
				others = append(others, n)
				continue
			}
			author := n.Note.Author.Name
			if author == "" {
				author = "(unknown)"
			}
			if idx, ok := index[author]; ok {
				groups[idx].notes = append(groups[idx].notes, n)
			} else {
				index[author] = len(groups)
				groups = append(groups, group{author: author, notes: []view.TicketNote{n}})
			}
		}
		// Render per-reviewer groups
		for _, g := range groups {
			// Verdict summary per reviewer: list verdicts present
			verdicts := []string{}
			for _, n := range g.notes {
				c := map[string]any{}
				if n.Note.Content.Representation == exchange.StructuredJSON {
					_ = json.Unmarshal(n.Note.ContentPayload, &c)
				}
				if v, _ := c["verdict"].(string); v != "" {
					verdicts = append(verdicts, v)
				}
			}
			summary := ""
			if len(verdicts) > 0 {
				summary = " — " + strings.Join(verdicts, ", ")
			}
			fmt.Fprintf(s.W, "%s  %s%s\n", s.Accent("Reviewer"), s.Accent(g.author), s.Dim(summary))
			for _, n := range g.notes {
				renderTicketNote(s, n.Note)
				for _, reply := range n.Replies {
					renderTicketReply(s, reply)
				}
			}
			fmt.Fprintln(s.W)
		}
		// Render non-review notes flat
		for _, n := range others {
			renderTicketNote(s, n.Note)
			for _, reply := range n.Replies {
				renderTicketReply(s, reply)
			}
		}
		return
	}
	for _, n := range p.Notes {
		renderTicketNote(s, n.Note)
		for _, reply := range n.Replies {
			renderTicketReply(s, reply)
		}
	}
}

// renderTicketNote renders one cmt- note as a comment card: the note
// line, its role tag and note-state mark on the headline, the
// authoring metadata (by, created) and the per-role content fields
// (implementation: summary + changes/tests; review: verdict + notes;
// fix: detail + addresses). Content is decoded from the unit's
// structured-json payload; undecodable payloads degrade gracefully.
func renderTicketNote(s *ui.Style, n *exchange.Unit) {
	content := map[string]any{}
	if n.Content.Representation == exchange.StructuredJSON {
		if err := json.Unmarshal(n.ContentPayload, &content); err != nil {
			content = map[string]any{}
		}
	}
	role, _ := content["role"].(string)
	if role == "" {
		role = string(n.Identity.Type) // cmt-; role absent degrades to the type
	}

	headline := fmt.Sprintf("%s  %s  %s",
		LineFormShown(n),
		noteRoleTag(s, role),
		noteStateMark(s, n.StateVector.NoteState))
	fmt.Fprintln(s.W, headline)

	meta := "cmt:" + n.Identity.ID
	if n.Author.Name != "" {
		meta = meta + " · " + authorLabel(s, n.Author)
	}
	if n.Created != "" {
		meta = meta + " · " + n.Created
	}
	if d := n.Classification.Domain; d != "" {
		meta = meta + " · " + s.Dim(d)
	}
	fmt.Fprintf(s.W, "  %s\n", s.Dim(meta))

	for _, line := range noteContentLines(s, content) {
		fmt.Fprintf(s.W, "  %s\n", line)
	}
	fmt.Fprintln(s.W)
}

// LineFormShown renders the note's identity line form for display.
func LineFormShown(n *exchange.Unit) string {
	return view.LineForm(n.Identity.Namespace, n.Identity.Type, n.Identity.ID)
}

// noteRoleTag renders the role as a colored tag: implementation in
// progress color, review in warning color, fix in danger color,
// unknown roles dim.
func noteRoleTag(s *ui.Style, role string) string {
	word := role
	var colored string
	switch role {
	case "implementation":
		colored = s.Progress(word)
	case "review":
		colored = s.Warning(word)
	case "fix":
		colored = s.Error(word)
	default:
		colored = s.Dim(word)
	}
	return "[" + colored + "]"
}

// noteStateMark renders the note-state as a colored mark: open info,
// resolved success, dismissed dim, unknown dim.
func noteStateMark(s *ui.Style, state string) string {
	switch state {
	case "open":
		return s.Info("open")
	case "resolved":
		return s.Success("resolved")
	case "dismissed":
		return s.Dim("dismissed")
	default:
		return s.Dim(state)
	}
}

// noteContentLines renders the per-role content fields of a note as
// deterministic display lines: the primary field first (summary /
// verdict / detail), then the list fields (changes, tests, notes,
// addresses) as bulleted lines. Unknown or absent fields are skipped.
func noteContentLines(s *ui.Style, content map[string]any) []string {
	var out []string
	for _, k := range []string{"summary", "verdict", "detail"} {
		if v, ok := content[k].(string); ok && strings.TrimSpace(v) != "" {
			out = append(out, s.Accent(k)+": "+v)
			break
		}
	}
	for _, k := range []string{"changes", "tests", "notes", "addresses"} {
		items, ok := asStringList(content[k])
		if !ok || len(items) == 0 {
			continue
		}
		out = append(out, s.Accent(k)+":")
		for _, item := range items {
			out = append(out, "  • "+item)
		}
	}
	return out
}

// asStringList coerces a JSON-decoded value to a list of strings:
// []string verbatim, []any of strings converted, anything else not
// ok. The conformance helper is unexported, so the renderers keep
// their own.
func asStringList(v any) ([]string, bool) {
	switch t := v.(type) {
	case []string:
		return t, true
	case []any:
		out := make([]string, 0, len(t))
		for _, item := range t {
			s, ok := item.(string)
			if !ok {
				return nil, false
			}
			out = append(out, s)
		}
		return out, true
	default:
		return nil, false
	}
}

// renderTicketReply renders one reply of a note card as the tree
// child: the branch prefix ("└─") indents the reply under its parent,
// the reply identity with the role tag and note-state mark, the
// authoring metadata (author with identity kind · created), and the
// reply body.
func renderTicketReply(s *ui.Style, r *exchange.Unit) {
	content := map[string]any{}
	if r.Content.Representation == exchange.StructuredJSON {
		if err := json.Unmarshal(r.ContentPayload, &content); err != nil {
			content = map[string]any{}
		}
	}
	role, _ := content["role"].(string)
	if role == "" {
		role = "reply"
	}
	headline := fmt.Sprintf("  └─ %s  %s  %s",
		LineFormShown(r),
		noteRoleTag(s, role),
		noteStateMark(s, r.StateVector.NoteState))
	fmt.Fprintln(s.W, headline)
	meta := "cmt:" + r.Identity.ID
	if r.Author.Name != "" {
		meta = meta + " · " + authorLabel(s, r.Author)
	}
	if r.Created != "" {
		meta = meta + " · " + r.Created
	}
	fmt.Fprintf(s.W, "     %s\n", s.Dim(meta))
	if body, ok := content["body"].(string); ok && strings.TrimSpace(body) != "" {
		for _, line := range strings.Split(body, "\n") {
			fmt.Fprintf(s.W, "     %s\n", line)
		}
	}
	fmt.Fprintln(s.W)
}
