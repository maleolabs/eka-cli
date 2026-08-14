package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/maleolabs/eka-core/metadata"
	"github.com/maleolabs/eka-core/sync"
	"github.com/spf13/cobra"
)

// newSnapshotCommand builds the `eka snapshot` command tree: the
// snapshot tooling of the Knowledge Runtime. The snapshot directory
// <repo>/exchange/snapshots carries two kinds of entries:
//
//   - source entries: units/ (unit.json metadata + content payloads)
//     and attachments/ — the knowledge itself, byte-exact, merged by
//     Git normally (every unit is its own file);
//   - derived aggregates: manifest.json, declarations.json and
//     integrity.json — deterministic whole-package projections,
//     regenerated from the source entries on every push.
func newSnapshotCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "snapshot",
		Short: "Inspect and repair repository snapshots",
		Long: `Snapshot tooling of the EKA Knowledge Runtime.

The snapshot is the transport between a repository and the workspace
canonical store: the directory <repo>/exchange/snapshots. It has two
kinds of entries:

  source entries    header.json (the package contract), units/
                    (unit.json metadata + content payloads) and
                    attachments/ — the knowledge itself, byte-exact,
                    merged by Git normally; these are committed
  derived entries   manifest.json, declarations.json, integrity.json —
                    whole-package projections of the source entries,
                    regenerated deterministically on every push and
                    NOT committed (ADR-027 — committing them would
                    turn every parallel merge into a synthetic
                    conflict)

Since the derived entries are not committed, merging two pushed
snapshots is a normal Git merge — no repair step is needed. The
repair subcommand exists for LEGACY snapshots (EKA < 0.10) that still
carry the aggregates.

Subcommands:
  repair  regenerate the derived entries of a legacy snapshot`,
	}
	cmd.AddCommand(newSnapshotRepairCommand())
	return cmd
}

// newSnapshotRepairCommand builds `eka snapshot repair` — the legacy
// snapshot aggregate repair command: it regenerates the derived
// aggregates (manifest.json, declarations.json, integrity.json) and
// normalizes the unit.json entries of the current repository's
// snapshot in place, from its units/ and attachments/ trees
// (sync.RepairSnapshot — the same serializer a push uses, so identical
// input produces byte-identical aggregates).
//
// When to run it: after a Git merge combined two LEGACY snapshots
// (EKA < 0.10) that still commit the aggregates. The aggregates change
// on both sides of any parallel knowledge change and are assigned a
// keep-ours merge driver in .gitattributes (merge=ekasnap, registered
// once per machine with 'git config --global merge.ekasnap.driver
// true') — a line-wise merge of the two versions is almost always
// digest-inconsistent (the next `eka sync` pull refuses it byte-exact,
// exit 1), and a custom regenerating driver cannot work either: Git's
// ort strategy invokes merge drivers before the merged source entries
// are written to the index or the working tree. The merge therefore
// takes the pre-merge aggregate bytes without conflict, the merged
// units/ tree is the union of both sides, and this command rebuilds
// the aggregates from that union — then stage and commit them, and
// `eka sync` verifies and seeds. On a source-only snapshot (the
// current layout) the command is a no-op normalizer: nothing to
// repair.
//
// Repair never touches content payloads, attachments or header.json
// (header.json carries the package identity facts — a change there is
// an identity change, refused by sync) and refuses to write anything
// while a unit is undecodable (an unresolved unit-level conflict must
// be resolved by hand first).
//
// Exit codes:
//
//	0  regenerated (or nothing to repair)
//	1  not resolvable (no eka.yaml, corrupt units — nothing written)
//	2  usage or internal error
func newSnapshotRepairCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "repair",
		Short: "Regenerate the derived aggregates of a legacy snapshot",
		Long: `Regenerate the derived aggregates of the current repository's
snapshot in place: manifest.json, declarations.json and
integrity.json, plus the canonical re-serialization of unit.json
entries, from the merged units/ and attachments/ trees — the same
serializer a push uses, so a regenerated snapshot is digest-consistent
and the next ` + "`eka sync`" + ` verifies and seeds it normally.

Run it after a Git merge combined two LEGACY snapshots (EKA < 0.10)
that still commit the aggregates (the aggregates are assigned a
keep-ours merge driver in .gitattributes (merge=ekasnap, registered
once per machine with 'git config --global merge.ekasnap.driver
true') — a line-wise merge of the two aggregate versions is almost
always digest-inconsistent), then:

  git merge feature-branch     # units/ merged normally; aggregates ours
  eka snapshot repair          # rebuild the aggregates from the union
  git add exchange/snapshots
  git commit -m "snapshot: regenerate aggregates"
  eka sync                     # verify byte-exact, seed, re-push

Snapshots written since ADR-027 are source-only (the aggregates are
gitignored and regenerated on every push) — merging them is a normal
Git merge and this command is a no-op normalizer.

The repair refuses to write anything while a unit is undecodable —
resolve unit-level conflicts (the same unit changed on both sides)
by hand first. Content payloads, attachments and header.json are
never touched.

Examples:
  eka snapshot repair`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("eka snapshot repair failed: %w", err) // Exit 2.
			}
			root, err := snapshotRepositoryRoot(cwd)
			if err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "eka snapshot repair: %v\n", err)
				return &exitError{code: exitFail} // 1: not resolvable.
			}
			snapshotDir := filepath.Join(root, "exchange", "snapshots")
			if err := sync.RepairSnapshot(snapshotDir); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(),
					"eka snapshot repair: cannot regenerate the snapshot: %v (resolve unit-level conflicts first, then re-run)\n", err)
				return &exitError{code: exitFail} // 1: not resolvable.
			}
			fmt.Fprintln(cmd.OutOrStdout(),
				"snapshot repaired: aggregates regenerated (stage and commit exchange/snapshots, then run 'eka sync' to verify and seed)")
			return nil
		},
	}
	return cmd
}

// snapshotRepositoryRoot locates the repository root of cwd: the
// nearest directory carrying eka.yaml (the ADR-018 walk-up, shared with
// the sync engine). A directory without eka.yaml is not an EKA
// repository — its snapshot cannot be repaired.
func snapshotRepositoryRoot(cwd string) (string, error) {
	_, mdir, hasMeta, err := metadata.Find(cwd)
	if err != nil {
		return "", err
	}
	if !hasMeta {
		return "", fmt.Errorf("%s is not an EKA repository (no eka.yaml); cannot regenerate the snapshot", cwd)
	}
	return mdir, nil
}
