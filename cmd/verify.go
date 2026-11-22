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
	"github.com/maleolabs/eka-core/conformance"
	"github.com/maleolabs/eka-core/exchange"
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
// (deterministic JSON bytes): file, line, severity, message.
//
// Line is the 1-based file line of the finding when the check can
// attribute one (the AGENTS.md drift finding carries the line of the
// first differing line); it is nil — JSON null — when no line is
// available, which is the case for every finding that is not
// line-attributable, including all conformance results (the eka-core
// conformance.Result exposes no position) and repository-level findings.
// A line is never invented.
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
//
// The run function receives the changed set (nil when --changed was not
// passed) so a check can scope itself: the conformance check filters its
// results to the changed files; the drift and snapshot checks are
// whole-repository by nature and ignore it.
type verifyCheck struct {
	id          string
	constraint  string
	paths       []string
	requirement string
	fix         string
	run         func(root string, changedPaths []string) (findings []verifyFinding, skipped bool)
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
		paths:       []string{bootstrap.AgentsMDName, "exchange/snapshots", "eka.yaml"},
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
		// A --base that starts with "-" would be parsed by git as an
		// option (never a ref), silently yielding an empty changed set and
		// an all-skipped PASS. Reject it as a usage error (exit 2).
		if strings.HasPrefix(base, "-") {
			return fmt.Errorf("verify: --base %q is not a valid git ref", base)
		}
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
			findings, skipped := chk.run(root, changedPaths)
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
	// Opt-in telemetry (spec:telemetry-capture:1): the verify verdict is
	// recorded as a gate event only when a session id is present. It is
	// best-effort and never changes the exit code or the output.
	if report.Verdict == verdictFail {
		telemetryAutoEmit(telKindGateRefusal, "", "", telGateVerify, verifyFailReason(report))
	} else {
		telemetryAutoEmit(telKindGatePass, "", "", telGateVerify, "")
	}
	// --force keeps the honest verdict in the document but exits 0: the
	// deliberate-violation escape hatch.
	if report.Verdict == verdictFail && !force {
		return &exitError{code: exitFail} // Exit 1: blocking violation.
	}
	return nil
}

// verifyFailReason is the deterministic reason string of a failing
// verify verdict: the sorted, comma-joined ids of the failing checks.
func verifyFailReason(report verifyReport) string {
	var ids []string
	for _, c := range report.Checks {
		if c.Verdict == verdictFail {
			ids = append(ids, c.ID)
		}
	}
	sort.Strings(ids)
	return "verify failed: " + strings.Join(ids, ",")
}

// runAgentsMDDriftCheck checks the AGENTS.md managed-region drift (the
// M0 gate): it regenerates the block from the identity (eka.yaml) and
// the committed-snapshot index and fails when the merge is non-nil
// (MergeAgentsMD semantics: nil means "already current"). A missing
// AGENTS.md is a WARNING, not a blocking violation. An unreadable
// eka.yaml/snapshot is a blocking finding (it cannot be certified). The
// changed set is ignored: the block derives from eka.yaml and the whole
// snapshot, so the check is whole-repository by nature.
func runAgentsMDDriftCheck(root string, _ []string) ([]verifyFinding, bool) {
	path := filepath.Join(root, bootstrap.AgentsMDName)
	existing, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []verifyFinding{{
				File:     bootstrap.AgentsMDName,
				Severity: severityWarning,
				Message:  "AGENTS.md does not exist; run 'eka agents-md --write' (or 'eka init') to generate it",
			}}, false
		}
		return []verifyFinding{{File: bootstrap.AgentsMDName, Severity: severityError, Message: "cannot read AGENTS.md: " + err.Error()}}, false
	}
	block, _, _, _, err := bootstrap.RenderAgentsMDBlock(root)
	if err != nil {
		return []verifyFinding{{File: bootstrap.AgentsMDName, Severity: severityError, Message: err.Error()}}, false
	}
	merged := bootstrap.MergeAgentsMD(existing, block)
	if merged == nil {
		return nil, false
	}
	summary := agentsMDFirstDiff(existing, merged)
	return []verifyFinding{{
		File:     bootstrap.AgentsMDName,
		Line:     verifyFirstDiffLine(summary),
		Severity: severityError,
		Message:  "managed region drifted from the generated block (" + summary + "); run 'eka agents-md --write'",
	}}, false
}

// verifyFirstDiffLine extracts the 1-based line from the first-diff
// summary agentsMDFirstDiff produces ("line N: ..."); nil when the
// summary carries no line. It never invents a line.
func verifyFirstDiffLine(summary string) *int {
	const prefix = "line "
	if !strings.HasPrefix(summary, prefix) {
		return nil
	}
	digits := summary[len(prefix):]
	if i := strings.IndexByte(digits, ':'); i >= 0 {
		digits = digits[:i]
	}
	n, err := strconv.Atoi(digits)
	if err != nil {
		return nil
	}
	return &n
}

// runSnapshotIntegrityCheck loads and verifies the committed snapshot
// with exchange.LoadSnapshot (ADR-027 source-only transport). A missing
// snapshot directory is SKIPPED (a fresh v2 repository may have none);
// a present-but-unloadable directory (corrupt unit, partial aggregate
// state, undecodable) is a blocking finding carrying the error text. The
// changed set is ignored: the snapshot is verified as one package.
func runSnapshotIntegrityCheck(root string, _ []string) ([]verifyFinding, bool) {
	dir := filepath.Join(root, "exchange", "snapshots")
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return nil, true // No snapshot directory: skipped, not a violation.
	}
	if _, err := exchange.LoadSnapshot(dir); err != nil {
		return []verifyFinding{{File: "exchange/snapshots", Severity: severityError, Message: err.Error()}}, false
	}
	return nil, false
}

// runConformanceCheck runs the store-free authoring conformance gate
// (runtime.Authoring.Validate, CI-safe). A report with no docs/ tree
// (report.Skipped — the normal v2 case) is SKIPPED. A report with
// blocking errors fails; each blocking result carries its rule id in the
// message (no rule→store-id mapping is invented — the constraint is the
// standard itself). Warnings are reported but never block.
//
// When changedPaths is non-empty (--changed), the report results are
// filtered to the files in the changed set (exact repo-relative slash
// match on res.File): a pre-existing unrelated error in docs/ does not
// block a change to a clean file. With no changed set the scan is the
// full tree (unchanged behaviour).
func runConformanceCheck(root string, changedPaths []string) ([]verifyFinding, bool) {
	report, err := runtime.Authoring.Validate(root)
	if err != nil {
		return []verifyFinding{{File: "docs", Severity: severityError, Message: "cannot run the conformance gate: " + err.Error()}}, false
	}
	if report.Skipped != "" {
		return nil, true // No docs/ tree: skipped.
	}
	results := report.SortedResults()
	if len(changedPaths) > 0 {
		inChanged := make(map[string]bool, len(changedPaths))
		for _, p := range changedPaths {
			inChanged[p] = true
		}
		filtered := make([]conformance.Result, 0, len(results))
		for _, res := range results {
			if inChanged[filepath.ToSlash(res.File)] {
				filtered = append(filtered, res)
			}
		}
		results = filtered
	}
	var findings []verifyFinding
	for _, res := range results {
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

// gitChangedFiles returns the changed paths (repo-relative, slash form,
// sorted) of the working tree vs base: the modified/staged set from
// `git diff --name-only <base>` UNIONED with the untracked, non-ignored
// files from `git ls-files --others --exclude-standard` (so a brand-new
// un-staged file is visible to --changed).
//
// ponytail: ignored files (matched by .gitignore) are deliberately out —
// they are not part of the repository. Remaining ceiling: a path deleted
// from the working tree still appears in the diff (fine), and a
// submodule/DENY-listed path is not expanded. Upgrade path: none needed
// until a hook must gate ignored artifacts.
func gitChangedFiles(root, base string) ([]string, error) {
	diffOut, err := gitOutput(root, "diff", "--name-only", base)
	if err != nil {
		return nil, fmt.Errorf("git diff --name-only %s: %w", base, err)
	}
	othersOut, err := gitOutput(root, "ls-files", "--others", "--exclude-standard")
	if err != nil {
		return nil, fmt.Errorf("git ls-files --others: %w", err)
	}
	seen := map[string]bool{}
	var files []string
	for _, line := range strings.Split(string(diffOut)+"\n"+string(othersOut), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		slash := filepath.ToSlash(line)
		if seen[slash] {
			continue
		}
		seen[slash] = true
		files = append(files, slash)
	}
	sort.Strings(files)
	return files, nil
}

// gitOutput runs one git command at root and returns its combined
// stdout.
func gitOutput(root string, args ...string) ([]byte, error) {
	c := exec.Command("git", args...)
	c.Dir = root
	return c.CombinedOutput()
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
