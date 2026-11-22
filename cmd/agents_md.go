package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/maleolabs/eka-cli/bootstrap"
	"github.com/spf13/cobra"
)

// newAgentsMDCommand builds the `eka agents-md` command: the generated,
// drift-checked AGENTS.md entry point. Both this command and `eka init`
// render the SAME managed block from bootstrap.BuildAgentsMDBlock, whose
// constraint index comes from bootstrap.ConstraintsFromSnapshot reading
// the COMMITTED snapshot at <repo>/exchange/snapshots — never the
// workspace store, so init, agents-md and CI all agree (laptop == CI).
// The index carries the repository's own Discovery + Architecture
// constraints (authority strata 1-2), bounded by the block budget.
//
// The managed region is delimited by fixed markers and is the ONLY part
// this command ever touches: user content outside the markers is
// preserved byte-identical by the same merge `eka init` uses
// (bootstrap.MergeAgentsMD). Deterministic: same repo state and snapshot
// produce byte-identical bytes, so `--check` is a stable CI gate.
//
// Flags:
//
//	--write   regenerate and write the managed region; a current file is
//	          left untouched ("current").
//	--check   regenerate and compare with the on-disk managed region
//	          (the read-only default when neither flag is given).
//	--json    emit a single-line report (combinable with --check/--write).
//
// Exit codes (the CLI 0/1/2 contract):
//
//	0  current (or written, on --write)
//	1  drift (--check: the managed region differs from the generated block)
//	2  usage or IO error (no eka.yaml, no AGENTS.md on --check, an
//	   unreadable/corrupt snapshot, a bad flag combination)
//
// A MISSING snapshot directory is not an error: the block degrades to
// identity-only and the command says so on stderr.
func newAgentsMDCommand() *cobra.Command {
	var write, check, asJSON bool
	cmd := &cobra.Command{
		Use:   "agents-md",
		Short: "Generate or drift-check the AGENTS.md workflow-context block",
		Long: `Generate or drift-check the auto-managed AGENTS.md workflow-context
block — the generated, drift-checked entry point of an EKA repository.

The block carries the repository identity and a bounded index of the
repository's own constraints (the Discovery + Architecture Engineering
Domains, authority strata 1-2): the latest active instance line per
(type, id), grouped by type token. The index is sourced from the
COMMITTED snapshot (<repo>/exchange/snapshots), never the workspace
store, so the same repository state produces the same block everywhere
(laptop == CI). The constraint index is bounded (3200 bytes): an
oversized index is truncated deterministically with a "... +N more
(eka status)" line.

Only the marker-delimited managed region is ever touched — user content
outside the markers is preserved byte-identical. Same generator and
index as 'eka init', so '--check' passes right after 'init'.

Flags:

  --write   regenerate and write the managed region; a current file is
            left untouched.
  --check   compare the on-disk managed region with the generated block
            (the read-only default when neither flag is given).
  --json    emit a single-line machine-readable report:
            {"file":"AGENTS.md","status":"current|drift|missing",
             "constraints":<n>,"blockBytes":<n>,"budget":3200,
             "truncated":<bool>}. Deterministic field order; the same
            store and snapshot yield identical bytes.

Exit codes:
  0  current (or written, on --write)
  1  drift (--check: the managed region differs from the generated block)
  2  usage or IO error (no eka.yaml, no AGENTS.md on --check, an
     unreadable/corrupt snapshot, a bad flag combination)`,
		Example: `  eka agents-md            # check the managed region (exit 1 on drift)
  eka agents-md --check    # the same, explicit
  eka agents-md --write    # regenerate the managed region
  eka agents-md --check --json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAgentsMD(cmd, write, check, asJSON)
		},
	}
	cmd.Flags().BoolVar(&write, "write", false, "regenerate and write the managed region (a current file is left untouched)")
	cmd.Flags().BoolVar(&check, "check", false, "compare the on-disk managed region with the generated block (the read-only default)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit a single-line machine-readable report (combinable with --check/--write)")
	return cmd
}

// agentsMDReport is the `eka agents-md --json` document. Field order is
// fixed (deterministic bytes): file, status, constraints, blockBytes,
// budget, truncated.
type agentsMDReport struct {
	File        string `json:"file"`
	Status      string `json:"status"`
	Constraints int    `json:"constraints"`
	BlockBytes  int    `json:"blockBytes"`
	Budget      int    `json:"budget"`
	Truncated   bool   `json:"truncated"`
}

// runAgentsMD implements the `eka agents-md` contract (see the command
// constructor for the exit codes). It resolves the repository root (the
// tree carrying eka.yaml), reads the identity from eka.yaml, builds the
// constraint index from the committed snapshot, generates the block,
// and either checks or writes it.
func runAgentsMD(cmd *cobra.Command, write, check, asJSON bool) error {
	if write && check {
		return fmt.Errorf("agents-md: --write and --check are mutually exclusive")
	}
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("agents-md: %w", err)
	}
	root, err := snapshotRepositoryRoot(cwd)
	if err != nil {
		return fmt.Errorf("agents-md: %w", err) // Exit 2: no eka.yaml.
	}
	block, omitted, constraints, missingSnapshot, err := bootstrap.RenderAgentsMDBlock(root)
	if err != nil {
		return fmt.Errorf("agents-md: %w", err) // Exit 2.
	}
	if missingSnapshot {
		fmt.Fprintln(cmd.ErrOrStderr(), "eka: agents-md: no committed snapshot (exchange/snapshots) — identity-only block, constraint index omitted")
	}
	path := filepath.Join(root, bootstrap.AgentsMDName)
	existing, rerr := os.ReadFile(path)
	missingFile := false
	if rerr != nil {
		if !os.IsNotExist(rerr) {
			return fmt.Errorf("agents-md: cannot read %s: %w", bootstrap.AgentsMDName, rerr)
		}
		missingFile = true
	}
	merged := bootstrap.MergeAgentsMD(existing, block)
	report := agentsMDReport{
		File:        bootstrap.AgentsMDName,
		Constraints: constraints,
		BlockBytes:  len(block),
		Budget:      bootstrap.AgentsMDBlockBudget,
		Truncated:   omitted > 0,
	}
	switch {
	case missingFile:
		report.Status = "missing"
	case merged != nil:
		report.Status = "drift"
	default:
		report.Status = "current"
	}

	if write {
		return writeAgentsMD(cmd, path, merged, report, asJSON)
	}
	return checkAgentsMD(cmd, existing, merged, report, asJSON)
}

// writeAgentsMD applies the merge: a non-nil merged means the managed
// region changed (or the file was missing) and is written; nil means the
// file is already current. Exit 0 either way.
func writeAgentsMD(cmd *cobra.Command, path string, merged []byte, report agentsMDReport, asJSON bool) error {
	if merged == nil {
		if asJSON {
			return writeAgentsMDJSON(cmd, report)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "%s: current\n", bootstrap.AgentsMDName)
		return nil
	}
	if err := os.WriteFile(path, merged, 0o644); err != nil {
		return fmt.Errorf("agents-md: cannot write %s: %w", bootstrap.AgentsMDName, err)
	}
	report.Status = "current" // the on-disk region is current after the write.
	if asJSON {
		return writeAgentsMDJSON(cmd, report)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "%s: written (%d constraints, %d bytes)\n",
		bootstrap.AgentsMDName, report.Constraints, report.BlockBytes)
	return nil
}

// checkAgentsMD compares the on-disk managed region with the generated
// block: 0 current, 1 drift, 2 when the file is absent (nothing to
// check against). The drift summary names at least the first differing
// line.
func checkAgentsMD(cmd *cobra.Command, existing, merged []byte, report agentsMDReport, asJSON bool) error {
	if asJSON {
		if err := writeAgentsMDJSON(cmd, report); err != nil {
			return err
		}
	} else {
		switch report.Status {
		case "current":
			fmt.Fprintf(cmd.OutOrStdout(), "%s: current\n", bootstrap.AgentsMDName)
		case "drift":
			fmt.Fprintf(cmd.ErrOrStderr(), "eka: agents-md: %s drifted from the generated block\n", bootstrap.AgentsMDName)
			fmt.Fprintf(cmd.ErrOrStderr(), "  first differing line: %s\n", agentsMDFirstDiff(existing, merged))
			fmt.Fprintf(cmd.ErrOrStderr(), "  run 'eka agents-md --write' to regenerate\n")
		case "missing":
			fmt.Fprintf(cmd.ErrOrStderr(), "eka: agents-md: %s does not exist; run 'eka agents-md --write' (or 'eka init') first\n", bootstrap.AgentsMDName)
		}
	}
	switch report.Status {
	case "current":
		return nil
	case "missing":
		return &exitError{code: exitUsage}
	default: // drift
		return &exitError{code: exitFail}
	}
}

// writeAgentsMDJSON emits the report as a single JSON line (fixed field
// order, one trailing newline) — the machine-readable form.
func writeAgentsMDJSON(cmd *cobra.Command, report agentsMDReport) error {
	out, err := json.Marshal(report)
	if err != nil {
		return fmt.Errorf("agents-md: %w", err)
	}
	out = append(out, '\n')
	_, err = cmd.OutOrStdout().Write(out)
	return err
}

// agentsMDFirstDiff returns "line N: <got> (want <want>)" for the first
// line that differs between the on-disk file and the generated merge —
// a concise drift summary.
func agentsMDFirstDiff(existing, merged []byte) string {
	a := strings.Split(string(existing), "\n")
	b := strings.Split(string(merged), "\n")
	for i := 0; i < len(a) || i < len(b); i++ {
		var al, bl string
		if i < len(a) {
			al = a[i]
		}
		if i < len(b) {
			bl = b[i]
		}
		if al != bl {
			return fmt.Sprintf("line %d: %q (want %q)", i+1, al, bl)
		}
	}
	return "(no line difference)"
}
