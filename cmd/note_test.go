package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/maleolabs/eka-core/metadata"
	"github.com/maleolabs/eka-core/workspace"
)

// This file tests `eka note` at CLI level (ADR-019 D8, revised): the
// note is created as a DRAFT under EKA_HOME/drafts (the repository docs
// tree is legacy authoring), with the discusses wiring, the role content
// contract, the deterministic id derivation, the machine report and the
// exit-code mapping.

// noteEnv builds a registered repository with one synced work item
// (sto:one at todo) and moves the working directory into it. The
// subject must be in the workspace store for the note draft.
func noteEnv(t *testing.T) (*workspace.Workspace, string) {
	t.Helper()
	gitIdentityEnv(t, "test-agent")
	t.Setenv("EKA_HOME", t.TempDir())
	w, err := workspace.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.Close() })
	repo := t.TempDir()
	writeEkaYAML(t, repo, "proj", "repo", "test-ns")
	m := metadata.Metadata{Version: 1, Project: "proj", Name: "repo", Namespace: "test-ns"}
	if _, _, _, err := w.RegisterRepoMetadata(repo, m); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(repo, "docs/operating/work-items/stories/sto-one.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	work := `{
  "namespace": "test-ns",
  "type": "sto",
  "id": "one",
  "instanceVersion": 1,
  "revision": 1,
  "author": "Engineering Architecture",
  "created": "2026-08-05",
  "updated": "2026-08-05",
  "state": {"executionState": "todo", "existenceState": "active"},
  "changeLog": [
    {"date": "2026-08-05", "domain": "existenceState", "from": "-", "to": "active", "by": "Engineering Architecture"},
    {"date": "2026-08-05", "domain": "executionState", "from": "-", "to": "planned", "by": "Engineering Architecture"},
    {"date": "2026-08-05", "domain": "executionState", "from": "planned", "to": "todo", "by": "Engineering Architecture"}
  ],
  "content": {"description": "The one story.", "acceptanceCriteria": "- Works."}
}
`
	if err := os.WriteFile(path, []byte(work), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(repo)
	if code, _, errText := runIn([]string{"sync"}); code != 0 {
		t.Fatalf("seed sync: exit = %d, stderr %q", code, errText)
	}
	return w, repo
}

// noteBody writes a note content JSON object and returns its path.
func noteBody(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "note-content.json")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// draftPathOf returns the workspace draft path of a note identity.
func draftPathOf(t *testing.T, id string) string {
	t.Helper()
	return filepath.Join(os.Getenv("EKA_HOME"), "drafts", "proj", "cmt-"+id+".json")
}

func TestNoteDraftHappyPath(t *testing.T) {
	_, repo := noteEnv(t)
	body := noteBody(t, `{"summary": "one implemented", "changes": ["x"], "tests": ["y"]}`)

	code, out, errText := runIn([]string{"note", "sto:one", "--role", "implementation", "--content-file", body, "--by", "agent-x"})
	if code != 0 {
		t.Fatalf("note: exit = %d, stderr %q", code, errText)
	}
	if !strings.Contains(out, "Note") || !strings.Contains(out, "Target") || !strings.Contains(out, "cmt:one-implementation") {
		t.Errorf("stdout = %q, want the themed note report", out)
	}
	if !strings.Contains(out, "eka publish to persist the note") {
		t.Errorf("stdout = %q, want the publish next-step", out)
	}
	// The draft exists under EKA_HOME/drafts (NOT in the repo docs).
	draftPath := draftPathOf(t, "one-implementation")
	raw, err := os.ReadFile(draftPath)
	if err != nil {
		t.Fatalf("draft file not found at %s: %v", draftPath, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("draft is not valid JSON: %v", err)
	}
	rels := doc["relationships"].(map[string]any)
	discusses := rels["discusses"].([]any)
	if discusses[0] != "test-ns/sto:one" {
		t.Errorf("draft discusses = %v, want the resolved subject", discusses)
	}
	state := doc["state"].(map[string]any)
	if state["noteState"] != "open" {
		t.Errorf("draft noteState = %v, want open (initial)", state["noteState"])
	}
	content := doc["content"].(map[string]any)
	if content["role"] != "implementation" || content["summary"] != "one implemented" {
		t.Errorf("draft content = %v, want the merged role content", content)
	}
	// The repo docs tree is untouched.
	docs, err := filepath.Glob(filepath.Join(repo, "docs", "**", "cmt-*"))
	if err != nil || len(docs) != 0 {
		t.Errorf("no cmt file may be written to the repo docs tree, got %v", docs)
	}
	// The note is NOT published yet: get refuses.
	if code, _, _ := runIn([]string{"get", "test-ns/cmt:one-implementation"}); code != 2 {
		t.Errorf("get on a draft note: exit = %d, want 2 (not published)", code)
	}
}

func TestNoteDraftPublishFlow(t *testing.T) {
	w, _ := noteEnv(t)
	body := noteBody(t, `{"summary": "one", "changes": [], "tests": []}`)

	if code, _, errText := runIn([]string{"note", "sto:one", "--role", "implementation", "--content-file", body, "--by", "agent-x"}); code != 0 {
		t.Fatalf("note: exit = %d, stderr %q", code, errText)
	}
	// Publish the draft: the note becomes an ordinary workspace unit.
	if code, _, errText := runIn([]string{"publish", "test-ns/cmt:one-implementation"}); code != 0 {
		t.Fatalf("publish note: exit = %d, stderr %q", code, errText)
	}
	code, out, errText := runIn([]string{"get", "test-ns/cmt:one-implementation"})
	if code != 0 {
		t.Fatalf("get published note: exit = %d, stderr %q", code, errText)
	}
	if !strings.Contains(out, `"canonicalForm": "test-ns/cmt:one-implementation:1"`) || !strings.Contains(out, `"noteState": "open"`) {
		t.Errorf("get = %q, want the note document with noteState", out)
	}
	// Timeline shows the note-state transitions.
	code, out, errText = runIn([]string{"get", "test-ns/cmt:one-implementation", "--timeline"})
	if code != 0 {
		t.Fatalf("get --timeline: exit = %d, stderr %q", code, errText)
	}
	if !strings.Contains(out, `"domain": "note-state"`) {
		t.Errorf("timeline = %q, want the note-state transitions", out)
	}
	// The draft is gone (single-use ticket).
	if _, err := os.Stat(draftPathOf(t, "one-implementation")); !os.IsNotExist(err) {
		t.Errorf("the draft file must be removed after publish")
	}
	// The gate sees the published note (open — not yet resolved). The
	// fixture has no active container, so --force confirms.
	code, _, errText = runIn([]string{"transition", "sto:one", "in-progress", "--force"})
	if code != 0 {
		t.Fatalf("transition to in-progress: exit = %d, stderr %q", code, errText)
	}
	if code, _, errText := runIn([]string{"transition", "sto:one", "in-review", "--force"}); code != 1 || !strings.Contains(errText, "transition gate R13") {
		t.Errorf("in-review with an open published note: exit = %d, stderr = %q, want the gate refusal", code, errText)
	}
	_ = w
}

func TestNoteIDDerivation(t *testing.T) {
	noteEnv(t)
	body := noteBody(t, `{"summary": "first", "changes": [], "tests": []}`)

	if code, _, errText := runIn([]string{"note", "sto:one", "--role", "implementation", "--content-file", body, "--by", "agent-x"}); code != 0 {
		t.Fatalf("first note: exit = %d, stderr %q", code, errText)
	}
	// A second note on the same subject derives a free identity.
	code, out, errText := runIn([]string{"note", "sto:one", "--role", "review", "--content-file", noteBody(t, `{"verdict": "approve", "notes": ["ok"]}`), "--by", "agent-x"})
	if code != 0 {
		t.Fatalf("second note: exit = %d, stderr %q", code, errText)
	}
	if !strings.Contains(out, "cmt:one-review") {
		t.Errorf("stdout = %q, want the derived note identity (one-review)", out)
	}
	if _, err := os.Stat(draftPathOf(t, "one-review")); err != nil {
		t.Errorf("second draft must exist: %v", err)
	}
}

func TestNoteJSONGolden(t *testing.T) {
	noteEnv(t)
	body := noteBody(t, `{"summary": "one", "changes": [], "tests": []}`)

	code, out, errText := runIn([]string{"note", "sto:one", "--role", "implementation", "--content-file", body, "--by", "agent-x", "--json"})
	if code != 0 {
		t.Fatalf("note --json: exit = %d, stderr %q", code, errText)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("--json output is not valid JSON: %v", err)
	}
	if doc["schema"] != "eka-note-v1" || doc["ok"] != true || doc["id"] != "one-implementation" ||
		doc["target"] != "test-ns/sto:one" || doc["by"] != "agent-x" {
		t.Errorf("--json = %v, want the pinned eka-note-v1 document", doc)
	}
	if draft, ok := doc["draft"].(string); !ok || draft == "" {
		t.Errorf("--json = %v, want the draft path field", doc)
	}
}

func TestNoteUsageAndRefusals(t *testing.T) {
	noteEnv(t)

	// Unknown role: exit 2 (usage).
	if code, _, errText := runIn([]string{"note", "sto:one", "--role", "bogus", "--by", "a"}); code != 2 || !strings.Contains(errText, "unknown role") {
		t.Errorf("unknown role: exit = %d, stderr = %q, want 2", code, errText)
	}
	// Missing --by source: exit 2.
	gitIdentityEnv(t, "")
	if code, _, errText := runIn([]string{"note", "sto:one", "--role", "review"}); code != 2 || !strings.Contains(errText, "pass --by <name>") {
		t.Errorf("missing by: exit = %d, stderr = %q, want 2", code, errText)
	}
	gitIdentityEnv(t, "test-agent")
	// Subject not in the workspace store: exit 1 (refusal + sync hint).
	if code, _, errText := runIn([]string{"note", "sto:missing", "--role", "review", "--by", "a"}); code != 1 || !strings.Contains(errText, "run 'eka sync' first") {
		t.Errorf("missing subject: exit = %d, stderr = %q, want 1 + sync hint", code, errText)
	}
	// Malformed target: exit 2.
	if code, _, _ := runIn([]string{"note", "bad-target", "--role", "review", "--by", "a"}); code != 2 {
		t.Errorf("malformed target: exit = %d, want 2", code)
	}
	// Not an EKA repository: exit 1 (refusal).
	dir := t.TempDir()
	old, _ := os.Getwd()
	t.Chdir(dir)
	defer t.Chdir(old)
	if code, _, errText := runIn([]string{"note", "sto:x", "--role", "review", "--by", "a"}); code != 1 || !strings.Contains(errText, "not an EKA repository") {
		t.Errorf("no eka.yaml: exit = %d, stderr = %q, want 1", code, errText)
	}
}

// noteEnvWithDraftSubject builds the note env plus one subject that
// exists ONLY as a draft of the project (eka new scaffolds
// EKA_HOME/drafts/proj/sto-two.json; never synced into the store).
func noteEnvWithDraftSubject(t *testing.T) {
	t.Helper()
	noteEnv(t)
	if code, _, errText := runIn([]string{"new", "sto:two"}); code != 0 {
		t.Fatalf("seed subject draft: exit = %d, stderr %q", code, errText)
	}
	subjectDraft := filepath.Join(os.Getenv("EKA_HOME"), "drafts", "proj", "sto-two.json")
	if _, err := os.Stat(subjectDraft); err != nil {
		t.Fatalf("subject draft not found at %s: %v", subjectDraft, err)
	}
	// The subject is NOT in the store: get on the line refuses.
	if code, _, errText := runIn([]string{"get", "test-ns/sto:two"}); code != 2 {
		t.Fatalf("get on draft subject: exit = %d, stderr %q, want 2 (not published)", code, errText)
	}
}

// TestNoteDraftSubjectDraftTolerance: `eka note` accepts a subject that
// exists only as a draft of the same project (draft tolerance — the
// skill records evidence BEFORE the subject is approved). The human
// report marks the draft resolution.
func TestNoteDraftSubjectDraftTolerance(t *testing.T) {
	noteEnvWithDraftSubject(t)
	body := noteBody(t, `{"summary": "two implemented", "changes": ["x"], "tests": ["y"]}`)

	code, out, errText := runIn([]string{"note", "sto:two", "--role", "implementation", "--content-file", body, "--by", "agent-x"})
	if code != 0 {
		t.Fatalf("note to draft subject: exit = %d, stderr %q", code, errText)
	}
	if !strings.Contains(out, "test-ns/sto:two (draft)") {
		t.Errorf("stdout = %q, want the draft-resolved subject marker", out)
	}
	// The note draft exists and carries the discusses edge to the draft
	// subject line.
	draftPath := draftPathOf(t, "two-implementation")
	raw, err := os.ReadFile(draftPath)
	if err != nil {
		t.Fatalf("draft file not found at %s: %v", draftPath, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("draft is not valid JSON: %v", err)
	}
	rels := doc["relationships"].(map[string]any)
	discusses := rels["discusses"].([]any)
	if discusses[0] != "test-ns/sto:two" {
		t.Errorf("draft discusses = %v, want the draft subject line", discusses)
	}
	// The subject draft is untouched (the note did not publish it).
	if _, err := os.Stat(filepath.Join(os.Getenv("EKA_HOME"), "drafts", "proj", "sto-two.json")); err != nil {
		t.Errorf("subject draft must survive the note: %v", err)
	}
}

// TestNoteDraftSubjectDraftToleranceJSON: the machine report records
// the draft resolution through the subjectState field.
func TestNoteDraftSubjectDraftToleranceJSON(t *testing.T) {
	noteEnvWithDraftSubject(t)
	body := noteBody(t, `{"summary": "two", "changes": [], "tests": []}`)

	code, out, errText := runIn([]string{"note", "sto:two", "--role", "implementation", "--content-file", body, "--by", "agent-x", "--json"})
	if code != 0 {
		t.Fatalf("note --json to draft subject: exit = %d, stderr %q", code, errText)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("--json output is not valid JSON: %v", err)
	}
	if doc["schema"] != "eka-note-v1" || doc["ok"] != true || doc["id"] != "two-implementation" ||
		doc["target"] != "test-ns/sto:two" || doc["subjectState"] != "draft" {
		t.Errorf("--json = %v, want the draft-subject document (subjectState draft)", doc)
	}
}

// TestNoteDraftSubjectDraftGone: a subject that is neither in the store
// nor a draft of the project stays refused with the same message.
func TestNoteDraftSubjectDraftGone(t *testing.T) {
	noteEnvWithDraftSubject(t)
	// Discard the subject draft: the note must refuse again.
	if code, _, errText := runIn([]string{"discard", "test-ns/sto:two", "--force"}); code != 0 {
		t.Fatalf("discard subject draft: exit = %d, stderr %q", code, errText)
	}
	body := noteBody(t, `{"summary": "two", "changes": [], "tests": []}`)
	code, _, errText := runIn([]string{"note", "sto:two", "--role", "implementation", "--content-file", body, "--by", "agent-x"})
	if code != 1 || !strings.Contains(errText, "was not found in the workspace store") || !strings.Contains(errText, "run 'eka sync' first") {
		t.Errorf("note to discarded subject: exit = %d, stderr = %q, want 1 + sync hint", code, errText)
	}
}
