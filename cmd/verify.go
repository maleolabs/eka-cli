package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/maleolabs/eka-cli/bootstrap"
	"github.com/maleolabs/eka-cli/cmd/ui"
	"github.com/maleolabs/eka-core/exchange"
	"github.com/maleolabs/eka-core/metadata"
	"github.com/maleolabs/eka-core/runtime"
	"github.com/spf13/cobra"
)

// newVerifyCommand builds the `eka verify` command: the repository-scope
// verification gate. It resolves the repository root (the tree carrying
// eka.yaml, reusing snapshotRepositoryRoot) and runs a fixed BUILT-IN
// check set over it — AGENTS.md managed-region drift, committed-snapshot
// integrity and authoring conformance. Each check maps to a constraint
// id and its failing output prints the exact `eka get <constraint-id>`
// command plus the fix, so an agent can act without further retrieval.
//
// This is stage A of sto:verify-changed-gate (dec:m3a-verify-staging): a
// built-in set, NOT declared checks (that mechanism is
// sto:standard-check-declarations, stage B). The registry is a plain
// table (see verifyChecks) — no interface, no factory.
//
// Flags:
//
//	--changed    only run checks whose relevant paths intersect the
//	             changed set (git diff --name-only <base>); an unchanged
//	             check reports verdict "skipped", never a violation.
//	--base ref   the diff base for --changed (default HEAD: staged +
//	             unstaged changes vs HEAD).
//	--json       emit a single deterministic JSON document (fixed field
//	             order, sorted checks/findings, no timestamps).
//	--force      run the checks and print the findings but exit 0 — the
//	             deliberate-violation escape hatch (so the gate is not
//	             skipped with --no-verify).
//
// Exit codes (the CLI 0/1/2 contract):
//
//	0  no blocking violation (warnings never block; --force always 0)
//	1  a blocking violation
//	2  usage or IO error (no eka.yaml, a git diff failure)
//
// A MISSING snapshot directory and a missing docs/ tree are SKIPPED, not
// violations (a fresh v2 repository may have neither). A MISSING
// AGENTS.md is a WARNING, not a blocking violation.
func newVerifyCommand() *cobra.Command {
	var changed, asJSON, force bool
	var base string
	cmd := &cobra.Command{
		Use:   "verify",
		Short: "Run the repository-scope verification gate",
		Long: `Run the repository-scope verification gate: a fixed built-in check
set over the repository rooted at the tree carrying eka.yaml.

Checks (each mapped to a constraint id; a failing check prints the exact
'eka get <constraint-id>' command and the fix):

  agents-md-drift     the AGENTS.md managed region must equal the block
                      generated from the committed snapshot
                      (eka/req:agent-self-describing-surface)
  snapshot-integrity  the committed snapshot (exchange/snapshots) must
                      load and verify (eka/adr:027-source-only-snapshot-transport)
  conformance         the docs/ knowledge tree must pass the conformance
                      rules with no blocking errors (eka/std:eka-standard-1.0.0)

A missing snapshot directory and a missing docs/ tree are SKIPPED, not
violations; a missing AGENTS.md is a warning. Warnings never block.

Flags:
  --changed    only run checks whose relevant paths intersect the changed
               set (git diff --name-only <base>); an unchanged check is
               "skipped".
  --base ref   the diff base for --changed (default HEAD).
  --json       emit a single deterministic JSON document.
  --force      print the findings but exit 0 (the deliberate-violation
               escape hatch).

Exit codes:
  0  no blocking violation (warnings never block; --force always 0)
  1  a blocking violation
  2  usage or IO error (no eka.yaml, a git diff failure)`,
		Example: `  eka verify                  verify the current repository
  eka verify --changed        verify only what changed vs HEAD
  eka verify --changed --base main
  eka verify --json
  eka verify --force          report but do not block`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runVerify(cmd, changed, base, asJSON, force)
		},
	}
	cmd.Flags().BoolVar(&changed, "changed", false, "only run checks whose relevant paths changed vs --base")
	cmd.Flags().StringVar(&base, "base", "HEAD", "the diff base for --changed (a git ref)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit a single deterministic JSON document")
	cmd.Flags().BoolVar(&force, "force", false, "print the findings but exit 0 (deliberate-violation escape hatch)")
	return cmd
}

// Verdict tokens shared by the JSON document and the human renderer.
const (
	verdictPass    = "pass"
	verdictFail    = "fail"
	verdictSkipped = "skipped"
)

// verifyFinding is one finding of one check. Field order is fixed
// (deterministic JSON bytes): file, line, severity, message. Line is a
// pointer so an unknown line serializes as JSON null (never 0).
type verifyFinding struct {
	File     string `json:"file"`
	Line     *int   `json:"line"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
}

// verifyCheckResult is the outcome of one check. Field order is fixed:
// id, constraint, verdict, findings.
type verifyCheckResult struct {
	ID         string          `json:"id"`
	Constraint string          `json:"constraint"`
	Verdict    string          `json:"verdict"`
	Findings   []verifyFinding `json:"findings"`
}

// verifyReport is the `eka verify --json` document. Field order is fixed
// and no field carries a timestamp: root, changed, checks, verdict.
type verifyReport struct {
	Root    string              `json:"root"`
	Changed bool                `json:"changed"`
	Checks  []verifyCheckResult `json:"checks"`
	Verdict string              `json:"verdict"`
}

// verifyCheck is one registry entry of the built-in check set. It is a
// plain data row: id, constraint id, the repository-relative paths that
// make it relevant (for --changed), a human requirement line, the fix
// command, and the run function. No interface, no factory.
type verifyCheck struct {
	id          string
	constraint  string
	paths       []string
	requirement string
	fix         string
	run         func(root string) (findings []verifyFinding, skipped bool)
}

// verifyChecks is the fixed built-in registry (stage A of M3a). Order is
// presentation order; the JSON document sorts by id.
//
// ponytail: this built-in set is a stage-A construct — a declared-checks
// mechanism (sto:standard-check-declarations, M3b) will generalize it.
// Ceiling: checks are compiled in, so a repository cannot declare its
// own. Upgrade path: load declared checks from the standard when M3b
// lands and append them here.
var verifyChecks = []verifyCheck{
	{
		id:          "agents-md-drift",
		constraint:  "eka/req:agent-self-describing-surface",
		paths:       []string{bootstrap.AgentsMDName},
		requirement: "the AGENTS.md managed region must equal the block generated from the committed snapshot",
		fix:         "eka agents-md --write",
		run:         runAgentsMDDriftCheck,
	},
	{
		id:          "snapshot-integrity",
		constraint:  "eka/adr:027-source-only-snapshot-transport",
		paths:       []string{"exchange/snapshots"},
		requirement: "the committed snapshot at exchange/snapshots must load and verify (source-only)",
		fix:         "eka snapshot repair",
		run:         runSnapshotIntegrityCheck,
	},
	{
		id:          "conformance",
		constraint:  "eka/std:eka-standard-1.0.0",
		paths:       []string{"docs"},
		requirement: "the docs/ knowledge tree must pass the conformance rules with no blocking errors",
		fix:         "eka validate",
		run:         runConformanceCheck,
	},
}

// runVerify implements the `eka verify` contract (see the command
// constructor for the exit codes). It resolves the root, computes the
// changed set when requested, runs the built-in checks, renders the
// report and maps the verdict to the exit code (0/1), reserving 2 for
// root resolution and git failures.
func runVerify(cmd *cobra.Command, changed bool, base string, asJSON, force bool) error {
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("verify: %w", err)
	}
	root, err := snapshotRepositoryRoot(cwd)
	if err != nil {
		return fmt.Errorf("verify: %w", err) // Exit 2: no eka.yaml.
	}
	var changedPaths []string
	if changed {
		changedPaths, err = gitChangedFiles(root, base)
		if err != nil {
			return fmt.Errorf("verify: %w", err) // Exit 2: unusable diff base.
		}
	}

	report := verifyReport{Root: root, Changed: changed}
	for _, chk := range verifyChecks {
		res := verifyCheckResult{ID: chk.id, Constraint: chk.constraint, Findings: []verifyFinding{}}
		if changed && !pathsChanged(chk.paths, changedPaths) {
			// Unchanged paths never run: a skipped check is never a
			// violation.
			res.Verdict = verdictSkipped
		} else {
			findings, skipped := chk.run(root)
			res.Findings = sortFindings(findings)
			res.Verdict = checkVerdict(res.Findings, skipped)
		}
		report.Checks = append(report.Checks, res)
	}
	sort.SliceStable(report.Checks, func(i, j int) bool { return report.Checks[i].ID < report.Checks[j].ID })
	report.Verdict = reportVerdict(report.Checks)

	if asJSON {
		if err := writeVerifyJSON(cmd, report); err != nil {
			return err
		}
	} else {
		renderVerify(styleFor(cmd), report)
	}
	// --force keeps the honest verdict in the document but exits 0: the
	// deliberate-violation escape hatch.
	if report.Verdict == verdictFail && !force {
		return &exitError{code: exitFail} // Exit 1: blocking violation.
	}
	return nil
}

// runAgentsMDDriftCheck checks the AGENTS.md managed-region drift (the
// M0 gate): it regenerates the block from the identity (eka.yaml) and
// the committed-snapshot index and fails when the merge is non-nil
// (MergeAgentsMD semantics: nil means "already current"). A missing
// AGENTS.md is a WARNING, not a blocking violation. An unreadable
// eka.yaml/snapshot is a blocking finding (it cannot be certified).
func runAgentsMDDriftCheck(root string) ([]verifyFinding, bool) {
	path := filepath.Join(root, bootstrap.AgentsMDName)
	existing, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []verifyFinding{{
				File:     bootstrap.AgentsMDName,
				Severity: string(severityWarning),
				Message:  "AGENTS.md does not exist; run 'eka agents-md --write' (or 'eka init') to generate it",
			}}, false
		}
		return []verifyFinding{{File: bootstrap.AgentsMDName, Severity: string(severityError), Message: "cannot read AGENTS.md: " + err.Error()}}, false
	}
	raw, err := os.ReadFile(filepath.Join(root, "eka.yaml"))
	if err != nil {
		return []verifyFinding{{File: "eka.yaml", Severity: string(severityError), Message: "cannot read eka.yaml: " + err.Error()}}, false
	}
	meta, err := metadata.Parse(raw)
	if err != nil {
		return []verifyFinding{{File: "eka.yaml", Severity: string(severityError), Message: "invalid eka.yaml: " + err.Error()}}, false
	}
	index, _, err := bootstrap.ConstraintsFromSnapshot(filepath.Join(root, "exchange", "snapshots"))
	if err != nil {
		return []verifyFinding{{File: "exchange/snapshots", Severity: string(severityError), Message: "cannot read the committed snapshot: " + err.Error()}}, false
	}
	block, _ := bootstrap.BuildAgentsMDBlock(meta.Project, meta.Namespace, index)
	merged := bootstrap.MergeAgentsMD(existing, block)
	if merged == nil {
		return nil, false
	}
	return []verifyFinding{{
		File:     bootstrap.AgentsMDName,
		Severity: string(severityError),
		Message:  "managed region drifted from the generated block (" + agentsMDFirstDiff(existing, merged) + "); run 'eka agents-md --write'",
	}}, false
}

// runSnapshotIntegrityCheck loads and verifies the committed snapshot
// with exchange.LoadSnapshot (ADR-027 source-only transport). A missing
// snapshot directory is SKIPPED (a fresh v2 repository may have none);
// a present-but-unloadable directory (corrupt unit, partial aggregate
// state, undecodable) is a blocking finding carrying the error text.
func runSnapshotIntegrityCheck(root string) ([]verifyFinding, bool) {
	dir := filepath.Join(root, "exchange", "snapshots")
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return nil, true // No snapshot directory: skipped, not a violation.
	}
	if _, err := exchange.LoadSnapshot(dir); err != nil {
		return []verifyFinding{{File: "exchange/snapshots", Severity: string(severityError), Message: err.Error()}}, false
	}
	return nil, false
}

// runConformanceCheck runs the store-free authoring conformance gate
// (runtime.Authoring.Validate, CI-safe). A report with no docs/ tree
// (report.Skipped — the normal v2 case) is SKIPPED. A report with
// blocking errors fails; each blocking result carries its rule id in the
// message (no rule→store-id mapping is invented — the constraint is the
// standard itself). Warnings are reported but never block.
func runConformanceCheck(root string) ([]verifyFinding, bool) {
	report, err := runtime.Authoring.Validate(root)
	if err != nil {
		return []verifyFinding{{File: "docs", Severity: string(severityError), Message: "cannot run the conformance gate: " + err.Error()}}, false
	}
	if report.Skipped != "" {
		return nil, true // No docs/ tree: skipped.
	}
	var findings []verifyFinding
	for _, res := range report.SortedResults() {
		findings = append(findings, verifyFinding{
			File:     res.File,
			Severity: string(res.Severity),
			Message:  res.Rule + ": " + res.Message,
		})
	}
	return findings, false
}

// severityError and severityWarning mirror conformance's severity tokens
// (the JSON contract's "error"|"warning").
const (
	severityError   = "error"
	severityWarning = "warning"
)

// checkVerdict maps a check's findings/skip flag to its verdict: skipped
// wins, then any error finding fails, else pass. Warnings never fail.
func checkVerdict(findings []verifyFinding, skipped bool) string {
	if skipped {
		return verdictSkipped
	}
	for _, f := range findings {
		if f.Severity == severityError {
			return verdictFail
		}
	}
	return verdictPass
}

// reportVerdict is the top-level verdict: fail when any check failed.
func reportVerdict(checks []verifyCheckResult) string {
	for _, c := range checks {
		if c.Verdict == verdictFail {
			return verdictFail
		}
	}
	return verdictPass
}

// sortFindings returns findings in deterministic order (file, then
// severity error-before-warning, then message). A nil/empty input
// returns an empty slice, never nil, so JSON carries [].
func sortFindings(findings []verifyFinding) []verifyFinding {
	out := make([]verifyFinding, len(findings))
	copy(out, findings)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.File != b.File {
			return a.File < b.File
		}
		if a.Severity != b.Severity {
			return a.Severity == severityError
		}
		return a.Message < b.Message
	})
	return out
}

// pathsChanged reports whether any changed path intersects the check's
// relevant paths: an exact match, or a path under a relevant directory
// (prefix + "/").
func pathsChanged(paths, changed []string) bool {
	for _, p := range paths {
		for _, c := range changed {
			if c == p || strings.HasPrefix(c, p+"/") {
				return true
			}
		}
	}
	return false
}

// gitChangedFiles returns the changed paths (repo-relative, slash form)
// reported by `git diff --name-only <base>` run at root.
//
// ponytail: untracked files are not in `git diff` output, so a brand-new
// un-staged file is not "changed" for --changed. Ceiling: untracked
// additions are missed. Upgrade path: also union `git ls-files
// --others --exclude-standard` if a hook needs to gate new files.
func gitChangedFiles(root, base string) ([]string, error) {
	c := exec.Command("git", "diff", "--name-only", base)
	c.Dir = root
	out, err := c.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("git diff --name-only %s: %w", base, err)
	}
	var files []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			files = append(files, filepath.ToSlash(line))
		}
	}
	return files, nil
}

// writeVerifyJSON emits the report as a single JSON document plus one
// trailing newline (fixed field order; deterministic bytes).
func writeVerifyJSON(cmd *cobra.Command, report verifyReport) error {
	out, err := json.Marshal(report)
	if err != nil {
		return fmt.Errorf("verify: %w", err)
	}
	out = append(out, '\n')
	_, err = cmd.OutOrStdout().Write(out)
	return err
}

// renderVerify prints the human report in the deterministic non-TTY
// style (plain text, no ANSI when piped): the context header, one line
// per check, and — for every failing check — the finding with its file
// and line, the requirement, the exact `eka get <constraint-id>` read
// command and the fix. It closes with the verdict.
func renderVerify(s *ui.Style, report verifyReport) {
	ui.NewHeader(s, "Verify").
		Add("Root", report.Root).
		Add("Changed", strconv.FormatBool(report.Changed)).
		Pipeline("Verify").
		Render()

	fmt.Fprintf(s.W, "\nChecks:\n")
	for _, c := range report.Checks {
		fmt.Fprintf(s.W, "  [%s] %s (%s)\n", strings.ToUpper(c.Verdict), c.ID, c.Constraint)
		for _, f := range c.Findings {
			loc := f.File
			if f.Line != nil {
				loc = fmt.Sprintf("%s:%d", f.File, *f.Line)
			}
			fmt.Fprintf(s.W, "    %s %s: %s\n", strings.ToUpper(f.Severity), loc, f.Message)
		}
		if c.Verdict != verdictFail {
			continue
		}
		chk, ok := verifyCheckByID(c.ID)
		if !ok {
			continue
		}
		// Actionable block: an agent can fix without further retrieval.
		fmt.Fprintf(s.W, "    required: %s\n", chk.requirement)
		fmt.Fprintf(s.W, "    read:     eka get %s\n", chk.constraint)
		fmt.Fprintf(s.W, "    fix:      %s\n", chk.fix)
	}

	verdict := strings.ToUpper(report.Verdict)
	if report.Verdict == verdictFail {
		verdict = s.Error(verdict)
	} else {
		verdict = s.Success(verdict)
	}
	fmt.Fprintf(s.W, "\nVerdict: %s\n", verdict)
}

// verifyCheckByID looks up a registry entry by id.
func verifyCheckByID(id string) (verifyCheck, bool) {
	for _, c := range verifyChecks {
		if c.id == id {
			return c, true
		}
	}
	return verifyCheck{}, false
}
