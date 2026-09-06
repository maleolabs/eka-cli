package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

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
	cmd := &cobra.Command{
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
			showAll, _ := cmd.Flags().GetBool("all")
			showJSON, _ := cmd.Flags().GetBool("json")
			if !r.Exists() {
				if showJSON {
					return renderStatusJSON(s, nil, home)
				}
				fmt.Fprintf(s.W, "%s\n", s.Accent("Runtime"))
				fmt.Fprintf(s.W, "  %s\n", s.Info("No EKA workspace at "+home+" yet. Run 'eka project register' to create it."))
				return nil
			}
			st, err := r.Workspace.Status()
			if err != nil {
				return fmt.Errorf("status failed: %w", err)
			}
			if !showAll {
				st = scopeStatus(st)
			}
			if showJSON {
				return renderStatusJSON(s, st, home)
			}
			return renderStatus(s, st)
		},
	}
	cmd.Flags().Bool("all", false, "show global workspace status (disable repo-scoped filtering)")
	cmd.Flags().Bool("json", false, "emit deterministic JSON (schema status-v1)")
	return cmd
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

// scopeStatus returns a view of the workspace status scoped to the
// repository that contains the current working directory. When cwd is
// inside a registered repository, only that project is returned;
// otherwise the full global status is returned. The store totals
// (Objects/Payloads/Attachments) remain global.
func scopeStatus(st *runtime.WorkspaceStatus) *runtime.WorkspaceStatus {
	cwd, err := os.Getwd()
	if err != nil {
		return st
	}
	cwd = filepath.Clean(cwd)
	var matched *runtime.ProjectStatus
	matchedLen := -1
	for i := range st.Projects {
		for _, rs := range st.Projects[i].Repos {
			repoPath := filepath.Clean(rs.Repo.Path)
			if cwd == repoPath || strings.HasPrefix(cwd+string(filepath.Separator), repoPath+string(filepath.Separator)) {
				if len(repoPath) > matchedLen {
					matchedLen = len(repoPath)
					matched = &st.Projects[i]
				}
			}
		}
	}
	if matched == nil {
		return st
	}
	out := *st
	out.Projects = []runtime.ProjectStatus{*matched}
	return &out
}

// statusJSON is the deterministic wire format for `eka status --json`
// (schema status-v1). Field order is fixed by struct definition.
type statusJSON struct {
	Schema  string                `json:"schema"`
	Path    string                `json:"path"`
	ID      string                `json:"id"`
	Created string                `json:"created"`
	SchemaVersion int            `json:"schemaVersion"`
	Objects int                  `json:"objects"`
	Payloads int                 `json:"payloads"`
	Attachments int              `json:"attachments"`
	Projects []jsonProject      `json:"projects"`
}
type jsonProject struct {
	ID    string      `json:"id"`
	Name  string      `json:"name"`
	Created string   `json:"created"`
	Repos []jsonRepo `json:"repos"`
}
type jsonRepo struct {
	ProjectID string `json:"projectId"`
	Name      string `json:"name"`
	Path      string `json:"path"`
	Created   string `json:"created"`
	Namespace string `json:"namespace"`
	LastSync  *runtime.SyncEntry `json:"lastSync,omitempty"`
}

func renderStatusJSON(s *ui.Style, st *runtime.WorkspaceStatus, home string) error {
	if st == nil {
		out := map[string]string{"schema":"status-v1","message":"No EKA workspace at "+home+" yet. Run 'eka project register' to create it."}
		b, _ := json.Marshal(out)
		fmt.Fprintln(s.W, string(b))
		return nil
	}
	js := statusJSON{
		Schema: "status-v1",
		Path: st.Path,
		ID: st.ID,
		Created: st.Created,
		SchemaVersion: st.SchemaVersion,
		Objects: st.Objects,
		Payloads: st.Payloads,
		Attachments: st.Attachments,
		Projects: make([]jsonProject, 0, len(st.Projects)),
	}
	for _, p := range st.Projects {
		jp := jsonProject{ID: p.Project.ID, Name: p.Project.Name, Created: p.Project.Created}
		for _, rs := range p.Repos {
			jp.Repos = append(jp.Repos, jsonRepo{ProjectID: rs.Repo.ProjectID, Name: rs.Repo.Name, Path: rs.Repo.Path, Created: rs.Repo.Created, Namespace: rs.Repo.Namespace, LastSync: rs.LastSync})
		}
		js.Projects = append(js.Projects, jp)
	}
	b, err := json.Marshal(js)
	if err != nil {
		return fmt.Errorf("status json failed: %w", err)
	}
	fmt.Fprintln(s.W, string(b))
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
