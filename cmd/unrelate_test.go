package cmd

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/maleolabs/eka-core/workspace"
)

// This file tests `eka unrelate` at CLI level (Opsi A bagian 2): the
// ticket derives-from edge removal with the same-version re-point (no
// instance churn), the R8 last-container refusal, the container-scope
// gates (planned-only, retired-bypass in active, history-locked when
// completed), the confirmation gate, and the help distinction between
// unlink and retire.

// ticketFixtureEnv seeds a repository with the ticket 2-edge
// convention: plan:roadmap-v1 (approved), ctr:wave-1 (planned,
// depends-on the plan), sto:item-1 (todo), and tkt:ticket-1 published
// with derives-from ctr:wave-1 + sto:item-1. It returns the workspace.
func ticketFixtureEnv(t *testing.T) *workspace.Workspace {
	t.Helper()
	w, _ := authoringEnv(t, "acme")
	if code, _, errText := runIn([]string{"new", "plan:roadmap-v1", "--dimension", "planning"}); code != 0 {
		t.Fatalf("new plan: exit = %d\nstderr: %s", code, errText)
	}
	if code, _, errText := runIn([]string{"publish", "plan:roadmap-v1"}); code != 0 {
		t.Fatalf("publish plan: exit = %d\nstderr: %s", code, errText)
	}
	if code, _, errText := runIn([]string{"transition", "plan:roadmap-v1", "approved", "--force"}); code != 0 {
		t.Fatalf("approve plan: exit = %d\nstderr: %s", code, errText)
	}
	if code, _, errText := runIn([]string{"new", "ctr:wave-1", "--depends-on", "plan:roadmap-v1"}); code != 0 {
		t.Fatalf("new ctr: exit = %d\nstderr: %s", code, errText)
	}
	if code, _, errText := runIn([]string{"publish", "ctr:wave-1"}); code != 0 {
		t.Fatalf("publish ctr: exit = %d\nstderr: %s", code, errText)
	}
	body := stoBody(t)
	if code, _, errText := runIn([]string{"new", "sto:item-1", "--content-file", body}); code != 0 {
		t.Fatalf("new sto: exit = %d\nstderr: %s", code, errText)
	}
	if code, _, errText := runIn([]string{"publish", "sto:item-1"}); code != 0 {
		t.Fatalf("publish sto: exit = %d\nstderr: %s", code, errText)
	}
	if code, _, errText := runIn([]string{"new", "tkt:ticket-1", "--derives-from", "ctr:wave-1,sto:item-1"}); code != 0 {
		t.Fatalf("new tkt: exit = %d\nstderr: %s", code, errText)
	}
	if code, _, errText := runIn([]string{"publish", "tkt:ticket-1"}); code != 0 {
		t.Fatalf("publish tkt: exit = %d\nstderr: %s", code, errText)
	}
	return w
}

// ticketRels returns the relationship list of the latest published
// instance of a line, decoded from `eka get`.
func ticketRels(t *testing.T, target string) []any {
	t.Helper()
	doc := getDoc(t, target)
	rels, _ := doc["relationships"].([]any)
	return rels
}

// TestUnrelateHappyPath: unlinking the work-item edge from a ticket in
// a planned container removes exactly that edge, keeps the instance
// version at 1 (no churn), and writes exactly one payload row.
func TestUnrelateHappyPath(t *testing.T) {
	w := ticketFixtureEnv(t)
	payloadsBefore := payloadCount(t, w)

	code, out, errText := runIn([]string{"unrelate", "tkt:ticket-1", "sto:item-1", "--force"})
	if code != 0 {
		t.Fatalf("unrelate: exit = %d\nstdout: %s\nstderr: %s", code, out, errText)
	}
	if !strings.Contains(out, "sto:item-1") || !strings.Contains(out, "removed") {
		t.Errorf("unrelate output must name the removed edge:\n%s", out)
	}
	if !strings.Contains(out, "unchanged") {
		t.Errorf("unrelate output must show the unchanged instance version:\n%s", out)
	}

	doc := getDoc(t, "acme/tkt:ticket-1")
	ident, _ := doc["identity"].(map[string]any)
	if v, _ := ident["instanceVersion"].(float64); v != 1 {
		t.Errorf("identity.instanceVersion = %v, want 1 (no instance churn)", ident["instanceVersion"])
	}
	rels := ticketRels(t, "acme/tkt:ticket-1")
	if len(rels) != 1 {
		t.Fatalf("relationships = %+v, want exactly the surviving ctr edge", rels)
	}
	rel, _ := rels[0].(map[string]any)
	if rel["type"] != "derives-from" || rel["target"] != "ctr:wave-1" {
		t.Errorf("relationship = %+v, want derives-from -> ctr:wave-1", rel)
	}
	if got := payloadCount(t, w); got != payloadsBefore+1 {
		t.Errorf("payloads = %d -> %d, want exactly +1", payloadsBefore, got)
	}
}

// TestUnrelateLastCtrRefusedR8: a ticket with only the container edge
// cannot unlink it — R8 (at least one resolving ctr-) refuses, exit 1,
// nothing written.
func TestUnrelateLastCtrRefusedR8(t *testing.T) {
	w := ticketFixtureEnv(t)
	if code, _, errText := runIn([]string{"new", "tkt:solo", "--derives-from", "ctr:wave-1"}); code != 0 {
		t.Fatalf("new tkt:solo: exit = %d\nstderr: %s", code, errText)
	}
	if code, _, errText := runIn([]string{"publish", "tkt:solo"}); code != 0 {
		t.Fatalf("publish tkt:solo: exit = %d\nstderr: %s", code, errText)
	}
	payloadsBefore := payloadCount(t, w)

	code, out, errText := runIn([]string{"unrelate", "tkt:solo", "ctr:wave-1", "--force"})
	if code != 1 {
		t.Fatalf("exit = %d, want 1\nstdout: %s\nstderr: %s", code, out, errText)
	}
	if !strings.Contains(errText, "last resolving container") {
		t.Errorf("stderr = %q, want the R8 last-container refusal", errText)
	}
	if got := payloadCount(t, w); got != payloadsBefore {
		t.Errorf("a refused unrelate must not write, got %d -> %d", payloadsBefore, got)
	}
	if rels := ticketRels(t, "acme/tkt:solo"); len(rels) != 1 {
		t.Errorf("relationships = %+v, want the ctr edge intact", rels)
	}
}

// TestUnrelateCompletedContainerRefused: unlinking from a completed
// container is history-locked — exit 1 even with --force.
func TestUnrelateCompletedContainerRefused(t *testing.T) {
	ticketFixtureEnv(t)
	// Cancel the work item (no note gates on cancel), then complete
	// the container (all-done: canceled satisfies the gate).
	if code, _, errText := runIn([]string{"transition", "sto:item-1", "canceled", "--force"}); code != 0 {
		t.Fatalf("cancel sto: exit = %d\nstderr: %s", code, errText)
	}
	if code, _, errText := runIn([]string{"transition", "ctr:wave-1", "active", "--force"}); code != 0 {
		t.Fatalf("activate ctr: exit = %d\nstderr: %s", code, errText)
	}
	if code, _, errText := runIn([]string{"transition", "ctr:wave-1", "completed", "--force"}); code != 0 {
		t.Fatalf("complete ctr: exit = %d\nstderr: %s", code, errText)
	}

	code, _, errText := runIn([]string{"unrelate", "tkt:ticket-1", "sto:item-1", "--force"})
	if code != 1 {
		t.Fatalf("exit = %d, want 1\nstderr: %s", code, errText)
	}
	if !strings.Contains(errText, "history-locked") {
		t.Errorf("stderr = %q, want the history-locked refusal", errText)
	}
	if rels := ticketRels(t, "acme/tkt:ticket-1"); len(rels) != 2 {
		t.Errorf("relationships = %+v, want both edges intact", rels)
	}
}

// TestUnrelateActiveContainerScopeLocked: unlinking from an active
// container refuses while the ticket is live (scope locked) — the hint
// names the retire-then-unlink flow.
func TestUnrelateActiveContainerScopeLocked(t *testing.T) {
	ticketFixtureEnv(t)
	if code, _, errText := runIn([]string{"transition", "ctr:wave-1", "active", "--force"}); code != 0 {
		t.Fatalf("activate ctr: exit = %d\nstderr: %s", code, errText)
	}

	code, _, errText := runIn([]string{"unrelate", "tkt:ticket-1", "sto:item-1", "--force"})
	if code != 1 {
		t.Fatalf("exit = %d, want 1\nstderr: %s", code, errText)
	}
	if !strings.Contains(errText, "scope is locked") || !strings.Contains(errText, "eka retire") {
		t.Errorf("stderr = %q, want the scope-locked refusal with the retire hint", errText)
	}
}

// TestUnrelateRetiredTicketInActiveContainer: the audited
// retire-then-unlink flow — a retired ticket MAY unlink from an
// active container (the withdrawal was already recorded with a
// reason).
func TestUnrelateRetiredTicketInActiveContainer(t *testing.T) {
	ticketFixtureEnv(t)
	if code, _, errText := runIn([]string{"transition", "ctr:wave-1", "active", "--force"}); code != 0 {
		t.Fatalf("activate ctr: exit = %d\nstderr: %s", code, errText)
	}
	if code, _, errText := runIn([]string{"retire", "tkt:ticket-1", "--reason", "item dropped from the wave", "--force"}); code != 0 {
		t.Fatalf("retire tkt: exit = %d\nstderr: %s", code, errText)
	}

	code, out, errText := runIn([]string{"unrelate", "tkt:ticket-1", "sto:item-1", "--force"})
	if code != 0 {
		t.Fatalf("unrelate retired ticket: exit = %d, want 0\nstdout: %s\nstderr: %s", code, out, errText)
	}
	if rels := ticketRels(t, "acme/tkt:ticket-1"); len(rels) != 1 {
		t.Fatalf("relationships = %+v, want exactly the surviving ctr edge", rels)
	}
}

// TestUnrelateEdgeNotPresent: removing an edge the ticket does not
// carry is a deterministic refusal (exit 1) listing the current
// derives-from edges — fail-closed against typos.
func TestUnrelateEdgeNotPresent(t *testing.T) {
	w := ticketFixtureEnv(t)
	payloadsBefore := payloadCount(t, w)

	code, _, errText := runIn([]string{"unrelate", "tkt:ticket-1", "sto:ghost", "--force"})
	if code != 1 {
		t.Fatalf("exit = %d, want 1\nstderr: %s", code, errText)
	}
	if !strings.Contains(errText, "carries no derives-from edge") || !strings.Contains(errText, "ctr:wave-1") {
		t.Errorf("stderr = %q, want the edge-not-present refusal with the current edges", errText)
	}
	if got := payloadCount(t, w); got != payloadsBefore {
		t.Errorf("a refused unrelate must not write, got %d -> %d", payloadsBefore, got)
	}
}

// TestUnrelateNonTicketRefused: unrelate applies to tickets only.
func TestUnrelateNonTicketRefused(t *testing.T) {
	ticketFixtureEnv(t)
	code, _, errText := runIn([]string{"unrelate", "sto:item-1", "ctr:wave-1", "--force"})
	if code != 1 {
		t.Fatalf("exit = %d, want 1\nstderr: %s", code, errText)
	}
	if !strings.Contains(errText, "not a ticket") {
		t.Errorf("stderr = %q, want the non-ticket refusal", errText)
	}
}

// TestUnrelateNonTTYWithoutForce: runIn uses a non-terminal stdin —
// without --force the unlink is a usage error (exit 2), the discard
// mirror. --force never bypasses the other gates (covered by the R8
// test shape: R8 refuses even with --force).
func TestUnrelateNonTTYWithoutForce(t *testing.T) {
	ticketFixtureEnv(t)
	code, _, errText := runIn([]string{"unrelate", "tkt:ticket-1", "sto:item-1"})
	if code != 2 {
		t.Fatalf("exit = %d, want 2\nstderr: %s", code, errText)
	}
	if !strings.Contains(errText, "--force") {
		t.Errorf("stderr = %q, want the --force requirement", errText)
	}
}

// TestUnrelateBareIDUsageError: a bare-id edge target cannot
// disambiguate the membership — usage error (exit 2).
func TestUnrelateBareIDUsageError(t *testing.T) {
	ticketFixtureEnv(t)
	code, _, errText := runIn([]string{"unrelate", "tkt:ticket-1", "item-1", "--force"})
	if code != 2 {
		t.Fatalf("exit = %d, want 2\nstderr: %s", code, errText)
	}
	if !strings.Contains(errText, "<type>:<id>") {
		t.Errorf("stderr = %q, want the typed-target hint", errText)
	}
}

// TestUnrelateHelpDistinguishesUnlinkVsRetire: the unlink vs retire
// distinction is discoverable in the help texts (Opsi A bagian 2 §3).
func TestUnrelateHelpDistinguishesUnlinkVsRetire(t *testing.T) {
	for _, args := range [][]string{{"unrelate", "--help"}, {"relate", "--help"}, {"retire", "--help"}} {
		code, text, _ := runIn(args)
		if code != 0 {
			t.Fatalf("%v --help: exit = %d, want 0", args, code)
		}
		if !strings.Contains(text, "unlink") || !strings.Contains(text, "retire") {
			t.Errorf("%v --help must distinguish unlink vs retire:\n%s", args, text)
		}
	}
	code, text, _ := runIn([]string{"unrelate", "--help"})
	if code != 0 {
		t.Fatalf("unrelate --help: exit = %d, want 0", code)
	}
	if !strings.Contains(text, "eka unrelate") || !strings.Contains(text, "--force") {
		t.Errorf("unrelate --help missing usage/flags:\n%s", text)
	}
}

// TestUnrelateJSONContract: --json emits the eka-unrelate-v1 machine
// report (stdout carries ONLY the document).
func TestUnrelateJSONContract(t *testing.T) {
	ticketFixtureEnv(t)
	code, out, errText := runIn([]string{"unrelate", "tkt:ticket-1", "sto:item-1", "--force", "--json"})
	if code != 0 {
		t.Fatalf("unrelate --json: exit = %d\nstdout: %s\nstderr: %s", code, out, errText)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("unrelate --json stdout is not valid JSON: %v\n%s", err, out)
	}
	if doc["schema"] != unrelateSchema {
		t.Errorf("schema = %v, want %q", doc["schema"], unrelateSchema)
	}
	if doc["target"] != "acme/tkt:ticket-1" || doc["state"] != "published" {
		t.Errorf("target/state = %v/%v, want acme/tkt:ticket-1/published", doc["target"], doc["state"])
	}
	removed, ok := doc["removed"].([]any)
	if !ok || len(removed) != 1 || removed[0] != "sto:item-1" {
		t.Errorf("removed = %v, want [sto:item-1]", doc["removed"])
	}
	hash, ok := doc["objectHash"].(string)
	if !ok || len(hash) != 64 {
		t.Errorf("objectHash = %v, want the 64-hex digest of the new payload", doc["objectHash"])
	}
}
