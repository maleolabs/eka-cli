package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/maleolabs/eka-cli/bootstrap"
	"github.com/maleolabs/eka-cli/cmd/ui"
	"github.com/spf13/cobra"
)

// newInitCommand builds the `eka init` command: bootstrap a new EKA
// repository. All bootstrap logic lives in the bootstrap engine; this
// command validates arguments, renders the outcome and maps the result
// to the exit code contract.
//
// `eka init` is identity-only: the generated repository contains
// eka.yaml only (+ optional git init). The legacy docs-in-repo skeleton
// is never scaffolded — knowledge lives in the EKA workspace.
//
// Exit codes:
//
//	0  init completed and the generated repository validates; dry-run
//	1  init completed but validation found blocking violations
//	2  usage or internal error (unknown flag, invalid project id or
//	   namespace, generation failure)
func newInitCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "init [name]",
		Short: "Bootstrap a new EKA repository",
		Long: `Bootstrap a new EKA repository at the current directory or at the
directory name (relative to the current directory). The generated
repository contains eka.yaml only (+ optional git init): the identity
file carrying the project id, the repository name and the namespace.
The legacy docs-in-repo skeleton is never generated — knowledge lives
in the EKA workspace. If name already exists as a directory it is
adopted; existing files with identical content are reused, conflicting
files are never overwritten silently.

Identity options:
  --project     fix the project id (eka.yaml project)
  --namespace   fix the namespace (eka.yaml namespace)
When given, the corresponding wizard question is skipped. Both must be
valid EKA identifiers (lowercase letters, digits, single hyphens).

Prompts (project id, namespace, git init) are asked only when stdin is
a terminal; otherwise deterministic defaults are used and git is never
initialized.

Exit codes:
  0  init completed and the generated repository validates
  1  init completed but validation found blocking violations
  2  usage or internal error (unknown flag, invalid project id or
     namespace, generation failure)`,
		Example: `  eka init              bootstrap the current directory
  eka init myproject    create and bootstrap ./myproject
  eka init --project atrium --namespace atrium-api
                        bootstrap with a fixed identity
  eka init --dry-run    preview the plan without writing anything`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dryRun, err := cmd.Flags().GetBool("dry-run")
			if err != nil {
				return fmt.Errorf("init failed: %w", err)
			}
			project, err := cmd.Flags().GetString("project")
			if err != nil {
				return fmt.Errorf("init failed: %w", err)
			}
			namespace, err := cmd.Flags().GetString("namespace")
			if err != nil {
				return fmt.Errorf("init failed: %w", err)
			}
			target := "."
			if len(args) == 1 {
				name := args[0]
				if strings.ContainsAny(name, `/\`) {
					return fmt.Errorf("project name %q must not contain path separators", name)
				}
				target = name
			}
			// Refuse targets that exist as plain files; directories are
			// adopted.
			if info, err := os.Stat(target); err == nil && !info.IsDir() {
				return fmt.Errorf("target %q exists and is not a directory", target)
			}

			s := styleFor(cmd)
			outcome, err := bootstrap.Run(bootstrap.Options{
				Target:    target,
				Project:   project,
				Namespace: namespace,
				DryRun:    dryRun,
				Stdin:     cmd.InOrStdin(),
				Stdout:    cmd.OutOrStdout(),
				Stderr:    cmd.ErrOrStderr(),
			})
			if err != nil {
				return fmt.Errorf("init failed: %w", err)
			}

			renderInit(s, outcome)
			if outcome.DryRun {
				return nil
			}
			if outcome.Report != nil && !outcome.Report.Pass() {
				fmt.Fprintf(cmd.ErrOrStderr(),
					"eka: init generated a non-conformant repository (see validation results above)\n")
				return &exitError{code: exitFail}
			}
			return nil
		},
	}
	cmd.Flags().Bool("dry-run", false,
		"preview the plan (identity file, git, validation); writes nothing")
	cmd.Flags().String("project", "",
		"fix the project id (eka.yaml project); a valid EKA identifier")
	cmd.Flags().String("namespace", "",
		"fix the namespace (eka.yaml namespace); a valid EKA identifier")
	return cmd
}

// initStageNames are the five bootstrap stages (see bootstrap.Bootstrap
// docs). The tree labels carry the deterministic "[i/5] " prefix.
const (
	initStageDiscover  = "Discover workspace"
	initStagePlan      = "Plan bootstrap"
	initStageConfigure = "Configure (interactive)"
	initStageGenerate  = "Generate repository"
	initStageValidate  = "Validate"
)

// initHeader renders the context header identifying the repository
// object and the bootstrap pipeline.
func initHeader(s *ui.Style, o *bootstrap.Outcome) {
	ui.NewHeader(s, "Repository").
		Add("Project", o.Project).
		Add("Target", o.Target).
		Add("Namespace", o.Namespace).
		Add("Knowledge", "EKA v"+standardVersion).
		Pipeline("Bootstrap").
		Render()
}

// renderInit renders the init outcome: the context header, then a
// progressive tree over the five bootstrap stages plus the closing
// summary. On a non-TTY the stage lines are emitted sequentially with
// deterministic details; the tree is fully rendered by the time the
// command returns.
func renderInit(s *ui.Style, o *bootstrap.Outcome) {
	initHeader(s, o)
	if o.DryRun {
		renderInitDryRun(s, o)
		return
	}

	tree := ui.NewTree(s, "Repository")
	tree.Add(ui.Step(1, 5) + initStageDiscover).Done(discoveryDetail(o))
	tree.Add(ui.Step(2, 5) + initStagePlan).Done(fmt.Sprintf("%s planned", plural(len(o.Plan), "action", "actions")))
	tree.Add(ui.Step(3, 5) + initStageConfigure).Done(fmt.Sprintf("project: %s, namespace: %s", o.Project, o.Namespace))
	tree.Add(ui.Step(4, 5) + initStageGenerate).Done(generateDetail(o))

	validate := tree.Add(ui.Step(5, 5) + initStageValidate)
	if o.Report == nil {
		validate.Done("not run")
	} else if o.Report.Pass() {
		validate.Done(validationDetail(o.Report))
	} else {
		validate.Fail(validationDetail(o.Report))
	}
	tree.Finish()

	// A failing validation must stay diagnosable: the findings are
	// printed under the failed leaf (never color alone).
	if o.Report != nil && !o.Report.Pass() {
		renderFindings(s, o.Report)
	}

	if s.Verbose {
		planItems := make([]string, 0, len(o.Plan))
		for _, a := range o.Plan {
			planItems = append(planItems, a.String())
		}
		s.Bullets("Plan actions:", planItems)
		s.Bullets("Created dirs:", o.CreatedDirs)
		s.Bullets("Created files:", o.CreatedFiles)
		s.Bullets("Reused files:", o.ReusedFiles)
		s.Bullets("Overwritten files:", o.OverwrittenFiles)
		s.Bullets("Skipped files:", o.SkippedFiles)
	}

	ui.NewSummary(s).
		Add("Project", o.Project).
		Add("Namespace", o.Namespace).
		Add("Repository Type", o.RepoType).
		Add("Git Status", o.GitStatus).
		Add("Standard", "EKA v"+standardVersion).
		Add("Validation", validationDetail(o.Report)).
		Render()
	renderInitIdentity(s, o)
}

// renderInitIdentity renders the repository identity line when the run
// planned the eka.yaml generation: the deterministic one-liner listing
// the identity file and its recorded triple. The identity is editable
// in eka.yaml until the first sync freezes it.
func renderInitIdentity(s *ui.Style, o *bootstrap.Outcome) {
	if o.Identity == nil {
		return
	}
	fmt.Fprintf(s.W, "%s eka.yaml (project %s, name %s, namespace %s — edit eka.yaml before the first sync to change it)\n",
		s.Info("Identity:"), o.Identity.Project, o.Identity.Name, o.Identity.Namespace)
}

// renderInitDryRun prints the plan as a tree: every planned action is
// a node (no spinner — nothing is executed). The summary closes with
// the same fields as a real run, with the plan-derived git status and
// "not run" validation.
func renderInitDryRun(s *ui.Style, o *bootstrap.Outcome) {
	tree := ui.NewTree(s, "Bootstrap plan (dry-run)")
	for _, a := range o.Plan {
		detail := ""
		if s.Verbose && len(a.Content) > 0 {
			detail = fmt.Sprintf("writes %d bytes", len(a.Content))
		}
		tree.Add(a.String()).Done(detail)
	}
	tree.Finish()
	fmt.Fprintln(s.W, s.Dim("Dry-run: no changes were written."))

	ui.NewSummary(s).
		Add("Project", o.Project).
		Add("Namespace", o.Namespace).
		Add("Repository Type", o.RepoType).
		Add("Git Status", dryRunGitStatus(o.Plan)).
		Add("Standard", "EKA v"+standardVersion).
		Add("Validation", "not run (dry-run)").
		Render()
	// Same identity line as a real run: deterministic output for the
	// same inputs.
	renderInitIdentity(s, o)
}

// dryRunGitStatus derives the git status of a dry-run from the plan
// (the generation stage never runs, so Outcome.GitStatus stays empty).
func dryRunGitStatus(plan []bootstrap.Action) string {
	for _, a := range plan {
		switch a.Kind {
		case bootstrap.ActionGitInit:
			return "planned (git init)"
		case bootstrap.ActionGitSkip:
			return a.Detail
		}
	}
	return "not planned"
}

// discoveryDetail renders the deterministic stage-1 detail from the
// repository classification.
func discoveryDetail(o *bootstrap.Outcome) string {
	switch o.RepoType {
	case "new":
		return "target does not exist"
	case "existing-dir":
		return "existing directory adopted"
	case "existing-eka":
		return "existing EKA repository (already initialized)"
	default:
		return o.RepoType
	}
}

// generateDetail renders the deterministic stage-4 detail from the
// generation counters. An already-initialized repository that created
// nothing keeps the "no changes made" line; an adoption run (legacy
// repository that just gained eka.yaml) reports the generic counters
// line so the created file is visible.
func generateDetail(o *bootstrap.Outcome) string {
	if o.AlreadyInitialized && len(o.CreatedFiles) == 0 && len(o.CreatedDirs) == 0 {
		return "no changes made (already initialized)"
	}
	return fmt.Sprintf("created %s, %s; reused %d; overwritten %d; skipped %d",
		plural(len(o.CreatedDirs), "dir", "dirs"),
		plural(len(o.CreatedFiles), "file", "files"),
		len(o.ReusedFiles), len(o.OverwrittenFiles), len(o.SkippedFiles))
}
