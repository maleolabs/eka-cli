package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
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

// This file implements `eka retire <target>` (soft-delete, never hard
// delete): publish a NEW instance (highest+1) of the line with
// existence-state flipped to retired/archived and an appended
// existence-state change-log entry. Content, relationships and every
// other state domain stay frozen; the old instances stay in the
// content-addressed archive untouched; no row is ever deleted.
//
// The command is a thin Cobra layer over the store primitives — the
// third documented exception to the client-only boundary in root.go
// (after `eka project register` and the assignment edge writes of
// cmd/assigned.go): the Authoring API has no retire operation (relate
// is edge-add only, transition re-points in place at the SAME instance
// version), so the retire write mirrors the publish new-instance
// mechanism instead — MaxInstanceVersion+1, CKO-level ValidateCKO with
// the store resolver, store.PutUnit with the preserved provenance
// pair — the same faithful-copy discipline as writeAssignmentPublished
// (copy the runtime mechanism, never drift from it).
//
// Gates (deterministic refusals, exit 1, one line + hint):
//
//	draft-target      the line has no published instance but carries a
//	                  pending draft — hint `eka discard` (retire never
//	                  touches drafts; `eka discard` stays draft-only)
//	unknown line      no published instance and no pending draft
//	already retired   current existence-state already equals the
//	                  requested one — idempotent exit 0, nothing written
//	downstream        another ACTIVE published line references the
//	                  target via depends-on/derives-from/validates/
//	                  supersedes/amends — refused with the sorted
//	                  blocker list (discusses edges never block)
//	protected         an active ctr- cannot retire; an approved or
//	                  immutable plan- depended-on by an active/planned
//	                  container cannot retire
//
// --reason (min 10 chars) and --by (flag or `git config user.name`)
// are required; outside a terminal --force is required (mirror of the
// discard gate — it never bypasses any other gate); --dry-run is
// read-only. --cascade=cmt additionally retires the active cmt- notes
// discussing the target (each its own new instance); any other
// downstream type still blocks.
//
// Exit codes (the transition/note contract):
//
//	0  retired (published), already-retired (unchanged), or dry-run
//	   (nothing written)
//	1  refusal (draft-target, unknown line, downstream blockers,
//	   protected target, CKO-level validation findings)
//	2  usage or internal error (versioned target, bad --as/--cascade,
//	   missing/short --reason, unresolved --by source, non-TTY
//	   without --force, workspace errors)
//
// --json emits the deterministic machine report (schema
// "eka-retire-v1"); the default output is the human report in the CLI
// house style.
//
// NOTE (model deviation, documented): exchange.ChangeLogEntry carries
// {date, domain, from, to, by} — no reason slot — so the retire reason
// travels in the command's machine/human report, not in the stored
// change-log entry. Adding a slot would change the canonical RSF
// serialization (an eka-core change); the stored entry follows the
// transition convention exactly (domain existence-state, from, to,
// by, date).

// retireSchema is the schema id of the retire machine report.
const retireSchema = "eka-retire-v1"

// retireTargetJSON is the addressed line and its instance movement.
type retireTargetJSON struct {
	CanonicalForm string `json:"canonicalForm"`
	PrevInstance  int    `json:"prevInstance"`
	NewInstance   int    `json:"newInstance"`
}

// retireExistenceJSON is the existence-state movement.
type retireExistenceJSON struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// retireDownstreamJSON carries the downstream reference sets: the
// ACTIVE lines that block the retire (depends-on/derives-from/
// validates/supersedes/amends into the target — sorted canonical line
// forms) and the references that stay behind (retained — every other
// downstream edge into the target, e.g. cmt- discusses without
// --cascade=cmt, or edges from already-retired lines).
type retireDownstreamJSON struct {
	Blockers []string `json:"blockers"`
	Retained []string `json:"retained"`
}

// retireCascadeJSON carries the cascade mode and the cascaded lines.
type retireCascadeJSON struct {
	Mode     string   `json:"mode"`
	Cascaded []string `json:"cascaded"`
}

// retireMembershipJSON carries the ticket membership of a ticket
// retire: the owning container, the registered work item and the
// container's state (each "" when unresolving). Present on ticket
// targets only.
type retireMembershipJSON struct {
	Container      string `json:"container"`
	WorkItem       string `json:"workItem"`
	ContainerState string `json:"containerState"`
}

// retireGateImpactJSON carries the container all-done gate impact of a
// ticket retire: the pending work items still blocking completion
// (sorted "type:id (state)" forms — a retired ticket stays a blocker
// until it is explicitly unlinked, fail-closed) and whether the
// container is completable.
type retireGateImpactJSON struct {
	AllDoneBlockedBy []string `json:"allDoneBlockedBy"`
	Completable      bool     `json:"completable"`
}

// retireJSON is the deterministic machine report of one retire run
// (schema "eka-retire-v1"; pinned field order).
type retireJSON struct {
	Schema         string                `json:"schema"`
	Target         *retireTargetJSON     `json:"target,omitempty"`
	ExistenceState *retireExistenceJSON  `json:"existenceState,omitempty"`
	Reason         string                `json:"reason,omitempty"`
	By             string                `json:"by,omitempty"`
	ByKind         string                `json:"byKind,omitempty"`
	Downstream     *retireDownstreamJSON `json:"downstream,omitempty"`
	Cascade        *retireCascadeJSON    `json:"cascade,omitempty"`
	Status         string                `json:"status,omitempty"`
	ObjectHash     string                `json:"objectHash,omitempty"`
	DryRun         bool                  `json:"dryRun,omitempty"`
	// Membership and GateImpact are present on ticket (tkt-) targets
	// only: the owning container/work-item/container-state and the
	// container all-done gate impact (a retired ticket stays a
	// blocker until explicitly unlinked).
	Membership *retireMembershipJSON `json:"membership,omitempty"`
	GateImpact *retireGateImpactJSON `json:"gateImpact,omitempty"`
	Hint       string                `json:"hint,omitempty"`
}

// retireRefusal is a deterministic refusal (exit 1) of the retire
// command: reason + hint, nothing was written.
type retireRefusal struct {
	reason string
	hint   string
}

// Error renders the deterministic refusal message.
func (e *retireRefusal) Error() string {
	return fmt.Sprintf("%s; %s", e.reason, e.hint)
}

// retireBlockerRels are the relationship types that make an active
// downstream line block a retire. discusses never blocks (notes observe
// the target; --cascade=cmt retires them alongside instead).
var retireBlockerRels = []string{"depends-on", "derives-from", "validates", "supersedes", "amends"}

// newRetireCommand builds `eka retire <target> --reason "..."`.
func newRetireCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "retire <target> --reason \"...\"",
		Short: "Retire a published artifact line (soft-delete)",
		Long: `Retire a published artifact line: publish a NEW instance (highest+1)
with existence-state retired (or archived via --as) and an appended
existence-state change-log entry. Content, relationships and every
other state domain stay frozen; the old instances stay in the
content-addressed archive untouched; no row is ever deleted. This is
the soft-delete — there is no hard delete ('eka purge' does not
exist).

The target is the artifact line: <type>:<id> (unqualified — the
repository namespace applies) or <ns>/<type>:<id> (qualified — the
namespace must equal the repository's). A versioned form (<type>:<id>:<v>)
is refused: retire addresses the line, never one immutable instance.

Retire is refused when the line has no published instance but carries
a pending draft (use 'eka discard' — retire never touches drafts),
when the line is unknown, when another ACTIVE published line
references the target via depends-on/derives-from/validates/
supersedes/amends (the sorted blocker list; discusses edges never
block), when the target is an active container (ctr-), or when the
target is an approved/immutable plan depended-on by an active or
planned container. Retiring an already-retired line is an idempotent
no-op (exit 0, nothing written). --cascade=cmt additionally retires
the active cmt- notes discussing the target.

Tickets (tkt-) retire through a zero-owned-state branch: a ticket
owns no state and no change-log domain, so the new instance carries
the retirement marker in content instead of an existence-state flip
(edges frozen). A retired ticket stays a container all-done blocker
until it is explicitly unlinked — retire never removes edges
(fail-closed, never auto-excluded). Retire refuses when the owning
container is completed (history-locked), and when it is active while
the registered work item is still pending (transition the work item
to done/canceled first, or --force with the reason recorded).
Retiring a work item referenced by an ACTIVE ticket is refused
(downstream-blocker — never orphan a live ticket); retired tickets
do not block. --dry-run on a ticket shows the membership
(container, work item, container state) and the gate impact
(allDoneBlockedBy, completable).

Retire vs unlink: retire WITHDRAWS a line (new instance, edges
intact); unlink ('eka unrelate') EDITS ticket membership (removes one
derives-from edge, same instance version). To drop a ticket from a
wave: retire it first (reason recorded), then unlink it.

--reason (minimum 10 characters) and the change-log authority --by
(default: ` + "`git config user.name`" + `) are required. Outside a
terminal --force is required (agents decide programmatically; --force
never bypasses any other gate). --dry-run prints the plan and writes
nothing.

Exit codes:
  0  retired, already-retired (unchanged), or dry-run
  1  refused (draft-target, unknown line, downstream blockers,
     protected target, validation findings)
  2  usage or internal error (versioned target, bad --as/--cascade,
     missing/short --reason, unresolved --by source, non-TTY
     without --force)`,
		Example: `  eka retire sto:legacy --reason "superseded by sto:next-gen" --force
  eka retire adr:003 --reason "decision revisited in Q3" --as archived --force
  eka retire sto:old --reason "obsolete after migration" --cascade=cmt --force --json
  eka retire sto:old --reason "obsolete after migration" --dry-run --force`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRetire(cmd, args[0])
		},
	}
	cmd.Flags().String("reason", "", "retire reason (required, minimum 10 characters)")
	cmd.Flags().String("by", "", "change-log authority name (default: `git config user.name`)")
	cmd.Flags().String("by-kind", "", "author identity kind: user, agent, or worker (default: user)")
	cmd.Flags().String("as", "retired", "existence-state to publish: retired or archived")
	cmd.Flags().String("cascade", "", "cascade mode: cmt (retire active cmt- notes discussing the target alongside)")
	cmd.Flags().Bool("force", false, "required outside a terminal; it never bypasses any retire gate")
	cmd.Flags().Bool("dry-run", false, "print the retire plan without writing anything")
	cmd.Flags().Bool("json", false, "emit the deterministic machine report (schema eka-retire-v1)")
	return cmd
}

// runRetire executes one retire run: flag gates, target resolution,
// the refusal gates, then the dry-run report or the new-instance
// publish (plus the cascade publishes).
func runRetire(cmd *cobra.Command, target string) error {
	reason, _ := cmd.Flags().GetString("reason")
	byFlag, _ := cmd.Flags().GetString("by")
	byKindFlag, _ := cmd.Flags().GetString("by-kind")
	asFlag, _ := cmd.Flags().GetString("as")
	cascadeFlag, _ := cmd.Flags().GetString("cascade")
	force, _ := cmd.Flags().GetBool("force")
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	jsonOut, _ := cmd.Flags().GetBool("json")

	to := strings.TrimSpace(asFlag)
	if to != "retired" && to != "archived" {
		return retireUsage(cmd, jsonOut, fmt.Sprintf("invalid --as %q: the existence-state is retired or archived", asFlag))
	}
	cascadeMode := strings.TrimSpace(cascadeFlag)
	if cascadeMode != "" && cascadeMode != "cmt" {
		return retireUsage(cmd, jsonOut, fmt.Sprintf("invalid --cascade %q: the only cascade mode is cmt", cascadeFlag))
	}
	if strings.TrimSpace(reason) == "" {
		return retireUsage(cmd, jsonOut, "retire requires --reason \"...\": the retire reason (minimum 10 characters)")
	}
	if len([]rune(strings.TrimSpace(reason))) < 10 {
		return retireUsage(cmd, jsonOut, "retire requires --reason of at least 10 characters")
	}
	by, err := runtime.BySource(byFlag, byKindFlag, ".")
	if err != nil {
		return retireUsage(cmd, jsonOut, err.Error())
	}
	if !force && !isTTYReader(cmd.InOrStdin()) {
		return retireUsage(cmd, jsonOut, "retire requires --force when not running in a terminal")
	}

	ref, err := conformance.ParseReference(target, "", "")
	if err != nil {
		return retireUsage(cmd, jsonOut, fmt.Sprintf("invalid target %q: %v", target, err))
	}
	if ref.HasVersion {
		return retireUsage(cmd, jsonOut, fmt.Sprintf("%s is a canonical published form; retire addresses the artifact line", target))
	}

	r, err := openAuthoringRuntime(cmd)
	if err != nil {
		return err // Exit 2: workspace resolution.
	}
	defer r.Close()

	ctx, err := resolveRetireTarget(r, ref)
	if err != nil {
		var refusal *retireRefusal
		if errors.As(err, &refusal) {
			return retireRefused(cmd, jsonOut, nil, refusal.reason, refusal.hint)
		}
		return retireUsage(cmd, jsonOut, err.Error())
	}
	// The namespace-defaulted line identity (an unqualified target
	// resolves to the repository namespace inside resolveRetireTarget).
	ref = ctx.ref

	// Tickets (tkt-) retire through their own branch: a ticket owns no
	// state domain (R4) and no change-log domain (R7), so the
	// existence-state flip below would fail CKO-level validation. The
	// ticket retire publishes a new instance carrying the retirement
	// marker in content instead (edges frozen — a retired ticket stays
	// an all-done blocker until explicitly unlinked).
	if ref.Type == "tkt" {
		return runRetireTicket(cmd, ctx, ref, reason, by, force, dryRun, jsonOut, asFlag, cascadeMode)
	}

	from := ctx.unit.StateVector.ExistenceState
	if from == "" {
		from = "active" // Pre-existence-state payloads are live knowledge.
	}
	lineForm := view.LineForm(ref.Namespace, ref.Type, ref.ID)
	prevInstance := ctx.unit.Identity.InstanceVersion

	// Idempotent: already in the requested existence-state — exit 0,
	// no new revision, nothing written.
	if from == to {
		report := baseRetireReport(lineForm, prevInstance, prevInstance, from, to, reason, by, cascadeMode)
		report.Status = "already-retired"
		if jsonOut {
			return emitJSON(cmd, report)
		}
		renderRetireResult(styleFor(cmd), lineForm, prevInstance, prevInstance, from, to, "already-retired", "", nil, nil, by)
		return nil
	}
	if from != "active" {
		// A non-active line in a DIFFERENT non-active state (retired vs
		// archived) never moves between the two — the states are
		// terminal rest states, not a lifecycle.
		return retireRefused(cmd, jsonOut, nil,
			fmt.Sprintf("%s is already %s; it cannot move to %s", lineForm, from, to),
			"retired and archived are terminal rest states")
	}

	// The protected gates (before the downstream scan so the message
	// names the real blocker).
	if err := retireProtectedGuard(ctx.units, ref, lineForm); err != nil {
		var refusal *retireRefusal
		if errors.As(err, &refusal) {
			return retireRefused(cmd, jsonOut, nil, refusal.reason, refusal.hint)
		}
		return fmt.Errorf("retire: %w", err)
	}

	blockers, retained, cascaded := scanRetireDownstream(ctx.units, ref, cascadeMode)

	if len(blockers) > 0 {
		report := baseRetireReport(lineForm, prevInstance, prevInstance+1, from, to, reason, by, cascadeMode)
		report.Status = "refused"
		report.Downstream.Blockers = blockers
		report.Downstream.Retained = retained
		report.Cascade.Cascaded = cascaded
		return retireRefused(cmd, jsonOut, &report,
			fmt.Sprintf("%s is still referenced by %d active downstream line(s): %s", lineForm, len(blockers), strings.Join(blockers, ", ")),
			"retire the downstream lines first, or remove their references")
	}

	if dryRun {
		report := baseRetireReport(lineForm, prevInstance, prevInstance+1, from, to, reason, by, cascadeMode)
		report.Status = "dry-run"
		report.DryRun = true
		report.Downstream.Blockers = blockers
		report.Downstream.Retained = retained
		report.Cascade.Cascaded = cascaded
		if jsonOut {
			return emitJSON(cmd, report)
		}
		renderRetireDryRun(styleFor(cmd), lineForm, prevInstance, prevInstance+1, from, to, retained, cascaded, cascadeMode, by)
		return nil
	}

	hash, err := publishRetire(ctx, to, by)
	if err != nil {
		var refusal *retireRefusal
		if errors.As(err, &refusal) {
			return retireRefused(cmd, jsonOut, nil, refusal.reason, refusal.hint)
		}
		var ve *retireValidationError
		if errors.As(err, &ve) {
			printCKOReport(styleFor(cmd), ve.Target, ve.Report)
			fmt.Fprintf(cmd.ErrOrStderr(), "eka: retire refused: %s\n", err)
			return &exitError{code: exitFail}
		}
		return err // Exit 2: internal.
	}

	// The cascade publishes (each its own new instance, same authority
	// and reason lineage): a failed cascade line refuses AFTER the
	// target was already published — the target retire stands (it was
	// valid on its own), the error names the cascade line.
	cascadedForms, cerr := publishRetireCascade(ctx, cascaded, to, by)
	if cerr != nil {
		var refusal *retireRefusal
		if errors.As(cerr, &refusal) {
			fmt.Fprintf(cmd.ErrOrStderr(), "eka: retire refused: %s; %s\n", refusal.reason, refusal.hint)
			return &exitError{code: exitFail}
		}
		return cerr
	}

	report := baseRetireReport(lineForm, prevInstance, prevInstance+1, from, to, reason, by, cascadeMode)
	report.Status = "retired"
	report.ObjectHash = hash
	report.Downstream.Blockers = blockers
	report.Downstream.Retained = retained
	report.Cascade.Cascaded = cascadedForms
	if jsonOut {
		return emitJSON(cmd, report)
	}
	renderRetireResult(styleFor(cmd), lineForm, prevInstance, prevInstance+1, from, to, "retired", hash, retained, cascadedForms, by)
	return nil
}

// retireTarget is the resolved context of one retire run.
type retireTarget struct {
	ref     conformance.Reference // the artifact line (namespace/type/id)
	project string                // the registered project of the repository
	units   []*exchange.Unit      // every unit of the project
	unit    *exchange.Unit        // the line's current (highest) instance; nil when only a draft exists
}

// resolveRetireTarget resolves the retire target and its repository
// context: the line-form gate (usage errors), the repository-context
// gate (ADR-018), the namespace gate (the relate/transition ownership
// gate), the project units and the line's current instance. The
// draft-target and unknown-line gates are deterministic refusals (exit
// 1); malformed targets are usage errors (exit 2).
func resolveRetireTarget(r *runtime.Runtime, ref conformance.Reference) (*retireTarget, error) {
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
		return nil, &retireRefusal{
			reason: fmt.Sprintf("%s is not an EKA repository (no eka.yaml)", abs),
			hint:   "run 'eka init' first",
		}
	}
	repo, found, err := r.Workspace.FindRepo(abs)
	if err != nil {
		return nil, fmt.Errorf("cannot resolve the repository registration: %w", err) // Exit 2.
	}
	if !found {
		return nil, &retireRefusal{
			reason: fmt.Sprintf("repository %s is not registered in the EKA workspace", abs),
			hint:   "run 'eka sync' (auto-registers) or 'eka project register' first",
		}
	}
	ns := meta.Namespace
	if ns == "" {
		ns = repo.Namespace
	}
	if ref.Namespace != "" {
		if ref.Namespace != ns {
			return nil, &retireRefusal{
				reason: fmt.Sprintf("target namespace %s differs from the repository namespace %s; cross-platform access is read-only", ref.Namespace, ns),
				hint:   "retire only artifacts of the repository's own namespace",
			}
		}
	} else {
		ref.Namespace = ns
	}
	units, err := r.Knowledge.UnitsByProject(repo.ProjectID)
	if err != nil {
		return nil, fmt.Errorf("cannot read the project knowledge: %w", err) // Exit 2: store failure.
	}
	ctx := &retireTarget{
		ref:     ref,
		project: repo.ProjectID,
		units:   units,
		unit:    currentLineUnit(units, ref),
	}
	if ctx.unit == nil {
		form := view.LineForm(ref.Namespace, ref.Type, ref.ID)
		root, herr := workspace.HomeDir()
		if herr != nil {
			return nil, fmt.Errorf("cannot resolve the workspace root: %w", herr) // Exit 2.
		}
		_, hasPendingDraft, derr := pendingDraftPath(root, ctx.project, ref.Type, ref.ID)
		if derr != nil {
			return nil, derr // Exit 2: draft resolution failure.
		}
		if hasPendingDraft {
			return nil, &retireRefusal{
				reason: fmt.Sprintf("%s is a draft, not a published line; retire applies to published lines only", form),
				hint:   "use 'eka discard' to drop the draft",
			}
		}
		return nil, &retireRefusal{
			reason: fmt.Sprintf("line not found: %s is not in the workspace store", form),
			hint:   "run 'eka sync' first, then pass a published <ns>/<type>:<id> line",
		}
	}
	return ctx, nil
}

// retireProtectedGuard enforces the protected-target gates: an active
// container (ctr-) cannot retire, and an approved/immutable plan
// depended-on by an active or planned container cannot retire.
func retireProtectedGuard(units []*exchange.Unit, ref conformance.Reference, lineForm string) error {
	byLine := highestByLine(units)
	if ref.Type == "ctr" {
		if cur := byLine[lineForm]; cur != nil && cur.StateVector.ContainerState == "active" {
			return &retireRefusal{
				reason: fmt.Sprintf("container %s is active; an active container cannot retire", lineForm),
				hint:   "complete the container first ('eka transition ctr:<id> completed')",
			}
		}
	}
	if ref.Type == "plan" {
		cur := byLine[lineForm]
		if cur != nil && (cur.StateVector.PlanningState == "approved" || cur.StateVector.PlanningState == "immutable") {
			var holders []string
			for line, u := range byLine {
				if u.Identity.Type != "ctr" {
					continue
				}
				if u.StateVector.ContainerState != "active" && u.StateVector.ContainerState != "planned" {
					continue
				}
				for _, rel := range u.Relationships {
					if rel.Type != "depends-on" {
						continue
					}
					if retireRefResolvesTo(rel.Target, u.Identity.Namespace, u.Identity.Type, ref) {
						holders = append(holders, line+" ("+u.StateVector.ContainerState+")")
						break
					}
				}
			}
			if len(holders) > 0 {
				sort.Strings(holders)
				return &retireRefusal{
					reason: fmt.Sprintf("plan %s is %s and still depended-on by %s", lineForm, cur.StateVector.PlanningState, strings.Join(holders, ", ")),
					hint:   "retire or re-point the holding containers first",
				}
			}
		}
	}
	return nil
}

// scanRetireDownstream scans the project's current (highest-per-line)
// units for downstream references into the target: blockers (active
// lines via the retireBlockerRels types), retained (every other edge
// into the target that stays behind), and cascaded (active cmt- lines
// discussing the target — only when mode == "cmt"). All three lists
// are sorted canonical line forms.
func scanRetireDownstream(units []*exchange.Unit, ref conformance.Reference, mode string) (blockers, retained, cascaded []string) {
	byLine := highestByLine(units)
	targetLine := view.LineForm(ref.Namespace, ref.Type, ref.ID)
	blockers = []string{}
	retained = []string{}
	cascaded = []string{}
	seen := map[string]bool{}
	for line, u := range byLine {
		if line == targetLine {
			continue // The target never blocks or retains itself.
		}
		active := u.StateVector.ExistenceState == "" || u.StateVector.ExistenceState == "active"
		if u.Identity.Type == "tkt" && ticketRetired(u) {
			// A retired ticket is withdrawn: it never orphan-blocks
			// the retire of its work item (only ACTIVE tickets do —
			// "jangan yatimkan ticket"). It stays behind as retained
			// (its edges are frozen — it still blocks the container
			// all-done gate until explicitly unlinked).
			active = false
		}
		var blocks, discusses bool
		var touches bool
		for _, rel := range u.Relationships {
			if !retireRefResolvesTo(rel.Target, u.Identity.Namespace, u.Identity.Type, ref) {
				continue
			}
			touches = true
			switch {
			case rel.Type == "discusses" && u.Identity.Type == "cmt":
				discusses = true
			case isRetireBlockerRel(rel.Type):
				blocks = true
			}
		}
		if !touches {
			continue
		}
		switch {
		case blocks && active:
			if !seen["b\x00"+line] {
				seen["b\x00"+line] = true
				blockers = append(blockers, line)
			}
		case discusses && active && mode == "cmt":
			if !seen["c\x00"+line] {
				seen["c\x00"+line] = true
				cascaded = append(cascaded, line)
			}
		default:
			if !seen["r\x00"+line] {
				seen["r\x00"+line] = true
				retained = append(retained, line)
			}
		}
	}
	sort.Strings(blockers)
	sort.Strings(retained)
	sort.Strings(cascaded)
	return blockers, retained, cascaded
}

// isRetireBlockerRel reports whether the relationship type blocks a
// retire of its target.
func isRetireBlockerRel(relType string) bool {
	for _, t := range retireBlockerRels {
		if relType == t {
			return true
		}
	}
	return false
}

// retireRefResolvesTo reports whether the raw stored relationship
// target resolves to the retire target line (namespace/type/id
// comparison — version-insensitive, like the transition R9 check).
func retireRefResolvesTo(raw, sourceNS, sourceType string, target conformance.Reference) bool {
	parsed, err := conformance.ParseReference(raw, sourceNS, sourceType)
	if err != nil {
		return false
	}
	return parsed.Namespace == target.Namespace && parsed.Type == target.Type && parsed.ID == target.ID
}

// highestByLine indexes the current (highest instance) unit per
// canonical line form.
func highestByLine(units []*exchange.Unit) map[string]*exchange.Unit {
	byLine := make(map[string]*exchange.Unit, len(units))
	for _, u := range units {
		key := view.LineForm(u.Identity.Namespace, u.Identity.Type, u.Identity.ID)
		if cur, ok := byLine[key]; !ok || u.Identity.InstanceVersion > cur.Identity.InstanceVersion {
			byLine[key] = u
		}
	}
	return byLine
}

// retireValidationError reports that the would-be retired unit failed
// CKO-level validation (the standard publish validation); nothing was
// written.
type retireValidationError struct {
	// Target is the line form the retire addressed.
	Target string
	// Report is the CKO-level validation report.
	Report *conformance.Report
}

// Error renders the deterministic refusal message.
func (e *retireValidationError) Error() string {
	return fmt.Sprintf("%s failed CKO-level validation with %d blocking error(s); nothing was changed",
		e.Target, e.Report.ErrorCount())
}

// publishRetire publishes the retire of the context line: the
// new-instance write (highest+1) with the existence-state flip and the
// appended change-log entry. It returns the new payload's object hash.
func publishRetire(ctx *retireTarget, to string, by conformance.AuthorIdentity) (string, error) {
	ws, err := workspace.Ensure()
	if err != nil {
		return "", err // Exit 2: workspace resolution.
	}
	defer ws.Close()
	return publishRetireLineCtx(ws.Store(), ctx.project, ctx.ref, to, by)
}

// publishRetireLine publishes the retire of an arbitrary line of the
// context project (the target or one cascade cmt-): the fresh highest
// instance is re-read from the store (forward-only P7), so a cascade
// line published earlier in the same run is never clobbered.
func publishRetireLine(ctx *retireTarget, ref conformance.Reference, to string, by conformance.AuthorIdentity) (string, error) {
	ws, err := workspace.Ensure()
	if err != nil {
		return "", err // Exit 2: workspace resolution.
	}
	defer ws.Close()
	return publishRetireLineCtx(ws.Store(), ctx.project, ref, to, by)
}

// publishRetireLineCtx performs the retire new-instance write against
// an open store: copy the line's current (highest) instance, freeze
// everything except existence-state/updated/change-log, validate at
// CKO level with the store resolver (the publish discipline of
// draft.go Publish and writeAssignmentPublished), and insert via
// store.PutUnit with the preserved provenance pair (the tombstone
// propagates through `eka sync push` like any other publish —
// integrity stays green, the old payloads stay archived).
func publishRetireLineCtx(st *store.Store, project string, ref conformance.Reference, to string, by conformance.AuthorIdentity) (string, error) {
	line, err := st.UnitsByLine(ref.Namespace, ref.Type, ref.ID)
	if err != nil {
		return "", fmt.Errorf("retire: %w", err) // Exit 2: store failure.
	}
	var current *exchange.Unit
	for _, u := range line {
		if current == nil || u.Identity.InstanceVersion > current.Identity.InstanceVersion {
			current = u
		}
	}
	lineForm := view.LineForm(ref.Namespace, ref.Type, ref.ID)
	if current == nil {
		return "", &retireRefusal{
			reason: fmt.Sprintf("line not found: %s is not in the workspace store", lineForm),
			hint:   "run 'eka sync' first, then pass a published <ns>/<type>:<id> line",
		}
	}
	from := current.StateVector.ExistenceState
	if from == "" {
		from = "active"
	}
	if from != "active" {
		return "", &retireRefusal{
			reason: fmt.Sprintf("%s is already %s; it cannot move to %s", lineForm, from, to),
			hint:   "retired and archived are terminal rest states",
		}
	}
	max, err := st.MaxInstanceVersion(ref.Namespace, ref.Type, ref.ID)
	if err != nil {
		return "", fmt.Errorf("retire: %w", err) // Exit 2: store failure.
	}
	newVersion := max + 1
	if newVersion <= current.Identity.InstanceVersion {
		newVersion = current.Identity.InstanceVersion + 1
	}

	// The would-be unit: everything frozen except the existence-state,
	// the updated date and the appended change-log entry (the
	// transition publish convention).
	today := time.Now().Format("2006-01-02")
	next := *current // shallow copy; ChangeLog below is rebuilt, nothing else is mutated.
	next.Identity.InstanceVersion = newVersion
	next.CanonicalIdentityForm = next.Identity.CanonicalForm()
	next.StateVector.ExistenceState = to
	next.Updated = today
	next.ChangeLog = append(append([]exchange.ChangeLogEntry{}, current.ChangeLog...), exchange.ChangeLogEntry{
		Date: today, Domain: conformance.DomainExistenceState, From: from, To: to, By: by,
	})

	// The standard publish validation (mirror of writeAssignmentPublished):
	// the would-be unit must validate at CKO level with the store
	// resolver (Rule 5 reference resolution plus the structural
	// checks). R13 transition gates do not apply to retire.
	resolver := &assignmentStoreResolver{st: st}
	report, err := conformance.ValidateCKO(&next, conformance.ValidateCKOOptions{
		Resolve: resolver.Resolve,
	})
	if err != nil {
		return "", fmt.Errorf("retire: validation failed: %w", err)
	}
	report.Results = append(report.Results,
		resolver.Findings(next.CanonicalIdentityForm, next.StateVector.ContentState)...)
	if !report.Pass() {
		return "", &retireValidationError{Target: lineForm, Report: report}
	}

	// The provenance pair is preserved from the current reference (like
	// writeAssignmentPublished): the tombstone stays attributed to the
	// repository that owns the line.
	curRef, ok, err := st.Ref(current.CanonicalIdentityForm)
	if err != nil {
		return "", fmt.Errorf("retire: %w", err)
	}
	if !ok {
		return "", &retireRefusal{
			reason: fmt.Sprintf("the reference of %s is missing (store corruption)", current.CanonicalIdentityForm),
			hint:   "run 'eka integrity check'",
		}
	}
	unitJSON, err := exchange.MarshalUnit(&next)
	if err != nil {
		return "", fmt.Errorf("retire: cannot serialize %s: %w", next.CanonicalIdentityForm, err)
	}
	hash, _, err := st.PutUnit(unitJSON, next.ContentPayload, store.Ref{
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
		UpdatedAt:       today,
	})
	if err != nil {
		return "", fmt.Errorf("retire: cannot publish %s: %w", next.CanonicalIdentityForm, err)
	}
	return hash, nil
}

// ---------------------------------------------------------------------------
// Ticket retire (tkt-): the zero-owned-state special case (Opsi A).
//
// A ticket owns no state domain (R4) and no change-log domain (R7),
// so the general existence-state flip cannot apply: ValidateCKO would
// refuse the would-be unit. The ticket retire instead publishes a NEW
// instance (highest+1) with everything frozen — zero state vector,
// empty change-log, same derives-from edges — plus the retirement
// marker in content (the "retirement" object: date/by/byKind/reason;
// R9 constrains content only to the required keys, so the additive
// marker validates clean).
//
// Consequences (fail-closed, documented):
//
//   - the retired ticket keeps its membership: the container all-done
//     gate reads edges, not the marker, so a retired ticket STAYS a
//     blocker until it is explicitly unlinked (`eka unrelate`). The
//     dry-run spells this out via membership + gateImpact.
//   - the marker makes the ticket "withdrawn" for the downstream scan:
//     a retired ticket never blocks the retire of its work item (only
//     ACTIVE tickets orphan-block — "jangan yatimkan ticket").
//   - container gates: planned allows; active allows only when the
//     registered work item is done/canceled (or with --force, the
//     reason recorded); completed is history-locked (absolute).
// ---------------------------------------------------------------------------

// runRetireTicket executes the ticket branch of `eka retire`: the
// ticket gates, then the dry-run report or the marker-instance publish
// (plus the cascade publishes, reused from the general path).
func runRetireTicket(cmd *cobra.Command, ctx *retireTarget, ref conformance.Reference, reason string, by conformance.AuthorIdentity, force, dryRun, jsonOut bool, asFlag, cascadeMode string) error {
	lineForm := view.LineForm(ref.Namespace, ref.Type, ref.ID)
	prevInstance := ctx.unit.Identity.InstanceVersion
	if strings.TrimSpace(asFlag) != "retired" {
		return retireUsage(cmd, jsonOut, "retiring a ticket supports --as retired only: tickets own no existence-state (archived is not a ticket state)")
	}

	membershipOf := func() (*retireMembershipJSON, string, string) {
		containerLine, workItemLine := ticketMembershipOf(ctx.units, ctx.unit)
		byLine := highestByLine(ctx.units)
		var containerState, workItemState string
		if containerLine != "" {
			if u := byLine[containerLine]; u != nil {
				containerState = u.StateVector.ContainerState
			}
		}
		if workItemLine != "" {
			wref, err := conformance.ParseReference(workItemLine, "", "")
			if err == nil {
				if u := byLine[view.LineForm(wref.Namespace, wref.Type, wref.ID)]; u != nil {
					workItemState = u.StateVector.ExecutionState
				}
			}
		}
		return &retireMembershipJSON{Container: containerLine, WorkItem: workItemLine, ContainerState: containerState}, containerLine, workItemState
	}

	// Idempotent: the current instance already carries the retirement
	// marker — exit 0, nothing written.
	if ticketRetired(ctx.unit) {
		membership, containerLine, _ := membershipOf()
		report := baseRetireReport(lineForm, prevInstance, prevInstance, "active", "retired", reason, by, cascadeMode)
		report.Status = "already-retired"
		report.ExistenceState = nil // Tickets own no existence-state; there is no movement to report.
		report.Membership = membership
		report.GateImpact = ticketGateImpact(ctx.units, containerLine)
		if jsonOut {
			return emitJSON(cmd, report)
		}
		renderTicketRetireResult(styleFor(cmd), lineForm, prevInstance, prevInstance, "already-retired", "", by, membership, report.GateImpact)
		return nil
	}

	membership, containerLine, workItemState := membershipOf()
	if refusal := ticketRetireGate(lineForm, containerLine, membership.ContainerState, membership.WorkItem, workItemState, force); refusal != nil {
		return retireRefused(cmd, jsonOut, nil, refusal.reason, refusal.hint)
	}

	blockers, retained, cascaded := scanRetireDownstream(ctx.units, ref, cascadeMode)
	if len(blockers) > 0 {
		report := baseRetireReport(lineForm, prevInstance, prevInstance+1, "active", "retired", reason, by, cascadeMode)
		report.Status = "refused"
		report.ExistenceState = nil
		report.Membership = membership
		report.GateImpact = ticketGateImpact(ctx.units, containerLine)
		report.Downstream.Blockers = blockers
		report.Downstream.Retained = retained
		report.Cascade.Cascaded = cascaded
		return retireRefused(cmd, jsonOut, &report,
			fmt.Sprintf("%s is still referenced by %d active downstream line(s): %s", lineForm, len(blockers), strings.Join(blockers, ", ")),
			"retire the downstream lines first, or remove their references")
	}

	gateImpact := ticketGateImpact(ctx.units, containerLine)
	if dryRun {
		report := baseRetireReport(lineForm, prevInstance, prevInstance+1, "active", "retired", reason, by, cascadeMode)
		report.Status = "dry-run"
		report.DryRun = true
		report.ExistenceState = nil
		report.Membership = membership
		report.GateImpact = gateImpact
		report.Downstream.Blockers = blockers
		report.Downstream.Retained = retained
		report.Cascade.Cascaded = cascaded
		if jsonOut {
			return emitJSON(cmd, report)
		}
		renderTicketRetireDryRun(styleFor(cmd), lineForm, prevInstance, prevInstance+1, by, membership, gateImpact, retained, cascaded, cascadeMode)
		return nil
	}

	hash, err := publishRetireTicket(ctx, by, reason)
	if err != nil {
		var refusal *retireRefusal
		if errors.As(err, &refusal) {
			return retireRefused(cmd, jsonOut, nil, refusal.reason, refusal.hint)
		}
		var ve *retireValidationError
		if errors.As(err, &ve) {
			printCKOReport(styleFor(cmd), ve.Target, ve.Report)
			fmt.Fprintf(cmd.ErrOrStderr(), "eka: retire refused: %s\n", err)
			return &exitError{code: exitFail}
		}
		return err // Exit 2: internal.
	}

	// The cascade publishes (each its own new instance — the general
	// path; a failed cascade line refuses AFTER the ticket retire
	// stands, the error names the cascade line).
	cascadedForms, cerr := publishRetireCascade(ctx, cascaded, "retired", by)
	if cerr != nil {
		var refusal *retireRefusal
		if errors.As(cerr, &refusal) {
			fmt.Fprintf(cmd.ErrOrStderr(), "eka: retire refused: %s; %s\n", refusal.reason, refusal.hint)
			return &exitError{code: exitFail}
		}
		return cerr
	}

	report := baseRetireReport(lineForm, prevInstance, prevInstance+1, "active", "retired", reason, by, cascadeMode)
	report.Status = "retired"
	report.ObjectHash = hash
	report.ExistenceState = nil
	report.Membership = membership
	report.GateImpact = gateImpact
	report.Downstream.Blockers = blockers
	report.Downstream.Retained = retained
	report.Cascade.Cascaded = cascadedForms
	if jsonOut {
		return emitJSON(cmd, report)
	}
	renderTicketRetireResult(styleFor(cmd), lineForm, prevInstance, prevInstance+1, "retired", hash, by, membership, gateImpact)
	return nil
}

// ticketRetireGate enforces the container gates of a ticket retire
// (Opsi A fail-closed): a ticket without a resolving container is
// broken membership (refuse); a completed container is history-locked
// (absolute — no override); an active container with a still-pending
// work item refuses by default and allows only with force (the reason
// is always recorded — --reason is mandatory on every retire run).
// Planned containers, active containers with done/canceled (or
// work-item-less) tickets always pass. A pure function so the
// fail-closed default is unit-testable without a terminal.
func ticketRetireGate(lineForm, containerLine, containerState, workItemLine, workItemState string, force bool) *retireRefusal {
	if containerLine == "" {
		return &retireRefusal{
			reason: fmt.Sprintf("ticket %s has no resolving container; its membership is broken", lineForm),
			hint:   "run 'eka integrity check' — a ticket must derive from at least one resolving container (R8)",
		}
	}
	switch containerState {
	case "completed":
		return &retireRefusal{
			reason: fmt.Sprintf("container %s is completed; its membership is history-locked", containerLine),
			hint:   "completed containers are immutable history — ticket retire is refused",
		}
	case "active":
		if workItemLine != "" && workItemState != "done" && workItemState != "canceled" && !force {
			return &retireRefusal{
				reason: fmt.Sprintf("container %s is active and work item %s is %s; retire would withdraw a live registration", containerLine, workItemLine, workItemState),
				hint:   fmt.Sprintf("transition %s to done (or canceled) first, or re-run with --force to retire anyway (the reason is recorded)", workItemLine),
			}
		}
	}
	return nil
}

// ticketMembershipOf resolves the container and work-item lines a
// ticket derives from: the first resolving ctr- reference in stored
// order is the container, the first resolving execution-state-owning
// reference is the work item (the CLI mirror of the core
// ticketMembership — same stored-order, first-resolving semantics).
func ticketMembershipOf(units []*exchange.Unit, ticket *exchange.Unit) (containerLine, workItemLine string) {
	byLine := highestByLine(units)
	return ticketMembershipOfUnit(ticket, func(ref conformance.Reference) *exchange.Unit {
		return byLine[view.LineForm(ref.Namespace, ref.Type, ref.ID)]
	})
}

// ticketMembershipOfUnit resolves the membership of one ticket unit
// against the given line resolver.
func ticketMembershipOfUnit(ticket *exchange.Unit, resolve func(conformance.Reference) *exchange.Unit) (containerLine, workItemLine string) {
	if ticket == nil {
		return "", ""
	}
	for _, rel := range ticket.Relationships {
		if rel.Type != "derives-from" {
			continue
		}
		ref, err := conformance.ParseReference(rel.Target, ticket.Identity.Namespace, ticket.Identity.Type)
		if err != nil || resolve(ref) == nil {
			continue
		}
		line := view.LineForm(ref.Namespace, ref.Type, ref.ID)
		switch {
		case containerLine == "" && ref.Type == "ctr":
			containerLine = line
		case workItemLine == "" && conformance.IsWorkItemType(ref.Type):
			workItemLine = line
		}
	}
	return containerLine, workItemLine
}

// ticketGateImpact computes the container all-done gate impact of a
// ticket retire: the pending work items of the container (sorted
// "type:id (state)" forms, the transition all-done gate shape) and
// whether the container is completable. The retiring ticket's own
// work item is included when pending — retirement freezes edges, so
// a retired ticket stays a blocker until explicitly unlinked
// (fail-closed, never auto-excluded).
func ticketGateImpact(units []*exchange.Unit, containerLine string) *retireGateImpactJSON {
	pending := containerGatePending(units, containerLine)
	return &retireGateImpactJSON{AllDoneBlockedBy: pending, Completable: len(pending) == 0}
}

// containerGatePending lists the pending work items of a container
// line: every tkt- unit (any instance — the transition all-done gate
// reads membership off every ticket payload) whose derives-from
// resolves to the container, resolved to the work item's highest
// instance, kept when its execution-state is neither done nor
// canceled. Deterministic: sorted, deduped by work-item line.
func containerGatePending(units []*exchange.Unit, containerLine string) []string {
	pending := []string{}
	if containerLine == "" {
		return pending
	}
	byLine := highestByLine(units)
	resolve := func(ref conformance.Reference) *exchange.Unit {
		return byLine[view.LineForm(ref.Namespace, ref.Type, ref.ID)]
	}
	seen := map[string]bool{}
	for _, u := range units {
		if u.Identity.Type != "tkt" {
			continue
		}
		container, workItem := ticketMembershipOfUnit(u, resolve)
		if container != containerLine || workItem == "" || seen[workItem] {
			continue
		}
		seen[workItem] = true
		wref, err := conformance.ParseReference(workItem, "", "")
		if err != nil {
			continue
		}
		w := resolve(wref)
		if w == nil {
			continue
		}
		if state := w.StateVector.ExecutionState; state != "done" && state != "canceled" {
			pending = append(pending, w.Identity.Type+":"+w.Identity.ID+" ("+state+")")
		}
	}
	sort.Strings(pending)
	return pending
}

// publishRetireTicket publishes the ticket retire: a NEW instance
// (highest+1) with the state vector, change-log and edges frozen and
// the retirement marker added to content. It returns the new payload's
// object hash. The write mirrors publishRetireLineCtx (re-read the
// highest instance — forward-only P7 — CKO-level ValidateCKO with the
// store resolver, store.PutUnit with the preserved provenance pair);
// only the payload delta differs (content marker instead of the
// existence-state flip + change-log entry, which R4/R7 forbid on
// tickets).
func publishRetireTicket(ctx *retireTarget, by conformance.AuthorIdentity, reason string) (string, error) {
	ws, err := workspace.Ensure()
	if err != nil {
		return "", err // Exit 2: workspace resolution.
	}
	defer ws.Close()
	st := ws.Store()

	line, err := st.UnitsByLine(ctx.ref.Namespace, ctx.ref.Type, ctx.ref.ID)
	if err != nil {
		return "", fmt.Errorf("retire: %w", err) // Exit 2: store failure.
	}
	var current *exchange.Unit
	for _, u := range line {
		if current == nil || u.Identity.InstanceVersion > current.Identity.InstanceVersion {
			current = u
		}
	}
	lineForm := view.LineForm(ctx.ref.Namespace, ctx.ref.Type, ctx.ref.ID)
	if current == nil {
		return "", &retireRefusal{
			reason: fmt.Sprintf("line %s not found in the workspace store", lineForm),
			hint:   "run 'eka sync' first, then pass a published <ns>/<type>:<id> line",
		}
	}
	if ticketRetired(current) {
		return "", &retireRefusal{
			reason: fmt.Sprintf("ticket %s is already retired", lineForm),
			hint:   "a retired ticket stays an all-done blocker until it is explicitly unlinked ('eka unrelate')",
		}
	}
	max, err := st.MaxInstanceVersion(ctx.ref.Namespace, ctx.ref.Type, ctx.ref.ID)
	if err != nil {
		return "", fmt.Errorf("retire: %w", err) // Exit 2: store failure.
	}
	newVersion := max + 1
	if newVersion <= current.Identity.InstanceVersion {
		newVersion = current.Identity.InstanceVersion + 1
	}

	// The would-be unit: everything frozen except the updated date and
	// the content retirement marker (a ticket owns no state and no
	// change-log domain — R4/R7).
	if current.Content.Representation != exchange.StructuredJSON {
		return "", &retireRefusal{
			reason: fmt.Sprintf("ticket %s carries a non-JSON content payload, which cannot carry the retirement marker deterministically", lineForm),
			hint:   "run 'eka integrity check'",
		}
	}
	var content map[string]any
	if err := json.Unmarshal(current.ContentPayload, &content); err != nil || content == nil {
		return "", &retireRefusal{
			reason: fmt.Sprintf("ticket %s carries an unreadable content payload", lineForm),
			hint:   "run 'eka integrity check'",
		}
	}
	today := time.Now().Format("2006-01-02")
	content["retirement"] = map[string]any{
		"date":   today,
		"by":     by.Name,
		"byKind": by.Kind,
		"reason": strings.TrimSpace(reason),
	}
	payload, err := json.Marshal(content)
	if err != nil {
		return "", fmt.Errorf("retire: cannot serialize the retirement marker of %s: %w", lineForm, err)
	}
	var indented bytes.Buffer
	if err := json.Indent(&indented, payload, "", "  "); err != nil {
		return "", fmt.Errorf("retire: cannot serialize the retirement marker of %s: %w", lineForm, err)
	}
	indented.WriteByte('\n')

	next := *current // shallow copy; ChangeLog/Relationships below are never mutated.
	next.Identity.InstanceVersion = newVersion
	next.CanonicalIdentityForm = next.Identity.CanonicalForm()
	next.Updated = today
	next.ContentPayload = indented.Bytes()

	// The standard publish validation: the would-be unit must validate
	// at CKO level with the store resolver (R4 zero-state, R7 empty
	// change-log, R8 resolving ctr-, R9 required content keys).
	resolver := &assignmentStoreResolver{st: st}
	report, err := conformance.ValidateCKO(&next, conformance.ValidateCKOOptions{
		Resolve: resolver.Resolve,
	})
	if err != nil {
		return "", fmt.Errorf("retire: validation failed: %w", err)
	}
	report.Results = append(report.Results,
		resolver.Findings(next.CanonicalIdentityForm, next.StateVector.ContentState)...)
	if !report.Pass() {
		return "", &retireValidationError{Target: lineForm, Report: report}
	}

	curRef, ok, err := st.Ref(current.CanonicalIdentityForm)
	if err != nil {
		return "", fmt.Errorf("retire: %w", err)
	}
	if !ok {
		return "", &retireRefusal{
			reason: fmt.Sprintf("the reference of %s is missing (store corruption)", current.CanonicalIdentityForm),
			hint:   "run 'eka integrity check'",
		}
	}
	unitJSON, err := exchange.MarshalUnit(&next)
	if err != nil {
		return "", fmt.Errorf("retire: cannot serialize %s: %w", next.CanonicalIdentityForm, err)
	}
	hash, _, err := st.PutUnit(unitJSON, next.ContentPayload, store.Ref{
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
		UpdatedAt:       today,
	})
	if err != nil {
		return "", fmt.Errorf("retire: cannot publish %s: %w", next.CanonicalIdentityForm, err)
	}
	return hash, nil
}

// publishRetireCascade publishes the retire of every cascade line (the
// target or ticket retire already stands when this runs — a failed
// cascade line refuses without rolling it back). It returns the cascade
// line forms in input order.
func publishRetireCascade(ctx *retireTarget, cascaded []string, to string, by conformance.AuthorIdentity) ([]string, error) {
	forms := make([]string, 0, len(cascaded))
	for _, form := range cascaded {
		cref, err := conformance.ParseReference(form, "", "")
		if err != nil {
			return nil, fmt.Errorf("retire: %w", err)
		}
		if _, err := publishRetireLine(ctx, cref, to, by); err != nil {
			return nil, err
		}
		forms = append(forms, form)
	}
	return forms, nil
}

// renderTicketRetireResult renders one ticket retire outcome: the
// header (target, instances, authority), the retirement line, the
// membership + gate impact (a retired ticket stays a blocker until
// unlinked), and the new object hash on the published path.
func renderTicketRetireResult(s *ui.Style, target string, prevInstance, newInstance int, status, hash string, by conformance.AuthorIdentity, membership *retireMembershipJSON, gateImpact *retireGateImpactJSON) {
	header := ui.NewHeader(s, "Retire").
		Add("Target", target).
		Add("Instance", fmt.Sprintf("%d -> %d", prevInstance, newInstance)).
		Add("By", by.Name)
	header.Pipeline("Retire").Render()
	switch status {
	case "retired":
		fmt.Fprintf(s.W, "  %s %s\n", ui.IconDone, s.Success(target+" retired (withdrawal marker published; edges frozen)"))
	case "already-retired":
		fmt.Fprintf(s.W, "  %s %s\n", ui.IconDone, s.Success(target+" is already retired — nothing written"))
	}
	renderTicketMembership(s, membership, gateImpact)
	if status == "retired" {
		ui.NewSummary(s).
			Add("Object Hash", hash).
			Add("Next", "unlink explicitly ('eka unrelate') to drop the membership; run 'eka sync push' to refresh the repository snapshot").
			Render()
	}
}

// renderTicketRetireDryRun renders the read-only ticket retire plan:
// the instance movement, the membership, the gate impact (what stays
// blocked), and the downstream sets.
func renderTicketRetireDryRun(s *ui.Style, target string, prevInstance, newInstance int, by conformance.AuthorIdentity, membership *retireMembershipJSON, gateImpact *retireGateImpactJSON, retained, cascaded []string, cascadeMode string) {
	header := ui.NewHeader(s, "Retire (dry-run)").
		Add("Target", target).
		Add("Instance", fmt.Sprintf("%d -> %d", prevInstance, newInstance)).
		Add("By", by.Name)
	header.Pipeline("Retire").Render()
	fmt.Fprintf(s.W, "  Dry-run: no changes were written.\n")
	renderTicketMembership(s, membership, gateImpact)
	if len(retained) > 0 {
		fmt.Fprintf(s.W, "  Retained downstream: %s\n", strings.Join(retained, ", "))
	} else {
		fmt.Fprintf(s.W, "  Retained downstream: none\n")
	}
	if cascadeMode == "cmt" {
		if len(cascaded) > 0 {
			fmt.Fprintf(s.W, "  Cascade preview (cmt): %s\n", strings.Join(cascaded, ", "))
		} else {
			fmt.Fprintf(s.W, "  Cascade preview (cmt): none\n")
		}
	}
}

// renderTicketMembership renders the membership + gate-impact block
// shared by the ticket retire result and dry-run: the owning
// container, the work item, the container state, and the pending
// blockers (with the fail-closed note when the container is not
// completable).
func renderTicketMembership(s *ui.Style, membership *retireMembershipJSON, gateImpact *retireGateImpactJSON) {
	if membership == nil {
		return
	}
	container := membership.Container
	if container == "" {
		container = "unresolved"
	}
	workItem := membership.WorkItem
	if workItem == "" {
		workItem = "unresolved"
	}
	containerState := membership.ContainerState
	if containerState == "" {
		containerState = "unresolved"
	}
	fmt.Fprintf(s.W, "  Membership: container %s (%s), work item %s\n", container, containerState, workItem)
	if gateImpact == nil {
		return
	}
	if gateImpact.Completable {
		fmt.Fprintf(s.W, "  Gate impact: container completable (no pending work items)\n")
		return
	}
	fmt.Fprintf(s.W, "  Gate impact: still blocked by %s — retire does not unblock; unlink explicitly\n",
		strings.Join(gateImpact.AllDoneBlockedBy, ", "))
}

// baseRetireReport builds the deterministic machine report skeleton of
// one retire run (the caller sets Status/ObjectHash/DryRun).
func baseRetireReport(lineForm string, prevInstance, newInstance int, from, to, reason string, by conformance.AuthorIdentity, cascadeMode string) retireJSON {
	return retireJSON{
		Schema:         retireSchema,
		Target:         &retireTargetJSON{CanonicalForm: lineForm, PrevInstance: prevInstance, NewInstance: newInstance},
		ExistenceState: &retireExistenceJSON{From: from, To: to},
		Reason:         reason,
		By:             by.Name,
		ByKind:         by.Kind,
		Downstream:     &retireDownstreamJSON{Blockers: []string{}, Retained: []string{}},
		Cascade:        &retireCascadeJSON{Mode: cascadeMode, Cascaded: []string{}},
	}
}

// retireUsage renders a usage-class failure (exit 2): the error is a
// deterministic "eka: <error>" line on stderr; --json additionally
// gets the machine refusal document on stdout.
func retireUsage(cmd *cobra.Command, jsonOut bool, message string) error {
	if jsonOut {
		_ = emitJSON(cmd, retireJSON{Schema: retireSchema, Status: "refused", Reason: message})
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "eka: %s\n", message)
	return &exitError{code: exitUsage}
}

// retireRefused renders a deterministic refusal (exit 1): the
// single-line human refusal on stderr, and the machine refusal document
// on stdout with --json.
func retireRefused(cmd *cobra.Command, jsonOut bool, report *retireJSON, reason, hint string) error {
	if jsonOut {
		doc := retireJSON{Schema: retireSchema, Status: "refused", Reason: reason, Hint: hint}
		if report != nil {
			doc = *report
			doc.Status = "refused"
			doc.Reason = reason
			doc.Hint = hint
		}
		_ = emitJSON(cmd, doc)
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "eka: retire refused: %s; %s\n", reason, hint)
	return &exitError{code: exitFail}
}

// renderRetireResult renders one retire outcome deterministically: the
// header (target, instances, existence movement, authority) and the
// state line; the published path adds the new object hash.
func renderRetireResult(s *ui.Style, target string, prevInstance, newInstance int, from, to, status, hash string, retained, cascaded []string, by conformance.AuthorIdentity) {
	header := ui.NewHeader(s, "Retire").
		Add("Target", target).
		Add("Instance", fmt.Sprintf("%d -> %d", prevInstance, newInstance)).
		Add("Existence", from+" -> "+to).
		Add("By", by.Name)
	header.Pipeline("Retire").Render()
	switch status {
	case "retired":
		fmt.Fprintf(s.W, "  %s %s\n", ui.IconDone, s.Success(target+" retired ("+from+" -> "+to+")"))
	case "already-retired":
		fmt.Fprintf(s.W, "  %s %s\n", ui.IconDone, s.Success(target+" is already "+to+" — nothing written"))
	}
	if len(retained) > 0 {
		fmt.Fprintf(s.W, "  Retained downstream: %s\n", strings.Join(retained, ", "))
	}
	if len(cascaded) > 0 {
		fmt.Fprintf(s.W, "  Cascaded: %s\n", strings.Join(cascaded, ", "))
	}
	if status == "retired" {
		ui.NewSummary(s).
			Add("Object Hash", hash).
			Add("Next", "run 'eka sync push' to refresh the repository snapshot").
			Render()
	}
}

// renderRetireDryRun renders the read-only retire plan: the instance
// movement, the downstream sets and the cascade preview.
func renderRetireDryRun(s *ui.Style, target string, prevInstance, newInstance int, from, to string, retained, cascaded []string, cascadeMode string, by conformance.AuthorIdentity) {
	header := ui.NewHeader(s, "Retire (dry-run)").
		Add("Target", target).
		Add("Instance", fmt.Sprintf("%d -> %d", prevInstance, newInstance)).
		Add("Existence", from+" -> "+to).
		Add("By", by.Name)
	header.Pipeline("Retire").Render()
	fmt.Fprintf(s.W, "  Dry-run: no changes were written.\n")
	if len(retained) > 0 {
		fmt.Fprintf(s.W, "  Retained downstream: %s\n", strings.Join(retained, ", "))
	} else {
		fmt.Fprintf(s.W, "  Retained downstream: none\n")
	}
	if cascadeMode == "cmt" {
		if len(cascaded) > 0 {
			fmt.Fprintf(s.W, "  Cascade preview (cmt): %s\n", strings.Join(cascaded, ", "))
		} else {
			fmt.Fprintf(s.W, "  Cascade preview (cmt): none\n")
		}
	}
}
