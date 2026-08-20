package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"github.com/maleolabs/eka-core/metadata"
	"github.com/maleolabs/eka-core/workspace"
)

// This file tests `eka transition` at CLI level (ADR-019 D2, revised):
// the D1 transition table, the --forward/--backward derivation, the R13
// gate over the workspace notes (published units + EKA_HOME/drafts),
// the active-container warning + confirmation, the machine report and
// the publish-to-store semantics (the repository docs tree is never
// touched).

// gitIdentityEnv pins `git config user.name` to a deterministic value
// for the duration of the test (GIT_CONFIG_GLOBAL), so the --by default
// resolution is testable without touching the user's git configuration.
// The TestMain PATH pin (plugin-registration hermeticity) is lifted for
// the duration of the test: eka-core resolves the identity by running
// git via exec.Command, which needs git on PATH.
func gitIdentityEnv(t *testing.T, name string) {
	t.Helper()
	cfg := filepath.Join(t.TempDir(), "gitconfig")
	if name == "" {
		if err := os.WriteFile(cfg, []byte(""), 0o644); err != nil {
			t.Fatal(err)
		}
	} else {
		content := "[user]\n\tname = " + name + "\n"
		if err := os.WriteFile(cfg, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("GIT_CONFIG_GLOBAL", cfg)
	if testOrigPath != "" {
		t.Setenv("PATH", testOrigPath)
	}
}

// transitionEnv builds a repository whose docs tree seeds the workspace
// store: an active container (ctr-wave-1, depends-on plan:roadmap-v1),
// two plans (plan:roadmap-v1 approved, plan:roadmap-v2 draft), a
// ticket (tkt-t1) deriving from the container AND referencing sto:one,
// the work items sto:one (todo), sto:two (in-progress), sto:done
// (done), sto:orphan (planned, referenced by NO ticket), and a
// resolved implementation note for sto:one. The repository is
// registered, synced, and the working directory moves into it.
func transitionEnv(t *testing.T) (*workspace.Workspace, string) {
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
	writeTransitionFixture(t, repo)
	t.Chdir(repo)
	if code, _, errText := runIn([]string{"sync"}); code != 0 {
		t.Fatalf("seed sync: exit = %d, stderr %q", code, errText)
	}
	return w, repo
}

// writeTransitionFixture writes the docs tree of the transition test
// repository (conformant, so the seed sync passes).
func writeTransitionFixture(t *testing.T, repo string) {
	t.Helper()
	write := func(rel, content string) {
		path := filepath.Join(repo, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("docs/operating/containers/ctr-wave-1.json", `{
  "namespace": "test-ns",
  "type": "ctr",
  "id": "wave-1",
  "instanceVersion": 1,
  "revision": 1,
  "author": "Engineering Architecture",
  "created": "2026-08-05",
  "updated": "2026-08-05",
  "state": {"containerState": "active", "existenceState": "active"},
  "relationships": {"dependsOn": ["test-ns/plan:roadmap-v1"]},
  "changeLog": [
    {"date": "2026-08-05", "domain": "containerState", "from": "-", "to": "active", "by": "Engineering Architecture"},
    {"date": "2026-08-05", "domain": "existenceState", "from": "-", "to": "active", "by": "Engineering Architecture"}
  ],
  "content": {"objective": "wave one", "workItems": "", "changeLog": ""}
}
`)
	// wave-2 and wave-3 are PLANNED containers (Option B: containers
	// are born planned; activation locks the plan). wave-2 derives from
	// the approved roadmap-v1, wave-3 from the draft roadmap-v2.
	ctr := func(id, planID string) string {
		return `{
  "namespace": "test-ns",
  "type": "ctr",
  "id": "` + id + `",
  "instanceVersion": 1,
  "revision": 1,
  "author": "Engineering Architecture",
  "created": "2026-08-05",
  "updated": "2026-08-05",
  "state": {"containerState": "planned", "existenceState": "active"},
  "relationships": {"dependsOn": ["test-ns/plan:` + planID + `"]},
  "changeLog": [
    {"date": "2026-08-05", "domain": "containerState", "from": "-", "to": "planned", "by": "Engineering Architecture"},
    {"date": "2026-08-05", "domain": "existenceState", "from": "-", "to": "active", "by": "Engineering Architecture"}
  ],
  "content": {"objective": "wave ` + id + `", "workItems": "", "changeLog": ""}
}
`
	}
	write("docs/operating/containers/ctr-wave-2.json", ctr("wave-2", "roadmap-v1"))
	write("docs/operating/containers/ctr-wave-3.json", ctr("wave-3", "roadmap-v2"))
	plan := func(id, planningState string) string {
		return `{
  "namespace": "test-ns",
  "type": "plan",
  "id": "` + id + `",
  "instanceVersion": 1,
  "revision": 1,
  "author": "Engineering Architecture",
  "created": "2026-08-05",
  "updated": "2026-08-05",
  "dimension": "planning",
  "state": {"contentState": "draft", "planningState": "` + planningState + `", "existenceState": "active"},
  "changeLog": [
    {"date": "2026-08-05", "domain": "contentState", "from": "-", "to": "draft", "by": "Engineering Architecture"},
    {"date": "2026-08-05", "domain": "planningState", "from": "-", "to": "` + planningState + `", "by": "Engineering Architecture"},
    {"date": "2026-08-05", "domain": "existenceState", "from": "-", "to": "active", "by": "Engineering Architecture"}
  ],
  "content": {"objective": "roadmap ` + id + `", "scope": "all", "outOfScope": "none"}
}
`
	}
	write("docs/planning/plan-roadmap-v1-v1.json", plan("roadmap-v1", "approved"))
	write("docs/planning/plan-roadmap-v2-v1.json", plan("roadmap-v2", "draft"))
	ticket := func(id, target string) string {
		return `{
  "namespace": "test-ns",
  "type": "tkt",
  "id": "` + id + `",
  "instanceVersion": 1,
  "revision": 1,
  "author": "Engineering Architecture",
  "created": "2026-08-05",
  "updated": "2026-08-05",
  "relationships": {"derivesFrom": ["ctr:wave-1", "` + target + `"]},
  "changeLog": [],
  "content": {
    "commands": "> Generated \u2014 State Projection. Do NOT edit state here; refresh on read.\n",
    "projectedStatus": ""
  }
}
`
	}
	write("docs/operating/projections/tkt-t1.json", ticket("t1", "sto:one"))
	write("docs/operating/projections/tkt-t2.json", ticket("t2", "sto:two"))
	write("docs/operating/projections/tkt-t3.json", ticket("t3", "sto:done"))
	work := func(id, state string, log []string) string {
		var logLines []string
		for _, e := range log {
			logLines = append(logLines, `    {"date": "2026-08-05", "domain": "executionState", `+e+`, "by": "Engineering Architecture"}`)
		}
		entries := ""
		if len(logLines) > 0 {
			entries = strings.Join(logLines, ",\n") + "\n"
		}
		return `{
  "namespace": "test-ns",
  "type": "sto",
  "id": "` + id + `",
  "instanceVersion": 1,
  "revision": 1,
  "author": "Engineering Architecture",
  "created": "2026-08-05",
  "updated": "2026-08-05",
  "state": {"executionState": "` + state + `", "existenceState": "active"},
  "changeLog": [
    {"date": "2026-08-05", "domain": "existenceState", "from": "-", "to": "active", "by": "Engineering Architecture"},
` + entries + `  ],
  "content": {"description": "The ` + id + ` story.", "acceptanceCriteria": "- Works."}
}
`
	}
	write("docs/operating/work-items/stories/sto-one.json", work("one", "todo", []string{
		`"from": "-", "to": "planned"`, `"from": "planned", "to": "todo"`,
	}))
	write("docs/operating/work-items/stories/sto-two.json", work("two", "in-progress", []string{
		`"from": "-", "to": "planned"`, `"from": "planned", "to": "todo"`, `"from": "todo", "to": "in-progress"`,
	}))
	write("docs/operating/work-items/stories/sto-done.json", work("done", "done", []string{
		`"from": "-", "to": "planned"`, `"from": "planned", "to": "todo"`, `"from": "todo", "to": "in-progress"`,
		`"from": "in-progress", "to": "in-review"`, `"from": "in-review", "to": "done"`,
	}))
	write("docs/operating/work-items/stories/sto-orphan.json", work("orphan", "planned", []string{
		`"from": "-", "to": "planned"`,
	}))
	write("docs/operating/notes/cmt-one-implementation.json", `{
  "namespace": "test-ns",
  "type": "cmt",
  "id": "one-implementation",
  "instanceVersion": 1,
  "revision": 1,
  "author": "Engineering Architecture",
  "created": "2026-08-05",
  "updated": "2026-08-05",
  "domain": "Execution",
  "state": {"contentState": "draft", "existenceState": "active", "noteState": "resolved"},
  "relationships": {"discusses": ["test-ns/sto:one"]},
  "changeLog": [
    {"date": "2026-08-05", "domain": "contentState", "from": "-", "to": "draft", "by": "Engineering Architecture"},
    {"date": "2026-08-05", "domain": "existenceState", "from": "-", "to": "active", "by": "Engineering Architecture"},
    {"date": "2026-08-05", "domain": "noteState", "from": "-", "to": "open", "by": "Engineering Architecture"},
    {"date": "2026-08-05", "domain": "noteState", "from": "open", "to": "resolved", "by": "Engineering Architecture"}
  ],
  "content": {"role": "implementation", "summary": "one implemented", "changes": ["x"], "tests": ["y"]}
}
`)
}

// readRel reads a fixture file relative to the repo root.
func readRel(t *testing.T, repo, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repo, rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// storeUnitState reads the current execution-state of a line from the
// workspace store and its change-log length.
func storeUnitState(t *testing.T, w *workspace.Workspace, ns, typ, id string) (string, int) {
	t.Helper()
	units, err := w.Store().UnitsByLine(ns, typ, id)
	if err != nil || len(units) == 0 {
		t.Fatalf("UnitsByLine(%s, %s, %s) = %d units (err %v)", ns, typ, id, len(units), err)
	}
	u := units[0]
	for _, cand := range units {
		if cand.Identity.InstanceVersion > u.Identity.InstanceVersion {
			u = cand
		}
	}
	return u.StateVector.ExecutionState, len(u.ChangeLog)
}

func TestTransitionPublishesToStore(t *testing.T) {
	w, repo := transitionEnv(t)
	docsBefore := readRel(t, repo, "docs/operating/work-items/stories/sto-one.json")

	code, out, errText := runIn([]string{"transition", "sto:one", "in-progress"})
	if code != 0 {
		t.Fatalf("transition: exit = %d, stderr %q", code, errText)
	}
	if !strings.Contains(out, "Transition") || !strings.Contains(out, "todo") || !strings.Contains(out, "in-progress") {
		t.Errorf("stdout = %q, want the themed transition report", out)
	}
	if !strings.Contains(out, "Object: test-ns/sto:one") || !strings.Contains(out, "run 'eka sync push'") {
		t.Errorf("stdout = %q, want the summary with the sync-push hint", out)
	}
	// The store now holds the transitioned state; the change-log grew.
	state, logLen := storeUnitState(t, w, "test-ns", "sto", "one")
	if state != "in-progress" {
		t.Errorf("store state = %q, want in-progress", state)
	}
	if logLen != 4 {
		t.Errorf("changeLog entries = %d, want 4 (one appended)", logLen)
	}
	// The repository docs tree is untouched (legacy authoring path).
	docsAfter := readRel(t, repo, "docs/operating/work-items/stories/sto-one.json")
	if docsBefore != docsAfter {
		t.Errorf("the repository docs tree must not be touched by a transition")
	}
}

func TestTransitionForwardBackwardFlags(t *testing.T) {
	w, _ := transitionEnv(t)

	// --forward: todo -> in-progress (the next sequential step).
	if code, out, errText := runIn([]string{"transition", "sto:one", "--forward"}); code != 0 || !strings.Contains(out, "in-progress") {
		t.Fatalf("--forward: exit = %d, stdout %q, stderr %q", code, out, errText)
	}
	if state, _ := storeUnitState(t, w, "test-ns", "sto", "one"); state != "in-progress" {
		t.Errorf("--forward state = %q, want in-progress", state)
	}
	// --forward again: in-progress -> in-review (the published resolved
	// implementation note gate-satisfies).
	if code, _, errText := runIn([]string{"transition", "sto:one", "--forward"}); code != 0 {
		t.Fatalf("--forward to in-review: exit = %d, stderr %q", code, errText)
	}
	// --backward: in-progress -> todo (one-step pull-back).
	if code, _, errText := runIn([]string{"transition", "sto:two", "--backward"}); code != 0 {
		t.Fatalf("--backward: exit = %d, stderr %q", code, errText)
	}
	if state, _ := storeUnitState(t, w, "test-ns", "sto", "two"); state != "todo" {
		t.Errorf("--backward state = %q, want todo", state)
	}
	// --backward from todo: no pull-back step (refusal).
	if code, _, errText := runIn([]string{"transition", "sto:two", "--backward"}); code != 1 || !strings.Contains(errText, "no backward transition") {
		t.Errorf("--backward from todo: exit = %d, stderr = %q, want 1 + refusal", code, errText)
	}
	// --forward from done: no forward step (refusal).
	if code, _, errText := runIn([]string{"transition", "sto:done", "--forward"}); code != 1 || !strings.Contains(errText, "no forward transition") {
		t.Errorf("--forward from done: exit = %d, stderr = %q, want 1 + refusal", code, errText)
	}
	// Conflicting destinations: usage errors.
	if code, _, _ := runIn([]string{"transition", "sto:one", "todo", "--forward"}); code != 2 {
		t.Errorf("to + --forward: exit = %d, want 2", code)
	}
	if code, _, _ := runIn([]string{"transition", "sto:one", "--forward", "--backward"}); code != 2 {
		t.Errorf("--forward + --backward: exit = %d, want 2", code)
	}
	if code, _, _ := runIn([]string{"transition", "sto:one"}); code != 2 {
		t.Errorf("no destination: exit = %d, want 2", code)
	}
}

func TestTransitionCancelAndReactivate(t *testing.T) {
	w, _ := transitionEnv(t)

	if code, _, errText := runIn([]string{"transition", "sto:done", "canceled"}); code != 0 {
		t.Fatalf("done -> canceled: exit = %d, stderr %q", code, errText)
	}
	if state, _ := storeUnitState(t, w, "test-ns", "sto", "done"); state != "canceled" {
		t.Errorf("state = %q, want canceled", state)
	}
	if code, _, errText := runIn([]string{"transition", "sto:done", "todo"}); code != 0 {
		t.Fatalf("canceled -> todo: exit = %d, stderr %q", code, errText)
	}
	if state, _ := storeUnitState(t, w, "test-ns", "sto", "done"); state != "todo" {
		t.Errorf("state = %q, want todo (re-activated)", state)
	}
}

func TestTransitionIllegal(t *testing.T) {
	transitionEnv(t)

	cases := []struct {
		name string
		args []string
	}{
		{"revert to planned", []string{"transition", "sto:one", "planned"}},
		{"skip forward", []string{"transition", "sto:one", "in-review"}},
		{"done to in-progress", []string{"transition", "sto:done", "in-progress"}},
		{"canceled to planned", []string{"transition", "sto:done", "planned"}},
	}
	for _, c := range cases {
		code, _, errText := runIn(c.args)
		if code != 1 {
			t.Errorf("%s: exit = %d, want 1", c.name, code)
		}
		if !strings.Contains(errText, "transition refused") || !strings.Contains(errText, "D1") {
			t.Errorf("%s: stderr = %q, want the deterministic refusal naming the D1 table", c.name, errText)
		}
		if !strings.Contains(errText, "legal transitions from") {
			t.Errorf("%s: stderr = %q, want the legal-transitions hint", c.name, errText)
		}
	}
}

func TestTransitionGateUnmetAndMet(t *testing.T) {
	w, _ := transitionEnv(t)

	// sto:two has no note: in-progress -> in-review is refused by the
	// early R13 gate check (over published units + drafts).
	code, _, errText := runIn([]string{"transition", "sto:two", "in-review"})
	if code != 1 {
		t.Fatalf("gate-unmet transition: exit = %d, want 1", code)
	}
	if !strings.Contains(errText, "transition gate R13") {
		t.Errorf("stderr = %q, want the R13 gate refusal", errText)
	}

	// Create an implementation note draft, resolve it, and the gate
	// passes — the draft is visible to the gate without publishing.
	body := noteBody(t, `{"summary": "two implemented", "changes": ["x"], "tests": ["y"]}`)
	if code, _, errText := runIn([]string{"note", "sto:two", "--role", "implementation", "--content-file", body, "--by", "agent-x"}); code != 0 {
		t.Fatalf("note draft: exit = %d, stderr %q", code, errText)
	}
	// The open draft does not gate-satisfy yet.
	if code, _, _ := runIn([]string{"transition", "sto:two", "in-review"}); code != 1 {
		t.Errorf("open draft note: transition exit = %d, want 1 (gate unmet)", code)
	}
	// Resolve the draft note in place.
	draftPath := filepath.Join(os.Getenv("EKA_HOME"), "drafts", "proj", "cmt-two-implementation.json")
	raw, err := os.ReadFile(draftPath)
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(string(raw), `"noteState": "open"`, `"noteState": "resolved"`, 1)
	if err := os.WriteFile(draftPath, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, errText := runIn([]string{"transition", "sto:two", "in-review"}); code != 0 {
		t.Fatalf("in-review with resolved draft note: exit = %d, stderr %q", code, errText)
	}
	if state, _ := storeUnitState(t, w, "test-ns", "sto", "two"); state != "in-review" {
		t.Errorf("state = %q, want in-review", state)
	}
}

func TestTransitionNotRegisteredWarning(t *testing.T) {
	_, _ = transitionEnv(t)

	// sto:orphan is referenced by NO ticket of the active container:
	// the warning banner renders and the non-TTY run refuses without
	// --force.
	code, _, errText := runIn([]string{"transition", "sto:orphan", "--forward"})
	if code != 1 {
		t.Fatalf("unregistered without --force: exit = %d, want 1", code)
	}
	if !strings.Contains(errText, "eka: warning: test-ns/sto:orphan is not registered in the current active container") {
		t.Errorf("stderr = %q, want the warning banner", errText)
	}
	if !strings.Contains(errText, "pass --force") {
		t.Errorf("stderr = %q, want the --force hint", errText)
	}
	// --force proceeds (agents) and the warning travels in the JSON.
	code, out, errText := runIn([]string{"transition", "sto:orphan", "--forward", "--force", "--json"})
	if code != 0 {
		t.Fatalf("unregistered with --force: exit = %d, stderr %q", code, errText)
	}
	if !strings.Contains(out, `"ok":true`) || !strings.Contains(out, `"warning"`) || !strings.Contains(out, "not registered") {
		t.Errorf("--json = %q, want ok:true with the warning field", out)
	}
}

func TestTransitionByAndJSONGolden(t *testing.T) {
	transitionEnv(t)

	code, out, errText := runIn([]string{"transition", "sto:one", "in-progress", "--by", "flag-agent", "--json"})
	if code != 0 {
		t.Fatalf("--json transition: exit = %d, stderr %q", code, errText)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("--json output is not valid JSON: %v", err)
	}
	if doc["schema"] != "eka-transition-v1" || doc["ok"] != true || doc["target"] != "test-ns/sto:one" ||
		doc["from"] != "todo" || doc["to"] != "in-progress" || doc["by"] != "flag-agent" {
		t.Errorf("--json = %v, want the pinned eka-transition-v1 document", doc)
	}
	if _, has := doc["warning"]; has {
		t.Errorf("--json = %v, want no warning field for a registered work item", doc)
	}
	if hash, ok := doc["objectHash"].(string); !ok || hash == "" {
		t.Errorf("--json = %v, want the object hash", doc)
	}
	// The git default identity is used without --by.
	code, out, errText = runIn([]string{"transition", "sto:one", "--forward", "--json"})
	if code != 0 {
		t.Fatalf("default-by transition: exit = %d, stderr %q", code, errText)
	}
	if !strings.Contains(out, `"by":"test-agent"`) {
		t.Errorf("--json = %q, want the git-default by", out)
	}
}

func TestTransitionRefusals(t *testing.T) {
	transitionEnv(t)

	// Missing --by source: exit 2.
	gitIdentityEnv(t, "")
	if code, _, errText := runIn([]string{"transition", "sto:one", "--forward"}); code != 2 || !strings.Contains(errText, "pass --by <name>") {
		t.Errorf("missing by: exit = %d, stderr = %q, want 2", code, errText)
	}
	gitIdentityEnv(t, "test-agent")
	// Target not in the workspace store: exit 1 + sync hint.
	if code, _, errText := runIn([]string{"transition", "sto:missing", "--forward"}); code != 1 || !strings.Contains(errText, "run 'eka sync' first") {
		t.Errorf("missing work item: exit = %d, stderr = %q, want 1 + sync hint", code, errText)
	}
	// Non-transitionable target (neither work item, plan, container nor
	// a content-state-owning artifact): exit 2.
	if code, _, errText := runIn([]string{"transition", "tkt:t1", "todo"}); code != 2 || !strings.Contains(errText, "not transitionable") {
		t.Errorf("non-transitionable target: exit = %d, stderr = %q, want 2", code, errText)
	}
	// Malformed target: exit 2.
	if code, _, _ := runIn([]string{"transition", "not-a-target", "todo"}); code != 2 {
		t.Errorf("malformed target: exit = %d, want 2", code)
	}
	// No EKA repository: exit 1 (refusal).
	dir := t.TempDir()
	old, _ := os.Getwd()
	t.Chdir(dir)
	defer t.Chdir(old)
	if code, _, errText := runIn([]string{"transition", "sto:x", "--forward", "--by", "a"}); code != 1 || !strings.Contains(errText, "not an EKA repository") {
		t.Errorf("no eka.yaml: exit = %d, stderr = %q, want 1 + refusal", code, errText)
	}
}

func TestTransitionGetAndTimeline(t *testing.T) {
	w, _ := transitionEnv(t)

	if code, _, errText := runIn([]string{"transition", "sto:one", "in-progress"}); code != 0 {
		t.Fatalf("transition: exit = %d, stderr %q", code, errText)
	}
	// The machine interface shows the current state (line resolution).
	code, out, errText := runIn([]string{"get", "test-ns/sto:one"})
	if code != 0 {
		t.Fatalf("get: exit = %d, stderr %q", code, errText)
	}
	if !strings.Contains(out, `"executionState": "in-progress"`) {
		t.Errorf("get = %q, want the transitioned state", out)
	}
	// The timeline renders the change-log transitions.
	code, out, errText = runIn([]string{"get", "test-ns/sto:one", "--timeline"})
	if code != 0 {
		t.Fatalf("get --timeline: exit = %d, stderr %q", code, errText)
	}
	if !strings.Contains(out, `"domain": "execution-state"`) || !strings.Contains(out, `"to": "in-progress"`) {
		t.Errorf("timeline = %q, want the execution-state transitions", out)
	}
	_ = w
}

// TestTransitionInteractiveConfirm: the active-container confirmation
// renders the reusable arrow-selected menu (bubbletea) on a real
// terminal (both stdin and stdout are the pty slave, so the menu and
// the container margin apply); Enter confirms the default option and
// the transition publishes. A second run selects Cancel and reports
// the padded cancelled line. The pty master is drained by a single
// background reader (PTYs do not support read deadlines).
func TestTransitionInteractiveConfirm(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("PTY test is Linux-only (/dev/ptmx)")
	}

	master, slave := openPTY(t)
	defer master.Close()
	defer slave.Close()

	// One background reader drains the master continuously; drain
	// returns the bytes collected so far.
	var mu sync.Mutex
	var collected []byte
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		buf := make([]byte, 4096)
		for {
			n, err := master.Read(buf)
			mu.Lock()
			if n > 0 {
				collected = append(collected, buf[:n]...)
			}
			mu.Unlock()
			if err != nil {
				return
			}
		}
	}()
	drain := func() string {
		// The command has already returned: the output is in the pty
		// buffer; give the reader a moment to collect it.
		time.Sleep(300 * time.Millisecond)
		mu.Lock()
		defer mu.Unlock()
		return string(collected)
	}

	_, _ = transitionEnv(t)

	// Enter on the default option ("Continue transition"): delivered
	// once the menu is reading (the pre-flight work takes a moment).
	go func() {
		time.Sleep(1500 * time.Millisecond)
		master.WriteString("\r")
	}()
	var errb bytes.Buffer
	code := Execute([]string{"transition", "sto:orphan", "--forward"}, slave, slave, &errb)
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstderr: %s", code, errb.String())
	}
	out := drain()
	for _, want := range []string{"Continue transition", "Cancel", "Object Hash"} {
		if !strings.Contains(out, want) {
			t.Errorf("output must contain %q:\n%s", want, out)
		}
	}
	if !strings.Contains(errb.String(), "eka: warning:") {
		t.Errorf("the warning banner must render on stderr, got %q", errb.String())
	}

	// Cancel path: down arrow to Cancel + Enter → cancelled, exit 0.
	// The cancelled line is a human notice: it must render on stderr
	// (the pty carries the menu + the machine document only), with the
	// container margin.
	go func() {
		time.Sleep(1500 * time.Millisecond)
		master.WriteString("\x1b[B\r")
	}()
	errb.Reset()
	code = Execute([]string{"transition", "sto:orphan", "--forward"}, slave, slave, &errb)
	if code != 0 {
		t.Fatalf("cancel: exit = %d, want 0\nstderr: %s", code, errb.String())
	}
	out = drain()
	if strings.Contains(out, "transition cancelled") {
		t.Errorf("the cancelled line must not pollute stdout (the pty), got:\n%s", out)
	}
	if !strings.Contains(errb.String(), "  eka: transition cancelled; no changes made") {
		t.Errorf("the cancelled line must render on stderr with the container margin, got %q", errb.String())
	}
}

// openPTY opens a pseudo-terminal pair and returns the master (the
// test's control side) and the slave (a real terminal for the command
// under test). Linux-only; callers skip on other platforms.
func openPTY(t *testing.T) (*os.File, *os.File) {
	t.Helper()
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		t.Skipf("cannot open /dev/ptmx: %v", err)
	}
	var n uint32
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, master.Fd(), syscall.TIOCGPTN, uintptr(unsafe.Pointer(&n))); errno != 0 {
		master.Close()
		t.Fatalf("TIOCGPTN failed: %v", errno)
	}
	lock := 0
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, master.Fd(), syscall.TIOCSPTLCK, uintptr(unsafe.Pointer(&lock))); errno != 0 {
		master.Close()
		t.Fatalf("TIOCSPTLCK failed: %v", errno)
	}
	slave, err := os.OpenFile("/dev/pts/"+strconv.Itoa(int(n)), os.O_RDWR, 0)
	if err != nil {
		master.Close()
		t.Fatalf("cannot open the pty slave: %v", err)
	}
	return master, slave
}

// storeContentState reads the current content-state of a knowledge
// artifact line from the workspace store and its change-log length.
func storeContentState(t *testing.T, w *workspace.Workspace, typ, id string) (string, int) {
	t.Helper()
	units, err := w.Store().UnitsByLine("test-ns", typ, id)
	if err != nil || len(units) == 0 {
		t.Fatalf("UnitsByLine(%s:%s) = %d units (err %v)", typ, id, len(units), err)
	}
	u := units[0]
	for _, cand := range units {
		if cand.Identity.InstanceVersion > u.Identity.InstanceVersion {
			u = cand
		}
	}
	return u.StateVector.ContentState, len(u.ChangeLog)
}

// TestTransitionContentStateCLI: the content-state lifecycle at CLI
// level — the acceptance example (eka new adr:x --dimension
// architecture, publish, then eka transition adr:x accepted, with the
// machine document and the store line), the standard-variant steps for
// a living-type artifact, the steps-not-skips refusal and the R9
// supersede gate. The dispatch lives in the linked eka-core engine: the
// test requires the content-state transition support (eka-core v1.1.0+,
// the release-chain decision — eka-core ships the feature first, then
// this CLI bumps the dependency); against the v1.0.0 engine the
// transition is refused as "not transitionable" and the test skips.
func TestTransitionContentStateCLI(t *testing.T) {
	w, _ := transitionEnv(t)

	// The acceptance example: new (the adr- template initializes
	// content-state to proposed) -> publish -> transition, without
	// hand-editing the draft JSON.
	if code, _, errText := runIn([]string{"new", "adr:x", "--dimension", "architecture"}); code != 0 {
		t.Fatalf("new adr:x: exit = %d, stderr %q", code, errText)
	}
	if code, _, errText := runIn([]string{"publish", "adr:x"}); code != 0 {
		t.Fatalf("publish adr:x: exit = %d, stderr %q", code, errText)
	}
	code, out, errText := runIn([]string{"transition", "adr:x", "accepted", "--json"})
	if code == 2 && strings.Contains(errText, "not transitionable") {
		t.Skip("the linked eka-core does not support content-state transitions yet (v1.0.0); activates with the eka-core release + dependency bump")
	}
	if code != 0 {
		t.Fatalf("transition adr:x accepted: exit = %d, stderr %q", code, errText)
	}
	if !strings.Contains(out, `"ok":true`) || !strings.Contains(out, `"from":"proposed"`) || !strings.Contains(out, `"to":"accepted"`) {
		t.Errorf("--json = %q, want the content-state proposed -> accepted document", out)
	}
	if state, logLen := storeContentState(t, w, "adr", "x"); state != "accepted" || logLen != 3 {
		t.Errorf("store content-state = %q with %d entries, want accepted with 3", state, logLen)
	}

	// The R9 supersede gate: accepted -> superseded refuses without a
	// published replacement referencing the ADR via supersedes.
	code, out, errText = runIn([]string{"transition", "adr:x", "superseded", "--json"})
	if code != 1 || !strings.Contains(out, `"ok":false`) || !strings.Contains(out, "transition gate R9") {
		t.Errorf("superseded without replacement: exit = %d, stdout = %q, want 1 + the R9 refusal document", code, out)
	}
	if state, _ := storeContentState(t, w, "adr", "x"); state != "accepted" {
		t.Errorf("refused supersession must not publish: content-state = %q, want accepted", state)
	}

	// The standard variant for a living-type artifact: draft -> review
	// (--forward) -> approved (explicit <to>); a direct draft ->
	// approved skip refuses (steps, not skips).
	if code, _, errText := runIn([]string{"new", "spec:api", "--dimension", "specifications"}); code != 0 {
		t.Fatalf("new spec:api: exit = %d, stderr %q", code, errText)
	}
	if code, _, errText := runIn([]string{"publish", "spec:api"}); code != 0 {
		t.Fatalf("publish spec:api: exit = %d, stderr %q", code, errText)
	}
	if code, _, errText := runIn([]string{"transition", "spec:api", "approved", "--json"}); code != 1 || !strings.Contains(errText, "is not in the content-state table") {
		t.Errorf("draft -> approved skip: exit = %d, stderr = %q, want 1 + the table refusal", code, errText)
	}
	if code, _, errText := runIn([]string{"transition", "spec:api", "--forward"}); code != 0 {
		t.Fatalf("spec:api --forward: exit = %d, stderr %q", code, errText)
	}
	if state, _ := storeContentState(t, w, "spec", "api"); state != "review" {
		t.Errorf("content-state = %q, want review", state)
	}
	if code, out, errText := runIn([]string{"transition", "spec:api", "approved", "--json"}); code != 0 {
		t.Fatalf("review -> approved: exit = %d, stderr %q", code, errText)
	} else if !strings.Contains(out, `"to":"approved"`) {
		t.Errorf("--json = %q, want the review -> approved document", out)
	}
}

// storePlanningState reads the current planning-state of a plan line
// from the workspace store and its change-log length.
func storePlanningState(t *testing.T, w *workspace.Workspace, id string) (string, int) {
	t.Helper()
	units, err := w.Store().UnitsByLine("test-ns", "plan", id)
	if err != nil || len(units) == 0 {
		t.Fatalf("UnitsByLine(plan:%s) = %d units (err %v)", id, len(units), err)
	}
	u := units[0]
	for _, cand := range units {
		if cand.Identity.InstanceVersion > u.Identity.InstanceVersion {
			u = cand
		}
	}
	return u.StateVector.PlanningState, len(u.ChangeLog)
}

// storeContainerState reads the current container-state of a ctr line
// from the workspace store and its change-log length.
func storeContainerState(t *testing.T, w *workspace.Workspace, id string) (string, int) {
	t.Helper()
	units, err := w.Store().UnitsByLine("test-ns", "ctr", id)
	if err != nil || len(units) == 0 {
		t.Fatalf("UnitsByLine(ctr:%s) = %d units (err %v)", id, len(units), err)
	}
	u := units[0]
	for _, cand := range units {
		if cand.Identity.InstanceVersion > u.Identity.InstanceVersion {
			u = cand
		}
	}
	return u.StateVector.ContainerState, len(u.ChangeLog)
}

// TestTransitionPlanCLI: plan transitions at CLI level — the draft
// plan approves (exit 0; the JSON document carries the planning-state
// values); the approved plan refuses the lock with the lock hint
// (exit 1, explicit <to> and --forward alike); --backward refuses
// (forward-only); the refusal travels in --json.
func TestTransitionPlanCLI(t *testing.T) {
	w, _ := transitionEnv(t)

	// The fixture draft plan approves through --forward.
	code, out, errText := runIn([]string{"transition", "plan:roadmap-v2", "--forward", "--json"})
	if code != 0 {
		t.Fatalf("--forward: exit = %d, stderr %q", code, errText)
	}
	if !strings.Contains(out, `"ok":true`) || !strings.Contains(out, `"from":"draft"`) || !strings.Contains(out, `"to":"approved"`) {
		t.Errorf("--json = %q, want the planning-state draft -> approved document", out)
	}
	if state, _ := storePlanningState(t, w, "roadmap-v2"); state != "approved" {
		t.Errorf("store planning-state = %q, want approved", state)
	}

	// The approved plan refuses the lock (--forward and explicit <to>)
	// with the lock hint; nothing publishes.
	for _, args := range [][]string{
		{"transition", "plan:roadmap-v2", "--forward"},
		{"transition", "plan:roadmap-v2", "immutable"},
	} {
		code, _, errText := runIn(args)
		if code != 1 || !strings.Contains(errText, "planning-state immutable is the container lock") {
			t.Errorf("args %v: exit = %d, stderr = %q, want 1 + lock hint", args, code, errText)
		}
	}
	if state, _ := storePlanningState(t, w, "roadmap-v2"); state != "approved" {
		t.Errorf("refused locks must not publish: planning-state = %q, want approved", state)
	}
	// --backward refuses (planning-state is forward-only).
	if code, _, errText := runIn([]string{"transition", "plan:roadmap-v2", "--backward"}); code != 1 || !strings.Contains(errText, "planning-state is forward-only") {
		t.Errorf("--backward: exit = %d, stderr = %q, want 1 + forward-only", code, errText)
	}
	// The lock refusal document travels in --json.
	code, out, errText = runIn([]string{"transition", "plan:roadmap-v1", "immutable", "--json"})
	if code != 1 || !strings.Contains(out, `"ok":false`) || !strings.Contains(out, "container lock") {
		t.Errorf("--json refusal: exit = %d, stdout = %q, want the refusal document", code, out)
	}
}

// TestTransitionContainerCLI: container transitions at CLI level — the
// all-done gate refuses with the deterministic pending listing (exit
// 1; --force does not bypass it); after the pending items move to done
// / canceled the completion publishes (exit 0 + themed report);
// --forward from completed refuses (terminal).
func TestTransitionContainerCLI(t *testing.T) {
	w, _ := transitionEnv(t)

	// The fixture container registers sto:one (todo), sto:two
	// (in-progress) and sto:done (done): the all-done gate refuses
	// with the two pending items.
	code, out, errText := runIn([]string{"transition", "ctr:wave-1", "completed", "--json"})
	if code != 1 {
		t.Fatalf("completed with pending items: exit = %d, want 1\nstderr: %s", code, errText)
	}
	if !strings.Contains(out, `"ok":false`) || !strings.Contains(out, "2 work item(s) not done or canceled") {
		t.Errorf("--json = %q, want the pending-items refusal document", out)
	}
	for _, want := range []string{"sto:one (todo)", "sto:two (in-progress)"} {
		if !strings.Contains(out, want) {
			t.Errorf("--json = %q, want the pending item %q", out, want)
		}
	}
	if !strings.Contains(out, "transition the pending work items to done (or canceled) first") {
		t.Errorf("--json = %q, want the transition-first hint", out)
	}
	// --force does not bypass the all-done gate.
	if code, _, _ := runIn([]string{"transition", "ctr:wave-1", "completed", "--force"}); code != 1 {
		t.Errorf("--force with pending items: exit = %d, want 1", code)
	}
	if state, _ := storeContainerState(t, w, "wave-1"); state != "active" {
		t.Errorf("container-state = %q, want active (refused runs publish nothing)", state)
	}

	// Move the pending items: sto:one along the D1 chain to done (its
	// resolved implementation note gate-satisfies in-review and done),
	// sto:two to canceled — then the completion publishes.
	for _, step := range []string{"in-progress", "in-review", "done"} {
		if code, _, errText := runIn([]string{"transition", "sto:one", step}); code != 0 {
			t.Fatalf("sto:one %s: exit = %d, stderr %q", step, code, errText)
		}
	}
	if code, _, errText := runIn([]string{"transition", "sto:two", "canceled"}); code != 0 {
		t.Fatalf("sto:two canceled: exit = %d, stderr %q", code, errText)
	}
	code, out, errText = runIn([]string{"transition", "ctr:wave-1", "completed"})
	if code != 0 {
		t.Fatalf("completed with all done/canceled: exit = %d, stderr %q", code, errText)
	}
	if !strings.Contains(out, "active") || !strings.Contains(out, "completed") {
		t.Errorf("stdout = %q, want the themed container-state report", out)
	}
	if state, _ := storeContainerState(t, w, "wave-1"); state != "completed" {
		t.Errorf("container-state = %q, want completed", state)
	}
	// --forward from completed: terminal.
	if code, _, errText := runIn([]string{"transition", "ctr:wave-1", "--forward"}); code != 1 || !strings.Contains(errText, "completed is terminal") {
		t.Errorf("--forward from completed: exit = %d, stderr = %q, want 1 + terminal", code, errText)
	}
}

// TestTransitionContainerActivationCLI: the activation at CLI level —
// refused while another container is active (protocol §3, with the
// complete-it hint), the human run renders the Plan Locked summary
// (the depends-on plan moves to immutable atomically with the
// activation), refused when the plan is draft (approve-it-first), and
// the --json document carries the lockedPlan fields.
func TestTransitionContainerActivationCLI(t *testing.T) {
	w, _ := transitionEnv(t)

	// Gate (protocol §3): wave-1 is active — activating wave-2 is
	// refused, naming the active offender and the completion hint; the
	// refusal document travels in --json.
	code, out, errText := runIn([]string{"transition", "ctr:wave-2", "active", "--json"})
	if code != 1 {
		t.Fatalf("activation while another container is active: exit = %d, want 1\nstderr: %s", code, errText)
	}
	if !strings.Contains(out, `"ok":false`) || !strings.Contains(out, "another container test-ns/ctr:wave-1 is active; activate test-ns/ctr:wave-2 only after it completes") {
		t.Errorf("--json = %q, want the other-active refusal document", out)
	}
	if !strings.Contains(out, "eka transition ctr:wave-1 completed") {
		t.Errorf("--json = %q, want the complete-the-offender hint", out)
	}
	if state, _ := storeContainerState(t, w, "wave-2"); state != "planned" {
		t.Errorf("container-state = %q, want planned (refused runs publish nothing)", state)
	}

	// Free the stage: move the pending items (sto:one -> done along
	// the D1 chain — its resolved implementation note gate-satisfies —
	// sto:two -> canceled), then complete wave-1.
	for _, step := range []string{"in-progress", "in-review", "done"} {
		if code, _, errText := runIn([]string{"transition", "sto:one", step}); code != 0 {
			t.Fatalf("sto:one %s: exit = %d, stderr %q", step, code, errText)
		}
	}
	if code, _, errText := runIn([]string{"transition", "sto:two", "canceled"}); code != 0 {
		t.Fatalf("sto:two canceled: exit = %d, stderr %q", code, errText)
	}
	if code, _, errText := runIn([]string{"transition", "ctr:wave-1", "completed"}); code != 0 {
		t.Fatalf("complete wave-1: exit = %d, stderr %q", code, errText)
	}

	// The plan-approval gate: wave-3 derives from the draft roadmap-v2
	// — activating it is refused with the approve-it-first hint (the
	// exactly-one-active gate already passed: wave-1 completed).
	code, out, errText = runIn([]string{"transition", "ctr:wave-3", "active", "--json"})
	if code != 1 {
		t.Fatalf("activation with a draft plan: exit = %d, want 1\nstderr: %s", code, errText)
	}
	if !strings.Contains(out, `"ok":false`) || !strings.Contains(out, "the plan test-ns/plan:roadmap-v2 is not approved (planning-state: draft)") {
		t.Errorf("--json = %q, want the not-approved refusal document", out)
	}
	if !strings.Contains(out, "approve it first: eka transition plan:roadmap-v2 approved") {
		t.Errorf("--json = %q, want the approve-it-first hint", out)
	}
	if state, _ := storeContainerState(t, w, "wave-3"); state != "planned" {
		t.Errorf("container-state = %q, want planned (refused runs publish nothing)", state)
	}

	// The human activation: planned -> active with the Plan Locked
	// summary — roadmap-v1 moved to immutable atomically with the
	// activation.
	code, out, errText = runIn([]string{"transition", "ctr:wave-2", "active"})
	if code != 0 {
		t.Fatalf("activation: exit = %d, stderr %q", code, errText)
	}
	if !strings.Contains(out, "planned") || !strings.Contains(out, "active") {
		t.Errorf("stdout = %q, want the themed planned -> active report", out)
	}
	if !strings.Contains(out, "Plan Locked: test-ns/plan:roadmap-v1 → immutable") {
		t.Errorf("stdout = %q, want the Plan Locked summary line", out)
	}
	if state, _ := storeContainerState(t, w, "wave-2"); state != "active" {
		t.Errorf("container-state = %q, want active", state)
	}
	if state, _ := storePlanningState(t, w, "roadmap-v1"); state != "immutable" {
		t.Errorf("planning-state = %q, want immutable (locked by the activation)", state)
	}

	// wave-2 completes (no items: the all-done gate passes trivially),
	// freeing the stage for wave-3.
	if code, _, errText := runIn([]string{"transition", "ctr:wave-2", "completed"}); code != 0 {
		t.Fatalf("complete wave-2: exit = %d, stderr %q", code, errText)
	}

	// Approve roadmap-v2 and activate wave-3 with --json: the machine
	// document carries the lockedPlan fields.
	if code, _, errText := runIn([]string{"transition", "plan:roadmap-v2", "approved"}); code != 0 {
		t.Fatalf("approve roadmap-v2: exit = %d, stderr %q", code, errText)
	}
	code, out, errText = runIn([]string{"transition", "ctr:wave-3", "active", "--json"})
	if code != 0 {
		t.Fatalf("activation --json: exit = %d, stderr %q", code, errText)
	}
	if !strings.Contains(out, `"ok":true`) || !strings.Contains(out, `"from":"planned"`) || !strings.Contains(out, `"to":"active"`) {
		t.Errorf("--json = %q, want the planned -> active document", out)
	}
	if !strings.Contains(out, `"lockedPlan":"test-ns/plan:roadmap-v2"`) {
		t.Errorf("--json = %q, want the lockedPlan field", out)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("--json output is not valid JSON: %v", err)
	}
	if hash, ok := doc["lockedPlanHash"].(string); !ok || hash == "" {
		t.Errorf("--json = %v, want a non-empty lockedPlanHash", doc)
	}
	if state, _ := storeContainerState(t, w, "wave-3"); state != "active" {
		t.Errorf("container-state = %q, want active", state)
	}
	if state, _ := storePlanningState(t, w, "roadmap-v2"); state != "immutable" {
		t.Errorf("planning-state = %q, want immutable (locked by the activation)", state)
	}
}
