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
	cmd.AddCommand(newSyncPullCommand(), newSyncPushCommand(), newSyncAdoptCommand())
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

--adopt (ADR-032 Option C2): before pushing, the workspace-native
units of the repository's project (published via ` + "`eka publish`" + `,
provenance source_repo = "runtime") are re-attributed to the
repository provenance, so this push carries them into the snapshot and
a clone on another device receives them. The immutable payloads are
never touched — adopt is a reference-only re-attribution.

Exit codes:
  0  push succeeded (or no-op)
  2  usage or internal error`,
		Example: `  eka sync push
  eka sync push /path/to/repo
  eka sync push --adopt`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			override, err := cmd.Flags().GetBool("override")
			if err != nil {
				return fmt.Errorf("sync push failed: %w", err)
			}
			adopt, err := cmd.Flags().GetBool("adopt")
			if err != nil {
				return fmt.Errorf("sync push failed: %w", err)
			}
			return runSync(cmd, args, runtime.SyncOptions{Push: true, Override: override, AdoptBeforePush: adopt})
		},
	}
	cmd.Flags().Bool("override", false,
		"align the repository identity to the content namespace when they differ (machine override)")
	cmd.Flags().Bool("adopt", false,
		"adopt workspace-native units into the repository before pushing (ADR-032)")
	return cmd
}

// newSyncAdoptCommand builds `eka sync adopt [path] [target ...]`.
func newSyncAdoptCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "adopt [path] [target ...]",
		Short: "Adopt workspace-native units into the repository",
		Long: `Re-attribute the workspace-native units of the EKA repository at path
to the repository provenance (ADR-032 Option C2).

Units published via ` + "`eka publish`" + ` live in the workspace
canonical store under the workspace-native provenance sentinel
(source_repo = "runtime") and never enter a repository snapshot — a
clone on another device only receives the snapshot's units. Adopt
moves the REFERENCE of such units to the repository provenance, so the
next push assembles them into the snapshot. The immutable payloads are
never touched: adopt is a reference-only re-attribution, with no
validation or note gates.

Without targets every workspace-native unit of the repository's
project AND namespace is adopted; a unit whose namespace differs from
the repository namespace is left in place and reported as ignored (a
repository is one platform). With targets only the matching units are
adopted: each target is a reference of the form <namespace>/<type>:<id>
or <type>:<id> (optional :<instance-version> suffix); the namespace
must equal the repository namespace. A target matching no
workspace-native unit is refused.

--dry-run computes the identical result (adopted count, skipped and
ignored units) without changing the store — the repository is not
registered by a dry run either.

Exit codes:
  0  adopt succeeded (or no workspace-native units)
  2  usage or internal error (invalid target, namespace mismatch, no
     matching workspace-native unit, or the path is not an EKA
     repository)`,
		Example: `  eka sync adopt
  eka sync adopt /path/to/repo
  eka sync adopt . eka-sync-fixture/sto:runtime-only
  eka sync adopt --dry-run`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			dryRun, err := cmd.Flags().GetBool("dry-run")
			if err != nil {
				return fmt.Errorf("sync adopt failed: %w", err)
			}
			path := "."
			var targets []string
			if len(args) > 0 {
				path = args[0]
				targets = args[1:]
			}
			return runSyncAdopt(cmd, path, targets, dryRun)
		},
	}
	cmd.Flags().Bool("dry-run", false,
		"count and list the adoptable units without changing the store")
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

// runSyncAdopt resolves the workspace and repository path, runs the
// adopt engine through the Authoring API and renders the report.
// Errors map to the exit code contract: adopt has no validation or
// integrity failure class, so every refusal (invalid target, namespace
// mismatch, no matching workspace-native unit) and internal failure is
// exit 2.
func runSyncAdopt(cmd *cobra.Command, path string, targets []string, dryRun bool) error {
	r, err := runtime.Ensure()
	if err != nil {
		return err // Exit 2: workspace resolution.
	}
	defer r.Close()

	s := styleFor(cmd)
	spinner := ui.NewSpinner(s, "Adopting workspace-native units...")
	result, err := runtime.Authoring.SyncAdopt(r, path, targets, dryRun)
	spinner.Stop()
	if err != nil {
		return err // Exit 2: usage/internal.
	}
	renderAdoptReport(s, r, path, result)
	return nil
}

// renderAdoptReport renders the adopt outcome: the Runtime context
// header and the closing summary. The repository identity is resolved
// through the workspace registry (the adopt result itself carries only
// the counts).
func renderAdoptReport(s *ui.Style, r *runtime.Runtime, path string, res *runtime.SyncAdoptResult) {
	repoName, projectID := "", ""
	if repo, found, err := r.Workspace.FindRepo(path); err == nil && found {
		repoName, projectID = repo.Name, repo.ProjectID
	}

	ui.NewHeader(s, "Adopt").
		Add("Workspace", r.Path()).
		Add("Project", projectID).
		Add("Repository", repoName).
		Add("Pipeline", "Adopt").
		Render()

	ui.NewSummary(s).
		Add("Repository", repoName).
		Add("Project", projectID).
		Add("Status", adoptStatus(res)).
		Add("Adopted", plural(res.Units, "unit", "units")).
		Render()

	if len(res.Skipped) > 0 {
		for _, form := range res.Skipped {
			fmt.Fprintf(s.W, "  %s %s\n", ui.IconBullet, s.Warning("skipped: "+form+" (the repository already references a different payload)"))
		}
	}
	if len(res.Ignored) > 0 {
		for _, form := range res.Ignored {
			fmt.Fprintf(s.W, "  %s %s\n", ui.IconBullet, s.Info("ignored: "+form+" (namespace differs from the repository namespace)"))
		}
	}
}

// adoptStatus classifies the adopt outcome deterministically.
func adoptStatus(res *runtime.SyncAdoptResult) string {
	switch {
	case res.DryRun:
		return "dry run (no changes)"
	case res.Units == 0 && len(res.Skipped) == 0 && len(res.Ignored) == 0:
		return "no workspace-native units"
	case res.Units == 0:
		return "no adoptable units"
	default:
		return "adopted"
	}
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

	if r.AdoptedUnits > 0 || len(r.AdoptedSkipped) > 0 {
		adopted := plural(r.AdoptedUnits, "unit", "units")
		if len(r.AdoptedSkipped) > 0 {
			adopted += fmt.Sprintf(", %d skipped", len(r.AdoptedSkipped))
		}
		fmt.Fprintf(s.W, "  %s %s\n", ui.IconBullet, s.Info("adopted before push: "+adopted))
	}

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
