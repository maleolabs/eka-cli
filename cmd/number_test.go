package cmd

import (
	"encoding/json"
	"strings"
	"testing"
)

// This file tests the issue-number references (RFC: per-group
// incremental numbers — "#<n>" addresses a line; work items, tickets
// and notes count independently per project): the machine number
// field, the "#<n>" resolution across view/get/note/transition, the
// group-narrowed ticket lookup, and the display labels.

// numberEnv builds the multi-type env: bug:two (work-item #1) and
// tkt:t-one (ticket #1) — both groups carry #1, so a bare "#1" is
// ambiguous while the group-narrowed forms are not. sto:one is the
// work-item #2 (canonical seed order: bug:two numbers first).
func numberEnv(t *testing.T) string {
	t.Helper()
	return noteEnvMulti(t)
}

func TestGetNumberField(t *testing.T) {
	numberEnv(t)
	// The machine document carries the additive "number" field.
	code, out, errText := runIn([]string{"get", "test-ns/sto:one", "--compact"})
	if code != 0 {
		t.Fatalf("get: exit = %d, stderr %q", code, errText)
	}
	var doc struct {
		Number int `json:"number"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Number != 2 {
		t.Errorf("get number = %d, want 2 (sto:one is the second work item)", doc.Number)
	}
}

func TestGetNumberTarget(t *testing.T) {
	numberEnv(t)
	// "#1" is ambiguous here (work-item #1 AND ticket #1): the bare
	// lookup lists the candidates.
	code, _, errText := runIn([]string{"get", "#1"})
	if code != 2 || !strings.Contains(errText, "ambiguous") {
		t.Fatalf("get #1: exit = %d, stderr %q; want the ambiguity usage error", code, errText)
	}
	// An unknown number is a deterministic error.
	code, _, errText = runIn([]string{"get", "#99"})
	if code != 2 || !strings.Contains(errText, "no item with number #99") {
		t.Fatalf("get #99: exit = %d, stderr %q", code, errText)
	}
}

func TestViewNumberTarget(t *testing.T) {
	repo := numberEnv(t)
	// The ticket projection narrows to the ticket group: "#1" is the
	// ticket — and the header shows the "#1" label.
	code, out, errText := runIn([]string{"view", "ticket", "#1"})
	if code != 0 {
		t.Fatalf("view ticket #1: exit = %d, stderr %q", code, errText)
	}
	if !strings.Contains(out, "test-ns/tkt:t-one") || !strings.Contains(out, "#1") {
		t.Errorf("view ticket #1 must render the ticket with its number label:\n%s", out)
	}
	// The document projection (bare argument) renders the number too.
	code, out, _ = runIn([]string{"view", "sto:one"})
	if code != 0 || !strings.Contains(out, "#2") {
		t.Errorf("view sto:one must render the number label:\n%s", out)
	}
	// The bare "#1" is ambiguous (both groups carry #1).
	code, _, errText = runIn([]string{"view", "#1"})
	if code != 2 || !strings.Contains(errText, "ambiguous") {
		t.Errorf("view #1: exit = %d, stderr %q; want the ambiguity usage error", code, errText)
	}
	_ = repo
}

func TestTransitionNumberTarget(t *testing.T) {
	numberEnv(t)
	// The transition resolves "#1" in the work-item group: bug:two.
	code, out, errText := runIn([]string{"transition", "#1", "--forward", "--force"})
	if code != 0 {
		t.Fatalf("transition #1: exit = %d, stderr %q", code, errText)
	}
	if !strings.Contains(out, "test-ns/bug:two") || !strings.Contains(out, "in-progress") {
		t.Errorf("transition #1 must move bug:two:\n%s", out)
	}
}

func TestNoteNumberTarget(t *testing.T) {
	numberEnv(t)
	// `eka note #1` resolves the subject (the work-item group is the
	// only numbered note subject here — the ticket is the other #1,
	// so the bare form is ambiguous; the note command requires the
	// unambiguous resolution).
	code, _, errText := runIn([]string{"note", "#1", "--role", "review", "--by", "x"})
	if code != 2 || !strings.Contains(errText, "ambiguous") {
		t.Fatalf("note #1: exit = %d, stderr %q; want the ambiguity usage error", code, errText)
	}
	// Unambiguous case: after the ticket line's group is out of scope
	// (resolve via the qualified form works), the note attaches to
	// sto:one via its qualified line.
	body := noteBody(t, `{"verdict": "approve", "notes": []}`)
	code, _, errText = runIn([]string{"note", "test-ns/sto:one", "--role", "review", "--content-file", body, "--by", "x"})
	if code != 0 {
		t.Fatalf("note qualified: exit = %d, stderr %q", code, errText)
	}
}
