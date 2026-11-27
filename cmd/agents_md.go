package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/maleolabs/eka-cli/v2/bootstrap"
	"github.com/maleolabs/eka-core/v2/metadata"
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
//	--nested  project the binding index per directory: emit D/AGENTS.md for
//	          every directory that directly holds a tracked file and has a
//	          non-empty bound constraint index, plus per-host shims.
//	--host    emit per-host shims for the declared target(s); requires
//	          --nested. Repeatable.
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
	var write, check, asJSON, nested bool
	var hosts []string
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
  --nested  project the binding index per directory (D/AGENTS.md for every
            directory that directly holds a tracked file and has a
            non-empty bound index) and, with --host, per-host shims.
  --host    emit per-host shims for the declared target only, and only if
            that host is configured (.claude/ or .cursor/ present);
            opencode reads nested AGENTS.md natively (no-op). Repeatable.
            Requires --nested. claude → .claude/rules/<slug>.md,
            cursor → .cursor/rules/<slug>.mdc.

Exit codes:
  0  current (or written, on --write)
  1  drift (--check: the managed region differs from the generated block)
  2  usage or IO error (no eka.yaml, no AGENTS.md on --check, an
     unreadable/corrupt snapshot, a bad flag combination)`,
		Example: `  eka agents-md            # check the managed region (exit 1 on drift)
  eka agents-md --check    # the same, explicit
  eka agents-md --write    # regenerate the managed region
  eka agents-md --check --json
  eka agents-md --nested --write           # project the index per directory
  eka agents-md --nested --host claude --write
  eka agents-md --nested --check --json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAgentsMD(cmd, write, check, asJSON, nested, hosts)
		},
	}
	cmd.Flags().BoolVar(&write, "write", false, "regenerate and write the managed region (a current file is left untouched)")
	cmd.Flags().BoolVar(&check, "check", false, "compare the on-disk managed region with the generated block (the read-only default)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit a single-line machine-readable report (combinable with --check/--write)")
	cmd.Flags().BoolVar(&nested, "nested", false, "project the binding index per directory (nested AGENTS.md entry points)")
	cmd.Flags().StringArrayVar(&hosts, "host", nil, "emit per-host projection shims for the declared target (claude|cursor|opencode); requires --nested (repeatable)")
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
// and either checks or writes it. With nested=true it instead projects the
// index per directory and (optionally) per host (see runAgentsMDNested).
func runAgentsMD(cmd *cobra.Command, write, check, asJSON, nested bool, hosts []string) error {
	if write && check {
		return fmt.Errorf("agents-md: --write and --check are mutually exclusive")
	}
	if len(hosts) > 0 && !nested {
		return fmt.Errorf("agents-md: --host requires --nested")
	}
	if nested {
		return runAgentsMDNested(cmd, write, check, asJSON, hosts)
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

// nestedFileReport is one expected nested AGENTS.md (or shim) entry of the
// --nested --json document: path, status and bound-constraint count. Status
// is one of create|overwrite (write mode), drift|stale (check mode) or
// current. `stale` marks a projected file whose bindings are now empty.
// Field order is fixed (deterministic bytes).
type nestedFileReport struct {
	File        string `json:"file"`
	Status      string `json:"status"`
	Constraints int    `json:"constraints"`
}

// agentsMDHostReport is the per-host shim outcome: the declared host, its
// status (written|current|drift|skipped|noop) and the emitted shim paths.
type agentsMDHostReport struct {
	Host   string   `json:"host"`
	Status string   `json:"status"`
	Files  []string `json:"files,omitempty"`
}

// agentsMDNestedReport is the `eka agents-md --nested --json` document.
// Field order is fixed and no field carries a timestamp: nested, hosts.
// nested is sorted by file, hosts by host name — deterministic bytes.
type agentsMDNestedReport struct {
	Nested []nestedFileReport   `json:"nested"`
	Hosts  []agentsMDHostReport `json:"hosts"`
}

// staleFile is one generated nested AGENTS.md on disk whose bindings are
// now empty (it must be removed on --write and is drift on --check).
type staleFile struct {
	rel string
	abs string
}

// runAgentsMDNested implements the `--nested` projection: it reads the
// committed snapshot and the tracked set, derives the per-directory bound
// index, and either writes the expected nested files + shims or checks them
// for drift. The rules are documented in bootstrap/nested.go (nested files)
// and bootstrap/shim.go (host shims).
func runAgentsMDNested(cmd *cobra.Command, write, check, asJSON bool, hosts []string) error {
	for _, h := range hosts {
		if !bootstrap.IsKnownHost(h) {
			return fmt.Errorf("agents-md: unknown --host %q (want claude, cursor or opencode)", h)
		}
	}
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("agents-md: %w", err)
	}
	root, err := snapshotRepositoryRoot(cwd)
	if err != nil {
		return fmt.Errorf("agents-md: %w", err) // Exit 2: no eka.yaml.
	}
	rawMeta, err := os.ReadFile(filepath.Join(root, "eka.yaml"))
	if err != nil {
		return fmt.Errorf("agents-md: cannot read eka.yaml: %w", err)
	}
	meta, err := metadata.Parse(rawMeta)
	if err != nil {
		return fmt.Errorf("agents-md: invalid eka.yaml: %w", err)
	}
	tracked, err := gitTrackedFiles(root)
	if err != nil {
		return fmt.Errorf("agents-md: %w", err) // Exit 2: no tracked set.
	}
	entries, missingSnapshot, err := bootstrap.NestedEntriesFromSnapshot(
		filepath.Join(root, filepath.FromSlash(bootstrap.SnapshotDir)), meta.Project, meta.Namespace, tracked)
	if err != nil {
		return fmt.Errorf("agents-md: %w", err) // Exit 2: unreadable/corrupt snapshot.
	}
	if missingSnapshot {
		fmt.Fprintln(cmd.ErrOrStderr(), "eka: agents-md: no committed snapshot (exchange/snapshots) — no nested projection")
	}

	report := agentsMDNestedReport{Nested: []nestedFileReport{}, Hosts: []agentsMDHostReport{}}
	drift := false
	expected := make(map[string]bool, len(entries))
	for _, e := range entries {
		expected[e.Dir] = true
		rel := path.Join(e.Dir, bootstrap.AgentsMDName)
		abs := filepath.Join(root, filepath.FromSlash(rel))
		existing, rerr := os.ReadFile(abs)
		missing := false
		if rerr != nil {
			if !os.IsNotExist(rerr) {
				return fmt.Errorf("agents-md: cannot read %s: %w", rel, rerr)
			}
			missing = true
		}
		merged := bootstrap.MergeAgentsMD(existing, e.Block)
		entry := nestedFileReport{File: rel, Constraints: bootstrap.IndexEntryCount(e.Index)}
		switch {
		case missing && write:
			entry.Status = "create"
		case missing:
			entry.Status = "drift"
			drift = true
		case merged == nil:
			entry.Status = "current"
		case write:
			entry.Status = "overwrite"
		default:
			entry.Status = "drift"
			drift = true
		}
		if write && merged != nil {
			if err := writeNestedFile(abs, merged); err != nil {
				return err
			}
		}
		report.Nested = append(report.Nested, entry)
	}

	stale, err := staleNestedFiles(root, tracked, expected)
	if err != nil {
		return fmt.Errorf("agents-md: %w", err)
	}
	for _, s := range stale {
		entry := nestedFileReport{File: s.rel, Status: "stale"}
		if write {
			if err := os.Remove(s.abs); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("agents-md: cannot remove stale %s: %w", s.rel, err)
			}
		} else {
			drift = true
		}
		report.Nested = append(report.Nested, entry)
	}
	sort.Slice(report.Nested, func(i, j int) bool { return report.Nested[i].File < report.Nested[j].File })

	if len(hosts) > 0 {
		hostDrift, herr := runAgentsMDHosts(cmd, root, meta.Namespace, entries, uniqueSortedHosts(hosts), write, &report)
		if herr != nil {
			return herr
		}
		drift = drift || hostDrift
	}

	if asJSON {
		if err := writeAgentsMDNestedJSON(cmd, report); err != nil {
			return err
		}
	} else {
		renderAgentsMDNested(cmd, report)
	}
	if drift && !write {
		if !asJSON {
			fmt.Fprintln(cmd.ErrOrStderr(), "eka: agents-md: nested projection drifted; run 'eka agents-md --nested --write' to regenerate")
		}
		return &exitError{code: exitFail}
	}
	return nil
}

// runAgentsMDHosts emits (or drift-checks) the per-host shims of each
// declared host, appending one report row per host. A host whose config
// directory is absent is SKIPPED (never approximated); opencode is an
// acknowledged no-op. It returns whether any shim drifted (check mode).
func runAgentsMDHosts(cmd *cobra.Command, root, namespace string, entries []bootstrap.NestedEntry, hosts []string, write bool, report *agentsMDNestedReport) (bool, error) {
	drift := false
	for _, host := range hosts {
		row := agentsMDHostReport{Host: host}
		if host == bootstrap.HostOpencode {
			row.Status = "noop"
			report.Hosts = append(report.Hosts, row)
			continue
		}
		cfg := bootstrap.HostConfigDir(host)
		if !dirExists(filepath.Join(root, cfg)) {
			row.Status = "skipped"
			report.Hosts = append(report.Hosts, row)
			continue
		}
		shims, err := bootstrap.BuildHostShims(host, namespace, entries)
		if err != nil {
			return false, fmt.Errorf("agents-md: %w", err)
		}
		changed := false
		for _, s := range shims {
			abs := filepath.Join(root, filepath.FromSlash(s.Path))
			existing, rerr := os.ReadFile(abs)
			missing := false
			if rerr != nil {
				if !os.IsNotExist(rerr) {
					return false, fmt.Errorf("agents-md: cannot read %s: %w", s.Path, rerr)
				}
				missing = true
			}
			changed = changed || missing || !bytes.Equal(existing, s.Content)
			if write && (missing || !bytes.Equal(existing, s.Content)) {
				if err := writeNestedFile(abs, s.Content); err != nil {
					return false, err
				}
			}
			row.Files = append(row.Files, s.Path)
		}
		sort.Strings(row.Files)
		switch {
		case changed && write:
			row.Status = "written"
		case changed:
			row.Status = "drift"
			drift = true
		default:
			row.Status = "current"
		}
		report.Hosts = append(report.Hosts, row)
	}
	return drift, nil
}

// staleNestedFiles returns the tracked nested AGENTS.md files whose managed
// region marks them as generated and whose directory is not in the expected
// set (its bindings are now empty). Hand-written AGENTS.md (no managed
// region) is never reported — it is not ours to remove. Only the tracked set
// is scanned, which is exactly the committed projection.
func staleNestedFiles(root string, tracked []string, expected map[string]bool) ([]staleFile, error) {
	var out []staleFile
	seen := map[string]bool{}
	for _, f := range tracked {
		if path.Base(f) != bootstrap.AgentsMDName {
			continue
		}
		dir := path.Dir(f)
		if dir == "." || dir == "/" || expected[dir] || seen[dir] {
			continue
		}
		abs := filepath.Join(root, filepath.FromSlash(f))
		raw, err := os.ReadFile(abs)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		if !bootstrap.HasManagedRegion(raw) {
			continue // Hand-written: not a generated projection.
		}
		seen[dir] = true
		out = append(out, staleFile{rel: f, abs: abs})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].rel < out[j].rel })
	return out, nil
}

// writeNestedFile writes one generated file, creating its directory.
func writeNestedFile(abs string, content []byte) error {
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return fmt.Errorf("agents-md: cannot create directory for %s: %w", filepath.Base(abs), err)
	}
	if err := os.WriteFile(abs, content, 0o644); err != nil {
		return fmt.Errorf("agents-md: cannot write %s: %w", filepath.Base(abs), err)
	}
	return nil
}

// dirExists reports whether p is an existing directory.
func dirExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}

// uniqueSortedHosts dedupes and sorts the declared hosts for a deterministic
// report (the flag is repeatable; order must not leak into the bytes).
func uniqueSortedHosts(hosts []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(hosts))
	for _, h := range hosts {
		if seen[h] {
			continue
		}
		seen[h] = true
		out = append(out, h)
	}
	sort.Strings(out)
	return out
}

// renderAgentsMDNested prints the human report: one line per nested file and
// one per declared host. skipped/noop hosts are explained on stderr/stdout.
func renderAgentsMDNested(cmd *cobra.Command, report agentsMDNestedReport) {
	for _, n := range report.Nested {
		fmt.Fprintf(cmd.OutOrStdout(), "%s: %s (%d constraints)\n", n.File, n.Status, n.Constraints)
	}
	for _, h := range report.Hosts {
		switch h.Status {
		case "noop":
			fmt.Fprintf(cmd.OutOrStdout(), "--host %s: no-op (reads nested AGENTS.md natively)\n", h.Host)
		case "skipped":
			fmt.Fprintf(cmd.ErrOrStderr(), "eka: agents-md: --host %s skipped (no %s/ config directory)\n", h.Host, bootstrap.HostConfigDir(h.Host))
		default:
			fmt.Fprintf(cmd.OutOrStdout(), "--host %s: %s (%d shim(s))\n", h.Host, h.Status, len(h.Files))
		}
	}
}

// writeAgentsMDNestedJSON emits the nested report as a single JSON line
// (fixed field order, one trailing newline).
func writeAgentsMDNestedJSON(cmd *cobra.Command, report agentsMDNestedReport) error {
	out, err := json.Marshal(report)
	if err != nil {
		return fmt.Errorf("agents-md: %w", err)
	}
	out = append(out, '\n')
	_, err = cmd.OutOrStdout().Write(out)
	return err
}
