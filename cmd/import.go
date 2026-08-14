package cmd

import (
	"errors"
	"fmt"
	"path/filepath"
	"strconv"

	"github.com/maleolabs/eka-cli/cmd/ui"
	"github.com/maleolabs/eka-core/exchange"
	"github.com/maleolabs/eka-core/metadata"
	"github.com/spf13/cobra"
)

// newImportCommand builds the `eka import` command: import an RSF package
// (.ekapkg ZIP container or directory layout) into the EKA repository
// rooted at the current directory. All import logic lives in the exchange
// engine; this command validates arguments, renders the outcome and maps
// the result to the exit code contract.
//
// Exit codes:
//
//	0  import succeeded (artifacts imported, or every artifact was a no-op
//	   duplicate)
//	1  repository-side failure: target has no docs/ knowledge tree,
//	   repository failed validation (pre-import gate or post-commit
//	   revalidation with rollback), conflicts, unresolved non-draft
//	   relationships
//	2  usage or internal error (missing/extra arguments, unreadable or
//	   malformed package, integrity failure, unsupported versions,
//	   filesystem failure, or the current directory is not an EKA
//	   repository — no eka.yaml; run 'eka init' first)
//
// Output contract: nothing is written to stdout until the import
// succeeds; failure diagnostics go to stderr only. This keeps the
// stdout-empty-on-error property (asserted by the CLI tests) intact.
func newImportCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "import <package-path>",
		Short: "Import an RSF package into the current repository",
		Long: `Import an EKA package in the Reference Serialization Format (RSF)
v1.0 into the EKA repository rooted at the current directory.

The package (a single-file .ekapkg ZIP container or the equivalent
directory layout) is verified first: entry structure, strict JSON
decoding (unknown fields are rejected), SHA-256 integrity (package,
per-unit, per-attachment digests) and manifest self-consistency.
The import then runs the Exchange Contract phases: contract
validation, identity, state, structure, referential resolution
(local -> global -> declared external, with draft tolerance),
conflict detection (reject-by-default) and duplicate detection
(identical artifacts are skipped as no-ops).

The integration strategy is a conservative merge: only NEW artifacts
are written; identical duplicates are skipped; any difference on an
existing identity is a conflict and aborts the import with a summary
before anything is written. Nothing is ever overwritten or deleted.
After the commit the repository is revalidated; on any failure every
written file is rolled back.

Exit codes:
  0  import succeeded (or all no-op)
  1  repository invalid, conflicts, unresolved relationships, or
     post-commit revalidation failure (rolled back)
  2  package invalid/malformed/unsupported, usage, fs failure, or the
     current directory is not an EKA repository (no eka.yaml; run
     'eka init' first)`,
		Example: `  eka import rsf-repo-acme-1.ekapkg
  eka import /tmp/package-layout/`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// The repository context gate (ADR-018): import writes
			// authoring files into the docs/ tree of the repository
			// rooted at the current directory — without eka.yaml the
			// tree is not an EKA repository and the import is refused
			// deterministically (exit 2, the usage/internal class).
			abs, err := filepath.Abs(".")
			if err != nil {
				return fmt.Errorf("import failed: %w", err)
			}
			abs = filepath.Clean(abs)
			_, _, hasMeta, err := metadata.Find(abs)
			if err != nil {
				return fmt.Errorf("import failed: %w", err)
			}
			if !hasMeta {
				return fmt.Errorf("import failed: %s is not an EKA repository (no eka.yaml); run 'eka init' first", abs)
			}
			res, err := exchange.Import(args[0], exchange.ImportOptions{Root: "."})
			if err != nil {
				s := styleFor(cmd)
				var ive *exchange.ImportValidationError
				if errors.As(err, &ive) {
					if ive.Report != nil {
						printReport(s, ive.Report)
					}
					fmt.Fprintf(cmd.ErrOrStderr(), "eka: %s\n", ive.Error())
					return &exitError{code: exitFail}
				}
				var ce *exchange.ConflictError
				if errors.As(err, &ce) {
					fmt.Fprintf(cmd.ErrOrStderr(), "eka: %s\n", ce.Error())
					for _, c := range ce.Conflicts {
						fmt.Fprintf(cmd.ErrOrStderr(), "  - %s\n", c.String())
					}
					return &exitError{code: exitFail}
				}
				var re *exchange.RelationshipError
				if errors.As(err, &re) {
					fmt.Fprintf(cmd.ErrOrStderr(), "eka: %s\n", re.Error())
					for _, d := range re.Details {
						fmt.Fprintf(cmd.ErrOrStderr(), "  - %s\n", d)
					}
					return &exitError{code: exitFail}
				}
				return err // Mapped to exit 2 by Execute.
			}
			renderImport(styleFor(cmd), res, args[0])
			return nil
		},
	}
	return cmd
}

// importStageNames are the seven import stages (Exchange Contract
// phases). The tree labels carry the deterministic "[i/7] " prefix.
const (
	importStageVerify       = "Verify package"
	importStageValidateRepo = "Validate repository"
	importStageIdentities   = "Resolve identities"
	importStageRelations    = "Resolve relationships"
	importStageConflicts    = "Detect conflicts"
	importStageIntegrate    = "Integrate"
	importStageRevalidate   = "Revalidate"
)

// renderImport renders the import outcome: the context header
// identifying the knowledge package (identity: the package path), the
// seven Exchange Contract phase tree and the closing summary. It runs
// only after a successful import, so stdout stays empty on every error
// path.
func renderImport(s *ui.Style, r *exchange.ImportResult, pkgPath string) {
	ui.NewHeader(s, "Knowledge Package").
		Add("Package", pkgPath).
		Add("Format", "RSF v1").
		Add("Knowledge", "EKA v"+standardVersion).
		Pipeline("Import").
		Render()

	tree := ui.NewTree(s, "Knowledge Package")
	tree.Add(ui.Step(1, 7) + importStageVerify).Done(fmt.Sprintf("%s (RSF v1.0, serialization %s, exchange format %s, specification %s)",
		r.PackageLabel, exchange.SerializationVersion, exchange.ExchangeFormatVersion, exchange.SpecificationVersion))
	tree.Add(ui.Step(2, 7) + importStageValidateRepo).Done(validationDetail(r.PreValidation))
	tree.Add(ui.Step(3, 7) + importStageIdentities).Done(fmt.Sprintf("resolved %s",
		plural(len(r.ImportedArtifacts)+len(r.SkippedArtifacts), "identity", "identities")))

	relations := "all relationships resolved"
	if len(r.Warnings) > 0 {
		relations = plural(len(r.Warnings), "draft tolerance warning", "draft tolerance warnings")
	}
	tree.Add(ui.Step(4, 7) + importStageRelations).Done(relations)
	tree.Add(ui.Step(5, 7) + importStageConflicts).Done("no conflicts")
	tree.Add(ui.Step(6, 7) + importStageIntegrate).Done(fmt.Sprintf("wrote %s, skipped %s, %s",
		plural(len(r.ImportedArtifacts), "artifact", "artifacts"),
		plural(len(r.SkippedArtifacts), "no-op duplicate", "no-op duplicates"),
		plural(len(r.AttachmentsImported), "attachment", "attachments")))
	tree.Add(ui.Step(7, 7) + importStageRevalidate).Done(validationDetail(r.Validation))
	tree.Finish()

	if s.Verbose {
		s.Bullets("Imported:", r.ImportedArtifacts)
		s.Bullets("Skipped (no-op):", r.SkippedArtifacts)
		s.Bullets("Warnings:", r.Warnings)
	}

	attachments := strconv.Itoa(len(r.AttachmentsImported))
	if n := len(r.AttachmentsSkipped); n > 0 {
		attachments += fmt.Sprintf(" (+%d skipped)", n)
	}
	ui.NewSummary(s).
		Add("Imported", strconv.Itoa(len(r.ImportedArtifacts))).
		Add("Skipped (no-op)", strconv.Itoa(len(r.SkippedArtifacts))).
		Add("Conflicts", strconv.Itoa(len(r.Conflicts))).
		Add("Attachments", attachments).
		Add("Warnings", strconv.Itoa(len(r.Warnings))).
		Add("Validation", validationDetail(r.Validation)).
		Render()
}
