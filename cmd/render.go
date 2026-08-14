package cmd

import (
	"fmt"

	"github.com/maleolabs/eka-cli/cmd/ui"
	"github.com/maleolabs/eka-core/conformance"
	"github.com/spf13/cobra"
)

// flagVerbose is the presentation-only persistent --verbose flag added
// on the root command.
const flagVerbose = "verbose"

// styleFor builds the presentation Style for one command execution
// from the writer and the --verbose flag. All command renderers take
// the Style explicitly; there is no ambient state.
func styleFor(cmd *cobra.Command) *ui.Style {
	verbose, err := cmd.Flags().GetBool(flagVerbose)
	if err != nil {
		// The flag is always registered on the root persistent set;
		// failure is impossible in practice. Default to concise.
		verbose = false
	}
	return ui.NewStyle(cmd.OutOrStdout(), verbose)
}

// plural renders n with the correct noun form ("1 unit", "2 units").
func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// validationDetail renders the deterministic validation verdict with
// counts: "PASS (0 errors, 0 warnings)" / "FAIL (2 errors, 1 warning)".
func validationDetail(r *conformance.Report) string {
	if r.Pass() {
		return fmt.Sprintf("PASS (%d errors, %d warnings)", r.ErrorCount(), r.WarningCount())
	}
	return fmt.Sprintf("FAIL (%d errors, %d warnings)", r.ErrorCount(), r.WarningCount())
}

// renderFindings prints the validation findings under a heading. It is
// used when a command carries a failing validation report and the
// findings must stay visible (init's validation stage).
func renderFindings(s *ui.Style, r *conformance.Report) {
	fmt.Fprintln(s.W, s.Error("Validation findings:"))
	results := r.SortedResults()
	if len(results) == 0 {
		fmt.Fprintln(s.W, "  (no findings)")
		return
	}
	for _, res := range results {
		fmt.Fprintf(s.W, "  [%s] %s %s: %s\n", res.Severity, res.Rule, res.File, res.Message)
	}
}
