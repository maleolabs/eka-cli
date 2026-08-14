package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// copySyncFixture copies the sync test fixture tree into a fresh temp
// dir and returns its path.
func copySyncFixture(t *testing.T) string {
	t.Helper()
	return copyFixture(t, filepath.Join("..", "testdata", "sync", "valid"))
}

func TestSyncHelpExitsZero(t *testing.T) {
	for _, args := range [][]string{{"sync", "-h"}, {"sync", "pull", "-h"}, {"sync", "push", "-h"}, {"sync", "--help"}} {
		code, text, _ := runIn(args)
		if code != 0 {
			t.Errorf("args %v: exit = %d, want 0", args, code)
		}
		if !strings.Contains(text, "eka sync") {
			t.Errorf("args %v: help must mention eka sync", args)
		}
	}
}

func TestSyncHelpDocumentsModel(t *testing.T) {
	_, text, _ := runIn([]string{"sync", "-h"})
	for _, want := range []string{"workspace", "$EKA_HOME", "exchange/snapshots", "idempotent", "Deletions", "source-only", "gitignored"} {
		if !strings.Contains(text, want) {
			t.Errorf("sync help must document %q", want)
		}
	}
}

// TestSyncHappyPath: syncing a fresh fixture repository exits 0 and
// renders the deterministic report; the snapshot directory is created.
func TestSyncHappyPath(t *testing.T) {
	t.Setenv("EKA_HOME", t.TempDir())
	repo := copySyncFixture(t)
	code, text, errText := runIn([]string{"sync", repo})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", code, text, errText)
	}
	for _, want := range []string{"Runtime", "Workspace", "Project", "Repository", "Pull", "Push", "Snapshot", "rsf-repo-eka-sync-fixture-2", "registered (new)", "docs: 4 units, 1 attachment", "4 units, 1 attachment"} {
		if !strings.Contains(text, want) {
			t.Errorf("output must contain %q:\n%s", want, text)
		}
	}
	if strings.Contains(errText, "\x1b") {
		t.Errorf("stderr must not contain ANSI escapes: %q", errText)
	}
	for _, want := range []string{"header.json"} {
		if _, err := os.Stat(filepath.Join(repo, "exchange", "snapshots", want)); err != nil {
			t.Errorf("snapshot missing %s: %v", want, err)
		}
	}
	// The derived aggregates are not committed (ADR-027).
	for _, gone := range []string{"manifest.json", "declarations.json", "integrity.json"} {
		if _, err := os.Stat(filepath.Join(repo, "exchange", "snapshots", gone)); err == nil {
			t.Errorf("snapshot must not carry the derived aggregate %s", gone)
		}
	}
}

// TestSyncSecondRunUnchanged: the second sync reports unchanged.
func TestSyncSecondRunUnchanged(t *testing.T) {
	t.Setenv("EKA_HOME", t.TempDir())
	repo := copySyncFixture(t)
	if code, _, errText := runIn([]string{"sync", repo}); code != 0 {
		t.Fatalf("first sync: exit %d\n%s", code, errText)
	}
	code, text, errText := runIn([]string{"sync", repo})
	if code != 0 {
		t.Fatalf("second sync: exit = %d\nstdout: %s\nstderr: %s", code, text, errText)
	}
	for _, want := range []string{"unchanged", "snapshot up to date"} {
		if !strings.Contains(text, want) {
			t.Errorf("second sync must report unchanged, missing %q:\n%s", want, text)
		}
	}
}

// TestSyncPullFromDocs: pull --from-docs re-seeds from the docs tree.
func TestSyncPullFromDocs(t *testing.T) {
	t.Setenv("EKA_HOME", t.TempDir())
	repo := copySyncFixture(t)
	if code, _, errText := runIn([]string{"sync", repo}); code != 0 {
		t.Fatalf("first sync: exit %d\n%s", code, errText)
	}
	code, text, errText := runIn([]string{"sync", "pull", repo, "--from-docs"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", code, text, errText)
	}
	if !strings.Contains(text, "docs: 4 units, 1 attachment") {
		t.Errorf("pull --from-docs must report the docs source:\n%s", text)
	}
}

// TestSyncPushOnly: push alone assembles the snapshot without pulling.
func TestSyncPushOnly(t *testing.T) {
	t.Setenv("EKA_HOME", t.TempDir())
	repo := copySyncFixture(t)
	code, _, errText := runIn([]string{"sync", "push", repo})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstderr: %s", code, errText)
	}
	// Nothing pulled, so the store is empty and the push is a no-op.
	if _, err := os.Stat(filepath.Join(repo, "exchange", "snapshots")); !os.IsNotExist(err) {
		t.Error("push of an empty store must not write a snapshot")
	}
	// Pull first, then push.
	if code, _, errText := runIn([]string{"sync", "pull", repo}); code != 0 {
		t.Fatalf("pull: exit %d\n%s", code, errText)
	}
	code, text, errText := runIn([]string{"sync", "push", repo})
	if code != 0 {
		t.Fatalf("push after pull: exit = %d\nstdout: %s\nstderr: %s", code, text, errText)
	}
	if !strings.Contains(text, "rsf-repo-eka-sync-fixture-2") {
		t.Errorf("push output must carry the snapshot label:\n%s", text)
	}
	if _, err := os.Stat(filepath.Join(repo, "exchange", "snapshots", "header.json")); err != nil {
		t.Error("push must write the snapshot")
	}
}

// TestSyncValidationFailureExitsOne: a non-conformant repository (no
// snapshot yet) is refused by the docs gate with the report — exit 1.
func TestSyncValidationFailureExitsOne(t *testing.T) {
	t.Setenv("EKA_HOME", t.TempDir())
	repo := t.TempDir()
	dir := filepath.Join(repo, "docs", "decisions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// The repository must be an EKA repository first (the context gate
	// would refuse before the docs gate otherwise).
	writeEkaYAML(t, repo, filepath.Base(repo), filepath.Base(repo), "eka-sync-fixture")
	bad := "---\nnamespace: x\nid: 1\n---\n# bad\n" // type missing: R0 error
	if err := os.WriteFile(filepath.Join(dir, "bad.md"), []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	code, text, errText := runIn([]string{"sync", repo})
	if code != 1 {
		t.Fatalf("exit = %d, want 1\nstdout: %s\nstderr: %s", code, text, errText)
	}
	if !strings.Contains(text, "Verdict: FAIL") {
		t.Errorf("stdout must contain the validation report:\n%s", text)
	}
	if !strings.Contains(errText, "knowledge compile refused") {
		t.Errorf("stderr must explain the refusal, got %q", errText)
	}
}

// TestSyncRefusesWithoutEKA (ADR-018): sync on a directory whose
// walk-up finds no eka.yaml is refused with the pinned sentence, exit
// 2 — in the full-cycle, pull-only and push-only modes alike.
func TestSyncRefusesWithoutEKA(t *testing.T) {
	t.Setenv("EKA_HOME", t.TempDir())
	dir := t.TempDir()
	for _, args := range [][]string{
		{"sync", dir},
		{"sync", "pull", dir},
		{"sync", "push", dir},
	} {
		code, _, errText := runIn(args)
		if code != 2 {
			t.Errorf("%v: exit = %d, want 2\nstderr: %s", args, code, errText)
		}
		if !strings.Contains(errText, "eka: sync refused:") ||
			!strings.Contains(errText, "is not an EKA repository (no eka.yaml)") ||
			!strings.Contains(errText, "run 'eka init' first") {
			t.Errorf("%v: stderr must carry the pinned ADR-018 refusal, got %q", args, errText)
		}
	}
}

// TestSyncCorruptSnapshotExitsOne: a structurally corrupted snapshot is
// an integrity failure — exit 1.
func TestSyncCorruptSnapshotExitsOne(t *testing.T) {
	t.Setenv("EKA_HOME", t.TempDir())
	repo := copySyncFixture(t)
	if code, _, errText := runIn([]string{"sync", repo}); code != 0 {
		t.Fatalf("first sync: exit %d\n%s", code, errText)
	}
	unitJSON := filepath.Join(repo, "exchange", "snapshots", "units", "eka-sync-fixture", "adr-001-runtime-v1", "unit.json")
	data, err := os.ReadFile(unitJSON)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(unitJSON, append([]byte("X"), data[1:]...), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, errText := runIn([]string{"sync", repo})
	if code != 1 {
		t.Fatalf("exit = %d, want 1\nstderr: %s", code, errText)
	}
	if !strings.Contains(errText, "snapshot package refused") {
		t.Errorf("stderr must explain the integrity refusal, got %q", errText)
	}
}

// TestSyncOutputDeterministic: two sync runs of identical state
// produce byte-identical output. The first run settles the repository
// (docs pull + registration); the second and third runs both report
// "unchanged".
func TestSyncOutputDeterministic(t *testing.T) {
	t.Setenv("EKA_HOME", t.TempDir())
	repo := copySyncFixture(t)
	if code, _, errText := runIn([]string{"sync", repo}); code != 0 {
		t.Fatalf("settling sync: exit %d\n%s", code, errText)
	}
	run := func() string {
		_, text, _ := runIn([]string{"sync", repo})
		return text
	}
	first := run()
	second := run()
	if first != second {
		t.Error("sync output differs between identical runs")
	}
}

// TestSyncContentNamespaceMismatchExitsTwo (ADR-020): a non-TTY sync
// whose content resolves to exactly ONE namespace differing from the
// declared one is refused with the pinned byte-exact sentence, exit 2.
func TestSyncContentNamespaceMismatchExitsTwo(t *testing.T) {
	t.Setenv("EKA_HOME", t.TempDir())
	repo := copySyncFixture(t)
	writeEkaYAML(t, repo, "eka-sync-fixture", "eka-sync-fixture", "other")

	code, _, errText := runIn([]string{"sync", repo})
	if code != 2 {
		t.Fatalf("exit = %d, want 2\nstderr: %s", code, errText)
	}
	want := "eka: sync refused: the repository content namespace eka-sync-fixture differs from the registered repository namespace other; run 'eka sync --override' to align the repository identity to eka-sync-fixture"
	if !strings.Contains(errText, want) {
		t.Errorf("stderr must carry the pinned byte-exact refusal, got %q", errText)
	}
	// The refusal leaves eka.yaml untouched.
	data, err := os.ReadFile(filepath.Join(repo, "eka.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "namespace: other") {
		t.Errorf("eka.yaml must stay untouched after the refusal:\n%s", data)
	}
}

// TestSyncOverrideAlignsIdentity (ADR-020): --override on a mismatched
// repository aligns the identity to the content — exit 0, eka.yaml
// rewritten, repos.namespace updated, the aligned note printed.
func TestSyncOverrideAlignsIdentity(t *testing.T) {
	t.Setenv("EKA_HOME", t.TempDir())
	repo := copySyncFixture(t)
	writeEkaYAML(t, repo, "eka-sync-fixture", "eka-sync-fixture", "other")

	code, text, errText := runIn([]string{"sync", "--override", repo})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", code, text, errText)
	}
	note := "repository namespace aligned: other → eka-sync-fixture (eka.yaml updated; identity frozen again)"
	if !strings.Contains(text, note) {
		t.Errorf("output must carry the aligned note, got:\n%s", text)
	}
	data, err := os.ReadFile(filepath.Join(repo, "eka.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "namespace: eka-sync-fixture") {
		t.Errorf("eka.yaml must be rewritten to the content namespace:\n%s", data)
	}
	w := mustWorkspace(t)
	t.Cleanup(func() { w.Close() })
	repos, err := w.Repos("eka-sync-fixture")
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 1 || repos[0].Namespace != "eka-sync-fixture" {
		t.Errorf("repos.namespace = %+v, want the aligned eka-sync-fixture", repos)
	}
}

// TestSyncOverrideOnConsistentRepo (ADR-020): --override on a
// repository whose content namespace already matches the declared one
// changes nothing — no aligned note, eka.yaml untouched.
func TestSyncOverrideOnConsistentRepo(t *testing.T) {
	t.Setenv("EKA_HOME", t.TempDir())
	repo := copySyncFixture(t)

	code, text, errText := runIn([]string{"sync", "--override", repo})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", code, text, errText)
	}
	if strings.Contains(text, "repository namespace aligned:") {
		t.Errorf("a consistent repository must not be aligned:\n%s", text)
	}
	data, err := os.ReadFile(filepath.Join(repo, "eka.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "namespace: eka-sync-fixture") {
		t.Errorf("eka.yaml must stay untouched:\n%s", data)
	}
}
