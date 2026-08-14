package cmd

import (
	"errors"
	"fmt"

	"github.com/maleolabs/eka-cli/cmd/ui"
	"github.com/maleolabs/eka-core/exchange"
	"github.com/maleolabs/eka-core/runtime"
	"github.com/spf13/cobra"
)

// newSyncCommand builds the `eka sync` command tree: the Knowledge
// Runtime synchronization between a registered repository and the EKA
// workspace canonical store. `eka sync [path]` runs the full cycle
// (pull + push); `eka sync pull` and `eka sync push` run one side.
//
// Model (help contract): the workspace (~/.eka or $EKA_HOME) is the
// canonical store; the transport is the snapshot directory
// <repo>/exchange/snapshots, an RSF package verified byte-exact on
// every read and written atomically. Pulls are idempotent (unchanged
// snapshot digests skip the work); deletions are never applied.
//
// Exit codes:
//
//	0  sync succeeded (newly registered, pulled, pushed, or unchanged)
//	1  repository failed the docs validation gate, or the snapshot
//	   package is corrupt/refused (integrity failure)
//	2  usage or internal error (workspace resolution, registry,
//	   filesystem failure, or the path is not an EKA repository — no
//	   eka.yaml; run 'eka init' first)
func newSyncCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sync [path]",
		Short: "Sync a repository with the EKA workspace",
		Long: `Synchronize the EKA repository at path (default: the current
directory) with the EKA workspace canonical store.

The workspace (default ~/.eka, or $EKA_HOME) is the canonical storage
of the Knowledge Runtime: objects, relationships, change logs and
attachments live in the workspace database. The transport between a
repository and the workspace is the snapshot directory
<repo>/exchange/snapshots — an RSF package in directory layout.

` + "`eka sync`" + ` runs the full cycle: pull (verify the snapshot and
seed the canonical store, or seed from the docs tree when no snapshot
exists yet) then push (assemble the repository's canonical objects
into a fresh snapshot). Pulls are idempotent: an unchanged snapshot
digest skips the work. Deletions are never applied.

The repository must be an EKA repository: a directory tree carrying
eka.yaml (run 'eka init' to create one — there is no legacy mode, so
a directory without eka.yaml is refused). The repository is
registered automatically on first sync with the identity from
eka.yaml — project, name, namespace. Works well with git: the
snapshot directory is a deterministic transport that can be committed
or ignored.

The committed snapshot is source-only (ADR-027): header.json (the
package contract), units/ and attachments/ — every unit is its own
file, merged by Git normally. The derived aggregates (manifest.json,
declarations.json, integrity.json) are whole-package projections that
would turn every parallel merge into a synthetic conflict; they are
regenerated on every push and gitignored. Merging two pushed
snapshots is therefore a normal Git merge; only a unit changed on
both sides needs a hand resolution. Legacy snapshots carrying the
aggregates (EKA < 0.10) verify byte-exact as before.

Exit codes:
  0  sync succeeded (pulled, pushed, or unchanged)
  1  repository validation failed, or the snapshot is corrupt
  2  usage or internal error, or the path is not an EKA repository
     (no eka.yaml; run 'eka init' first)`,
		Example: `  eka sync            sync the current repository (pull + push)
  eka sync /path/to/repo
  eka sync pull       pull only
  eka sync pull --from-docs
  eka sync push       push only
  eka sync --override align the repository identity to the content namespace`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			override, err := cmd.Flags().GetBool("override")
			if err != nil {
				return fmt.Errorf("sync failed: %w", err)
			}
			return runSync(cmd, args, runtime.SyncOptions{Pull: true, Push: true, Override: override})
		},
	}
	cmd.Flags().Bool("override", false,
		"align the repository identity to the content namespace when they differ (machine override)")
	cmd.AddCommand(newSyncPullCommand(), newSyncPushCommand())
	return cmd
}

// newSyncPullCommand builds `eka sync pull [path] [--from-docs]`.
func newSyncPullCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pull [path]",
		Short: "Pull repository knowledge into the workspace",
		Long: `Pull the knowledge of the EKA repository at path into the EKA
workspace canonical store.

Snapshot mode (default): the snapshot directory
<repo>/exchange/snapshots is verified byte-exact (structure, strict
JSON, SHA-256 integrity, self-consistency) and its units and
attachments are upserted into the canonical store. An unchanged
snapshot digest is reported as "unchanged" and skips the work; a
corrupt snapshot is an error.

Docs mode (--from-docs, or when no snapshot exists): the repository's
docs tree is validated against the conformance rules (blocking
violations refuse the pull) and seeded exactly as ` + "`eka export`" + `
would assemble it — the migration path for repositories without a
snapshot. Docs-mode pulls always re-seed (no digest skip: the docs
tree carries no package digest).

Deletions are never applied: units missing from a new pull stay in
the canonical store.

Exit codes:
  0  pull succeeded (seeded or unchanged)
  1  repository validation failed, or the snapshot is corrupt
  2  usage or internal error`,
		Example: `  eka sync pull
  eka sync pull --from-docs`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			fromDocs, err := cmd.Flags().GetBool("from-docs")
			if err != nil {
				return fmt.Errorf("sync pull failed: %w", err)
			}
			override, err := cmd.Flags().GetBool("override")
			if err != nil {
				return fmt.Errorf("sync pull failed: %w", err)
			}
			return runSync(cmd, args, runtime.SyncOptions{Pull: true, FromDocs: fromDocs, Override: override})
		},
	}
	cmd.Flags().Bool("from-docs", false,
		"seed the canonical store from the repository's docs tree instead of the snapshot directory")
	cmd.Flags().Bool("override", false,
		"align the repository identity to the content namespace when they differ (machine override)")
	return cmd
}

// newSyncPushCommand builds `eka sync push [path]`.
func newSyncPushCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "push [path]",
		Short: "Push workspace knowledge into the repository snapshot",
		Long: `Push the canonical knowledge of the EKA repository at path into its
snapshot directory <repo>/exchange/snapshots.

The repository's objects in the workspace canonical store are
assembled into an RSF package (namespace: the existing snapshot's
namespace, else the most common namespace among the objects) and
written atomically: the entries are staged in
<repo>/exchange/.snapshots-tmp and swapped into place, so a failed
push leaves the previous snapshot untouched. A repository with no
stored objects is a no-op.

Exit codes:
  0  push succeeded (or no-op)
  2  usage or internal error`,
		Example: `  eka sync push
  eka sync push /path/to/repo`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			override, err := cmd.Flags().GetBool("override")
			if err != nil {
				return fmt.Errorf("sync push failed: %w", err)
			}
			return runSync(cmd, args, runtime.SyncOptions{Push: true, Override: override})
		},
	}
	cmd.Flags().Bool("override", false,
		"align the repository identity to the content namespace when they differ (machine override)")
	return cmd
}

// runSync resolves the workspace and repository path, runs the sync
// engine through the Authoring API and renders the report, mapping
// errors to the exit code contract.
//
// Namespace reconciliation (ADR-020): the --override flag is the
// machine path to align the repository identity to the content
// namespace; without it, an interactive TTY (real stdin terminal) gets
// the arrow-selected confirmation (cmd/ui Select) and any non-TTY run
// is refused deterministically (exit 2).
func runSync(cmd *cobra.Command, args []string, opts runtime.SyncOptions) error {
	path := "."
	if len(args) == 1 {
		path = args[0]
	}

	r, err := runtime.Ensure()
	if err != nil {
		return err // Exit 2: workspace resolution.
	}
	defer r.Close()

	s := styleFor(cmd)
	spinner := ui.NewSpinner(s, "Synchronizing Engineering Knowledge...")
	// The interactive confirmation is wired only when the flag is NOT
	// set, stdin is a real terminal AND stdout is a terminal too: a
	// run whose output is captured (pipes, CI, IDE run panels) must
	// never block on a prompt the user cannot see — it refuses
	// deterministically with the override hint instead. The spinner is
	// stopped before the prompt so the Select renders cleanly (the
	// animation would otherwise overwrite the option list).
	if !opts.Override && isTTYReader(cmd.InOrStdin()) && s.TTY {
		stdin, stdout := cmd.InOrStdin(), cmd.OutOrStdout()
		opts.Confirm = func(prompt string, options []string, defaultIdx int) (int, error) {
			// Halt the spinner before the prompt so the menu renders
			// cleanly (no animation overwriting it); when the user
			// chooses the ALIGN option (index 0), resume the spinner
			// so the remaining — potentially slow — alignment + pull
			// work shows progress before the final report renders.
			// One blank line separates the resumed spinner from the
			// menu frame (it must not stick to the usage hint).
			spinner.Halt()
			items := make([]ui.MenuItem, len(options))
			for i, o := range options {
				items[i] = ui.MenuItem{Title: o, Value: o}
			}
			value, err := ui.Select(s, stdin, stdout, prompt, items, defaultIdx)
			if err != nil {
				if errors.Is(err, ui.ErrCancelled) {
					// Cancelled: the abort slot (deterministic
					// refusal, exit 2).
					return len(options) - 1, nil
				}
				return defaultIdx, err
			}
			idx := 0
			if value != options[0] {
				idx = len(options) - 1
			}
			if idx == 0 {
				fmt.Fprintln(stdout)
				spinner.Start()
			}
			return idx, nil
		}
	}
	report, err := runtime.Authoring.Sync(r, path, opts)
	spinner.Stop()
	if err != nil {
		var ve *runtime.ValidationError
		if errors.As(err, &ve) {
			printReport(s, ve.Report)
			fmt.Fprintf(cmd.ErrOrStderr(), "eka: %s\n", ve.Error())
			return &exitError{code: exitFail}
		}
		var pe *exchange.PackageError
		if errors.As(err, &pe) {
			fmt.Fprintf(cmd.ErrOrStderr(), "eka: %s\n", err)
			return &exitError{code: exitFail}
		}
		return err // Exit 2: usage/internal.
	}
	renderSyncReport(s, report)
	return nil
}

// renderSyncReport renders the sync outcome: the Runtime context
// header and the closing summary.
func renderSyncReport(s *ui.Style, r *runtime.SyncResult) {
	ui.NewHeader(s, "Runtime").
		Add("Workspace", r.Workspace).
		Add("Project", r.Project).
		Add("Repository", r.Repo).
		Add("Pipeline", "Sync").
		Render()

	ui.NewSummary(s).
		Add("Repository", r.Repo).
		Add("Project", r.Project).
		Add("Status", repoStatus(r)).
		Add("Pull", pullDetail(r)).
		Add("Push", pushDetail(r)).
		Add("Snapshot", snapshotDetail(r)).
		Render()

	if len(r.Warnings) > 0 {
		for _, w := range r.Warnings {
			fmt.Fprintf(s.W, "  %s %s\n", ui.IconBullet, s.Info(w))
		}
	}
}

// repoStatus classifies the run outcome deterministically. The
// "unchanged" claim covers both sides: an idempotent pull AND a push
// that left the snapshot digest untouched. A push that rewrote the
// snapshot (store and snapshot were out of sync) is reported as a
// change, never hidden behind "unchanged".
func repoStatus(r *runtime.SyncResult) string {
	switch {
	case r.NewRepo:
		return "registered (new)"
	case r.Unchanged && !r.PushChanged:
		return "unchanged"
	case r.Unchanged && r.PushChanged:
		return "synced (snapshot updated)"
	default:
		return "synced"
	}
}

// pullDetail renders the pull side of the report.
func pullDetail(r *runtime.SyncResult) string {
	switch {
	case r.PullSource == "":
		return "not run"
	case r.Unchanged:
		return "unchanged (snapshot up to date)"
	default:
		return fmt.Sprintf("%s: %s, %s",
			r.PullSource,
			plural(r.PulledUnits, "unit", "units"),
			plural(r.PulledAttachments, "attachment", "attachments"))
	}
}

// pushDetail renders the push side of the report.
func pushDetail(r *runtime.SyncResult) string {
	if r.SnapshotLabel == "" {
		return "no-op (no stored objects)"
	}
	return fmt.Sprintf("%s, %s",
		plural(r.PushedUnits, "unit", "units"),
		plural(r.PushedAttachments, "attachment", "attachments"))
}

// snapshotDetail renders the snapshot label/digest line.
func snapshotDetail(r *runtime.SyncResult) string {
	if r.SnapshotLabel == "" && r.SnapshotDigest == "" {
		return "none"
	}
	if r.SnapshotLabel == "" {
		return r.SnapshotDigest
	}
	if r.SnapshotDigest == "" {
		return r.SnapshotLabel
	}
	return fmt.Sprintf("%s (%s)", r.SnapshotLabel, shortDigest(r.SnapshotDigest))
}

// shortDigest abbreviates a SHA-256 digest to 12 hex characters.
func shortDigest(digest string) string {
	if len(digest) <= 12 {
		return digest
	}
	return digest[:12]
}
