package cmd

import (
	"fmt"
	"strconv"

	"github.com/maleolabs/eka-cli/cmd/ui"
	"github.com/maleolabs/eka-core/runtime"
	"github.com/spf13/cobra"
)

// newIntegrityCommand builds the `eka integrity` command tree: the
// integrity verification of the EKA workspace canonical store.
//
// Exit codes:
//
//	0  no integrity violations
//	1  integrity violations found
//	2  usage or internal error (workspace resolution, store failure)
func newIntegrityCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "integrity",
		Short: "Verify the EKA workspace integrity",
		Long: `Verify the integrity of the EKA workspace canonical store
(default ~/.eka, or $EKA_HOME).

Engineering Knowledge Objects are immutable and content-addressed:
every payload row is keyed by SHA-256(unit.json || content), written
once, and never updated. This command recomputes every content-derived
value and compares it with the stored state across four verification
levels:

  1. payload-hash:    recompute SHA-256(unit.json || content) per payload
  2. payload-decode:  strict-decode every unit.json (reject-by-default)
  3. reference-target: every reference's object_hash must exist
  4. reference-index: every reference's index columns must match its
                      payload (identity tuple, revision, dimension,
                      domain, phase, canonical form)

plus attachment digests and the repository registry. Unreferenced
payloads are the immutable history archive — they are counted, never
reported as violations.

The check detects manual database modification; it does not prevent
it (SQLite is a persistence layer, not a trust boundary). A tampered
payload row stays flagged permanently — there is no repair command in
v0.2. Remediation is manual: delete the tampered row (e.g. via
sqlite3), then re-pull from a clean snapshot or re-seed with
'eka sync pull --from-docs'; the reference then points at a verified
payload and the check passes again.

Exit codes:
  0  no violations
  1  violations found
  2  usage or internal error`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	cmd.AddCommand(newIntegrityCheckCommand())
	return cmd
}

// newIntegrityCheckCommand builds `eka integrity check`.
func newIntegrityCheckCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "check",
		Short: "Check the canonical store integrity",
		Long: `Check the integrity of the EKA workspace canonical store:
recompute every content-derived hash and strict-decode every payload,
verify every reference (target existence and index columns), every
attachment digest and the repository registry. All SQL is parameterized
and the scan is read-only.

Unreferenced payloads are retained history — they are reported as
"History payloads" and never count as violations.

Exit codes:
  0  no violations
  1  violations found
  2  usage or internal error`,
		Example: `  eka integrity check`,
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runIntegrityCheck(cmd)
		},
	}
	return cmd
}

// runIntegrityCheck runs the store scan and renders the deterministic
// report, mapping the outcome to the exit code contract: 0 when clean,
// 1 when violations exist, 2 on internal error (workspace resolution
// and store failures fall through to the default usage class).
func runIntegrityCheck(cmd *cobra.Command) error {
	r, err := runtime.Ensure()
	if err != nil {
		return err // Exit 2: workspace resolution.
	}
	defer r.Close()

	report, err := r.Integrity.Verify()
	if err != nil {
		return fmt.Errorf("integrity check failed: %w", err) // Exit 2.
	}
	sv, err := r.Integrity.SchemaVersion()
	if err != nil {
		return fmt.Errorf("integrity check failed: %w", err)
	}

	s := styleFor(cmd)
	ui.NewHeader(s, "Runtime").
		Add("Workspace", r.Path()).
		Add("Schema", "v"+strconv.Itoa(sv)).
		Render()

	ui.NewSummary(s).
		Add("Payloads checked", strconv.Itoa(report.PayloadsChecked)).
		Add("References checked", strconv.Itoa(report.RefsChecked)).
		Add("Attachments checked", strconv.Itoa(report.AttachmentsChecked)).
		Add("History payloads", strconv.Itoa(report.OrphanPayloads)).
		Add("Violations", strconv.Itoa(len(report.Violations))).
		Render()

	for _, v := range report.Violations {
		fmt.Fprintf(s.W, "  %s %s: %s — %s\n", ui.IconBullet, s.Accent(v.Kind), s.Info(v.Subject), v.Detail)
	}
	if len(report.Violations) > 0 {
		fmt.Fprintf(cmd.ErrOrStderr(), "eka: integrity check found %d violation(s)\n", len(report.Violations))
		return &exitError{code: exitFail}
	}
	return nil
}
