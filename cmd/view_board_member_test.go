package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/maleolabs/eka-core/workspace"
)

// This file tests the advisory member-scoped board (`eka view board
// --member me|<mbr-id>`, ADR-029 Decision 3): the member filter with
// its assignee tags, the dedicated 'No assignee' bucket and the
// machine keys. The work-ownership commands (assign/reassign/unassign)
// were removed; the assigned-to edge is seeded through the authoring
// batch path (`eka new --file` + `eka publish --all`) instead.

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

// memberRepo sets up a repository (namespace acme) with two published
// member lines (alice, bob — each authored by its own identity so the
// --member me resolution can match exactly one line) and returns the
// workspace.
func memberRepo(t *testing.T) *workspace.Workspace {
	t.Helper()
	w, _ := authoringEnv(t, "acme")
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

// assignmentEnv sets up the memberRepo members plus two published work
// items (item-a, item-b), both WITHOUT an assigned-to edge.
func assignmentEnv(t *testing.T) *workspace.Workspace {
	t.Helper()
	w := memberRepo(t)
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

// boardMemberEnv sets up the memberRepo members plus two published work
// items with item-a already carrying an assigned-to edge to mbr:alice
// and item-b unassigned. The edge is injected into item-a's pending
// draft (the batch schema deliberately omits assigned-to; ScanFile and
// publish accept it) and published — the ownership commands were
// removed.
func boardMemberEnv(t *testing.T) *workspace.Workspace {
	t.Helper()
	w := memberRepo(t)
	body := stoBody(t)
	for _, id := range []string{"item-a", "item-b"} {
		if code, _, errText := runIn([]string{"new", "sto:" + id, "--content-file", body}); code != 0 {
			t.Fatalf("new sto:%s: exit = %d\nstderr: %s", id, code, errText)
		}
	}
	project := projectOf(t, w, mustAbs(t, "."))
	path := draftFile(t, w, project, "sto", "item-a")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read draft: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("draft JSON: %v", err)
	}
	doc["relationships"] = map[string]any{"assignedTo": []any{"mbr:alice"}}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(out, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, errText := runIn([]string{"publish", "--all"}); code != 0 {
		t.Fatalf("publish --all: exit = %d\nstderr: %s", code, errText)
	}
	return w
}

// TestViewBoardMemberMeHappyPath: `--member me` resolves the operator
// identity from `git config user.name` to exactly one member line and
// renders the member-scoped board: the member's assigned items in
// their state columns (the assignee tag on the card) plus the
// dedicated 'No assignee' bucket. The container-axis "Unassigned"
// insight is suppressed in member views — the "No Assignee" bucket
// replaces it there.
func TestViewBoardMemberMeHappyPath(t *testing.T) {
	boardMemberEnv(t)
	gitIdentityEnv(t, "alice")
	code, out, errText := runIn([]string{"view", "board", "--member", "me"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", code, out, errText)
	}
	for _, want := range []string{
		"Member       acme/mbr:alice",
		"2 work items in member scope · 1 item without an assignee",
		"│ ▸ #1 item-a",
		"│   [sto] · unassigned · alice", // the assignee tag on the card
		"│ No Assignee (1)",
		"│ ▸ #2 item-b",  // the no-edge item surfaces in the bucket
		"No Assignee: 1", // the member-axis insight
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output must contain %q:\n%s", want, out)
		}
	}
	// The container-axis "Unassigned" insight is suppressed in member
	// views (the "No Assignee" bucket replaces it there).
	if strings.Contains(out, "Unassigned:") {
		t.Errorf("member view must not carry the container-axis Unassigned insight:\n%s", out)
	}
}

// TestViewBoardMemberMeZeroMatchRefused: `--member me` with an operator
// identity that matches NO member line is a deterministic refusal
// (exit 2) listing the available members.
func TestViewBoardMemberMeZeroMatchRefused(t *testing.T) {
	assignmentEnv(t)
	gitIdentityEnv(t, "ghost")
	code, out, errText := runIn([]string{"view", "board", "--member", "me"})
	if code != 2 {
		t.Fatalf("exit = %d, want 2\nstdout: %s\nstderr: %s", code, out, errText)
	}
	if !strings.Contains(errText, `the operator identity "ghost" matches 0 member line(s)`) {
		t.Errorf("stderr = %q, want the zero-match refusal", errText)
	}
	for _, want := range []string{"acme/mbr:alice", "acme/mbr:bob"} {
		if !strings.Contains(errText, want) {
			t.Errorf("stderr must list the available member %q:\n%s", want, errText)
		}
	}
}

// TestViewBoardMemberMeMultipleMatchRefused: `--member me` with an
// operator identity that matches MORE than one member line is a
// deterministic refusal (exit 2) listing the available members.
func TestViewBoardMemberMeMultipleMatchRefused(t *testing.T) {
	assignmentEnv(t)
	// A second member line authored by alice: the operator identity
	// "alice" then matches both mbr:alice and mbr:alice-dup.
	gitIdentityEnv(t, "alice")
	if code, _, errText := runIn([]string{"new", "mbr:alice-dup", "--content-file", mbrBody(t)}); code != 0 {
		t.Fatalf("new mbr:alice-dup: exit = %d\nstderr: %s", code, errText)
	}
	if code, _, errText := runIn([]string{"publish", "mbr:alice-dup"}); code != 0 {
		t.Fatalf("publish mbr:alice-dup: exit = %d\nstderr: %s", code, errText)
	}
	code, out, errText := runIn([]string{"view", "board", "--member", "me"})
	if code != 2 {
		t.Fatalf("exit = %d, want 2\nstdout: %s\nstderr: %s", code, out, errText)
	}
	if !strings.Contains(errText, `the operator identity "alice" matches 2 member line(s)`) {
		t.Errorf("stderr = %q, want the multiple-match refusal", errText)
	}
	for _, want := range []string{"acme/mbr:alice", "acme/mbr:alice-dup", "acme/mbr:bob"} {
		if !strings.Contains(errText, want) {
			t.Errorf("stderr must list the available member %q:\n%s", want, errText)
		}
	}
}

// TestViewBoardMemberUnresolvableRefused: an unresolvable --member id
// is a deterministic refusal (exit 2) listing the available members.
func TestViewBoardMemberUnresolvableRefused(t *testing.T) {
	assignmentEnv(t)
	code, out, errText := runIn([]string{"view", "board", "--member", "ghost"})
	if code != 2 {
		t.Fatalf("exit = %d, want 2\nstdout: %s\nstderr: %s", code, out, errText)
	}
	if !strings.Contains(errText, `member "ghost" not found`) {
		t.Errorf("stderr = %q, want the not-found refusal", errText)
	}
	for _, want := range []string{"acme/mbr:alice", "acme/mbr:bob"} {
		if !strings.Contains(errText, want) {
			t.Errorf("stderr must list the available member %q:\n%s", want, errText)
		}
	}
}

// TestViewBoardMemberJSONPinnedKeys: the member-scoped board machine
// document (schema eka-board-member-v1) carries the pinned member-axis
// keys — "assignee" (the member's assigned items) and "no-assignee"
// (the dedicated 'No assignee' bucket, never "unassigned").
func TestViewBoardMemberJSONPinnedKeys(t *testing.T) {
	boardMemberEnv(t)
	code, out, errText := runIn([]string{"view", "board", "--member", "alice", "--json"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", code, out, errText)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("--json: invalid JSON: %v\n%s", err, out)
	}
	if doc["schema"] != "eka-board-member-v1" {
		t.Errorf("--json schema = %v, want eka-board-member-v1", doc["schema"])
	}
	if doc["member"] != "acme/mbr:alice" {
		t.Errorf("--json member = %v, want acme/mbr:alice", doc["member"])
	}
	assignee, _ := doc["assignee"].([]any)
	if len(assignee) != 1 || assignee[0] != "acme/sto:item-a" {
		t.Errorf("--json assignee = %v, want [acme/sto:item-a]", doc["assignee"])
	}
	noAssignee, _ := doc["no-assignee"].([]any)
	if len(noAssignee) != 1 || noAssignee[0] != "acme/sto:item-b" {
		t.Errorf("--json no-assignee = %v, want [acme/sto:item-b]", doc["no-assignee"])
	}
}

// TestViewBoardMemberNoAssigneeNeverHidden: every work item WITHOUT an
// assigned-to edge surfaces in the 'No assignee' bucket of the
// member-scoped board — never silently excluded (ADR-029 Decision 3).
// A member with no assigned items still sees the full unassigned work.
func TestViewBoardMemberNoAssigneeNeverHidden(t *testing.T) {
	assignmentEnv(t)
	code, out, errText := runIn([]string{"view", "board", "--member", "bob"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", code, out, errText)
	}
	for _, want := range []string{
		"2 work items in member scope · 2 items without an assignee",
		"│ No Assignee (2)",
		"│ ▸ #1 item-a",
		"│ ▸ #2 item-b",
		"No Assignee: 2",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output must contain %q:\n%s", want, out)
		}
	}
}
