package cmd

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/maleolabs/eka-cli/bootstrap"
	"github.com/maleolabs/eka-core/exchange"
)

// cmd tests for `eka verify`: the repository-scope verification gate
// (stage A of sto:verify-changed-gate). Fixtures reuse the agents-md
// source-only snapshot helpers (same package), so the gate is verified
// against exactly what a real committed snapshot looks like.

// staleAgentsMD writes a marked-but-stale AGENTS.md managed region.
func staleAgentsMD(t *testing.T, dir string) {
	t.Helper()
	body := "<!-- eka:workflow:start -->\nSTALE\n<!-- eka:workflow:end -->\n"
	if err := os.WriteFile(filepath.Join(dir, bootstrap.AgentsMDName), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// corruptSnapshot overwrites the first unit.json under dir with invalid
// JSON, so exchange.LoadSnapshot refuses the snapshot.
func corruptSnapshot(t *testing.T, dir string) {
	t.Helper()
	var target string
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && d.Name() == "unit.json" && target == "" {
			target = p
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if target == "" {
		t.Fatalf("no unit.json under %s", dir)
	}
	if err := os.WriteFile(target, []byte("{ not json"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// gitInit initializes a repository at dir with one commit of the
// current tree.
func gitInit(t *testing.T, dir string) {
	t.Helper()
	run := func(args ...string) {
		full := append([]string{"-c", "user.email=t@t", "-c", "user.name=t"}, args...)
		c := exec.Command("git", full...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("add", "-A")
	run("commit", "-qm", "init")
}

// verifyJSON decodes the single JSON document `eka verify --json` emits.
func verifyJSON(t *testing.T, out string) verifyReport {
	t.Helper()
	var r verifyReport
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatalf("unmarshal verify json: %v\n%s", err, out)
	}
	return r
}

// findVerifyCheck returns the result of one check id, or fails.
func findVerifyCheck(t *testing.T, r verifyReport, id string) verifyCheckResult {
	t.Helper()
	for _, c := range r.Checks {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("check %q not present in report", id)
	return verifyCheckResult{}
}

// TestVerifyCurrentRepo: a valid repository with a current AGENTS.md
// passes every check and exits 0.
func TestVerifyCurrentRepo(t *testing.T) {
	units := []*exchange.Unit{
		agentsMDFixtureUnit("fnd", "product-identity", "Discovery"),
		agentsMDFixtureUnit("adr", "001-x", "Architecture"),
	}
	agentsMDRepo(t, units)
	if code, _, errText := runIn([]string{"agents-md", "--write"}); code != 0 {
		t.Fatalf("agents-md --write: exit = %d\n%s", code, errText)
	}
	code, out, errText := runIn([]string{"verify"})
	if code != 0 {
		t.Fatalf("verify: exit = %d, want 0\nstdout: %s\nstderr: %s", code, out, errText)
	}
	if !strings.Contains(out, "Verdict: PASS") || !strings.Contains(out, "[PASS] agents-md-drift") {
		t.Errorf("verify must pass agents-md-drift:\n%s", out)
	}
}

// TestVerifyAgentsMDDrift: a stale managed region flips the gate to exit
// 1, and the output names the constraint id and the exact `eka get`
// command.
func TestVerifyAgentsMDDrift(t *testing.T) {
	repo := agentsMDRepo(t, []*exchange.Unit{agentsMDFixtureUnit("adr", "001-x", "Architecture")})
	staleAgentsMD(t, repo)
	code, out, _ := runIn([]string{"verify"})
	if code != 1 {
		t.Fatalf("verify with drift: exit = %d, want 1\n%s", code, out)
	}
	for _, want := range []string{
		"[FAIL] agents-md-drift",
		"eka/req:agent-self-describing-surface",
		"eka get eka/req:agent-self-describing-surface",
		"eka agents-md --write",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("drift output missing %q:\n%s", want, out)
		}
	}
	// Minor 3: the drift finding carries the first differing LINE.
	_, jout, _ := runIn([]string{"verify", "--json"})
	r := verifyJSON(t, jout)
	c := findVerifyCheck(t, r, "agents-md-drift")
	if len(c.Findings) == 0 || c.Findings[0].Line == nil {
		t.Fatalf("drift finding must carry a line:\n%s", jout)
	}
	if *c.Findings[0].Line != 2 { // staleAgentsMD writes STALE on line 2.
		t.Errorf("drift line = %d, want 2", *c.Findings[0].Line)
	}
}

// TestVerifyCorruptSnapshot: a present-but-corrupt snapshot is a
// blocking violation naming the snapshot constraint.
func TestVerifyCorruptSnapshot(t *testing.T) {
	repo := agentsMDRepo(t, []*exchange.Unit{agentsMDFixtureUnit("adr", "001-x", "Architecture")})
	corruptSnapshot(t, filepath.Join(repo, "exchange", "snapshots"))
	code, out, _ := runIn([]string{"verify"})
	if code != 1 {
		t.Fatalf("verify with corrupt snapshot: exit = %d, want 1\n%s", code, out)
	}
	for _, want := range []string{
		"[FAIL] snapshot-integrity",
		"eka/adr:027-source-only-snapshot-transport",
		"eka get eka/adr:027-source-only-snapshot-transport",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("corrupt-snapshot output missing %q:\n%s", want, out)
		}
	}
}

// TestVerifyNoSnapshotSkipped: a missing snapshot directory is SKIPPED,
// not a violation (a fresh v2 repository may have none).
func TestVerifyNoSnapshotSkipped(t *testing.T) {
	agentsMDRepo(t, nil) // no exchange/snapshots directory
	code, out, _ := runIn([]string{"verify", "--json"})
	if code != 0 {
		t.Fatalf("verify without snapshot: exit = %d, want 0\n%s", code, out)
	}
	r := verifyJSON(t, out)
	if got := findVerifyCheck(t, r, "snapshot-integrity").Verdict; got != verdictSkipped {
		t.Errorf("snapshot-integrity verdict = %q, want %q", got, verdictSkipped)
	}
}

// TestVerifyNoEkaYaml: outside an EKA repository the gate is a usage
// error (exit 2).
func TestVerifyNoEkaYaml(t *testing.T) {
	t.Chdir(t.TempDir())
	code, _, errText := runIn([]string{"verify"})
	if code != 2 {
		t.Fatalf("verify outside a repo: exit = %d, want 2\n%s", code, errText)
	}
	if !strings.Contains(errText, "not an EKA repository") {
		t.Errorf("stderr must explain the refusal: %q", errText)
	}
}

// TestVerifyChanged: with --changed, an unchanged check is SKIPPED and a
// changed path runs.
func TestVerifyChanged(t *testing.T) {
	// TestMain pins PATH to an empty dir for hermeticity; the --changed
	// diff runs git, so restore the real PATH for this test.
	if testOrigPath != "" {
		t.Setenv("PATH", testOrigPath)
	}
	repo := agentsMDRepo(t, []*exchange.Unit{agentsMDFixtureUnit("adr", "001-x", "Architecture")})
	gitInit(t, repo)

	// Nothing changed vs HEAD: every check skipped, exit 0.
	code, out, _ := runIn([]string{"verify", "--changed", "--json"})
	if code != 0 {
		t.Fatalf("verify --changed (clean): exit = %d, want 0\n%s", code, out)
	}
	r := verifyJSON(t, out)
	if !r.Changed {
		t.Errorf("changed flag must be true in the document")
	}
	for _, c := range r.Checks {
		if c.Verdict != verdictSkipped {
			t.Errorf("check %s: verdict = %q, want skipped (nothing changed)", c.ID, c.Verdict)
		}
	}

	// Stage a changed AGENTS.md: the agents-md-drift check runs (fails),
	// the others stay skipped.
	staleAgentsMD(t, repo)
	if out, err := exec.Command("git", "add", bootstrap.AgentsMDName).CombinedOutput(); err != nil {
		t.Fatalf("git add: %v\n%s", err, out)
	}
	code, out, _ = runIn([]string{"verify", "--changed", "--json"})
	if code != 1 {
		t.Fatalf("verify --changed (agents-md changed): exit = %d, want 1\n%s", code, out)
	}
	r = verifyJSON(t, out)
	if got := findVerifyCheck(t, r, "agents-md-drift").Verdict; got != verdictFail {
		t.Errorf("agents-md-drift verdict = %q, want fail (path changed)", got)
	}
	if got := findVerifyCheck(t, r, "snapshot-integrity").Verdict; got != verdictSkipped {
		t.Errorf("snapshot-integrity verdict = %q, want skipped (path unchanged)", got)
	}
}

// TestVerifyJSONStable: the JSON document is byte-stable across two runs
// and carries the fixed field order.
func TestVerifyJSONStable(t *testing.T) {
	repo := agentsMDRepo(t, []*exchange.Unit{agentsMDFixtureUnit("adr", "001-x", "Architecture")})
	staleAgentsMD(t, repo)
	code, out1, _ := runIn([]string{"verify", "--json"})
	if code != 1 {
		t.Fatalf("verify --json: exit = %d, want 1", code)
	}
	_, out2, _ := runIn([]string{"verify", "--json"})
	if out1 != out2 {
		t.Errorf("json document is not byte-stable:\n%q\n%q", out1, out2)
	}
	// Assert the TOP-LEVEL verdict by parsing (a strings.Index substring
	// would match checks[0] and hide a missing top-level key).
	r := verifyJSON(t, out1)
	if r.Verdict != verdictFail {
		t.Errorf("top-level verdict = %q, want %q", r.Verdict, verdictFail)
	}
	if got := findVerifyCheck(t, r, "agents-md-drift").Verdict; got != verdictFail {
		t.Errorf("agents-md-drift verdict = %q, want %q", got, verdictFail)
	}
	// The top-level verdict is the LAST field (fixed order): the document
	// must end with it.
	if !strings.HasSuffix(strings.TrimSpace(out1), `"verdict":"fail"}`) {
		t.Errorf("document must end with the top-level verdict field:\n%s", out1)
	}
	// Fixed field order at the top level.
	ordered := []string{`"root":`, `"changed":`, `"checks":`}
	last := -1
	for _, k := range ordered {
		i := strings.Index(out1, k)
		if i < 0 {
			t.Fatalf("json missing %q:\n%s", k, out1)
		}
		if i < last {
			t.Errorf("field %q out of order:\n%s", k, out1)
		}
		last = i
	}
}

// TestVerifyForce: --force prints the violation but exits 0.
func TestVerifyForce(t *testing.T) {
	repo := agentsMDRepo(t, []*exchange.Unit{agentsMDFixtureUnit("adr", "001-x", "Architecture")})
	staleAgentsMD(t, repo)
	code, out, _ := runIn([]string{"verify", "--force"})
	if code != 0 {
		t.Fatalf("verify --force: exit = %d, want 0\n%s", code, out)
	}
	if !strings.Contains(out, "[FAIL] agents-md-drift") || !strings.Contains(out, "Verdict: FAIL") {
		t.Errorf("--force must still print the violation and the honest verdict:\n%s", out)
	}
}

// TestVerifyChangedDriftOnSnapshotChange: a change to exchange/snapshots
// (the block's index source) with a now-stale AGENTS.md must run the
// drift check under --changed and exit 1 — not skip it (Major 1).
func TestVerifyChangedDriftOnSnapshotChange(t *testing.T) {
	if testOrigPath != "" {
		t.Setenv("PATH", testOrigPath)
	}
	unitA := agentsMDFixtureUnit("adr", "001-x", "Architecture")
	repo := agentsMDRepo(t, []*exchange.Unit{unitA})
	if code, _, e := runIn([]string{"agents-md", "--write"}); code != 0 {
		t.Fatalf("agents-md --write: %s", e)
	}
	gitInit(t, repo)
	// Add a second unit: the index (and thus the block) changes while the
	// committed AGENTS.md stays as generated for unitA — drift, and only
	// exchange/snapshots changed.
	writeAgentsMDSnapshot(t, filepath.Join(repo, "exchange", "snapshots"),
		[]*exchange.Unit{unitA, agentsMDFixtureUnit("adr", "002-y", "Architecture")})
	code, out, _ := runIn([]string{"verify", "--changed", "--json"})
	if code != 1 {
		t.Fatalf("verify --changed (snapshot changed): exit = %d, want 1\n%s", code, out)
	}
	r := verifyJSON(t, out)
	if got := findVerifyCheck(t, r, "agents-md-drift").Verdict; got != verdictFail {
		t.Errorf("agents-md-drift verdict = %q, want fail (snapshot path changed)", got)
	}
}

// TestVerifyChangedDriftOnEkaYamlChange: a change to eka.yaml (the
// block's identity source) must likewise trigger the drift check
// (Major 1).
func TestVerifyChangedDriftOnEkaYamlChange(t *testing.T) {
	if testOrigPath != "" {
		t.Setenv("PATH", testOrigPath)
	}
	repo := agentsMDRepo(t, []*exchange.Unit{agentsMDFixtureUnit("adr", "001-x", "Architecture")})
	if code, _, e := runIn([]string{"agents-md", "--write"}); code != 0 {
		t.Fatalf("agents-md --write: %s", e)
	}
	gitInit(t, repo)
	// Rename the project: the identity line changes → drift; eka.yaml is
	// a changed path → the check must run.
	if err := os.WriteFile(filepath.Join(repo, "eka.yaml"),
		[]byte("version: 1\nproject: ea2\nname: ea2\nnamespace: ea\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, _ := runIn([]string{"verify", "--changed", "--json"})
	if code != 1 {
		t.Fatalf("verify --changed (eka.yaml changed): exit = %d, want 1\n%s", code, out)
	}
	r := verifyJSON(t, out)
	if got := findVerifyCheck(t, r, "agents-md-drift").Verdict; got != verdictFail {
		t.Errorf("agents-md-drift verdict = %q, want fail (eka.yaml changed)", got)
	}
}

// TestVerifyChangedScopesConformance: with --changed, the conformance
// check reports only the changed files; a pre-existing unrelated error
// in docs/ does not block a change to a clean file (Major 2).
func TestVerifyChangedScopesConformance(t *testing.T) {
	if testOrigPath != "" {
		t.Setenv("PATH", testOrigPath)
	}
	repo := agentsMDRepo(t, nil)
	docs := filepath.Join(repo, "docs")
	if err := os.MkdirAll(docs, 0o755); err != nil {
		t.Fatal(err)
	}
	good := filepath.Join(docs, "good.md")
	bad := filepath.Join(docs, "bad.md")
	goodBody := "# Notes\n\nplain convention doc, no frontmatter\n"
	if err := os.WriteFile(good, []byte(goodBody), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bad, []byte("---\ntype: adr\n---\n\n# bad\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitInit(t, repo)

	// Touch ONLY the clean file: the pre-existing error in bad.md is out
	// of the changed set → exit 0.
	if err := os.WriteFile(good, []byte(goodBody+"changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, _ := runIn([]string{"verify", "--changed", "--json"})
	if code != 0 {
		t.Fatalf("verify --changed (clean file touched): exit = %d, want 0\n%s", code, out)
	}
	r := verifyJSON(t, out)
	if got := findVerifyCheck(t, r, "conformance").Verdict; got != verdictPass {
		t.Errorf("conformance verdict = %q, want pass (only the clean file changed)", got)
	}

	// Touch the erroring file: the error is in the changed set → exit 1.
	if err := os.WriteFile(bad, []byte("---\ntype: adr\n---\n\n# bad\n\nchanged\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, _ = runIn([]string{"verify", "--changed", "--json"})
	if code != 1 {
		t.Fatalf("verify --changed (erroring file touched): exit = %d, want 1\n%s", code, out)
	}
	r = verifyJSON(t, out)
	if got := findVerifyCheck(t, r, "conformance").Verdict; got != verdictFail {
		t.Errorf("conformance verdict = %q, want fail (erroring file changed)", got)
	}
}

// TestVerifyChangedUntracked: a brand-new un-staged file is visible to
// --changed through the git ls-files --others union (Minor 5).
func TestVerifyChangedUntracked(t *testing.T) {
	if testOrigPath != "" {
		t.Setenv("PATH", testOrigPath)
	}
	repo := agentsMDRepo(t, nil)
	gitInit(t, repo)
	docs := filepath.Join(repo, "docs")
	if err := os.MkdirAll(docs, 0o755); err != nil {
		t.Fatal(err)
	}
	// Untracked and NOT staged: only git ls-files --others sees it.
	if err := os.WriteFile(filepath.Join(docs, "bad.md"), []byte("---\ntype: adr\n---\n\n# bad\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, _ := runIn([]string{"verify", "--changed", "--json"})
	if code != 1 {
		t.Fatalf("verify --changed (untracked doc): exit = %d, want 1\n%s", code, out)
	}
	r := verifyJSON(t, out)
	if got := findVerifyCheck(t, r, "conformance").Verdict; got != verdictFail {
		t.Errorf("conformance verdict = %q, want fail (untracked file is changed)", got)
	}
}

// TestVerifyGitFailure: an unusable --base ref is a usage error (exit 2)
// (Minor 4).
func TestVerifyGitFailure(t *testing.T) {
	if testOrigPath != "" {
		t.Setenv("PATH", testOrigPath)
	}
	repo := agentsMDRepo(t, nil)
	gitInit(t, repo)
	code, _, errText := runIn([]string{"verify", "--changed", "--base", "refs/heads/does-not-exist"})
	if code != 2 {
		t.Fatalf("verify --changed --base <missing ref>: exit = %d, want 2\n%s", code, errText)
	}
}

// TestVerifyBaseFlagRejected: a --base that looks like a git option is
// rejected before git runs (exit 2), so it cannot silently produce an
// all-skipped PASS (Minor 6).
func TestVerifyBaseFlagRejected(t *testing.T) {
	if testOrigPath != "" {
		t.Setenv("PATH", testOrigPath)
	}
	repo := agentsMDRepo(t, nil)
	gitInit(t, repo)
	code, _, errText := runIn([]string{"verify", "--changed", "--base=-n"})
	if code != 2 {
		t.Fatalf("verify --changed --base=-n: exit = %d, want 2\n%s", code, errText)
	}
}

// TestVerifyNonVacuity proves each check flips exit 0 -> 1 when its
// violation is introduced (one baseline per check).
func TestVerifyNonVacuity(t *testing.T) {
	t.Run("agents-md-drift", func(t *testing.T) {
		repo := agentsMDRepo(t, []*exchange.Unit{agentsMDFixtureUnit("adr", "001-x", "Architecture")})
		if code, _, _ := runIn([]string{"agents-md", "--write"}); code != 0 {
			t.Fatal("agents-md --write failed")
		}
		if code, _, _ := runIn([]string{"verify"}); code != 0 {
			t.Fatalf("baseline exit = %d, want 0", code)
		}
		staleAgentsMD(t, repo)
		if code, _, _ := runIn([]string{"verify"}); code != 1 {
			t.Fatalf("drift exit = %d, want 1", code)
		}
	})
	t.Run("snapshot-integrity", func(t *testing.T) {
		repo := agentsMDRepo(t, []*exchange.Unit{agentsMDFixtureUnit("adr", "001-x", "Architecture")})
		if code, _, _ := runIn([]string{"verify"}); code != 0 {
			t.Fatalf("baseline exit = %d, want 0", code)
		}
		corruptSnapshot(t, filepath.Join(repo, "exchange", "snapshots"))
		if code, _, _ := runIn([]string{"verify"}); code != 1 {
			t.Fatalf("corrupt exit = %d, want 1", code)
		}
	})
	t.Run("conformance", func(t *testing.T) {
		repo := agentsMDRepo(t, nil)
		if code, _, _ := runIn([]string{"verify"}); code != 0 {
			t.Fatalf("baseline exit = %d, want 0", code)
		}
		docs := filepath.Join(repo, "docs")
		if err := os.MkdirAll(docs, 0o755); err != nil {
			t.Fatal(err)
		}
		// An artifact rule violation (type XOR id) is a blocking R0 error.
		if err := os.WriteFile(filepath.Join(docs, "bad.md"), []byte("---\ntype: adr\n---\n\n# bad\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if code, _, _ := runIn([]string{"verify"}); code != 1 {
			t.Fatalf("bad-docs exit = %d, want 1", code)
		}
	})
}

// TestVerifyTiming measures the wall time of `eka verify` on the eka-cli
// repository itself and of the AGENTS.md-drift check alone. It is a
// report-only measurement (no assertion beyond a loose ceiling).
func TestVerifyTiming(t *testing.T) {
	root, err := snapshotRepositoryRoot(".")
	if err != nil {
		t.Skipf("not running inside the eka-cli repository: %v", err)
	}
	start := time.Now()
	code, _, errText := runIn([]string{"verify"})
	full := time.Since(start)
	if code != 0 && code != 1 {
		t.Fatalf("verify: exit = %d\n%s", code, errText)
	}
	t.Logf("eka verify on eka-cli: %s (exit %d)", full, code)

	start = time.Now()
	runAgentsMDDriftCheck(root, nil)
	drift := time.Since(start)
	t.Logf("agents-md-drift check alone: %s", drift)

	if full > 30*time.Second {
		t.Errorf("eka verify took %s, want < 30s", full)
	}
}
