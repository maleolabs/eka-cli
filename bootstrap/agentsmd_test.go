package bootstrap

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestBuildAgentsMDBlock pins the managed block bytes: markers,
// identity line and the static command/skill lines.
func TestBuildAgentsMDBlock(t *testing.T) {
	b := string(buildAgentsMDBlock("atrium", "atrium-api"))
	for _, want := range []string{
		agentsMDMarkerStart,
		agentsMDMarkerEnd,
		"Project: atrium | Namespace: atrium-api | Active ctr: none",
		"eka get <form>",
		"eka-orientation",
	} {
		if !strings.Contains(b, want) {
			t.Errorf("block must contain %q:\n%s", want, b)
		}
	}
	if !strings.HasSuffix(b, agentsMDMarkerEnd) {
		t.Errorf("block must end at the end marker (no trailing newline — merge owns separation):\n%s", b)
	}
}

// TestMergeAgentsMDMissing: no existing content yields the block plus
// one trailing newline.
func TestMergeAgentsMDMissing(t *testing.T) {
	block := buildAgentsMDBlock("p", "n")
	if got := mergeAgentsMD(nil, block); string(got) != string(block)+"\n" {
		t.Errorf("missing file must yield the block plus newline, got %q", got)
	}
}

// TestMergeAgentsMDCurrent: an identical file reconciles to nil
// (reuse — nothing to write).
func TestMergeAgentsMDCurrent(t *testing.T) {
	block := buildAgentsMDBlock("p", "n")
	current := append(append([]byte(nil), block...), '\n')
	if got := mergeAgentsMD(current, block); got != nil {
		t.Errorf("current content must reconcile to nil, got %q", got)
	}
}

// TestMergeAgentsMDDrift: a stale marked region is replaced while user
// content outside the markers is preserved byte-identical.
func TestMergeAgentsMDDrift(t *testing.T) {
	old := "# My notes\n\n" + agentsMDMarkerStart + "\nSTALE\n" + agentsMDMarkerEnd + "\n\ntail\n"
	merged := mergeAgentsMD([]byte(old), buildAgentsMDBlock("p", "n"))
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
	if again := mergeAgentsMD(merged, buildAgentsMDBlock("p", "n")); again != nil {
		t.Errorf("merge must be idempotent, second pass wrote %q", again)
	}
}

// TestMergeAgentsMDNoMarkers: unmarked files gain the block appended
// after a blank line, existing bytes untouched.
func TestMergeAgentsMDForeign(t *testing.T) {
	existing := "# hello\n"
	merged := mergeAgentsMD([]byte(existing), buildAgentsMDBlock("p", "n"))
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
	block := buildAgentsMDBlock("x", "x")
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
	block := append(append([]byte(nil), buildAgentsMDBlock("p", "p")...), '\n')
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
	for _, f := range second.Plan {
		_ = f
	}
	got2, _ := os.ReadFile(filepath.Join(dir, AgentsMDName))
	if string(got2) != string(got) {
		t.Errorf("second run must leave AGENTS.md byte-identical")
	}
}
