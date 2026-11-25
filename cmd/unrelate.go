package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/maleolabs/eka-cli/cmd/ui"
	"github.com/maleolabs/eka-core/conformance"
	"github.com/maleolabs/eka-core/exchange"
	"github.com/maleolabs/eka-core/metadata"
	"github.com/maleolabs/eka-core/runtime"
	"github.com/maleolabs/eka-core/store"
	"github.com/maleolabs/eka-core/view"
	"github.com/maleolabs/eka-core/workspace"
	"github.com/spf13/cobra"
)

// This file implements `eka unrelate <line> <target>`: the
// relationship-only edge-REMOVEYAL of a ticket (tkt-) line — the inverse
// of `eka relate` for the ticket's derives-from edges. Like relate (and
// the assignment edge writes of cmd/assigned.go) the published path
// re-points the line's current instance to a new immutable payload with
// the SAME instance version and revision (no instance churn); the draft
// path rewrites the pending JSON draft's relationships block in place.
//
// UX decision (Opsi A bagian 2): a separate command, not
// `eka relate --remove`. Relate's contract is edge-add only (a relate
// with no relationship flags is a usage error, duplicates are an
// idempotent no-op); bolting an inverted --remove flag onto it would
// tangle two opposite semantics in one invocation. The assignment
// precedent (assign/reassign/unassign are separate commands) applies:
// removal gets its own verb. `eka unrelate` is ticket-scoped (tkt-
// lines only — the only artifact whose relationships are exclusively
// managed derives-from edges); other types keep their dedicated
// commands (`eka unassign` for assigned-to).
//
// Unlink vs retire (the distinction the help texts pin):
//
//	unlink (`eka unrelate`)  removes one derives-from edge from the
//	                         ticket line — membership changes, the line
//	                         stays live. Allowed only while the owning
//	                         container is planned, or — for an active
//	                         container — after the ticket was retired
//	                         (the audited retire-then-unlink flow).
//	                         Completed containers are history-locked:
//	                         unlink always refuses.
//	retire (`eka retire`)    withdraws the ticket WITHOUT removing
//	                         edges — a new instance carrying the
//	                         retirement marker. The retired ticket still
//	                         blocks the container all-done gate until it
//	                         is explicitly unlinked (fail-closed).
//
// Model facts honored here (tkt- = Execution projection):
//
//   - zero owned state: no state field may change (R4) and no change-log
//     entry may be appended (R7 — every change-log domain must be
//     owned). The unlink therefore writes NO change-log. Audit is the
//     confirmation gate instead: on a terminal an interactive
//     confirmation, outside one --force is required (the discard
//     mirror) — plus the deterministic output (removed edge, unchanged
//     instance version, new object hash).
//   - R8: after the removal at least one derives-from reference to a
//     RESOLVING container (ctr-) must remain, else the unlink is
//     refused (exit 1) and nothing is written.
//   - no transition: tickets have no state to transition (projected =
//     workItem.execution-state); `eka transition tkt-` stays refused.
//
// Exit codes (the transition/note contract):
//
//	0  unlinked (published, draft-mutated), or cancelled at the
//	   confirmation prompt
//	1  refusal (missing artifact, non-ticket target, edge not present,
//	   R8 violation, non-planned container scope, legacy Markdown
//	   draft, CKO-level validation findings)
//	2  usage or internal error (invalid target, canonical published
//	   form, bare-id edge target, non-TTY without --force, workspace
//	   errors)
//
// --json emits the deterministic machine report (schema
// "eka-unrelate-v1"); the default output is the human report in the
// CLI house style.

// unrelateSchema is the schema id of the unrelate machine report.
const unrelateSchema = "eka-unrelate-v1"

// unrelateJSON is the deterministic machine report of one unrelate run
// (schema "eka-unrelate-v1"; pinned field order).
type unrelateJSON struct {
	Schema string `json:"schema"`
	OK     bool   `json:"ok"`
	Target string `json:"target,omitempty"`
	// Removed lists the derives-from edges actually removed, in stored
	// order (empty on unchanged/cancelled).
	Removed []string `json:"removed,omitempty"`
	State   string   `json:"state,omitempty"`
	// ObjectHash is the content-derived hash of the new immutable
	// payload the reference now points at (published path only).
	ObjectHash string `json:"objectHash,omitempty"`
	Reason     string `json:"reason,omitempty"`
	Hint       string `json:"hint,omitempty"`
}

// unrelateRefusal is a deterministic refusal (exit 1) of the unrelate
// command: reason + hint, nothing was written.
type unrelateRefusal struct {
	reason string
	hint   string
}

// Error renders the deterministic refusal message.
func (e *unrelateRefusal) Error() string {
	return fmt.Sprintf("%s; %s", e.reason, e.hint)
}

// newUnrelateCommand builds `eka unrelate <line> <target>`.
func newUnrelateCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "unrelate <line> <target>",
		Short: "Remove one derives-from edge from a ticket (unlink membership)",
		Long: `Remove one derives-from edge from a ticket (tkt-) line WITHOUT a
full re-publish — no new instance version (no instance churn).

The line is the ticket: <type>:<id> (unqualified — the repository
namespace applies) or <ns>/<type>:<id> (qualified — the namespace
must equal the repository's). Only tickets (tkt-) are unrelatable:
their relationships are exclusively derives-from edges (the 2-edge
convention: ctr:<wave> + work-item sto/ts/bug/...). Any other type is
refused — assignments go through 'eka unassign', everything else
through its own command.

The target is the edge to remove: <type>:<id> or <ns>/<type>:<id>
(a bare id is refused — the edge type disambiguates the membership).
Every derives-from edge resolving to that (namespace, type, id) line
is removed (normally exactly one). An edge that is not present is
refused with the ticket's current derives-from edges listed
(fail-closed against typos — removal is not a silent no-op).

What happens depends on the line's state (the relate mechanism,
inverted):

  published  the line's current instance is re-pointed to a new
             immutable payload carrying the surviving edges. The
             instance version and revision stay UNCHANGED — the payload
             archive gains one row (immutability: history accumulates),
             but the artifact line does not advance. The old payload
             stays in the history (prev_hash lineage).
  draft      the line has no published instance yet, but a pending
             draft exists: the edge is removed from the draft file in
             place, then the draft is re-validated at CKO level
             (findings are reported, never destructive). Legacy
             Markdown drafts are refused.

Gates (all refusals write nothing):

  R8         after the removal at least one derives-from reference to
             a RESOLVING container (ctr-) must remain — a ticket that
             would lose its last resolving container is refused.
  scope      every resolving container the ticket derives from must be
             planned (membership is fluid while planning). A completed
             container is history-locked: unlink always refuses. An
             active container locks its scope: unlink refuses UNLESS
             the ticket was already retired ('eka retire tkt:<id>
             --reason "..."' first — the audited retire-then-unlink
             flow for dropping a ticket from a running wave).
             (A container's plan turns immutable atomically at
             activation, so plan-immutability needs no separate check:
             the container-state gate covers planned vs active vs
             completed.)

Unlink vs retire: unlink EDITS membership (the edge is gone, the line
stays live); retire WITHDRAWS the ticket (a new instance with the
retirement marker, edges intact — a retired ticket still blocks the
container all-done gate until it is explicitly unlinked). To drop a
ticket from a wave: retire it first (reason recorded), then unlink.

Tickets carry zero owned state, so unlink writes NO change-log entry
(R7: every change-log domain must be owned — relate writes none for
the same reason). Audit is the confirmation gate: on a terminal the
run asks for confirmation; outside one --force is required (mirror of
the 'eka discard' gate — it never bypasses any other gate).

Exit codes:
  0  unlinked (published or draft-mutated), or cancelled at the
     confirmation prompt
  1  refused (missing artifact, non-ticket target, edge not present,
     R8 violation, non-planned container scope, Markdown draft,
     validation findings on the published path)
  2  usage or internal error (invalid target, canonical published
     form, bare-id edge target, non-TTY without --force)`,
		Example: `  eka unrelate tkt:wave-7-item-1 sto:my-item --force
  eka unrelate tkt:wave-7-item-1 ctr:wave-6 --force --json`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runUnrelate(cmd, args[0], args[1])
		},
	}
	cmd.Flags().Bool("force", false, "required outside a terminal; it never bypasses any unrelate gate")
	cmd.Flags().Bool("json", false, "emit the deterministic machine report (schema eka-unrelate-v1)")
	return cmd
}

// runUnrelate executes one unrelate run: target resolution, the ticket
// gate, the edge match, the R8 + scope gates, the confirmation gate,
// then the published re-point or the draft rewrite.
func runUnrelate(cmd *cobra.Command, line, edge string) error {
	force, _ := cmd.Flags().GetBool("force")
	jsonOut, _ := cmd.Flags().GetBool("json")

	// The line-form gate (exit 2, usage): a canonical published form
	// addresses an immutable instance, and unrelate addresses the line.
	lineRef, err := conformance.ParseReference(line, "", "")
	if err != nil {
		return unrelateUsage(cmd, jsonOut, fmt.Sprintf("invalid ticket line %q: %v", line, err))
	}
	if lineRef.HasVersion {
		return unrelateUsage(cmd, jsonOut, fmt.Sprintf("%s is a canonical published form; unrelate addresses the ticket line", line))
	}
	// The edge target is typed-only (fail-closed against bare ids: a
	// bare id cannot disambiguate the ctr- vs work-item membership).
	if !strings.Contains(edge, ":") {
		return unrelateUsage(cmd, jsonOut, fmt.Sprintf("invalid edge target %q: pass <type>:<id> or <ns>/<type>:<id> (a bare id cannot disambiguate the membership)", edge))
	}
	edgeRef, err := conformance.ParseReference(edge, "", "")
	if err != nil {
		return unrelateUsage(cmd, jsonOut, fmt.Sprintf("invalid edge target %q: %v", edge, err))
	}
	if edgeRef.HasVersion {
		return unrelateUsage(cmd, jsonOut, fmt.Sprintf("%s is a versioned reference; unrelate matches the edge line, never one immutable instance", edge))
	}

	r, err := openAuthoringRuntime(cmd)
	if err != nil {
		return err // Exit 2: workspace resolution.
	}
	defer r.Close()

	ctx, err := resolveUnrelateTarget(r, lineRef)
	if err != nil {
		var refusal *unrelateRefusal
		if errors.As(err, &refusal) {
			return unrelateRefused(cmd, jsonOut, "", refusal.reason, refusal.hint)
		}
		return unrelateUsage(cmd, jsonOut, err.Error())
	}
	if edgeRef.Namespace == "" {
		edgeRef.Namespace = ctx.ref.Namespace
	}

	// The ticket's current derives-from edges: from the published
	// instance, or from the pending draft's relationships block when
	// the line has no published instance.
	var stored []exchange.Relationship
	var draftPath string
	if ctx.unit != nil {
		for _, rel := range ctx.unit.Relationships {
			if rel.Type == "derives-from" {
				stored = append(stored, rel)
			}
		}
	} else {
		draftPath = ctx.draftPath
		rels, derr := draftDerivesFromTargets(draftPath)
		if derr != nil {
			return fmt.Errorf("unrelate: %w", derr) // Exit 2: internal.
		}
		for _, raw := range rels {
			stored = append(stored, exchange.Relationship{Type: "derives-from", Target: raw})
		}
	}

	// The edge match: every derives-from edge resolving to the
	// requested (namespace, type, id) line.
	var matched, survivors []exchange.Relationship
	for _, rel := range stored {
		parsed, perr := conformance.ParseReference(rel.Target, ctx.ref.Namespace, ctx.ref.Type)
		if perr != nil {
			survivors = append(survivors, rel) // Malformed stored edges are R5's business; unlink never drops them silently.
			continue
		}
		if parsed.Namespace == edgeRef.Namespace && parsed.Type == edgeRef.Type && parsed.ID == edgeRef.ID {
			matched = append(matched, rel)
		} else {
			survivors = append(survivors, rel)
		}
	}
	if len(matched) == 0 {
		return unrelateRefused(cmd, jsonOut, ctx.form,
			fmt.Sprintf("ticket %s carries no derives-from edge to %s", ctx.form, view.LineForm(edgeRef.Namespace, edgeRef.Type, edgeRef.ID)),
			fmt.Sprintf("current derives-from edges: %s", unrelateEdgeList(stored)))
	}

	byLine := highestByLine(ctx.units)
	resolve := func(ref conformance.Reference) *exchange.Unit {
		return byLine[view.LineForm(ref.Namespace, ref.Type, ref.ID)]
	}

	// The R8 gate: the survivors must keep at least one derives-from
	// reference to a RESOLVING container (the ticket rule — a ticket
	// without a resolving ctr- is not a ticket). The published-path
	// ValidateCKO re-checks this; the explicit gate names the blocker
	// deterministically before anything is written.
	if !survivorsResolveCtr(survivors, ctx.ref.Namespace, ctx.ref.Type, resolve) {
		return unrelateRefused(cmd, jsonOut, ctx.form,
			fmt.Sprintf("cannot unlink %s from %s: the ticket would lose its last resolving container (ctr-)", view.LineForm(edgeRef.Namespace, edgeRef.Type, edgeRef.ID), ctx.form),
			"a ticket must derive from at least one resolving container (R8); unlink the ticket only after re-pointing it, or retire it instead")
	}

	// The scope gate: every RESOLVING container the ticket derives
	// from (pre-removal set — history/scope is about the current
	// membership) must allow the edit. Completed is history-locked
	// (absolute); active locks its scope unless the ticket was already
	// retired (the audited retire-then-unlink flow).
	retired := ctx.unit != nil && ticketRetired(ctx.unit)
	for _, ctr := range resolvingCtrs(stored, ctx.ref.Namespace, ctx.ref.Type, resolve) {
		state := ctr.StateVector.ContainerState
		switch state {
		case "completed":
			return unrelateRefused(cmd, jsonOut, ctx.form,
				fmt.Sprintf("container %s is completed; its membership is history-locked", view.LineForm(ctr.Identity.Namespace, ctr.Identity.Type, ctr.Identity.ID)),
				"completed containers are immutable history — unlink is refused")
		case "active":
			if !retired {
				return unrelateRefused(cmd, jsonOut, ctx.form,
					fmt.Sprintf("container %s is active; its scope is locked while running", view.LineForm(ctr.Identity.Namespace, ctr.Identity.Type, ctr.Identity.ID)),
					fmt.Sprintf("retire the ticket first ('eka retire %s:<id> --reason \"...\" --force'), then unlink; or transition the work item to done/canceled", ctx.ref.Type))
			}
		}
	}

	// The confirmation gate (the unlink audit — tickets carry no
	// change-log, so no --by/authority is recorded; the confirmation
	// IS the audit): outside a terminal --force is required (the
	// discard mirror, exit 2); on a terminal the run asks.
	if !force {
		s := styleFor(cmd)
		if !isTTYReader(cmd.InOrStdin()) || !s.TTY {
			return unrelateUsage(cmd, jsonOut, "unrelate requires --force when not running in a terminal")
		}
		value, serr := ui.Select(s, cmd.InOrStdin(), cmd.OutOrStdout(),
			fmt.Sprintf("Remove %d derives-from edge(s) from %s?", len(matched), ctx.form),
			[]ui.MenuItem{
				{Title: "Remove the edge(s)", Value: "remove"},
				{Title: "Cancel", Value: "cancel"},
			}, 1)
		if serr != nil {
			if errors.Is(serr, ui.ErrCancelled) {
				value = "cancel" // Esc/q/Ctrl-C: deterministic abort
			} else {
				return fmt.Errorf("unrelate: %w", serr)
			}
		}
		if value != "remove" {
			if jsonOut {
				_ = emitJSON(cmd, unrelateJSON{Schema: unrelateSchema, OK: true, Target: ctx.form, State: "cancelled"})
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "eka: unrelate cancelled; no changes made\n")
			return nil
		}
	}

	removed := make([]string, 0, len(matched))
	for _, rel := range matched {
		removed = append(removed, rel.Target)
	}
	if ctx.unit != nil {
		state, hash, werr := writeUnrelatePublished(ctx, survivors)
		if werr != nil {
			return unrelateWriteError(cmd, jsonOut, ctx.form, werr)
		}
		if jsonOut {
			return emitJSON(cmd, unrelateJSON{
				Schema: unrelateSchema, OK: true, Target: ctx.form,
				Removed: removed, State: state, ObjectHash: hash,
			})
		}
		renderUnrelateResult(styleFor(cmd), ctx.form, removed, state, hash)
		return nil
	}
	if werr := rewriteDraftDerivesFrom(draftPath, survivors); werr != nil {
		return fmt.Errorf("unrelate: %w", werr) // Exit 2: internal.
	}
	rt, rerr := runtime.Ensure()
	if rerr != nil {
		return rerr
	}
	defer rt.Close()
	if _, rerr := runtime.Authoring.ValidateDraft(rt, ctx.ref.Type+":"+ctx.ref.ID, ctx.project); rerr != nil {
		return fmt.Errorf("unrelate: %w", rerr)
	}
	if jsonOut {
		return emitJSON(cmd, unrelateJSON{
			Schema: unrelateSchema, OK: true, Target: ctx.form,
			Removed: removed, State: "draft",
		})
	}
	renderUnrelateResult(styleFor(cmd), ctx.form, removed, "draft", "")
	return nil
}

// unrelateTarget is the resolved context of one unrelate run: the
// ticket line, its repository/project, the project's units and graph,
// and the ticket's current state.
type unrelateTarget struct {
	ref     conformance.Reference // the ticket line (namespace/type/id)
	form    string                // the canonical line form "<ns>/tkt:<id>"
	project string                // the registered project of the repository
	units   []*exchange.Unit      // every unit of the project
	unit    *exchange.Unit        // the ticket's current (highest) instance; nil when only a draft exists
	// draftPath is the pending JSON draft of the ticket ("" when the
	// line has a published instance or no JSON draft).
	draftPath string
	// hasPendingDraft reports that the line carries a pending draft
	// (JSON or legacy Markdown) when it has no published instance.
	hasPendingDraft bool
}

// resolveUnrelateTarget resolves the ticket line and its repository
// context: the tkt- type gate, the repository-context gate (ADR-018),
// the namespace gate (the relate ownership gate), the project units
// and the ticket's current state. Usage-class errors are plain errors
// (exit 2); deterministic refusals are *unrelateRefusal (exit 1).
func resolveUnrelateTarget(r *runtime.Runtime, lineRef conformance.Reference) (*unrelateTarget, error) {
	if lineRef.Type != "tkt" {
		return nil, &unrelateRefusal{
			reason: fmt.Sprintf("%s is not a ticket; unrelate applies to tickets (tkt-) only", lineRef.Type+":"+lineRef.ID),
			hint:   "use 'eka unassign' to remove an assignment, 'eka retire' to withdraw any other artifact line",
		}
	}
	abs, err := filepath.Abs(".")
	if err != nil {
		return nil, fmt.Errorf("cannot resolve the working directory: %w", err) // Exit 2.
	}
	abs = filepath.Clean(abs)
	meta, _, hasMeta, err := metadata.Find(abs)
	if err != nil {
		return nil, fmt.Errorf("cannot resolve the repository context: %w", err) // Exit 2.
	}
	if !hasMeta {
		return nil, &unrelateRefusal{
			reason: fmt.Sprintf("%s is not an EKA repository (no eka.yaml)", abs),
			hint:   "run 'eka init' first",
		}
	}
	repo, found, err := r.Workspace.FindRepo(abs)
	if err != nil {
		return nil, fmt.Errorf("cannot resolve the repository registration: %w", err) // Exit 2.
	}
	if !found {
		return nil, &unrelateRefusal{
			reason: fmt.Sprintf("repository %s is not registered in the EKA workspace", abs),
			hint:   "run 'eka sync' (auto-registers) or 'eka project register' first",
		}
	}
	ns := meta.Namespace
	if ns == "" {
		ns = repo.Namespace
	}
	ref := lineRef
	if ref.Namespace != "" {
		if ref.Namespace != ns {
			return nil, &unrelateRefusal{
				reason: fmt.Sprintf("target namespace %s differs from the repository namespace %s; cross-platform access is read-only", ref.Namespace, ns),
				hint:   "unrelate only tickets of the repository's own namespace",
			}
		}
	} else {
		ref.Namespace = ns
	}
	units, err := r.Knowledge.UnitsByProject(repo.ProjectID)
	if err != nil {
		return nil, fmt.Errorf("cannot read the project knowledge: %w", err) // Exit 2: store failure.
	}
	ctx := &unrelateTarget{
		ref:     ref,
		form:    view.LineForm(ref.Namespace, ref.Type, ref.ID),
		project: repo.ProjectID,
		units:   units,
		unit:    currentLineUnit(units, ref),
	}
	if ctx.unit == nil {
		root, herr := workspace.HomeDir()
		if herr != nil {
			return nil, fmt.Errorf("cannot resolve the workspace root: %w", herr) // Exit 2.
		}
		ctx.draftPath, ctx.hasPendingDraft, err = pendingDraftPath(root, ctx.project, ref.Type, ref.ID)
		if err != nil {
			return nil, err // Exit 2: draft resolution failure.
		}
		if ctx.draftPath == "" && ctx.hasPendingDraft {
			return nil, &unrelateRefusal{
				reason: fmt.Sprintf("draft %s:%s is a legacy Markdown draft, which unrelate cannot mutate deterministically", ref.Type, ref.ID),
				hint:   "edit the file directly, or migrate the draft to the JSON format ('eka new' scaffolds JSON drafts)",
			}
		}
		if ctx.draftPath == "" {
			return nil, &unrelateRefusal{
				reason: fmt.Sprintf("ticket line %s has no published instance and no pending draft", ctx.form),
				hint:   "run 'eka new tkt:<id> --derives-from ctr:<wave>' to scaffold a draft first",
			}
		}
	}
	return ctx, nil
}

// survivorsResolveCtr reports whether the surviving derives-from edges
// keep at least one reference to a RESOLVING container (the R8 ticket
// rule, pre-write form): the edge must name a ctr- line that resolves
// through the given resolver.
func survivorsResolveCtr(survivors []exchange.Relationship, ns, typ string, resolve func(conformance.Reference) *exchange.Unit) bool {
	for _, rel := range survivors {
		parsed, err := conformance.ParseReference(rel.Target, ns, typ)
		if err != nil {
			continue
		}
		if parsed.Type != "ctr" {
			continue
		}
		if resolve(parsed) == nil {
			continue
		}
		return true
	}
	return false
}

// resolvingCtrs returns the highest instances of every resolving ctr-
// line the given derives-from edges name, in stored order (duplicates
// collapsed).
func resolvingCtrs(edges []exchange.Relationship, ns, typ string, resolve func(conformance.Reference) *exchange.Unit) []*exchange.Unit {
	var out []*exchange.Unit
	seen := map[string]bool{}
	for _, rel := range edges {
		parsed, err := conformance.ParseReference(rel.Target, ns, typ)
		if err != nil || parsed.Type != "ctr" {
			continue
		}
		u := resolve(parsed)
		if u == nil || u.Identity.Type != "ctr" {
			continue
		}
		key := view.LineForm(u.Identity.Namespace, u.Identity.Type, u.Identity.ID)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, u)
	}
	return out
}

// unrelateEdgeList renders the stored derives-from targets for the
// edge-not-present refusal (stored order, "—" when empty).
func unrelateEdgeList(edges []exchange.Relationship) string {
	if len(edges) == 0 {
		return "—"
	}
	targets := make([]string, 0, len(edges))
	for _, rel := range edges {
		targets = append(targets, rel.Target)
	}
	return strings.Join(targets, ", ")
}

// writeUnrelatePublished re-points the ticket line's current instance
// to a new immutable payload carrying exactly the surviving
// derives-from edges, with the SAME canonical form, instance version
// and revision — the exact relate published-path mechanism
// (runtime/relate.go relatePublished, mirrored by
// writeAssignmentPublished): the standard publish validation
// (ValidateCKO with the store resolver — R8 included) and the
// reference re-point via store.RepointUnit. No change-log entry is
// appended: tickets own no state domain (R7), the same reason relate
// writes none. Provenance (project_id, source_repo) is preserved from
// the current reference.
func writeUnrelatePublished(ctx *unrelateTarget, survivors []exchange.Relationship) (string, string, error) {
	ws, err := workspace.Ensure()
	if err != nil {
		return "", "", err // Exit 2: workspace resolution.
	}
	defer ws.Close()
	st := ws.Store()

	line, err := st.UnitsByLine(ctx.ref.Namespace, ctx.ref.Type, ctx.ref.ID)
	if err != nil {
		return "", "", fmt.Errorf("unrelate: %w", err) // Exit 2: store failure.
	}
	var current *exchange.Unit
	for _, u := range line {
		if current == nil || u.Identity.InstanceVersion > current.Identity.InstanceVersion {
			current = u
		}
	}
	if current == nil {
		return "", "", &unrelateRefusal{
			reason: fmt.Sprintf("ticket line %s has no published instance", ctx.form),
			hint:   "run 'eka sync' first, then pass a published tkt:<id> line",
		}
	}
	// The would-be unit: only the derives-from edges change (plus the
	// Updated date, mirroring relate); every other derives-from edge
	// type never appears on tickets, and non-derives-from edges (none
	// by convention) are preserved untouched.
	next := *current
	kept := make([]exchange.Relationship, 0, len(survivors))
	survivorSet := make(map[string]bool, len(survivors))
	for _, rel := range survivors {
		survivorSet[rel.Type+"\x00"+rel.Target] = true
	}
	for _, rel := range current.Relationships {
		if rel.Type == "derives-from" {
			if survivorSet[rel.Type+"\x00"+rel.Target] {
				kept = append(kept, rel)
				delete(survivorSet, rel.Type+"\x00"+rel.Target)
			}
			continue
		}
		kept = append(kept, rel)
	}
	// Survivors parsed from a draft-shaped edge list cannot occur here
	// (the published path always derives survivors from the published
	// instance itself); any leftover is appended deterministically.
	if len(survivorSet) > 0 {
		for _, rel := range survivors {
			if survivorSet[rel.Type+"\x00"+rel.Target] {
				kept = append(kept, rel)
			}
		}
	}
	next.Relationships = kept
	next.Updated = time.Now().Format("2006-01-02")

	resolver := &assignmentStoreResolver{st: st}
	report, err := conformance.ValidateCKO(&next, conformance.ValidateCKOOptions{
		Resolve: resolver.Resolve,
	})
	if err != nil {
		return "", "", fmt.Errorf("unrelate: validation failed: %w", err)
	}
	report.Results = append(report.Results,
		resolver.Findings(next.CanonicalIdentityForm, next.StateVector.ContentState)...)
	if !report.Pass() {
		return "", "", &unrelateValidationError{Target: ctx.form, Report: report}
	}

	curRef, ok, err := st.Ref(current.CanonicalIdentityForm)
	if err != nil {
		return "", "", fmt.Errorf("unrelate: %w", err)
	}
	if !ok {
		return "", "", &unrelateRefusal{
			reason: fmt.Sprintf("the reference of %s is missing (store corruption)", current.CanonicalIdentityForm),
			hint:   "run 'eka integrity check'",
		}
	}
	unitJSON, err := exchange.MarshalUnit(&next)
	if err != nil {
		return "", "", fmt.Errorf("unrelate: cannot serialize %s: %w", next.CanonicalIdentityForm, err)
	}
	hash, _, err := st.RepointUnit(unitJSON, next.ContentPayload, store.Ref{
		Form:            next.CanonicalIdentityForm,
		ProjectID:       curRef.ProjectID,
		SourceRepo:      curRef.SourceRepo,
		Namespace:       next.Identity.Namespace,
		Type:            next.Identity.Type,
		ID:              next.Identity.ID,
		InstanceVersion: next.Identity.InstanceVersion,
		Revision:        next.Revision,
		Dimension:       next.Classification.Dimension,
		Domain:          next.Classification.Domain,
		Phase:           next.Phase,
		UpdatedAt:       next.Updated,
	})
	if err != nil {
		return "", "", fmt.Errorf("unrelate: cannot publish %s: %w", next.CanonicalIdentityForm, err)
	}
	return "published", hash, nil
}

// unrelateValidationError reports that the would-be ticket failed
// CKO-level validation (the standard publish validation, R8
// included); nothing was written.
type unrelateValidationError struct {
	// Target is the line form the unrelate addressed.
	Target string
	// Report is the CKO-level validation report.
	Report *conformance.Report
}

// Error renders the deterministic refusal message.
func (e *unrelateValidationError) Error() string {
	return fmt.Sprintf("%s failed CKO-level validation with %d blocking error(s); nothing was changed",
		e.Target, e.Report.ErrorCount())
}

// draftDerivesFromTargets returns the raw derives-from targets of a
// pending JSON draft file (nil when the file carries none).
func draftDerivesFromTargets(path string) ([]string, error) {
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("cannot read draft %s: %w", path, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("draft %s is not valid JSON: %w", path, err)
	}
	var out []string
	raw, ok := doc["relationships"].(map[string]any)
	if !ok {
		return nil, nil
	}
	targets, ok := raw[conformance.StateKeyCamel("derives-from")].([]any)
	if !ok {
		return nil, nil
	}
	for _, t := range targets {
		if s, ok := t.(string); ok {
			out = append(out, s)
		}
	}
	return out, nil
}

// rewriteDraftDerivesFrom deterministically rewrites a JSON draft's
// relationships block so the derives-from edges equal the surviving
// set — the mirror of rewriteDraftAssignment (runtime/relate.go
// rewriteDraftRelationships): the file is parsed into a generic
// object, the `relationships` key is rebuilt from the surviving edge
// set (camelCase field names, per-field sorted targets), and the file
// is written back as 2-space-indented JSON with a trailing newline.
func rewriteDraftDerivesFrom(path string, survivors []exchange.Relationship) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("cannot read draft %s: %w", path, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("draft %s is not valid JSON: %w", path, err)
	}
	existing := draftRelationshipsOf(doc)
	merged := intersectDerivesFrom(existing, survivors)
	rels := make(map[string][]string)
	for _, field := range conformance.RelationshipFieldNames() {
		var targets []string
		for _, rel := range merged {
			if rel.Type != field {
				continue
			}
			targets = append(targets, rel.Target)
		}
		if len(targets) > 0 {
			sort.Strings(targets)
			rels[conformance.StateKeyCamel(field)] = targets
		}
	}
	if len(rels) > 0 {
		doc["relationships"] = rels
	} else {
		delete(doc, "relationships")
	}
	out, err := json.Marshal(&doc)
	if err != nil {
		return fmt.Errorf("cannot serialize draft %s: %w", path, err)
	}
	var indented bytes.Buffer
	if err := json.Indent(&indented, out, "", "  "); err != nil {
		return fmt.Errorf("cannot serialize draft %s: %w", path, err)
	}
	indented.WriteByte('\n')
	return os.WriteFile(path, indented.Bytes(), 0o644)
}

// intersectDerivesFrom returns the draft's edge set minus the removed
// derives-from edges: every non-derives-from edge is kept, and a
// derives-from edge is kept only when it survives (type+target set
// intersection, stored order preserved).
func intersectDerivesFrom(existing, survivors []exchange.Relationship) []exchange.Relationship {
	survivorSet := make(map[string]bool, len(survivors))
	for _, rel := range survivors {
		survivorSet[rel.Type+"\x00"+rel.Target] = true
	}
	var out []exchange.Relationship
	for _, rel := range existing {
		if rel.Type == "derives-from" && !survivorSet[rel.Type+"\x00"+rel.Target] {
			continue
		}
		out = append(out, rel)
	}
	return out
}

// unrelateWriteError maps an error of the published write path to the
// exit-code contract: refusals and validation failures are exit 1,
// everything else exit 2.
func unrelateWriteError(cmd *cobra.Command, jsonOut bool, target string, err error) error {
	var refusal *unrelateRefusal
	if errors.As(err, &refusal) {
		return unrelateRefused(cmd, jsonOut, target, refusal.reason, refusal.hint)
	}
	var ve *unrelateValidationError
	if errors.As(err, &ve) {
		printCKOReport(styleFor(cmd), ve.Target, ve.Report)
		fmt.Fprintf(cmd.ErrOrStderr(), "eka: unrelate refused: %s\n", err)
		return &exitError{code: exitFail}
	}
	return err // Exit 2: internal.
}

// unrelateUsage renders a usage-class failure (exit 2): the error is
// a deterministic "eka: <error>" line on stderr; --json additionally
// gets the machine refusal document on stdout.
func unrelateUsage(cmd *cobra.Command, jsonOut bool, message string) error {
	if jsonOut {
		_ = emitJSON(cmd, unrelateJSON{Schema: unrelateSchema, OK: false, Reason: message})
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "eka: %s\n", message)
	return &exitError{code: exitUsage}
}

// unrelateRefused renders a deterministic refusal (exit 1): the
// single-line human refusal on stderr, and the machine refusal document
// on stdout with --json.
func unrelateRefused(cmd *cobra.Command, jsonOut bool, target, reason, hint string) error {
	if jsonOut {
		_ = emitJSON(cmd, unrelateJSON{Schema: unrelateSchema, OK: false, Target: target, Reason: reason, Hint: hint})
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "eka: unrelate refused: %s; %s\n", reason, hint)
	return &exitError{code: exitFail}
}

// renderUnrelateResult renders one unrelate outcome deterministically:
// the header (ticket, removed edges, state) and the state line; the
// published path adds the no-churn proof (instance version unchanged +
// object hash) and the no-change-log note (tickets own no state).
func renderUnrelateResult(s *ui.Style, target string, removed []string, state, hash string) {
	header := ui.NewHeader(s, "Unrelate").
		Add("Ticket", target).
		Add("State", state)
	header.Pipeline("Unrelate").Render()
	for _, edge := range removed {
		fmt.Fprintf(s.W, "  %s derives-from %s removed\n", ui.IconBullet, edge)
	}
	switch state {
	case "published":
		fmt.Fprintf(s.W, "  %s %s\n", ui.IconDone, s.Success("edge(s) removed — instance version unchanged (no churn), no change-log entry (tickets own no state)"))
	case "draft":
		fmt.Fprintf(s.W, "  %s %s\n", ui.IconDone, s.Success("edge(s) removed from the pending draft"))
	}
	if state == "published" {
		ui.NewSummary(s).
			Add("Instance Version", "unchanged").
			Add("Object Hash", hash).
			Add("Next", "run 'eka sync push' to refresh the repository snapshot").
			Render()
	}
}

// ticketRetired reports whether the ticket unit carries the retirement
// marker: the "retirement" object in its structured-JSON content
// payload (written by `eka retire tkt:<id>` — the zero-owned-state
// ticket cannot carry existence-state or a change-log entry, so the
// marker lives in content, which R9 constrains only to the required
// keys). Legacy text payloads never carry it.
func ticketRetired(u *exchange.Unit) bool {
	if u == nil || u.Content.Representation != exchange.StructuredJSON {
		return false
	}
	var content map[string]any
	if err := json.Unmarshal(u.ContentPayload, &content); err != nil {
		return false
	}
	marker, ok := content["retirement"].(map[string]any)
	return ok && len(marker) > 0
}
