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

// This file implements the assignment commands of the Authoring API
// surface (ADR-029 / req:team-collaboration §4.3): `eka assign <item>
// --to <mbr>`, `eka reassign <item> --to <mbr>` and `eka unassign
// <item>`. Assignment is the assigned-to relationship edge from a work
// item to a member (mbr-) line — relationship-only (ADR-013), at most
// one target per work item (single-assignee, ADR-029 Decision 2).
// Assignment changes happen ONLY through these explicit commands; the
// other authoring commands never assign (no side-effect auto-assign,
// and `eka relate` deliberately keeps assigned-to off its flag
// surface).
//
// The commands are thin Cobra layers over the edge with validation:
// target resolution (item line, member line, repository provenance),
// the locked command semantics, rendering and the exit-code mapping.
// The edge ADD of `eka assign` delegates to the relate API
// (runtime.Authoring.Relate — the published no-churn re-point or the
// pending-draft mutation, exactly like `eka relate`). The edge REMOVAL
// of `eka unassign` and the single-operation REPLACE of `eka reassign`
// have no Authoring-API counterpart (relate is add-only), so those two
// write paths mirror relate's own store mechanism — the line's current
// instance is re-pointed to a new immutable payload with the SAME
// instance version and revision (no instance churn), or the pending
// draft's relationships block is rewritten in place. See the boundary
// note in root.go for why the mirror lives here.
//
// Semantics (locked in req:team-collaboration §4.3):
//
//	assign    sets the assignee. Idempotent when the item is already
//	          assigned to the SAME member (unchanged, exit 0);
//	          deterministic refusal when the item is already assigned
//	          to a DIFFERENT member (use reassign to move).
//	reassign  replaces the existing assignment in ONE operation.
//	          Refusal when the item has no assignee (use assign);
//	          idempotent on the same target.
//	unassign  removes the assigned-to edge. No-op exit 0 when the item
//	          has no assignee (precedence: relate unchanged).
//
// Exit codes (the transition/note contract):
//
//	0  the edge is in the requested state (published, draft-mutated,
//	   or unchanged — every case writes nothing harmful)
//	1  refusal (missing artifact, non-work-item target,
//	   cross-namespace item, unresolvable member, cross-repository
//	   member, already-assigned-to-a-different-member on assign,
//	   not-assigned on reassign, legacy Markdown draft, CKO-level
//	   validation findings)
//	2  usage or internal error (invalid target, canonical published
//	   form, missing --to, unresolved --by source, workspace errors)
//
// Provenance (mirror of the R13 assigned-to sub-check): the member
// target must originate from the same repository as the work item —
// the target's namespace must equal the item's namespace.
// Cross-repository assignment is refused deterministically. An
// unresolvable member id is refused WITH the known member lines of the
// repository listed.
//
// --json emits the deterministic machine report (schema
// "eka-assignment-v1"); the default output is the human report in the
// CLI house style. The pinned machine keys of the slice
// (req:team-collaboration §6): "assignee" renders the canonical member
// identity, "no-assignee" is the member-axis bucket flag (never
// "unassigned" — that key belongs to the container axis).

// assignmentAction discriminates the three assignment commands.
type assignmentAction string

const (
	actionAssign   assignmentAction = "assign"
	actionReassign assignmentAction = "reassign"
	actionUnassign assignmentAction = "unassign"
)

// assignmentSchema is the schema id of the assignment machine report.
const assignmentSchema = "eka-assignment-v1"

// assignmentJSON is the deterministic machine report of one assignment
// run (schema "eka-assignment-v1"; pinned field order).
type assignmentJSON struct {
	Schema string `json:"schema"`
	OK     bool   `json:"ok"`
	Action string `json:"action"`
	Item   string `json:"item,omitempty"`
	// Assignee is the canonical line identity form of the member the
	// item is assigned to after the operation (pinned machine key:
	// the assignee field renders the mbr identity). Absent when the
	// item has no assignee.
	Assignee string `json:"assignee,omitempty"`
	// NoAssignee reports that the item carries no assigned-to edge —
	// the pinned machine key of the member-axis bucket (never
	// "unassigned", which belongs to the container axis).
	NoAssignee bool   `json:"no-assignee,omitempty"`
	State      string `json:"state,omitempty"`
	ObjectHash string `json:"objectHash,omitempty"`
	By         string `json:"by,omitempty"`
	Reason     string `json:"reason,omitempty"`
	Hint       string `json:"hint,omitempty"`
}

// Flag names of the assignment options (declared once, shared by the
// help text and the flag lookups).
const (
	flagAssignTo     = "to"
	flagAssignBy     = "by"
	flagAssignByKind = "by-kind"
	flagAssignJSON   = "json"
)

// newAssignCommand builds `eka assign <item> --to <mbr>`.
func newAssignCommand() *cobra.Command {
	return newAssignmentCommand(actionAssign)
}

// newReassignCommand builds `eka reassign <item> --to <mbr>`.
func newReassignCommand() *cobra.Command {
	return newAssignmentCommand(actionReassign)
}

// newUnassignCommand builds `eka unassign <item>`.
func newUnassignCommand() *cobra.Command {
	return newAssignmentCommand(actionUnassign)
}

// newAssignmentCommand builds one assignment command. The three
// commands share the resolution pipeline, the edge-write paths and the
// render/exit-code machinery; only the usage surface and the locked
// semantics differ.
func newAssignmentCommand(action assignmentAction) *cobra.Command {
	var use, short string
	var long, example string
	switch action {
	case actionAssign:
		use = "assign <item> --to <mbr>"
		short = "Assign a work item to a member"
		long = `Assign a work item to a member: the assigned-to edge (work item ->
member) is added to the artifact line — published items are re-pointed
to a new immutable payload with the SAME instance version (no instance
churn, the relate no-churn mechanism); a pending draft gets its
relationships block mutated in place. Single-assignee (ADR-029): a
work item has at most one assignee, so an item that already carries an
assigned-to edge to a DIFFERENT member is refused deterministically —
use 'eka reassign' to move the assignment.

The item is the work item line: <type>:<id> (unqualified — the
repository namespace applies) or <ns>/<type>:<id> (qualified — the
namespace must equal the repository's). Only work items (sto-/ts-/bug-/
td-/ch-/spk-) are assignable. The member is the --to target:
<mbr-id>, mbr:<id>, mbr-<id>, or <ns>/mbr:<id> — the member must be a
resolvable mbr- line of the SAME repository (provenance mirror of the
R13 assigned-to sub-check; cross-repository assignment is refused). An
unresolvable member is refused with the repository's known member
lines listed.

Assigning runs the standard publish validation: the would-be unit is
checked at CKO level (Rule 5 reference resolution with the store
resolver, plus the structural checks). The R13 transition gates and
the conditional assigned-to sub-check run at sync time, not at
publish — a blocked assignment is refused with the validation report.

Exit codes:
  0  assigned (published, draft-mutated, or unchanged — already
     assigned to the same member)
  1  refused (missing artifact, non-work-item target, cross-namespace
     item, unresolvable member, cross-repository member, already
     assigned to a different member, validation findings)
  2  usage or internal error (invalid target, canonical published
     form, missing --to, unresolved --by source)`
		example = `  eka assign sto:12 --to mbr:alice
  eka assign atrium-api/sto:12 --to atrium-api/mbr:alice
  eka assign sto:12 --to alice --json`
	case actionReassign:
		use = "reassign <item> --to <mbr>"
		short = "Move a work item's assignment to another member"
		long = `Move a work item's assignment in ONE operation: the existing
assigned-to edge is replaced by the new one (same instance version,
no instance churn). Reassign is the sanctioned way to change an
assignee — 'eka assign' refuses an already-assigned item. An item
without an assignee is refused deterministically (use 'eka assign' to
set the first assignment); reassigning to the current assignee is an
idempotent no-op.

The item is the work item line: <type>:<id> (unqualified — the
repository namespace applies) or <ns>/<type>:<id> (qualified — the
namespace must equal the repository's). Only work items
(sto-/ts-/bug-/td-/ch-/spk-) are assignable. The member is the --to
target: <mbr-id>, mbr:<id>, mbr-<id>, or <ns>/mbr:<id> — the member
must be a resolvable mbr- line of the SAME repository (cross-
repository assignment is refused). An unresolvable member is refused
with the repository's known member lines listed.

Exit codes:
  0  reassigned (published, draft-mutated, or unchanged — same member)
  1  refused (missing artifact, non-work-item target, cross-namespace
     item, unresolvable member, cross-repository member, not assigned
     to any member, validation findings)
  2  usage or internal error (invalid target, canonical published
     form, missing --to, unresolved --by source)`
		example = `  eka reassign sto:12 --to mbr:bob
  eka reassign atrium-api/sto:12 --to mbr:bob --json`
	case actionUnassign:
		use = "unassign <item>"
		short = "Remove a work item's assignment"
		long = `Remove a work item's assigned-to edge: the item returns to the
pool of unassigned work ('No assignee', the member-axis bucket of the
member-scoped board). The edge is removed with the SAME instance
version (no instance churn); a pending draft gets its relationships
block mutated in place. An item without an assignee is a no-op — exit
0, nothing written (precedence: relate unchanged).

The item is the work item line: <type>:<id> (unqualified — the
repository namespace applies) or <ns>/<type>:<id> (qualified — the
namespace must equal the repository's). Only work items
(sto-/ts-/bug-/td-/ch-/spk-) are unassignable.

Exit codes:
  0  unassigned (published, draft-mutated, or no-op — no assignee)
  1  refused (missing artifact, non-work-item target, cross-namespace
     item, legacy Markdown draft, validation findings)
  2  usage or internal error (invalid target, canonical published
     form, unresolved --by source)`
		example = `  eka unassign sto:12
  eka unassign atrium-api/sto:12 --json`
	}
	cmd := &cobra.Command{
		Use:     use,
		Short:   short,
		Long:    long,
		Example: example,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAssignment(cmd, args, action)
		},
	}
	cmd.Flags().String(flagAssignTo, "", "the member line to assign to: <mbr-id>, mbr:<id>, mbr-<id>, or <ns>/mbr:<id> (assign/reassign)")
	cmd.Flags().String(flagAssignBy, "", "operation authority name (default: `git config user.name`)")
	cmd.Flags().String(flagAssignByKind, "", "author identity kind: user, agent, or worker (default: user)")
	cmd.Flags().Bool(flagAssignJSON, false, "emit the deterministic machine report (schema eka-assignment-v1)")
	if action == actionUnassign {
		// --to is an assign/reassign option: unassign removes the
		// assigned-to edge and never re-points it. Hidden so the
		// unassign help does not advertise it; passing it anyway is
		// refused as a usage error (runAssignment).
		cmd.Flags().MarkHidden(flagAssignTo)
	}
	return cmd
}

// runAssignment executes one assignment command: resolve the item and
// the member target, apply the locked semantics, write the edge, and
// render the deterministic report.
func runAssignment(cmd *cobra.Command, args []string, action assignmentAction) error {
	to, _ := cmd.Flags().GetString(flagAssignTo)
	jsonOut, _ := cmd.Flags().GetBool(flagAssignJSON)
	byFlag, _ := cmd.Flags().GetString(flagAssignBy)
	byKindFlag, _ := cmd.Flags().GetString(flagAssignByKind)
	if action == actionUnassign && cmd.Flags().Changed(flagAssignTo) {
		// --to is an assign/reassign option; unassign removes the edge
		// and never re-points it — passing --to is a usage error, not
		// a silently ignored option.
		return assignmentUsage(cmd, jsonOut, action, "unassign does not take --to <mbr>: the assigned-to edge is removed, not re-pointed")
	}
	if (action == actionAssign || action == actionReassign) && strings.TrimSpace(to) == "" {
		return assignmentUsage(cmd, jsonOut, action, fmt.Sprintf("%s requires --to <mbr>: the member line to assign to", action))
	}
	by, err := runtime.BySource(byFlag, byKindFlag, ".")
	if err != nil {
		return assignmentUsage(cmd, jsonOut, action, err.Error())
	}
	r, err := openAuthoringRuntime(cmd)
	if err != nil {
		return err // Exit 2: workspace resolution.
	}
	defer r.Close()

	ctx, err := resolveAssignmentTarget(r, args[0])
	if err != nil {
		var refusal *assignmentRefusal
		if errors.As(err, &refusal) {
			return assignmentRefused(cmd, jsonOut, action, refusal.reason, refusal.hint)
		}
		return assignmentUsage(cmd, jsonOut, action, err.Error())
	}

	// The member target (assign/reassign only): resolved BEFORE the
	// assignment-state checks so an unresolvable member refuses with
	// the member list regardless of the item's state.
	memberForm := ""
	if action == actionAssign || action == actionReassign {
		form, err := resolveMemberTarget(ctx, to)
		if err != nil {
			var refusal *assignmentRefusal
			if errors.As(err, &refusal) {
				return assignmentRefused(cmd, jsonOut, action, refusal.reason, refusal.hint)
			}
			return assignmentUsage(cmd, jsonOut, action, err.Error())
		}
		memberForm = form
	}

	// The item's current assigned-to targets: from the published
	// instance, or from the pending draft's relationships block when
	// the line has no published instance.
	raws := assignedToTargets(ctx.unit)
	if ctx.unit == nil && ctx.draftPath != "" {
		raws, err = draftAssignedToTargets(ctx.draftPath)
		if err != nil {
			return fmt.Errorf("%s: %w", action, err) // Exit 2: internal.
		}
	}
	members := assignedMembers(ctx.graph, ctx.ref.Namespace, raws)

	// The locked semantics (req:team-collaboration §4.3).
	switch action {
	case actionAssign:
		if len(raws) > 0 {
			// An existing edge refuses — idempotent only on the SAME
			// target (the single-assignee rule rejects a second edge).
			if len(members) > 0 && members[0] == memberForm {
				return assignmentUnchanged(cmd, action, ctx, memberForm, by)
			}
			current := "an unresolvable member target"
			if len(members) > 0 && members[0] != "" {
				current = members[0]
			}
			return assignmentRefused(cmd, jsonOut, action,
				fmt.Sprintf("%s is already assigned to %s", ctx.form, current),
				"use 'eka reassign' to move the assignment")
		}
		// The edge add: the relate API (published no-churn re-point or
		// pending-draft mutation — the same paths `eka relate` uses).
		// The stored target is the repository-local member form
		// (mbr:<id>) — the same-repository storage convention of the
		// assigned-to edge (mirror of how relate stores a target as
		// given; the view layer resolves both forms).
		res, err := runtime.Authoring.Relate(r, runtime.RelateRequest{
			RepoPath: ".",
			Target:   ctx.form,
			Relationships: []exchange.Relationship{
				{Type: "assigned-to", Target: localMemberForm(memberForm)},
			},
		})
		if err != nil {
			return assignmentRelateError(cmd, jsonOut, action, err)
		}
		if jsonOut {
			return emitJSON(cmd, assignmentJSON{
				Schema:     assignmentSchema,
				OK:         true,
				Action:     string(action),
				Item:       res.Target,
				Assignee:   memberForm,
				State:      res.State,
				ObjectHash: res.ObjectHash,
				By:         by.Name,
			})
		}
		renderAssignmentResult(styleFor(cmd), action, res.Target, memberForm, res.State, res.ObjectHash, by)
		return nil

	case actionReassign:
		if err := assignmentMissingGuard(cmd, jsonOut, action, ctx); err != nil {
			return err
		}
		if len(raws) == 0 {
			return assignmentRefused(cmd, jsonOut, action,
				fmt.Sprintf("%s is not assigned to any member", ctx.form),
				"use 'eka assign' to set the first assignment")
		}
		if len(members) > 0 && members[0] == memberForm {
			// Idempotent: the item is already assigned to the target.
			return assignmentUnchanged(cmd, action, ctx, memberForm, by)
		}
		state, hash, err := writeAssignment(ctx, memberForm)
		if err != nil {
			return assignmentWriteError(cmd, jsonOut, action, err)
		}
		if jsonOut {
			return emitJSON(cmd, assignmentJSON{
				Schema:     assignmentSchema,
				OK:         true,
				Action:     string(action),
				Item:       ctx.form,
				Assignee:   memberForm,
				State:      state,
				ObjectHash: hash,
				By:         by.Name,
			})
		}
		renderAssignmentResult(styleFor(cmd), action, ctx.form, memberForm, state, hash, by)
		return nil

	case actionUnassign:
		if err := assignmentMissingGuard(cmd, jsonOut, action, ctx); err != nil {
			return err
		}
		if len(raws) == 0 {
			// No-op: no edge to remove (exit 0, nothing written).
			return assignmentUnchanged(cmd, action, ctx, "", by)
		}
		state, hash, err := writeAssignment(ctx, "")
		if err != nil {
			return assignmentWriteError(cmd, jsonOut, action, err)
		}
		if jsonOut {
			return emitJSON(cmd, assignmentJSON{
				Schema:     assignmentSchema,
				OK:         true,
				Action:     string(action),
				Item:       ctx.form,
				NoAssignee: true,
				State:      state,
				ObjectHash: hash,
				By:         by.Name,
			})
		}
		renderAssignmentResult(styleFor(cmd), action, ctx.form, "", state, hash, by)
		return nil
	}
	return nil
}

// assignmentTarget is the resolved context of one assignment run: the
// item line, its repository/project, the project's units and graph,
// and the item's current state.
type assignmentTarget struct {
	ref     conformance.Reference // the item line (namespace/type/id)
	form    string                // the canonical line form "<ns>/<type>:<id>"
	project string                // the registered project of the repository
	units   []*exchange.Unit      // every unit of the project
	graph   *view.Graph           // the project graph (assignee/member resolution)
	unit    *exchange.Unit        // the item's current (highest) instance; nil when only a draft exists
	// hasPendingDraft reports that the line carries a pending draft
	// (JSON or legacy Markdown) when it has no published instance.
	hasPendingDraft bool
	// draftPath is the pending JSON draft of the item ("" when the line
	// has a published instance or no JSON draft).
	draftPath string
}

// assignmentRefusal is a deterministic refusal (exit 1) of the
// assignment commands: reason + hint, nothing was written.
type assignmentRefusal struct {
	reason string
	hint   string
}

// Error renders the deterministic refusal message.
func (e *assignmentRefusal) Error() string {
	return fmt.Sprintf("%s; %s", e.reason, e.hint)
}

// assignmentValidationError reports that the would-be unit failed
// CKO-level validation (the standard publish validation); nothing was
// written. The Report is carried so the command renders the findings.
type assignmentValidationError struct {
	// Target is the line form the assignment addressed.
	Target string
	// Report is the CKO-level validation report.
	Report *conformance.Report
}

// Error renders the deterministic refusal message.
func (e *assignmentValidationError) Error() string {
	return fmt.Sprintf("%s failed CKO-level validation with %d blocking error(s); nothing was changed",
		e.Target, e.Report.ErrorCount())
}

// resolveAssignmentTarget resolves the item target and its repository
// context: the line form gate (usage errors), the work-item type gate,
// the repository-context gate (ADR-018), the namespace gate (mirror of
// the relate ownership gate), the project units and the item's current
// state. Usage-class errors are returned as plain errors (exit 2);
// deterministic refusals as *assignmentRefusal (exit 1).
func resolveAssignmentTarget(r *runtime.Runtime, target string) (*assignmentTarget, error) {
	ref, err := conformance.ParseReference(target, "", "")
	if err != nil {
		return nil, fmt.Errorf("invalid target %q: %v", target, err) // Exit 2: usage.
	}
	if ref.HasVersion {
		return nil, fmt.Errorf("%s is a canonical published form; assignment addresses the artifact line", target) // Exit 2: usage.
	}
	if !conformance.IsWorkItemType(ref.Type) {
		return nil, &assignmentRefusal{
			reason: fmt.Sprintf("%s is not a work item; assignment applies to work items (sto/ts/bug/td/ch/spk) only", target),
			hint:   "pass a work item line such as sto:<id>",
		}
	}
	// The repository-context gate (ADR-018) + the registered-repository
	// gate: assignment writes the workspace store, so the repository
	// must be registered (the same gates `eka relate` and `eka
	// transition` enforce).
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
		return nil, &assignmentRefusal{
			reason: fmt.Sprintf("%s is not an EKA repository (no eka.yaml)", abs),
			hint:   "run 'eka init' first",
		}
	}
	repo, found, err := r.Workspace.FindRepo(abs)
	if err != nil {
		return nil, fmt.Errorf("cannot resolve the repository registration: %w", err) // Exit 2.
	}
	if !found {
		return nil, &assignmentRefusal{
			reason: fmt.Sprintf("repository %s is not registered in the EKA workspace", abs),
			hint:   "run 'eka sync' (auto-registers) or 'eka project register' first",
		}
	}
	// The namespace: the eka.yaml declaration wins, else the registered
	// repository's default namespace (the relate/transition
	// resolution). A QUALIFIED target must stay inside the
	// repository's own namespace (cross-platform access is read-only,
	// the same ownership gate the other authoring commands enforce).
	ns := meta.Namespace
	if ns == "" {
		ns = repo.Namespace
	}
	if ref.Namespace != "" {
		if ref.Namespace != ns {
			return nil, &assignmentRefusal{
				reason: fmt.Sprintf("target namespace %s differs from the repository namespace %s; cross-platform access is read-only", ref.Namespace, ns),
				hint:   "assign only artifacts of the repository's own namespace",
			}
		}
	} else {
		ref.Namespace = ns
	}
	units, err := r.Knowledge.UnitsByProject(repo.ProjectID)
	if err != nil {
		return nil, fmt.Errorf("cannot read the project knowledge: %w", err) // Exit 2: store failure.
	}
	ctx := &assignmentTarget{
		ref:     ref,
		form:    view.LineForm(ref.Namespace, ref.Type, ref.ID),
		project: repo.ProjectID,
		units:   units,
		graph:   view.NewGraph(".", units),
		unit:    currentLineUnit(units, ref),
	}
	// A line without a published instance may still carry a pending
	// draft (the edge-add path of `eka assign` mutates it in place).
	if ctx.unit == nil {
		root, herr := workspace.HomeDir()
		if herr != nil {
			return nil, fmt.Errorf("cannot resolve the workspace root: %w", herr) // Exit 2.
		}
		ctx.draftPath, ctx.hasPendingDraft, err = pendingDraftPath(root, ctx.project, ref.Type, ref.ID)
		if err != nil {
			return nil, err // Exit 2: draft resolution failure.
		}
	}
	return ctx, nil
}

// currentLineUnit returns the highest instance of the item line among
// the project units, or nil when the line has no published instance.
func currentLineUnit(units []*exchange.Unit, ref conformance.Reference) *exchange.Unit {
	var best *exchange.Unit
	for _, u := range units {
		if u.Identity.Namespace != ref.Namespace || u.Identity.Type != ref.Type || u.Identity.ID != ref.ID {
			continue
		}
		if best == nil || u.Identity.InstanceVersion > best.Identity.InstanceVersion {
			best = u
		}
	}
	return best
}

// pendingDraftPath resolves the pending draft file of one line:
// <workspace>/drafts/<project>/<type>-<id>.json, or the legacy .md
// form. It returns the JSON draft path ("" when none), and whether ANY
// pending draft (JSON or legacy Markdown) exists — the existence
// signal the unassign/reassign no-op guards need.
func pendingDraftPath(wsRoot, project, typeToken, id string) (string, bool, error) {
	root := filepath.Join(wsRoot, "drafts", project)
	jsonPath := filepath.Join(root, typeToken+"-"+id+".json")
	if _, err := os.Stat(jsonPath); err == nil {
		return jsonPath, true, nil
	}
	if _, err := os.Stat(filepath.Join(root, typeToken+"-"+id+".md")); err == nil {
		return "", true, nil
	}
	return "", false, nil
}

// assignedToTargets returns the raw stored targets of the item's
// assigned-to relationships, in stored order.
func assignedToTargets(u *exchange.Unit) []string {
	if u == nil {
		return nil
	}
	var out []string
	for _, rel := range u.Relationships {
		if rel.Type == "assigned-to" {
			out = append(out, rel.Target)
		}
	}
	return out
}

// assignedMembers canonicalizes the raw assigned-to targets to the
// member line forms the graph resolves them to, preserving order. A
// target that does not resolve (a cross-repository member, an
// unresolved member line, a malformed reference) stays "" — the R13
// sub-check's resolution semantics.
func assignedMembers(g *view.Graph, ns string, raws []string) []string {
	out := make([]string, len(raws))
	for i, raw := range raws {
		ref, err := conformance.ParseReference(raw, ns, "sto")
		if err != nil {
			continue
		}
		u := g.Resolve(ref)
		if u == nil || u.Identity.Type != "mbr" {
			continue
		}
		out[i] = view.LineForm(u.Identity.Namespace, u.Identity.Type, u.Identity.ID)
	}
	return out
}

// assignmentMissingGuard refuses the unassign/reassign edge-mutation
// paths when the line has no representation the commands can address:
// no published instance AND no pending draft is a missing-artifact
// refusal (mirror of the relate refusal); a legacy Markdown draft is
// refused (no deterministic in-place mutation path, mirror of relate).
func assignmentMissingGuard(cmd *cobra.Command, jsonOut bool, action assignmentAction, ctx *assignmentTarget) error {
	if ctx.unit != nil || ctx.draftPath != "" {
		return nil
	}
	if ctx.hasPendingDraft {
		return assignmentRefused(cmd, jsonOut, action,
			fmt.Sprintf("draft %s:%s is a legacy Markdown draft, which the assignment commands cannot mutate deterministically", ctx.ref.Type, ctx.ref.ID),
			"edit the file directly, or migrate the draft to the JSON format ('eka new' scaffolds JSON drafts)")
	}
	return assignmentRefused(cmd, jsonOut, action,
		fmt.Sprintf("artifact line %s has no published instance and no pending draft", ctx.form),
		"run 'eka new <type>:<id>' to scaffold a draft, or publish the pending draft first")
}

// resolveMemberTarget resolves the --to / --member target to the
// canonical member line form within the item's namespace: the target
// must parse (usage class), be an mbr- line, stay inside the item's
// repository (cross-repository assignment refused — the R13 provenance
// mirror), and resolve to a known member line (refusal with the known
// member lines listed). A pending draft member line of the same
// project counts as resolvable — the R5-mirrored draft tolerance (a
// draft work item may point at a draft member; the item-side
// content-state decides the tolerance at validation).
func resolveMemberTarget(ctx *assignmentTarget, raw string) (string, error) {
	form, err := parseMemberTarget(raw, ctx.ref.Namespace)
	if err != nil {
		return "", err
	}
	if !memberLineExists(ctx.units, form) && !draftMemberExists(ctx, form) {
		return "", &assignmentRefusal{
			reason: fmt.Sprintf("member %s does not resolve", form),
			hint:   fmt.Sprintf("available members of the repository: %s", strings.Join(memberLinesInNS(ctx.units, ctx.ref.Namespace), ", ")),
		}
	}
	return form, nil
}

// draftMemberExists reports whether the member line exists as a pending
// draft of the same project — the R5-mirrored draft tolerance (a draft
// work item may point at a draft member). The draft's frontmatter
// namespace must equal the item's namespace.
func draftMemberExists(ctx *assignmentTarget, form string) bool {
	ref, err := conformance.ParseReference(form, ctx.ref.Namespace, "mbr")
	if err != nil {
		return false
	}
	root, err := workspace.HomeDir()
	if err != nil {
		return false
	}
	path := filepath.Join(root, "drafts", ctx.project, "mbr-"+ref.ID+".json")
	artifact, err := conformance.ScanFile(path)
	if err != nil || artifact == nil {
		return false
	}
	return artifact.Namespace == ctx.ref.Namespace
}

// parseMemberTarget parses a member target into the candidate canonical
// member line form within the item's namespace. Accepted forms:
// <mbr-id>, mbr:<id>, mbr-<id>, and the qualified <ns>/mbr:<id>. A
// qualified target must name an mbr- line of the ITEM'S namespace —
// cross-namespace (cross-repository) targets are refused. The errors
// are *assignmentRefusal: the assignment commands map them to their
// refusal class (exit 1), the view command renders the reason text as
// its usage class (exit 2).
func parseMemberTarget(raw, itemNS string) (string, error) {
	t := strings.TrimSpace(raw)
	if t == "" {
		return "", &assignmentRefusal{reason: "a member target is required", hint: "pass --to <mbr-id>, mbr:<id>, mbr-<id>, or <ns>/mbr:<id>"}
	}
	if strings.Contains(t, "/") {
		ref, err := conformance.ParseReference(t, itemNS, "mbr")
		if err != nil {
			return "", &assignmentRefusal{reason: fmt.Sprintf("invalid member target %q", t), hint: "use <mbr-id>, mbr:<id>, mbr-<id>, or <ns>/mbr:<id>"}
		}
		if ref.Type != "mbr" {
			return "", &assignmentRefusal{reason: fmt.Sprintf("%q is not a member (mbr-) line", t), hint: "assignment points to a member line"}
		}
		if ref.Namespace != itemNS {
			return "", &assignmentRefusal{
				reason: fmt.Sprintf("member %s originates outside the work item's repository (%s); cross-repository assignment is refused",
					view.LineForm(ref.Namespace, "mbr", ref.ID), itemNS),
				hint: "assign only members of the same repository",
			}
		}
		return view.LineForm(ref.Namespace, "mbr", ref.ID), nil
	}
	id := strings.TrimPrefix(strings.TrimPrefix(t, "mbr:"), "mbr-")
	if id == "" {
		return "", &assignmentRefusal{reason: fmt.Sprintf("invalid member target %q", t), hint: "use <mbr-id>, mbr:<id>, mbr-<id>, or <ns>/mbr:<id>"}
	}
	return view.LineForm(itemNS, "mbr", id), nil
}

// memberLines lists the distinct member (mbr-) lines of the project
// units, sorted by canonical identity form.
func memberLines(units []*exchange.Unit) []string {
	seen := make(map[string]bool)
	var out []string
	for _, u := range units {
		if u.Identity.Type != "mbr" {
			continue
		}
		form := view.LineForm(u.Identity.Namespace, u.Identity.Type, u.Identity.ID)
		if seen[form] {
			continue
		}
		seen[form] = true
		out = append(out, form)
	}
	sort.Strings(out)
	return out
}

// memberLinesInNS lists the distinct member lines of one namespace —
// the assignable members of the item's repository.
func memberLinesInNS(units []*exchange.Unit, ns string) []string {
	var out []string
	for _, form := range memberLines(units) {
		if strings.HasPrefix(form, ns+"/mbr:") {
			out = append(out, form)
		}
	}
	return out
}

// memberLineExists reports whether the canonical member form is a known
// member line of the project.
func memberLineExists(units []*exchange.Unit, form string) bool {
	for _, u := range units {
		if u.Identity.Type == "mbr" && view.LineForm(u.Identity.Namespace, u.Identity.Type, u.Identity.ID) == form {
			return true
		}
	}
	return false
}

// writeAssignment applies the assignment edge set ("" = none) to the
// item line: the published path re-points the line's current instance
// to a new immutable payload with the SAME instance version and
// revision (the relate no-churn mechanism); the draft path rewrites the
// pending JSON draft's relationships block in place. It returns the
// write state ("published" | "draft") and the new payload's object
// hash ("" on the draft path). The assignee is stored in the
// repository-local member form (mbr:<id>) — the same-repository
// storage convention of the assigned-to edge.
func writeAssignment(ctx *assignmentTarget, assignee string) (string, string, error) {
	ws, err := workspace.Ensure()
	if err != nil {
		return "", "", err // Exit 2: workspace resolution.
	}
	defer ws.Close()
	st := ws.Store()

	line, err := st.UnitsByLine(ctx.ref.Namespace, ctx.ref.Type, ctx.ref.ID)
	if err != nil {
		return "", "", fmt.Errorf("assignment: %w", err) // Exit 2: store failure.
	}
	if len(line) > 0 {
		return writeAssignmentPublished(st, line, ctx.form, localMemberForm(assignee))
	}
	return writeAssignmentDraft(ws, ctx, localMemberForm(assignee))
}

// localMemberForm renders the repository-local form of a member line:
// "<type>:<id>" without the namespace — the stored form of an
// assigned-to edge whose target lives in the same repository (the
// relate convention: a target is stored as given, and the assignment
// commands always address same-repository members — cross-repository
// assignment is refused). The qualified canonical form stays the
// display/resolution form; only the stored edge uses the local form.
func localMemberForm(form string) string {
	if i := strings.Index(form, "/"); i >= 0 {
		return form[i+1:]
	}
	return form
}

// writeAssignmentPublished re-points the line's current instance to a
// new immutable payload whose assigned-to edges equal the requested
// set, with the SAME canonical form, instance version and revision —
// the exact relate published-path mechanism (runtime/relate.go
// relatePublished): the merged edge set, the standard publish
// validation (ValidateCKO with the store resolver), and the reference
// re-point via store.RepointUnit (the same-version re-point: a payload
// that equals an earlier same-version payload — e.g. re-adding an edge
// after an unassign — still moves the reference; the line never
// advances). Provenance (project_id, source_repo) is preserved from
// the current reference.
func writeAssignmentPublished(st *store.Store, line []*exchange.Unit, lineForm, assignee string) (string, string, error) {
	current := line[0]
	for _, u := range line {
		if u.Identity.InstanceVersion > current.Identity.InstanceVersion {
			current = u
		}
	}
	// The would-be unit: only the assigned-to edges change (plus the
	// Updated date, mirroring relate).
	next := *current
	next.Relationships = replaceAssignedTo(current.Relationships, assignee)
	next.Updated = time.Now().Format("2006-01-02")

	// The standard publish validation (mirror of relatePublished): the
	// would-be unit must validate at CKO level with the store resolver.
	// ValidateCKO runs with SkipGates — the R13 transition gates and
	// the conditional assigned-to sub-check are NOT evaluated here
	// (they run at sync time); only Rule 5 reference resolution and
	// the structural checks apply. The single-assignee, provenance and
	// resolvability rules are enforced by the CLI's own resolution
	// (resolveMemberTarget) before this point.
	resolver := &assignmentStoreResolver{st: st}
	report, err := conformance.ValidateCKO(&next, conformance.ValidateCKOOptions{
		Resolve: resolver.Resolve,
	})
	if err != nil {
		return "", "", fmt.Errorf("assignment: validation failed: %w", err)
	}
	report.Results = append(report.Results,
		resolver.Findings(next.CanonicalIdentityForm, next.StateVector.ContentState)...)
	if !report.Pass() {
		return "", "", &assignmentValidationError{Target: lineForm, Report: report}
	}

	// The reference is the mutable part: re-point it with the SAME
	// identity, instance version and revision (the no-churn mechanism).
	curRef, ok, err := st.Ref(current.CanonicalIdentityForm)
	if err != nil {
		return "", "", fmt.Errorf("assignment: %w", err)
	}
	if !ok {
		return "", "", &assignmentRefusal{
			reason: fmt.Sprintf("the reference of %s is missing (store corruption)", current.CanonicalIdentityForm),
			hint:   "run 'eka integrity check'",
		}
	}
	unitJSON, err := exchange.MarshalUnit(&next)
	if err != nil {
		return "", "", fmt.Errorf("assignment: cannot serialize %s: %w", next.CanonicalIdentityForm, err)
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
		return "", "", fmt.Errorf("assignment: cannot publish %s: %w", next.CanonicalIdentityForm, err)
	}
	return "published", hash, nil
}

// writeAssignmentDraft rewrites the pending JSON draft's relationships
// block in place (an edge mutation before publish — the relate draft
// path). Legacy Markdown drafts are refused. The post-mutation draft is
// re-validated at CKO level (non-destructive, mirroring relate).
func writeAssignmentDraft(ws *workspace.Workspace, ctx *assignmentTarget, assignee string) (string, string, error) {
	if ctx.draftPath == "" {
		// The legacy .md form is refused before the generic
		// no-draft refusal (its message names the real blocker).
		mdPath := filepath.Join(ws.Path(), "drafts", ctx.project, ctx.ref.Type+"-"+ctx.ref.ID+".md")
		if _, err := os.Stat(mdPath); err == nil {
			return "", "", &assignmentRefusal{
				reason: fmt.Sprintf("draft %s:%s is a legacy Markdown draft, which the assignment commands cannot mutate deterministically", ctx.ref.Type, ctx.ref.ID),
				hint:   "edit the file directly, or migrate the draft to the JSON format ('eka new' scaffolds JSON drafts)",
			}
		}
		return "", "", &assignmentRefusal{
			reason: fmt.Sprintf("artifact line %s has no published instance and no pending draft", ctx.form),
			hint:   "run 'eka new <type>:<id>' to scaffold a draft, or publish the pending draft first",
		}
	}
	if err := rewriteDraftAssignment(ctx.draftPath, assignee); err != nil {
		return "", "", err
	}
	// Post-mutation re-validation (non-destructive, mirroring `eka
	// relate` on the draft path): the draft stays and the findings are
	// reported.
	rt, err := runtime.Ensure()
	if err != nil {
		return "", "", err
	}
	defer rt.Close()
	if _, err := runtime.Authoring.ValidateDraft(rt, ctx.ref.Type+":"+ctx.ref.ID, ctx.project); err != nil {
		return "", "", fmt.Errorf("assignment: %w", err)
	}
	return "draft", "", nil
}

// replaceAssignedTo returns the relationships with every assigned-to
// edge replaced by exactly one assigned-to edge to the given target
// ("" = no assigned-to edge), in canonical (type, target) order.
func replaceAssignedTo(rels []exchange.Relationship, assignee string) []exchange.Relationship {
	type relKey struct{ t, target string }
	seen := make(map[relKey]bool)
	keys := make([]relKey, 0, len(rels)+1)
	for _, rel := range rels {
		if rel.Type == "assigned-to" {
			continue // replaced by the single requested edge below
		}
		k := relKey{t: rel.Type, target: strings.TrimSpace(rel.Target)}
		if k.target == "" || seen[k] {
			continue
		}
		seen[k] = true
		keys = append(keys, k)
	}
	if assignee != "" {
		k := relKey{t: "assigned-to", target: assignee}
		if !seen[k] {
			seen[k] = true
			keys = append(keys, k)
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].t != keys[j].t {
			return keys[i].t < keys[j].t
		}
		return keys[i].target < keys[j].target
	})
	out := make([]exchange.Relationship, 0, len(keys))
	for _, k := range keys {
		out = append(out, exchange.Relationship{Type: k.t, Target: k.target})
	}
	return out
}

// draftAssignedToTargets returns the raw assigned-to targets of a
// pending JSON draft file ("" when the file carries none).
func draftAssignedToTargets(path string) ([]string, error) {
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
	targets, ok := raw[conformance.StateKeyCamel("assigned-to")].([]any)
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

// rewriteDraftAssignment deterministically rewrites a JSON draft's
// relationships block so the assigned-to edges equal the requested set
// ("" = none) — the mirror of relate's draft rewrite
// (runtime/relate.go rewriteDraftRelationships): the file is parsed
// into a generic object, the `relationships` key is rebuilt from the
// merged edge set (camelCase field names, per-field sorted targets),
// and the file is written back as 2-space-indented JSON with a
// trailing newline.
func rewriteDraftAssignment(path, assignee string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("assignment: cannot read draft %s: %w", path, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("assignment: draft %s is not valid JSON: %w", path, err)
	}
	existing := draftRelationshipsOf(doc)
	merged := replaceAssignedTo(existing, assignee)
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
		return fmt.Errorf("assignment: cannot serialize draft %s: %w", path, err)
	}
	var indented bytes.Buffer
	if err := json.Indent(&indented, out, "", "  "); err != nil {
		return fmt.Errorf("assignment: cannot serialize draft %s: %w", path, err)
	}
	indented.WriteByte('\n')
	return os.WriteFile(path, indented.Bytes(), 0o644)
}

// draftRelationshipsOf extracts the edge set of a generic JSON draft
// document's relationships block (camelCase field names -> targets),
// in canonical field order.
func draftRelationshipsOf(doc map[string]any) []exchange.Relationship {
	var out []exchange.Relationship
	raw, ok := doc["relationships"].(map[string]any)
	if !ok {
		return out
	}
	for _, field := range conformance.RelationshipFieldNames() {
		targets, ok := raw[conformance.StateKeyCamel(field)].([]any)
		if !ok {
			continue
		}
		for _, t := range targets {
			if s, ok := t.(string); ok {
				out = append(out, exchange.Relationship{Type: field, Target: s})
			}
		}
	}
	return out
}

// assignmentStoreResolver is the CKO-level relationship resolution
// callback over the canonical store — the mirror of the runtime's
// storeResolver (runtime/draft.go): a line-level reference resolves
// when the line has instances, a versioned reference only when the
// exact instance exists. Store failures resolve as "unresolved" (the
// conservative answer) but are never silent: every failed lookup is
// recorded and surfaced by Findings.
type assignmentStoreResolver struct {
	st   *store.Store
	errs map[string]error // referenced line key -> the first store error
}

// Resolve implements the conformance resolution callback.
func (r *assignmentStoreResolver) Resolve(ref conformance.Reference) bool {
	units, err := r.st.UnitsByLine(ref.Namespace, ref.Type, ref.ID)
	if err != nil {
		key := ref.Namespace + "/" + ref.Type + ":" + ref.ID
		if r.errs == nil {
			r.errs = make(map[string]error)
		}
		if _, seen := r.errs[key]; !seen {
			r.errs[key] = err
		}
		return false
	}
	if !ref.HasVersion {
		return len(units) > 0
	}
	for _, u := range units {
		if u.Identity.InstanceVersion == ref.Version {
			return true
		}
	}
	return false
}

// Findings converts the recorded store failures into report results in
// deterministic order (by referenced line), mirroring the runtime's
// Findings: a failed existence check is a warning while content-state
// is draft, an error otherwise (Rule 5's draft tolerance).
func (r *assignmentStoreResolver) Findings(file, contentState string) []conformance.Result {
	if len(r.errs) == 0 {
		return nil
	}
	keys := make([]string, 0, len(r.errs))
	for k := range r.errs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]conformance.Result, 0, len(keys))
	for _, k := range keys {
		sev := conformance.SeverityError
		if contentState == "draft" {
			sev = conformance.SeverityWarning
		}
		out = append(out, conformance.Result{
			File:     file,
			Rule:     conformance.Rule5,
			Severity: sev,
			Message:  fmt.Sprintf("reference %s could not be checked against the store: %v", k, r.errs[k]),
		})
	}
	return out
}

// assignmentRelateError maps an error of the assign edge-add (the
// relate API) to the assignment exit-code contract: relate refusals and
// validation failures are exit 1, everything else exit 2.
func assignmentRelateError(cmd *cobra.Command, jsonOut bool, action assignmentAction, err error) error {
	var refusal *runtime.RelateRefusal
	if errors.As(err, &refusal) {
		return assignmentRefused(cmd, jsonOut, action, refusal.Reason, refusal.Hint)
	}
	var ve *runtime.RelateValidationError
	if errors.As(err, &ve) {
		printCKOReport(styleFor(cmd), ve.Target, ve.Report)
		fmt.Fprintf(cmd.ErrOrStderr(), "eka: %s refused: %s\n", action, err)
		return &exitError{code: exitFail}
	}
	return err // Exit 2: internal.
}

// assignmentWriteError maps an error of the unassign/reassign write
// paths to the exit-code contract.
func assignmentWriteError(cmd *cobra.Command, jsonOut bool, action assignmentAction, err error) error {
	var refusal *assignmentRefusal
	if errors.As(err, &refusal) {
		return assignmentRefused(cmd, jsonOut, action, refusal.reason, refusal.hint)
	}
	var ve *assignmentValidationError
	if errors.As(err, &ve) {
		printCKOReport(styleFor(cmd), ve.Target, ve.Report)
		fmt.Fprintf(cmd.ErrOrStderr(), "eka: %s refused: %s\n", action, err)
		return &exitError{code: exitFail}
	}
	return err // Exit 2: internal.
}

// assignmentUnchanged renders the idempotent no-op (exit 0): the
// requested edge state already holds, nothing was written.
func assignmentUnchanged(cmd *cobra.Command, action assignmentAction, ctx *assignmentTarget, memberForm string, by conformance.AuthorIdentity) error {
	if jsonOut, _ := cmd.Flags().GetBool(flagAssignJSON); jsonOut {
		report := assignmentJSON{
			Schema: assignmentSchema,
			OK:     true,
			Action: string(action),
			Item:   ctx.form,
			State:  "unchanged",
			By:     by.Name,
		}
		if memberForm != "" {
			report.Assignee = memberForm
		} else {
			report.NoAssignee = true
		}
		return emitJSON(cmd, report)
	}
	renderAssignmentResult(styleFor(cmd), action, ctx.form, memberForm, "unchanged", "", by)
	return nil
}

// assignmentUsage renders a usage-class failure (exit 2): the error is
// a deterministic "eka: <error>" line on stderr; --json additionally
// gets the machine refusal document on stdout.
func assignmentUsage(cmd *cobra.Command, jsonOut bool, action assignmentAction, message string) error {
	if jsonOut {
		_ = emitJSON(cmd, assignmentJSON{Schema: assignmentSchema, OK: false, Action: string(action), Reason: message})
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "eka: %s\n", message)
	return &exitError{code: exitUsage}
}

// assignmentRefused renders a deterministic refusal (exit 1): the
// single-line human refusal on stderr, and the machine refusal document
// on stdout with --json.
func assignmentRefused(cmd *cobra.Command, jsonOut bool, action assignmentAction, reason, hint string) error {
	if jsonOut {
		_ = emitJSON(cmd, assignmentJSON{Schema: assignmentSchema, OK: false, Action: string(action), Reason: reason, Hint: hint})
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "eka: %s refused: %s; %s\n", action, reason, hint)
	return &exitError{code: exitFail}
}

// renderAssignmentResult renders one assignment outcome
// deterministically: the header (item, assignee, state, authority) and
// the state line; the published path adds the no-churn proof (instance
// version + object hash).
func renderAssignmentResult(s *ui.Style, action assignmentAction, item, assignee, state, hash string, by conformance.AuthorIdentity) {
	header := ui.NewHeader(s, titleCase(string(action))).
		Add("Item", item).
		Add("State", state).
		Add("By", by.Name)
	if assignee != "" {
		header.Add("Assignee", assignee)
	}
	header.Pipeline(titleCase(string(action))).Render()
	switch {
	case state == "published":
		switch action {
		case actionAssign:
			fmt.Fprintf(s.W, "  %s %s\n", ui.IconDone, s.Success("assigned to "+assignee))
		case actionReassign:
			fmt.Fprintf(s.W, "  %s %s\n", ui.IconDone, s.Success("reassigned to "+assignee))
		case actionUnassign:
			fmt.Fprintf(s.W, "  %s %s\n", ui.IconDone, s.Success("assignment removed"))
		}
	case state == "draft":
		switch action {
		case actionAssign:
			fmt.Fprintf(s.W, "  %s %s\n", ui.IconDone, s.Success("assigned to "+assignee+" on the pending draft"))
		case actionReassign:
			fmt.Fprintf(s.W, "  %s %s\n", ui.IconDone, s.Success("reassigned to "+assignee+" on the pending draft"))
		case actionUnassign:
			fmt.Fprintf(s.W, "  %s %s\n", ui.IconDone, s.Success("assignment removed from the pending draft"))
		}
	default: // unchanged
		switch action {
		case actionAssign, actionReassign:
			fmt.Fprintf(s.W, "  %s %s\n", ui.IconDone, s.Success("already assigned to "+assignee+" — nothing written"))
		case actionUnassign:
			fmt.Fprintf(s.W, "  %s %s\n", ui.IconDone, s.Success("no assignment to remove — nothing written"))
		}
	}
	if state == "published" {
		ui.NewSummary(s).
			Add("Instance Version", "unchanged").
			Add("Object Hash", hash).
			Add("Next", "run 'eka sync push' to refresh the repository snapshot").
			Render()
	}
}

// titleCase capitalizes the first letter of an ASCII word.
func titleCase(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
