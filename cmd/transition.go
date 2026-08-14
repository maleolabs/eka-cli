package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/maleolabs/eka-cli/cmd/ui"
	"github.com/maleolabs/eka-core/runtime"
	"github.com/spf13/cobra"
)

// This file implements `eka transition` (ADR-019 D2, revised): the
// structured way to move a work item along the D1 transition table. The
// command is a thin Cobra layer over the Authoring API
// (runtime.Authoring.Transition): it resolves the change-log authority
// (--by flag -> git config user.name), derives the destination
// (explicit <to>, --forward or --backward), renders the deterministic
// warning banner + interactive confirmation when the work item is not
// registered in the current active container, and maps the result to
// the exit code contract. The transition is published to the workspace
// store — `eka sync push` refreshes the repository snapshot.
//
// Exit codes (spec §6.1):
//
//	0  transition published (or cancelled by the user at the prompt)
//	1  refusal (illegal transition, gate unmet, repository/workspace
//	   state, unregistered work item without confirmation)
//	2  usage or internal error (unknown flag, malformed target,
//	   missing --by source, conflicting destination flags)
//
// --json emits the deterministic machine report on stdout (one line,
// one trailing newline); the default output is the human report in the
// CLI house style.

// transitionJSON is the deterministic machine report of one transition
// run (schema "eka-transition-v1"; pinned field order).
type transitionJSON struct {
	Schema string `json:"schema"`
	OK     bool   `json:"ok"`
	Target string `json:"target,omitempty"`
	From   string `json:"from,omitempty"`
	To     string `json:"to,omitempty"`
	By     string `json:"by,omitempty"`
	Hash   string `json:"objectHash,omitempty"`
	// LockedPlan and LockedPlanHash are set when the transition was a
	// container ACTIVATION that locked its depends-on plan
	// (planning-state -> immutable); omitted otherwise (additive,
	// schema-stable).
	LockedPlan     string `json:"lockedPlan,omitempty"`
	LockedPlanHash string `json:"lockedPlanHash,omitempty"`
	// Warning is set when the work item was not registered in the
	// current active container (the machine mirror of the banner).
	Warning string `json:"warning,omitempty"`
	Reason  string `json:"reason,omitempty"`
	Hint    string `json:"hint,omitempty"`
}

// transitionSchema is the schema id of the transition machine report.
const transitionSchema = "eka-transition-v1"

// newTransitionCommand builds `eka transition <target> [<to>|--forward|
// --backward] [--by <name>] [--json] [--force]`.
func newTransitionCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "transition <target> [<to>]",
		Short: "Transition a work item, plan, container or artifact state",
		Long: `Transition a work item along the execution-state table (ADR-019
D1), a plan along the planning-state table, a container along the
container-state table, or a knowledge artifact along the content-state
lifecycle, and publish the result to the workspace store.

The destination is the explicit <to> value, or the derived step of
--forward / --backward. For work items: --forward is the next
sequential state (planned -> todo -> in-progress -> in-review -> done;
canceled -> todo), --backward the one-step pull-back (in-review ->
in-progress, in-progress -> todo). For plans: --forward approves a
draft plan (draft -> approved); planning-state immutable is the
container lock — it happens atomically with the container activation
(eka transition ctr:<id> active) and cannot be requested directly. For
containers (three states, planned -> active -> completed): --forward
activates a planned container (planned -> active) or completes an
active one (active -> completed, gated on every work item the
container registers being done or canceled). Activation is gated on
the exactly-one-active rule (no other container may be active — a
planned container activates only after the active one completes) and
on the depends-on plan being approved; the activation LOCKS the plan
(planning-state -> immutable) atomically with the activation. The
three destination flags are mutually exclusive.

For knowledge artifacts (vis/str/req/scp/epc/adr/dec/arc/spec/std/run/
rel/gls/trc/fnd, plus rvw- and cmt-) the content-state lifecycle of
the type's variant applies, forward-only, one step at a time: the
standard variant draft -> review -> approved -> amended, the ADR
variant proposed -> accepted -> superseded, the decision variant
draft -> accepted -> superseded (amended / superseded are terminal).
A skip (e.g. draft -> approved), a revert or a no-op is refused; a
superseded ADR must be referenced by a replacement via ` + "`supersedes`" + `.
plan- keeps planning-state as its transition domain (its content-state
is out of scope for the transition API).

The target is the line in the workspace store: <type>:<id>
(unqualified — the repository namespace applies) or <ns>/<type>:<id>
(qualified — the namespace must equal the repository's). The target
type selects the transition domain, so a destination keyword never
clashes across domains: work items (sto/ts/bug/td/ch/spk) transition
along execution-state, plans (plan-) along planning-state, containers
(ctr-) along container-state, knowledge artifacts along content-state
(eka transition adr:001 accepted); run 'eka sync' first so the line
exists in the workspace. ` + "`done`" + ` is terminal for work items
(canceled is its only exit) and ` + "`canceled`" + ` re-activates to
` + "`todo`" + `; ` + "`completed`" + ` is terminal for containers.

The work-item gates (R13) are checked early: in-review requires a
resolved implementation note, done requires every note resolved —
notes are the published cmt- units and the EKA_HOME/drafts cmt-
drafts discussing the work item. Plan and container transitions carry
no note gates.

A work item NOT registered in the current active container (no
ticket deriving from an active ctr- references it) shows a warning
banner and — on a terminal — an interactive confirmation; outside a
terminal --force confirms (the work-item confirmation only: --force
never bypasses a transition gate, e.g. the container all-done gate).

The change-log authority (by) comes from --by, or defaults to
` + "`git config user.name`" + ` — the engine never falls back to a
default authority.

Exit codes:
  0  transition published (or cancelled at the confirmation prompt)
  1  refused (illegal transition, gate unmet, unregistered work item
     without --force, not an EKA repository)
  2  usage or internal error (malformed target, conflicting
     destination flags, missing --by source)`,
		Example: `  eka transition sto:12 in-review
  eka transition plan:roadmap-v1 approved
  eka transition ctr:wave-7 completed
  eka transition adr:001 accepted
  eka transition spec:api review
  eka transition sto:12 --forward
  eka transition sto:12 --backward
  eka transition atrium-api/sto:12 done --by agent-x
  eka transition sto:12 canceled --force --json`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			byFlag, _ := cmd.Flags().GetString("by")
			jsonOut, _ := cmd.Flags().GetBool("json")
			forward, _ := cmd.Flags().GetBool("forward")
			backward, _ := cmd.Flags().GetBool("backward")
			force, _ := cmd.Flags().GetBool("force")

			to := ""
			if len(args) == 2 {
				to = args[1]
			}
			switch {
			case to != "" && (forward || backward):
				return transitionUsage(cmd, jsonOut, "pass either an explicit <to> or --forward/--backward, not both")
			case forward && backward:
				return transitionUsage(cmd, jsonOut, "--forward and --backward are mutually exclusive")
			case to == "" && !forward && !backward:
				return transitionUsage(cmd, jsonOut, "a destination is required: <to>, --forward, or --backward")
			}

			byKindFlag, _ := cmd.Flags().GetString("by-kind")
			by, err := runtime.BySource(byFlag, byKindFlag, ".")
			if err != nil {
				return transitionUsage(cmd, jsonOut, err.Error())
			}
			r, err := openAuthoringRuntime(cmd)
			if err != nil {
				return err // Exit 2: workspace resolution.
			}
			defer r.Close()

			// Issue-number target (RFC): "#<n>" resolves in the
			// work-item group (the transition operates on work items).
			target := args[0]
			if strings.HasPrefix(target, "#") {
				resolved, rerr := resolveNumberTargetInRepo(r, ".", target, "work-item")
				if rerr != nil {
					return transitionUsage(cmd, jsonOut, rerr.Error())
				}
				target = resolved
			}
			req := runtime.TransitionRequest{
				RepoPath:  ".",
				Target:    target,
				To:        to,
				Forward:   forward,
				Backward:  backward,
				By:        by.Name,
				ByKind:    by.Kind,
				Confirmed: force, // --force pre-authorizes the active-container confirmation.
			}
			res, err := runtime.Authoring.Transition(r, req)
			if err != nil {
				var refusal *runtime.TransitionRefusal
				if errors.As(err, &refusal) {
					// The active-container confirmation gate: the
					// transition was NOT published (pre-flight). On a
					// terminal the user confirms with the arrow-key
					// selector; outside one --force (agents) or a
					// refusal.
					if refusal.Confirmation && !force {
						if refusal.Warning != "" {
							fmt.Fprintf(cmd.ErrOrStderr(), "%seka: warning: %s\n", ui.Margin, refusal.Warning)
						}
						// The interactive confirmation requires BOTH stdin
						// and stdout to be terminals (a captured-output run
						// must never block on a prompt the user cannot
						// see — it refuses deterministically instead;
						// agents use --force).
						s := styleFor(cmd)
						if !isTTYReader(cmd.InOrStdin()) || !s.TTY {
							return transitionRefused(cmd, jsonOut, refusal.Reason, refusal.Hint)
						}
						// The reusable arrow-selected menu (the m2apps
						// bubbletea pattern — the same primitive the
						// namespace-alignment confirmations use).
						value, err := ui.Select(s, cmd.InOrStdin(), cmd.OutOrStdout(),
							"The work item is outside the current execution scope.",
							[]ui.MenuItem{
								{Title: "Continue transition", Value: "continue"},
								{Title: "Cancel", Value: "cancel"},
							}, 0)
						if err != nil {
							if errors.Is(err, ui.ErrCancelled) {
								value = "cancel" // Esc/q/Ctrl-C: deterministic abort
							} else {
								return fmt.Errorf("transition: %w", err)
							}
						}
						if value != "continue" {
							if jsonOut {
								_ = emitJSON(cmd, transitionJSON{Schema: transitionSchema, OK: false, Reason: "cancelled by the user"})
							}
							// The human cancellation line must never
							// pollute the machine document: with --json,
							// stdout carries ONLY the JSON (the emitJSON
							// contract), so the notice goes to stderr
							// like every other human line of this
							// command.
							fmt.Fprintf(cmd.ErrOrStderr(), "%seka: transition cancelled; no changes made\n", ui.Margin)
							return nil
						}
						req.Confirmed = true
						res, err = runtime.Authoring.Transition(r, req)
						if err != nil {
							var again *runtime.TransitionRefusal
							if errors.As(err, &again) {
								return transitionRefused(cmd, jsonOut, again.Reason, again.Hint)
							}
							return fmt.Errorf("transition: %w", err)
						}
					} else {
						if refusal.Warning != "" {
							fmt.Fprintf(cmd.ErrOrStderr(), "%seka: warning: %s\n", ui.Margin, refusal.Warning)
						}
						return transitionRefused(cmd, jsonOut, refusal.Reason, refusal.Hint)
					}
				} else {
					return fmt.Errorf("transition: %w", err) // Exit 2: usage/internal.
				}
			}

			if jsonOut {
				return emitJSON(cmd, transitionJSON{
					Schema:         transitionSchema,
					OK:             true,
					Target:         res.Target,
					From:           res.From,
					To:             res.To,
					By:             res.By.Name,
					Hash:           res.ObjectHash,
					LockedPlan:     res.LockedPlan,
					LockedPlanHash: res.LockedPlanHash,
					Warning:        res.Warning,
				})
			}
			s := styleFor(cmd)
			ui.NewHeader(s, "Transition").
				Add("Target", res.Target).
				Add("From", res.From).
				Add("To", res.To).
				Add("By", res.By.Name).
				Pipeline("Transition").
				Render()
			ui.NewSummary(s).
				Add("Object", res.Target).
				Add("Object Hash", res.ObjectHash).
				Add("Next", "run 'eka sync push' to refresh the repository snapshot").
				Render()
			// A container activation locked its depends-on plan
			// atomically with the activation (protocol §4).
			if res.LockedPlan != "" {
				ui.NewSummary(s).
					Add("Plan Locked", res.LockedPlan+" → immutable").
					Render()
			}
			return nil
		},
	}
	cmd.Flags().String("by", "", "change-log authority name (default: `git config user.name`)")
	cmd.Flags().String("by-kind", "", "author identity kind: user, agent, or worker (default: user)")
	cmd.Flags().Bool("json", false, "emit the deterministic machine report (schema eka-transition-v1)")
	cmd.Flags().Bool("forward", false, "transition to the next sequential state of the D1 table")
	cmd.Flags().Bool("backward", false, "transition back one step of the D1 table (pull-back)")
	cmd.Flags().Bool("force", false, "pre-authorize the work-item active-container confirmation; it never bypasses a transition gate (e.g. the container all-done gate)")
	return cmd
}

// transitionUsage renders a usage-class failure (exit 2): the error is
// a deterministic "eka: <error>" line on stderr; --json additionally
// gets the machine refusal document on stdout (the JSON contract is
// identical for both failure classes).
func transitionUsage(cmd *cobra.Command, jsonOut bool, message string) error {
	if jsonOut {
		_ = emitJSON(cmd, transitionJSON{Schema: transitionSchema, OK: false, Reason: message})
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "eka: %s\n", message)
	return &exitError{code: exitUsage}
}

// transitionRefused renders a deterministic refusal (exit 1): the
// single-line human refusal on stderr, and the machine refusal document
// on stdout with --json.
func transitionRefused(cmd *cobra.Command, jsonOut bool, reason, hint string) error {
	if jsonOut {
		_ = emitJSON(cmd, transitionJSON{Schema: transitionSchema, OK: false, Reason: reason, Hint: hint})
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "eka: transition refused: %s; %s\n", reason, hint)
	return &exitError{code: exitFail}
}

// emitJSON writes the deterministic machine document to stdout as one
// compact JSON line plus a single trailing newline (the machine
// contract: stdout carries ONLY the document).
func emitJSON(cmd *cobra.Command, doc any) error {
	data, err := json.Marshal(doc)
	if err != nil {
		return fmt.Errorf("cannot serialize the machine report: %w", err)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "%s\n", data)
	return nil
}
