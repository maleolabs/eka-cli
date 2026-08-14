package cmd

import (
	"fmt"
	"strconv"

	"github.com/maleolabs/eka-cli/cmd/ui"
	"github.com/maleolabs/eka-core/runtime"
	"github.com/spf13/cobra"
)

// newStatusCommand builds the `eka status` command: a deterministic
// overview of the EKA workspace — path, schema version, registered
// projects/repositories, canonical store counts and the last sync per
// repository. An uninitialized workspace (no workspace.json) is
// reported informatively and exits 0: status is a read-only probe and
// never creates the workspace.
func newStatusCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show the EKA workspace status",
		Long: `Show the EKA workspace status: the workspace path, the schema
version, the registered projects and repositories, the canonical
store totals (objects, immutable payloads, attachments) and the last
pull/push per repository (from the sync log).

When no workspace exists yet an informational message is printed and
the command exits 0 — status never initializes the workspace.

Exit codes:
  0  status shown (or no workspace yet)
  2  usage or internal error`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			home, err := runtime.HomeDir()
			if err != nil {
				return err
			}
			s := styleFor(cmd)
			// The read-only entry: Open never initializes the
			// workspace — a missing workspace.json is reported through
			// Exists, not created.
			r, err := runtime.Open()
			if err != nil {
				return err
			}
			defer r.Close()
			if !r.Exists() {
				fmt.Fprintf(s.W, "%s\n", s.Accent("Runtime"))
				fmt.Fprintf(s.W, "  %s\n", s.Info("No EKA workspace at "+home+" yet. Run 'eka project register' to create it."))
				return nil
			}
			st, err := r.Workspace.Status()
			if err != nil {
				return fmt.Errorf("status failed: %w", err)
			}
			return renderStatus(s, st)
		},
	}
}

// renderStatus renders the workspace overview deterministically. Any
// store failure is propagated: status is a monitoring command and must
// never report a false healthy state (exit code 2 on internal error).
func renderStatus(s *ui.Style, st *runtime.WorkspaceStatus) error {
	ui.NewHeader(s, "Runtime").
		Add("Workspace", st.Path).
		Add("Schema", "v"+strconv.Itoa(st.SchemaVersion)).
		Add("ID", st.ID).
		Add("Created", st.Created).
		Render()

	ui.NewSummary(s).
		Add("Projects", strconv.Itoa(len(st.Projects))).
		Add("Objects", strconv.Itoa(st.Objects)).
		Add("Payloads", strconv.Itoa(st.Payloads)).
		Add("Attachments", strconv.Itoa(st.Attachments)).
		Render()

	if len(st.Projects) == 0 {
		fmt.Fprintf(s.W, "\n%s\n", s.Info("No projects registered. Run 'eka project register' to add one."))
		return nil
	}
	for _, p := range st.Projects {
		fmt.Fprintf(s.W, "\n%s %s\n", ui.IconBullet, s.Accent(p.Project.ID))
		for _, r := range p.Repos {
			fmt.Fprintf(s.W, "  %s %s  (%s)%s\n", ui.IconBullet, s.Info(r.Repo.Name), displayPath(r.Repo.Path), lastSyncDetail(r.LastSync))
		}
	}
	return nil
}

// lastSyncDetail renders the most recent sync-log entry of one
// repository ("" when none).
func lastSyncDetail(e *runtime.SyncEntry) string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("  [%s %s at %s]", e.Direction, shortDigest(e.SnapshotDigest), e.At)
}
