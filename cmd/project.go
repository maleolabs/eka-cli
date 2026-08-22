package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/maleolabs/eka-cli/cmd/ui"
	"github.com/maleolabs/eka-core/conformance"
	"github.com/maleolabs/eka-core/metadata"
	"github.com/maleolabs/eka-core/runtime"
	"github.com/maleolabs/eka-core/workspace"
	"github.com/spf13/cobra"
)

// newProjectCommand builds the `eka project` command tree: the project
// and repository registry of the EKA workspace.
//
// Exit codes:
//
//	0  registration succeeded (new or already registered); list printed
//	2  usage or internal error (workspace resolution, registry failure,
//	   path is not an EKA repository)
func newProjectCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "project",
		Short: "Manage EKA workspace projects",
		Long: `Manage the projects and repositories registered in the EKA
workspace (default ~/.eka, or $EKA_HOME).

A project groups one or more repositories; inside an EKA repository
(a directory tree carrying eka.yaml) the repository identity comes
from the file — project, repository name and namespace. The canonical
store attributes every pulled object to its repository.

'eka project remove <project>/<name>' unregisters a repository;
removing a project's LAST repository deletes the empty project too
(canonical knowledge objects stay in the workspace store).

'eka project unregister <project> [--force]' unregisters a whole
project and all its repositories (path moved, project deleted,
cleanup). On a terminal the command prompts for confirmation;
outside a terminal --force is required.

Exit codes:
  0  success
  2  usage or internal error`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	cmd.AddCommand(newProjectRegisterCommand(), newProjectListCommand(), newProjectRemoveCommand(), newProjectUnregisterCommand())
	return cmd
}

// newProjectRegisterCommand builds `eka project register [path]
// [--name NAME] [--override]`.
func newProjectRegisterCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "register [path]",
		Short: "Register a repository in the workspace",
		Long: `Register the EKA repository at path (default: the current
directory) in the EKA workspace.

Registration requires an EKA repository: a directory tree carrying
eka.yaml (run 'eka init' to create one — there is no legacy mode, so
a directory without eka.yaml is refused). The identity comes from the
file — project, repository name and default namespace — and --name is
refused when it conflicts with the recorded project (the metadata is
the identity authority). Registering the same repository again is a
no-op (the stored path is refreshed).

The CONTENT namespace is authoritative for unit identity (ADR-020):
when the docs tree resolves to exactly one namespace differing from
the one recorded in eka.yaml, registration is refused with an override
hint (exit 2) — or aligned with --override (or an interactive
confirmation on a terminal): eka.yaml namespace rewritten, repos.namespace
updated, the identity frozen again. Content spanning multiple
namespaces is refused without override (a repository is one platform —
consolidate the content).

Exit codes:
  0  registration succeeded
  2  usage or internal error (workspace resolution, registry failure,
     path is not an EKA repository, or the content namespace differs
     from the declared one)`,
		Example: `  eka project register
  eka project register /path/to/repo
  eka project register --override   align the identity to the content namespace`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := cmd.Flags().GetString("name")
			if err != nil {
				return fmt.Errorf("project register failed: %w", err)
			}
			override, err := cmd.Flags().GetBool("override")
			if err != nil {
				return fmt.Errorf("project register failed: %w", err)
			}
			path := "."
			if len(args) == 1 {
				path = args[0]
			}
			if info, err := os.Stat(path); err != nil {
				return fmt.Errorf("project register failed: cannot access %s: %w", path, err)
			} else if !info.IsDir() {
				return fmt.Errorf("project register failed: %s is not a directory", path)
			}
			abs, err := filepath.Abs(path)
			if err != nil {
				return fmt.Errorf("project register failed: cannot resolve %s: %w", path, err)
			}
			abs = filepath.Clean(abs)

			// The repository context gate (ADR-018): registration
			// requires an EKA repository — a directory tree carrying
			// eka.yaml. There is no legacy mode: a directory without
			// the file is refused deterministically. The walk-up
			// directory is the repository root — registering from a
			// subdirectory registers the root, never the subdir.
			m, mdir, hasMeta, err := metadata.Find(abs)
			if err != nil {
				return fmt.Errorf("project register failed: %w", err)
			}
			if !hasMeta {
				return fmt.Errorf("project register failed: %s is not an EKA repository (no eka.yaml); run 'eka init' first", abs)
			}

			// Identity metadata wins (ADR-017 §5.3): inside a
			// repository with eka.yaml the identity triple comes from
			// the file; an explicit --name that conflicts with the
			// recorded project is refused.
			if name != "" && name != m.Project {
				return fmt.Errorf("project register failed: --name %s conflicts with the project %s recorded in eka.yaml; the metadata is the identity authority — use --name only for repositories without eka.yaml",
					name, m.Project)
			}

			// Content namespace reconciliation (ADR-020 Decision 3):
			// the CONTENT namespace is authoritative for unit
			// identity (P3). At registration time the content source
			// is the docs tree (no store pull has happened yet), so
			// the distinct namespaces come from the scanned
			// artifacts (conformance.Scan), sorted deterministically.
			// Multi-ns content is refused without override; a single
			// distinct namespace differing from the declared one is
			// refused with the override hint — or aligned by
			// --override / the interactive confirmation, which is
			// wired exactly like the sync engine's.
			alignedNote := ""
			artifacts, err := conformance.Scan(mdir)
			if err != nil {
				return fmt.Errorf("project register failed: %w", err)
			}
			nsSet := map[string]bool{}
			var nsList []string
			for _, a := range artifacts {
				if a.Namespace == "" || nsSet[a.Namespace] {
					continue
				}
				nsSet[a.Namespace] = true
				nsList = append(nsList, a.Namespace)
			}
			sort.Strings(nsList)
			switch {
			case len(nsList) >= 2:
				return fmt.Errorf("project register failed: the repository content spans multiple namespaces (%s); a repository is one platform — consolidate the content",
					strings.Join(nsList, ", "))
			case len(nsList) == 1 && nsList[0] != m.Namespace:
				contentNS := nsList[0]
				align := override
				// Interactive confirmation only when BOTH stdin and
				// stdout are terminals (a captured-output run must
				// never block on a prompt the user cannot see — it
				// refuses deterministically instead).
				if !align && isTTYReader(cmd.InOrStdin()) && styleFor(cmd).TTY {
					// Interactive confirmation (TTY): the
					// arrow-selected options are "align identity to
					// <contentNS>" (default) and "abort" — abort
					// yields the same deterministic refusal, exit 2.
					value, err := ui.Select(styleFor(cmd), cmd.InOrStdin(), cmd.OutOrStdout(),
						"the repository content namespace "+contentNS+" differs from the registered repository namespace "+m.Namespace,
						[]ui.MenuItem{
							{Title: "align identity to " + contentNS, Value: "align"},
							{Title: "abort", Value: "abort"},
						},
						0)
					if err != nil {
						if !errors.Is(err, ui.ErrCancelled) {
							return fmt.Errorf("project register failed: %w", err)
						}
						// Cancelled: the abort path below.
						align = false
					} else {
						align = value == "align"
					}
				}
				if !align {
					return fmt.Errorf("project register failed: the repository content namespace %s differs from the registered repository namespace %s; run 'eka project register --override' to align the repository identity to %s",
						contentNS, m.Namespace, contentNS)
				}
				// Alignment: the declared identity moves to the
				// content namespace — registration writes the aligned
				// value, repos.namespace is re-pointed (the upsert
				// never updates it), eka.yaml is rewritten (project/
				// name untouched), and the deterministic aligned note
				// is printed in the output.
				oldNS := m.Namespace
				m.Namespace = contentNS
				alignedNote = fmt.Sprintf("repository namespace aligned: %s → %s (eka.yaml updated; identity frozen again)", oldNS, contentNS)
			}

			// The WorkspaceService (runtime) does not expose the
			// metadata registration path; the registry itself is
			// idempotent and shares the same EKA_HOME store. The
			// register command touches only the registry, so the
			// workspace layer is the whole Runtime it needs. The
			// registered path is the repository root (mdir), never
			// the argument path — a subdirectory of a repository
			// registers the root.
			ws, err := workspace.Ensure()
			if err != nil {
				return err
			}
			defer ws.Close()
			project, repo, created, err := ws.RegisterRepoMetadata(mdir, m)
			if err != nil {
				return err
			}
			if alignedNote != "" {
				// The registration upsert never updates repos.namespace
				// on an existing row: re-point it explicitly.
				if err := ws.Store().SetRepoNamespace(repo.ProjectID, repo.Name, m.Namespace); err != nil {
					return fmt.Errorf("project register failed: %w", err)
				}
				// Rewrite eka.yaml at the walk-up root from the
				// CURRENT file (re-read + re-parse: a file that
				// stopped parsing must refuse before being
				// replaced), with only the namespace changed.
				data, err := os.ReadFile(filepath.Join(mdir, "eka.yaml"))
				if err != nil {
					return fmt.Errorf("project register failed: cannot read eka.yaml for the namespace alignment: %w", err)
				}
				parsed, err := metadata.Parse(data)
				if err != nil {
					return fmt.Errorf("project register failed: cannot parse eka.yaml for the namespace alignment: %w", err)
				}
				aligned := metadata.Metadata{Version: parsed.Version, Project: parsed.Project, Name: parsed.Name, Namespace: m.Namespace}
				if err := os.WriteFile(filepath.Join(mdir, "eka.yaml"), aligned.Marshal(), 0o644); err != nil {
					return fmt.Errorf("project register failed: cannot rewrite eka.yaml with the aligned namespace: %w", err)
				}
			}
			status := "already registered"
			if created {
				status = "registered"
			}
			s := styleFor(cmd)
			ui.NewHeader(s, "Project").
				Add("Project", project.ID).
				Add("Repository", repo.Name).
				Add("Path", repo.Path).
				Render()
			ui.NewSummary(s).
				Add("Project", project.ID).
				Add("Repository", repo.Name).
				Add("Path", repo.Path).
				Add("Status", status).
				Render()
			if alignedNote != "" {
				fmt.Fprintf(s.W, "  %s %s\n", ui.IconBullet, s.Info(alignedNote))
			}
			return nil
		},
	}
	cmd.Flags().String("name", "", "project name (default: the project recorded in eka.yaml)")
	cmd.Flags().Bool("override", false,
		"align the repository identity to the content namespace when they differ (machine override)")
	return cmd
}

// newProjectListCommand builds `eka project list`.
func newProjectListCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List registered projects and repositories",
		Long: `List every project registered in the EKA workspace with its
repositories (name and path), sorted deterministically: projects by
id, repositories by name. A workspace with no registered projects
prints an informational message and exits 0.

Exit codes:
  0  success
  2  usage or internal error`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			r, err := runtime.Ensure()
			if err != nil {
				return err
			}
			defer r.Close()

			projects, err := r.Workspace.Projects()
			if err != nil {
				return err
			}
			s := styleFor(cmd)
			ui.NewHeader(s, "Projects").
				Add("Workspace", r.Path()).
				Render()
			if len(projects) == 0 {
				fmt.Fprintf(s.W, "\n%s\n", s.Info("No projects registered yet. Run 'eka project register' to add one."))
				return nil
			}
			for _, p := range projects {
				repos, err := r.Workspace.Repos(p.ID)
				if err != nil {
					return err
				}
				fmt.Fprintf(s.W, "\n%s %s\n", ui.IconBullet, s.Accent(p.ID))
				for _, r := range repos {
					fmt.Fprintf(s.W, "  %s %s  (%s)\n", ui.IconBullet, s.Info(r.Name), displayPath(r.Path))
				}
			}
			return nil
		},
	}
	return cmd
}

// newProjectRemoveCommand builds `eka project remove <project>/<name>`.
func newProjectRemoveCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "remove <project>/<name>",
		Short: "Remove a repository from the workspace registry",
		Long: `Remove (unregister) the repository <name> of <project> from
the EKA workspace registry. The target is the composite
<project>/<name> — exactly as 'eka project list' renders it.

Removing a repository deletes its registry row; when it was the
project's LAST repository, the emptied project row is deleted too.
Canonical knowledge objects are NOT deleted: they remain in the
workspace store under their provenance pair, and re-registering the
repository restores their provenance access.

Exit codes:
  0  removal succeeded
  2  usage or internal error (bad composite argument, unknown project
     or repository, registry failure)`,
		Example: `  eka project remove atrium/api
  eka project remove eka-sync-fixture/eka-sync-fixture`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			projectID, name, err := parseProjectRepoArg(args[0])
			if err != nil {
				return fmt.Errorf("project remove failed: %w", err)
			}

			r, err := runtime.Ensure()
			if err != nil {
				return err
			}
			defer r.Close()

			// Deterministic candidate listing: an unknown project or
			// repository names what IS registered (the same style the
			// other commands use for unknown targets).
			projects, err := r.Workspace.Projects()
			if err != nil {
				return err
			}
			var known bool
			for _, p := range projects {
				if p.ID == projectID {
					known = true
					break
				}
			}
			if !known {
				ids := make([]string, 0, len(projects))
				for _, p := range projects {
					ids = append(ids, p.ID)
				}
				sort.Strings(ids)
				if len(ids) == 0 {
					return fmt.Errorf("project remove failed: unknown project %q — no projects are registered; run 'eka project register' first", projectID)
				}
				return fmt.Errorf("project remove failed: unknown project %q — available projects: %s", projectID, strings.Join(ids, ", "))
			}
			repos, err := r.Workspace.Repos(projectID)
			if err != nil {
				return err
			}
			var repo *workspace.Repo
			names := make([]string, 0, len(repos))
			for i := range repos {
				names = append(names, repos[i].Name)
				if repos[i].Name == name {
					repo = &repos[i]
				}
			}
			if repo == nil {
				if len(names) == 0 {
					return fmt.Errorf("project remove failed: unknown repository %q in project %q — the project has no repositories", name, projectID)
				}
				sort.Strings(names)
				return fmt.Errorf("project remove failed: unknown repository %q in project %q — available repositories: %s", name, projectID, strings.Join(names, ", "))
			}

			removed, err := r.Workspace.UnregisterRepo(projectID, name)
			if err != nil {
				return err
			}
			if !removed {
				return fmt.Errorf("project remove failed: repository %s/%s vanished during removal; nothing was changed", projectID, name)
			}
			s := styleFor(cmd)
			ui.NewHeader(s, "Projects").
				Add("Workspace", r.Path()).
				Render()
			fmt.Fprintf(s.W, "\n%s removed %s  (%s)\n", ui.IconBullet, s.Info(repo.Name), displayPath(repo.Path))
			fmt.Fprintf(s.W, "\n%s\n", s.Dim("Canonical knowledge objects remain in the workspace store; re-registering restores provenance access."))
			return nil
		},
	}
	return cmd
}

// newProjectUnregisterCommand builds `eka project unregister <project> [--force]`.
func newProjectUnregisterCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "unregister <project> [--force]",
		Short: "Remove a project and all its repositories from the workspace registry",
		Long: `Remove (unregister) the whole project <project> and all its
repositories from the EKA workspace registry. The target is the
project id — exactly as 'eka project list' renders it.

Unregistering a project deletes every repository row under that
project; the emptied project row is deleted too. Canonical knowledge
objects are NOT deleted: they remain in the workspace store under
their provenance pairs, and re-registering restores provenance
access. Use for: project deleted, path moved, cleanup.

On a terminal the command prompts for confirmation when the project
has repositories; outside a terminal --force is required (agents
decide programmatically). The prompt defaults to abort.

Exit codes:
  0  unregistration succeeded (or aborted at the prompt)
  2  usage or internal error (bad argument, unknown project, registry
     failure, or --force required outside a terminal)`,
		Example: `  eka project unregister atrium --force
  eka project unregister eka-sync-fixture`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			projectID := args[0]
			if strings.Contains(projectID, "/") || strings.TrimSpace(projectID) == "" {
				return fmt.Errorf("project unregister failed: the target must be <project>, got %q", args[0])
			}
			force, err := cmd.Flags().GetBool("force")
			if err != nil {
				return fmt.Errorf("project unregister failed: %w", err)
			}

			r, err := runtime.Ensure()
			if err != nil {
				return err
			}
			defer r.Close()

			projects, err := r.Workspace.Projects()
			if err != nil {
				return err
			}
			var known bool
			for _, p := range projects {
				if p.ID == projectID {
					known = true
					break
				}
			}
			if !known {
				ids := make([]string, 0, len(projects))
				for _, p := range projects {
					ids = append(ids, p.ID)
				}
				sort.Strings(ids)
				if len(ids) == 0 {
					return fmt.Errorf("project unregister failed: unknown project %q — no projects are registered; run 'eka project register' first", projectID)
				}
				return fmt.Errorf("project unregister failed: unknown project %q — available projects: %s", projectID, strings.Join(ids, ", "))
			}
			repos, err := r.Workspace.Repos(projectID)
			if err != nil {
				return err
			}
			n := len(repos)

			// Confirmation: interactive when tty, otherwise require --force.
			if n > 0 && !force {
				sTTY := styleFor(cmd)
				tty := sTTY.TTY && isTTYReader(cmd.InOrStdin())
				if !tty {
					return fmt.Errorf("project unregister failed: project %q has %s; run 'eka project unregister %s --force' to unregister or confirm interactively on a terminal", projectID, plural(n, "repository", "repositories"), projectID)
				}
				// Interactive confirmation (TTY): unregister vs abort, default abort.
				prompt := fmt.Sprintf("unregister project %q (%s) and all its repositories?", projectID, plural(n, "repository", "repositories"))
				value, err := ui.Select(sTTY, cmd.InOrStdin(), cmd.OutOrStdout(), prompt,
					[]ui.MenuItem{
						{Title: "unregister project " + projectID, Value: "unregister"},
						{Title: "abort", Value: "abort"},
					}, 1)
				if err != nil {
					if errors.Is(err, ui.ErrCancelled) {
						fmt.Fprintf(sTTY.W, "Aborted; project %s kept.\n", projectID)
						return nil
					}
					return fmt.Errorf("project unregister failed: %w", err)
				}
				if value != "unregister" {
					fmt.Fprintf(sTTY.W, "Aborted; project %s kept.\n", projectID)
					return nil
				}
			}

			// Remove the whole project in one registry operation: every
			// repository row under it and the project row itself
			// (workspace.UnregisterProject semantics). Canonical objects
			// stay. The count is re-read at removal time, so a repository
			// registered between the listing above and this call is
			// removed too — the confirmation prompt names the count seen
			// at prompt time, the summary reports what was actually
			// removed.
			removedCount, err := r.Workspace.UnregisterProject(projectID)
			if err != nil {
				return err
			}
			s := styleFor(cmd)
			ui.NewHeader(s, "Projects").
				Add("Workspace", r.Path()).
				Render()
			if n == 0 {
				fmt.Fprintf(s.W, "\n%s unregistered project %s (no repositories)\n", ui.IconBullet, s.Info(projectID))
			} else {
				fmt.Fprintf(s.W, "\n%s unregistered project %s (%s)\n", ui.IconBullet, s.Info(projectID), plural(removedCount, "repository", "repositories"))
				for _, repo := range repos {
					fmt.Fprintf(s.W, "  %s %s  (%s)\n", ui.IconBullet, s.Info(repo.Name), displayPath(repo.Path))
				}
			}
			fmt.Fprintf(s.W, "\n%s\n", s.Dim("Canonical knowledge objects remain in the workspace store; re-registering restores provenance access."))
			return nil
		},
	}
	cmd.Flags().Bool("force", false, "unregister without the confirmation prompt (non-interactive runs)")
	return cmd
}

// parseProjectRepoArg splits the composite `<project>/<name>` argument:
// exactly one separator with two non-empty halves — anything else is a
// usage error (exit 2).
func parseProjectRepoArg(arg string) (project, name string, err error) {
	parts := strings.Split(arg, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("the target must be <project>/<name>, got %q", arg)
	}
	return parts[0], parts[1], nil
}

// displayPath renders a repository path relative to the current
// directory when it is a descendant, else absolute — shorter output,
// still deterministic.
func displayPath(path string) string {
	wd, err := filepath.Abs(".")
	if err != nil {
		return path
	}
	rel, err := filepath.Rel(wd, path)
	if err != nil || rel == ".." || len(rel) > 2 && rel[:3] == ".."+string(filepath.Separator) {
		return path
	}
	return rel
}
