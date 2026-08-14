package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/maleolabs/eka-core/workspace"
)

// This file tests the batch forms of the authoring commands: `eka new
// --file <batch.json>` (multi-scaffold, all-or-nothing rollback, schema
// validation) and `eka publish --all` / `eka publish --pending`
// (topological order over the pending draft graph, cycle and
// dangling-reference refusal, per-draft atomic publish).

// writeBatchFile writes a batch JSON document to a temp file and
// returns its path.
func writeBatchFile(t *testing.T, targets []map[string]any) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "batch.json")
	data, err := json.Marshal(map[string]any{"drafts": targets})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// planningUnitBatchJSON is the acceptance shape of the batch feature: a
// planning unit — scope, plan, work items, container and tickets — with
// the work-item text's edges (sto depends-on ctr, tkt derives-from
// ctr+sto, plan derives-from scp).
func planningUnitBatchJSON() []map[string]any {
	rels := func(m map[string][]string) map[string]any {
		out := map[string]any{}
		for k, v := range m {
			out[k] = v
		}
		return out
	}
	return []map[string]any{
		{"type": "scp", "id": "product-v1", "dimension": "planning"},
		{"type": "plan", "id": "roadmap-v2", "dimension": "planning",
			"relationships": rels(map[string][]string{"derivesFrom": {"scp:product-v1"}})},
		{"type": "ctr", "id": "wave-7",
			"relationships": rels(map[string][]string{"dependsOn": {"plan:roadmap-v2"}})},
		{"type": "sto", "id": "item-1",
			"relationships": rels(map[string][]string{"dependsOn": {"ctr:wave-7"}})},
		{"type": "sto", "id": "item-2",
			"relationships": rels(map[string][]string{"dependsOn": {"ctr:wave-7"}})},
		{"type": "tkt", "id": "ticket-1",
			"relationships": rels(map[string][]string{"derivesFrom": {"ctr:wave-7", "sto:item-1"}})},
		{"type": "tkt", "id": "ticket-2",
			"relationships": rels(map[string][]string{"derivesFrom": {"ctr:wave-7", "sto:item-2"}})},
	}
}

// --- eka new --file ----------------------------------------------------

// TestNewBatchHelpDocumentsBatchForms: the help of `eka new` and `eka
// publish` documents the batch forms (--file, --all, --pending).
func TestNewBatchHelpDocumentsBatchForms(t *testing.T) {
	code, text, _ := runIn([]string{"new", "-h"})
	if code != 0 {
		t.Fatalf("new -h: exit = %d, want 0", code)
	}
	for _, want := range []string{"--file <batch.json>", `"drafts"`, "dependsOn", "all-or-nothing"} {
		if !strings.Contains(text, want) {
			t.Errorf("new -h missing %q", want)
		}
	}
	code, text, _ = runIn([]string{"publish", "-h"})
	if code != 0 {
		t.Fatalf("publish -h: exit = %d, want 0", code)
	}
	for _, want := range []string{"--all", "--pending", "topological", "cycle"} {
		if !strings.Contains(text, want) {
			t.Errorf("publish -h missing %q", want)
		}
	}
}

// TestNewBatchScaffoldsSet: one `eka new --file` invocation scaffolds
// the whole planning-unit set with its relationships.
func TestNewBatchScaffoldsSet(t *testing.T) {
	w, _ := authoringEnv(t, "atrium-api")
	path := writeBatchFile(t, planningUnitBatchJSON())
	code, text, errText := runIn([]string{"new", "--file", path})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", code, text, errText)
	}
	project := projectOf(t, w, mustAbs(t, "."))
	for _, id := range []string{"scp-product-v1", "plan-roadmap-v2", "ctr-wave-7", "sto-item-1", "sto-item-2", "tkt-ticket-1", "tkt-ticket-2"} {
		p := draftFile(t, w, project, strings.SplitN(id, "-", 2)[0], strings.SplitN(id, "-", 2)[1])
		if _, err := os.Stat(p); err != nil {
			t.Errorf("draft %s missing: %v", p, err)
		}
	}
	if !strings.Contains(text, "Batch") || !strings.Contains(text, "7 drafts") {
		t.Errorf("output missing the batch summary:\n%s", text)
	}
	// The tkt- target carries its derivesFrom frontmatter.
	data, err := os.ReadFile(draftFile(t, w, project, "tkt", "ticket-1"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"derivesFrom"`) || !strings.Contains(string(data), "ctr:wave-7") {
		t.Errorf("tkt draft missing derivesFrom ctr:wave-7:\n%s", data)
	}
}

// TestNewBatchContentMerged: the per-target content object is merged
// into the draft (the batch form of --content-file).
func TestNewBatchContentMerged(t *testing.T) {
	w, _ := authoringEnv(t, "atrium-api")
	path := writeBatchFile(t, []map[string]any{
		{"type": "sto", "id": "filled",
			"content": map[string]any{"description": "batch body", "acceptanceCriteria": "ac"}},
	})
	code, text, errText := runIn([]string{"new", "--file", path})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", code, text, errText)
	}
	project := projectOf(t, w, mustAbs(t, "."))
	data, err := os.ReadFile(draftFile(t, w, project, "sto", "filled"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"description": "batch body"`) {
		t.Errorf("draft content missing the batch content:\n%s", data)
	}
}

// TestNewBatchRollbackOnFailure: when one target cannot be scaffolded
// (a tkt- without a container reference), the run refuses and removes
// the drafts it created — no partial set is left behind.
func TestNewBatchRollbackOnFailure(t *testing.T) {
	w, _ := authoringEnv(t, "atrium-api")
	path := writeBatchFile(t, []map[string]any{
		{"type": "sto", "id": "ok-1"},
		{"type": "sto", "id": "ok-2"},
		{"type": "tkt", "id": "orphan"},
	})
	code, text, errText := runIn([]string{"new", "--file", path})
	if code != 1 {
		t.Fatalf("exit = %d, want 1\nstdout: %s\nstderr: %s", code, text, errText)
	}
	for _, want := range []string{"batch draft 3 of 3", "tkt:orphan", "were removed"} {
		if !strings.Contains(errText, want) {
			t.Errorf("stderr missing %q:\n%s", want, errText)
		}
	}
	project := projectOf(t, w, mustAbs(t, "."))
	for _, id := range []string{"ok-1", "ok-2"} {
		p := draftFile(t, w, project, "sto", id)
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("draft %s must be rolled back, stat err = %v", p, err)
		}
	}
}

// TestNewBatchMalformedSchema: schema violations are deterministic
// refusals (exit 1) before anything is scaffolded.
func TestNewBatchMalformedSchema(t *testing.T) {
	writeRaw := func(t *testing.T, doc string) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "batch.json")
		if err := os.WriteFile(path, []byte(doc), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	cases := map[string]struct {
		doc string
	}{
		"unknown relationship key": {
			doc: `{"drafts": [{"type": "sto", "id": "a", "relationships": {"depends": ["sto:b"]}}]}`,
		},
		"duplicate identity": {
			doc: `{"drafts": [{"type": "sto", "id": "dup"}, {"type": "sto", "id": "dup"}]}`,
		},
		"unknown type": {
			doc: `{"drafts": [{"type": "bogus", "id": "x"}]}`,
		},
		"empty id": {
			doc: `{"drafts": [{"type": "sto", "id": ""}]}`,
		},
		"unknown top-level key": {
			doc: `{"drafts": [{"type": "sto", "id": "a"}], "extra": true}`,
		},
		"non-object content": {
			doc: `{"drafts": [{"type": "sto", "id": "a", "content": ["not", "an", "object"]}]}`,
		},
		"trailing data": {
			doc: `{"drafts": [{"type": "sto", "id": "a"}]} {"drafts": []}`,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			authoringEnv(t, "atrium-api")
			path := writeRaw(t, tc.doc)
			code, _, errText := runIn([]string{"new", "--file", path})
			if code != 1 {
				t.Fatalf("exit = %d, want 1\nstderr: %s", code, errText)
			}
			if !strings.HasPrefix(errText, "eka: new:") {
				t.Errorf("stderr must be the deterministic refusal, got %q", errText)
			}
		})
	}
}

// TestNewBatchEmptyDraftsRefused: an empty "drafts" array is refused.
func TestNewBatchEmptyDraftsRefused(t *testing.T) {
	authoringEnv(t, "atrium-api")
	path := writeBatchFile(t, []map[string]any{})
	code, _, errText := runIn([]string{"new", "--file", path})
	if code != 1 {
		t.Fatalf("exit = %d, want 1\nstderr: %s", code, errText)
	}
	if !strings.Contains(errText, "no drafts") {
		t.Errorf("stderr missing the empty-batch refusal:\n%s", errText)
	}
}

// TestNewBatchUsageErrors: mixing --file with a positional target, with
// --edit, or with any single-target flag (--dimension, --phase, a
// relationship flag, --content-file) is a usage error (exit 2) — the
// batch path refuses instead of silently dropping them. --by/--by-kind
// stay legal (the batch-wide change-log authority).
func TestNewBatchUsageErrors(t *testing.T) {
	authoringEnv(t, "atrium-api")
	path := writeBatchFile(t, []map[string]any{{"type": "sto", "id": "a"}})
	code, _, errText := runIn([]string{"new", "sto:target", "--file", path})
	if code != 2 {
		t.Errorf("target + --file: exit = %d, want 2\nstderr: %s", code, errText)
	}
	code, _, errText = runIn([]string{"new", "--file", path, "--edit"})
	if code != 2 {
		t.Errorf("--file + --edit: exit = %d, want 2\nstderr: %s", code, errText)
	}
	for _, args := range [][]string{
		{"new", "--file", path, "--dimension", "planning"},
		{"new", "--file", path, "--phase", "mvp"},
		{"new", "--file", path, "--depends-on", "ctr:x"},
		{"new", "--file", path, "--derives-from", "sto:y"},
		{"new", "--file", path, "--content-file", "body.json"},
	} {
		code, _, errText = runIn(args)
		if code != 2 {
			t.Errorf("args %v: exit = %d, want 2 (usage error, not a silent drop)\nstderr: %s", args, code, errText)
		}
		if !strings.Contains(errText, "single-target flag") {
			t.Errorf("args %v: stderr missing the single-target-flag refusal:\n%s", args, errText)
		}
	}
	// --by/--by-kind are batch-meaningful and stay legal.
	code, _, errText = runIn([]string{"new", "--file", path, "--by", "agent-x", "--by-kind", "agent"})
	if code != 0 {
		t.Errorf("--file + --by: exit = %d, want 0\nstderr: %s", code, errText)
	}
}

// --- eka publish --all / --pending ------------------------------------

// TestPublishAllPlanningUnit: the planning unit created by one `eka new
// --file` is published by one `eka publish --all` — every draft becomes
// an immutable object, referenced drafts first, and no draft file
// remains.
func TestPublishAllPlanningUnit(t *testing.T) {
	w, _ := authoringEnv(t, "atrium-api")
	path := writeBatchFile(t, planningUnitBatchJSON())
	if code, _, errText := runIn([]string{"new", "--file", path}); code != 0 {
		t.Fatalf("new --file: exit = %d\nstderr: %s", code, errText)
	}

	code, text, errText := runIn([]string{"publish", "--all"})
	if code != 0 {
		t.Fatalf("publish --all: exit = %d, want 0\nstdout: %s\nstderr: %s", code, text, errText)
	}
	// The topological order is deterministic and referenced-first: the
	// ticket rows appear after their container's row.
	ctrIdx := strings.Index(text, "ctr:wave-7 -> atrium-api/ctr:wave-7:1")
	tktIdx := strings.Index(text, "tkt:ticket-1 -> atrium-api/tkt:ticket-1:1")
	if ctrIdx < 0 || tktIdx < 0 || tktIdx < ctrIdx {
		t.Errorf("publish order must be referenced-first (ctr before its tkt):\n%s", text)
	}
	if !strings.Contains(text, "Published") || !strings.Contains(text, "7") {
		t.Errorf("output missing the summary:\n%s", text)
	}
	// Every draft file is gone: the single-use tickets were consumed.
	drafts, err := runtimeDrafts(t, w)
	if err != nil {
		t.Fatal(err)
	}
	if len(drafts) != 0 {
		t.Errorf("%d drafts still pending after publish --all", len(drafts))
	}
}

// runtimeDrafts lists the workspace drafts of every project (test
// helper mirroring Authoring.Drafts through the CLI's runtime).
func runtimeDrafts(t *testing.T, w *workspace.Workspace) ([]string, error) {
	t.Helper()
	root := filepath.Join(w.Dir, "drafts")
	var out []string
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		out = append(out, path)
		return nil
	})
	return out, nil
}

// TestPublishPendingSynonym: --pending is a synonym of --all (same
// output for the same backlog).
func TestPublishPendingSynonym(t *testing.T) {
	w, _ := authoringEnv(t, "atrium-api")
	path := writeBatchFile(t, []map[string]any{
		{"type": "sto", "id": "only"},
	})
	if code, _, errText := runIn([]string{"new", "--file", path}); code != 0 {
		t.Fatalf("new --file: exit = %d\nstderr: %s", code, errText)
	}
	_ = w
	code, text, errText := runIn([]string{"publish", "--pending"})
	if code != 0 {
		t.Fatalf("publish --pending: exit = %d, want 0\nstdout: %s\nstderr: %s", code, text, errText)
	}
	if !strings.Contains(text, "sto:only ->") {
		t.Errorf("--pending must publish the backlog like --all:\n%s", text)
	}
}

// TestPublishAllEmptyBacklog: no pending drafts is informational.
func TestPublishAllEmptyBacklog(t *testing.T) {
	authoringEnv(t, "atrium-api")
	code, text, errText := runIn([]string{"publish", "--all"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", code, text, errText)
	}
	if !strings.Contains(text, "Published") || !strings.Contains(text, "0") {
		t.Errorf("empty backlog must render the informational summary:\n%s", text)
	}
}

// TestPublishAllCycleRefused: a dependency cycle among pending drafts
// publishes nothing and refuses deterministically, naming the cycle.
func TestPublishAllCycleRefused(t *testing.T) {
	authoringEnv(t, "atrium-api")
	path := writeBatchFile(t, []map[string]any{
		{"type": "sto", "id": "a", "relationships": map[string]any{"dependsOn": []any{"sto:b"}}},
		{"type": "sto", "id": "b", "relationships": map[string]any{"dependsOn": []any{"sto:a"}}},
	})
	if code, _, errText := runIn([]string{"new", "--file", path}); code != 0 {
		t.Fatalf("new --file: exit = %d\nstderr: %s", code, errText)
	}
	code, text, errText := runIn([]string{"publish", "--all"})
	if code != 1 {
		t.Fatalf("exit = %d, want 1\nstdout: %s\nstderr: %s", code, text, errText)
	}
	for _, want := range []string{"cycle among pending drafts", "sto:a, sto:b"} {
		if !strings.Contains(errText, want) {
			t.Errorf("stderr missing %q:\n%s", want, errText)
		}
	}
	// Both drafts stay pending (nothing was published).
	if strings.Contains(text, "->") {
		t.Errorf("a refused run must publish nothing:\n%s", text)
	}
}

// TestPublishAllUnresolvedRefused: a draft referencing a target that is
// neither pending nor published refuses deterministically, naming the
// draft and the target.
func TestPublishAllUnresolvedRefused(t *testing.T) {
	authoringEnv(t, "atrium-api")
	path := writeBatchFile(t, []map[string]any{
		{"type": "sto", "id": "dangling", "relationships": map[string]any{"dependsOn": []any{"ctr:ghost"}}},
	})
	if code, _, errText := runIn([]string{"new", "--file", path}); code != 0 {
		t.Fatalf("new --file: exit = %d\nstderr: %s", code, errText)
	}
	code, text, errText := runIn([]string{"publish", "--all"})
	if code != 1 {
		t.Fatalf("exit = %d, want 1\nstdout: %s\nstderr: %s", code, text, errText)
	}
	for _, want := range []string{"sto:dangling", "ctr:ghost", "neither a pending draft nor a published object"} {
		if !strings.Contains(errText, want) {
			t.Errorf("stderr missing %q:\n%s", want, errText)
		}
	}
	if strings.Contains(text, "->") {
		t.Errorf("a refused run must publish nothing:\n%s", text)
	}
}

// TestPublishAllPartialFailure: a draft failing CKO-level validation
// stops the run — the drafts ordered before it are published, the
// failing draft stays pending.
func TestPublishAllPartialFailure(t *testing.T) {
	w, _ := authoringEnv(t, "atrium-api")
	path := writeBatchFile(t, []map[string]any{
		{"type": "ctr", "id": "wave-7", "relationships": map[string]any{"dependsOn": []any{"plan:roadmap-v2"}}},
		{"type": "plan", "id": "roadmap-v2", "dimension": "planning"},
		{"type": "tkt", "id": "broken",
			"relationships": map[string]any{"derivesFrom": []any{"ctr:wave-7"}},
			"content":       map[string]any{"commands": "not a projection header"}},
	})
	if code, _, errText := runIn([]string{"new", "--file", path}); code != 0 {
		t.Fatalf("new --file: exit = %d\nstderr: %s", code, errText)
	}
	project := projectOf(t, w, mustAbs(t, "."))

	code, text, errText := runIn([]string{"publish", "--all"})
	if code != 1 {
		t.Fatalf("exit = %d, want 1\nstdout: %s\nstderr: %s", code, text, errText)
	}
	// The ctr and the plan were published before the failure.
	for _, want := range []string{"plan:roadmap-v2 ->", "ctr:wave-7 ->", "failed validation"} {
		if !strings.Contains(text, want) {
			t.Errorf("output missing %q:\n%s", want, text)
		}
	}
	if !strings.Contains(errText, "tkt:broken failed validation") {
		t.Errorf("stderr missing the failure verdict:\n%s", errText)
	}
	// The failing draft stays pending.
	if _, serr := os.Stat(draftFile(t, w, project, "tkt", "broken")); serr != nil {
		t.Errorf("the failing draft must stay pending: %v", serr)
	}
}

// TestPublishAllUsageErrors: a target with --all/--pending, and
// --all/--pending with --instance-version (a single-target flag), are
// usage errors (exit 2) — the batch path refuses instead of silently
// dropping the override.
func TestPublishAllUsageErrors(t *testing.T) {
	authoringEnv(t, "atrium-api")
	code, _, errText := runIn([]string{"publish", "sto:x", "--all"})
	if code != 2 {
		t.Errorf("target + --all: exit = %d, want 2\nstderr: %s", code, errText)
	}
	code, _, errText = runIn([]string{"publish", "sto:x", "--pending"})
	if code != 2 {
		t.Errorf("target + --pending: exit = %d, want 2\nstderr: %s", code, errText)
	}
	for _, args := range [][]string{
		{"publish", "--all", "--instance-version", "3"},
		{"publish", "--pending", "--instance-version", "3"},
	} {
		code, _, errText = runIn(args)
		if code != 2 {
			t.Errorf("args %v: exit = %d, want 2 (usage error, not a silent drop)\nstderr: %s", args, code, errText)
		}
		if !strings.Contains(errText, "single-target flag") {
			t.Errorf("args %v: stderr missing the single-target-flag refusal:\n%s", args, errText)
		}
	}
}

// TestPublishAllDraftNotFoundSummary: when a draft vanishes between the
// graph build and its turn (here: a draft whose file name no longer
// matches its frontmatter identity), the mid-loop failure renders the
// same "Published N / Remaining M" summary as the validation paths.
func TestPublishAllDraftNotFoundSummary(t *testing.T) {
	w, _ := authoringEnv(t, "atrium-api")
	path := writeBatchFile(t, []map[string]any{
		{"type": "sto", "id": "ok"},
		{"type": "sto", "id": "renamed"},
	})
	if code, _, errText := runIn([]string{"new", "--file", path}); code != 0 {
		t.Fatalf("new --file: exit = %d\nstderr: %s", code, errText)
	}
	project := projectOf(t, w, mustAbs(t, "."))
	// Rename the second draft file without touching its frontmatter:
	// the graph keys it by frontmatter identity (sto:renamed), but the
	// publish lookup by that identity no longer finds a file.
	if err := os.Rename(draftFile(t, w, project, "sto", "renamed"), draftFile(t, w, project, "sto", "other")); err != nil {
		t.Fatal(err)
	}

	code, text, errText := runIn([]string{"publish", "--all"})
	if code != 1 {
		t.Fatalf("exit = %d, want 1\nstdout: %s\nstderr: %s", code, text, errText)
	}
	// The first draft published; the summary names both counts.
	if !strings.Contains(text, "sto:ok ->") {
		t.Errorf("the draft ordered before the failure must be published:\n%s", text)
	}
	if !strings.Contains(text, "sto:renamed") || !strings.Contains(text, "not found") {
		t.Errorf("the failing row must be marked not found:\n%s", text)
	}
	if !strings.Contains(text, "Published") || !strings.Contains(text, "1") || !strings.Contains(text, "Remaining") {
		t.Errorf("the failure summary must be rendered:\n%s", text)
	}
	if !strings.Contains(errText, "sto:renamed") {
		t.Errorf("stderr must name the failing draft:\n%s", errText)
	}
}
