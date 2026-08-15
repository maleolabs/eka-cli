// Package bootstrap implements the official `eka init` engine: workspace
// discovery, bootstrap planning, the interactive wizard, repository
// generation from the deterministic plan, and post-generation validation.
//
// The package is a reusable engine (like conformance/), deliberately free of
// CLI concerns: it talks to the caller through Options/Outcome and never
// prints or exits on its own. cmd/eka remains a thin orchestration layer.
//
// Five-stage model (each stage is a separate component in this package):
//
//  1. Workspace Discovery  — discover.go: inspect the target directory.
//  2. Bootstrap Planning   — plan.go: deterministic action list derived
//     from discovery + wizard answers.
//  3. Interactive Wizard   — wizard.go: asks the project id, the namespace
//     and the git init question — the answers the identity file needs.
//  4. Repository Generation — generate.go: apply the plan, never
//     overwriting silently.
//  5. Validation           — validate.go: run conformance.Validate over the
//     generated repository.
//
// `eka init` is identity-only (ADR-020 Phase B1): the generated repository
// contains eka.yaml only (+ optional git init). The legacy docs-in-repo
// skeleton is never scaffolded — knowledge lives in the EKA workspace and
// docs-in-repo remains legacy backward-compat, never generated.
//
// Behavioral contracts:
//
//   - Output is deterministic: the same target and answers always produce
//     the same plan and the same written bytes.
//   - Nothing is ever overwritten silently. Existing files with identical
//     content are reused; existing files with different content require
//     explicit confirmation (interactive) or are skipped (non-interactive).
//   - Non-interactive runs (stdin is not a terminal) skip all prompts, use
//     discovery-derived defaults, and never run `git init`.
package bootstrap

import (
	"fmt"
	"io"
	"os"

	"github.com/maleolabs/eka-core/conformance"
	"github.com/maleolabs/eka-core/metadata"
	"golang.org/x/term"
)

// Options configures one Run.
type Options struct {
	// Target is the directory to bootstrap (default: "."). An empty string
	// means the current directory.
	Target string
	// Project pre-answers the project id question (eka.yaml `project`):
	// when non-empty the wizard skips it. Must be a valid EKA identifier.
	Project string
	// Namespace pre-answers the namespace question (eka.yaml
	// `namespace`): when non-empty the wizard skips it. Must be a valid
	// EKA identifier.
	Namespace string
	// DryRun prints the plan and writes nothing.
	DryRun bool
	// Stdin feeds the wizard in interactive mode and overwrite
	// confirmations. When Stdin is not a terminal (not an *os.File, or an
	// *os.File whose fd is not a terminal — pipes, regular files,
	// /dev/null), the run is non-interactive and all prompts are skipped.
	Stdin io.Reader
	// Stdout receives wizard prompts and generation output.
	Stdout io.Writer
	// Stderr receives warnings (e.g. a failed `git init`).
	Stderr io.Writer
	// Validate overrides the validation stage (default: conformance.Validate).
	// Injectable for tests.
	Validate Validator
	// GitInit overrides the `git init` runner (default: exec.Command git
	// init with inherited output). Injectable for tests.
	GitInit func(dir string, stdout, stderr io.Writer) error
}

// Outcome carries everything a caller needs to render the run result:
// the summary fields, the deterministic plan, the repository identity
// metadata (when eka.yaml is generated), generation counters, and the
// validation report (nil when the run was dry-run only).
type Outcome struct {
	// Target is the target directory as given by the caller.
	Target string
	// Project is the repository project id written to eka.yaml (ADR-017).
	Project string
	// Namespace is the frontmatter namespace written to eka.yaml.
	Namespace string
	// DryRun mirrors Options.DryRun.
	DryRun bool
	// AlreadyInitialized reports that the target was already an EKA
	// repository; the plan then contains only reuse + validate.
	AlreadyInitialized bool
	// RepoType classifies the target: "new" (created by this run),
	// "existing-dir" (existed, adopted), or "existing-eka" (already an EKA
	// repository).
	RepoType string
	// GitStatus is one of "initialized", "existing", "skipped (…)" or
	// "failed (…)".
	GitStatus string
	// Plan is the deterministic action list, in execution order.
	Plan []Action
	// Identity is the repository identity metadata the plan writes to
	// eka.yaml (project, name, namespace — ADR-017); nil when no
	// eka.yaml generation is planned (e.g. the reuse-only plan of an
	// already-initialized repository, or an existing eka.yaml that
	// would only be reused/overwrite-confirmed).
	Identity *metadata.Metadata

	// Generation counters (empty for dry-run runs).
	CreatedDirs      []string
	CreatedFiles     []string
	ReusedFiles      []string
	OverwrittenFiles []string
	SkippedFiles     []string

	// Report is the post-generation validation result; nil when DryRun.
	Report *conformance.Report
}

// Run executes the five bootstrap stages against Options.Target.
//
// It returns an error only for usage/internal failures (undiscoverable
// target, invalid flag values, generation failure). A failing validation
// is not an error: it is reported through Outcome.Report so callers can
// map it to exit code 1.
func Run(opts Options) (*Outcome, error) {
	if opts.Target == "" {
		opts.Target = "."
	}
	if opts.Stdout == nil {
		opts.Stdout = io.Discard
	}
	if opts.Stderr == nil {
		opts.Stderr = io.Discard
	}
	if opts.Validate == nil {
		opts.Validate = conformance.Validate
	}
	// Flag pre-answers must be valid EKA identifiers (the wizard's
	// re-prompt loop would enforce the same rule interactively).
	if opts.Project != "" && !IsValidIdent(opts.Project) {
		return nil, fmt.Errorf("invalid project id %q: use lowercase letters, digits and hyphens only", opts.Project)
	}
	if opts.Namespace != "" && !IsValidIdent(opts.Namespace) {
		return nil, fmt.Errorf("invalid namespace %q: use lowercase letters, digits and hyphens only", opts.Namespace)
	}

	// Stage 1: discovery.
	d, err := Discover(opts.Target)
	if err != nil {
		return nil, fmt.Errorf("discovery failed: %w", err)
	}
	if d.Exists && !d.IsDir {
		return nil, fmt.Errorf("target is not a directory: %s", opts.Target)
	}

	// Stage 3 (answers feed planning): interactive wizard when stdin is a
	// terminal and the run is not a dry-run, deterministic defaults
	// otherwise. An already-initialized EKA repository skips the wizard
	// entirely: discovery answers everything. Flag pre-answers override
	// whichever branch produced the answers, so `eka init --project X`
	// fixes the identity on every path (fresh, adopted, already-eka).
	interactive := !opts.DryRun && isInteractive(opts.Stdin)
	already := d.Exists && d.IsEkaRepo
	var answers Answers
	switch {
	case already:
		answers = DefaultAnswers(d)
	case interactive:
		answers, err = Ask(d, opts.Stdin, opts.Stdout, PreAnswers{Project: opts.Project, Namespace: opts.Namespace})
		if err != nil {
			return nil, fmt.Errorf("wizard failed: %w", err)
		}
	default:
		answers = DefaultAnswers(d)
	}
	if opts.Project != "" {
		answers.Project = opts.Project
	}
	if opts.Namespace != "" {
		answers.Namespace = opts.Namespace
	}

	// Stage 2: deterministic plan derived from discovery + answers.
	plan := BuildPlan(opts.Target, d, answers)
	out := &Outcome{
		Target:             opts.Target,
		Project:            answers.Project,
		Namespace:          answers.Namespace,
		DryRun:             opts.DryRun,
		AlreadyInitialized: already,
		RepoType:           repoType(d),
		Plan:               plan,
	}
	// The identity is reported only when the plan actually generates
	// eka.yaml (an overwrite-confirm or reuse step does not guarantee
	// the written bytes, so no identity is claimed). The loop covers the
	// adoption branch too: a legacy repository (docs markers, no
	// eka.yaml) plans the generation action, so the run reports the
	// deterministic identity (project = the wizard project id, name =
	// basename, namespace = the wizard namespace — editable before the
	// first sync). The reported identity shares the plan's derivation:
	// the same answers produce the same bytes in both.
	for _, a := range plan {
		if a.Kind == ActionGenerateEkaYAML {
			out.Identity = &metadata.Metadata{
				Version:   metadata.SchemaVersion,
				Project:   answers.Project,
				Name:      ekaYAMLName(d),
				Namespace: answers.Namespace,
			}
			break
		}
	}
	if opts.DryRun {
		return out, nil
	}

	// Stage 4: apply the plan.
	res, err := Apply(opts.Target, plan, ApplyOptions{
		Interactive: interactive,
		Stdin:       opts.Stdin,
		Stdout:      opts.Stdout,
		Stderr:      opts.Stderr,
		GitInit:     opts.GitInit,
	})
	if err != nil {
		return nil, fmt.Errorf("generation failed: %w", err)
	}
	out.CreatedDirs = res.CreatedDirs
	out.CreatedFiles = res.CreatedFiles
	out.ReusedFiles = res.ReusedFiles
	out.OverwrittenFiles = res.OverwrittenFiles
	out.SkippedFiles = res.SkippedFiles
	out.GitStatus = res.GitStatus
	if !planHasGitAction(plan) {
		// The reuse-only plan (already-initialized repository) carries no
		// git action; report the discovered state instead.
		if d.IsGitRepo {
			out.GitStatus = "existing"
		} else {
			out.GitStatus = "skipped (no git action planned)"
		}
	}

	// Stage 5: validation.
	report, err := RunValidation(opts.Target, opts.Validate)
	if err != nil {
		return nil, fmt.Errorf("validation failed: %w", err)
	}
	out.Report = report
	return out, nil
}

// repoType classifies the target from discovery results.
func repoType(d *Discovery) string {
	switch {
	case d.Exists && d.IsEkaRepo:
		return "existing-eka"
	case !d.Exists:
		return "new"
	default:
		return "existing-dir"
	}
}

// planHasGitAction reports whether the plan carries a git step.
func planHasGitAction(plan []Action) bool {
	for _, a := range plan {
		if a.Kind == ActionGitInit || a.Kind == ActionGitSkip {
			return true
		}
	}
	return false
}

// isInteractive reports whether r is a real terminal: an *os.File whose
// underlying fd answers true to term.IsTerminal. The char-device heuristic
// (os.ModeCharDevice) is not sufficient: /dev/null is a char device but
// must be treated as non-interactive so that `eka init < /dev/null` in
// scripts/CI never prompts and never runs `git init`. Any other reader
// (strings.Reader, bytes.Buffer, a pipe) is also non-interactive so that
// piped input can never block a run.
func isInteractive(r io.Reader) bool {
	if r == nil {
		return false
	}
	f, ok := r.(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(int(f.Fd()))
}
