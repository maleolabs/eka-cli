package cmd

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/maleolabs/eka-core/workspace"
)

// This file tests the `eka relate` command at CLI level: exit codes,
// the relationship-flag contract (comma-joined + repeated), the
// no-churn acceptance (adding an edge must NOT advance the instance
// version), the idempotent duplicate handling, the refusal paths and
// the pending-draft mutation.

// publishPair publishes two publishable sto- drafts (item-a, item-b)
// in the authoring environment and returns the workspace.
func publishPair(t *testing.T) *workspace.Workspace {
	t.Helper()
	w, _ := authoringEnv(t, "acme")
	body := stoBody(t)
	for _, id := range []string{"item-a", "item-b"} {
		if code, _, errText := runIn([]string{"new", "sto:" + id, "--content-file", body}); code != 0 {
			t.Fatalf("new sto:%s: exit = %d\nstderr: %s", id, code, errText)
		}
		if code, _, errText := runIn([]string{"publish", "sto:" + id}); code != 0 {
			t.Fatalf("publish sto:%s: exit = %d\nstderr: %s", id, code, errText)
		}
	}
	return w
}

// getDoc runs `eka get <target>` and decodes the machine document.
func getDoc(t *testing.T, target string) map[string]any {
	t.Helper()
	code, out, errText := runIn([]string{"get", target})
	if code != 0 {
		t.Fatalf("get %s: exit = %d\nstderr: %s", target, code, errText)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("get %s: invalid JSON: %v\n%s", target, err, out)
	}
	return doc
}

// payloadCount returns the store's payload archive size.
func payloadCount(t *testing.T, w *workspace.Workspace) int {
	t.Helper()
	n, err := w.Store().PayloadCount()
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// TestRelateHelpExitsZero: the command documents itself.
func TestRelateHelpExitsZero(t *testing.T) {
	code, text, _ := runIn([]string{"relate", "-h"})
	if code != 0 {
		t.Errorf("relate -h: exit = %d, want 0", code)
	}
	if !strings.Contains(text, "eka relate") {
		t.Errorf("relate -h: missing usage")
	}
}

// TestRelateAddsEdgeWithoutInstanceChurn is the acceptance test at CLI
// level: `eka relate sto:item-a --depends-on sto:item-b` adds the edge
// and `eka get` shows BOTH the edge AND the unchanged instance version
// (1) — the artifact line did not advance.
func TestRelateAddsEdgeWithoutInstanceChurn(t *testing.T) {
	w := publishPair(t)
	payloadsBefore := payloadCount(t, w)

	code, out, errText := runIn([]string{"relate", "sto:item-a", "--depends-on", "sto:item-b"})
	if code != 0 {
		t.Fatalf("relate: exit = %d\nstdout: %s\nstderr: %s", code, out, errText)
	}
	if !strings.Contains(out, "sto:item-a") || !strings.Contains(out, "depends-on sto:item-b") {
		t.Errorf("relate output missing the edge:\n%s", out)
	}
	if !strings.Contains(out, "Instance Version: 1") {
		t.Errorf("relate output must show the unchanged instance version 1:\n%s", out)
	}

	// The machine document: the edge is present AND the instance
	// version is still 1 — the acceptance.
	doc := getDoc(t, "acme/sto:item-a")
	ident, _ := doc["identity"].(map[string]any)
	if ident == nil {
		t.Fatalf("get document missing identity: %+v", doc)
	}
	if v, _ := ident["instanceVersion"].(float64); v != 1 {
		t.Errorf("identity.instanceVersion = %v, want 1 (no instance churn)", ident["instanceVersion"])
	}
	rels, _ := doc["relationships"].([]any)
	if len(rels) != 1 {
		t.Fatalf("relationships = %+v, want exactly the depends-on edge", rels)
	}
	rel, _ := rels[0].(map[string]any)
	if rel["type"] != "depends-on" || rel["target"] != "sto:item-b" {
		t.Errorf("relationship = %+v, want depends-on -> sto:item-b", rel)
	}

	// The store-level churn proof: exactly one new payload row (the
	// edge payload).
	if got := payloadCount(t, w); got != payloadsBefore+1 {
		t.Errorf("payloads = %d -> %d, want exactly +1", payloadsBefore, got)
	}
}

// TestRelateDuplicateEdgeIdempotent: relating an already-present edge
// writes nothing (the command reports it) and the artifact keeps
// exactly one edge.
func TestRelateDuplicateEdgeIdempotent(t *testing.T) {
	w := publishPair(t)
	if code, _, errText := runIn([]string{"relate", "sto:item-a", "--depends-on", "sto:item-b"}); code != 0 {
		t.Fatalf("first relate: exit = %d\nstderr: %s", code, errText)
	}
	payloadsAfterFirst := payloadCount(t, w)

	code, out, errText := runIn([]string{"relate", "sto:item-a", "--depends-on", "sto:item-b"})
	if code != 0 {
		t.Fatalf("duplicate relate: exit = %d\nstdout: %s\nstderr: %s", code, out, errText)
	}
	if !strings.Contains(out, "already present") {
		t.Errorf("duplicate relate output must report the no-op:\n%s", out)
	}
	if got := payloadCount(t, w); got != payloadsAfterFirst {
		t.Errorf("a duplicate relate must not write a payload, got %d -> %d", payloadsAfterFirst, got)
	}
	doc := getDoc(t, "acme/sto:item-a")
	rels, _ := doc["relationships"].([]any)
	if len(rels) != 1 {
		t.Errorf("relationships = %+v, want exactly one edge", rels)
	}
}

// TestRelateCommaAndRepeatedFlags: comma-joined values and repeated
// flags accumulate (the StringSlice contract of `eka new`) — no target
// is silently dropped.
func TestRelateCommaAndRepeatedFlags(t *testing.T) {
	publishPair(t)
	body := stoBody(t)
	for _, id := range []string{"item-c", "item-d", "item-e"} {
		if code, _, errText := runIn([]string{"new", "sto:" + id, "--content-file", body}); code != 0 {
			t.Fatalf("new sto:%s: exit = %d\nstderr: %s", id, code, errText)
		}
		if code, _, errText := runIn([]string{"publish", "sto:" + id}); code != 0 {
			t.Fatalf("publish sto:%s: exit = %d\nstderr: %s", id, code, errText)
		}
	}
	code, _, errText := runIn([]string{
		"relate", "sto:item-a",
		"--depends-on", "sto:item-c,sto:item-d",
		"--depends-on", "sto:item-e",
	})
	if code != 0 {
		t.Fatalf("relate: exit = %d\nstderr: %s", code, errText)
	}
	doc := getDoc(t, "acme/sto:item-a")
	rels, _ := doc["relationships"].([]any)
	if len(rels) != 3 {
		t.Fatalf("relationships = %+v, want the three comma-joined + repeated targets", rels)
	}
	// Canonical (type, target) order.
	want := []string{"sto:item-c", "sto:item-d", "sto:item-e"}
	for i, target := range want {
		rel, _ := rels[i].(map[string]any)
		if rel["type"] != "depends-on" || rel["target"] != target {
			t.Errorf("relationship %d = %+v, want depends-on -> %s", i, rel, target)
		}
	}
}

// TestRelateSelfReferenceRefused: a self-reference is refused (exit 1,
// the R5 mirror) and nothing is written.
func TestRelateSelfReferenceRefused(t *testing.T) {
	w := publishPair(t)
	payloadsBefore := payloadCount(t, w)
	code, out, errText := runIn([]string{"relate", "sto:item-a", "--depends-on", "sto:item-a"})
	if code != 1 {
		t.Fatalf("exit = %d, want 1\nstdout: %s\nstderr: %s", code, out, errText)
	}
	if !strings.Contains(errText, "self-reference") {
		t.Errorf("stderr = %q, want the self-reference refusal", errText)
	}
	if got := payloadCount(t, w); got != payloadsBefore {
		t.Errorf("a refused relate must not write, got %d -> %d", payloadsBefore, got)
	}
}

// TestRelateMissingTargetRefused: a line with no published instance
// and no pending draft is refused (exit 1).
func TestRelateMissingTargetRefused(t *testing.T) {
	authoringEnv(t, "acme")
	code, out, errText := runIn([]string{"relate", "sto:ghost", "--depends-on", "sto:item-a"})
	if code != 1 {
		t.Fatalf("exit = %d, want 1\nstdout: %s\nstderr: %s", code, out, errText)
	}
	if !strings.Contains(errText, "no published instance and no pending draft") {
		t.Errorf("stderr = %q, want the missing-artifact refusal", errText)
	}
}

// TestRelateOnDraftMutatesFile: a line with a pending draft (no
// published instance) gets its edge added to the draft file in place —
// no publish, no published instance created.
func TestRelateOnDraftMutatesFile(t *testing.T) {
	w := publishPair(t)
	body := stoBody(t)
	if code, _, errText := runIn([]string{"new", "sto:item-c", "--content-file", body}); code != 0 {
		t.Fatalf("new sto:item-c: exit = %d\nstderr: %s", code, errText)
	}
	project := projectOf(t, w, mustAbs(t, "."))
	path := draftFile(t, w, project, "sto", "item-c")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	code, out, errText := runIn([]string{"relate", "sto:item-c", "--depends-on", "sto:item-a"})
	if code != 0 {
		t.Fatalf("relate on a draft: exit = %d\nstdout: %s\nstderr: %s", code, out, errText)
	}
	if !strings.Contains(out, "draft") {
		t.Errorf("relate output must report the draft state:\n%s", out)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(after), `"dependsOn": [`) ||
		!strings.Contains(string(after), `"sto:item-a"`) {
		t.Errorf("draft file missing the related edge:\n%s", after)
	}
	if string(before) == string(after) {
		t.Errorf("the draft file must have been rewritten")
	}
	// No published instance was created.
	if code, _, _ := runIn([]string{"get", "sto:item-c"}); code == 0 {
		t.Errorf("relate on a draft must not publish an instance")
	}
}

// TestRelateVersionedTargetRefused: a canonical published form is a
// usage error (exit 2) — relate addresses the line.
func TestRelateVersionedTargetRefused(t *testing.T) {
	authoringEnv(t, "acme")
	code, out, errText := runIn([]string{"relate", "sto:item-a:1", "--depends-on", "sto:item-b"})
	if code != 2 {
		t.Fatalf("exit = %d, want 2\nstdout: %s\nstderr: %s", code, out, errText)
	}
	if !strings.Contains(errText, "canonical published form") {
		t.Errorf("stderr = %q, want the published-form usage error", errText)
	}
}

// TestRelateNoFlagsUsageError: a relate without any relationship flag
// is a usage error (exit 2) — never a silent "unchanged" (the
// idempotent-duplicate case has a distinct message and exit 0).
func TestRelateNoFlagsUsageError(t *testing.T) {
	authoringEnv(t, "acme")
	code, out, errText := runIn([]string{"relate", "sto:item-a"})
	if code != 2 {
		t.Fatalf("exit = %d, want 2\nstdout: %s\nstderr: %s", code, out, errText)
	}
	if !strings.Contains(errText, "no relationship targets") {
		t.Errorf("stderr = %q, want the no-relationship-targets usage error", errText)
	}
}

// TestRelateQualifiedCrossNamespaceRefused: inside a repository context
// a qualified target whose namespace differs from the repository's is
// refused (exit 1) — cross-platform access is read-only, the same
// ownership gate the other authoring commands enforce.
func TestRelateQualifiedCrossNamespaceRefused(t *testing.T) {
	publishPair(t)
	code, out, errText := runIn([]string{"relate", "other/sto:item-a", "--depends-on", "sto:item-b"})
	if code != 1 {
		t.Fatalf("exit = %d, want 1\nstdout: %s\nstderr: %s", code, out, errText)
	}
	if !strings.Contains(errText, "cross-platform access is read-only") {
		t.Errorf("stderr = %q, want the cross-platform ownership refusal", errText)
	}
	// Nothing was written: the artifact stays edge-free.
	doc := getDoc(t, "acme/sto:item-a")
	if rels, _ := doc["relationships"].([]any); len(rels) != 0 {
		t.Errorf("relationships = %+v, want none (refused before write)", rels)
	}
}

// TestRelateDeterministicOutput: the same relate operation produces
// byte-identical output across runs.
func TestRelateDeterministicOutput(t *testing.T) {
	var outputs []string
	for i := 0; i < 2; i++ {
		publishPair(t)
		code, out, errText := runIn([]string{"relate", "sto:item-a", "--depends-on", "sto:item-b"})
		if code != 0 {
			t.Fatalf("relate run %d: exit = %d\nstderr: %s", i, code, errText)
		}
		outputs = append(outputs, out)
	}
	if outputs[0] != outputs[1] {
		t.Errorf("relate output is not deterministic:\n--- run 1 ---\n%s\n--- run 2 ---\n%s", outputs[0], outputs[1])
	}
}
