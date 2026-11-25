package cmd

import (
	"bytes"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/maleolabs/eka-cli/cmd/ui"
	"github.com/maleolabs/eka-core/conformance"
	"github.com/maleolabs/eka-core/exchange"
	"github.com/maleolabs/eka-core/view"
)

// rendererTestContext builds the plain style + an empty graph used by
// the renderer unit tests.
func rendererTestContext(t *testing.T) (*ui.Style, *bytes.Buffer, *view.Graph) {
	t.Helper()
	var buf bytes.Buffer
	s := ui.NewStyle(&buf, false)
	return s, &buf, view.NewGraph(".", nil)
}

// unit builds one canonical unit for renderer tests: identity line at
// instance-version 1, the given revision, a state vector from the
// domain map, and relationships canonicalized to the RSF identity form
// (targets written in the authoring reference convention).
func unit(t *testing.T, ns, token, id string, revision int, states map[string]string, rels ...exchange.Relationship) *exchange.Unit {
	t.Helper()
	u := &exchange.Unit{
		Identity:              exchange.Identity{Namespace: ns, Type: token, ID: id, InstanceVersion: 1},
		CanonicalIdentityForm: ns + "/" + token + ":" + id + ":1",
		Revision:              revision,
		StateVector: exchange.StateVector{
			ContentState:   states[conformance.DomainContentState],
			ExecutionState: states[conformance.DomainExecutionState],
			PlanningState:  states[conformance.DomainPlanningState],
			ContainerState: states[conformance.DomainContainerState],
			ExistenceState: states[conformance.DomainExistenceState],
		},
		Relationships: []exchange.Relationship{},
	}
	for _, r := range rels {
		ref, err := conformance.ParseReference(r.Target, ns, token)
		if err != nil {
			t.Fatalf("unit: relationship target %q: %v", r.Target, err)
		}
		version := 1
		if ref.HasVersion {
			version = ref.Version
		}
		u.Relationships = append(u.Relationships, exchange.Relationship{
			Type:   r.Type,
			Target: ref.Namespace + "/" + ref.Type + ":" + ref.ID + ":" + strconv.Itoa(version),
		})
	}
	return u
}

// graphWith builds a graph over the given units (relationship targets
// in the authoring reference convention).
func graphWith(units ...*exchange.Unit) *view.Graph {
	return view.NewGraph(".", units)
}

// graphWithContainer builds a graph where each of the given work item
// forms is a member of container feather/ctr:wave-7 (one ticket per
// item), so renderer tests resolve container tags to "wave-7".
func graphWithContainer(t *testing.T, forms ...string) *view.Graph {
	t.Helper()
	units := []*exchange.Unit{
		unit(t, "feather", "ctr", "wave-7", 1,
			map[string]string{conformance.DomainContainerState: "active"}),
	}
	for i, form := range forms {
		parts := strings.SplitN(form, "/", 2)
		ns, rest := parts[0], parts[1]
		typeID := strings.SplitN(rest, ":", 2)
		token, id := typeID[0], typeID[1]
		units = append(units,
			unit(t, ns, token, id, 1,
				map[string]string{conformance.DomainExecutionState: "todo"}),
			unit(t, ns, "tkt", fmt.Sprintf("tkt-%d", i), 1, nil,
				exchange.Relationship{Type: "derives-from", Target: "ctr:wave-7"},
				exchange.Relationship{Type: "derives-from", Target: token + ":" + id}),
		)
	}
	return view.NewGraph(".", units)
}

// TestRenderBoardProjection: the board renders every work item with its
// container tag; unassigned items carry the unassigned tag.
func TestRenderBoardProjection(t *testing.T) {
	s, buf, g := rendererTestContext(t)
	p := &view.BoardProjection{
		Columns: view.BoardColumns{
			{State: "planned", WorkItems: []view.BoardItem{
				{WorkItem: view.WorkItem{Identity: "feather/sto:alpha", Type: "sto", ID: "alpha", State: "planned"}, Containers: []string{"feather/ctr:wave-7"}},
			}},
			{State: "todo", WorkItems: []view.BoardItem{
				{WorkItem: view.WorkItem{Identity: "feather/sto:orphan", Type: "sto", ID: "orphan", State: "todo"}},
			}},
			{State: "in-progress", WorkItems: []view.BoardItem{
				{WorkItem: view.WorkItem{Identity: "feather/sto:beta", Type: "sto", ID: "beta", State: "in-progress"}, Containers: []string{"feather/ctr:wave-7"}},
			}},
			{State: "in-review", WorkItems: nil},
			{State: "done", WorkItems: []view.BoardItem{
				{WorkItem: view.WorkItem{Identity: "feather/ch:gamma", Type: "ch", ID: "gamma", State: "done"}, Containers: []string{"feather/ctr:wave-7"}},
			}},
		},
		Total:          4,
		Unassigned:     1,
		ContainerCount: 1,
	}
	renderBoardProjection(s, g, p, nil, "")
	out := buf.String()
	for _, want := range []string{
		"Board",
		"Container    all",
		"Domain       Execution",
		"4 work items across 1 container",
		"│ Planned (1)",
		"│ Todo (1)",
		"│ In Progress (1)",
		"│ In Review (0)",
		"│ Done (1)",
		"│ ▸ alpha",
		"│   [sto] · wave-7",
		"│   0 notes",
		"│ ▸ orphan",
		"│   [sto] · unassigned",
		"│   0 notes",
		"│ ▸ beta",
		"│   [sto] · wave-7",
		"│   0 notes",
		"│ ▸ gamma",
		"│   [ch] · wave-7",
		"│   0 notes",
		"—", // empty column
		"1 work item not referenced by any ticket container",
		"Total Work Items: 4",
		"Active Work: 1",
		"Completed Work: 1",
		"Review Queue: 0",
		"Unassigned: 1",
		"Overall Progress: ██░░░░░░░░ 1/4 (25%)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("board output must contain %q:\n%s", want, out)
		}
	}
}

// TestRenderBoardProjectionEmpty: no work items renders a calm empty
// projection with the full five-column shape and a zero summary.
func TestRenderBoardProjectionEmpty(t *testing.T) {
	s, buf, g := rendererTestContext(t)
	p := &view.BoardProjection{Columns: emptyBoardColumns()}
	renderBoardProjection(s, g, p, nil, "")
	out := buf.String()
	for _, want := range []string{
		"No work items.",
		"│ Planned (0)",
		"│ Todo (0)",
		"│ In Progress (0)",
		"│ In Review (0)",
		"│ Done (0)",
		"Total Work Items: 0",
		"Overall Progress: ░░░░░░░░░░ 0/0 (0%)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("empty board output must contain %q:\n%s", want, out)
		}
	}
}

// emptyBoardColumns returns the fixed five empty board columns.
func emptyBoardColumns() view.BoardColumns {
	cols := make(view.BoardColumns, 0, 5)
	for _, state := range []string{"planned", "todo", "in-progress", "in-review", "done"} {
		cols = append(cols, view.BoardColumn{State: state})
	}
	return cols
}

// cardText flattens a card to its display text ("line1\nline2").
func cardText(c ui.Card) string {
	lines := make([]string, len(c))
	for i, line := range c {
		parts := make([]string, len(line))
		for j, seg := range line {
			parts[j] = seg.Text
		}
		lines[i] = strings.Join(parts, "")
	}
	return strings.Join(lines, "\n")
}

// TestBoardCard: the three-line card composes the issue-number label
// (when present) followed by the name on the first line, the type ·
// container context on the second, and the note count on the third;
// truncation prefers the name, the number label is never truncated,
// and on narrow budgets the badge is dropped before the container tag.
func TestBoardCard(t *testing.T) {
	budget := ui.BoardItemBudget(0, 5)
	cases := []struct {
		id, number, typeToken, tag string
		notes                      int
		want                       string
	}{
		// Fits: number label + full name + full context + notes.
		{"alpha", "#3", "sto", "wave-7", 3, "#3 alpha\n[sto] · wave-7\n3 notes"},
		// Number label omitted when empty.
		{"alpha", "", "sto", "wave-7", 3, "alpha\n[sto] · wave-7\n3 notes"},
		// Long name fits whole on its own line (the card's point);
		// the context line stays intact.
		{"markdown-syntax-highlighting", "", "sto", "wave-7", 0, "markdown-syntax-highlighting\n[sto] · wave-7\n0 notes"},
		// Unassigned context kept too.
		{"markdown-syntax-highlighting", "", "sto", "unassigned", 0, "markdown-syntax-highlighting\n[sto] · unassigned\n0 notes"},
	}
	for _, c := range cases {
		got := boardCard(c.id, c.number, c.typeToken, c.tag, "", budget, nil, typeBadgeColor(ui.NewStyle(&bytes.Buffer{}, false), c.typeToken), nil, c.notes)
		if cardText(got) != c.want {
			t.Errorf("boardCard(%q, %q, %q, %q, %d) = %q, want %q", c.id, c.number, c.typeToken, c.tag, budget, cardText(got), c.want)
		}
	}
}

// TestBoardCardNarrowBudget: on a narrower terminal the budget shrinks
// with the column width; the badge is dropped before the tag.
func TestBoardCardNarrowBudget(t *testing.T) {
	// 80-cell terminal: (80-16)/5 = 12 per column, budget 10.
	budget := ui.BoardItemBudget(80, 5)
	if budget != 10 {
		t.Fatalf("BoardItemBudget(80, 5) = %d, want 10", budget)
	}
	got := boardCard("markdown-syntax-highlighting", "", "sto", "wave-7", "", budget, nil, typeBadgeColor(ui.NewStyle(&bytes.Buffer{}, false), "sto"), nil, 0)
	want := "markdown-…\nwave-7\n0 notes"
	if cardText(got) != want {
		t.Errorf("boardCard on 80-col = %q, want %q (tag intact, badge dropped, notes on own line)", cardText(got), want)
	}
	// Number label is never truncated; the name is truncated to fit.
	got2 := boardCard("markdown-syntax-highlighting", "#7", "sto", "wave-7", "", budget, nil, typeBadgeColor(ui.NewStyle(&bytes.Buffer{}, false), "sto"), nil, 0)
	want2 := "#7 markdo…\nwave-7\n0 notes"
	if cardText(got2) != want2 {
		t.Errorf("boardCard with overflow numberLabel = %q, want %q (number kept, name truncated)", cardText(got2), want2)
	}
}

// TestBoardCardSegments: the badge and the container tag are separate
// colored segments — the badge takes the type color, the tag takes the
// execution-state color. The issue-number label is a third segment on
// the first line with the accent color.
func TestBoardCardSegments(t *testing.T) {
	colored := &ui.Style{Color: true, W: &bytes.Buffer{}}
	state := colored.Progress // in-progress presentation
	badge := typeBadgeColor(colored, "bug")
	card := boardCard("fix-login", "#3", "bug", "wave-7", "", ui.BoardItemBudget(0, 5), state, badge, colored.Accent, 2)
	if len(card) != 3 {
		t.Fatalf("card lines = %d, want 3", len(card))
	}
	line1 := card[0]
	if len(line1) != 2 {
		t.Fatalf("name segments = %d, want 2 (number label + name)", len(line1))
	}
	if line1[0].Text != "#3" || line1[1].Text != " fix-login" {
		t.Errorf("name segments = %q | %q, want #3 |  fix-login", line1[0].Text, line1[1].Text)
	}
	if line1[0].Color("#3") != colored.Accent("#3") {
		t.Errorf("number label color mismatch: got %q, want accent", line1[0].Color("#3"))
	}
	line2 := card[1]
	if len(line2) != 2 {
		t.Fatalf("context segments = %d, want 2 (badge + tag)", len(line2))
	}
	if line2[0].Text != "[bug]" || line2[1].Text != " · wave-7" {
		t.Errorf("context segments = %q | %q, want [bug] |  · wave-7", line2[0].Text, line2[1].Text)
	}
	line3 := card[2]
	if len(line3) != 1 {
		t.Fatalf("notes segments = %d, want 1", len(line3))
	}
	if line3[0].Text != "2 notes" {
		t.Errorf("notes segment = %q, want 2 notes", line3[0].Text)
	}
	badgeRendered := line2[0].Color("[bug]")
	tagRendered := line2[1].Color(" · wave-7")
	if badgeRendered == tagRendered {
		t.Error("badge and tag must render in different colors")
	}
	if !strings.Contains(badgeRendered, "\x1b") || !strings.Contains(tagRendered, "\x1b") {
		t.Error("colored segments must carry ANSI on a colored style")
	}
}

// TestTypeBadgeColor: canonical tokens and aliases share a color, and
// unknown tokens fall back to the neutral default.
func TestTypeBadgeColor(t *testing.T) {
	// Non-TTY: every color is identity — plain text, no escapes.
	plain := ui.NewStyle(&bytes.Buffer{}, false)
	if got := typeBadgeColor(plain, "unknown-type")("x"); got != "x" {
		t.Errorf("unknown token badge = %q, want plain %q (never an error)", got, "x")
	}
	// TTY: colors differ per type; aliases share their canonical color.
	colored := &ui.Style{Color: true, W: &bytes.Buffer{}}
	sto := typeBadgeColor(colored, "sto")
	story := typeBadgeColor(colored, "story")
	if sto("x") != story("x") {
		t.Error("sto and story must share the badge color")
	}
	if typeBadgeColor(colored, "bug")("x") == sto("x") {
		t.Error("bug must differ from story")
	}
	if strings.Contains(sto("x"), "\x1b") == false {
		t.Error("colored badge must carry ANSI on a colored style")
	}
}

func TestRenderExecutionBoard(t *testing.T) {
	s, buf, g := rendererTestContext(t)
	p := &view.ExecutionProjection{
		Container: &view.Container{Identity: "feather/ctr:wave-7", State: "active"},
		Tickets: []view.Ticket{
			{Identity: "feather/tkt:a", Projected: "done"},
			{Identity: "feather/tkt:b", Projected: "in-progress"},
		},
		Columns: view.StateColumns{
			{State: "planned", WorkItems: []view.WorkItem{{Identity: "feather/sto:alpha", Type: "sto", ID: "alpha", State: "planned"}}},
			{State: "todo", WorkItems: nil},
			{State: "in-progress", WorkItems: []view.WorkItem{{Identity: "feather/sto:beta", Type: "sto", ID: "beta", State: "in-progress"}}},
			{State: "in-review", WorkItems: nil},
			{State: "done", WorkItems: []view.WorkItem{{Identity: "feather/ch:gamma", Type: "ch", ID: "gamma", State: "done"}}},
		},
		Total: 3,
	}
	// The items resolve to container wave-7 through the graph, so their
	// labels carry the container tag — same rule as the board.
	g = graphWithContainer(t, "feather/sto:alpha", "feather/sto:beta", "feather/ch:gamma")
	renderExecution(s, g, p)
	out := buf.String()
	for _, want := range []string{
		"Execution",
		"• feather/ctr:wave-7  (active)",
		"┌",
		"│ Planned (1)",
		"│ Todo (0)",
		"│ In Progress (1)",
		"│ In Review (0)",
		"│ Done (1)",
		"│ ▸ alpha",
		"│   [sto] · wave-7",
		"│   0 notes",
		"│ ▸ beta",
		"│   [sto] · wave-7",
		"│   0 notes",
		"│ ▸ gamma",
		"│   [ch] · wave-7",
		"│   0 notes",
		"—", // empty columns
		"2 tickets project these work items",
		"Active Work: 1",
		"Completed Work: 1",
		"Review Queue: 0",
		"Overall Progress: ███░░░░░░░ 1/3 (33%)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("execution board output must contain %q:\n%s", want, out)
		}
	}
}

// TestRenderExecutionSharedContainerTag: an item referenced by tickets
// of two containers shows both tags on the active container's board —
// the same tag rule as the board projection.
func TestRenderExecutionSharedContainerTag(t *testing.T) {
	units := []*exchange.Unit{
		unit(t, "feather", "ctr", "wave-7", 1,
			map[string]string{conformance.DomainContainerState: "active"}),
		unit(t, "feather", "ctr", "wave-0", 1,
			map[string]string{conformance.DomainContainerState: "completed"}),
		unit(t, "feather", "sto", "shared", 1,
			map[string]string{conformance.DomainExecutionState: "in-progress"}),
		unit(t, "feather", "tkt", "one", 1, nil,
			exchange.Relationship{Type: "derives-from", Target: "ctr:wave-7"},
			exchange.Relationship{Type: "derives-from", Target: "sto:shared"}),
		unit(t, "feather", "tkt", "two", 1, nil,
			exchange.Relationship{Type: "derives-from", Target: "ctr:wave-0"},
			exchange.Relationship{Type: "derives-from", Target: "sto:shared"}),
	}
	s, buf, _ := rendererTestContext(t)
	g := graphWith(units...)
	p := &view.ExecutionProjection{
		Container: &view.Container{Identity: "feather/ctr:wave-7", State: "active"},
		Columns: view.StateColumns{
			{State: "in-progress", WorkItems: []view.WorkItem{{Identity: "feather/sto:shared", Type: "sto", ID: "shared", State: "in-progress"}}},
		},
		Total: 1,
	}
	renderExecution(s, g, p)
	out := buf.String()
	for _, want := range []string{
		"│ ▸ shared",
		"│   [sto] · wave-0, wave-7",
		"│   0 notes",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("execution board output must contain %q:\n%s", want, out)
		}
	}
}

func TestRenderExecutionNoContainer(t *testing.T) {
	s, buf, g := rendererTestContext(t)
	p := &view.ExecutionProjection{
		Columns: view.StateColumns{
			{State: "planned"}, {State: "todo"}, {State: "in-progress"},
			{State: "in-review"}, {State: "done"},
		},
	}
	renderExecution(s, g, p)
	out := buf.String()
	for _, want := range []string{"No active container.", "—", "0/0 (0%)"} {
		if !strings.Contains(out, want) {
			t.Errorf("empty execution output must contain %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "tickets project") {
		t.Errorf("no-container output must not claim tickets:\n%s", out)
	}
}

// TestRenderExecutionContainerNoTickets: an active container without
// tkt- membership renders the no-tickets warning — the board shows no
// work items (they would be unassigned on the board projection). The
// warning is a plain footer line: exit path and summary stay intact.
func TestRenderExecutionContainerNoTickets(t *testing.T) {
	s, buf, g := rendererTestContext(t)
	p := &view.ExecutionProjection{
		Container: &view.Container{Identity: "feather/ctr:wave-7", State: "active"},
		Columns: view.StateColumns{
			{State: "planned"}, {State: "todo"}, {State: "in-progress"},
			{State: "in-review"}, {State: "done"}, {State: "canceled"},
		},
	}
	renderExecution(s, g, p)
	out := buf.String()
	for _, want := range []string{
		"• feather/ctr:wave-7  (active)",
		"Container has no tickets — work items are not linked to this container (create tkt- tickets deriving from it)",
		"—",
		"0/0 (0%)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("no-ticket execution output must contain %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "project these work items") {
		t.Errorf("no-ticket output must not claim tickets:\n%s", out)
	}
}

func TestRenderExecutionShortIDAmbiguity(t *testing.T) {
	// Same id across two work item types keeps the type prefix.
	items := []view.WorkItem{
		{Identity: "feather/sto:alpha", Type: "sto", ID: "alpha", State: "planned"},
		{Identity: "feather/ts:alpha", Type: "ts", ID: "alpha", State: "done"},
		{Identity: "feather/sto:beta", Type: "sto", ID: "beta", State: "done"},
	}
	short := shortWorkItemID(items)
	if got := short(items[0]); got != "sto:alpha" {
		t.Errorf("ambiguous id must keep the type prefix, got %q", got)
	}
	if got := short(items[1]); got != "ts:alpha" {
		t.Errorf("ambiguous id must keep the type prefix, got %q", got)
	}
	if got := short(items[2]); got != "beta" {
		t.Errorf("unique id must render bare, got %q", got)
	}
}

func TestRenderPlanningRoadmap(t *testing.T) {
	s, buf, g := rendererTestContext(t)
	p := &view.PlanningProjection{
		Plans: []view.PlanGroup{
			{Plan: view.DomainArtifact{Identity: "feather/plan:roadmap-v1", Type: "plan", ID: "roadmap-v1", ContentState: "approved", HasContentState: true, PlanningState: "approved", HasPlanningState: true, Phase: "mvp", HasPhase: true}},
		},
		Orphans: []view.DomainArtifact{
			{Identity: "feather/scp:mvp-v1", Type: "scp", ID: "mvp-v1", ContentState: "approved", HasContentState: true, Phase: "mvp", HasPhase: true},
			{Identity: "feather/epc:authoring", Type: "epc", ID: "authoring", ContentState: "review", HasContentState: true},
			{Identity: "feather/epc:distribution", Type: "epc", ID: "distribution", ContentState: "draft", HasContentState: true},
		},
		Traceability: []view.DomainArtifact{
			{Identity: "feather/trc:feather-trace", Type: "trc", ID: "feather-trace", ContentState: "approved", HasContentState: true},
		},
		PlansByState: []view.StateCount{
			{State: "draft", Count: 0}, {State: "approved", Count: 1}, {State: "immutable", Count: 0},
		},
	}
	renderPlanning(s, g, p)
	out := buf.String()
	for _, want := range []string{
		"Planning",
		"✓ feather/plan:roadmap-v1  (approved, planning-state approved, phase mvp)",
		"────",
		"▸ feather/scp:mvp-v1  (approved, phase mvp)",
		"▸ feather/epc:authoring  (review)",
		"▸ feather/epc:distribution  (draft)",
		"▸ traceability: feather/trc:feather-trace (approved)",
		"Committed: 1",
		"Exploring: 1",
		"Next milestone: mvp",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("planning roadmap output must contain %q:\n%s", want, out)
		}
	}
}

// TestRenderPlanningHierarchy: scope/epic lines deriving from a plan
// render as tree branches under their milestone (created-order per
// group); sub-plans nest under their parent plan with their own
// children one level deeper; plan roots all share the same level;
// orphan lines render after every plan group, before the
// traceability separator.
func TestRenderPlanningHierarchy(t *testing.T) {
	s, buf, g := rendererTestContext(t)
	p := &view.PlanningProjection{
		Plans: []view.PlanGroup{
			{Plan: view.DomainArtifact{Identity: "feather/plan:foundation", Type: "plan", ID: "foundation", ContentState: "approved", HasContentState: true, PlanningState: "approved", HasPlanningState: true, Phase: "mvp", HasPhase: true},
				Children: []view.DomainArtifact{
					{Identity: "feather/scp:foundation", Type: "scp", ID: "foundation", ContentState: "approved", HasContentState: true, Phase: "mvp", HasPhase: true},
				},
				SubPlans: []view.PlanGroup{
					{Plan: view.DomainArtifact{Identity: "feather/plan:sub", Type: "plan", ID: "sub", ContentState: "approved", HasContentState: true, PlanningState: "approved", HasPlanningState: true, Phase: "mvp", HasPhase: true},
						Children: []view.DomainArtifact{
							{Identity: "feather/scp:sub-a", Type: "scp", ID: "sub-a", ContentState: "approved", HasContentState: true, Phase: "mvp", HasPhase: true},
						}},
				}},
			{Plan: view.DomainArtifact{Identity: "feather/plan:feature-wave-1", Type: "plan", ID: "feature-wave-1", ContentState: "approved", HasContentState: true, PlanningState: "approved", HasPlanningState: true, Phase: "mvp", HasPhase: true},
				Children: []view.DomainArtifact{
					{Identity: "feather/scp:feature-mvp", Type: "scp", ID: "feature-mvp", ContentState: "approved", HasContentState: true, Phase: "mvp", HasPhase: true},
				}},
		},
		Orphans: []view.DomainArtifact{
			{Identity: "feather/scp:unplanned", Type: "scp", ID: "unplanned", ContentState: "draft", HasContentState: true},
		},
		Traceability: []view.DomainArtifact{
			{Identity: "feather/trc:feather-trace", Type: "trc", ID: "feather-trace", ContentState: "approved", HasContentState: true},
		},
		PlansByState: []view.StateCount{
			{State: "draft", Count: 0}, {State: "approved", Count: 3}, {State: "immutable", Count: 0},
		},
	}
	renderPlanning(s, g, p)
	out := buf.String()
	for _, want := range []string{
		"✓ feather/plan:foundation  (approved, planning-state approved, phase mvp)",
		// Every plan root shares the same level — no rail, no indent.
		"✓ feather/plan:feature-wave-1  (approved, planning-state approved, phase mvp)",
		// Child rows render as tree branches (├─ except the last └─).
		"├─ ▸ feather/scp:foundation  (approved, phase mvp)",
		"└─ ✓ feather/plan:sub  (approved, planning-state approved, phase mvp)",
		// Sub-plan children nest one level deeper under the branch.
		"   └─ ▸ feather/scp:sub-a  (approved, phase mvp)",
		"└─ ▸ feather/scp:feature-mvp  (approved, phase mvp)",
		// Orphans render after every plan group, before the separator.
		"▸ feather/scp:unplanned  (draft)",
		"────",
		"▸ traceability: feather/trc:feather-trace (approved)",
		"Committed: 3",
		"Next milestone: mvp",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("planning hierarchy output must contain %q:\n%s", want, out)
		}
	}
}

func TestRenderPlanningNoPlan(t *testing.T) {
	s, buf, g := rendererTestContext(t)
	p := &view.PlanningProjection{
		Orphans: []view.DomainArtifact{
			{Identity: "feather/epc:distribution", Type: "epc", ID: "distribution", ContentState: "draft", HasContentState: true},
		},
		PlansByState: []view.StateCount{{State: "draft"}, {State: "approved"}, {State: "immutable"}},
	}
	renderPlanning(s, g, p)
	out := buf.String()
	for _, want := range []string{"no plan yet — roadmap undefined", "Next milestone: —"} {
		if !strings.Contains(out, want) {
			t.Errorf("plan-less roadmap must contain %q:\n%s", want, out)
		}
	}
}

func TestRenderArchitectureTree(t *testing.T) {
	s, buf, _ := rendererTestContext(t)
	g := graphWith(
		unit(t, "feather", "adr", "content-storage", 1, nil,
			exchange.Relationship{Type: "depends-on", Target: "fnd:markdown-editor-options"}),
		unit(t, "feather", "fnd", "markdown-editor-options", 1, nil),
		unit(t, "feather", "arc", "feather-system", 1, nil),
	)
	p := &view.ArchitectureProjection{
		Groups: []view.Group{
			{Name: "Decisions", Artifacts: []view.DomainArtifact{
				{Identity: "feather/adr:content-storage", Type: "adr", ID: "content-storage", ContentState: "accepted", HasContentState: true},
				{Identity: "feather/dec:reverse-proxy", Type: "dec", ID: "reverse-proxy", ContentState: "review", HasContentState: true},
			}},
			{Name: "Architecture Descriptions", Artifacts: []view.DomainArtifact{
				{Identity: "feather/arc:feather-system", Type: "arc", ID: "feather-system", ContentState: "approved", HasContentState: true},
			}},
			{Name: "Specifications", Artifacts: nil},
			{Name: "Standards & Guidelines", Artifacts: []view.DomainArtifact{
				{Identity: "feather/std:definition-of-done", Type: "std", ID: "definition-of-done", ContentState: "approved", HasContentState: true},
			}},
			{Name: "Vocabulary", Artifacts: []view.DomainArtifact{
				{Identity: "feather/gls:feather-terms", Type: "gls", ID: "feather-terms", ContentState: "amended", HasContentState: true},
			}},
		},
	}
	renderArchitecture(s, g, p)
	out := buf.String()
	for _, want := range []string{
		"feather/arc:feather-system  (approved)",
		"├── Decisions",
		"│  ├── ✓ feather/adr:content-storage  (accepted) (depends-on fnd:markdown-editor-options)",
		"│  └── • feather/dec:reverse-proxy  (review)",
		"├── Standards & Guidelines",
		"│  └── ✓ feather/std:definition-of-done  (approved)",
		"└── Vocabulary",
		"   └── • feather/gls:feather-terms  (amended)",
		"Accepted decisions: 1",
		"Open items: 1",
		"Superseded: 0",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("architecture tree output must contain %q:\n%s", want, out)
		}
	}
	// The empty Specifications group is skipped.
	if strings.Contains(out, "Specifications") {
		t.Errorf("empty group must be skipped:\n%s", out)
	}
}

func TestRenderArchitectureNoDescription(t *testing.T) {
	s, buf, g := rendererTestContext(t)
	p := &view.ArchitectureProjection{
		Groups: []view.Group{
			{Name: "Decisions", Artifacts: []view.DomainArtifact{
				{Identity: "feather/adr:one", Type: "adr", ID: "one", ContentState: "accepted", HasContentState: true},
			}},
		},
	}
	renderArchitecture(s, g, p)
	out := buf.String()
	if !strings.Contains(out, "Architecture\n") {
		t.Errorf("no arc- artifact must root the tree at the Architecture node:\n%s", out)
	}
	// A single child subtree renders as the last branch.
	if !strings.Contains(out, "└── Decisions") {
		t.Errorf("decisions subtree must render under the root:\n%s", out)
	}
}

func TestRenderDiscoveryCards(t *testing.T) {
	s, buf, _ := rendererTestContext(t)
	g2 := graphWith(
		unit(t, "feather", "vis", "feather-vision", 3, nil),
		unit(t, "feather", "req", "comments-phase2", 1, nil),
	)
	p := &view.DiscoveryProjection{
		Groups: []view.Group{
			{Name: "Vision", Artifacts: []view.DomainArtifact{
				{Identity: "feather/vis:feather-vision", Type: "vis", ID: "feather-vision", ContentState: "approved", HasContentState: true},
			}},
			{Name: "Strategy", Artifacts: nil},
			{Name: "Requirements", Artifacts: []view.DomainArtifact{
				{Identity: "feather/req:comments-phase2", Type: "req", ID: "comments-phase2", ContentState: "draft", HasContentState: true},
			}},
			{Name: "Research Findings", Artifacts: nil},
		},
	}
	renderDiscovery(s, g2, p)
	out := buf.String()
	for _, want := range []string{
		"Vision",
		"┌",
		"│ ✓ feather/vis:feather-vision │",
		"│ approved · revision 3",
		"└",
		"Requirements",
		"│ ○ feather/req:comments-phase2 │",
		"│ draft · revision 1",
		"Committed direction: 1",
		"Exploring: 1",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("discovery cards output must contain %q:\n%s", want, out)
		}
	}
	// All content rows share one box width per group; the box width is
	// the widest content line + 4 (bars and pads).
	wantWidths := map[int]bool{}
	for _, s := range []string{"✓ feather/vis:feather-vision", "○ feather/req:comments-phase2"} {
		wantWidths[len([]rune(s))+4] = true
	}
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if !strings.HasPrefix(line, "│") {
			continue
		}
		if !wantWidths[len([]rune(line))] {
			t.Errorf("discovery card row %q spans %d cells, want one of %v", line, len([]rune(line)), wantWidths)
		}
	}
	if strings.Contains(out, "Strategy") || strings.Contains(out, "Research Findings") {
		t.Errorf("empty groups must be skipped:\n%s", out)
	}
}

func TestRenderOperationsRelease(t *testing.T) {
	s, buf, _ := rendererTestContext(t)
	g2 := graphWith(
		unit(t, "feather", "rel", "v090", 1, nil,
			exchange.Relationship{Type: "derives-from", Target: "plan:roadmap-v1:1"}),
		unit(t, "feather", "plan", "roadmap-v1", 1, nil),
	)
	p := &view.OperationsProjection{
		Groups: []view.Group{
			{Name: "Runbooks", Artifacts: []view.DomainArtifact{
				{Identity: "feather/run:deploy-feather", Type: "run", ID: "deploy-feather", ContentState: "approved", HasContentState: true},
				{Identity: "feather/run:backup-feather", Type: "run", ID: "backup-feather", ContentState: "draft", HasContentState: true},
			}},
			{Name: "Release Records", Artifacts: []view.DomainArtifact{
				{Identity: "feather/rel:v090", Type: "rel", ID: "v090", ContentState: "approved", HasContentState: true},
			}},
		},
	}
	renderOperations(s, g2, p)
	out := buf.String()
	for _, want := range []string{
		"Release Records",
		"┌",
		"│ ✓ feather/rel:v090",
		"│ approved",
		"│ derives-from plan:roadmap-v1",
		"Runbooks",
		"▸ feather/run:deploy-feather  (approved)",
		"│ ▸ feather/run:backup-feather  (draft)",
		"Releases delivered: 1",
		"Runbooks maintained: 2",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("operations output must contain %q:\n%s", want, out)
		}
	}
}

func TestRenderTicketDetail(t *testing.T) {
	s, buf, g := rendererTestContext(t)
	p := &view.TicketProjection{
		Ticket:     view.Ticket{Identity: "feather/tkt:sto-draft-autosave", Type: "tkt", ID: "sto-draft-autosave", Projected: "in-progress"},
		Container:  &view.Container{Identity: "feather/ctr:wave-7", State: "active"},
		WorkItem:   &view.WorkItem{Identity: "feather/sto:draft-autosave", Type: "sto", ID: "draft-autosave", State: "in-progress"},
		Projected:  "in-progress",
		References: []string{"ctr:wave-7", "sto:draft-autosave"},
	}
	renderTicket(s, g, p, false)
	out := buf.String()
	// The supporting rows align labels to the widest label ("Derives
	// From", 12 cells) plus 3 spaces, and the card pads every row to
	// its width — computed, not hand-counted.
	rows := [][2]string{
		{"Work Item", "feather/sto:draft-autosave (in-progress)"},
		{"Container", "feather/ctr:wave-7"},
		{"Derives From", "ctr:wave-7, sto:draft-autosave"},
	}
	width := len([]rune("feather/tkt:sto-draft-autosave"))
	for _, r := range rows {
		if w := 12 + 3 + len([]rune(r[1])); w > width {
			width = w
		}
	}
	ticketRow := func(label, value string) string {
		content := fmt.Sprintf("%-12s   %s", label, value)
		return "│ " + content + strings.Repeat(" ", width-len([]rune(content))) + " │"
	}
	for _, want := range []string{
		"Ticket",
		"Projected Status  → in-progress",
		"┌",
		"│ feather/tkt:sto-draft-autosave",
		ticketRow(rows[0][0], rows[0][1]),
		ticketRow(rows[1][0], rows[1][1]),
		ticketRow(rows[2][0], rows[2][1]),
		"└",
		"Projected status: in-progress",
		"Work item: feather/sto:draft-autosave (in-progress)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("ticket detail output must contain %q:\n%s", want, out)
		}
	}
}

func TestRenderTicketUnresolved(t *testing.T) {
	s, buf, g := rendererTestContext(t)
	p := &view.TicketProjection{
		Ticket:    view.Ticket{Identity: "feather/tkt:ghost", Projected: "unresolved"},
		Projected: "unresolved",
	}
	renderTicket(s, g, p, false)
	out := buf.String()
	rows := [][2]string{
		{"Work Item", "unresolved"},
		{"Container", "unresolved"},
		{"Derives From", "—"},
	}
	width := len([]rune("feather/tkt:ghost"))
	for _, r := range rows {
		if w := 12 + 3 + len([]rune(r[1])); w > width {
			width = w
		}
	}
	ticketRow := func(label, value string) string {
		content := fmt.Sprintf("%-12s   %s", label, value)
		return "│ " + content + strings.Repeat(" ", width-len([]rune(content))) + " │"
	}
	for _, want := range []string{
		"Projected Status  • unresolved",
		ticketRow(rows[0][0], rows[0][1]),
		ticketRow(rows[1][0], rows[1][1]),
		ticketRow(rows[2][0], rows[2][1]),
		"Projected status: unresolved",
		"Work item: unresolved",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("unresolved ticket output must contain %q:\n%s", want, out)
		}
	}
}

func TestRenderEmptyDomains(t *testing.T) {
	cases := []struct {
		name    string
		render  func(*ui.Style, *view.Graph)
		line    string
		summary string
	}{
		{"planning", func(s *ui.Style, g *view.Graph) {
			renderPlanning(s, g, &view.PlanningProjection{PlansByState: []view.StateCount{{State: "draft"}, {State: "approved"}, {State: "immutable"}}})
		}, "No Planning artifacts.", "Committed: 0"},
		{"architecture", func(s *ui.Style, g *view.Graph) {
			renderArchitecture(s, g, &view.ArchitectureProjection{Groups: nil})
		}, "No Architecture artifacts.", "Accepted decisions: 0"},
		{"discovery", func(s *ui.Style, g *view.Graph) {
			renderDiscovery(s, g, &view.DiscoveryProjection{Groups: nil})
		}, "No Discovery artifacts.", "Committed direction: 0"},
		{"operations", func(s *ui.Style, g *view.Graph) {
			renderOperations(s, g, &view.OperationsProjection{Groups: nil})
		}, "No Operations artifacts.", "Releases delivered: 0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			s := ui.NewStyle(&buf, false)
			g := view.NewGraph(".", nil)
			tc.render(s, g)
			out := buf.String()
			if !strings.Contains(out, tc.line) {
				t.Errorf("must render %q:\n%s", tc.line, out)
			}
			if !strings.Contains(out, tc.summary) {
				t.Errorf("must render insight %q:\n%s", tc.summary, out)
			}
			if !strings.Contains(out, "Summary:") {
				t.Errorf("must render the summary block:\n%s", out)
			}
		})
	}
}

func TestRenderersDeterministic(t *testing.T) {
	build := func() string {
		var buf bytes.Buffer
		s := ui.NewStyle(&buf, false)
		g := graphWithContainer(t, "feather/sto:alpha")
		renderExecution(s, g, &view.ExecutionProjection{
			Container: &view.Container{Identity: "feather/ctr:wave-7", State: "active"},
			Columns: view.StateColumns{
				{State: "done", WorkItems: []view.WorkItem{{Identity: "feather/sto:alpha", Type: "sto", ID: "alpha", State: "done"}}},
			},
			Total: 1,
		})
		renderPlanning(s, g, &view.PlanningProjection{PlansByState: nil})
		renderArchitecture(s, g, &view.ArchitectureProjection{Groups: nil})
		renderDiscovery(s, g, &view.DiscoveryProjection{Groups: nil})
		renderOperations(s, g, &view.OperationsProjection{Groups: nil})
		renderTicket(s, g, &view.TicketProjection{Ticket: view.Ticket{Identity: "x"}, Projected: "unresolved"}, false)
		renderBoardProjection(s, g, &view.BoardProjection{
			Columns: view.BoardColumns{
				{State: "done", WorkItems: []view.BoardItem{
					{WorkItem: view.WorkItem{Identity: "feather/sto:alpha", Type: "sto", ID: "alpha", State: "done"}, Containers: []string{"feather/ctr:wave-7"}},
				}},
			},
			Total: 1, ContainerCount: 1,
		}, nil, "")
		return buf.String()
	}
	if build() != build() {
		t.Error("renderer output must be deterministic")
	}
}

func TestRenderersNoANSI(t *testing.T) {
	var buf bytes.Buffer
	s := ui.NewStyle(&buf, false)
	g := graphWithContainer(t, "feather/sto:alpha")
	renderExecution(s, g, &view.ExecutionProjection{
		Container: &view.Container{Identity: "feather/ctr:wave-7", State: "active"},
		Columns: view.StateColumns{
			{State: "in-progress", WorkItems: []view.WorkItem{{Identity: "feather/sto:alpha", Type: "sto", ID: "alpha", State: "in-progress"}}},
			{State: "done", WorkItems: []view.WorkItem{{Identity: "feather/ch:beta", Type: "ch", ID: "beta", State: "done"}}},
		},
		Total: 2,
	})
	renderPlanning(s, g, &view.PlanningProjection{Plans: []view.PlanGroup{
		{Plan: view.DomainArtifact{Identity: "feather/plan:roadmap", Type: "plan", ID: "roadmap", ContentState: "approved", HasContentState: true, PlanningState: "approved", HasPlanningState: true, Phase: "mvp", HasPhase: true}},
	}, PlansByState: []view.StateCount{{State: "draft"}, {State: "approved", Count: 1}, {State: "immutable"}}})
	renderArchitecture(s, g, &view.ArchitectureProjection{Groups: []view.Group{
		{Name: "Decisions", Artifacts: []view.DomainArtifact{
			{Identity: "feather/adr:one", Type: "adr", ID: "one", ContentState: "accepted", HasContentState: true},
		}},
	}})
	renderDiscovery(s, g, &view.DiscoveryProjection{Groups: []view.Group{
		{Name: "Vision", Artifacts: []view.DomainArtifact{
			{Identity: "feather/vis:v", Type: "vis", ID: "v", ContentState: "approved", HasContentState: true},
		}},
	}})
	renderOperations(s, g, &view.OperationsProjection{Groups: []view.Group{
		{Name: "Runbooks", Artifacts: []view.DomainArtifact{
			{Identity: "feather/run:r", Type: "run", ID: "r", ContentState: "approved", HasContentState: true},
		}},
	}})
	renderTicket(s, g, &view.TicketProjection{Ticket: view.Ticket{Identity: "feather/tkt:t", Projected: "done"},
		WorkItem: &view.WorkItem{Identity: "feather/sto:w", Type: "sto", ID: "w", State: "done"}, Projected: "done"}, true)
	renderBoardProjection(s, g, &view.BoardProjection{
		Columns: view.BoardColumns{
			{State: "done", WorkItems: []view.BoardItem{
				{WorkItem: view.WorkItem{Identity: "feather/sto:b", Type: "sto", ID: "b", State: "done"}, Containers: []string{"feather/ctr:wave-7"}},
			}},
		},
		Total: 1, ContainerCount: 1,
	}, nil, "")
	if strings.Contains(buf.String(), "\x1b") {
		t.Error("non-TTY renderer output must not contain ANSI escapes")
	}
}

// TestStateColorCanceledDanger: the canceled column uses the danger
// (error/red) theme, distinct from the success theme of done.
func TestStateColorCanceledDanger(t *testing.T) {
	colored := &ui.Style{Color: true, W: &bytes.Buffer{}}
	if stateColor(colored, "canceled")("x") != colored.Error("x") {
		t.Error("canceled must use the danger (error) color")
	}
	if stateColor(colored, "canceled")("x") == stateColor(colored, "done")("x") {
		t.Error("canceled must not share the success color of done")
	}
	plain := ui.NewStyle(&bytes.Buffer{}, false)
	if stateColor(plain, "canceled")("x") != "x" {
		t.Error("canceled color is identity on a non-TTY style")
	}
}

// TestContainerStateColorPlannedDim: the planned container state uses
// the dim theme (born planned, waiting for activation) — distinct from
// the info theme of active; identity on a non-TTY style.
func TestContainerStateColorPlannedDim(t *testing.T) {
	colored := &ui.Style{Color: true, W: &bytes.Buffer{}}
	if containerStateColor(colored, "planned")("x") != colored.Dim("x") {
		t.Error("planned must use the dim theme")
	}
	if containerStateColor(colored, "planned")("x") == containerStateColor(colored, "active")("x") {
		t.Error("planned must not share the info theme of active")
	}
	if containerStateColor(colored, "active")("x") != colored.Info("x") {
		t.Error("active must use the info theme")
	}
	if containerStateColor(colored, "completed")("x") != colored.Success("x") {
		t.Error("completed must use the success theme")
	}
	plain := ui.NewStyle(&bytes.Buffer{}, false)
	if containerStateColor(plain, "planned")("x") != "x" {
		t.Error("planned color is identity on a non-TTY style")
	}
}

// TestRenderArchitectureCompact: on a narrow terminal the edge
// annotations are dropped whole — the tree structure and node text
// stay intact; non-TTY output keeps the annotations.
func TestRenderArchitectureCompact(t *testing.T) {
	s, buf, _ := rendererTestContext(t)
	g := graphWith(
		unit(t, "feather", "adr", "content-storage", 1, nil,
			exchange.Relationship{Type: "depends-on", Target: "fnd:markdown-editor-options"}),
		unit(t, "feather", "fnd", "markdown-editor-options", 1, nil),
	)
	p := &view.ArchitectureProjection{
		Groups: []view.Group{
			{Name: "Decisions", Artifacts: []view.DomainArtifact{
				{Identity: "feather/adr:content-storage", Type: "adr", ID: "content-storage", ContentState: "accepted", HasContentState: true},
			}},
			{Name: "Architecture Descriptions", Artifacts: []view.DomainArtifact{
				{Identity: "feather/arc:feather-system", Type: "arc", ID: "feather-system", ContentState: "approved", HasContentState: true},
			}},
		},
	}
	// Full layout (non-TTY, width 0): the resolvable depends-on edge is
	// annotated.
	renderArchitecture(s, g, p)
	if !strings.Contains(buf.String(), "depends-on fnd:markdown-editor-options") {
		t.Errorf("full layout must keep the edge annotation:\n%s", buf.String())
	}
	// Narrow terminal: the edge annotation is dropped whole.
	buf.Reset()
	s.Width = 60
	renderArchitecture(s, g, p)
	out := buf.String()
	if strings.Contains(out, "depends-on") || strings.Contains(out, "derives-from") {
		t.Errorf("narrow terminal must drop edge annotations:\n%s", out)
	}
	if !strings.Contains(out, "feather/adr:content-storage  (accepted)") {
		t.Errorf("node text stays intact on a narrow terminal:\n%s", out)
	}
}

// TestRenderPlanningCompact: on a narrow terminal the planning rows
// drop the planning-state/phase detail and show the content state
// alone — whole, never truncated; non-TTY output keeps the full
// state text.
func TestRenderPlanningCompact(t *testing.T) {
	s, buf, g := rendererTestContext(t)
	p := &view.PlanningProjection{
		Plans: []view.PlanGroup{
			{Plan: view.DomainArtifact{Identity: "feather/plan:roadmap-v1", Type: "plan", ID: "roadmap-v1", ContentState: "approved", HasContentState: true, PlanningState: "approved", HasPlanningState: true, Phase: "mvp", HasPhase: true}},
		},
		PlansByState: []view.StateCount{
			{State: "draft", Count: 0}, {State: "approved", Count: 1}, {State: "immutable", Count: 0},
		},
	}
	// Full layout (non-TTY, width 0): the full state text.
	renderPlanning(s, g, p)
	if !strings.Contains(buf.String(), "planning-state approved, phase mvp") {
		t.Errorf("full layout must keep the full state text:\n%s", buf.String())
	}
	// Narrow terminal: only the content state remains.
	buf.Reset()
	s.Width = 60
	renderPlanning(s, g, p)
	out := buf.String()
	if !strings.Contains(out, "feather/plan:roadmap-v1  (approved)") {
		t.Errorf("narrow terminal must show the compact state text:\n%s", out)
	}
	if strings.Contains(out, "planning-state") || strings.Contains(out, "phase mvp") {
		t.Errorf("narrow terminal must drop the planning-state/phase detail:\n%s", out)
	}
}

// TestRenderContainersNarrowCards: on a narrow terminal the containers
// projection swaps the aligned table for a stacked card list — every
// container keeps its full information; non-TTY output keeps the
// table.
func TestRenderContainersNarrowCards(t *testing.T) {
	s, buf, g := rendererTestContext(t)
	p := &view.ContainersProjection{
		Containers: []view.ContainerSummary{
			{Identity: "feather/ctr:wave-7", Type: "ctr", ID: "wave-7", Plan: "feather/plan:roadmap-v1", Items: 3, Tickets: 4, StartedAt: "2026-08-01", EndedAt: "", State: "active"},
		},
		Total:  1,
		Active: 1,
	}
	// Full layout (non-TTY, width 0): the aligned table.
	renderContainers(s, g, p, "", "")
	if !strings.Contains(buf.String(), "ITEMS/TICKETS") {
		t.Errorf("full layout must keep the table header:\n%s", buf.String())
	}
	// Narrow terminal: the card list keeps every field.
	buf.Reset()
	s.Width = 60
	renderContainers(s, g, p, "", "")
	out := buf.String()
	if strings.Contains(out, "ITEMS/TICKETS") {
		t.Errorf("narrow terminal must not render the table header:\n%s", out)
	}
	for _, want := range []string{
		"active", // the state word
		"wave-7", // the id
		"plan: feather/plan:roadmap-v1",
		"items/tickets: 3/4",
		"started: 2026-08-01",
		"ended: -",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("narrow terminal card must contain %q:\n%s", want, out)
		}
	}
}

// TestRenderContainersNarrowCardsTTY: on a color TTY the card header
// must not leak — the header is passed plain with its color function
// separate, so padding is computed on the plain text and the box
// stays closed even when ANSI escapes are applied.
func TestRenderContainersNarrowCardsTTY(t *testing.T) {
	s, buf, g := rendererTestContext(t)
	s.Color = true
	s.Width = 60
	p := &view.ContainersProjection{
		Containers: []view.ContainerSummary{
			{Identity: "feather/ctr:wave-7", Type: "ctr", ID: "wave-7", Plan: "feather/plan:roadmap-v1", Items: 3, Tickets: 4, StartedAt: "2026-08-01", EndedAt: "", State: "active"},
		},
		Total:  1,
		Active: 1,
	}
	renderContainers(s, g, p, "", "")
	out := buf.String()
	if !strings.Contains(out, "\x1b[") {
		t.Errorf("color-enabled containers cards must emit ANSI:\n%q", out)
	}
	ansi := regexp.MustCompile(`\x1b\[[0-9;]*m`)
	for _, line := range strings.Split(out, "\n") {
		plain := ansi.ReplaceAllString(line, "")
		if !strings.Contains(plain, "• wave-7") {
			continue
		}
		rest := plain[strings.Index(plain, "wave-7")+len("wave-7"):]
		if !strings.HasPrefix(rest, " ") {
			t.Errorf("colored card header must pad to the box width, got %q", line)
		}
	}
}
