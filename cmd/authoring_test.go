package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/maleolabs/eka-core/metadata"
	"github.com/maleolabs/eka-core/workspace"
)

// This file tests the draft-publish authoring commands at CLI level:
// eka new, eka edit, eka draft list, eka publish, eka discard — exit
// codes, namespace resolution, determinism and the end-to-end
// draft -> published-CKO flow.
//
// Tests seed the workspace registry directly (workspace + store are
// test-only imports of this package, documented in root.go).

// authoringEnv sets EKA_HOME to a temp workspace, creates an EKA
// repository at a fresh temp directory (eka.yaml: project/name = the
// directory basename, namespace = ns), registers it with the metadata
// identity (ADR-017 §5.3: RegisterRepoMetadata writes the namespace
// immediately) and moves the working directory into the repository. It
// returns the workspace and the repository path.
func authoringEnv(t *testing.T, ns string) (*workspace.Workspace, string) {
	t.Helper()
	t.Setenv("EKA_HOME", t.TempDir())
	w, err := workspace.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.Close() })
	repoDir := t.TempDir()
	writeEkaYAML(t, repoDir, filepath.Base(repoDir), filepath.Base(repoDir), ns)
	m := metadata.Metadata{Version: 1, Project: filepath.Base(repoDir), Name: filepath.Base(repoDir), Namespace: ns}
	if _, _, _, err := w.RegisterRepoMetadata(repoDir, m); err != nil {
		t.Fatal(err)
	}
	t.Chdir(repoDir)
	return w, repoDir
}

// projectOf resolves the registered project of the given repository.
func projectOf(t *testing.T, w *workspace.Workspace, repoDir string) string {
	t.Helper()
	repo, found, err := w.FindRepo(repoDir)
	if err != nil || !found {
		t.Fatalf("FindRepo = %v, %v", found, err)
	}
	return repo.ProjectID
}

// stoBody writes a publishable sto- content object (both required
// section keys) and returns its path.
func stoBody(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "body.json")
	if err := os.WriteFile(path, []byte(`{"description": "d", "acceptanceCriteria": "ac"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// draftFile returns the drafts path of one draft identity in the
// workspace.
func draftFile(t *testing.T, w *workspace.Workspace, project, typeToken, id string) string {
	t.Helper()
	return filepath.Join(w.Dir, "drafts", project, typeToken+"-"+id+".json")
}

// TestAuthoringHelpExitsZero: every authoring command documents itself.
func TestAuthoringHelpExitsZero(t *testing.T) {
	for _, args := range [][]string{
		{"new", "-h"}, {"edit", "-h"}, {"draft", "-h"}, {"draft", "list", "-h"},
		{"draft", "validate", "-h"},
		{"publish", "-h"}, {"discard", "-h"},
	} {
		code, text, _ := runIn(args)
		if code != 0 {
			t.Errorf("args %v: exit = %d, want 0", args, code)
		}
		if !strings.Contains(text, "eka ") {
			t.Errorf("args %v: help output missing usage", args)
		}
	}
}

// TestNewScaffoldsDraft: `eka new` inside a registered repository
// scaffolds the draft under the repository's project with the
// repository's default namespace, and the template carries the type's
// required sections.
func TestNewScaffoldsDraft(t *testing.T) {
	w, _ := authoringEnv(t, "atrium-api")
	code, text, errText := runIn([]string{"new", "sto:my-item"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", code, text, errText)
	}
	project := projectOf(t, w, mustAbs(t, "."))
	data, err := os.ReadFile(draftFile(t, w, project, "sto", "my-item"))
	if err != nil {
		t.Fatalf("draft file missing: %v", err)
	}
	content := string(data)
	for _, want := range []string{
		`"namespace": "atrium-api"`, // the repository default
		`"type": "sto"`,
		`"id": "my-item"`,
		`"executionState": "planned"`,
		`"existenceState": "active"`,
		`"description"`,
		`"acceptanceCriteria"`,
	} {
		if !strings.Contains(content, want) {
			t.Errorf("draft template missing %q:\n%s", want, content)
		}
	}
	if !strings.Contains(text, "sto:my-item") {
		t.Errorf("output must show the draft identity:\n%s", text)
	}
	if strings.Contains(text, "\x1b") {
		t.Errorf("non-TTY output must stay ANSI-free:\n%s", text)
	}
}

// TestNewByDefaultsToGitIdentity: the change-log authority of a
// scaffolded draft defaults to `git config user.name` — the same
// BySource resolution `eka note` and `eka transition` use, so every
// authoring command defaults to the same identity (the author-identity
// consistency fix). --by overrides the default; an unresolved git
// identity is a usage error (exit 2), never a silent fallback to a
// placeholder.
func TestNewByDefaultsToGitIdentity(t *testing.T) {
	w, _ := authoringEnv(t, "atrium-api")
	project := projectOf(t, w, mustAbs(t, "."))
	gitIdentityEnv(t, "test-agent")

	// Without --by the draft carries the git identity: the change-log
	// entries AND the author field.
	if code, _, errText := runIn([]string{"new", "sto:by-default"}); code != 0 {
		t.Fatalf("new without --by: exit = %d, stderr %q", code, errText)
	}
	raw, err := os.ReadFile(draftFile(t, w, project, "sto", "by-default"))
	if err != nil {
		t.Fatalf("draft file missing: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("draft is not valid JSON: %v", err)
	}
	if doc["author"] != "test-agent" {
		t.Errorf("draft author = %v, want the git default identity", doc["author"])
	}
	cl := doc["changeLog"].([]any)
	for _, e := range cl {
		if e.(map[string]any)["by"] != "test-agent" {
			t.Errorf("changeLog entry by = %v, want the git default identity", e.(map[string]any)["by"])
		}
	}

	// --by overrides the git identity (change-log and author alike).
	if code, _, errText := runIn([]string{"new", "sto:by-flag", "--by", "Foo"}); code != 0 {
		t.Fatalf("new --by Foo: exit = %d, stderr %q", code, errText)
	}
	raw, err = os.ReadFile(draftFile(t, w, project, "sto", "by-flag"))
	if err != nil {
		t.Fatalf("draft file missing: %v", err)
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("draft is not valid JSON: %v", err)
	}
	if doc["author"] != "Foo" {
		t.Errorf("draft author = %v, want the --by override", doc["author"])
	}
	for _, e := range doc["changeLog"].([]any) {
		if e.(map[string]any)["by"] != "Foo" {
			t.Errorf("changeLog entry by = %v, want the --by override", e.(map[string]any)["by"])
		}
	}

	// An unresolved git identity refuses (exit 2, usage class) with
	// the --by hint; nothing scaffolds.
	gitIdentityEnv(t, "")
	if code, _, errText := runIn([]string{"new", "sto:no-by"}); code != 2 || !strings.Contains(errText, "pass --by <name>") {
		t.Errorf("missing git identity: exit = %d, stderr = %q, want 2 + --by hint", code, errText)
	}
	if _, err := os.Stat(draftFile(t, w, project, "sto", "no-by")); !os.IsNotExist(err) {
		t.Errorf("a refused run must not leave a draft file behind")
	}
}

// TestNewRelationshipFlagsAccumulate: repeated --depends-on /
// --derives-from flags accumulate instead of silently overriding (the
// last occurrence no longer wins). Comma-joined values and repeated
// occurrences mix freely; every target lands in the draft's
// relationships object.
func TestNewRelationshipFlagsAccumulate(t *testing.T) {
	w, _ := authoringEnv(t, "atrium-api")
	project := projectOf(t, w, mustAbs(t, "."))

	// Repeated flags accumulate: --depends-on a --depends-on b must
	// record BOTH edges (the acceptance criterion).
	if code, _, errText := runIn([]string{"new", "sto:rep",
		"--depends-on", "sto:a", "--depends-on", "sto:b"}); code != 0 {
		t.Fatalf("new repeated --depends-on: exit = %d\nstderr: %s", code, errText)
	}
	assertDraftRelationships(t, w, project, "sto", "rep", map[string][]string{
		"dependsOn": {"sto:a", "sto:b"},
	})

	// Comma-joined values: --derives-from a,b records both targets.
	if code, _, errText := runIn([]string{"new", "sto:comma",
		"--derives-from", "sto:a,sto:b"}); code != 0 {
		t.Fatalf("new comma-joined --derives-from: exit = %d\nstderr: %s", code, errText)
	}
	assertDraftRelationships(t, w, project, "sto", "comma", map[string][]string{
		"derivesFrom": {"sto:a", "sto:b"},
	})

	// Mixed forms: comma-joined and repeated occurrences accumulate
	// across fields, in one invocation.
	if code, _, errText := runIn([]string{"new", "sto:mixed",
		"--depends-on", "sto:a,sto:b", "--depends-on", "sto:c",
		"--derives-from", "ctr:x", "--derives-from", "ctr:y"}); code != 0 {
		t.Fatalf("new mixed forms: exit = %d\nstderr: %s", code, errText)
	}
	assertDraftRelationships(t, w, project, "sto", "mixed", map[string][]string{
		"dependsOn":   {"sto:a", "sto:b", "sto:c"},
		"derivesFrom": {"ctr:x", "ctr:y"},
	})

	// A single occurrence keeps its existing behavior.
	if code, _, errText := runIn([]string{"new", "sto:single",
		"--depends-on", "sto:only"}); code != 0 {
		t.Fatalf("new single --depends-on: exit = %d\nstderr: %s", code, errText)
	}
	assertDraftRelationships(t, w, project, "sto", "single", map[string][]string{
		"dependsOn": {"sto:only"},
	})
}

// assertDraftRelationships parses the scaffolded draft and asserts its
// relationships object: camelCase field keys, targets sorted and
// deduplicated by the core template (byte-deterministic).
func assertDraftRelationships(t *testing.T, w *workspace.Workspace, project, typeToken, id string, want map[string][]string) {
	t.Helper()
	data, err := os.ReadFile(draftFile(t, w, project, typeToken, id))
	if err != nil {
		t.Fatalf("draft file missing: %v", err)
	}
	var doc struct {
		Relationships map[string][]string `json:"relationships"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("draft is not valid JSON: %v\n%s", err, data)
	}
	if len(doc.Relationships) != len(want) {
		t.Fatalf("relationships = %v, want %v", doc.Relationships, want)
	}
	for field, targets := range want {
		if got := strings.Join(doc.Relationships[field], ","); got != strings.Join(targets, ",") {
			t.Errorf("relationships[%q] = %v, want %v", field, doc.Relationships[field], targets)
		}
	}
}

func mustAbs(t *testing.T, p string) string {
	t.Helper()
	abs, err := filepath.Abs(p)
	if err != nil {
		t.Fatal(err)
	}
	return abs
}

// TestNewNamespaceResolutionMatrix: the §3.2 + D6 resolution rules —
// repository default for unqualified targets inside a repo, the
// qualified target equal to the repository namespace, the refusal of
// a qualified target with a different namespace, and the refusal
// hints outside a registered repository.
func TestNewNamespaceResolutionMatrix(t *testing.T) {
	w, _ := authoringEnv(t, "atrium-api")

	// Unqualified inside the repo: the repository default applies.
	runIn([]string{"new", "sto:default-ns"})
	project := projectOf(t, w, mustAbs(t, "."))
	assertDraftNS(t, w, project, "sto-default-ns.json", "atrium-api")

	// A qualified target whose namespace equals the repository's
	// namespace is allowed (D6 keeps the in-repo path working).
	runIn([]string{"new", "atrium-api/sto:qualified"})
	assertDraftNS(t, w, project, "sto-qualified.json", "atrium-api")

	// D6: a qualified target with a namespace that differs from the
	// repository's namespace is refused — cross-platform access is
	// read-only (the old --namespace override shape).
	code, _, errText := runIn([]string{"new", "feather/sto:foreign"})
	if code != 1 {
		t.Errorf("different-namespace target: exit = %d, want 1", code)
	}
	if !strings.Contains(errText, "refused: namespace feather differs from the repository namespace atrium-api") ||
		!strings.Contains(errText, "cross-platform access is read-only") {
		t.Errorf("stderr must carry the D6 refusal, got %q", errText)
	}
	if _, err := os.Stat(draftFile(t, w, project, "sto", "foreign")); err == nil {
		t.Error("a refused scaffold must not write a draft")
	}

	// Outside an EKA repository (no eka.yaml), an unqualified target
	// is refused with the ADR-018 gate message (exit 1).
	outside := t.TempDir()
	t.Chdir(outside)
	code, _, errText = runIn([]string{"new", "sto:ghost"})
	if code != 1 {
		t.Errorf("outside an EKA repository, exit = %d, want 1", code)
	}
	if !strings.Contains(errText, "is not an EKA repository (no eka.yaml)") ||
		!strings.Contains(errText, "run 'eka init' first") {
		t.Errorf("stderr must carry the ADR-018 gate refusal, got %q", errText)
	}
	if _, err := os.Stat(draftFile(t, w, project, "sto", "ghost")); err == nil {
		t.Error("a refused scaffold must not write a draft")
	}

	// Outside an EKA repository, even a qualified target is refused
	// by the same gate (--project no longer exists).
	code, _, errText = runIn([]string{"new", "feather/sto:remote"})
	if code != 1 {
		t.Errorf("qualified target outside an EKA repository: exit = %d, want 1", code)
	}
	if !strings.Contains(errText, "is not an EKA repository (no eka.yaml)") {
		t.Errorf("stderr must refuse with the ADR-018 gate, got %q", errText)
	}

	// Inside an EKA repository that is not registered yet, the
	// unqualified target keeps the spec's hint (the namespace was
	// never resolved).
	unregistered := t.TempDir()
	writeEkaYAML(t, unregistered, filepath.Base(unregistered), filepath.Base(unregistered), "atrium-api")
	t.Chdir(unregistered)
	code, _, errText = runIn([]string{"new", "sto:ghost"})
	if code != 1 {
		t.Errorf("unregistered EKA repository: exit = %d, want 1", code)
	}
	if !strings.Contains(errText, "cannot resolve a namespace here") ||
		!strings.Contains(errText, "run 'eka sync' once to resolve the repository identity from eka.yaml") {
		t.Errorf("stderr must carry the spec's hint, got %q", errText)
	}
	// The qualified target in the same unregistered repository is
	// refused: the project cannot resolve without a registration
	// (--project no longer exists).
	code, _, errText = runIn([]string{"new", "feather/sto:remote"})
	if code != 1 {
		t.Errorf("qualified target in an unregistered EKA repository: exit = %d, want 1", code)
	}
	if !strings.Contains(errText, "cannot resolve a project here") {
		t.Errorf("stderr must refuse the unresolvable project, got %q", errText)
	}

	// An invalid target is refused (exit 1).
	code, _, _ = runIn([]string{"new", "bogus"})
	if code != 1 {
		t.Errorf("invalid target: exit = %d, want 1", code)
	}
	// A canonical published form is refused.
	code, _, errText = runIn([]string{"new", "sto:x:1"})
	if code != 1 || !strings.Contains(errText, "drafts only") {
		t.Errorf("published form: exit = %d, %q; want 1 + drafts-only", code, errText)
	}
}

// assertDraftNS reads one draft file and asserts its namespace field.
func assertDraftNS(t *testing.T, w *workspace.Workspace, project, name, wantNS string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(w.Dir, "drafts", project, name))
	if err != nil {
		t.Fatalf("draft %s missing: %v", name, err)
	}
	if !strings.Contains(string(data), `"namespace": "`+wantNS+`"`) {
		t.Errorf("draft %s must carry namespace %q:\n%s", name, wantNS, data)
	}
}

// TestNewCollision: a second draft with the same project/type/id is
// refused with exit 1.
func TestNewCollision(t *testing.T) {
	w, _ := authoringEnv(t, "feather")
	runIn([]string{"new", "sto:my-item"})
	code, _, errText := runIn([]string{"new", "sto:my-item"})
	if code != 1 {
		t.Errorf("collision exit = %d, want 1", code)
	}
	if !strings.Contains(errText, "already exists in project") {
		t.Errorf("stderr must explain the collision, got %q", errText)
	}
	_ = w
}

// TestNewContentFile: --content-file merges a JSON object into the
// draft content while the template stays the schema's.
func TestNewContentFile(t *testing.T) {
	w, _ := authoringEnv(t, "feather")
	body := filepath.Join(t.TempDir(), "c.json")
	if err := os.WriteFile(body, []byte(`{"description": "custom", "extra": "kept"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, errText := runIn([]string{"new", "sto:custom", "--content-file", body})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\n%s", code, errText)
	}
	project := projectOf(t, w, mustAbs(t, "."))
	data, err := os.ReadFile(draftFile(t, w, project, "sto", "custom"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if !strings.Contains(content, `"namespace": "feather"`) || !strings.Contains(content, `"description": "custom"`) {
		t.Errorf("content-file draft must carry the template + the merged object:\n%s", content)
	}
	if !strings.Contains(content, `"extra": "kept"`) {
		t.Errorf("extra content keys must be merged:\n%s", content)
	}
}

// TestNewEditNonTTY: --edit without a terminal is refused (exit 1).
func TestNewEditNonTTY(t *testing.T) {
	authoringEnv(t, "feather")
	code, _, errText := runIn([]string{"new", "sto:x", "--edit"})
	if code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
	if !strings.Contains(errText, "--edit requires a terminal") {
		t.Errorf("stderr must explain the TTY requirement, got %q", errText)
	}
}

// TestEditRefusals: `eka edit` is TTY-only and strictly draft-only.
func TestEditRefusals(t *testing.T) {
	authoringEnv(t, "feather")
	// Non-TTY refusal (exit 2).
	code, _, errText := runIn([]string{"edit", "sto:x"})
	if code != 2 {
		t.Errorf("non-TTY edit exit = %d, want 2", code)
	}
	if !strings.Contains(errText, "requires a terminal") {
		t.Errorf("stderr must explain the TTY requirement, got %q", errText)
	}
	// Published canonical form refusal.
	code, _, errText = runIn([]string{"edit", "sto:x:1"})
	if code != 2 {
		t.Errorf("published form exit = %d, want 2", code)
	}
	if !strings.Contains(errText, "is a published knowledge object; drafts only") {
		t.Errorf("stderr must refuse the published form, got %q", errText)
	}
}

// TestDraftList: the backlog renders deterministically with the
// per-draft validation marker.
func TestDraftList(t *testing.T) {
	w, _ := authoringEnv(t, "atrium-api")
	body := stoBody(t)
	runIn([]string{"new", "sto:alpha", "--content-file", body})
	runIn([]string{"new", "sto:beta", "--content-file", body})
	// A broken draft (invalid JSON) written directly into the backlog.
	project := projectOf(t, w, mustAbs(t, "."))
	broken := draftFile(t, w, project, "sto", "broken")
	if err := os.WriteFile(broken, []byte(`{"namespace": "x",`), 0o644); err != nil {
		t.Fatal(err)
	}

	code, text, errText := runIn([]string{"draft", "list"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", code, text, errText)
	}
	for _, want := range []string{"Drafts", "Project   all", "sto:alpha", "sto:beta", "sto:broken"} {
		if !strings.Contains(text, want) {
			t.Errorf("draft list missing %q:\n%s", want, text)
		}
	}
	if !strings.Contains(text, "invalid — 1 error") {
		t.Errorf("draft list must mark the broken draft:\n%s", text)
	}
	if !strings.Contains(text, "Summary:") {
		t.Errorf("draft list must render the summary:\n%s", text)
	}
	if strings.Contains(text, "\x1b") {
		t.Errorf("non-TTY output must stay ANSI-free:\n%s", text)
	}
	// Determinism: two runs produce byte-identical output.
	_, again, _ := runIn([]string{"draft", "list"})
	if text != again {
		t.Error("draft list output differs between runs")
	}
	// Project filter.
	code, text, _ = runIn([]string{"draft", "list", "--project", project})
	if code != 0 || !strings.Contains(text, "Project   "+project) {
		t.Errorf("--project filter: exit = %d\n%s", code, text)
	}
	// Empty backlog: informational, exit 0.
	code, _, _ = runIn([]string{"draft", "list", "--project", "empty-project"})
	if code != 0 {
		t.Errorf("empty backlog exit = %d, want 0", code)
	}
}

// --- eka draft validate ------------------------------------------------

// TestDraftValidateValid: `eka draft validate` on a publishable draft
// exits 0 with the short success message, and the draft file survives
// (non-destructive by contract).
func TestDraftValidateValid(t *testing.T) {
	w, _ := authoringEnv(t, "feather")
	body := stoBody(t)
	if code, _, errText := runIn([]string{"new", "feather/sto:my-item", "--content-file", body}); code != 0 {
		t.Fatalf("new: exit = %d\n%s", code, errText)
	}
	project := projectOf(t, w, mustAbs(t, "."))
	draft := draftFile(t, w, project, "sto", "my-item")

	code, text, errText := runIn([]string{"draft", "validate", "feather/sto:my-item"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", code, text, errText)
	}
	if !strings.Contains(text, "feather/sto:my-item is valid") || !strings.Contains(text, "ready to publish") {
		t.Errorf("stdout must carry the success message:\n%s", text)
	}
	if strings.Contains(text, "\x1b") {
		t.Errorf("non-TTY output must stay ANSI-free:\n%s", text)
	}
	if _, err := os.Stat(draft); err != nil {
		t.Errorf("validate must never remove the draft: %v", err)
	}
	// The validated draft still publishes afterwards (single-use
	// ticket intact).
	if code, _, errText := runIn([]string{"publish", "feather/sto:my-item"}); code != 0 {
		t.Fatalf("publish after validate: exit = %d\n%s", code, errText)
	}
}

// TestDraftValidateFailure: a draft that fails CKO-level validation
// exits 1 with the same report publish refuses with (Verdict: FAIL),
// and the draft survives — the early-warning loop before the publish
// gate.
func TestDraftValidateFailure(t *testing.T) {
	w, _ := authoringEnv(t, "feather")
	project := projectOf(t, w, mustAbs(t, "."))
	path := draftFile(t, w, project, "spec", "broken")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	bad := `{
  "namespace": "feather",
  "type": "spec",
  "id": "broken",
  "revision": 1,
  "state": {
    "contentState": "bogus",
    "existenceState": "active"
  },
  "changeLog": [
    {"date": "2026-08-07", "domain": "existenceState", "from": "-", "to": "active", "by": "Engineering"},
    {"date": "2026-08-07", "domain": "contentState", "from": "-", "to": "bogus", "by": "Engineering"}
  ],
  "content": {
    "purpose": "p",
    "content": "c"
  }
}`
	if err := os.WriteFile(path, []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}

	code, text, errText := runIn([]string{"draft", "validate", "feather/spec:broken"})
	if code != 1 {
		t.Fatalf("exit = %d, want 1\nstdout: %s\nstderr: %s", code, text, errText)
	}
	if !strings.Contains(text, "Draft validation") || !strings.Contains(text, "Verdict: FAIL") {
		t.Errorf("stdout must render the validation report:\n%s", text)
	}
	if !strings.Contains(errText, "failed validation") {
		t.Errorf("stderr must state the failed validation, got %q", errText)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("validate must never remove the draft: %v", err)
	}

	// A structurally malformed draft (unreadable as JSON) renders the
	// scan report and exits 1 too.
	malformed := draftFile(t, w, project, "sto", "malformed")
	if err := os.WriteFile(malformed, []byte(`{"namespace": "x",`), 0o644); err != nil {
		t.Fatal(err)
	}
	code, text, errText = runIn([]string{"draft", "validate", "feather/sto:malformed"})
	if code != 1 {
		t.Fatalf("malformed: exit = %d, want 1\nstdout: %s\nstderr: %s", code, text, errText)
	}
	if !strings.Contains(text, "structural errors") || !strings.Contains(text, "Verdict: FAIL") {
		t.Errorf("malformed: stdout must render the scan report:\n%s", text)
	}
}

// TestDraftValidateNotFound: a missing draft exits 1 with the
// deterministic draft-not-found guard (the publish class), and a
// malformed target is a usage error (exit 2).
func TestDraftValidateNotFound(t *testing.T) {
	authoringEnv(t, "feather")
	code, _, errText := runIn([]string{"draft", "validate", "feather/sto:ghost"})
	if code != 1 {
		t.Errorf("missing draft: exit = %d, want 1", code)
	}
	if !strings.Contains(errText, "not found") {
		t.Errorf("stderr must report the missing draft, got %q", errText)
	}

	code, _, _ = runIn([]string{"draft", "validate", "sto:"})
	if code != 2 {
		t.Errorf("malformed target: exit = %d, want 2 (usage)", code)
	}
}

// TestPublishEndToEnd: new -> publish -> get shows the CKO -> draft
// gone -> second publish fails at the read.
func TestPublishEndToEnd(t *testing.T) {
	w, _ := authoringEnv(t, "feather")
	body := stoBody(t)
	code, _, errText := runIn([]string{"new", "feather/sto:my-item", "--content-file", body})
	if code != 0 {
		t.Fatalf("new: exit = %d\n%s", code, errText)
	}
	project := projectOf(t, w, mustAbs(t, "."))
	draft := draftFile(t, w, project, "sto", "my-item")

	code, text, errText := runIn([]string{"publish", "feather/sto:my-item"})
	if code != 0 {
		t.Fatalf("publish: exit = %d, want 0\nstdout: %s\nstderr: %s", code, text, errText)
	}
	for _, want := range []string{"feather/sto:my-item:1", "Instance Version", "Object Hash"} {
		if !strings.Contains(text, want) {
			t.Errorf("publish output missing %q:\n%s", want, text)
		}
	}
	if _, err := os.Stat(draft); !os.IsNotExist(err) {
		t.Errorf("the draft file must be removed by publish: %v", err)
	}

	// The published CKO is readable through `eka get`.
	code, text, errText = runIn([]string{"get", "feather/sto:my-item:1"})
	if code != 0 {
		t.Fatalf("get: exit = %d, want 0\nstdout: %s\nstderr: %s", code, text, errText)
	}
	if !strings.Contains(text, `"namespace": "feather"`) || !strings.Contains(text, `"id": "my-item"`) {
		t.Errorf("get must show the published object:\n%s", text)
	}

	// Second publish: the draft file is the single-use ticket. The
	// guard message names the project that was tried.
	code, _, errText = runIn([]string{"publish", "feather/sto:my-item"})
	if code != 1 {
		t.Errorf("second publish exit = %d, want 1", code)
	}
	if !strings.Contains(errText, "not found in project") || !strings.Contains(errText, "already published or discarded") {
		t.Errorf("stderr must carry the duplicate-publish guard naming the project, got %q", errText)
	}
}

// TestPublishValidationFailureKeepsDraft: a draft that fails CKO-level
// validation exits 1 with the report and the draft survives.
func TestPublishValidationFailureKeepsDraft(t *testing.T) {
	w, _ := authoringEnv(t, "feather")
	project := projectOf(t, w, mustAbs(t, "."))
	path := draftFile(t, w, project, "spec", "broken")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	bad := `{
  "namespace": "feather",
  "type": "spec",
  "id": "broken",
  "revision": 1,
  "state": {
    "contentState": "bogus",
    "existenceState": "active"
  },
  "changeLog": [
    {"date": "2026-08-07", "domain": "existenceState", "from": "-", "to": "active", "by": "Engineering"},
    {"date": "2026-08-07", "domain": "contentState", "from": "-", "to": "bogus", "by": "Engineering"}
  ],
  "content": {
    "purpose": "p",
    "content": "c"
  }
}`
	if err := os.WriteFile(path, []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}

	code, text, errText := runIn([]string{"publish", "feather/spec:broken"})
	if code != 1 {
		t.Fatalf("exit = %d, want 1\nstdout: %s\nstderr: %s", code, text, errText)
	}
	if !strings.Contains(text, "Verdict: FAIL") {
		t.Errorf("stdout must render the validation report:\n%s", text)
	}
	if !strings.Contains(errText, "the draft was kept") {
		t.Errorf("stderr must state the draft was kept, got %q", errText)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("the draft must survive a refused publish: %v", err)
	}
}

// TestPublishInstanceVersionAutoAndOverride: auto-assignment is max+1
// per line across projects; --instance-version overrides when it
// exceeds the line's highest.
func TestPublishInstanceVersionAutoAndOverride(t *testing.T) {
	w, _ := authoringEnv(t, "feather")
	body := stoBody(t)

	// Draft + publish in project A: auto v1.
	if code, _, errText := runIn([]string{"new", "feather/sto:line", "--content-file", body}); code != 0 {
		t.Fatalf("new: %d\n%s", code, errText)
	}
	if code, _, errText := runIn([]string{"publish", "feather/sto:line"}); code != 0 {
		t.Fatalf("publish v1: %d\n%s", code, errText)
	}

	// A second draft of the same line in project B (published from a
	// repository of that project): auto v2.
	repoB := t.TempDir()
	projectB := "proj-b"
	writeEkaYAML(t, repoB, projectB, filepath.Base(repoB), "feather")
	if _, _, _, err := w.RegisterRepo(repoB, projectB); err != nil {
		t.Fatal(err)
	}
	t.Chdir(repoB)
	if code, _, errText := runIn([]string{"new", "feather/sto:line", "--content-file", body}); code != 0 {
		t.Fatalf("new in proj-b: %d\n%s", code, errText)
	}
	code, text, errText := runIn([]string{"publish", "feather/sto:line"})
	if code != 0 || !strings.Contains(text, "feather/sto:line:2") {
		t.Fatalf("auto v2: exit = %d\n%s\n%s", code, text, errText)
	}

	// Override: a third project publishes with --instance-version 5.
	repoC := t.TempDir()
	projectC := "proj-c"
	writeEkaYAML(t, repoC, projectC, filepath.Base(repoC), "feather")
	if _, _, _, err := w.RegisterRepo(repoC, projectC); err != nil {
		t.Fatal(err)
	}
	t.Chdir(repoC)
	if code, _, errText := runIn([]string{"new", "feather/sto:line", "--content-file", body}); code != 0 {
		t.Fatalf("new in proj-c: %d\n%s", code, errText)
	}
	code, text, errText = runIn([]string{"publish", "feather/sto:line", "--instance-version", "5"})
	if code != 0 || !strings.Contains(text, "feather/sto:line:5") {
		t.Fatalf("override v5: exit = %d\n%s\n%s", code, text, errText)
	}
	code, _, _ = runIn([]string{"get", "feather/sto:line:5", "--no-content"})
	if code != 0 {
		t.Errorf("the overridden instance must be readable, exit = %d", code)
	}

	// An override at or below the line's highest (5) is refused.
	repoD := t.TempDir()
	writeEkaYAML(t, repoD, "proj-d", filepath.Base(repoD), "feather")
	if _, _, _, err := w.RegisterRepo(repoD, "proj-d"); err != nil {
		t.Fatal(err)
	}
	t.Chdir(repoD)
	runIn([]string{"new", "feather/sto:line", "--content-file", body})
	code, _, errText = runIn([]string{"publish", "feather/sto:line", "--instance-version", "5"})
	if code != 2 {
		t.Errorf("override at the highest: exit = %d, want 2", code)
	}
	if !strings.Contains(errText, "must exceed the line's highest") {
		t.Errorf("stderr must explain the override rule, got %q", errText)
	}
}

// TestPublishUnresolvedRelationship: a non-draft unresolved reference
// blocks the publish (exit 1, draft kept); draft tolerance publishes.
func TestPublishUnresolvedRelationship(t *testing.T) {
	w, _ := authoringEnv(t, "feather")
	body := stoBody(t)

	// sto- carries no content-state: unresolved -> blocking.
	if code, _, errText := runIn([]string{"new", "sto:blocked", "--content-file", body,
		"--depends-on", "feather/ctr:ghost"}); code != 0 {
		t.Fatalf("new: %d\n%s", code, errText)
	}
	code, text, errText := runIn([]string{"publish", "feather/sto:blocked"})
	if code != 1 {
		t.Fatalf("blocked publish: exit = %d, want 1\nstdout: %s\nstderr: %s", code, text, errText)
	}
	if !strings.Contains(text, "unresolved reference") {
		t.Errorf("the report must carry the unresolved-reference finding:\n%s", text)
	}
	project := projectOf(t, w, mustAbs(t, "."))
	if _, err := os.Stat(draftFile(t, w, project, "sto", "blocked")); err != nil {
		t.Errorf("the blocked draft must be kept: %v", err)
	}

	// A knowledge-type draft (content-state: draft from the template)
	// tolerates the same unresolved reference: publishes.
	specBody := filepath.Join(t.TempDir(), "s.json")
	if err := os.WriteFile(specBody, []byte(`{"purpose": "p", "content": "c"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, errText := runIn([]string{"new", "feather/spec:tol", "--content-file", specBody,
		"--dimension", "specifications", "--depends-on", "feather/ctr:ghost"}); code != 0 {
		t.Fatalf("new spec: %d\n%s", code, errText)
	}
	code, _, errText = runIn([]string{"publish", "feather/spec:tol"})
	if code != 0 {
		t.Fatalf("draft tolerance must publish: exit = %d\n%s", code, errText)
	}
}

// TestDiscard: --force discards (exit 0); non-TTY without --force is
// refused (exit 2); a missing draft is refused (exit 2).
func TestDiscard(t *testing.T) {
	w, _ := authoringEnv(t, "feather")
	body := stoBody(t)
	if code, _, errText := runIn([]string{"new", "sto:my-item", "--content-file", body}); code != 0 {
		t.Fatalf("new: %d\n%s", code, errText)
	}
	project := projectOf(t, w, mustAbs(t, "."))
	draft := draftFile(t, w, project, "sto", "my-item")

	// Non-TTY without --force: refused.
	code, _, errText := runIn([]string{"discard", "feather/sto:my-item"})
	if code != 2 {
		t.Errorf("non-TTY discard exit = %d, want 2", code)
	}
	if !strings.Contains(errText, "--force") {
		t.Errorf("stderr must require --force, got %q", errText)
	}
	if _, err := os.Stat(draft); err != nil {
		t.Errorf("the draft must survive the refused discard: %v", err)
	}

	// --force discards.
	code, text, errText := runIn([]string{"discard", "feather/sto:my-item", "--force"})
	if code != 0 {
		t.Fatalf("force discard exit = %d, want 0\n%s\n%s", code, text, errText)
	}
	if !strings.Contains(text, "Discarded draft feather/sto:my-item.") {
		t.Errorf("output must confirm the discard:\n%s", text)
	}
	if _, err := os.Stat(draft); !os.IsNotExist(err) {
		t.Errorf("the draft file must be gone: %v", err)
	}

	// A missing draft is refused (exit 2).
	code, _, errText = runIn([]string{"discard", "feather/sto:my-item", "--force"})
	if code != 2 {
		t.Errorf("missing draft discard exit = %d, want 2", code)
	}
	if !strings.Contains(errText, "not found") {
		t.Errorf("stderr must report the missing draft, got %q", errText)
	}
}

// TestNewOutputNotANSI: the new-command output stays ANSI-free on
// non-TTY.
func TestNewOutputNotANSI(t *testing.T) {
	authoringEnv(t, "feather")
	var out, errb bytes.Buffer
	code := Execute([]string{"new", "sto:x"}, strings.NewReader(""), &out, &errb)
	if code != 0 {
		t.Fatalf("new: exit = %d", code)
	}
	if strings.Contains(out.String(), "\x1b") || strings.Contains(errb.String(), "\x1b") {
		t.Errorf("non-TTY authoring output must not contain ANSI escapes:\n%s\n%s", out.String(), errb.String())
	}
}

// --- M1/D6: publish without --project, cross-project fallback --------

// TestPublishCrossProjectWithoutFlag (M1 + D6): `eka publish` has no
// --project flag anymore — the project is the repository registered at
// the current directory, and the cross-project fallback resolves a
// draft that lives in another project (a draft visible in `eka draft
// list` is always addressable). `eka discard --project` keeps the flag
// (edit/discard are unaffected by D6).
func TestPublishCrossProjectWithoutFlag(t *testing.T) {
	w, repoA := authoringEnv(t, "feather")
	projectA := projectOf(t, w, mustAbs(t, "."))
	body := stoBody(t)
	// The draft lives in project A (the cwd repository).
	if code, _, errText := runIn([]string{"new", "feather/sto:proj-item", "--content-file", body}); code != 0 {
		t.Fatalf("new: %d\n%s", code, errText)
	}
	// Move into a second repository registered under a different
	// project: without --project the cwd project (B) does not hold the
	// draft — the cross-project fallback resolves it from project A and
	// the note names the project it was resolved from.
	repoB := t.TempDir()
	projectB := "proj-b"
	writeEkaYAML(t, repoB, projectB, filepath.Base(repoB), "feather")
	if _, _, _, err := w.RegisterRepo(repoB, projectB); err != nil {
		t.Fatal(err)
	}
	t.Chdir(repoB)
	code, text, errText := runIn([]string{"publish", "feather/sto:proj-item"})
	if code != 0 {
		t.Fatalf("publish without --project (cross-project fallback): exit = %d\n%s\n%s", code, text, errText)
	}
	if !strings.Contains(text, "feather/sto:proj-item:1") {
		t.Errorf("publish output must show the form:\n%s", text)
	}
	if !strings.Contains(text, "resolved from project "+projectA) {
		t.Errorf("publish output must carry the cross-project note:\n%s", text)
	}
	if _, err := os.Stat(draftFile(t, w, projectA, "sto", "proj-item")); !os.IsNotExist(err) {
		t.Errorf("the draft must be removed: %v", err)
	}

	// discard with --project A: create another draft in project A,
	// then discard it from repository B with the project flag (the
	// flag survives on edit/discard).
	t.Chdir(repoA)
	if code, _, errText := runIn([]string{"new", "feather/sto:discard-me", "--content-file", body}); code != 0 {
		t.Fatalf("new: %d\n%s", code, errText)
	}
	t.Chdir(repoB)
	code, _, errText = runIn([]string{"discard", "feather/sto:discard-me", "--project", projectA, "--force"})
	if code != 0 {
		t.Fatalf("discard --project: exit = %d\n%s", code, errText)
	}
	if _, err := os.Stat(draftFile(t, w, projectA, "sto", "discard-me")); !os.IsNotExist(err) {
		t.Errorf("the discarded draft must be gone: %v", err)
	}
}

// --- m5b: publish target namespace mismatch (CLI) ----------------------

// TestPublishTargetNamespaceMismatchCLI (m5b): a target namespace that
// differs from the draft frontmatter's namespace is a deterministic
// usage error.
func TestPublishTargetNamespaceMismatchCLI(t *testing.T) {
	authoringEnv(t, "feather")
	body := stoBody(t)
	if code, _, errText := runIn([]string{"new", "feather/sto:nsx", "--content-file", body}); code != 0 {
		t.Fatalf("new: %d\n%s", code, errText)
	}
	code, _, errText := runIn([]string{"publish", "other/sto:nsx"})
	if code != 2 {
		t.Errorf("mismatched target namespace: exit = %d, want 2", code)
	}
	if !strings.Contains(errText, "target namespace other does not match draft namespace feather") {
		t.Errorf("stderr must carry the mismatch error, got %q", errText)
	}
	// The draft survives the refused attempt.
	w := mustWorkspace(t)
	t.Cleanup(func() { w.Close() })
	if _, err := os.Stat(draftFile(t, w, projectOf(t, w, mustAbs(t, ".")), "sto", "nsx")); err != nil {
		t.Errorf("the draft must survive a refused publish: %v", err)
	}
}

// --- m5c: publish identity mismatch (CLI) ------------------------------

// TestPublishIdentityMismatchCLI (m5c): the draft's identity is its
// frontmatter, not its file name — publishing under the file-name
// identity while the frontmatter carries a different identity is a
// deterministic usage error (the frontmatter identity would silently
// publish under the target's address), and the matching identity
// publishes normally.
func TestPublishIdentityMismatchCLI(t *testing.T) {
	w, _ := authoringEnv(t, "feather")
	body := stoBody(t)
	if code, _, errText := runIn([]string{"new", "feather/sto:setup-ci-web", "--content-file", body}); code != 0 {
		t.Fatalf("new: %d\n%s", code, errText)
	}
	project := projectOf(t, w, mustAbs(t, "."))
	draft := draftFile(t, w, project, "sto", "setup-ci-web")
	data, err := os.ReadFile(draft)
	if err != nil {
		t.Fatal(err)
	}
	// Tamper the frontmatter identity: the file name says setup-ci-web,
	// the frontmatter says setup-ci-pipelines.
	tampered := bytes.ReplaceAll(data, []byte(`"id": "setup-ci-web"`), []byte(`"id": "setup-ci-pipelines"`))
	if err := os.WriteFile(draft, tampered, 0o644); err != nil {
		t.Fatal(err)
	}

	// The file-name identity is refused with the deterministic error.
	code, text, errText := runIn([]string{"publish", "feather/sto:setup-ci-web"})
	if code != 2 {
		t.Errorf("identity mismatch: exit = %d, want 2\nstdout: %s\nstderr: %s", code, text, errText)
	}
	if !strings.Contains(errText, "carries identity sto:setup-ci-pipelines; expected sto:setup-ci-web") ||
		!strings.Contains(errText, "rename the file or publish the draft's own identity") {
		t.Errorf("stderr must carry the identity-mismatch refusal, got %q", errText)
	}
	if _, err := os.Stat(draft); err != nil {
		t.Errorf("the refused publish must keep the draft: %v", err)
	}

	// Restore the frontmatter identity: the matching publish succeeds.
	if err := os.WriteFile(draft, data, 0o644); err != nil {
		t.Fatal(err)
	}
	code, text, errText = runIn([]string{"publish", "feather/sto:setup-ci-web"})
	if code != 0 {
		t.Fatalf("matching identity publish: exit = %d, want 0\nstdout: %s\nstderr: %s", code, text, errText)
	}
	if !strings.Contains(text, "feather/sto:setup-ci-web:1") {
		t.Errorf("publish output must show the form:\n%s", text)
	}
}

// --- test #6: registered-but-never-synced repo ------------------------

// TestNewUnsyncedRepoHint: inside a registered repository whose
// namespace was never resolved (repos.namespace "" — never synced), an
// unqualified target is refused with the deterministic hint. The state
// is the legacy-adoption shape (ADR-018 migration): a legacy-shaped
// registration (project/name = basename) whose tree carries eka.yaml
// with the same identity — the identity lookup hits, the namespace is
// still empty until the first sync.
func TestNewUnsyncedRepoHint(t *testing.T) {
	// Register WITHOUT setting the namespace (no SetRepoNamespace).
	t.Setenv("EKA_HOME", t.TempDir())
	w, err := workspace.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.Close() })
	repoDir := t.TempDir()
	if _, _, _, err := w.RegisterRepo(repoDir, ""); err != nil {
		t.Fatal(err)
	}
	// The tree carries eka.yaml with the same identity as the legacy
	// registration, but the repository was never synced.
	writeEkaYAML(t, repoDir, filepath.Base(repoDir), filepath.Base(repoDir), "feather")
	t.Chdir(repoDir)

	code, _, errText := runIn([]string{"new", "sto:x"})
	if code != 1 {
		t.Errorf("unqualified target in an unsynced repo: exit = %d, want 1", code)
	}
	if !strings.Contains(errText, "cannot resolve a namespace here") {
		t.Errorf("stderr must carry the spec's hint, got %q", errText)
	}
	// A qualified target still works (the namespace comes from the
	// target).
	if code, _, errText := runIn([]string{"new", "feather/sto:q"}); code != 0 {
		t.Errorf("qualified target: exit = %d, want 0\n%s", code, errText)
	}
}

// --- ADR-018: repository context gate -----------------------------------

// TestAuthoringRefuseOutsideEKA (ADR-018): every mutating authoring
// command run outside an EKA repository (no eka.yaml) is refused with
// the pinned gate sentence wrapped in the command's house wording and
// its refusal exit class: new 1, publish 1 (the refusal style), edit 2
// and discard 2 (the usage/internal class).
func TestAuthoringRefuseOutsideEKA(t *testing.T) {
	dir := t.TempDir()
	chdirInto(t, dir)
	cases := []struct {
		args []string
		code int
		word string
	}{
		// new renders through the refuse() helper: "eka: new: refused: …".
		{[]string{"new", "sto:x"}, 1, "new: refused"},
		{[]string{"publish", "sto:x"}, 1, "publish refused"},
		{[]string{"edit", "sto:x"}, 2, "edit refused"},
		{[]string{"discard", "sto:x", "--force"}, 2, "discard refused"},
	}
	for _, tc := range cases {
		code, _, errText := runIn(tc.args)
		if code != tc.code {
			t.Errorf("%v: exit = %d, want %d\nstderr: %s", tc.args, code, tc.code, errText)
		}
		if !strings.Contains(errText, tc.word) ||
			!strings.Contains(errText, "is not an EKA repository (no eka.yaml)") ||
			!strings.Contains(errText, "run 'eka init' first") {
			t.Errorf("%v: stderr must carry the pinned ADR-018 refusal, got %q", tc.args, errText)
		}
	}
}

// TestNewPublishUnknownFlags (ADR-017 D6): the removed --project and
// --namespace flags are unknown to `eka new` / `eka publish` — a cobra
// usage error (exit 2), never a silent ignore.
func TestNewPublishUnknownFlags(t *testing.T) {
	authoringEnv(t, "atrium-api")
	for _, args := range [][]string{
		{"new", "sto:x", "--project", "foo"},
		{"new", "sto:x", "--namespace", "foo"},
		{"publish", "sto:x", "--project", "foo"},
	} {
		code, _, errText := runIn(args)
		if code != 2 {
			t.Errorf("%v: exit = %d, want 2 (usage)", args, code)
		}
		if !strings.Contains(errText, "unknown flag") {
			t.Errorf("%v: stderr must report the unknown flag, got %q", args, errText)
		}
	}
}

// --- test #7 / M5: --edit non-TTY leaves no file -----------------------

// TestNewEditNonTTYLeavesNoFile (M5): the --edit TTY check runs BEFORE
// the scaffold — a refused run leaves no draft file behind.
func TestNewEditNonTTYLeavesNoFile(t *testing.T) {
	w, _ := authoringEnv(t, "feather")
	project := projectOf(t, w, mustAbs(t, "."))
	code, _, errText := runIn([]string{"new", "sto:x", "--edit"})
	if code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
	if !strings.Contains(errText, "--edit requires a terminal") {
		t.Errorf("stderr must explain the TTY requirement, got %q", errText)
	}
	if _, err := os.Stat(draftFile(t, w, project, "sto", "x")); !os.IsNotExist(err) {
		t.Errorf("a refused --edit run must not leave a draft file: %v", err)
	}
}

// --- m4b: draft frontmatter instance-version (CLI) ---------------------

// TestPublishFrontmatterInstanceVersionCLI (m4b): the draft
// frontmatter's instance-version is honored at publish; a forward-only
// violation is refused and the draft is kept.
func TestPublishFrontmatterInstanceVersionCLI(t *testing.T) {
	w, _ := authoringEnv(t, "feather")
	project := projectOf(t, w, mustAbs(t, "."))
	writeDraftFile := func(id, version string) {
		t.Helper()
		dir := filepath.Join(w.Dir, "drafts", project)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		doc := `{
  "namespace": "feather",
  "type": "sto",
  "id": "` + id + `"`
		if version != "" {
			doc += `,
  "instanceVersion": ` + version
		}
		doc += `,
  "revision": 1,
  "state": {
    "executionState": "planned",
    "existenceState": "active"
  },
  "changeLog": [
    {"date": "2026-08-07", "domain": "executionState", "from": "-", "to": "planned", "by": "Engineering"},
    {"date": "2026-08-07", "domain": "existenceState", "from": "-", "to": "active", "by": "Engineering"}
  ],
  "content": {
    "description": "d",
    "acceptanceCriteria": "ac"
  }
}`
		if err := os.WriteFile(draftFile(t, w, project, "sto", id), []byte(doc), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Frontmatter version 3: honored.
	writeDraftFile("fv", "3")
	code, text, errText := runIn([]string{"publish", "feather/sto:fv"})
	if code != 0 || !strings.Contains(text, "feather/sto:fv:3") {
		t.Fatalf("frontmatter version: exit = %d\n%s\n%s", code, text, errText)
	}
	// A second draft of the same line at version 3 (the highest):
	// forward-only violation, exit 2, draft kept.
	writeDraftFile("fv", "3")
	code, _, errText = runIn([]string{"publish", "feather/sto:fv"})
	if code != 2 {
		t.Errorf("forward-only violation: exit = %d, want 2", code)
	}
	if !strings.Contains(errText, "must exceed the line's highest") {
		t.Errorf("stderr must explain the forward-only rule, got %q", errText)
	}
	if _, err := os.Stat(draftFile(t, w, project, "sto", "fv")); err != nil {
		t.Errorf("the blocked draft must be kept: %v", err)
	}
	// No frontmatter version: auto-assign max+1 = 4.
	writeDraftFile("fv", "")
	code, text, _ = runIn([]string{"publish", "feather/sto:fv"})
	if code != 0 || !strings.Contains(text, "feather/sto:fv:4") {
		t.Errorf("auto-assign after a frontmatter version: exit = %d\n%s", code, text)
	}
}

// --- M4: CKO-level draft list marker -----------------------------------

// TestDraftListCKOInvalidMarker (M4): the per-draft marker runs the
// CKO-level validation — a draft with a structurally valid frontmatter
// but missing required sections is marked with the rule-error count.
func TestDraftListCKOInvalidMarker(t *testing.T) {
	w, _ := authoringEnv(t, "atrium-api")
	body := stoBody(t)
	runIn([]string{"new", "sto:alpha", "--content-file", body})
	// A structurally valid draft whose content lacks the required
	// keys: CKO-level invalid (R9, 2 errors). The template always
	// scaffolds the required keys, so the draft is hand-written.
	project := projectOf(t, w, mustAbs(t, "."))
	dir := filepath.Join(w.Dir, "drafts", project)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	missing := `{
  "namespace": "atrium-api",
  "type": "sto",
  "id": "missing",
  "revision": 1,
  "state": {
    "executionState": "planned",
    "existenceState": "active"
  },
  "changeLog": [
    {"date": "2026-08-07", "domain": "executionState", "from": "-", "to": "planned", "by": "Engineering"},
    {"date": "2026-08-07", "domain": "existenceState", "from": "-", "to": "active", "by": "Engineering"}
  ],
  "content": {
    "foo": "bar"
  }
}`
	if err := os.WriteFile(filepath.Join(dir, "sto-missing.json"), []byte(missing), 0o644); err != nil {
		t.Fatal(err)
	}

	code, text, errText := runIn([]string{"draft", "list"})
	if code != 0 {
		t.Fatalf("exit = %d\n%s\n%s", code, text, errText)
	}
	if !strings.Contains(text, "sto:alpha") {
		t.Errorf("the valid draft must be listed unmarked:\n%s", text)
	}
	if !strings.Contains(text, "sto:missing     (atrium-api)     updated") {
		t.Errorf("the invalid draft must be listed:\n%s", text)
	}
	if !strings.Contains(text, "invalid — 2 errors") {
		t.Errorf("the CKO-level marker must count the rule errors:\n%s", text)
	}
	// The valid draft must not carry a marker.
	lines := strings.Split(text, "\n")
	for _, line := range lines {
		if strings.Contains(line, "sto:alpha") && strings.Contains(line, "invalid") {
			t.Errorf("a valid draft must not be marked:\n%s", line)
		}
	}
}

// --- helpers -----------------------------------------------------------

// mustWorkspace returns the workspace of the current test (the
// authoringEnv helper's EKA_HOME).
func mustWorkspace(t *testing.T) *workspace.Workspace {
	t.Helper()
	w, err := workspace.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	return w
}

// TestPublishCtrBornPlannedCLI: publishing a container renders NO Plan
// Locked line — the container is born planned and the depends-on plan
// stays approved (the lock moved from publish to activation: Option B).
func TestPublishCtrBornPlannedCLI(t *testing.T) {
	w, _ := authoringEnv(t, "atrium-api")
	defer w.Close()

	if code, _, errText := runIn([]string{"new", "plan:roadmap-v1", "--dimension", "planning"}); code != 0 {
		t.Fatalf("new plan: exit = %d\nstderr: %s", code, errText)
	}
	if code, _, errText := runIn([]string{"publish", "plan:roadmap-v1"}); code != 0 {
		t.Fatalf("publish plan: exit = %d\nstderr: %s", code, errText)
	}
	if code, _, errText := runIn([]string{"transition", "plan:roadmap-v1", "approved", "--by", "test-agent"}); code != 0 {
		t.Fatalf("approve plan: exit = %d\nstderr: %s", code, errText)
	}
	if code, _, errText := runIn([]string{"new", "ctr:wave-7", "--depends-on", "plan:roadmap-v1"}); code != 0 {
		t.Fatalf("new ctr: exit = %d\nstderr: %s", code, errText)
	}
	code, text, errText := runIn([]string{"publish", "ctr:wave-7"})
	if code != 0 {
		t.Fatalf("publish ctr: exit = %d\nstdout: %s\nstderr: %s", code, text, errText)
	}
	if !strings.Contains(text, "atrium-api/ctr:wave-7:1") {
		t.Errorf("publish output missing the container form:\n%s", text)
	}
	if strings.Contains(text, "Plan Locked") {
		t.Errorf("publish output must not render a Plan Locked line (the lock moved to activation):\n%s", text)
	}
	// The container is born planned; the plan stays approved.
	units, err := w.Store().UnitsByLine("atrium-api", "ctr", "wave-7")
	if err != nil || len(units) == 0 {
		t.Fatalf("container line = %d units (err %v)", len(units), err)
	}
	ctr := units[0]
	for _, cand := range units {
		if cand.Identity.InstanceVersion > ctr.Identity.InstanceVersion {
			ctr = cand
		}
	}
	if ctr.StateVector.ContainerState != "planned" {
		t.Errorf("container-state = %q, want planned (born planned)", ctr.StateVector.ContainerState)
	}
	planUnits, err := w.Store().UnitsByLine("atrium-api", "plan", "roadmap-v1")
	if err != nil || len(planUnits) == 0 {
		t.Fatalf("plan line = %d units (err %v)", len(planUnits), err)
	}
	plan := planUnits[0]
	for _, cand := range planUnits {
		if cand.Identity.InstanceVersion > plan.Identity.InstanceVersion {
			plan = cand
		}
	}
	if plan.StateVector.PlanningState != "approved" {
		t.Errorf("planning-state = %q, want approved (publish no longer locks)", plan.StateVector.PlanningState)
	}
}

// TestNewContainerRequiresPlanCLI: `eka new ctr:...` without
// --depends-on is refused at scaffold time (exit 1), mirroring the
// ticket guard.
func TestNewContainerRequiresPlanCLI(t *testing.T) {
	authoringEnv(t, "atrium-api")
	code, _, errText := runIn([]string{"new", "ctr:wave-7"})
	if code != 1 {
		t.Fatalf("new ctr without --depends-on: exit = %d, want 1\nstderr: %s", code, errText)
	}
	if !strings.Contains(errText, "container drafts require --depends-on with a plan- reference") {
		t.Errorf("stderr = %q, want the scaffold refusal", errText)
	}
}
