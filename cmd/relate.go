package cmd

import (
	"errors"
	"fmt"

	"github.com/maleolabs/eka-cli/cmd/ui"
	"github.com/maleolabs/eka-core/conformance"
	"github.com/maleolabs/eka-core/runtime"
	"github.com/spf13/cobra"
)

// This file implements `eka relate <target>`: the relationship-only
// edge-add command of the Authoring API (runtime.Authoring.Relate). It
// adds edges to an EXISTING artifact without a full re-publish — the
// published path re-points the line's reference to a new immutable
// payload with the SAME instance version (no instance churn), the draft
// path mutates a pending draft in place. The command is a thin Cobra
// layer: target parsing, relationship-flag collection (the same
// StringSlice flags `eka new` uses), rendering and exit-code mapping —
// no draft storage, no store access.
//
// Exit codes:
//
//	relate 0 related (published re-point, draft mutated, or unchanged —
//	       every requested edge was already present); 1 refused (missing
//	       artifact, cross-namespace target, self-reference, unknown
//	       relationship type, malformed reference, unresolved namespace,
//	       Markdown draft, CKO-level validation findings on the published
//	       path); 2 usage/internal (invalid target, canonical published
//	       form, no relationship targets, workspace errors)
//
// The target is an artifact LINE: <ns>/<type>:<id> (qualified) or
// <type>:<id> (unqualified — the repository namespace applies, the same
// resolution `eka new` and `eka transition` use). Canonical published
// forms (<ns>/<type>:<id>:<v>) are refused: relate addresses the line,
// never a single immutable instance. Inside a repository context a
// qualified target must stay inside the repository's own namespace
// (cross-platform access is read-only).

// newRelateCommand builds `eka relate <target>`.
func newRelateCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "relate <target>",
		Short: "Add relationship edges to an existing artifact without re-publishing",
		Long: `Add relationship edges to an existing artifact WITHOUT a full
re-publish — no new instance version (no instance churn).

The target is an artifact line: <ns>/<type>:<id> (qualified) or
<type>:<id> (unqualified — resolved to the repository's namespace
inside a registered repository; outside one an unqualified target is
refused with a hint). A canonical published form (<ns>/<type>:<id>:<v>)
is refused: relate addresses the line. Inside a repository context a
qualified target must stay inside the repository's own namespace —
cross-platform access is read-only, the same ownership rule 'eka new',
'eka publish' and 'eka transition' enforce.

At least one relationship flag is required: a relate with no targets is
a usage error. Edges that are already present are skipped (idempotent;
a relate whose edges are all already present writes nothing). A
self-reference, an unknown relationship type and a malformed reference
are refused. On the published path, unresolved targets follow the Rule
5 draft tolerance exactly like publish: a warning while the artifact's
content-state is draft, an error otherwise (a pending-draft target is
therefore tolerated on a draft-state artifact).

What happens depends on the line's state:

  published  the line's current instance is re-pointed to a new
             immutable payload carrying the added edges. The instance
             version and revision stay UNCHANGED — the payload archive
             gains one row (immutability: history accumulates), but the
             artifact line does not advance. The old payload stays in
             the history (prev_hash lineage).
  draft      the line has no published instance yet, but a pending
             draft exists: the edge is added to the draft file in
             place, then the draft is re-validated at CKO level
             (findings are reported, never destructive — mirroring
             'eka edit'). Legacy Markdown drafts are refused.
  unchanged  every requested edge is already present: nothing is
             written (idempotent — edges are a set).

Edges that are already present are skipped (idempotent). A
self-reference, an unknown relationship type and a malformed reference
are refused. On the published path, unresolved targets follow the Rule
5 draft tolerance exactly like publish: a warning while the artifact's
content-state is draft, an error otherwise (a pending-draft target is
therefore tolerated on a draft-state artifact).

Flags:
  --depends-on <ref>[,<ref>...]   relationship targets (also
  --derives-from, --validates, --supersedes, --amends); comma-joined
                                 values and repeated flags accumulate —
                                 the same flags 'eka new' accepts

Exit codes:
  0  related (published, draft, or unchanged)
  1  refused (missing artifact, cross-namespace target, self-reference,
     unknown type, malformed reference, unresolved namespace, Markdown
     draft, validation findings on the published path)
  2  usage or internal error`,
		Example: `  eka relate feather/sto:my-item --depends-on feather/ctr:wave-7
  eka relate sto:my-item --depends-on ctr:wave-7 --derives-from plan:roadmap-v1
  eka relate tkt:wave-7-item-1 --derives-from ctr:wave-7,sto:my-item`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			target := args[0]
			// The line-form gate (exit 2, usage): a canonical published
			// form addresses an immutable instance, and relate addresses
			// the line (the core re-checks; the CLI refuses early like
			// every authoring command's target parsing).
			ref, err := conformance.ParseReference(target, "", "")
			if err != nil {
				return newUsage(cmd, fmt.Sprintf("relate: invalid target %q: %v", target, err))
			}
			if ref.HasVersion {
				return newUsage(cmd, fmt.Sprintf("relate: %s is a canonical published form; relate addresses the artifact line", target))
			}
			// No relationship flags is a usage error, never a silent
			// "unchanged" (the idempotent-duplicate case has a distinct
			// message): the core refuses the same way, this check keeps
			// the refusal in the usage class with a clean message.
			rels := collectRelationships(cmd)
			if len(rels) == 0 {
				return newUsage(cmd, "relate: no relationship targets; pass --depends-on/--derives-from/--validates/--supersedes/--amends")
			}
			r, err := openAuthoringRuntime(cmd)
			if err != nil {
				return err // Exit 2: workspace resolution.
			}
			defer r.Close()

			res, err := runtime.Authoring.Relate(r, runtime.RelateRequest{
				RepoPath:      ".",
				Target:        target,
				Relationships: rels,
			})
			if err != nil {
				var refusal *runtime.RelateRefusal
				if errors.As(err, &refusal) {
					fmt.Fprintf(cmd.ErrOrStderr(), "eka: %s\n", err)
					return &exitError{code: exitFail}
				}
				var ve *runtime.RelateValidationError
				if errors.As(err, &ve) {
					printCKOReport(styleFor(cmd), target, ve.Report)
					fmt.Fprintf(cmd.ErrOrStderr(), "eka: %s\n", err)
					return &exitError{code: exitFail}
				}
				return err // Exit 2: internal.
			}

			s := styleFor(cmd)
			renderRelateResult(s, target, res)
			return nil
		},
	}
	// Relationship targets: StringSlice — repeated occurrences and
	// comma-joined values accumulate (never silently override), the same
	// flag contract as `eka new` (collectRelationships reads these
	// names).
	cmd.Flags().StringSlice(flagNewDependsOn, nil, "depends-on relationship targets; comma-separated values and repeated flags accumulate")
	cmd.Flags().StringSlice(flagNewDerivesFrom, nil, "derives-from relationship targets; comma-separated values and repeated flags accumulate")
	cmd.Flags().StringSlice(flagNewValidates, nil, "validates relationship targets; comma-separated values and repeated flags accumulate")
	cmd.Flags().StringSlice(flagNewSupersedes, nil, "supersedes relationship targets; comma-separated values and repeated flags accumulate")
	cmd.Flags().StringSlice(flagNewAmends, nil, "amends relationship targets; comma-separated values and repeated flags accumulate")
	return cmd
}

// renderRelateResult renders one relate outcome deterministically: the
// header (target + state), the added edges in canonical order, the
// no-churn proof (instance version + object hash on the published
// path), and the draft-path re-validation verdict.
func renderRelateResult(s *ui.Style, target string, res *runtime.RelateResult) {
	ui.NewHeader(s, "Relate").
		Add("Target", res.Target).
		Add("State", res.State).
		Pipeline("Relate").
		Render()
	switch res.State {
	case "unchanged":
		fmt.Fprintf(s.W, "  %s %s\n", ui.IconDone, s.Success("every requested edge is already present — nothing written"))
	default:
		if len(res.Added) == 0 {
			fmt.Fprintf(s.W, "  %s %s\n", ui.IconBullet, "no edges added")
		} else {
			for _, rel := range res.Added {
				fmt.Fprintf(s.W, "  %s %s %s\n", ui.IconBullet, rel.Type, rel.Target)
			}
		}
	}
	if res.State == "published" {
		ui.NewSummary(s).
			Add("Instance Version", fmt.Sprint(res.InstanceVersion)).
			Add("Object Hash", res.ObjectHash).
			Add("Next", "eka get "+res.Target).
			Render()
	}
	if res.DraftValidation != nil {
		report := res.DraftValidation.Report
		if report != nil && report.Pass() {
			fmt.Fprintf(s.W, "  %s %s\n", ui.IconDone, s.Success("draft re-validated clean"))
		} else if report != nil {
			printCKOReport(s, target, report)
		}
	}
}
