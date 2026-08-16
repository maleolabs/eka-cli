package cmd

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/maleolabs/eka-cli/cmd/ui"
	"github.com/maleolabs/eka-core/view"
)

// This file implements the Board projection renderer: every work item
// in the repository — across all execution containers (active and
// completed) and outside any container — as a kanban board. Where the
// execution board answers "what is currently being worked on?", the
// board answers "what is the total work in the repository?". Each item
// keeps its container context as a tag; items without a referencing
// ticket container render as unassigned.

// renderBoardProjection renders the Board projection: the context
// header, the scope line, the six-column kanban board with per-item
// container tags (and assignee tags when assigned), the unassigned
// warning when present (repository-wide board), the dedicated
// 'No assignee' bucket of the member-scoped board, and the insight
// summary. page windows every column to its [offset, offset+limit)
// slice (nil = full columns); when a column overflows its window, a
// dim "… N more" card closes the column. footer is the pre-rendered
// pagination line printed after the insights ("" when no window was
// applied). The full counts always come from the untouched projection.
func renderBoardProjection(s *ui.Style, g *view.Graph, p *view.BoardProjection, page *view.Page, footer string) {
	header := ui.NewHeader(s, "Board")
	if p.Member != "" {
		// The member-scoped board: the header names the member line
		// instead of the container axis.
		header.Add("Member", p.Member)
	} else {
		header.Add("Container", "all")
	}
	header.
		Add("Repository", g.Root()).
		Add("Knowledge", "EKA v"+standardVersion).
		Add("Domain", "Execution").
		Pipeline("View").
		Render()
	fmt.Fprintln(s.W)
	if p.Total == 0 {
		// Empty projection: a calm line, still exit 0 with the summary.
		fmt.Fprintf(s.W, "%s\n", s.Dim("No work items."))
	} else if p.Member != "" {
		// The member-scoped scope line: the member's items plus the
		// 'No assignee' bucket size (the member board never counts
		// containers — the container axis is out of scope there).
		fmt.Fprintf(s.W, "%s\n", s.Dim(plural(p.Total, "work item", "work items")+
			" in member scope · "+plural(len(p.NoAssignee), "item", "items")+" without an assignee"))
	} else {
		fmt.Fprintf(s.W, "%s\n", s.Dim(plural(p.Total, "work item", "work items")+
			" across "+plural(p.ContainerCount, "container", "containers")))
	}
	fmt.Fprintln(s.W)
	renderBoardColumns(s, g, p.Columns, page)
	if p.Unassigned > 0 {
		fmt.Fprintln(s.W)
		fmt.Fprintf(s.W, "%s\n", s.Warning(plural(p.Unassigned, "work item", "work items")+
			" not referenced by any ticket container"))
	}
	// The dedicated 'No assignee' bucket of the member-scoped board:
	// rendered below the six columns whenever non-empty — never
	// silently excluded (ADR-029 Decision 3).
	if p.Member != "" && len(p.NoAssignee) > 0 {
		renderNoAssigneeBucket(s, g, p.NoAssignee, page)
	}
	renderBoardInsights(s, p)
	if page != nil {
		fmt.Fprintln(s.W)
		fmt.Fprintf(s.W, "%s\n", s.Dim(footer))
	}
}

// renderBoardColumns renders the work board: the fixed five
// execution-state columns with the short ids of their work items as
// cell labels, each tagged with its container context and — when
// assigned — its member (assignee) context. page windows every column
// to its slice (nil = full columns); an overflowing column closes with
// a dim "… N more" card.
func renderBoardColumns(s *ui.Style, g *view.Graph, cols view.BoardColumns, page *view.Page) {
	board := ui.NewBoard(s)
	var all []view.BoardItem
	for _, col := range cols {
		all = append(all, col.WorkItems...)
	}
	short := shortWorkItemID(boardItemWorkItems(all))
	shortCtr := shortContainerID(all)
	shortMbr := shortMemberID(all)
	budget := ui.BoardItemBudget(s.Width, len(cols))
	for _, col := range cols {
		start, end := 0, len(col.WorkItems)
		if page != nil {
			start, end = page.Window(len(col.WorkItems))
		}
		labels := make([]ui.Card, 0, end-start+1)
		for _, bi := range col.WorkItems[start:end] {
			labels = append(labels, boardCard(short(bi.WorkItem), g.NumberLabel(bi.WorkItem.Identity), bi.Type, shortCtr(bi), shortMbr(bi), budget,
				stateColor(s, col.State), typeBadgeColor(s, bi.Type), s.Accent, bi.WorkItem.NotesCount))
		}
		if end < len(col.WorkItems) {
			labels = append(labels, ui.Card{{{
				Text:  "… " + plural(len(col.WorkItems)-end, "more", "more"),
				Color: s.Dim,
			}}})
		}
		board.AddCards(boardTitle(col.State), stateColor(s, col.State), labels)
	}
	board.Render()
}

// renderNoAssigneeBucket renders the dedicated 'No assignee' bucket of
// the member-scoped board: every work item WITHOUT an assigned-to edge
// as a single-column board below the six state columns. Rendered
// whenever non-empty — the bucket is never hidden (ADR-029 Decision 3:
// unassigned work surfaces here, never silently excluded). page
// windows the bucket like a column (the pagination window is shared).
func renderNoAssigneeBucket(s *ui.Style, g *view.Graph, bucket []view.BoardItem, page *view.Page) {
	fmt.Fprintln(s.W)
	board := ui.NewBoard(s)
	short := shortWorkItemID(boardItemWorkItems(bucket))
	shortCtr := shortContainerID(bucket)
	budget := ui.BoardItemBudget(s.Width, 1)
	start, end := 0, len(bucket)
	if page != nil {
		start, end = page.Window(len(bucket))
	}
	labels := make([]ui.Card, 0, end-start+1)
	for _, bi := range bucket[start:end] {
		labels = append(labels, boardCard(short(bi.WorkItem), g.NumberLabel(bi.WorkItem.Identity), bi.Type, shortCtr(bi), "", budget,
			stateColor(s, bi.State), typeBadgeColor(s, bi.Type), s.Accent, bi.WorkItem.NotesCount))
	}
	if end < len(bucket) {
		labels = append(labels, ui.Card{{{
			Text:  "… " + plural(len(bucket)-end, "more", "more"),
			Color: s.Dim,
		}}})
	}
	board.AddCards("No Assignee", s.Dim, labels)
	board.Render()
}

// typeBadgeColor returns the badge color function for a work item type
// token. Canonical EKA tokens and their common aliases (story → sto)
// share a color, so a repository using alternative tokens keeps the
// same badge; unknown tokens fall back to the neutral default — a new
// type never breaks the board. Extend by adding a case (or an alias to
// an existing case).
func typeBadgeColor(s *ui.Style, token string) func(string) string {
	switch token {
	case "sto", "story":
		return s.Info // story — primary blue
	case "ts", "tech-story":
		return s.Progress // technical story — cyan
	case "bug", "defect":
		return s.Error // bug — danger red
	case "td", "tech-debt":
		return s.Warning // tech debt — amber
	case "ch", "spk":
		return s.Dim // chore, spike — gray
	default:
		return s.Dim // unknown token — neutral default
	}
}

// boardCard composes the three-line item card: an optional issue-number
// label followed by the item name on the first line, the type badge and
// container context on the second, and the note count on the third.
// The badge and the container tag are separate colored segments: the
// badge takes the type color, the tag takes the execution-state color.
// memberTag ("" = none) appends the assignee tag after the container
// tag — the member axis of the card (ADR-029); when the column is
// narrow the badge is dropped first, the container tag survives, and
// the assignee tag truncates last (the container is the primary tag).
// The issue-number label takes the accent color and is never truncated;
// the name is truncated to fit the remaining budget.
func boardCard(id, numberLabel, typeToken, tag, memberTag string, budget int, stateColor, badgeColor, numberColor func(string) string, notesCount int) ui.Card {
	name := id
	var line1 ui.CardLine
	if numberLabel != "" {
		nameBudget := budget - utf8.RuneCountInString(numberLabel) - 1 // space before name
		if nameBudget < 1 {
			name = ""
		} else {
			name = truncateRunes(name, nameBudget)
		}
		line1 = ui.CardLine{{Text: numberLabel, Color: numberColor}}
		if name != "" {
			line1 = append(line1, ui.Segment{Text: " " + name, Color: stateColor})
		}
	} else {
		name = truncateRunes(name, budget)
		line1 = ui.CardLine{{Text: name, Color: stateColor}}
	}

	badge := "[" + typeToken + "]"
	joined := tag
	if memberTag != "" {
		joined += " · " + memberTag
	}
	context := badge + " · " + joined
	if utf8.RuneCountInString(context) > budget {
		// Narrow column: drop the badge, keep the tags in the state
		// color (the container tag first — it truncates last).
		if utf8.RuneCountInString(joined) <= budget {
			context = joined
			badge = ""
		} else {
			context = truncateRunes(joined, budget)
			badge = ""
		}
	}
	line2 := ui.CardLine{{Text: context, Color: stateColor}}
	if badge != "" {
		line2 = ui.CardLine{
			{Text: badge, Color: badgeColor},
			{Text: " · " + joined, Color: stateColor},
		}
	}

	// The note count is shown always, including 0 — the container board
	// surfaces the discussion load of every item.
	notesLine := strconv.Itoa(notesCount) + " notes"
	if utf8.RuneCountInString(notesLine) > budget {
		notesLine = truncateRunes(notesLine, budget)
	}
	line3 := ui.CardLine{{Text: notesLine, Color: stateColor}}

	return ui.Card{line1, line2, line3}
}

// truncateRunes shortens text to the display budget, appending "…"
// when it does not fit. Operates on runes (display cells), never on
// bytes.
func truncateRunes(text string, budget int) string {
	if utf8.RuneCountInString(text) <= budget {
		return text
	}
	runes := []rune(text)
	return string(runes[:budget-1]) + "…"
}

// boardItemWorkItems extracts the embedded work items of a board item
// set, preserving order (for the shared short-id ambiguity logic).
func boardItemWorkItems(items []view.BoardItem) []view.WorkItem {
	out := make([]view.WorkItem, len(items))
	for i, bi := range items {
		out[i] = bi.WorkItem
	}
	return out
}

// shortContainerID renders the container tag of a board item: the bare
// container id ("wave-7"), or the full canonical identity when the id
// is ambiguous across distinct containers; "(unassigned)" when no
// container references the item. Multiple containers join
// comma-separated.
func shortContainerID(items []view.BoardItem) func(view.BoardItem) string {
	forms := make([]string, len(items))
	byForm := make(map[string][]string, len(items))
	for i, bi := range items {
		forms[i] = bi.Identity
		byForm[bi.Identity] = bi.Containers
	}
	tag := containerTagRenderer(forms, func(form string) []string { return byForm[form] })
	return func(bi view.BoardItem) string { return tag(bi.Identity) }
}

// shortMemberID renders the assignee tag of a board item: the bare
// member id ("alice"), or the full canonical identity when the id is
// ambiguous across distinct member lines; "" when the item has no
// assignee. The member axis of the card (ADR-029).
func shortMemberID(items []view.BoardItem) func(view.BoardItem) string {
	// id → the distinct full identities sharing that id (a bare member
	// id is ambiguous only when TWO DIFFERENT member lines share it).
	idToForms := make(map[string][]string)
	for _, bi := range items {
		if bi.Assignee == "" {
			continue
		}
		id := shortID(bi.Assignee)
		if !stringSliceContains(idToForms[id], bi.Assignee) {
			idToForms[id] = append(idToForms[id], bi.Assignee)
		}
	}
	return func(bi view.BoardItem) string {
		if bi.Assignee == "" {
			return ""
		}
		if len(idToForms[shortID(bi.Assignee)]) > 1 {
			return bi.Assignee
		}
		return shortID(bi.Assignee)
	}
}

// containerTagRenderer builds the container-tag renderer shared by the
// board and execution projections. items are the canonical identity
// forms of the rendered item set; containersOf resolves the containers
// of one item (board: the item's own Containers; execution: the graph
// membership helper). A bare container id is ambiguous only when TWO
// DIFFERENT containers share it — not when one container references
// many items (the tag frequency).
func containerTagRenderer(items []string, containersOf func(string) []string) func(string) string {
	// id → the distinct full identities sharing that id.
	idToForms := make(map[string][]string)
	for _, form := range items {
		for _, c := range containersOf(form) {
			id := shortID(c)
			if !stringSliceContains(idToForms[id], c) {
				idToForms[id] = append(idToForms[id], c)
			}
		}
	}
	return func(form string) string {
		containers := containersOf(form)
		if len(containers) == 0 {
			return "unassigned"
		}
		parts := make([]string, 0, len(containers))
		for _, c := range containers {
			id := shortID(c)
			if len(idToForms[id]) > 1 {
				parts = append(parts, c)
			} else {
				parts = append(parts, id)
			}
		}
		return strings.Join(parts, ", ")
	}
}

// stringSliceContains reports whether s contains v.
func stringSliceContains(s []string, v string) bool {
	for _, e := range s {
		if e == v {
			return true
		}
	}
	return false
}

// shortID renders the bare id of a canonical identity form
// ("<namespace>/<type>:<id>" → "<id>"). Ids never contain colons
// (identity contract), so the last colon is the separator.
func shortID(form string) string {
	if i := strings.LastIndex(form, ":"); i >= 0 {
		return form[i+1:]
	}
	return form
}

// renderBoardInsights renders the board summary: total work, active
// work (in progress + in review), completed work, the review queue,
// unassigned items, and overall progress. The member-scoped board
// replaces the container-axis "Unassigned" insight with the
// "No Assignee" count of the member-axis bucket (ADR-029) — the
// container axis is suppressed in member views.
func renderBoardInsights(s *ui.Style, p *view.BoardProjection) {
	inProgress := p.Columns.Count("in-progress")
	inReview := p.Columns.Count("in-review")
	done := p.Columns.Count("done")
	percent := "0%"
	if p.Total > 0 {
		percent = strconv.Itoa(done*100/p.Total) + "%"
	}
	progress := ui.ProgressBar(s, done, p.Total) + " " + fmt.Sprintf("%d/%d (%s)", done, p.Total, percent)
	if p.Member != "" {
		ui.NewSummary(s).
			Add("Total Work Items", strconv.Itoa(p.Total)).
			Add("Active Work", strconv.Itoa(inProgress+inReview)).
			Add("Completed Work", strconv.Itoa(done)).
			Add("Review Queue", strconv.Itoa(inReview)).
			Add("No Assignee", strconv.Itoa(len(p.NoAssignee))).
			Add("Overall Progress", progress).
			Render()
		return
	}
	ui.NewSummary(s).
		Add("Total Work Items", strconv.Itoa(p.Total)).
		Add("Active Work", strconv.Itoa(inProgress+inReview)).
		Add("Completed Work", strconv.Itoa(done)).
		Add("Review Queue", strconv.Itoa(inReview)).
		Add("Unassigned", strconv.Itoa(p.Unassigned)).
		Add("Overall Progress", progress).
		Render()
}
