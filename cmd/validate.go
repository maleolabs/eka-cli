package cmd

import (
	"fmt"
	"strconv"

	"github.com/maleolabs/eka-cli/cmd/ui"
	"github.com/maleolabs/eka-core/conformance"
	"github.com/maleolabs/eka-core/runtime"
	"github.com/spf13/cobra"
)

// newValidateCommand builds the `eka validate` command: conformance
// validation of the repository rooted at an optional path (default: the
// current directory). All validation logic lives in the conformance
// package; this command only validates arguments, renders the report and
// maps the result to the exit code contract.
func newValidateCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "validate [path]",
		Short: "Validate an EKA repository against the conformance rules",
		Long: `Validate the EKA repository rooted at path against the conformance
rules.

With no path, the current directory is validated. Warnings never affect
the exit code; blocking violations exit 1. Usage or internal errors
(unknown flag, too many arguments, unreadable root) exit 2.`,
		Example: `  eka validate              validate the current directory
  eka validate docs          validate the repository rooted at docs`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := "."
			if len(args) == 1 {
				path = args[0]
			}
			// The Authoring API: validation is an authoring operation —
			// the conformance gate behind the Knowledge Compiler.
			report, err := runtime.Authoring.Validate(path)
			if err != nil {
				return fmt.Errorf("validate failed: %w", err)
			}
			s := styleFor(cmd)
			ui.NewHeader(s, "Repository").
				Add("Path", report.Root).
				Add("Knowledge", "EKA v"+standardVersion).
				Pipeline("Validate").
				Render()
			printReport(s, report)
			if !report.Pass() {
				return &exitError{code: exitFail}
			}
			return nil
		},
	}
}

// printReport renders the report in the deterministic non-TTY format
// (plain text plus UTF-8 icons, no ANSI escapes) and the colored TTY
// format (heading in accent, verdict colored):
//
//	Repository validation
//	Root: <root> — <n> authoring files, <n> artifacts, <n> errors, <n> warnings
//
//	Results (sorted by file, then rule):
//	  [ERROR] R6 docs/foo.md: message
//
//	Verdict: PASS
//	Summary:
//	└── Artifacts: 6
//	└── Errors: 0
//	└── Warnings: 0
//	└── Status: Repository conforms to EKA v1.0.0
//
// The scanned count includes .json authoring files, so the label is
// "authoring files" (was ".md files").
//
// When the repository has no docs/ knowledge tree the scan is skipped
// (ADR-018 Decision 2): the deterministic informational note prints
// right after the header line, the results list stays empty and the
// verdict is a clean PASS — the skip never affects the exit code:
//
//	Root: <root> — 0 authoring files, 0 artifacts, 0 errors, 0 warnings
//	  no docs/ knowledge tree — nothing to validate (docs-in-repo is legacy authoring — EKA v2 keeps knowledge in the workspace)
//
// The report IS the command output: nothing is dropped, the results
// list keeps its content and ordering contract.
func printReport(s *ui.Style, r *conformance.Report) {
	fmt.Fprintf(s.W, "%s\n", s.Accent("Repository validation"))
	fmt.Fprintf(s.W, "%s\n", s.Dim(fmt.Sprintf("Root: %s — %d authoring files, %d artifacts, %d errors, %d warnings",
		r.Root, r.FilesScanned, r.Artifacts, r.ErrorCount(), r.WarningCount())))
	if r.Skipped != "" {
		fmt.Fprintf(s.W, "  %s\n", s.Info(r.Skipped))
	}

	fmt.Fprintf(s.W, "\nResults (sorted by file, then rule):\n")
	results := r.SortedResults()
	if len(results) == 0 {
		fmt.Fprintf(s.W, "  (no violations found)\n")
	} else {
		for _, res := range results {
			fmt.Fprintf(s.W, "  [%s] %s %s: %s\n", res.Severity, res.Rule, res.File, res.Message)
		}
	}

	verdict := "PASS"
	status := "Repository conforms to EKA v" + standardVersion
	if !r.Pass() {
		verdict = "FAIL"
		status = "Repository does not conform to EKA v" + standardVersion
	}
	colored := s.Success(verdict)
	if !r.Pass() {
		colored = s.Error(verdict)
	}
	fmt.Fprintf(s.W, "\nVerdict: %s\n", colored)
	ui.NewSummary(s).
		Add("Artifacts", strconv.Itoa(r.Artifacts)).
		Add("Errors", strconv.Itoa(r.ErrorCount())).
		Add("Warnings", strconv.Itoa(r.WarningCount())).
		Add("Status", status).
		Render()
}
