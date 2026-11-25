package cmd

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/maleolabs/eka-core/exchange"
	"github.com/maleolabs/eka-core/workspace"
)

// This file tests `eka retire tkt:<id>` at CLI level (Opsi A bagian 2):
// the zero-owned-state ticket branch — the retirement-marker instance
// (highest+1, edges/state/change-log frozen), the container gates
// (planned vs active vs completed), the work-item downstream-blocker
// rule (active tickets block, retired tickets do not), the idempotent
// already-retired path, and the dry-run membership + gateImpact
// contract.

// ticketHighest returns the current (highest) instance of a ticket
// line from the workspace store.
func ticketHighest(t *testing.T, w *workspace.Workspace, ns, id string) *exchange.Unit {
	t.Helper()
	units, err := w.Store().UnitsByLine(ns, "tkt", id)
	if err != nil {
		t.Fatalf("UnitsByLine(%s, tkt, %s): %v", ns, id, err)
	}
	var best *exchange.Unit
	for _, u := range units {
		if best == nil || u.Identity.InstanceVersion > best.Identity.InstanceVersion {
			best = u
		}
	}
	if best == nil {
		t.Fatalf("ticket %s/tkt:%s has no published instance", ns, id)
	}
	return best
}

// ticketContentMarker decodes the retirement marker of a ticket unit
// (nil when absent).
func ticketContentMarker(t *testing.T, u *exchange.Unit) map[string]any {
	t.Helper()
	var content map[string]any
	if err := json.Unmarshal(u.ContentPayload, &content); err != nil {
		t.Fatalf("ticket content is not valid JSON: %v", err)
	}
	marker, _ := content["retirement"].(map[string]any)
	return marker
}

// TestRetireTicketHappyPath: retiring a ticket in a planned container
// publishes v2 with the retirement marker, frozen edges/state and no
// change-log entry; the projection still derives from the work item;
// integrity stays green.
func TestRetireTicketHappyPath(t *testing.T) {
	w := ticketFixtureEnv(t)

	code, out, errText := runIn([]string{"retire", "tkt:ticket-1", "--reason", "item dropped from planning", "--force", "--by", "test-bot"})
	if code != 0 {
		t.Fatalf("retire tkt: exit = %d\nstdout: %s\nstderr: %s", code, out, errText)
	}
	if !strings.Contains(out, "retired") {
		t.Errorf("retire output must report the retirement:\n%s", out)
	}

	v1units, err := w.Store().UnitsByLine("acme", "tkt", "ticket-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(v1units) != 2 {
		t.Fatalf("retire must publish a new instance: got %d instances, want 2", len(v1units))
	}
	var v1, v2 *exchange.Unit
	for _, u := range v1units {
		switch u.Identity.InstanceVersion {
		case 1:
			v1 = u
		case 2:
			v2 = u
		}
	}
	if v1 == nil || v2 == nil {
		t.Fatalf("instances = v1:%v v2:%v, want both", v1 != nil, v2 != nil)
	}
	// The old instance is never mutated.
	if ticketContentMarker(t, v1) != nil {
		t.Errorf("v1 gained a retirement marker (old instances are immutable)")
	}
	// Edges frozen: both derives-from edges survive.
	if len(v2.Relationships) != 2 {
		t.Errorf("v2 relationships = %+v, want the 2 frozen derives-from edges", v2.Relationships)
	}
	// Zero owned state: no state vector, no change-log delta.
	if v2.StateVector != (exchange.StateVector{}) {
		t.Errorf("v2 state vector = %+v, want the frozen zero vector (R4)", v2.StateVector)
	}
	if len(v2.ChangeLog) != len(v1.ChangeLog) {
		t.Errorf("v2 change-log grew %d -> %d, want frozen (R7: tickets own no change-log domain)", len(v1.ChangeLog), len(v2.ChangeLog))
	}
	// The marker: date/by/reason recorded in content.
	marker := ticketContentMarker(t, v2)
	if marker == nil {
		t.Fatalf("v2 carries no retirement marker")
	}
	if marker["by"] != "test-bot" || marker["reason"] != "item dropped from planning" {
		t.Errorf("retirement marker = %v, want by test-bot + the reason", marker)
	}
	if marker["date"] == "" {
		t.Errorf("retirement marker must carry a date: %v", marker)
	}

	// The projection still derives from the work item (planned) — retire
	// changes membership bookkeeping, never the projected status.
	code, out, errText = runIn([]string{"get", "ticket", "tkt:ticket-1"})
	if code != 0 {
		t.Fatalf("get ticket: exit = %d\nstderr: %s", code, errText)
	}
	if !strings.Contains(out, `"projected": "planned"`) {
		t.Errorf("retired ticket must still project the work-item state (planned):\n%s", out)
	}

	// Integrity stays green: the marker instance is a tombstone
	// publish, not a deletion.
	if code, out, errText := runIn([]string{"integrity", "check"}); code != 0 {
		t.Errorf("integrity check after ticket retire: exit = %d, want 0\nstdout: %s\nstderr: %s", code, out, errText)
	}
}

// TestRetireTicketGateMatrix unit-tests the fail-closed container
// gate directly (pure function — the default-refuse without --force
// is only reachable on a terminal, where runIn cannot go):
// planned allows; active + pending refuses by default but allows with
// force; active + done/canceled allows; completed refuses absolutely
// (even with force); broken membership refuses.
func TestRetireTicketGateMatrix(t *testing.T) {
	line, ctr, item := "acme/tkt:ticket-1", "acme/ctr:wave-1", "acme/sto:item-1"
	cases := []struct {
		name           string
		containerLine  string
		containerState string
		workItemLine   string
		workItemState  string
		force          bool
		wantRefuse     bool
		wantHint       string
	}{
		{"planned allows", ctr, "planned", item, "todo", false, false, ""},
		{"active pending refuses by default", ctr, "active", item, "todo", false, true, "transition acme/sto:item-1 to done"},
		{"active pending allows with force", ctr, "active", item, "in-progress", true, false, ""},
		{"active done allows", ctr, "active", item, "done", false, false, ""},
		{"active canceled allows", ctr, "active", item, "canceled", false, false, ""},
		{"active work-item-less allows", ctr, "active", "", "", false, false, ""},
		{"completed refuses absolutely", ctr, "completed", item, "todo", true, true, "history-locked"},
		{"broken membership refuses", "", "", item, "todo", true, true, "integrity check"},
	}
	for _, tc := range cases {
		refusal := ticketRetireGate(line, tc.containerLine, tc.containerState, tc.workItemLine, tc.workItemState, tc.force)
		if !tc.wantRefuse && refusal != nil {
			t.Errorf("%s: unexpected refusal: %s", tc.name, refusal.reason)
		}
		if tc.wantRefuse {
			if refusal == nil {
				t.Errorf("%s: want refusal, got allow", tc.name)
				continue
			}
			if !strings.Contains(refusal.reason+refusal.hint, tc.wantHint) {
				t.Errorf("%s: refusal %q; want hint containing %q", tc.name, refusal.reason+"; "+refusal.hint, tc.wantHint)
			}
		}
	}
}

// TestRetireTicketActiveContainerOverride: end-to-end — an active
// container with a pending work item retires with --force (the reason
// is recorded in the marker); the completed twin refuses absolutely.
func TestRetireTicketActiveContainerOverride(t *testing.T) {
	w := ticketFixtureEnv(t)
	if code, _, errText := runIn([]string{"transition", "ctr:wave-1", "active", "--force"}); code != 0 {
		t.Fatalf("activate ctr: exit = %d\nstderr: %s", code, errText)
	}
	code, _, errText := runIn([]string{"retire", "tkt:ticket-1", "--reason", "item dropped from the wave", "--force", "--by", "test-bot"})
	if code != 0 {
		t.Fatalf("retire with --force override: exit = %d, want 0\nstderr: %s", code, errText)
	}
	marker := ticketContentMarker(t, ticketHighest(t, w, "acme", "ticket-1"))
	if marker == nil || marker["reason"] != "item dropped from the wave" {
		t.Errorf("marker = %v, want the recorded reason", marker)
	}

	// The completed twin: cancel the item, complete the container,
	// retire refuses history-locked (no override exists).
	ticketFixtureEnv(t)
	if code, _, errText := runIn([]string{"transition", "sto:item-1", "canceled", "--force"}); code != 0 {
		t.Fatalf("cancel sto: exit = %d\nstderr: %s", code, errText)
	}
	if code, _, errText := runIn([]string{"transition", "ctr:wave-1", "active", "--force"}); code != 0 {
		t.Fatalf("activate ctr: exit = %d\nstderr: %s", code, errText)
	}
	if code, _, errText := runIn([]string{"transition", "ctr:wave-1", "completed", "--force"}); code != 0 {
		t.Fatalf("complete ctr: exit = %d\nstderr: %s", code, errText)
	}
	code, _, errText = runIn([]string{"retire", "tkt:ticket-1", "--reason", "withdrawing from history", "--force", "--by", "test-bot"})
	if code != 1 {
		t.Fatalf("retire in completed container: exit = %d, want 1\nstderr: %s", code, errText)
	}
	if !strings.Contains(errText, "history-locked") {
		t.Errorf("stderr = %q, want the history-locked refusal", errText)
	}
}

// TestRetireTicketDryRunGateImpact: the dry-run prints the membership
// {container, workItem, containerState} and the gate impact
// {allDoneBlockedBy, completable} — and writes nothing. The retired
// ticket's own work item stays listed: retire never auto-excludes.
func TestRetireTicketDryRunGateImpact(t *testing.T) {
	w := ticketFixtureEnv(t)

	code, out, errText := runIn([]string{"retire", "tkt:ticket-1", "--reason", "item dropped from planning", "--force", "--by", "test-bot", "--dry-run"})
	if code != 0 {
		t.Fatalf("dry-run: exit = %d\nstdout: %s\nstderr: %s", code, out, errText)
	}
	for _, want := range []string{
		"acme/ctr:wave-1", "planned", "acme/sto:item-1",
		"still blocked by", "sto:item-1 (planned)",
		"Dry-run: no changes were written.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("dry-run output must contain %q:\n%s", want, out)
		}
	}
	units, err := w.Store().UnitsByLine("acme", "tkt", "ticket-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(units) != 1 {
		t.Errorf("dry-run wrote an instance: %d instances, want 1", len(units))
	}

	code, out, _ = runIn([]string{"retire", "tkt:ticket-1", "--reason", "item dropped from planning", "--force", "--by", "test-bot", "--dry-run", "--json"})
	if code != 0 {
		t.Fatalf("dry-run --json: exit = %d, want 0", code)
	}
	doc := retireJSONDoc(t, out)
	if doc["status"] != "dry-run" {
		t.Errorf("status = %v, want dry-run", doc["status"])
	}
	membership, ok := doc["membership"].(map[string]any)
	if !ok || membership["container"] != "acme/ctr:wave-1" || membership["workItem"] != "acme/sto:item-1" || membership["containerState"] != "planned" {
		t.Errorf("membership = %v, want {acme/ctr:wave-1 acme/sto:item-1 planned}", doc["membership"])
	}
	gate, ok := doc["gateImpact"].(map[string]any)
	if !ok {
		t.Fatalf("gateImpact missing: %v", doc)
	}
	blocked, ok := gate["allDoneBlockedBy"].([]any)
	if !ok || len(blocked) != 1 || blocked[0] != "sto:item-1 (planned)" {
		t.Errorf("allDoneBlockedBy = %v, want [sto:item-1 (planned)] (retired-never-auto-excluded)", gate["allDoneBlockedBy"])
	}
	if gate["completable"] != false {
		t.Errorf("completable = %v, want false", gate["completable"])
	}
	if _, ok := doc["existenceState"]; ok {
		t.Errorf("ticket retire must not carry existenceState (zero owned state): %v", doc)
	}
}

// TestRetireWorkItemBlockedByActiveTicket: retiring a work item still
// referenced by an ACTIVE ticket is a downstream-blocker refusal
// (never orphan a live ticket). After the ticket itself retires, the
// work-item retire proceeds (retired tickets do not block).
func TestRetireWorkItemBlockedByActiveTicket(t *testing.T) {
	ticketFixtureEnv(t)

	code, _, errText := runIn([]string{"retire", "sto:item-1", "--reason", "withdrawing the story now", "--force", "--by", "test-bot"})
	if code != 1 {
		t.Fatalf("retire referenced work item: exit = %d, want 1\nstderr: %s", code, errText)
	}
	if !strings.Contains(errText, "acme/tkt:ticket-1") {
		t.Errorf("stderr = %q, want the active-ticket blocker listed", errText)
	}

	// Retire the ticket first (planned container: allowed), then the
	// work item retire proceeds.
	if code, _, errText := runIn([]string{"retire", "tkt:ticket-1", "--reason", "item dropped from planning", "--force", "--by", "test-bot"}); code != 0 {
		t.Fatalf("retire tkt: exit = %d\nstderr: %s", code, errText)
	}
	code, out, errText := runIn([]string{"retire", "sto:item-1", "--reason", "withdrawing the story now", "--force", "--by", "test-bot"})
	if code != 0 {
		t.Fatalf("retire work item after ticket retire: exit = %d, want 0\nstdout: %s\nstderr: %s", code, out, errText)
	}
}

// TestRetireTicketAlreadyRetired: the second retire is an idempotent
// no-op (exit 0, no new instance).
func TestRetireTicketAlreadyRetired(t *testing.T) {
	w := ticketFixtureEnv(t)
	args := []string{"retire", "tkt:ticket-1", "--reason", "item dropped from planning", "--force", "--by", "test-bot"}
	if code, _, errText := runIn(args); code != 0 {
		t.Fatalf("first retire: exit = %d\nstderr: %s", code, errText)
	}
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
	units, err := w.Store().UnitsByLine("acme", "tkt", "ticket-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(units) != 2 {
		t.Errorf("already-retired wrote an instance: %d instances, want 2", len(units))
	}
}
