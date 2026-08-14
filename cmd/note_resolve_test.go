package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// This file tests the note reply & resolve commands (ADR-019 D8
// revised): the explicit resolution flow (single + --all per unit),
// the optional documenting reply, the author identity kinds
// (--by-kind), the machine reports, and the reply tree of the ticket
// projection.

// noteDraft creates one implementation note draft on sto:one and
// returns its draft id.
func noteDraft(t *testing.T, by, kind string) string {
	t.Helper()
	body := noteBody(t, `{"summary": "impl done", "changes": ["x"], "tests": ["y"]}`)
	args := []string{"note", "sto:one", "--role", "implementation", "--content-file", body, "--by", by}
	if kind != "" {
		args = append(args, "--by-kind", kind)
	}
	code, _, errText := runIn(args)
	if code != 0 {
		t.Fatalf("note draft: exit = %d, stderr %q", code, errText)
	}
	return "one-implementation"
}

func TestNoteReplyDraft(t *testing.T) {
	noteEnvMulti(t)
	noteDraft(t, "agent-x", "agent")

	code, out, errText := runIn([]string{"note", "reply", "cmt:one-implementation",
		"--body", "Looks good, ship it.", "--by", "agent-x", "--by-kind", "agent"})
	if code != 0 {
		t.Fatalf("note reply: exit = %d, stderr %q", code, errText)
	}
	if !strings.Contains(out, "cmt:one-implementation-reply") {
		t.Errorf("stdout = %q, want the reply id", out)
	}
	path := draftPathOf(t, "one-implementation-reply")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reply draft not found at %s: %v", path, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	rels := doc["relationships"].(map[string]any)
	replies := rels["repliesTo"].([]any)
	if replies[0] != "test-ns/cmt:one-implementation" {
		t.Errorf("reply repliesTo = %v, want the parent line", replies)
	}
	content := doc["content"].(map[string]any)
	if content["role"] != "reply" || content["body"] != "Looks good, ship it." {
		t.Errorf("reply content = %v, want {role: reply, body}", content)
	}
	author := doc["author"]
	if author != "agent-x" {
		// An agent identity serializes as the structured object.
		if m, ok := author.(map[string]any); !ok || m["kind"] != "agent" || m["name"] != "agent-x" {
			t.Errorf("reply author = %v, want agent-x (agent)", author)
		}
	}
	// The reply draft's change-log authority carries the agent kind.
	cl := doc["changeLog"].([]any)
	entry := cl[0].(map[string]any)
	if entry["by"] != "agent-x" {
		if m, ok := entry["by"].(map[string]any); !ok || m["kind"] != "agent" || m["name"] != "agent-x" {
			t.Errorf("reply change-log by = %v, want agent-x (agent)", entry["by"])
		}
	}

	// A reply without a body is a usage error (exit 2).
	code, _, errText = runIn([]string{"note", "reply", "cmt:one-implementation", "--by", "x"})
	if code != 2 {
		t.Errorf("reply without body: exit = %d, want 2; stderr %q", code, errText)
	}
	// A reply to an unknown parent is refused (exit 1).
	code, _, _ = runIn([]string{"note", "reply", "cmt:ghost", "--body", "hi", "--by", "x"})
	if code != 1 {
		t.Errorf("reply to unknown parent: exit = %d, want 1", code)
	}
}

func TestNoteResolveDraftAndGate(t *testing.T) {
	noteEnvMulti(t)
	noteDraft(t, "agent-x", "agent")

	// Resolve the draft note: note-state resolved + change-log entry.
	code, out, errText := runIn([]string{"note", "resolve", "cmt:one-implementation", "--by", "agent-x", "--by-kind", "agent"})
	if code != 0 {
		t.Fatalf("note resolve: exit = %d, stderr %q", code, errText)
	}
	if !strings.Contains(out, "resolved (draft)") {
		t.Errorf("stdout = %q, want the draft-resolution report", out)
	}
	raw, err := os.ReadFile(draftPathOf(t, "one-implementation"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc["state"].(map[string]any)["noteState"] != "resolved" {
		t.Errorf("draft noteState = %v, want resolved", doc["state"])
	}
	cl := doc["changeLog"].([]any)
	last := cl[len(cl)-1].(map[string]any)
	if last["domain"] != "noteState" || last["to"] != "resolved" {
		t.Errorf("last change-log entry = %v, want the note-state open->resolved entry", last)
	}

	// The R13 gate sees the resolved draft: in-progress -> in-review.
	code, _, _ = runIn([]string{"transition", "sto:one", "--forward", "--force"})
	if code != 0 {
		t.Fatalf("forward to in-progress: exit = %d", code)
	}
	code, _, errText = runIn([]string{"transition", "sto:one", "--forward", "--force"})
	if code != 0 {
		t.Fatalf("forward to in-review with the resolved draft: exit = %d, stderr %q", code, errText)
	}

	// Resolving again is an already-resolved no-op (exit 0).
	code, out, _ = runIn([]string{"note", "resolve", "cmt:one-implementation", "--by", "x"})
	if code != 0 || !strings.Contains(out, "already resolved") {
		t.Errorf("re-resolve: exit = %d, stdout %q; want the already-resolved report", code, out)
	}
}

func TestNoteResolvePublished(t *testing.T) {
	noteEnvMulti(t)
	id := noteDraft(t, "user-x", "")
	if code, _, _ := runIn([]string{"publish", "cmt:" + id}); code != 0 {
		t.Fatalf("publish failed")
	}
	// The published unit is open.
	code, out, _ := runIn([]string{"get", "test-ns/cmt:" + id, "--compact"})
	if code != 0 || !strings.Contains(out, `"noteState":"open"`) {
		t.Fatalf("published note state: exit = %d\n%s", code, out)
	}
	// Resolve: the publish pipeline advances the line to a new instance.
	code, out, errText := runIn([]string{"note", "resolve", "cmt:" + id, "--by", "worker-1", "--by-kind", "worker"})
	if code != 0 {
		t.Fatalf("resolve published: exit = %d, stderr %q", code, errText)
	}
	if !strings.Contains(out, "published instance advanced") {
		t.Errorf("stdout = %q, want the published-resolution report", out)
	}
	// The line's CURRENT payload is the new instance (the machine
	// contract: a canonical-form lookup addresses the exact instance).
	code, out, _ = runIn([]string{"get", "test-ns/cmt:" + id + ":2", "--compact"})
	if code != 0 || !strings.Contains(out, `"noteState":"resolved"`) {
		t.Errorf("resolved unit: exit = %d\n%s", code, out)
	}
	if !strings.Contains(out, `"instanceVersion":2`) {
		t.Errorf("resolved unit must be instance 2 (immutable advance)\n%s", out)
	}
}

func TestNoteResolveAllInUnit(t *testing.T) {
	repo := noteEnvMulti(t)
	// Two open notes discussing sto:one: a draft and a published one.
	noteDraft(t, "agent-x", "agent")
	seedCmtNote(t, repo, "one-review", "test-ns/sto:one", "approve") // published via docs sync (noteState resolved in fixture)
	// The seeded review note is already resolved in its fixture; the
	// draft is open. Resolve --all: the draft resolves, the published
	// one is reported already-resolved.
	code, out, errText := runIn([]string{"note", "resolve", "sto:one", "--all", "--by", "agent-x", "--by-kind", "agent"})
	if code != 0 {
		t.Fatalf("resolve --all: exit = %d, stderr %q", code, errText)
	}
	if !strings.Contains(out, "one-implementation") || !strings.Contains(out, "one-review") {
		t.Errorf("stdout = %q, want both notes in the report", out)
	}
	// The draft is resolved.
	raw, err := os.ReadFile(draftPathOf(t, "one-implementation"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc["state"].(map[string]any)["noteState"] != "resolved" {
		t.Errorf("draft noteState after --all = %v, want resolved", doc["state"])
	}
}

func TestNoteResolveWithReply(t *testing.T) {
	noteEnvMulti(t)
	noteDraft(t, "agent-x", "agent")

	code, out, errText := runIn([]string{"note", "resolve", "cmt:one-implementation",
		"--reply", "Verified on staging.", "--by", "agent-x", "--by-kind", "agent"})
	if code != 0 {
		t.Fatalf("resolve --reply: exit = %d, stderr %q", code, errText)
	}
	if !strings.Contains(out, "cmt:one-implementation-reply") {
		t.Errorf("stdout = %q, want the reply id in the report", out)
	}
	// The reply draft exists with the documenting body.
	raw, err := os.ReadFile(draftPathOf(t, "one-implementation-reply"))
	if err != nil {
		t.Fatalf("reply draft not found: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc["content"].(map[string]any)["body"] != "Verified on staging." {
		t.Errorf("reply body = %v", doc["content"])
	}
	// The note is resolved.
	if doc2, _ := os.ReadFile(draftPathOf(t, "one-implementation")); !strings.Contains(string(doc2), `"noteState": "resolved"`) {
		t.Errorf("the resolved note must carry note-state resolved")
	}
}

func TestNoteResolveJSON(t *testing.T) {
	noteEnvMulti(t)
	noteDraft(t, "agent-x", "agent")
	code, out, _ := runIn([]string{"note", "resolve", "cmt:one-implementation", "--by", "agent-x", "--by-kind", "agent", "--json"})
	if code != 0 {
		t.Fatalf("resolve --json: exit = %d", code)
	}
	var doc struct {
		Schema   string   `json:"schema"`
		OK       bool     `json:"ok"`
		Target   string   `json:"target"`
		Resolved []string `json:"resolved"`
		Path     string   `json:"path"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Schema != noteResolveSchema || !doc.OK || doc.Target != "test-ns/cmt:one-implementation" {
		t.Errorf("resolve json = %+v", doc)
	}
	if len(doc.Resolved) != 1 || doc.Resolved[0] != "test-ns/cmt:one-implementation" || doc.Path == "" {
		t.Errorf("resolve json resolved = %v, path = %q", doc.Resolved, doc.Path)
	}
}

func TestNoteResolveRefusals(t *testing.T) {
	noteEnvMulti(t)
	// Unknown note line: refusal (exit 1).
	code, _, errText := runIn([]string{"note", "resolve", "cmt:ghost", "--by", "x"})
	if code != 1 || !strings.Contains(errText, "not found") {
		t.Errorf("resolve unknown note: exit = %d, stderr %q; want refusal", code, errText)
	}
	// Non-note target: usage (exit 2).
	code, _, _ = runIn([]string{"note", "resolve", "sto:one", "--by", "x"})
	if code != 2 {
		t.Errorf("resolve non-note: exit = %d, want 2", code)
	}
	// Unknown kind: usage (exit 2).
	code, _, _ = runIn([]string{"note", "resolve", "cmt:ghost", "--by", "x", "--by-kind", "bot"})
	if code != 2 {
		t.Errorf("resolve with unknown kind: exit = %d, want 2", code)
	}
}

// TestViewTicketNotesTree: the ticket projection renders the notes
// section with the heading/list separation, the reply tree (branch
// prefix) and the author identity tags (user/agent/worker).
func TestViewTicketNotesTree(t *testing.T) {
	repo := noteEnvMulti(t)
	// A work item note (draft -> published with an agent author), plus
	// a reply to it (worker author).
	noteDraft(t, "agent-x", "agent")
	if code, _, _ := runIn([]string{"publish", "cmt:one-implementation"}); code != 0 {
		t.Fatalf("publish failed")
	}
	if code, _, errText := runIn([]string{"note", "reply", "cmt:one-implementation",
		"--body", "Handled by worker.", "--by", "worker-1", "--by-kind", "worker"}); code != 0 {
		t.Fatalf("reply: exit = %d, stderr %q", code, errText)
	}
	if code, _, _ := runIn([]string{"publish", "cmt:one-implementation-reply"}); code != 0 {
		t.Fatalf("publish reply failed")
	}

	code, out, errText := runIn([]string{"view", "ticket", "sto:one", "--with-note"})
	if code != 0 {
		t.Fatalf("view ticket: exit = %d, stderr %q", code, errText)
	}
	// Heading/list separation: the count line is followed by a blank
	// line before the first comment card.
	if !strings.Contains(out, "note(s) discussing") || !strings.Contains(out, "\n\n  test-ns/cmt:one-implementation") {
		t.Errorf("notes section must separate heading and list with a blank line:\n%s", out)
	}
	// The reply tree: branch prefix + reply identity + worker tag.
	if !strings.Contains(out, "└─") || !strings.Contains(out, "cmt:one-implementation-reply") {
		t.Errorf("reply tree missing (branch + reply identity):\n%s", out)
	}
	if !strings.Contains(out, "[worker]") || !strings.Contains(out, "worker-1") {
		t.Errorf("reply author tag missing ([worker] worker-1):\n%s", out)
	}
	// The parent note's agent author tag.
	if !strings.Contains(out, "[agent]") || !strings.Contains(out, "agent-x") {
		t.Errorf("parent author tag missing ([agent] agent-x):\n%s", out)
	}
	// The reply body renders under the reply.
	if !strings.Contains(out, "Handled by worker.") {
		t.Errorf("reply body missing:\n%s", out)
	}
	_ = filepath.Join(repo, "unused")
}
