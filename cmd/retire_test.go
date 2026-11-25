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

// This file tests `eka retire` at CLI level (soft-delete, never hard
// delete): the new-instance publish (highest+1, existence-state flip,
// frozen content, appended change-log), every refusal gate, the
// idempotent already-retired path, the dry-run plan, the eka-retire-v1
// machine contract, the non-TTY --force requirement, the cmt cascade,
// and the subsystem impacts (get latest returns the retired instance,
// old versions stay, integrity stays green).

// retireEnv builds a repository whose docs tree seeds the workspace
// store: plan:roadmap-v1 (approved), ctr:wave-1 (active, depends-on
// the plan), sto:lone (todo, discussed by cmt:lone-note),
// sto:parent (todo, depended-on by sto:child), sto:child (todo,
// depends-on sto:parent), and cmt:lone-note (open, discusses
// sto:lone). The repository is registered, synced, and the working
// directory moves into it.
func retireEnv(t *testing.T) (*workspace.Workspace, string) {
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
	writeRetireFixture(t, repo)
	t.Chdir(repo)
	if code, _, errText := runIn([]string{"sync"}); code != 0 {
		t.Fatalf("seed sync: exit = %d, stderr %q", code, errText)
	}
	return w, repo
}

// writeRetireFixture writes the docs tree of the retire test
// repository (conformant, so the seed sync passes).
func writeRetireFixture(t *testing.T, repo string) {
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
	write("docs/planning/plan-roadmap-v1-v1.json", `{
  "namespace": "test-ns",
  "type": "plan",
  "id": "roadmap-v1",
  "instanceVersion": 1,
  "revision": 1,
  "author": "Engineering Architecture",
  "created": "2026-08-05",
  "updated": "2026-08-05",
  "dimension": "planning",
  "state": {"contentState": "draft", "planningState": "approved", "existenceState": "active"},
  "changeLog": [
    {"date": "2026-08-05", "domain": "contentState", "from": "-", "to": "draft", "by": "Engineering Architecture"},
    {"date": "2026-08-05", "domain": "planningState", "from": "-", "to": "approved", "by": "Engineering Architecture"},
    {"date": "2026-08-05", "domain": "existenceState", "from": "-", "to": "active", "by": "Engineering Architecture"}
  ],
  "content": {"objective": "roadmap v1", "scope": "all", "outOfScope": "none"}
}
`)
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
	work := func(id, state string, rels string) string {
		relBlock := ""
		if rels != "" {
			relBlock = "\n  \"relationships\": {" + rels + "},"
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
  "state": {"executionState": "` + state + `", "existenceState": "active"},` + relBlock + `
  "changeLog": [
    {"date": "2026-08-05", "domain": "existenceState", "from": "-", "to": "active", "by": "Engineering Architecture"},
    {"date": "2026-08-05", "domain": "executionState", "from": "-", "to": "planned", "by": "Engineering Architecture"},
    {"date": "2026-08-05", "domain": "executionState", "from": "planned", "to": "` + state + `", "by": "Engineering Architecture"}
  ],
  "content": {"description": "The ` + id + ` story.", "acceptanceCriteria": "- Works."}
}
`
	}
	write("docs/operating/work-items/stories/sto-lone.json", work("lone", "todo", ""))
	write("docs/operating/work-items/stories/sto-parent.json", work("parent", "todo", ""))
	write("docs/operating/work-items/stories/sto-child.json", work("child", "todo", `"dependsOn": ["test-ns/sto:parent"]`))
	write("docs/operating/notes/cmt-lone-note.json", `{
  "namespace": "test-ns",
  "type": "cmt",
  "id": "lone-note",
  "instanceVersion": 1,
  "revision": 1,
  "author": "Engineering Architecture",
  "created": "2026-08-05",
  "updated": "2026-08-05",
  "domain": "Execution",
  "state": {"contentState": "draft", "existenceState": "active", "noteState": "resolved"},
  "relationships": {"discusses": ["test-ns/sto:lone"]},
  "changeLog": [
    {"date": "2026-08-05", "domain": "contentState", "from": "-", "to": "draft", "by": "Engineering Architecture"},
    {"date": "2026-08-05", "domain": "existenceState", "from": "-", "to": "active", "by": "Engineering Architecture"},
    {"date": "2026-08-05", "domain": "noteState", "from": "-", "to": "open", "by": "Engineering Architecture"},
    {"date": "2026-08-05", "domain": "noteState", "from": "open", "to": "resolved", "by": "Engineering Architecture"}
  ],
  "content": {"role": "implementation", "summary": "lone implemented", "changes": ["x"], "tests": ["y"]}
}
`)
}

// retireLineUnits reads every instance of a line from the workspace
// store, ordered by instance version ascending.
func retireLineUnits(t *testing.T, w *workspace.Workspace, ns, typ, id string) []unitSnapshot {
	t.Helper()
	units, err := w.Store().UnitsByLine(ns, typ, id)
	if err != nil {
		t.Fatalf("UnitsByLine(%s, %s, %s): %v", ns, typ, id, err)
	}
	out := make([]unitSnapshot, 0, len(units))
	for _, u := range units {
		var lastDomain, lastFrom, lastTo, lastBy string
		if len(u.ChangeLog) > 0 {
			last := u.ChangeLog[len(u.ChangeLog)-1]
			lastDomain, lastFrom, lastTo, lastBy = last.Domain, last.From, last.To, last.By.Name
		}
		out = append(out, unitSnapshot{
			version:    u.Identity.InstanceVersion,
			existence:  u.StateVector.ExistenceState,
			execution:  u.StateVector.ExecutionState,
			content:    string(u.ContentPayload),
			changeLen:  len(u.ChangeLog),
			lastDomain: lastDomain, lastFrom: lastFrom, lastTo: lastTo, lastBy: lastBy,
		})
	}
	// Deterministic order: ascending instance version.
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j].version < out[i].version {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

// unitSnapshot is the observable projection of one stored instance.
type unitSnapshot struct {
	version    int
	existence  string
	execution  string
	content    string
	changeLen  int
	lastDomain string
	lastFrom   string
	lastTo     string
	lastBy     string
}

// retireJSONDoc decodes the eka-retire-v1 machine report of a --json
// run (stdout must carry ONLY the document: one line, one trailing
// newline).
func retireJSONDoc(t *testing.T, out string) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("retire --json stdout is not valid JSON: %v\n%s", err, out)
	}
	if doc["schema"] != retireSchema {
		t.Fatalf("schema = %v, want %q", doc["schema"], retireSchema)
	}
	return doc
}

func TestRetireHappyPath(t *testing.T) {
	w, _ := retireEnv(t)
	before := retireLineUnits(t, w, "test-ns", "sto", "lone")
	if len(before) != 1 || before[0].existence != "active" {
		t.Fatalf("precondition: sto:lone has %d instances, want 1 active", len(before))
	}

	code, out, errText := runIn([]string{"retire", "sto:lone", "--reason", "superseded by sto:next-gen", "--force", "--by", "test-bot"})
	if code != 0 {
		t.Fatalf("retire: exit = %d, want 0\nstdout: %s\nstderr: %s", code, out, errText)
	}

	after := retireLineUnits(t, w, "test-ns", "sto", "lone")
	if len(after) != 2 {
		t.Fatalf("retire must publish a new instance: got %d instances, want 2", len(after))
	}
	old, new := after[0], after[1]
	// The old CKO is never mutated.
	if old.existence != "active" || old.version != 1 {
		t.Errorf("old instance mutated: %+v", old)
	}
	// The new instance flips existence-state only.
	if new.version != 2 || new.existence != "retired" {
		t.Errorf("new instance = v%d/%s, want v2/retired", new.version, new.existence)
	}
	if new.execution != old.execution {
		t.Errorf("execution-state changed on retire: %q -> %q (content frozen)", old.execution, new.execution)
	}
	if new.content != old.content {
		t.Error("content payload changed on retire (content frozen)")
	}
	if new.changeLen != old.changeLen+1 {
		t.Errorf("change-log grew by %d, want exactly 1 appended entry", new.changeLen-old.changeLen)
	}
	if new.lastDomain != "existence-state" || new.lastFrom != "active" || new.lastTo != "retired" || new.lastBy != "test-bot" {
		t.Errorf("appended change-log entry = %s %s->%s by %s, want existence-state active->retired by test-bot",
			new.lastDomain, new.lastFrom, new.lastTo, new.lastBy)
	}
}

func TestRetireMissingReason(t *testing.T) {
	retireEnv(t)
	// Missing --reason is a usage error (exit 2).
	code, _, errText := runIn([]string{"retire", "sto:lone", "--force", "--by", "test-bot"})
	if code != 2 {
		t.Errorf("missing --reason: exit = %d, want 2", code)
	}
	if !strings.Contains(errText, "--reason") {
		t.Errorf("missing --reason: stderr must name --reason, got %q", errText)
	}
	// A short --reason is a usage error too (min 10 chars).
	code, _, errText = runIn([]string{"retire", "sto:lone", "--reason", "short", "--force", "--by", "test-bot"})
	if code != 2 {
		t.Errorf("short --reason: exit = %d, want 2", code)
	}
	// A versioned target is a usage error: retire addresses the line.
	code, _, errText = runIn([]string{"retire", "sto:lone:1", "--reason", "superseded by sto:next-gen", "--force", "--by", "test-bot"})
	if code != 2 {
		t.Errorf("versioned target: exit = %d, want 2", code)
	}
	if !strings.Contains(errText, "artifact line") {
		t.Errorf("versioned target: stderr must say retire addresses the line, got %q", errText)
	}
	// An unknown line is a refusal (exit 1), not usage.
	code, _, errText = runIn([]string{"retire", "sto:ghost", "--reason", "superseded by sto:next-gen", "--force", "--by", "test-bot"})
	if code != 1 {
		t.Errorf("unknown line: exit = %d, want 1", code)
	}
	if !strings.Contains(errText, "line not found") {
		t.Errorf("unknown line: stderr must say line not found, got %q", errText)
	}
}

func TestRetireDraftTarget(t *testing.T) {
	retireEnv(t)
	// A line with a pending draft but no published instance is a
	// draft-target refusal with the `eka discard` hint.
	if code, _, errText := runIn([]string{"new", "sto:draftonly"}); code != 0 {
		t.Fatalf("seed draft: exit = %d, stderr %q", code, errText)
	}
	code, _, errText := runIn([]string{"retire", "sto:draftonly", "--reason", "no longer needed here", "--force", "--by", "test-bot"})
	if code != 1 {
		t.Fatalf("draft-target: exit = %d, want 1\nstderr: %s", code, errText)
	}
	if !strings.Contains(errText, "eka discard") {
		t.Errorf("draft-target: stderr must hint `eka discard`, got %q", errText)
	}
}

func TestRetireAlreadyRetired(t *testing.T) {
	w, _ := retireEnv(t)
	args := []string{"retire", "sto:lone", "--reason", "superseded by sto:next-gen", "--force", "--by", "test-bot"}
	if code, _, errText := runIn(args); code != 0 {
		t.Fatalf("first retire: exit = %d, stderr %q", code, errText)
	}
	// The second run is idempotent: exit 0, no new revision.
	code, out, errText := runIn(append(args, "--json"))
	if code != 0 {
		t.Fatalf("second retire: exit = %d, want 0\nstdout: %s\nstderr: %s", code, out, errText)
	}
	doc := retireJSONDoc(t, out)
	if doc["status"] != "already-retired" {
		t.Errorf("status = %v, want already-retired", doc["status"])
	}
	target := doc["target"].(map[string]any)
	if target["prevInstance"] != target["newInstance"] {
		t.Errorf("already-retired must not advance the instance: %+v", target)
	}
	if _, ok := doc["objectHash"]; ok {
		t.Errorf("already-retired must carry no new objectHash: %v", doc["objectHash"])
	}
	units := retireLineUnits(t, w, "test-ns", "sto", "lone")
	if len(units) != 2 {
		t.Errorf("already-retired wrote a revision: %d instances, want 2", len(units))
	}
}

func TestRetireDownstreamBlocker(t *testing.T) {
	w, _ := retireEnv(t)
	// sto:parent is depended-on by the active sto:child — the retire
	// is refused with the sorted blocker list, nothing is written.
	code, out, errText := runIn([]string{"retire", "sto:parent", "--reason", "parent is obsolete now", "--force", "--by", "test-bot", "--json"})
	if code != 1 {
		t.Fatalf("downstream blocker: exit = %d, want 1\nstdout: %s\nstderr: %s", code, out, errText)
	}
	if !strings.Contains(errText, "test-ns/sto:child") {
		t.Errorf("downstream blocker: stderr must list test-ns/sto:child, got %q", errText)
	}
	doc := retireJSONDoc(t, out)
	if doc["status"] != "refused" {
		t.Errorf("status = %v, want refused", doc["status"])
	}
	downstream := doc["downstream"].(map[string]any)
	blockers := downstream["blockers"].([]any)
	if len(blockers) != 1 || blockers[0] != "test-ns/sto:child" {
		t.Errorf("blockers = %v, want [test-ns/sto:child]", blockers)
	}
	units := retireLineUnits(t, w, "test-ns", "sto", "parent")
	if len(units) != 1 {
		t.Errorf("refused retire wrote a revision: %d instances, want 1", len(units))
	}
}

func TestRetireProtectedContainer(t *testing.T) {
	retireEnv(t)
	// An active container cannot retire.
	code, _, errText := runIn([]string{"retire", "ctr:wave-1", "--reason", "wave one is complete", "--force", "--by", "test-bot"})
	if code != 1 {
		t.Fatalf("protected container: exit = %d, want 1\nstderr: %s", code, errText)
	}
	if !strings.Contains(errText, "active container") {
		t.Errorf("protected container: stderr must name the active container, got %q", errText)
	}
}

func TestRetireProtectedPlan(t *testing.T) {
	retireEnv(t)
	// plan:roadmap-v1 is approved and still depended-on by the active
	// ctr:wave-1 — the retire is refused.
	code, _, errText := runIn([]string{"retire", "plan:roadmap-v1", "--reason", "roadmap one is done", "--force", "--by", "test-bot"})
	if code != 1 {
		t.Fatalf("protected plan: exit = %d, want 1\nstderr: %s", code, errText)
	}
	if !strings.Contains(errText, "depended-on") {
		t.Errorf("protected plan: stderr must name the holding container, got %q", errText)
	}
}

func TestRetireDryRun(t *testing.T) {
	w, _ := retireEnv(t)
	// The dry-run prints the plan (target, prev->new instance,
	// retained downstream, cascade preview) and writes nothing.
	code, out, errText := runIn([]string{"retire", "sto:lone", "--reason", "superseded by sto:next-gen", "--force", "--by", "test-bot", "--dry-run"})
	if code != 0 {
		t.Fatalf("dry-run: exit = %d, want 0\nstdout: %s\nstderr: %s", code, out, errText)
	}
	for _, want := range []string{"test-ns/sto:lone", "1 -> 2", "active -> retired", "Dry-run: no changes were written."} {
		if !strings.Contains(out, want) {
			t.Errorf("dry-run output must contain %q:\n%s", want, out)
		}
	}
	if units := retireLineUnits(t, w, "test-ns", "sto", "lone"); len(units) != 1 {
		t.Errorf("dry-run wrote a revision: %d instances, want 1", len(units))
	}
	// The JSON dry-run carries dryRun:true and no new objectHash.
	code, out, _ = runIn([]string{"retire", "sto:lone", "--reason", "superseded by sto:next-gen", "--force", "--by", "test-bot", "--dry-run", "--json"})
	if code != 0 {
		t.Fatalf("dry-run --json: exit = %d, want 0", code)
	}
	doc := retireJSONDoc(t, out)
	if doc["status"] != "dry-run" || doc["dryRun"] != true {
		t.Errorf("dry-run JSON must carry status dry-run + dryRun:true: %v", doc)
	}
	if _, ok := doc["objectHash"]; ok {
		t.Errorf("dry-run must carry no new objectHash: %v", doc["objectHash"])
	}
	target := doc["target"].(map[string]any)
	if target["canonicalForm"] != "test-ns/sto:lone" || target["prevInstance"] != float64(1) || target["newInstance"] != float64(2) {
		t.Errorf("dry-run target = %+v, want test-ns/sto:lone 1->2", target)
	}
}

func TestRetireJSONContract(t *testing.T) {
	retireEnv(t)
	code, out, errText := runIn([]string{"retire", "sto:lone", "--reason", "superseded by sto:next-gen", "--force", "--by", "test-bot", "--by-kind", "agent", "--json"})
	if code != 0 {
		t.Fatalf("retire --json: exit = %d, want 0\nstdout: %s\nstderr: %s", code, out, errText)
	}
	doc := retireJSONDoc(t, out)
	// The contract keys of eka-retire-v1.
	target, ok := doc["target"].(map[string]any)
	if !ok || target["canonicalForm"] != "test-ns/sto:lone" || target["prevInstance"] != float64(1) || target["newInstance"] != float64(2) {
		t.Errorf("target = %v, want {test-ns/sto:lone 1 2}", doc["target"])
	}
	state, ok := doc["existenceState"].(map[string]any)
	if !ok || state["from"] != "active" || state["to"] != "retired" {
		t.Errorf("existenceState = %v, want {active retired}", doc["existenceState"])
	}
	if doc["reason"] != "superseded by sto:next-gen" || doc["by"] != "test-bot" || doc["byKind"] != "agent" {
		t.Errorf("reason/by/byKind = %v/%v/%v", doc["reason"], doc["by"], doc["byKind"])
	}
	downstream, ok := doc["downstream"].(map[string]any)
	if !ok {
		t.Fatalf("downstream missing: %v", doc)
	}
	retained, ok := downstream["retained"].([]any)
	if !ok || len(retained) != 1 || retained[0] != "test-ns/cmt:lone-note" {
		t.Errorf("retained = %v, want [test-ns/cmt:lone-note] (discusses never blocks, stays behind)", downstream["retained"])
	}
	if blockers, ok := downstream["blockers"].([]any); !ok || len(blockers) != 0 {
		t.Errorf("blockers = %v, want []", downstream["blockers"])
	}
	cascade, ok := doc["cascade"].(map[string]any)
	if !ok || cascade["mode"] != "" {
		t.Errorf("cascade = %v, want {mode:\"\" cascaded:[]}", doc["cascade"])
	}
	if doc["status"] != "retired" {
		t.Errorf("status = %v, want retired", doc["status"])
	}
	hash, ok := doc["objectHash"].(string)
	if !ok || len(hash) != 64 {
		t.Errorf("objectHash = %v, want the 64-hex digest of the new payload", doc["objectHash"])
	}
	if _, ok := doc["dryRun"]; ok {
		t.Errorf("a real run must not carry dryRun: %v", doc)
	}
}

func TestRetireNonTTYWithoutForce(t *testing.T) {
	retireEnv(t)
	// runIn uses a non-terminal stdin: without --force the retire is a
	// usage error (exit 2) — --force never bypasses the other gates,
	// it only pre-authorizes the non-terminal run.
	code, _, errText := runIn([]string{"retire", "sto:lone", "--reason", "superseded by sto:next-gen", "--by", "test-bot"})
	if code != 2 {
		t.Fatalf("non-TTY without --force: exit = %d, want 2\nstderr: %s", code, errText)
	}
	if !strings.Contains(errText, "--force") {
		t.Errorf("non-TTY without --force: stderr must name --force, got %q", errText)
	}
}

func TestRetireCascadeCmt(t *testing.T) {
	w, _ := retireEnv(t)
	// --cascade=cmt retires the active cmt- notes discussing the
	// target alongside (each its own new instance).
	code, out, errText := runIn([]string{"retire", "sto:lone", "--reason", "superseded by sto:next-gen", "--force", "--by", "test-bot", "--cascade=cmt", "--json"})
	if code != 0 {
		t.Fatalf("cascade: exit = %d, want 0\nstdout: %s\nstderr: %s", code, out, errText)
	}
	doc := retireJSONDoc(t, out)
	cascade := doc["cascade"].(map[string]any)
	if cascade["mode"] != "cmt" {
		t.Errorf("cascade.mode = %v, want cmt", cascade["mode"])
	}
	cascaded, ok := cascade["cascaded"].([]any)
	if !ok || len(cascaded) != 1 || cascaded[0] != "test-ns/cmt:lone-note" {
		t.Fatalf("cascaded = %v, want [test-ns/cmt:lone-note]", cascade["cascaded"])
	}
	notes := retireLineUnits(t, w, "test-ns", "cmt", "lone-note")
	if len(notes) != 2 || notes[1].existence != "retired" {
		t.Errorf("cascaded note must gain a retired v2 instance: %+v", notes)
	}
	// The cascade preview of a dry-run lists the same notes and writes
	// nothing.
	w2, _ := retireEnv(t)
	code, out, _ = runIn([]string{"retire", "sto:lone", "--reason", "superseded by sto:next-gen", "--force", "--by", "test-bot", "--cascade=cmt", "--dry-run", "--json"})
	if code != 0 {
		t.Fatalf("cascade dry-run: exit = %d, want 0", code)
	}
	preview := retireJSONDoc(t, out)["cascade"].(map[string]any)["cascaded"].([]any)
	if len(preview) != 1 || preview[0] != "test-ns/cmt:lone-note" {
		t.Errorf("cascade preview = %v, want [test-ns/cmt:lone-note]", preview)
	}
	if units := retireLineUnits(t, w2, "test-ns", "sto", "lone"); len(units) != 1 {
		t.Errorf("cascade dry-run wrote a revision: %d instances, want 1", len(units))
	}
}

func TestRetireSubsystemImpacts(t *testing.T) {
	w, _ := retireEnv(t)
	if code, _, errText := runIn([]string{"retire", "sto:lone", "--reason", "superseded by sto:next-gen", "--force", "--by", "test-bot"}); code != 0 {
		t.Fatalf("retire: exit = %d, stderr %q", code, errText)
	}
	// `eka get` on the line returns the retired latest instance.
	code, out, errText := runIn([]string{"get", "test-ns/sto:lone"})
	if code != 0 {
		t.Fatalf("get latest: exit = %d, stderr %q", code, errText)
	}
	if !strings.Contains(out, "retired") {
		t.Errorf("get latest must return the retired instance:\n%s", out)
	}
	// The old version is still addressable and still active.
	code, out, errText = runIn([]string{"get", "test-ns/sto:lone:1"})
	if code != 0 {
		t.Fatalf("get v1: exit = %d, stderr %q", code, errText)
	}
	if !strings.Contains(out, "active") {
		t.Errorf("get v1 must still return the active old instance:\n%s", out)
	}
	// Integrity stays green: the retire is a tombstone publish, not a
	// deletion — no orphan, no deletions-never-applied violation.
	if code, out, errText := runIn([]string{"integrity", "check"}); code != 0 {
		t.Errorf("integrity check after retire: exit = %d, want 0\nstdout: %s\nstderr: %s", code, out, errText)
	}
	_ = w
}

func TestRetireAsArchived(t *testing.T) {
	w, _ := retireEnv(t)
	code, out, errText := runIn([]string{"retire", "sto:lone", "--reason", "kept for audit trail", "--as", "archived", "--force", "--by", "test-bot", "--json"})
	if code != 0 {
		t.Fatalf("retire --as archived: exit = %d, want 0\nstdout: %s\nstderr: %s", code, out, errText)
	}
	doc := retireJSONDoc(t, out)
	if doc["existenceState"].(map[string]any)["to"] != "archived" {
		t.Errorf("existenceState.to = %v, want archived", doc["existenceState"])
	}
	if units := retireLineUnits(t, w, "test-ns", "sto", "lone"); len(units) != 2 || units[1].existence != "archived" {
		t.Errorf("archived retire must publish a v2/archived instance: %+v", units)
	}
	// An invalid --as is a usage error.
	if code, _, _ := runIn([]string{"retire", "sto:lone", "--reason", "kept for audit trail", "--as", "deleted", "--force"}); code != 2 {
		t.Errorf("invalid --as: exit = %d, want 2", code)
	}
}
