package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// This file tests the R13 enforcement at the sync/validate gate
// (ADR-019 D6; spec §10 item 6): a work item that reaches in-review or
// done without satisfying the gates fails validation — `eka sync` is
// refused and `eka validate` reports the identical R13 findings.

// writeGateRepo writes a repository whose docs tree carries one work
// item at in-review (no notes by default).
func writeGateRepo(t *testing.T, repo string, withNote bool) {
	t.Helper()
	writeEkaYAML(t, repo, "proj", "repo", "test-ns")
	work := `{
  "namespace": "test-ns",
  "type": "sto",
  "id": "gated",
  "instanceVersion": 1,
  "revision": 1,
  "author": "Engineering Architecture",
  "created": "2026-08-05",
  "updated": "2026-08-05",
  "state": {
    "executionState": "in-review",
    "existenceState": "active"
  },
  "changeLog": [
    {"date": "2026-08-05", "domain": "existenceState", "from": "-", "to": "active", "by": "Engineering Architecture"},
    {"date": "2026-08-05", "domain": "executionState", "from": "-", "to": "planned", "by": "Engineering Architecture"},
    {"date": "2026-08-05", "domain": "executionState", "from": "planned", "to": "todo", "by": "Engineering Architecture"},
    {"date": "2026-08-05", "domain": "executionState", "from": "todo", "to": "in-progress", "by": "Engineering Architecture"},
    {"date": "2026-08-05", "domain": "executionState", "from": "in-progress", "to": "in-review", "by": "Engineering Architecture"}
  ],
  "content": {
    "description": "The gated story.",
    "acceptanceCriteria": "- Gated works."
  }
}
`
	path := filepath.Join(repo, "docs/operating/work-items/stories/sto-gated.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(work), 0o644); err != nil {
		t.Fatal(err)
	}
	if withNote {
		note := `{
  "namespace": "test-ns",
  "type": "cmt",
  "id": "gated-implementation",
  "instanceVersion": 1,
  "revision": 1,
  "author": "Engineering Architecture",
  "created": "2026-08-05",
  "updated": "2026-08-05",
  "domain": "Execution",
  "state": {"contentState": "draft", "existenceState": "active", "noteState": "resolved"},
  "relationships": {"discusses": ["test-ns/sto:gated"]},
  "changeLog": [
    {"date": "2026-08-05", "domain": "contentState", "from": "-", "to": "draft", "by": "Engineering Architecture"},
    {"date": "2026-08-05", "domain": "existenceState", "from": "-", "to": "active", "by": "Engineering Architecture"},
    {"date": "2026-08-05", "domain": "noteState", "from": "-", "to": "open", "by": "Engineering Architecture"},
    {"date": "2026-08-05", "domain": "noteState", "from": "open", "to": "resolved", "by": "Engineering Architecture"}
  ],
  "content": {"role": "implementation", "summary": "gated implemented", "changes": ["x"], "tests": ["y"]}
}
`
		npath := filepath.Join(repo, "docs/operating/notes/cmt-gated-implementation.json")
		if err := os.MkdirAll(filepath.Dir(npath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(npath, []byte(note), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// gateEnv builds the R13 gate-test environment and moves into the repo.
func gateEnv(t *testing.T, withNote bool) string {
	t.Helper()
	t.Setenv("EKA_HOME", t.TempDir())
	repo := t.TempDir()
	writeGateRepo(t, repo, withNote)
	t.Chdir(repo)
	return repo
}

func TestSyncEnforcesGateR13(t *testing.T) {
	gateEnv(t, false)

	// `eka sync` (docs mode) is refused: the work item at in-review has
	// no resolved implementation note.
	code, out, errText := runIn([]string{"sync"})
	if code != 1 {
		t.Fatalf("sync without note: exit = %d, want 1", code)
	}
	if !strings.Contains(errText, "knowledge compile refused") {
		t.Errorf("sync stderr = %q, want the compile refusal summary", errText)
	}
	if !strings.Contains(out, "transition gate R13") {
		t.Errorf("sync report = %q, want the R13 gate finding", out)
	}
}

func TestValidateReportsSameR13Finding(t *testing.T) {
	gateEnv(t, false)

	code, _, _ := runIn([]string{"validate"})
	if code != 1 {
		t.Fatalf("validate without note: exit = %d, want 1", code)
	}
}

func TestSyncPassesWithResolvedNote(t *testing.T) {
	gateEnv(t, true)

	code, out, errText := runIn([]string{"sync"})
	if code != 0 {
		t.Fatalf("sync with note: exit = %d, stderr %q", code, errText)
	}
	if !strings.Contains(out, "Pull: docs: 2 units") {
		t.Errorf("sync stdout = %q, want the sync report with the seeded units", out)
	}
	// The synced store holds the note unit (ordinary unit).
	code, out, errText = runIn([]string{"get", "test-ns/cmt:gated-implementation"})
	if code != 0 {
		t.Fatalf("get note after sync: exit = %d, stderr %q", code, errText)
	}
	if !strings.Contains(out, `"canonicalForm": "test-ns/cmt:gated-implementation:1"`) {
		t.Errorf("get = %q, want the note document", out)
	}
}

func TestDoneGateListsOpenNotes(t *testing.T) {
	t.Setenv("EKA_HOME", t.TempDir())
	repo := t.TempDir()
	writeEkaYAML(t, repo, "proj", "repo", "test-ns")
	// Work item at done + one open note: the done gate lists the open
	// note identity.
	work := `{
  "namespace": "test-ns",
  "type": "sto",
  "id": "done-gated",
  "instanceVersion": 1,
  "revision": 1,
  "author": "Engineering Architecture",
  "created": "2026-08-05",
  "updated": "2026-08-05",
  "state": {"executionState": "done", "existenceState": "active"},
  "changeLog": [
    {"date": "2026-08-05", "domain": "existenceState", "from": "-", "to": "active", "by": "Engineering Architecture"},
    {"date": "2026-08-05", "domain": "executionState", "from": "-", "to": "planned", "by": "Engineering Architecture"},
    {"date": "2026-08-05", "domain": "executionState", "from": "planned", "to": "todo", "by": "Engineering Architecture"},
    {"date": "2026-08-05", "domain": "executionState", "from": "todo", "to": "in-progress", "by": "Engineering Architecture"},
    {"date": "2026-08-05", "domain": "executionState", "from": "in-progress", "to": "in-review", "by": "Engineering Architecture"},
    {"date": "2026-08-05", "domain": "executionState", "from": "in-review", "to": "done", "by": "Engineering Architecture"}
  ],
  "content": {"description": "The done-gated story.", "acceptanceCriteria": "- Works."}
}
`
	path := filepath.Join(repo, "docs/operating/work-items/stories/sto-done-gated.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(work), 0o644); err != nil {
		t.Fatal(err)
	}
	note := `{
  "namespace": "test-ns",
  "type": "cmt",
  "id": "done-gated-review",
  "instanceVersion": 1,
  "revision": 1,
  "author": "Engineering Architecture",
  "created": "2026-08-05",
  "updated": "2026-08-05",
  "domain": "Execution",
  "state": {"contentState": "draft", "existenceState": "active", "noteState": "open"},
  "relationships": {"discusses": ["test-ns/sto:done-gated"]},
  "changeLog": [
    {"date": "2026-08-05", "domain": "contentState", "from": "-", "to": "draft", "by": "Engineering Architecture"},
    {"date": "2026-08-05", "domain": "existenceState", "from": "-", "to": "active", "by": "Engineering Architecture"},
    {"date": "2026-08-05", "domain": "noteState", "from": "-", "to": "open", "by": "Engineering Architecture"}
  ],
  "content": {"role": "review", "verdict": "changes-requested", "notes": ["fix the parser"]}
}
`
	npath := filepath.Join(repo, "docs/operating/notes/cmt-done-gated-review.json")
	if err := os.MkdirAll(filepath.Dir(npath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(npath, []byte(note), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(repo)

	code, out, _ := runIn([]string{"validate"})
	if code != 1 {
		t.Fatalf("validate with open note: exit = %d, want 1", code)
	}
	if !strings.Contains(out, "open notes: test-ns/cmt:done-gated-review") {
		t.Errorf("validate stdout = %q, want the open-note identities listed", out)
	}
}
