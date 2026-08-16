package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/maleolabs/eka-core/workspace"
)

// This file tests the 'Assigned Work' CLI slice (ADR-029 /
// req:team-collaboration §4.3): the assignment commands (`eka assign`,
// `eka reassign`, `eka unassign` — the assigned-to edge) and the
// advisory member-scoped board (`eka view board --member me|<mbr-id>`
// with its assignee tags, 'No assignee' bucket and machine keys).
//
// Tests drive the CLI end-to-end (runIn) against a seeded workspace
// and assert the exit-code contract (0/1/2), the locked command
// semantics, the no-churn mechanism and the deterministic output.

// mbrBody writes a publishable mbr- content object (both required
// section keys) and returns its path.
func mbrBody(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "body.json")
	if err := os.WriteFile(path, []byte(`{"purpose": "p", "content": "c"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// assignmentEnv sets up a repository (namespace acme) with two
// published work items (item-a, item-b) and two published member lines
// (alice, bob — each authored by its own identity so the --member me
// resolution can match exactly one line) and returns the workspace.
func assignmentEnv(t *testing.T) *workspace.Workspace {
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
	mbr := mbrBody(t)
	for _, m := range []string{"alice", "bob"} {
		gitIdentityEnv(t, m)
		if code, _, errText := runIn([]string{"new", "mbr:" + m, "--content-file", mbr}); code != 0 {
			t.Fatalf("new mbr:%s: exit = %d\nstderr: %s", m, code, errText)
		}
		if code, _, errText := runIn([]string{"publish", "mbr:" + m}); code != 0 {
			t.Fatalf("publish mbr:%s: exit = %d\nstderr: %s", m, code, errText)
		}
	}
	gitIdentityEnv(t, "test-agent")
	return w
}

// assignedTo reads the assigned-to relationship targets of one item
// line from `eka get` ("" when the edge is absent).
func assignedTo(t *testing.T, target string) []string {
	t.Helper()
	doc := getDoc(t, target)
	rels, _ := doc["relationships"].([]any)
	var out []string
	for _, r := range rels {
		rel, _ := r.(map[string]any)
		if rel["type"] == "assigned-to" {
			if tgt, ok := rel["target"].(string); ok {
				out = append(out, tgt)
			}
		}
	}
	return out
}

// TestAssignmentHelpExitsZero: every assignment command documents
// itself, the --to flag and the exit-code contract.
func TestAssignmentHelpExitsZero(t *testing.T) {
	for _, args := range [][]string{
		{"assign", "-h"}, {"reassign", "-h"}, {"unassign", "-h"},
	} {
		code, text, _ := runIn(args)
		if code != 0 {
			t.Errorf("args %v: exit = %d, want 0", args, code)
		}
		for _, want := range []string{"eka ", "Exit codes:"} {
			if !strings.Contains(text, want) {
				t.Errorf("args %v: help missing %q:\n%s", args, want, text)
			}
		}
	}
	code, text, _ := runIn([]string{"assign", "--help"})
	if code != 0 || !strings.Contains(text, "--to") || !strings.Contains(text, "mbr") {
		t.Errorf("assign --help must document --to <mbr> (exit %d):\n%s", code, text)
	}
}

// TestAssignAddsEdgeWithoutInstanceChurn: `eka assign sto:item-a --to
// mbr:alice` adds the assigned-to edge and `eka get` shows BOTH the
// edge AND the unchanged instance version (1) — the artifact line did
// not advance (the relate no-churn mechanism).
func TestAssignAddsEdgeWithoutInstanceChurn(t *testing.T) {
	w := assignmentEnv(t)
	payloadsBefore := payloadCount(t, w)

	code, out, errText := runIn([]string{"assign", "sto:item-a", "--to", "mbr:alice"})
	if code != 0 {
		t.Fatalf("assign: exit = %d\nstdout: %s\nstderr: %s", code, out, errText)
	}
	if !strings.Contains(out, "assigned to acme/mbr:alice") {
		t.Errorf("assign output missing the assignment:\n%s", out)
	}
	if !strings.Contains(out, "Instance Version: unchanged") {
		t.Errorf("assign output must prove the unchanged instance version:\n%s", out)
	}
	if got := assignedTo(t, "acme/sto:item-a"); len(got) != 1 || got[0] != "mbr:alice" {
		t.Errorf("assigned-to = %v, want [mbr:alice]", got)
	}
	doc := getDoc(t, "acme/sto:item-a")
	ident, _ := doc["identity"].(map[string]any)
	if v, _ := ident["instanceVersion"].(float64); v != 1 {
		t.Errorf("identity.instanceVersion = %v, want 1 (no instance churn)", ident["instanceVersion"])
	}
	if got := payloadCount(t, w); got != payloadsBefore+1 {
		t.Errorf("payloads = %d -> %d, want exactly +1", payloadsBefore, got)
	}
}

// TestAssignIdempotentSameTarget: assigning an already-assigned item
// to the SAME member is an idempotent no-op (exit 0, nothing written).
func TestAssignIdempotentSameTarget(t *testing.T) {
	w := assignmentEnv(t)
	if code, _, errText := runIn([]string{"assign", "sto:item-a", "--to", "mbr:alice"}); code != 0 {
		t.Fatalf("first assign: exit = %d\nstderr: %s", code, errText)
	}
	payloadsAfterFirst := payloadCount(t, w)

	code, out, errText := runIn([]string{"assign", "sto:item-a", "--to", "mbr:alice"})
	if code != 0 {
		t.Fatalf("duplicate assign: exit = %d\nstdout: %s\nstderr: %s", code, out, errText)
	}
	if !strings.Contains(out, "already assigned") || !strings.Contains(out, "nothing written") {
		t.Errorf("duplicate assign output must report the no-op:\n%s", out)
	}
	if got := payloadCount(t, w); got != payloadsAfterFirst {
		t.Errorf("an idempotent assign must not write a payload, got %d -> %d", payloadsAfterFirst, got)
	}
	if got := assignedTo(t, "acme/sto:item-a"); len(got) != 1 {
		t.Errorf("assigned-to = %v, want exactly one edge", got)
	}
}

// TestAssignDifferentTargetRefused: assigning an item that is already
// assigned to a DIFFERENT member is a deterministic refusal (exit 1,
// nothing written) — the single-assignee rule of ADR-029.
func TestAssignDifferentTargetRefused(t *testing.T) {
	w := assignmentEnv(t)
	if code, _, errText := runIn([]string{"assign", "sto:item-a", "--to", "mbr:alice"}); code != 0 {
		t.Fatalf("first assign: exit = %d\nstderr: %s", code, errText)
	}
	payloadsAfterFirst := payloadCount(t, w)

	code, out, errText := runIn([]string{"assign", "sto:item-a", "--to", "mbr:bob"})
	if code != 1 {
		t.Fatalf("conflicting assign: exit = %d, want 1\nstdout: %s\nstderr: %s", code, out, errText)
	}
	if !strings.Contains(errText, "already assigned to acme/mbr:alice") {
		t.Errorf("stderr = %q, want the already-assigned refusal", errText)
	}
	if !strings.Contains(errText, "use 'eka reassign'") {
		t.Errorf("stderr = %q, want the reassign hint", errText)
	}
	if got := payloadCount(t, w); got != payloadsAfterFirst {
		t.Errorf("a refused assign must not write, got %d -> %d", payloadsAfterFirst, got)
	}
	if got := assignedTo(t, "acme/sto:item-a"); len(got) != 1 || got[0] != "mbr:alice" {
		t.Errorf("assigned-to = %v, want the unchanged [mbr:alice]", got)
	}
}

// TestAssignUnresolvableMemberRefusedWithList: an unresolvable member
// id refuses deterministically WITH the known member lines listed.
func TestAssignUnresolvableMemberRefusedWithList(t *testing.T) {
	assignmentEnv(t)
	code, out, errText := runIn([]string{"assign", "sto:item-a", "--to", "mbr:ghost"})
	if code != 1 {
		t.Fatalf("assign --to ghost: exit = %d, want 1\nstdout: %s\nstderr: %s", code, out, errText)
	}
	if !strings.Contains(errText, "member acme/mbr:ghost does not resolve") {
		t.Errorf("stderr = %q, want the does-not-resolve refusal", errText)
	}
	for _, want := range []string{"acme/mbr:alice", "acme/mbr:bob"} {
		if !strings.Contains(errText, want) {
			t.Errorf("stderr must list the available member %q:\n%s", want, errText)
		}
	}
}

// TestAssignCrossRepositoryRefused: an assigned-to target originating
// outside the work item's repository is refused (exit 1) — the R13
// provenance mirror.
func TestAssignCrossRepositoryRefused(t *testing.T) {
	assignmentEnv(t)
	code, out, errText := runIn([]string{"assign", "sto:item-a", "--to", "other/mbr:alice"})
	if code != 1 {
		t.Fatalf("cross-repo assign: exit = %d, want 1\nstdout: %s\nstderr: %s", code, out, errText)
	}
	if !strings.Contains(errText, "cross-repository assignment is refused") {
		t.Errorf("stderr = %q, want the cross-repository refusal", errText)
	}
	if got := assignedTo(t, "acme/sto:item-a"); len(got) != 0 {
		t.Errorf("assigned-to = %v, want none (refused before write)", got)
	}
}

// TestAssignNonWorkItemRefused: assignment applies to work items only —
// a knowledge artifact is refused (exit 1).
func TestAssignNonWorkItemRefused(t *testing.T) {
	assignmentEnv(t)
	code, out, errText := runIn([]string{"assign", "adr:001", "--to", "mbr:alice"})
	if code != 1 {
		t.Fatalf("assign adr: exit = %d, want 1\nstdout: %s\nstderr: %s", code, out, errText)
	}
	if !strings.Contains(errText, "not a work item") {
		t.Errorf("stderr = %q, want the work-item-only refusal", errText)
	}
}

// TestAssignMissingItemRefused: a line with no published instance and
// no pending draft is refused (exit 1, the relate mirror).
func TestAssignMissingItemRefused(t *testing.T) {
	assignmentEnv(t)
	code, out, errText := runIn([]string{"assign", "sto:ghost", "--to", "mbr:alice"})
	if code != 1 {
		t.Fatalf("assign sto:ghost: exit = %d, want 1\nstdout: %s\nstderr: %s", code, out, errText)
	}
	if !strings.Contains(errText, "no published instance and no pending draft") {
		t.Errorf("stderr = %q, want the missing-artifact refusal", errText)
	}
}

// TestAssignVersionedTargetUsage: a canonical published form is a
// usage error (exit 2) — assignment addresses the line.
func TestAssignVersionedTargetUsage(t *testing.T) {
	assignmentEnv(t)
	code, out, errText := runIn([]string{"assign", "sto:item-a:1", "--to", "mbr:alice"})
	if code != 2 {
		t.Fatalf("exit = %d, want 2\nstdout: %s\nstderr: %s", code, out, errText)
	}
	if !strings.Contains(errText, "canonical published form") {
		t.Errorf("stderr = %q, want the published-form usage error", errText)
	}
}

// TestAssignMissingToUsage: assign without --to is a usage error
// (exit 2).
func TestAssignMissingToUsage(t *testing.T) {
	assignmentEnv(t)
	for _, args := range [][]string{
		{"assign", "sto:item-a"},
		{"reassign", "sto:item-a"},
	} {
		code, out, errText := runIn(args)
		if code != 2 {
			t.Errorf("%v: exit = %d, want 2\nstdout: %s\nstderr: %s", args, code, out, errText)
		}
		if !strings.Contains(errText, "requires --to") {
			t.Errorf("%v: stderr = %q, want the missing --to usage error", args, errText)
		}
	}
}

// TestReassignMovesEdgeSingleOperation: reassign replaces the existing
// assigned-to edge with the new one in ONE operation — the machine
// document carries exactly the new assignee and the old edge is gone.
func TestReassignMovesEdgeSingleOperation(t *testing.T) {
	assignmentEnv(t)
	if code, _, errText := runIn([]string{"assign", "sto:item-a", "--to", "mbr:alice"}); code != 0 {
		t.Fatalf("assign: exit = %d\nstderr: %s", code, errText)
	}
	code, out, errText := runIn([]string{"reassign", "sto:item-a", "--to", "mbr:bob"})
	if code != 0 {
		t.Fatalf("reassign: exit = %d\nstdout: %s\nstderr: %s", code, out, errText)
	}
	if !strings.Contains(out, "reassigned to acme/mbr:bob") {
		t.Errorf("reassign output missing the move:\n%s", out)
	}
	if got := assignedTo(t, "acme/sto:item-a"); len(got) != 1 || got[0] != "mbr:bob" {
		t.Errorf("assigned-to = %v, want exactly [mbr:bob] (single-operation move)", got)
	}
	// The instance version stays 1: the move re-points the SAME
	// instance (no churn).
	doc := getDoc(t, "acme/sto:item-a")
	ident, _ := doc["identity"].(map[string]any)
	if v, _ := ident["instanceVersion"].(float64); v != 1 {
		t.Errorf("identity.instanceVersion = %v, want 1 (no instance churn)", ident["instanceVersion"])
	}
}

// TestReassignUnassignedRefused: reassign on an item without an
// assignee is a deterministic refusal (exit 1) — assign first.
func TestReassignUnassignedRefused(t *testing.T) {
	assignmentEnv(t)
	code, out, errText := runIn([]string{"reassign", "sto:item-a", "--to", "mbr:alice"})
	if code != 1 {
		t.Fatalf("reassign unassigned: exit = %d, want 1\nstdout: %s\nstderr: %s", code, out, errText)
	}
	if !strings.Contains(errText, "not assigned to any member") {
		t.Errorf("stderr = %q, want the not-assigned refusal", errText)
	}
	if !strings.Contains(errText, "use 'eka assign'") {
		t.Errorf("stderr = %q, want the assign hint", errText)
	}
}

// TestReassignSameTargetIdempotent: reassigning to the current
// assignee is an idempotent no-op (exit 0, nothing written).
func TestReassignSameTargetIdempotent(t *testing.T) {
	w := assignmentEnv(t)
	if code, _, errText := runIn([]string{"assign", "sto:item-a", "--to", "mbr:alice"}); code != 0 {
		t.Fatalf("assign: exit = %d\nstderr: %s", code, errText)
	}
	payloadsAfterAssign := payloadCount(t, w)
	code, out, errText := runIn([]string{"reassign", "sto:item-a", "--to", "mbr:alice"})
	if code != 0 {
		t.Fatalf("reassign same: exit = %d\nstdout: %s\nstderr: %s", code, out, errText)
	}
	if !strings.Contains(out, "already assigned") {
		t.Errorf("reassign same output must report the no-op:\n%s", out)
	}
	if got := payloadCount(t, w); got != payloadsAfterAssign {
		t.Errorf("an idempotent reassign must not write, got %d -> %d", payloadsAfterAssign, got)
	}
}

// TestUnassignRemovesEdge: unassign removes the assigned-to edge (exit
// 0, the same instance version — no churn).
func TestUnassignRemovesEdge(t *testing.T) {
	assignmentEnv(t)
	if code, _, errText := runIn([]string{"assign", "sto:item-a", "--to", "mbr:alice"}); code != 0 {
		t.Fatalf("assign: exit = %d\nstderr: %s", code, errText)
	}
	code, out, errText := runIn([]string{"unassign", "sto:item-a"})
	if code != 0 {
		t.Fatalf("unassign: exit = %d\nstdout: %s\nstderr: %s", code, out, errText)
	}
	if !strings.Contains(out, "assignment removed") {
		t.Errorf("unassign output missing the removal:\n%s", out)
	}
	if got := assignedTo(t, "acme/sto:item-a"); len(got) != 0 {
		t.Errorf("assigned-to = %v, want none after unassign", got)
	}
	doc := getDoc(t, "acme/sto:item-a")
	ident, _ := doc["identity"].(map[string]any)
	if v, _ := ident["instanceVersion"].(float64); v != 1 {
		t.Errorf("identity.instanceVersion = %v, want 1 (no instance churn)", ident["instanceVersion"])
	}
}

// TestUnassignNoOpWhenAbsent: unassign on an item without an assignee
// is a no-op (exit 0, NOTHING written — the payload archive stays).
func TestUnassignNoOpWhenAbsent(t *testing.T) {
	w := assignmentEnv(t)
	payloadsBefore := payloadCount(t, w)
	code, out, errText := runIn([]string{"unassign", "sto:item-a"})
	if code != 0 {
		t.Fatalf("unassign absent: exit = %d, want 0\nstdout: %s\nstderr: %s", code, out, errText)
	}
	if !strings.Contains(out, "no assignment to remove") {
		t.Errorf("unassign absent output must report the no-op:\n%s", out)
	}
	if got := payloadCount(t, w); got != payloadsBefore {
		t.Errorf("a no-op unassign must not write a payload, got %d -> %d", payloadsBefore, got)
	}
}

// TestAssignRoundTripSameDay (sto:assigned-work-cli regression): after
// assign -> unassign, re-assigning the SAME member on the same day
// recreates the earlier same-version payload. The reference must move
// back to it (store.RepointUnit — a same-version payload is a
// current-state candidate, never history); without the re-point the
// item would silently stay unassigned.
func TestAssignRoundTripSameDay(t *testing.T) {
	assignmentEnv(t)
	if code, _, errText := runIn([]string{"assign", "sto:item-a", "--to", "mbr:alice"}); code != 0 {
		t.Fatalf("assign #1: exit = %d\nstderr: %s", code, errText)
	}
	if code, _, errText := runIn([]string{"unassign", "sto:item-a"}); code != 0 {
		t.Fatalf("unassign: exit = %d\nstderr: %s", code, errText)
	}
	if got := assignedTo(t, "acme/sto:item-a"); len(got) != 0 {
		t.Fatalf("after unassign: assigned-to = %v, want none", got)
	}
	code, out, errText := runIn([]string{"assign", "sto:item-a", "--to", "mbr:alice"})
	if code != 0 {
		t.Fatalf("assign #2 (round trip): exit = %d\nstdout: %s\nstderr: %s", code, out, errText)
	}
	if got := assignedTo(t, "acme/sto:item-a"); len(got) != 1 || got[0] != "mbr:alice" {
		t.Errorf("after re-assign: assigned-to = %v, want [mbr:alice] (the reference must move back)", got)
	}
}

// TestAssignmentJSONPinnedKeys: the machine report (schema
// eka-assignment-v1) carries the pinned keys of the slice —
// "assignee" renders the member identity, "no-assignee" the member-
// axis bucket flag (never "unassigned").
func TestAssignmentJSONPinnedKeys(t *testing.T) {
	assignmentEnv(t)
	code, out, errText := runIn([]string{"assign", "sto:item-a", "--to", "mbr:alice", "--json"})
	if code != 0 {
		t.Fatalf("assign --json: exit = %d\nstderr: %s", code, errText)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("assign --json: invalid JSON: %v\n%s", err, out)
	}
	if doc["schema"] != "eka-assignment-v1" || doc["ok"] != true || doc["action"] != "assign" {
		t.Errorf("--json = %v, want the pinned eka-assignment-v1 document", doc)
	}
	if doc["assignee"] != "acme/mbr:alice" {
		t.Errorf("--json assignee = %v, want the canonical member form", doc["assignee"])
	}
	if _, present := doc["no-assignee"]; present {
		t.Errorf("--json must not carry a no-assignee key on an assignment: %v", doc)
	}

	code, out, errText = runIn([]string{"unassign", "sto:item-a", "--json"})
	if code != 0 {
		t.Fatalf("unassign --json: exit = %d\nstderr: %s", code, errText)
	}
	// A fresh map: json.Unmarshal into a non-nil map keeps entries the
	// new document does not mention, so a reused map would leak the
	// assign doc's assignee key into the unassign assertions.
	doc = nil
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("unassign --json: invalid JSON: %v\n%s", err, out)
	}
	if doc["schema"] != "eka-assignment-v1" || doc["action"] != "unassign" {
		t.Errorf("--json = %v, want the unassign document", doc)
	}
	if doc["no-assignee"] != true {
		t.Errorf("--json no-assignee = %v, want the pinned member-axis bucket flag", doc["no-assignee"])
	}
	if _, present := doc["assignee"]; present {
		t.Errorf("--json must not carry an assignee key on an unassign: %v", doc)
	}
}

// TestAssignmentRefusalJSON: with --json the refusal document travels
// on stdout (schema eka-assignment-v1, ok:false, reason + hint) while
// the human line goes to stderr — exit 1.
func TestAssignmentRefusalJSON(t *testing.T) {
	assignmentEnv(t)
	code, out, errText := runIn([]string{"assign", "sto:item-a", "--to", "mbr:ghost", "--json"})
	if code != 1 {
		t.Fatalf("refusal --json: exit = %d, want 1\nstdout: %s\nstderr: %s", code, out, errText)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("refusal --json: invalid JSON: %v\n%s", err, out)
	}
	if doc["ok"] != false || doc["action"] != "assign" {
		t.Errorf("--json refusal = %v, want ok:false", doc)
	}
	if !strings.Contains(errText, "does not resolve") {
		t.Errorf("stderr = %q, want the refusal line", errText)
	}
}

// TestAssignOnDraftMutatesFile: a line with a pending draft (no
// published instance) gets its assigned-to edge written to the draft
// file in place — no publish, no published instance created.
func TestAssignOnDraftMutatesFile(t *testing.T) {
	w := assignmentEnv(t)
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
	code, out, errText := runIn([]string{"assign", "sto:item-c", "--to", "mbr:alice"})
	if code != 0 {
		t.Fatalf("assign on a draft: exit = %d\nstdout: %s\nstderr: %s", code, out, errText)
	}
	if !strings.Contains(out, "draft") {
		t.Errorf("assign output must report the draft state:\n%s", out)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(after), `"assignedTo": [`) ||
		!strings.Contains(string(after), `"mbr:alice"`) {
		t.Errorf("draft file missing the assigned-to edge:\n%s", after)
	}
	if string(before) == string(after) {
		t.Errorf("the draft file must have been rewritten")
	}
	// No published instance was created.
	if code, _, _ := runIn([]string{"get", "sto:item-c"}); code == 0 {
		t.Errorf("assign on a draft must not publish an instance")
	}
}

// TestAssignmentDeterministicOutput: the same assignment operation
// produces byte-identical output across runs.
func TestAssignmentDeterministicOutput(t *testing.T) {
	var outputs []string
	for i := 0; i < 2; i++ {
		assignmentEnv(t)
		code, out, errText := runIn([]string{"assign", "sto:item-a", "--to", "mbr:alice"})
		if code != 0 {
			t.Fatalf("assign run %d: exit = %d\nstderr: %s", i, code, errText)
		}
		outputs = append(outputs, out)
	}
	if outputs[0] != outputs[1] {
		t.Errorf("assign output is not deterministic:\n--- run 1 ---\n%s\n--- run 2 ---\n%s", outputs[0], outputs[1])
	}
}
