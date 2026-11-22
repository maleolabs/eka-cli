package bootstrap

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/maleolabs/eka-core/exchange"
)

// testBlock builds the managed block for a test, discarding the omitted
// count (tests that care read it from BuildAgentsMDBlock directly).
func testBlock(project, namespace string, index []IndexEntry) []byte {
	b, _ := BuildAgentsMDBlock(project, namespace, index)
	return b
}

// TestBuildAgentsMDBlock pins the identity-only managed block (nil
// index): markers, the identity line (no volatile execution state — no
// "Active ctr"), the working loop, read/change verbs, hard rules and
// the skill routing pointer — the minimum viable operating picture per
// session. The constraint index heading is OMITTED without an index.
func TestBuildAgentsMDBlock(t *testing.T) {
	b := string(testBlock("atrium", "atrium-api", nil))
	for _, want := range []string{
		agentsMDMarkerStart,
		agentsMDMarkerEnd,
		"Project: atrium | Namespace: atrium-api | Position: `eka status`",
		"Understand -> Context -> Reason",
		"eka status",
		"eka get <ns>/<type>:<id>",
		"eka context <subject>",
		"eka transition <line> <to>",
		"eka sync push",
		"eka-router",
		"eka-engineering-workflow",
		"eka-troubleshooting",
	} {
		if !strings.Contains(b, want) {
			t.Errorf("block must contain %q:\n%s", want, b)
		}
	}
	if strings.Contains(b, "Active ctr") {
		t.Errorf("identity line must carry no volatile execution state:\n%s", b)
	}
	if strings.Contains(b, agentsMDConstraintsHeading) {
		t.Errorf("no index must omit the constraint heading:\n%s", b)
	}
	if !strings.HasSuffix(b, agentsMDMarkerEnd) {
		t.Errorf("block must end at the end marker (no trailing newline — merge owns separation):\n%s", b)
	}
	// Token economy: the identity-only block must stay compact.
	if lines := strings.Count(b, "\n"); lines > 16 {
		t.Errorf("block = %d lines, want ≤ 16 (token budget)", lines)
	}
}

// TestBuildAgentsMDBlockIndex: a non-empty index renders the heading and
// one "type: id id ..." line per group, in the supplied order, within
// the byte budget.
func TestBuildAgentsMDBlockIndex(t *testing.T) {
	index := []IndexEntry{
		{Type: "fnd", IDs: []string{"a", "b"}},
		{Type: "adr", IDs: []string{"001-x"}},
	}
	b := string(testBlock("p", "n", index))
	if !strings.Contains(b, agentsMDConstraintsHeading) {
		t.Errorf("non-empty index must carry the constraint heading:\n%s", b)
	}
	if !strings.Contains(b, "fnd: a b\n") || !strings.Contains(b, "adr: 001-x\n") {
		t.Errorf("index must render one sorted line per type:\n%s", b)
	}
	if len(b) > AgentsMDBlockBudget {
		t.Errorf("block = %d bytes, want ≤ %d", len(b), AgentsMDBlockBudget)
	}
	// Determinism: same index → identical bytes.
	if b2 := string(testBlock("p", "n", index)); b2 != b {
		t.Errorf("block must be deterministic:\n%s\n%s", b, b2)
	}
}

// TestBuildAgentsMDBlockBounded: an oversized index truncates
// deterministically and the block stays within the byte budget (assert
// the byte count, not a line count).
func TestBuildAgentsMDBlockBounded(t *testing.T) {
	var index []IndexEntry
	for _, typ := range []string{"fnd", "req", "str", "vis", "adr", "dec", "spec", "std", "arc", "gls"} {
		var ids []string
		for i := 0; i < 200; i++ {
			ids = append(ids, fmt.Sprintf("%03d-a-fairly-long-constraint-identifier-%d", i, i))
		}
		index = append(index, IndexEntry{Type: typ, IDs: ids})
	}
	b, omitted := BuildAgentsMDBlock("p", "n", index)
	if len(b) > AgentsMDBlockBudget {
		t.Fatalf("block = %d bytes, want ≤ %d", len(b), AgentsMDBlockBudget)
	}
	if omitted == 0 {
		t.Fatalf("oversized index must report omitted entries, got 0")
	}
	s := string(b)
	if !strings.Contains(s, AgentsMDTruncatedPrefix) || !strings.Contains(s, "more (eka status)") {
		t.Errorf("oversized index must carry the truncation line:\n%s", s)
	}
	// Determinism: same oversized index → identical bytes.
	if s2, o2 := BuildAgentsMDBlock("p", "n", index); string(s2) != s || o2 != omitted {
		t.Errorf("truncated block must be deterministic (omitted %d vs %d)", omitted, o2)
	}
}

// TestBuildAgentsMDBlockOverlongIdentity: a pathological identity leaves
// no room for the index, so the block renders NO heading and NO dangling
// truncation line (the index is omitted entirely, identifying-only). The
// budget binds the INDEX, not the fixed prose+identity (EKA identifiers
// are uncapped — metadata.ValidIdent imposes no length limit).
func TestBuildAgentsMDBlockOverlongIdentity(t *testing.T) {
	index := []IndexEntry{{Type: "fnd", IDs: []string{"a", "b", "c"}}}
	long := strings.Repeat("x", AgentsMDBlockBudget)
	b := string(testBlock(long, long, index))
	if strings.Contains(b, agentsMDConstraintsHeading) {
		t.Errorf("over-long identity must omit the constraint heading:\n%s", b)
	}
	if strings.Contains(b, AgentsMDTruncatedPrefix) {
		t.Errorf("over-long identity must not emit a dangling truncation line:\n%s", b)
	}
	if !strings.HasSuffix(b, agentsMDMarkerEnd) {
		t.Errorf("block must still end at the end marker:\n%s", b)
	}
}

// TestMergeAgentsMDMissing: no existing content yields the block plus
// one trailing newline.
func TestMergeAgentsMDMissing(t *testing.T) {
	block := testBlock("p", "n", nil)
	if got := MergeAgentsMD(nil, block); string(got) != string(block)+"\n" {
		t.Errorf("missing file must yield the block plus newline, got %q", got)
	}
}

// TestMergeAgentsMDCurrent: an identical file reconciles to nil
// (reuse — nothing to write).
func TestMergeAgentsMDCurrent(t *testing.T) {
	block := testBlock("p", "n", nil)
	current := append(append([]byte(nil), block...), '\n')
	if got := MergeAgentsMD(current, block); got != nil {
		t.Errorf("current content must reconcile to nil, got %q", got)
	}
}

// TestMergeAgentsMDDrift: a stale marked region is replaced while user
// content outside the markers is preserved byte-identical.
func TestMergeAgentsMDDrift(t *testing.T) {
	old := "# My notes\n\n" + agentsMDMarkerStart + "\nSTALE\n" + agentsMDMarkerEnd + "\n\ntail\n"
	merged := MergeAgentsMD([]byte(old), testBlock("p", "n", nil))
	if merged == nil {
		t.Fatal("drifted block must reconcile to new bytes")
	}
	s := string(merged)
	if strings.Contains(s, "STALE") {
		t.Errorf("stale region must be replaced:\n%s", s)
	}
	for _, want := range []string{"# My notes", "tail", "Project: p | Namespace: n"} {
		if !strings.Contains(s, want) {
			t.Errorf("merge must preserve %q:\n%s", want, s)
		}
	}
	// Reconciling twice is a fixed point.
	if again := MergeAgentsMD(merged, testBlock("p", "n", nil)); again != nil {
		t.Errorf("merge must be idempotent, second pass wrote %q", again)
	}
}

// TestMergeAgentsMDForeign: unmarked files gain the block appended
// after a blank line, existing bytes untouched.
func TestMergeAgentsMDForeign(t *testing.T) {
	existing := "# hello\n"
	merged := MergeAgentsMD([]byte(existing), testBlock("p", "n", nil))
	if merged == nil {
		t.Fatal("unmarked file must gain the block")
	}
	s := string(merged)
	if !strings.HasPrefix(s, "# hello\n\n"+agentsMDMarkerStart) {
		t.Errorf("block must append after a blank line:\n%s", s)
	}
}

// TestPlanAgentsMDSkipped: without opt-in the plan never mentions
// AGENTS.md.
func TestPlanAgentsMDSkipped(t *testing.T) {
	dir := t.TempDir()
	d := &Discovery{AbsTarget: dir, BaseName: "x"}
	for _, a := range BuildPlan(dir, d, Answers{Project: "x", Namespace: "x"}) {
		if a.Path == AgentsMDName {
			t.Errorf("plan must not touch AGENTS.md without opt-in: %v", a)
		}
	}
}

// TestPlanAgentsMDMissing: opt-in on a fresh target plans the merge.
func TestPlanAgentsMDMissing(t *testing.T) {
	dir := t.TempDir()
	d := &Discovery{AbsTarget: dir, BaseName: "x"}
	var found bool
	for _, a := range BuildPlan(dir, d, Answers{Project: "x", Namespace: "x", AgentsMD: true}) {
		if a.Path == AgentsMDName {
			found = true
			if a.Kind != ActionAgentsMDMerge {
				t.Errorf("kind = %q, want agents-md-merge", a.Kind)
			}
			if !strings.Contains(string(a.Content), "Project: x") {
				t.Errorf("plan content must carry the identity:\n%s", a.Content)
			}
		}
	}
	if !found {
		t.Error("opt-in plan must contain an AGENTS.md step")
	}
}

// TestPlanAgentsMDCurrent: a current file plans reuse.
func TestPlanAgentsMDRuse(t *testing.T) {
	dir := t.TempDir()
	block := testBlock("x", "x", nil)
	current := append(append([]byte(nil), block...), '\n')
	if err := os.WriteFile(filepath.Join(dir, AgentsMDName), current, 0o644); err != nil {
		t.Fatal(err)
	}
	d := &Discovery{AbsTarget: dir, BaseName: "x"}
	for _, a := range BuildPlan(dir, d, Answers{Project: "x", Namespace: "x", AgentsMD: true}) {
		if a.Path == AgentsMDName && a.Kind != ActionReuse {
			t.Errorf("current file must plan reuse, got %v", a)
		}
	}
}

// TestAskAgentsMDConfirm: the wizard asks last, defaulting to yes;
// "n" opts out.
func TestAskAgentsMDConfirm(t *testing.T) {
	d := &Discovery{BaseName: "x", HasReadme: true, IsGitRepo: true, GitAvailable: true}
	a, err := Ask(d, strings.NewReader("x\nx\nn\n"), &strings.Builder{}, PreAnswers{})
	if err != nil {
		t.Fatal(err)
	}
	if a.AgentsMD {
		t.Error("explicit n must opt out of AGENTS.md")
	}
	b, err := Ask(d, strings.NewReader("x\nx\n\n"), &strings.Builder{}, PreAnswers{})
	if err != nil {
		t.Fatal(err)
	}
	if !b.AgentsMD {
		t.Error("empty answer must default to managing AGENTS.md")
	}
}

// TestAskAgentsMDPreset: the flag preset skips the question entirely.
func TestAskAgentsMDPreset(t *testing.T) {
	d := &Discovery{BaseName: "x", HasReadme: true, IsGitRepo: true, GitAvailable: true}
	no := false
	var out strings.Builder
	a, err := Ask(d, strings.NewReader("x\nx\n"), &out, PreAnswers{AgentsMD: &no})
	if err != nil {
		t.Fatal(err)
	}
	if a.AgentsMD {
		t.Error("preset false must win")
	}
	if strings.Contains(out.String(), "AGENTS.md") {
		t.Errorf("preset question must be skipped:\n%s", out.String())
	}
}

// TestRunRerunBackfillsAgentsMD: re-running init on an
// already-initialized repository manages AGENTS.md by default (missing
// files backfilled) even with no flags — the wizard is skipped there,
// so the opt-in cannot come from a question.
func TestRunRerunBackfillsAgentsMD(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "eka.yaml"), []byte("version: 1\nproject: p\nname: d\nnamespace: p\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if _, err := Run(Options{
		Target: dir,
		Stdin:  strings.NewReader(""),
		Stdout: &out,
		Stderr: &errb,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, AgentsMDName))
	if err != nil {
		t.Fatalf("re-run must backfill AGENTS.md: %v", err)
	}
	if !strings.Contains(string(got), "Project: p | Namespace: p") {
		t.Errorf("backfilled block must carry the eka.yaml identity:\n%s", got)
	}
}

// TestRunRerunNoAgentsMDOptsOut: --no-agents-md leaves AGENTS.md alone
// on re-runs.
func TestRunRerunNoAgentsMDOptsOut(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "eka.yaml"), []byte("version: 1\nproject: p\nname: d\nnamespace: p\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if _, err := Run(Options{
		Target: dir, NoAgentsMD: true,
		Stdin:  strings.NewReader(""),
		Stdout: &out,
		Stderr: &errb,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, AgentsMDName)); !os.IsNotExist(err) {
		t.Errorf("--no-agents-md must leave AGENTS.md untouched, stat err = %v", err)
	}
}

// TestRunRerunAgentsMDIdempotent: a current block reconciles to reuse.
func TestRunRerunAgentsMDIdempotent(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "eka.yaml"), []byte("version: 1\nproject: p\nname: d\nnamespace: p\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	block := append(append([]byte(nil), testBlock("p", "p", nil)...), '\n')
	if err := os.WriteFile(filepath.Join(dir, AgentsMDName), block, 0o644); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(dir, AgentsMDName))
	var out, errb bytes.Buffer
	if _, err := Run(Options{
		Target: dir,
		Stdin:  strings.NewReader(""),
		Stdout: &out,
		Stderr: &errb,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	after, _ := os.ReadFile(filepath.Join(dir, AgentsMDName))
	if string(after) != string(before) {
		t.Errorf("current block must stay byte-identical")
	}
}
func TestRunAgentsMDEndToEnd(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "proj")
	var out, errb bytes.Buffer
	opts := Options{
		Target: dir, Project: "proj", Namespace: "proj",
		AgentsMD: true,
		Stdin:    strings.NewReader(""),
		Stdout:   &out,
		Stderr:   &errb,
	}
	if _, err := Run(opts); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, AgentsMDName))
	if err != nil {
		t.Fatalf("AGENTS.md missing: %v", err)
	}
	if !strings.Contains(string(got), "Project: proj | Namespace: proj") {
		t.Errorf("AGENTS.md must carry the identity:\n%s", got)
	}
	// Second run reconciles to reuse (idempotent).
	opts2 := opts
	second, err := Run(opts2)
	if err != nil {
		t.Fatalf("second Run: %v", err)
	}
	var reused bool
	for _, f := range second.Plan {
		if f.Path == AgentsMDName && f.Kind == ActionReuse {
			reused = true
		}
	}
	if !reused {
		t.Errorf("second run must plan ActionReuse for AGENTS.md, got %+v", second.Plan)
	}
	got2, _ := os.ReadFile(filepath.Join(dir, AgentsMDName))
	if string(got2) != string(got) {
		t.Errorf("second run must leave AGENTS.md byte-identical")
	}
}

// writeConstraintsSnapshot emits a source-only committed snapshot at dir
// carrying the given units, so bootstrap tests exercise the same
// snapshot reader production does.
func writeConstraintsSnapshot(t *testing.T, dir string, units []*exchange.Unit) {
	t.Helper()
	pkg := &exchange.Package{
		Header: exchange.Header{
			SerializationVersion:  exchange.SerializationVersion,
			ExchangeFormatVersion: exchange.ExchangeFormatVersion,
			SpecificationVersion:  exchange.SpecificationVersion,
			Exporter:              exchange.Exporter,
			PackageIdentityLabel:  "rsf-repo-ea-2",
			ExportScope:           exchange.ScopeRepository,
			Namespace:             "ea",
		},
		Units:        units,
		Declarations: exchange.Declarations{Closure: exchange.ClosureDeclaration{Scope: exchange.ScopeRepository}},
	}
	files, _, err := exchange.EmitSource(pkg)
	if err != nil {
		t.Fatalf("emit fixture snapshot: %v", err)
	}
	for _, f := range files {
		p := filepath.Join(dir, filepath.FromSlash(f.Name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, f.Data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// constraintFixtureUnit builds one active stratum-1/2 fixture unit.
func constraintFixtureUnit(typ, id, domain string) *exchange.Unit {
	return &exchange.Unit{
		Identity:              exchange.Identity{Namespace: "ea", Type: typ, ID: id, InstanceVersion: 1},
		CanonicalIdentityForm: "ea/" + typ + ":" + id + ":1",
		Revision:              1,
		StateVector:           exchange.StateVector{ContentState: "accepted", ExistenceState: "active"},
		ChangeLog:             []exchange.ChangeLogEntry{},
		Relationships:         []exchange.Relationship{},
		Classification:        exchange.Classification{Domain: domain},
		Content:               exchange.ContentRef{Representation: exchange.StructuredJSON, File: "content"},
		ContentPayload:        []byte("body"),
	}
}

// TestPlanAgentsMDIndexedReuse (M1): an already-indexed AGENTS.md must
// NOT be overwritten with an identity-only block by a re-run of init.
// The plan passes the SAME index `eka agents-md` derives (from the
// committed snapshot), so a current indexed file plans Reuse and the
// merge is a fixed point — init and agents-md never fight.
func TestPlanAgentsMDIndexedReuse(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "eka.yaml"),
		[]byte("version: 1\nproject: x\nname: x\nnamespace: x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	snapDir := filepath.Join(dir, "exchange", "snapshots")
	writeConstraintsSnapshot(t, snapDir, []*exchange.Unit{
		constraintFixtureUnit("fnd", "product-identity", "Discovery"),
		constraintFixtureUnit("adr", "001-x", "Architecture"),
		constraintFixtureUnit("sto", "ignored", "Execution"), // stratum 3: excluded
	})
	index, missing, err := ConstraintsFromSnapshot(snapDir)
	if err != nil {
		t.Fatalf("ConstraintsFromSnapshot: %v", err)
	}
	if missing {
		t.Fatal("fixture snapshot must be present")
	}
	if len(index) == 0 {
		t.Fatal("fixture snapshot must yield a non-empty index")
	}
	block, _ := BuildAgentsMDBlock("x", "x", index)
	indexed := append(append([]byte(nil), block...), '\n')
	if err := os.WriteFile(filepath.Join(dir, AgentsMDName), indexed, 0o644); err != nil {
		t.Fatal(err)
	}
	// The same index reconciles the indexed file to itself (fixed point).
	if got := MergeAgentsMD(indexed, testBlock("x", "x", index)); got != nil {
		t.Fatalf("indexed existing must reconcile to nil, got %q", got)
	}
	// The init plan must reuse it — never plan an identity-only overwrite.
	d := &Discovery{AbsTarget: dir, BaseName: "x"}
	var sawAgentsMD bool
	for _, a := range BuildPlan(dir, d, Answers{Project: "x", Namespace: "x", AgentsMD: true}) {
		if a.Path != AgentsMDName {
			continue
		}
		sawAgentsMD = true
		if a.Kind != ActionReuse {
			t.Errorf("indexed AGENTS.md must plan Reuse, got kind %q (would overwrite the index)", a.Kind)
		}
	}
	if !sawAgentsMD {
		t.Error("plan must contain an AGENTS.md step")
	}
	// And the on-disk file is unchanged after planning.
	after, _ := os.ReadFile(filepath.Join(dir, AgentsMDName))
	if !bytes.Equal(after, indexed) {
		t.Error("planning must not mutate the indexed AGENTS.md")
	}
}
