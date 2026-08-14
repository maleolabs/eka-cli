package cmd

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"

	"github.com/maleolabs/eka-cli/cmd/ui"
	"github.com/maleolabs/eka-core/exchange"
	"github.com/maleolabs/eka-core/metadata"
	"github.com/spf13/cobra"
)

// newExportCommand builds the `eka export` command: export an EKA
// repository (rooted at the current directory) to the Reference
// Serialization Format (RSF) v1.0 package. All export logic lives in the
// exchange engine; this command only validates flags/arguments, renders
// the outcome and maps the result to the exit code contract.
//
// Exit codes:
//
//	0  export produced a package (validation gate passed, warnings allowed)
//	1  repository validation failed: the export is refused and no package
//	   is produced (the full report is printed)
//	2  usage or internal error (malformed/unknown export target, flag
//	   error, serialization or filesystem failure, or the current
//	   directory is not an EKA repository — no eka.yaml; run 'eka init'
//	   first)
func newExportCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "export [target ...]",
		Short: "Export an EKA repository to an RSF package",
		Long: `Export the EKA repository rooted at the current directory to a
package in the EKA Reference Serialization Format (RSF) v1.0.

The repository is validated against the conformance rules first
(R0-R12); a repository with blocking violations is refused and no
package is produced. Warnings never block an export.

Export targets are canonical reference forms:

  <type>:<id>[:<instance-version>]        same namespace
  <namespace>/<type>:<id>[:<version>]     cross namespace

With no target the whole repository is exported (Repository scope).
One target without an instance-version exports its Artifact Line
(all instances); one versioned target exports exactly that instance;
several targets export their union (Collection scope).

The package is written as a single .ekapkg ZIP container named
rsf-<scope>-<namespace>-1.ekapkg, or as a directory layout when
--output names an existing directory or ends with a path separator.
Exports are deterministic: identical repository state yields
byte-identical packages.

Exit codes:
  0  export succeeded
  1  repository validation failed (no package produced)
  2  usage or internal error (bad target, unwritable output, or the
     current directory is not an EKA repository — no eka.yaml; run
     'eka init' first)`,
		Example: `  eka export
  eka export sto:login-email
  eka export adr:001-exchange:1
  eka export plan:rilis-1 sto:login-email
  eka export -o /tmp/package
  eka export -o my-package.ekapkg`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			output, err := cmd.Flags().GetString("output")
			if err != nil {
				return fmt.Errorf("export failed: %w", err)
			}
			// The repository context gate (ADR-018): export roots at
			// the current directory, so the export root is the working
			// directory — without eka.yaml the tree is not an EKA
			// repository and the export is refused deterministically
			// (exit 2, the usage/internal class of export failures).
			abs, err := filepath.Abs(".")
			if err != nil {
				return fmt.Errorf("export failed: %w", err)
			}
			abs = filepath.Clean(abs)
			// The walk-up directory is the export root: exporting
			// from a subdirectory of a repository exports the
			// repository root (the tree carrying eka.yaml), never
			// the working directory.
			_, mdir, hasMeta, err := metadata.Find(abs)
			if err != nil {
				return fmt.Errorf("export failed: %w", err)
			}
			if !hasMeta {
				return fmt.Errorf("export refused: %s is not an EKA repository (no eka.yaml); run 'eka init' first", abs)
			}
			s := styleFor(cmd)
			// A contextual loading state while the engine runs (TTY:
			// animated spinner; non-TTY: one deterministic line). The
			// context header follows on success — it carries the package
			// identity, which only exists after the engine ran.
			spinner := ui.NewSpinner(s, "Loading Engineering Knowledge...")
			res, err := exchange.Export(mdir, exchange.Options{Targets: args, Output: output})
			spinner.Stop()
			if err != nil {
				var ve *exchange.ValidationError
				if errors.As(err, &ve) {
					// Blocking violations: print the full report and exit 1.
					printReport(s, ve.Report)
					fmt.Fprintf(cmd.ErrOrStderr(), "eka: %s\n", ve.Error())
					return &exitError{code: exitFail}
				}
				return err // Mapped to exit 2 by Execute.
			}
			renderExport(s, res)
			return nil
		},
	}
	cmd.Flags().StringP("output", "o", "",
		"output path: a .ekapkg file, an existing directory, or a path ending in / (default: <label>.ekapkg in the current directory)")
	return cmd
}

// exportStageNames are the six export stages. The tree labels carry the
// deterministic "[i/6] " prefix.
const (
	exportStageDiscover  = "Discover repository"
	exportStageLoad      = "Load Engineering Knowledge"
	exportStageBuild     = "Build Exchange Model"
	exportStageSerialize = "Serialize Knowledge Package"
	exportStageWrite     = "Write package"
	exportStageValidate  = "Validate package"
)

// renderExport renders the export outcome: the context header
// identifying the knowledge package (canonical identity: the package
// label), the six-stage tree and the closing summary.
func renderExport(s *ui.Style, r *exchange.Result) {
	ui.NewHeader(s, "Knowledge Package").
		Add("Package", r.Label).
		Add("Scope", string(r.Package.Header.ExportScope)).
		Add("Output", r.Output).
		Add("Format", "RSF v1").
		Add("Knowledge", "EKA v"+standardVersion).
		Pipeline("Export").
		Render()

	tree := ui.NewTree(s, "Knowledge Package")
	tree.Add(ui.Step(1, 6) + exportStageDiscover).Done(fmt.Sprintf("scanned %s, %s",
		plural(r.Validation.FilesScanned, "authoring file", "authoring files"),
		plural(r.Validation.Artifacts, "artifact", "artifacts")))
	tree.Add(ui.Step(2, 6) + exportStageLoad).Done(fmt.Sprintf("loaded %s, %s, %s",
		plural(r.Units, "unit", "units"),
		plural(r.Attachments, "attachment", "attachments"),
		plural(r.ExternalReferences, "external reference", "external references")))
	tree.Add(ui.Step(3, 6) + exportStageBuild).Done(fmt.Sprintf("scope %s, namespace %s",
		string(r.Package.Header.ExportScope), r.Package.Header.Namespace))
	tree.Add(ui.Step(4, 6) + exportStageSerialize).Done(fmt.Sprintf("RSF v1.0 (serialization version %s)",
		exchange.SerializationVersion))
	tree.Add(ui.Step(5, 6) + exportStageWrite).Done(fmt.Sprintf("wrote %s (%s)", r.Output, outputKind(r)))
	tree.Add(ui.Step(6, 6) + exportStageValidate).Done(validationDetail(r.Validation))
	tree.Finish()

	renderExportSummary(s, r)
}

// renderExportSummary renders the optional verbose detail sections
// (per-unit lists) and the closing summary block.
func renderExportSummary(s *ui.Style, r *exchange.Result) {
	if s.Verbose {
		units := make([]string, 0, len(r.Package.Units))
		for _, u := range r.Package.Units {
			units = append(units, u.CanonicalIdentityForm)
		}
		sort.Strings(units)
		s.Bullets("Units:", units)
		attachments := make([]string, 0, len(r.Package.Attachments))
		for _, a := range r.Package.Attachments {
			attachments = append(attachments, a.ID)
		}
		sort.Strings(attachments)
		s.Bullets("Attachments:", attachments)
		externals := make([]string, 0, len(r.Package.Declarations.ExternalReferences))
		for _, ext := range r.Package.Declarations.ExternalReferences {
			externals = append(externals, fmt.Sprintf("%s %s %s", ext.Source, ui.IconArrow, ext.Target))
		}
		s.Bullets("External references:", externals)
	}

	ui.NewSummary(s).
		Add("Package Label", r.Label).
		Add("Output", r.Output+" ("+outputKind(r)+")").
		Add("Scope", string(r.Package.Header.ExportScope)).
		Add("Artifacts", strconv.Itoa(r.Units)).
		Add("Attachments", strconv.Itoa(r.Attachments)).
		Add("External References", strconv.Itoa(r.ExternalReferences)).
		Add("Integrity (SHA-256)", r.Package.Integrity.PackageDigest).
		Add("Validation", validationDetail(r.Validation)).
		Render()
}

// outputKind classifies the written package form.
func outputKind(r *exchange.Result) string {
	if r.Directory {
		return "directory"
	}
	return "ZIP container"
}
