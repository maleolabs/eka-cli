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
	// Fixed field order at the top level and within a check.
	ordered := []string{`"root":`, `"changed":`, `"checks":`, `"verdict":"fail"`}
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
	checkOrder := []string{`"id":"agents-md-drift"`, `"constraint":"eka/req:agent-self-describing-surface"`, `"verdict":"fail"`, `"findings":`}
	last = -1
	for _, k := range checkOrder {
		i := strings.Index(out1, k)
		if i < 0 {
			t.Fatalf("json missing %q:\n%s", k, out1)
		}
		if i < last {
			t.Errorf("check field %q out of order:\n%s", k, out1)
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
	runAgentsMDDriftCheck(root)
	drift := time.Since(start)
	t.Logf("agents-md-drift check alone: %s", drift)

	if full > 30*time.Second {
		t.Errorf("eka verify took %s, want < 30s", full)
	}
}
