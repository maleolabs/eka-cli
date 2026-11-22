package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/maleolabs/eka-cli/bootstrap"
	"github.com/maleolabs/eka-core/exchange"
	"github.com/maleolabs/eka-core/metadata"
)

// cmd tests for `eka agents-md`: the generated, drift-checked AGENTS.md
// entry point. Fixtures build a SOURCE-ONLY committed snapshot via the
// public exchange emitter, so the fixtures verify exactly what a real
// committed snapshot does.

const (
	testAgentsMDStart = "<!-- eka:workflow:start -->"
	testAgentsMDEnd   = "<!-- eka:workflow:end -->"
)

// agentsMDFixtureUnit builds one active fixture unit of the given type
// and domain.
func agentsMDFixtureUnit(typ, id, domain string) *exchange.Unit {
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

// writeAgentsMDSnapshot emits units as a source-only snapshot at dir.
func writeAgentsMDSnapshot(t *testing.T, dir string, units []*exchange.Unit) {
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

// agentsMDRepo creates a repository (eka.yaml + snapshot) in a temp dir
// and chdirs into it. units may be nil (no snapshot files written).
func agentsMDRepo(t *testing.T, units []*exchange.Unit) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "eka.yaml"),
		[]byte("version: 1\nproject: ea\nname: ea\nnamespace: ea\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if units != nil {
		writeAgentsMDSnapshot(t, filepath.Join(dir, "exchange", "snapshots"), units)
	}
	t.Chdir(dir)
	return dir
}

// managedBlock extracts the marker-delimited managed region of file.
func managedBlock(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	start := strings.Index(s, testAgentsMDStart)
	end := strings.Index(s, testAgentsMDEnd)
	if start < 0 || end < start {
		t.Fatalf("no managed region in %s:\n%s", path, s)
	}
	return s[start : end+len(testAgentsMDEnd)]
}

// TestAgentsMDWriteThenCheck: --write produces the block; a second
// --check is current (exit 0). A drift of ONE index line flips --check
// to exit 1 (non-vacuity).
func TestAgentsMDWriteThenCheck(t *testing.T) {
	units := []*exchange.Unit{
		agentsMDFixtureUnit("fnd", "product-identity", "Discovery"),
		agentsMDFixtureUnit("adr", "001-identity-serialization", "Architecture"),
		agentsMDFixtureUnit("sto", "ignored-execution", "Execution"),
	}
	repo := agentsMDRepo(t, units)

	if code, _, errText := runIn([]string{"agents-md", "--write"}); code != 0 {
		t.Fatalf("--write: exit = %d, want 0\nstderr: %s", code, errText)
	}
	path := filepath.Join(repo, "AGENTS.md")
	block := managedBlock(t, path)
	if !strings.Contains(block, "fnd: product-identity") || !strings.Contains(block, "adr: 001-identity-serialization") {
		t.Errorf("index must carry the stratum-1/2 units only:\n%s", block)
	}
	if strings.Contains(block, "ignored-execution") {
		t.Errorf("index must exclude execution-domain units:\n%s", block)
	}
	if code, _, errText := runIn([]string{"agents-md", "--check"}); code != 0 {
		t.Fatalf("--check after --write: exit = %d, want 0\nstderr: %s", code, errText)
	}

	// Perturb exactly ONE index line (the adr id) — --check must drift.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	drifted := strings.Replace(string(raw), "001-identity-serialization", "001-PERTURBED", 1)
	if drifted == string(raw) {
		t.Fatal("perturbation did not change the file")
	}
	if err := os.WriteFile(path, []byte(drifted), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, errText := runIn([]string{"agents-md", "--check"})
	if code != 1 {
		t.Fatalf("--check after one-line drift: exit = %d, want 1\nstderr: %s", code, errText)
	}
	if !strings.Contains(errText, "drifted") || !strings.Contains(errText, "first differing line") {
		t.Errorf("drift summary must name the first differing line:\n%s", errText)
	}
}

// TestAgentsMDNoEkaYaml: outside an EKA repository --check is a usage/IO
// error (exit 2).
func TestAgentsMDNoEkaYaml(t *testing.T) {
	t.Chdir(t.TempDir())
	code, _, errText := runIn([]string{"agents-md", "--check"})
	if code != 2 {
		t.Fatalf("exit = %d, want 2\nstderr: %s", code, errText)
	}
	if !strings.Contains(errText, "not an EKA repository") {
		t.Errorf("stderr must explain the refusal: %q", errText)
	}
}

// TestAgentsMDBounded: a snapshot large enough to overflow the budget
// truncates deterministically and the block stays within the byte
// budget (assert the byte count, not a line count).
func TestAgentsMDBounded(t *testing.T) {
	var units []*exchange.Unit
	for i := 0; i < 400; i++ {
		units = append(units,
			agentsMDFixtureUnit("adr", fmt.Sprintf("adr-%03d-a-long-identifier-to-force-overflow", i), "Architecture"))
	}
	agentsMDRepo(t, units)
	if code, _, errText := runIn([]string{"agents-md", "--write"}); code != 0 {
		t.Fatalf("--write: exit = %d, want 0\nstderr: %s", code, errText)
	}
	block := managedBlock(t, filepath.Join("AGENTS.md"))
	if len(block) > 3200 {
		t.Fatalf("block = %d bytes, want ≤ 3200", len(block))
	}
	if !strings.Contains(block, "... +") || !strings.Contains(block, "more (eka status)") {
		t.Errorf("oversized index must carry the truncation line:\n%s", block)
	}
	// Determinism: a second --check is current (regeneration is stable).
	if code, _, errText := runIn([]string{"agents-md", "--check"}); code != 0 {
		t.Fatalf("--check must be current after --write: exit = %d\nstderr: %s", code, errText)
	}
}

// TestAgentsMDNoSnapshot: without a committed snapshot the block is
// identity-only (no constraint heading), --write succeeds, and the
// reason is reported on stderr; --check then exits 0.
func TestAgentsMDNoSnapshot(t *testing.T) {
	agentsMDRepo(t, nil) // no snapshot directory
	code, _, errText := runIn([]string{"agents-md", "--write"})
	if code != 0 {
		t.Fatalf("--write: exit = %d, want 0\nstderr: %s", code, errText)
	}
	if !strings.Contains(errText, "no committed snapshot") {
		t.Errorf("stderr must report the missing snapshot: %q", errText)
	}
	block := managedBlock(t, "AGENTS.md")
	if strings.Contains(block, "Constraints (") {
		t.Errorf("identity-only block must omit the constraint heading:\n%s", block)
	}
	if code, _, errText := runIn([]string{"agents-md", "--check"}); code != 0 {
		t.Fatalf("--check identity-only: exit = %d, want 0\nstderr: %s", code, errText)
	}
}

// TestAgentsMDJSONStable: the --json report is byte-stable across two
// runs and carries the deterministic field set.
func TestAgentsMDJSONStable(t *testing.T) {
	units := []*exchange.Unit{
		agentsMDFixtureUnit("fnd", "product-identity", "Discovery"),
		agentsMDFixtureUnit("adr", "001-x", "Architecture"),
	}
	agentsMDRepo(t, units)
	if code, _, errText := runIn([]string{"agents-md", "--write"}); code != 0 {
		t.Fatalf("--write: exit = %d\nstderr: %s", code, errText)
	}
	code, out1, _ := runIn([]string{"agents-md", "--check", "--json"})
	if code != 0 {
		t.Fatalf("--check --json: exit = %d", code)
	}
	_, out2, _ := runIn([]string{"agents-md", "--check", "--json"})
	if out1 != out2 {
		t.Errorf("json report is not byte-stable:\n%q\n%q", out1, out2)
	}
	for _, want := range []string{
		`"file":"AGENTS.md"`, `"status":"current"`, `"constraints":2`,
		`"blockBytes":`, `"budget":3200`, `"truncated":false`,
	} {
		if !strings.Contains(out1, want) {
			t.Errorf("json report missing %q:\n%s", want, out1)
		}
	}
}

// TestAgentsMDHelp: the command help documents the flags and exit codes.
func TestAgentsMDHelp(t *testing.T) {
	code, out, _ := runIn([]string{"agents-md", "--help"})
	if code != 0 {
		t.Fatalf("--help: exit = %d, want 0", code)
	}
	for _, want := range []string{"--write", "--check", "--json", "Exit codes", "exchange/snapshots"} {
		if !strings.Contains(out, want) {
			t.Errorf("help must document %q", want)
		}
	}
}

// TestAgentsMDCommittedBudget asserts the block generated for THIS
// repository's committed snapshot fits the byte budget (a test of the
// real snapshot, not a fixture). If it cannot fit, the budget must not
// be raised silently — that is a report condition.
func TestAgentsMDCommittedBudget(t *testing.T) {
	root, err := snapshotRepositoryRoot("..")
	if err != nil {
		t.Skipf("not running inside the eka-cli repository: %v", err)
	}
	index, missing, err := bootstrap.ConstraintsFromSnapshot(filepath.Join(root, "exchange", "snapshots"))
	if err != nil {
		t.Fatalf("load committed snapshot: %v", err)
	}
	if missing {
		t.Skip("no committed snapshot in this repository")
	}
	block, _ := bootstrap.BuildAgentsMDBlock("eka", "eka", index)
	if len(block) > 3200 {
		t.Fatalf("committed-snapshot block = %d bytes, want ≤ 3200", len(block))
	}
}

// TestAgentsMDCommittedBlockMatches is criterion 4's real gate: the
// committed AGENTS.md's managed region must equal the block regenerated
// from the committed snapshot byte-for-byte. A stale file fails here.
// Skips cleanly when the repo files are absent (extracted module).
func TestAgentsMDCommittedBlockMatches(t *testing.T) {
	root, err := snapshotRepositoryRoot("..")
	if err != nil {
		t.Skipf("not running inside the eka-cli repository: %v", err)
	}
	rawMeta, err := os.ReadFile(filepath.Join(root, "eka.yaml"))
	if err != nil {
		t.Skipf("no eka.yaml in %s: %v", root, err)
	}
	meta, err := metadata.Parse(rawMeta)
	if err != nil {
		t.Fatalf("invalid eka.yaml: %v", err)
	}
	index, missing, err := bootstrap.ConstraintsFromSnapshot(filepath.Join(root, "exchange", "snapshots"))
	if err != nil {
		t.Fatalf("load committed snapshot: %v", err)
	}
	if missing {
		t.Skip("no committed snapshot in this repository")
	}
	want, _ := bootstrap.BuildAgentsMDBlock(meta.Project, meta.Namespace, index)
	path := filepath.Join(root, bootstrap.AgentsMDName)
	if _, err := os.Stat(path); err != nil {
		t.Skipf("no %s in %s: %v", bootstrap.AgentsMDName, root, err)
	}
	got := managedBlock(t, path)
	if got != string(want) {
		t.Fatalf("committed %s managed region is stale (%d bytes) vs regenerated (%d bytes); run 'eka agents-md --write'",
			bootstrap.AgentsMDName, len(got), len(want))
	}
}
