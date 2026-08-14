package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/maleolabs/eka-core/runtime"
)

// viewFixtureAbs resolves the absolute path of a view test fixture.
func viewFixtureAbs(t *testing.T, name string) string {
	t.Helper()
	abs, err := filepath.Abs(filepath.Join("..", "testdata", "view", name))
	if err != nil {
		t.Fatal(err)
	}
	return abs
}

// seedViewRepo copies one view fixture into a fresh repo, seeds a fresh
// workspace (EKA_HOME) with it through the Runtime (the Authoring API —
// docs-mode pull + push — the store-backed setup of the projection
// path), and chdirs into the repo copy. Returns the repo path.
func seedViewRepo(t *testing.T, name string) string {
	t.Helper()
	t.Setenv("EKA_HOME", t.TempDir())
	repo := copyFixture(t, filepath.Join("..", "testdata", "view", name))
	r, err := runtime.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if _, err := runtime.Authoring.Sync(r, repo, runtime.SyncOptions{Pull: true, Push: true}); err != nil {
		t.Fatal(err)
	}
	chdirInto(t, repo)
	return repo
}

// openRuntime opens the Runtime of the current EKA_HOME and closes it
// on cleanup.
func openRuntime(t *testing.T) *runtime.Runtime {
	t.Helper()
	r, err := runtime.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	return r
}

// writeStory writes a minimal conformant work-item story doc (the
// same shape the sync fixtures use).
func writeStory(t *testing.T, path, ns, id, state string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nnamespace: " + ns + "\n"
	content += "type: sto\nid: " + id + "\n"
	content += "instance-version: 1\nrevision: 1\n"
	content += "execution-state: " + state + "\nexistence-state: active\n"
	content += "author: Engineering\ncreated: 2026-08-05\nupdated: 2026-08-05\n"
	content += "supersedes: []\nderives-from: []\ndepends-on: []\n"
	content += "change-log:\n"
	content += "  - date: 2026-08-05\n    domain: existence-state\n    from: \"-\"\n    to: active\n    by: Engineering\n"
	content += "  - date: 2026-08-05\n    domain: execution-state\n    from: \"-\"\n    to: " + state + "\n    by: Engineering\n"
	content += "---\n# " + id + "\n\n## Description\n\nd\n\n## Acceptance Criteria\n\nc\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestViewNoArgsListsProjections: `eka view` without arguments is a
// calm mini-landing listing the canonical projections and their
// aliases — exit 0.
func TestViewNoArgsListsProjections(t *testing.T) {
	code, out, errText := runIn([]string{"view"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstderr: %s", code, errText)
	}
	if errText != "" {
		t.Errorf("stderr must be empty, got %q", errText)
	}
	for _, want := range []string{
		"Knowledge Projections",
		"discovery", "architecture", "planning", "execution", "operations", "ticket",
		"Aliases", "sprint", "wave",
		"eka view ticket <tkt-id>",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("landing missing %q:\n%s", want, out)
		}
	}
	_, out2, _ := runIn([]string{"view"})
	if out != out2 {
		t.Error("view landing is not deterministic")
	}
}

// TestViewHelpExitsZero covers the help entry points.
func TestViewHelpExitsZero(t *testing.T) {
	for _, args := range [][]string{{"view", "-h"}, {"view", "--help"}} {
		code, text, _ := runIn(args)
		if code != 0 {
			t.Errorf("args %v: exit = %d, want 0", args, code)
		}
		for _, want := range []string{"eka view", "discovery", "architecture", "planning", "execution", "operations", "ticket", "sprint", "wave"} {
			if !strings.Contains(text, want) {
				t.Errorf("args %v: help missing %q:\n%s", args, want, text)
			}
		}
		if !strings.Contains(text, "EKA workspace") {
			t.Errorf("args %v: help must document the workspace canonical source:\n%s", args, text)
		}
	}
}

// TestViewBareArgumentFallsBackToTicket: a SINGLE argument that is not
// a registered projection resolves as a TICKET target (domain first,
// then the specific document) — the document diagnostic with the
// resolution hint, exit 2 when the target does not resolve.
func TestViewBareArgumentFallsBackToTicket(t *testing.T) {
	seedViewRepo(t, "valid")
	code, _, errText := runIn([]string{"view", "bogus"})
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(errText, "document projection: target \"bogus\" not found (resolve with <ns>/<type>:<id>, <type>:<id>, or a bare id)") {
		t.Errorf("stderr must carry the document diagnostic with the resolution hint, got %q", errText)
	}
}

// TestViewUnknownProjectionWithExtraArgsExitsTwo: an unregistered
// projection WITH extra arguments stays a usage error (the bare-argument
// fallback applies to a single argument only).
func TestViewUnknownProjectionWithExtraArgsExitsTwo(t *testing.T) {
	code, _, errText := runIn([]string{"view", "bogus", "extra"})
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(errText, "unknown projection \"bogus\"") {
		t.Errorf("stderr must name the projection, got %q", errText)
	}
	if !strings.Contains(errText, "available projections: architecture, board, containers, discovery, document, execution, operations, planning, ticket (aliases: sprint, wave)") {
		t.Errorf("stderr must list canonical projections and aliases, got %q", errText)
	}
}

// TestViewTicketBareArgumentFallbackResolves: `eka view <tkt-id>`
// resolves the ticket exactly like the explicit ticket projection.
func TestViewTicketBareArgumentFallbackResolves(t *testing.T) {
	seedViewRepo(t, "valid")
	code, text, errText := runIn([]string{"view", "tkt-bug-delta"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\n%s", code, errText)
	}
	if !strings.Contains(text, "eka-view-fixture/tkt:bug-delta") {
		t.Errorf("the ticket card must name the resolved ticket:\n%s", text)
	}
}

// TestViewTicketQualifiedTarget: the qualified <ns>/tkt:<id> form
// resolves the ticket (the rest of the CLI's reference grammar).
func TestViewTicketQualifiedTarget(t *testing.T) {
	seedViewRepo(t, "valid")
	code, text, errText := runIn([]string{"view", "ticket", "eka-view-fixture/tkt:bug-delta"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\n%s", code, errText)
	}
	if !strings.Contains(text, "eka-view-fixture/tkt:bug-delta") {
		t.Errorf("the ticket card must name the resolved ticket:\n%s", text)
	}
	// A qualified target in the WRONG namespace must not resolve.
	code, _, errText = runIn([]string{"view", "ticket", "other-ns/tkt:bug-delta"})
	if code != 2 || !strings.Contains(errText, "not found") {
		t.Errorf("wrong-namespace qualified target: exit = %d, %q; want 2 + not found", code, errText)
	}
}

// TestViewBareArgumentResolvesAnyDocument: the bare-argument fallback
// resolves a document of ANY canonical type — a work item here (the
// board's items are reachable by their bare id).
func TestViewBareArgumentResolvesAnyDocument(t *testing.T) {
	seedViewRepo(t, "valid")
	code, text, errText := runIn([]string{"view", "alpha"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\n%s", code, errText)
	}
	if !strings.Contains(text, "eka-view-fixture/sto:alpha") {
		t.Errorf("the document card must name the resolved story:\n%s", text)
	}
	if !strings.Contains(text, "Status") {
		t.Errorf("a work item document must carry its status:\n%s", text)
	}
}

// TestViewDocumentLicenseStyle: a top-level document (ADR) renders in
// the license-text style — the uppercase type · id heading and the
// content sections.
func TestViewDocumentLicenseStyle(t *testing.T) {
	seedViewRepo(t, "valid")
	code, text, errText := runIn([]string{"view", "document", "adr:001-login-serialization"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\n%s", code, errText)
	}
	if !strings.Contains(text, "ADR · 001-LOGIN-SERIALIZATION") {
		t.Errorf("the license heading must be the uppercase type · id:\n%s", text)
	}
	if !strings.Contains(text, "CONTEXT") || !strings.Contains(text, "DECISION") {
		t.Errorf("the license body must render the content sections:\n%s", text)
	}
}

// TestViewTicketWorkItem: the ticket projection resolves direct work
// items (board items) with their own state as the projected status.
func TestViewTicketWorkItem(t *testing.T) {
	seedViewRepo(t, "valid")
	code, text, errText := runIn([]string{"view", "ticket", "alpha"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\n%s", code, errText)
	}
	if !strings.Contains(text, "eka-view-fixture/sto:alpha") {
		t.Errorf("the ticket card must name the resolved work item:\n%s", text)
	}
}

// TestViewTicketMissingTargetExitsTwo: the ticket projection requires
// its target.
func TestViewTicketMissingTargetExitsTwo(t *testing.T) {
	code, _, errText := runIn([]string{"view", "ticket"})
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(errText, "requires a target") {
		t.Errorf("stderr must explain the requirement, got %q", errText)
	}
}

// TestViewTooManyArgsExitsTwo: at most one projection and one target.
func TestViewTooManyArgsExitsTwo(t *testing.T) {
	code, _, _ := runIn([]string{"view", "execution", "a", "b"})
	if code != 2 {
		t.Errorf("exit = %d, want 2", code)
	}
}

// TestViewExecutionHappyPath: the execution projection of a synced
// fixture — header, container line, the kanban board (column titles
// with counts, short work item ids, box borders) and the insight
// summary — exit 0. The projection is store-backed: the knowledge
// comes from the workspace canonical store seeded by the sync engine,
// not from Markdown.
func TestViewExecutionHappyPath(t *testing.T) {
	seedViewRepo(t, "valid")
	code, out, errText := runIn([]string{"view", "execution"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", code, out, errText)
	}
	for _, want := range []string{
		"Execution",
		"Container    eka-view-fixture/ctr:wave-1",
		"Repository   .",
		"Knowledge    EKA v1.0",
		"Domain       Execution",
		"↓ View",
		"• eka-view-fixture/ctr:wave-1  (active)",
		// The board: box borders, the five fixed columns with counts,
		// and the short ids of the work items.
		"┌",
		"┐",
		"│ Planned (1)",
		"│ Todo (1)",
		"│ In Progress (1)",
		"│ In Review (1)",
		"│ Done (1)",
		"│ ▸ #3 alpha",
		"│   [sto] · wave-1",
		"│   0 notes",
		"│ ▸ #4 beta",
		"│   [sto] · wave-1",
		"│   0 notes",
		"│ ▸ #6 gamma",
		"│   [ts] · wave-1",
		"│   0 notes",
		"│ ▸ #1 delta",
		"│   [bug] · wave-1",
		"│   1 notes",
		"│ ▸ #2 epsilon",
		"│   [ch] · wave-1",
		"│   1 notes",
		"8 tickets project these work items",
		// The insight summary: meaningful numbers, not raw columns.
		"Summary:",
		"Active Work: 2",
		"Completed Work: 1",
		"Review Queue: 1",
		"Overall Progress: ██░░░░░░░░ 1/5 (20%)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output must contain %q:\n%s", want, out)
		}
	}
}

// TestViewExecutionAliasesIdentical: the sprint and wave aliases render
// byte-identical output to the canonical execution projection.
func TestViewExecutionAliasesIdentical(t *testing.T) {
	seedViewRepo(t, "valid")
	runOnce := func(args ...string) string {
		_, out, _ := runIn(args)
		return out
	}
	execution := runOnce([]string{"view", "execution"}...)
	for _, alias := range []string{"sprint", "wave"} {
		if got := runOnce([]string{"view", alias}...); got != execution {
			t.Errorf("view %s output must be byte-identical to view execution", alias)
		}
	}
}

// TestViewMultipleActiveWarning: the multi-active container anomaly is
// surfaced at the CLI — the warning line names the deterministically
// chosen container (lexicographically smallest canonical identity) and
// the command still exits 0.
func TestViewMultipleActiveWarning(t *testing.T) {
	seedViewRepo(t, "multi-active")
	code, out, errText := runIn([]string{"view", "execution"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", code, out, errText)
	}
	if errText != "" {
		t.Errorf("stderr must be empty, got %q", errText)
	}
	for _, want := range []string{
		"Multiple active containers — showing eka-view-fixture/ctr:wave-1",
		"Container    eka-view-fixture/ctr:wave-1",
		"│ Planned (0)",
		"│ Done (0)",
		"—",
		"Active Work: 0",
		"Overall Progress: ░░░░░░░░░░ 0/0 (0%)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output must contain %q:\n%s", want, out)
		}
	}
	// Deterministic across runs, like every other projection.
	_, out2, _ := runIn([]string{"view", "execution"})
	if out != out2 {
		t.Error("multi-active execution output is not deterministic")
	}
}

// TestViewBoardHappyPath: the board projection — every work item of the
// fixture across both containers (wave-0 completed, wave-1 active), on
// the fixed five-column board with container tags.
func TestViewBoardHappyPath(t *testing.T) {
	seedViewRepo(t, "valid")
	code, out, errText := runIn([]string{"view", "board"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", code, out, errText)
	}
	for _, want := range []string{
		"Board",
		"Container    all",
		"Repository   .",
		"Knowledge    EKA v1.0",
		"Domain       Execution",
		"↓ View",
		"6 work items across 2 containers",
		"┌",
		"┐",
		"│ Planned (1)",
		"│ Todo (1)",
		"│ In Progress (1)",
		"│ In Review (1)",
		"│ Done (2)",
		"│ ▸ #3 alpha",
		"│   [sto] · wave-1",
		"│   0 notes",
		"│ ▸ #4 beta",
		"│   [sto] · wave-1",
		"│   0 notes",
		"│ ▸ #6 gamma",
		"│   [ts] · wave-1",
		"│   0 notes",
		"│ ▸ #1 delta",
		"│   [bug] · wave-1",
		"│   0 notes",
		"│ ▸ #2 epsilon",
		"│   [ch] · wave-1",
		"│   0 notes",
		"│ ▸ #5 legacy",
		"│   [sto] · wave-0",
		"│   0 notes",
		"Summary:",
		"Total Work Items: 6",
		"Active Work: 2",
		"Completed Work: 2",
		"Review Queue: 1",
		"Unassigned: 0",
		"Overall Progress: ███░░░░░░░ 2/6 (33%)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output must contain %q:\n%s", want, out)
		}
	}
}

// TestViewPlanningHappyPath: the planning projection — the roadmap by
// phase (mvp, release) with the milestone line, the scope/epic/plan
// timeline rows and the phase context, plus the plans-by-state summary.
func TestViewPlanningHappyPath(t *testing.T) {
	seedViewRepo(t, "valid")
	code, out, errText := runIn([]string{"view", "planning"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", code, out, errText)
	}
	for _, want := range []string{
		"Planning",
		"Domain       Planning",
		"✓ eka-view-fixture/plan:roadmap-2026  (approved, planning-state approved, phase release)",
		"──", // milestone separator
		"▸ eka-view-fixture/scp:wave-2  (approved, phase mvp)",
		"▸ eka-view-fixture/epc:auth  (review)",
		"▸ traceability: eka-view-fixture/trc:spec-trace (draft)",
		"Summary:",
		"Committed: 1",
		"Exploring: 0",
		"Next milestone: release",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output must contain %q:\n%s", want, out)
		}
	}
}

// TestViewArchitectureHappyPath: the architecture projection — the
// dependency tree rooted at the architecture description with the
// grouped subtrees, and the insight summary. The Decisions group merges
// adr-/dec- (including the superseded ADR).
func TestViewArchitectureHappyPath(t *testing.T) {
	seedViewRepo(t, "valid")
	code, out, errText := runIn([]string{"view", "architecture"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", code, out, errText)
	}
	for _, want := range []string{
		"Architecture",
		"Domain       Architecture",
		"eka-view-fixture/arc:system-architecture  (approved)",
		"├── Decisions",
		"│  ├── ✓ eka-view-fixture/adr:001-login-serialization  (accepted)",
		"│  ├── • eka-view-fixture/adr:002-session-encoding  (superseded)",
		"│  ├── ✓ eka-view-fixture/adr:003-token-format  (accepted)",
		"│  └── ✓ eka-view-fixture/dec:001-api-shape  (accepted)",
		"├── Specifications",
		"│  └── ○ eka-view-fixture/spec:auth-flow  (draft)",
		"├── Standards & Guidelines",
		"│  └── • eka-view-fixture/std:gofmt  (review)",
		"└── Vocabulary",
		"   └── • eka-view-fixture/gls:domain-terms  (amended)",
		"Summary:",
		"Accepted decisions: 3",
		"Open items: 0",
		"Superseded: 1",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output must contain %q:\n%s", want, out)
		}
	}
}

// TestViewDiscoveryHappyPath: the discovery projection — one boxed card
// per artifact under its group heading, drafts visually distinct (○),
// and the insight summary.
func TestViewDiscoveryHappyPath(t *testing.T) {
	seedViewRepo(t, "valid")
	code, out, errText := runIn([]string{"view", "discovery"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", code, out, errText)
	}
	for _, want := range []string{
		"Discovery",
		"Domain       Discovery",
		"Vision",
		"┌",
		"│ ○ eka-view-fixture/vis:product-vision │",
		"│ draft · revision 1",
		"Strategy",
		"│ • eka-view-fixture/str:go-to-market │",
		"│ review · revision 1",
		"Requirements",
		"│ ✓ eka-view-fixture/req:onboarding │",
		"│ approved · revision 1",
		"Research Findings",
		"│ ✓ eka-view-fixture/fnd:market-research │",
		"Summary:",
		"Committed direction: 2",
		"Exploring: 1",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output must contain %q:\n%s", want, out)
		}
	}
}

// TestViewOperationsHappyPath: the operations projection — the release
// record card and the runbook activity timeline, with the insight
// summary.
func TestViewOperationsHappyPath(t *testing.T) {
	seedViewRepo(t, "valid")
	code, out, errText := runIn([]string{"view", "operations"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", code, out, errText)
	}
	for _, want := range []string{
		"Operations",
		"Domain       Operations",
		"Release Records",
		"┌",
		"│ • eka-view-fixture/rel:release-1 │",
		"│ review",
		"Runbooks",
		"▸ eka-view-fixture/run:deploy  (approved)",
		"Summary:",
		"Releases delivered: 0",
		"Runbooks maintained: 1",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output must contain %q:\n%s", want, out)
		}
	}
}

// TestViewTicketHappyPath: the ticket projection derives the projected
// status from the work item's owner state; the status leads the detail
// card.
func TestViewTicketHappyPath(t *testing.T) {
	seedViewRepo(t, "valid")
	code, out, errText := runIn([]string{"view", "ticket", "tkt-ts-gamma"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", code, out, errText)
	}
	for _, want := range []string{
		"Ticket",
		"Ticket       eka-view-fixture/tkt:ts-gamma",
		"Domain       Execution",
		"Projected Status  → in-progress",
		"┌",
		"│ eka-view-fixture/tkt:ts-gamma",
		"│ Work Item      eka-view-fixture/ts:gamma (in-progress)",
		"│ Container      eka-view-fixture/ctr:wave-1",
		"│ Derives From   ctr:wave-1, ts:gamma",
		"└",
		"Projected status: in-progress",
		"Work item: eka-view-fixture/ts:gamma (in-progress)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output must contain %q:\n%s", want, out)
		}
	}
}

// TestViewTicketBareID: a bare ticket id resolves like tkt-<id>.
func TestViewTicketBareID(t *testing.T) {
	seedViewRepo(t, "valid")
	code, out, _ := runIn([]string{"view", "ticket", "ts-gamma"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\n%s", code, out)
	}
	if !strings.Contains(out, "eka-view-fixture/tkt:ts-gamma") {
		t.Errorf("bare id must resolve to the same ticket:\n%s", out)
	}
}

// TestViewTicketUnresolved: a ticket without a resolvable work item
// renders an explicit unresolved status — exit 0.
func TestViewTicketUnresolved(t *testing.T) {
	seedViewRepo(t, "valid")
	code, out, _ := runIn([]string{"view", "ticket", "tkt-unresolved"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\n%s", code, out)
	}
	for _, want := range []string{
		"Projected Status  • unresolved",
		"│ Work Item      unresolved",
		"│ Container      eka-view-fixture/ctr:wave-1",
		"│ Derives From   ctr:wave-1",
		"Projected status: unresolved",
		"Work item: unresolved",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output must contain %q:\n%s", want, out)
		}
	}
}

// TestViewTicketNotFoundExitsTwo: an unknown ticket target is a usage
// error with the available tickets.
func TestViewTicketNotFoundExitsTwo(t *testing.T) {
	seedViewRepo(t, "valid")
	code, _, errText := runIn([]string{"view", "ticket", "tkt-ghost"})
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(errText, "target \"tkt-ghost\" not found") {
		t.Errorf("stderr must explain the missing target, got %q", errText)
	}
	if !strings.Contains(errText, "available tickets: bug-delta, ch-epsilon, sto-alpha, sto-alpha-dup, sto-beta, sto-beta-multi, sto-legacy, ts-gamma, unresolved") {
		t.Errorf("stderr must list the available tickets, got %q", errText)
	}
}

// TestViewUnregisteredRepoExitsOne: the repository-state gate runs
// first. Two refusal classes (ADR-018): a directory without eka.yaml
// is not an EKA repository — refused with the pinned gate message;
// a metadata repository that is not registered is refused with the
// existing byte-identical message. Both exit 1, no projection.
func TestViewUnregisteredRepoExitsOne(t *testing.T) {
	t.Setenv("EKA_HOME", t.TempDir())
	chdirInto(t, t.TempDir())
	code, out, errText := runIn([]string{"view", "execution"})
	if code != 1 {
		t.Fatalf("exit = %d, want 1\nstdout: %s\nstderr: %s", code, out, errText)
	}
	if out != "" {
		t.Errorf("stdout must be empty (no projection), got %q", out)
	}
	if !strings.Contains(errText, "is not an EKA repository (no eka.yaml)") ||
		!strings.Contains(errText, "run 'eka init' first") {
		t.Errorf("stderr must carry the pinned ADR-018 refusal, got %q", errText)
	}

	// A metadata repository that is not registered keeps the existing
	// refusal message (byte-identical, ADR-018 §8).
	meta := t.TempDir()
	writeEkaYAML(t, meta, filepath.Base(meta), filepath.Base(meta), "eka-view-fixture")
	chdirInto(t, meta)
	code, out, errText = runIn([]string{"view", "execution"})
	if code != 1 {
		t.Fatalf("unregistered metadata repo: exit = %d, want 1\nstdout: %s\nstderr: %s", code, out, errText)
	}
	if out != "" {
		t.Errorf("stdout must be empty (no projection), got %q", out)
	}
	if !strings.Contains(errText, "eka: view refused: repository") {
		t.Errorf("stderr must carry the refusal, got %q", errText)
	}
	if !strings.Contains(errText, "not registered in the EKA workspace") {
		t.Errorf("stderr must explain the refusal, got %q", errText)
	}
	if !strings.Contains(errText, "eka sync") {
		t.Errorf("stderr must hint at 'eka sync', got %q", errText)
	}
	if !strings.Contains(errText, "eka project register") {
		t.Errorf("stderr must hint at 'eka project register', got %q", errText)
	}
}

// TestViewNoSyncedKnowledgeExitsZero: a registered but never-synced
// repository renders the empty projection with the informational note —
// exit 0, consistent with the existing empty-projection behavior. The
// tree carries eka.yaml (an EKA repository), so the context gate
// passes; the registration is the legacy-shaped identity (project/name
// = basename) matched by the file.
func TestViewNoSyncedKnowledgeExitsZero(t *testing.T) {
	t.Setenv("EKA_HOME", t.TempDir())
	repo := t.TempDir()
	writeEkaYAML(t, repo, filepath.Base(repo), filepath.Base(repo), "eka-view-fixture")
	r, err := runtime.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if _, _, _, err := r.Workspace.RegisterRepo(repo, ""); err != nil {
		t.Fatal(err)
	}
	chdirInto(t, repo)
	code, out, errText := runIn([]string{"view", "execution"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", code, out, errText)
	}
	if !strings.Contains(out, "no synced knowledge for project") {
		t.Errorf("output must carry the no-synced-knowledge note:\n%s", out)
	}
	for _, want := range []string{
		"No active container.",
		"│ Planned (0)",
		"—",
		"Active Work: 0",
		"Overall Progress: ░░░░░░░░░░ 0/0 (0%)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output must contain %q:\n%s", want, out)
		}
	}
}

// TestViewEmptyDomainExitsZero: a registered repository without synced
// knowledge renders a calm "No <Domain> artifacts." line per domain and
// still exits 0.
func TestViewEmptyDomainExitsZero(t *testing.T) {
	t.Setenv("EKA_HOME", t.TempDir())
	repo := t.TempDir()
	writeEkaYAML(t, repo, filepath.Base(repo), filepath.Base(repo), "eka-view-fixture")
	r, err := runtime.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if _, _, _, err := r.Workspace.RegisterRepo(repo, ""); err != nil {
		t.Fatal(err)
	}
	chdirInto(t, repo)
	for domain, want := range map[string]string{
		"planning":     "No Planning artifacts.",
		"architecture": "No Architecture artifacts.",
		"discovery":    "No Discovery artifacts.",
		"operations":   "No Operations artifacts.",
	} {
		code, out, errText := runIn([]string{"view", domain})
		if code != 0 {
			t.Fatalf("view %s: exit = %d, want 0\nstdout: %s\nstderr: %s", domain, code, out, errText)
		}
		if !strings.Contains(out, want) {
			t.Errorf("view %s must render %q:\n%s", domain, want, out)
		}
		// The summary block follows the calm line; assert its shape.
		if !strings.Contains(out, "Summary:") {
			t.Errorf("view %s must still render the summary:\n%s", domain, out)
		}
	}
}

// TestViewMultiRepoProjectUnion: two repositories of one project with
// distinct namespaces — the projection covers the whole project: the
// board viewed from repository A shows the work items of BOTH
// repositories (the complete Engineering Knowledge of the project).
func TestViewMultiRepoProjectUnion(t *testing.T) {
	t.Setenv("EKA_HOME", t.TempDir())
	repoA := t.TempDir()
	repoB := t.TempDir()
	writeStory(t, filepath.Join(repoA, "docs", "operating", "work-items", "stories", "sto-alpha.md"), "union-a", "alpha", "todo")
	writeStory(t, filepath.Join(repoB, "docs", "operating", "work-items", "stories", "sto-beta.md"), "union-b", "beta", "done")
	// Both are EKA repositories under the same project "union"; the
	// metadata name = basename, namespace = the content namespace.
	writeEkaYAML(t, repoA, "union", filepath.Base(repoA), "union-a")
	writeEkaYAML(t, repoB, "union", filepath.Base(repoB), "union-b")

	r, err := runtime.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	for _, repo := range []string{repoA, repoB} {
		if _, _, _, err := r.Workspace.RegisterRepo(repo, "union"); err != nil {
			t.Fatal(err)
		}
		if _, err := runtime.Authoring.Sync(r, repo, runtime.SyncOptions{Pull: true, Push: true}); err != nil {
			t.Fatal(err)
		}
	}

	chdirInto(t, repoA)
	code, out, errText := runIn([]string{"view", "board"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", code, out, errText)
	}
	for _, want := range []string{
		"Board",
		"2 work items across 0 containers",
		"│ ▸ #1 alpha",
		"│   [sto] · unassigned",
		"│ ▸ #2 beta",
		"│   [sto] · unassigned",
		"Summary:",
		"Total Work Items: 2",
		"Unassigned: 2",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("union board must contain %q:\n%s", want, out)
		}
	}
	// The projection source is the project, not the repository: the
	// board viewed from repository A carries repository B's item too.
	if strings.Contains(out, "no synced knowledge") {
		t.Errorf("union board must not carry the no-synced-knowledge note:\n%s", out)
	}
}

// TestViewDeterministicCLI: two runs of each projection produce
// byte-identical output.
func TestViewDeterministicCLI(t *testing.T) {
	seedViewRepo(t, "valid")
	runOnce := func(args ...string) string {
		_, out, _ := runIn(args)
		return out
	}
	for _, args := range [][]string{
		{"view"},
		{"view", "discovery"},
		{"view", "architecture"},
		{"view", "planning"},
		{"view", "execution"},
		{"view", "operations"},
		{"view", "ticket", "tkt-ts-gamma"},
		{"view", "ticket", "tkt-unresolved"},
		{"view", "sprint"},
		{"view", "wave"},
	} {
		if a, b := runOnce(args...), runOnce(args...); a != b {
			t.Errorf("output differs between runs for %v", args)
		}
	}
}

// TestViewNoANSIEscapesInNonTTYOutput verifies the determinism
// contract for the view command: non-TTY output carries no ANSI
// escapes.
func TestViewNoANSIEscapesInNonTTYOutput(t *testing.T) {
	seedViewRepo(t, "valid")
	for _, args := range [][]string{
		{"view"},
		{"view", "discovery"},
		{"view", "architecture"},
		{"view", "planning"},
		{"view", "execution"},
		{"view", "operations"},
		{"view", "ticket", "tkt-ts-gamma"},
		{"view", "sprint"},
		{"view", "wave"},
	} {
		var out, errb bytes.Buffer
		code := Execute(args, strings.NewReader(""), &out, &errb)
		if strings.Contains(out.String(), "\x1b") || strings.Contains(errb.String(), "\x1b") {
			t.Errorf("%v: non-TTY output must not contain ANSI escapes:\nstdout: %q\nstderr: %q",
				args, out.String(), errb.String())
		}
		if code != 0 {
			t.Errorf("%v: exit = %d, want 0", args, code)
		}
	}
}

// TestViewContainersHappyPath: the containers projection — the aligned
// table with name, plan, items/tickets, started/ended and the colored
// status, plus the insight line. Non-TTY output is plain text (no
// ANSI), deterministic.
func TestViewContainersHappyPath(t *testing.T) {
	seedViewRepo(t, "valid")
	code, out, errText := runIn([]string{"view", "containers"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", code, out, errText)
	}
	if errText != "" {
		t.Errorf("stderr must be empty, got %q", errText)
	}
	for _, want := range []string{
		"Containers",
		"Repository   .",
		"Knowledge    EKA v1.0",
		"Domain       Execution",
		"↓ View",
		"NAME    PLAN  ITEMS/TICKETS  STARTED     ENDED  STATUS",
		"wave-0  -     1/1            2026-08-05  -      • completed",
		"wave-1  -     5/8            2026-08-05  -      • active",
		"2 containers · 1 active",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output must contain %q:\n%s", want, out)
		}
	}
	// The table underline is the unicode box-drawing line (IconLine),
	// never the ASCII dash — the design system's line glyphs.
	if !strings.Contains(out, "──") {
		t.Errorf("the table underline must use the unicode line:\n%s", out)
	}
	if strings.Contains(out, "--") {
		t.Errorf("the table underline must not use ASCII dashes:\n%s", out)
	}
	if strings.Contains(out, "\x1b") {
		t.Errorf("non-TTY output must not contain ANSI escapes:\n%s", out)
	}
}

// TestViewContainersEmpty: a registered repository without synced
// knowledge renders the calm "No containers." line — exit 0.
func TestViewContainersEmpty(t *testing.T) {
	t.Setenv("EKA_HOME", t.TempDir())
	repo := t.TempDir()
	writeEkaYAML(t, repo, filepath.Base(repo), filepath.Base(repo), "eka-view-fixture")
	r, err := runtime.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if _, _, _, err := r.Workspace.RegisterRepo(repo, ""); err != nil {
		t.Fatal(err)
	}
	chdirInto(t, repo)
	code, out, errText := runIn([]string{"view", "containers"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", code, out, errText)
	}
	if !strings.Contains(out, "No containers.") {
		t.Errorf("empty projection must render the calm line:\n%s", out)
	}
	if strings.Contains(out, "NAME") {
		t.Errorf("an empty projection must not render the table:\n%s", out)
	}
}

// TestViewContainersActive: --active keeps only the active containers;
// the filter note and the full totals (total · active) stay visible.
func TestViewContainersActive(t *testing.T) {
	seedViewRepo(t, "valid")
	code, out, errText := runIn([]string{"view", "containers", "--active"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", code, out, errText)
	}
	for _, want := range []string{
		"active only",
		"wave-1  -     5/8            2026-08-05  -      • active",
		"2 containers · 1 active",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output must contain %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "wave-0") {
		t.Errorf("--active must exclude the completed container:\n%s", out)
	}
}

// TestViewContainersContainerFilter: --container keeps the single
// match (bare id, ctr-<id>, ctr:<id> and qualified); an unknown
// container is a usage error (exit 2) listing the available forms.
func TestViewContainersContainerFilter(t *testing.T) {
	seedViewRepo(t, "valid")
	for _, target := range []string{"wave-0", "ctr-wave-0", "ctr:wave-0", "eka-view-fixture/ctr:wave-0"} {
		code, out, errText := runIn([]string{"view", "containers", "--container", target})
		if code != 0 {
			t.Fatalf("--container %s: exit = %d, want 0\nstdout: %s\nstderr: %s", target, code, out, errText)
		}
		if !strings.Contains(out, "container wave-0") {
			t.Errorf("--container %s must render the filter note:\n%s", target, out)
		}
		if strings.Contains(out, "wave-1") {
			t.Errorf("--container %s must keep only wave-0:\n%s", target, out)
		}
	}
	code, _, errText := runIn([]string{"view", "containers", "--container", "wave-6"})
	if code != 2 {
		t.Fatalf("unknown container: exit = %d, want 2", code)
	}
	if !strings.Contains(errText, `view: container "wave-6" not found`) {
		t.Errorf("stderr must name the missing container, got %q", errText)
	}
	if !strings.Contains(errText, "available containers: eka-view-fixture/ctr:wave-0, eka-view-fixture/ctr:wave-1") {
		t.Errorf("stderr must list the available forms, got %q", errText)
	}
}

// TestViewContainersPagination: --limit windows the table and renders
// the pagination footer; the totals stay the full population.
func TestViewContainersPagination(t *testing.T) {
	seedViewRepo(t, "valid")
	code, out, errText := runIn([]string{"view", "containers", "--limit", "1"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", code, out, errText)
	}
	for _, want := range []string{
		"wave-0  -     1/1            2026-08-05  -      • completed",
		"Page 1/2 · containers 1–1 of 2",
		"2 containers · 1 active",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output must contain %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "wave-1") {
		t.Errorf("--limit 1 must window to the first container only:\n%s", out)
	}
	// Page 2: the second container.
	code, out, _ = runIn([]string{"view", "containers", "--limit", "1", "--page", "2"})
	if !strings.Contains(out, "wave-1  -     5/8") || !strings.Contains(out, "Page 2/2 · containers 2–2 of 2") {
		t.Errorf("page 2 must show wave-1 with its footer:\n%s", out)
	}
}

// TestViewBoardPagination: --limit windows every column (with the dim
// "… N more" card when a column overflows) and renders the paged
// footer; the full totals stay on the untouched projection.
func TestViewBoardPagination(t *testing.T) {
	seedViewRepo(t, "valid")
	code, out, errText := runIn([]string{"view", "board", "--limit", "1"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", code, out, errText)
	}
	for _, want := range []string{
		"│ Done (2)", // the full column count (the more-card keeps it)
		"│ ▸ #5 legacy",
		"│ ▸ … 1 more",
		"6 work items across 2 containers", // the full scope line
		"Total Work Items: 6",              // the full insight totals
		"Paged: 1 per column · page 1 (offset 0)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output must contain %q:\n%s", want, out)
		}
	}
	// Page 2: offset 1 — every single-item column empties.
	code, out, _ = runIn([]string{"view", "board", "--limit", "1", "--page", "2"})
	if !strings.Contains(out, "Paged: 1 per column · page 2 (offset 1)") {
		t.Errorf("page 2 must carry its footer:\n%s", out)
	}
	// The unpaged board stays byte-identical to the pre-pagination
	// rendering (no footer).
	_, plain, _ := runIn([]string{"view", "board"})
	if strings.Contains(plain, "Paged:") {
		t.Errorf("unpaged board must not render the paged footer:\n%s", plain)
	}
}

// TestViewRetrievalFlagErrors: the board/containers retrieval flags
// are applicability-validated — usage errors, exit 2.
func TestViewRetrievalFlagErrors(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"view", "board", "--active"}, "the board projection supports --offset/--limit/--page only"},
		{[]string{"view", "board", "--container", "wave-0"}, "the board projection supports --offset/--limit/--page only"},
		{[]string{"view", "execution", "--limit", "2"}, "the execution projection does not support these flags (board: pagination; containers: pagination and filters)"},
		{[]string{"view", "ticket", "tkt-ts-gamma", "--active"}, "the ticket projection does not support these flags (board: pagination; containers: pagination and filters)"},
	}
	for _, c := range cases {
		code, out, errText := runIn(c.args)
		if code != 2 {
			t.Errorf("%v: exit = %d, want 2\nstdout: %s", c.args, code, out)
			continue
		}
		if out != "" {
			t.Errorf("%v: stdout must be empty, got %q", c.args, out)
		}
		if !strings.Contains(errText, c.want) {
			t.Errorf("%v: stderr must carry %q, got %q", c.args, c.want, errText)
		}
	}
	// The flag errors are pure flag/projection validation — they fire
	// before any workspace access.
	t.Setenv("EKA_HOME", t.TempDir())
	chdirInto(t, t.TempDir())
	code, _, errText := runIn([]string{"view", "board", "--active"})
	if code != 2 || !strings.Contains(errText, "supports --offset/--limit/--page only") {
		t.Errorf("board --active without a workspace: exit = %d, %q; want 2 + the usage error", code, errText)
	}
}

// TestViewContainersDeterministicCLI: two runs of every containers and
// paged-board query produce byte-identical output.
func TestViewContainersDeterministicCLI(t *testing.T) {
	seedViewRepo(t, "valid")
	runOnce := func(args ...string) string {
		_, out, _ := runIn(args)
		return out
	}
	for _, args := range [][]string{
		{"view", "containers"},
		{"view", "containers", "--active"},
		{"view", "containers", "--container", "wave-0"},
		{"view", "containers", "--limit", "1"},
		{"view", "containers", "--limit", "1", "--page", "2"},
		{"view", "board", "--limit", "1"},
		{"view", "board", "--limit", "2", "--page", "2"},
	} {
		if a, b := runOnce(args...), runOnce(args...); a != b {
			t.Errorf("output differs between runs for %v", args)
		}
	}
}
