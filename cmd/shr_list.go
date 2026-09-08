package cmd

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/maleolabs/eka-cli/cmd/ui"
	"github.com/maleolabs/eka-core/exchange"
	"github.com/maleolabs/eka-core/runtime"
	"github.com/spf13/cobra"
)

func newShrListCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List shared knowledge (global, outside repo)",
		Long: `List shared knowledge (shr) stored in the workspace.

Global — does not require eka.yaml. Reads from the workspace store
(~/.eka) directly, so it works from any directory, inside or outside
an EKA repository.

Default output is clean: id + level only (id, level, project, version, title).
Use --verbose or --json for full detail, or shr show for one item.

Filters are server-side (shr only):

  --level L0|L1|L2   by level
  --project <name>   by sourceProject
  --version <ver>    by sourceVersion (semver)

Examples:
  eka shr list
  eka shr list --level L0 --project my-app
  eka shr list --level L2 --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			level, _ := cmd.Flags().GetString("level")
			project, _ := cmd.Flags().GetString("project")
			version, _ := cmd.Flags().GetString("version")
			jsonOut, _ := cmd.Flags().GetBool("json")
			verbose, _ := cmd.Flags().GetBool("verbose")
			if level != "" {
				lv := strings.ToUpper(strings.TrimSpace(level))
				if lv != "L0" && lv != "L1" && lv != "L2" {
					return fmt.Errorf("shr list: --level must be L0, L1 or L2, got %q", level)
				}
				level = lv
			}
			if version != "" {
				if _, _, _, ok := parseGetVersionParts(version); !ok {
					return fmt.Errorf("shr list: --version must be semver X.Y.Z or X.Y, got %q", version)
				}
			}
			r, err := runtime.Open()
			if err != nil {
				return err
			}
			defer r.Close()
			if !r.Exists() {
				fmt.Fprintf(cmd.ErrOrStderr(), "eka: no workspace at %s; run 'eka sync' first\n", r.Path())
				return &exitError{code: exitFail}
			}
			// Global search: all projects, Operations domain (shr list is global, outside repo)
			projects, err := r.Workspace.Projects()
			if err != nil {
				return fmt.Errorf("shr list failed: %w", err)
			}
			var allUnits []*exchange.Unit
			for _, proj := range projects {
				unitsForProj, err := r.Knowledge.Search(runtime.SearchQuery{ProjectID: proj.ID, Domain: "Operations", Type: "shr"})
				if err != nil {
					return fmt.Errorf("shr list failed: %w", err)
				}
				allUnits = append(allUnits, unitsForProj...)
			}
			units := allUnits
			if len(projects) == 0 {
				units = []*exchange.Unit{}
			}
			// Dedup latest per line (parity get)
			units = dedupLinesLatest(units)
			if level != "" {
				units = filterByShrLevel(units, level)
			}
			if project != "" {
				units = filterByShrProject(units, project)
			}
			if version != "" {
				units = filterByShrVersion(units, version)
			}
			s := styleFor(cmd)
			if jsonOut {
				// Machine JSON: clean list
				type entry struct {
					ID      string `json:"id"`
					Level   string `json:"level"`
					Project string `json:"project"`
					Version string `json:"version"`
					Title   string `json:"title"`
					Form    string `json:"form"`
				}
				out := make([]entry, 0, len(units))
				for _, u := range units {
					var m map[string]any
					_ = json.Unmarshal(u.ContentPayload, &m)
					title, _ := m["title"].(string)
					out = append(out, entry{
						ID:      u.Identity.ID,
						Level:   shrLevelOf(u),
						Project: shrProjectOf(u),
						Version: shrVersionOf(u),
						Title:   title,
						Form:    u.CanonicalIdentityForm,
					})
				}
				b, _ := json.MarshalIndent(out, "", "  ")
				fmt.Fprintln(cmd.OutOrStdout(), string(b))
				return nil
			}
			if len(units) == 0 {
				ui.NewHeader(s, "Shared knowledge (shr)").Render()
				fmt.Fprintln(s.W, "no shared knowledge (shr) in workspace")
				fmt.Fprintln(s.W, s.Dim("Use eka shr build <source> --level L0 to create one"))
				return nil
			}
			// Human clean list: id + level (project/version/title if verbose) — theme table inside global margin
			if verbose {
				ui.NewHeader(s, "Shared knowledge (shr) — verbose").Render()
				// Table head inside global margin (s.W) with theme table (header dim, aligned columns)
				fmt.Fprintln(s.W, s.Dim("ID                              | Level | Project         | Version    | Title"))
				fmt.Fprintln(s.W, s.Dim("--------------------------------+-------+-----------------+------------+------------------------------"))
				for _, u := range units {
					levelStr := shrLevelOf(u)
					var m map[string]any
					_ = json.Unmarshal(u.ContentPayload, &m)
					title, _ := m["title"].(string)
					fmt.Fprintf(s.W, "  %-30s %-4s  %-15s %-10s %s\n", u.Identity.ID, levelStr, shrProjectOf(u), shrVersionOf(u), title)
				}
			} else {
				ui.NewHeader(s, "Shared knowledge (shr) — clean list (id + level)").Render()
				fmt.Fprintln(s.W, s.Dim("ID                              | Level"))
				fmt.Fprintln(s.W, s.Dim("--------------------------------+-------"))
				for _, u := range units {
					fmt.Fprintf(s.W, "  %-30s %s\n", u.Identity.ID, shrLevelOf(u))
				}
				// Empty line margin top before tip, inside global margin
				fmt.Fprintln(s.W, "")
				fmt.Fprintln(s.W, s.Dim("Use --verbose for project/version/title, --json for machine, or eka shr show <id> for detail"))
			}
			ui.NewSummary(s).Add("Count", fmt.Sprintf("%d shr", len(units))).Render()
			return nil
		},
	}
	cmd.Flags().String("level", "", "filter by level L0|L1|L2")
	cmd.Flags().String("project", "", "filter by sourceProject")
	cmd.Flags().String("version", "", "filter by sourceVersion semver")
	cmd.Flags().Bool("json", false, "machine JSON (clean list)")
	cmd.Flags().BoolP("verbose", "v", false, "include project/version/title")
	return cmd
}

func newShrShowCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <id>",
		Short: "Show one shared knowledge detail (global)",
		Long: `Show one shared knowledge (shr) detail.

Global — does not require eka.yaml. Resolves shr by id (bare id, shr:<id>,
or qualified <ns>/shr:<id>[:<ver>]) from the workspace store.

Strict filters: --level/--project/--version must match the stored shr
or the command refuses (exit 2). Use --json for machine JSON, otherwise
human detail with overview, structure, and deepDocs per level.

Examples:
  eka shr show share-opensid-l0
  eka shr show share-opensid-l0 --level L0 --project nest-desacorp
  eka shr show share-opensid-l0 --json
  eka shr show eka/shr:share-adr-l2 --level L2`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			target := strings.TrimSpace(args[0])
			level, _ := cmd.Flags().GetString("level")
			project, _ := cmd.Flags().GetString("project")
			version, _ := cmd.Flags().GetString("version")
			jsonOut, _ := cmd.Flags().GetBool("json")
			withDocs, _ := cmd.Flags().GetBool("with-docs")
			if level != "" {
				lv := strings.ToUpper(strings.TrimSpace(level))
				if lv != "L0" && lv != "L1" && lv != "L2" {
					return fmt.Errorf("shr show: --level must be L0, L1 or L2, got %q", level)
				}
				level = lv
			}
			if version != "" {
				if _, _, _, ok := parseGetVersionParts(version); !ok {
					return fmt.Errorf("shr show: --version must be semver, got %q", version)
				}
			}
			// Normalize target: bare id → try resolve via workspace search
			r, err := runtime.Open()
			if err != nil {
				return err
			}
			defer r.Close()
			if !r.Exists() {
				fmt.Fprintf(cmd.ErrOrStderr(), "eka: no workspace\n")
				return &exitError{code: exitFail}
			}
			// Try resolve as qualified form first
			var formToResolve string
			if strings.Contains(target, "/") || strings.Contains(target, ":") {
				formToResolve = target
				if !strings.Contains(target, "/") && strings.Contains(target, ":") {
					// bare type:id without ns — try with default ns eka
					// For global show, we search instead
					formToResolve = ""
				}
			}
			var unit *exchange.Unit
			// Use runtime Resolver if form looks qualified
			if formToResolve != "" {
				u, ok, err := r.Resolver.Resolve(formToResolve)
				if err == nil && ok {
					unit = u
				}
			}
			if unit == nil {
				// Fallback: search global Operations shr by id
				id := target
				// strip shr: prefix and ns/
				if idx := strings.LastIndex(id, ":"); idx >= 0 {
					id = id[idx+1:]
				}
				if idx := strings.LastIndex(id, "/"); idx >= 0 {
					id = id[idx+1:]
				}
				id = strings.TrimPrefix(id, "shr:")
				projects, err := r.Workspace.Projects()
				if err != nil {
					return err
				}
				var allUnits []*exchange.Unit
				for _, proj := range projects {
					unitsForProj, err := r.Knowledge.Search(runtime.SearchQuery{ProjectID: proj.ID, Domain: "Operations", Type: "shr"})
					if err != nil {
						return err
					}
					allUnits = append(allUnits, unitsForProj...)
				}
				units := dedupLinesLatest(allUnits)
				for _, u := range units {
					if u.Identity.ID == id {
						unit = u
						break
					}
				}
				if unit == nil {
					return fmt.Errorf("shr show: %q not found", target)
				}
			}
			// Strict filter checks
			if level != "" && shrLevelOf(unit) != level {
				return fmt.Errorf("shr show: %q level %q does not match filter --level %q", target, shrLevelOf(unit), level)
			}
			if project != "" && shrProjectOf(unit) != project {
				return fmt.Errorf("shr show: %q project %q does not match filter --project %q", target, shrProjectOf(unit), project)
			}
			if version != "" && shrVersionOf(unit) != version {
				return fmt.Errorf("shr show: %q version %q does not match filter --version %q", target, shrVersionOf(unit), version)
			}
			// Render
			s := styleFor(cmd)
			if jsonOut {
				var m map[string]any
				_ = json.Unmarshal(unit.ContentPayload, &m)
				b, _ := json.MarshalIndent(m, "", "  ")
				fmt.Fprintln(cmd.OutOrStdout(), string(b))
				return nil
			}
			// Human detail
			var m map[string]any
			_ = json.Unmarshal(unit.ContentPayload, &m)
			title, _ := m["title"].(string)
			desc, _ := m["description"].(string)
			lvl, _ := m["level"].(string)
			proj, _ := m["sourceProject"].(string)
			ver, _ := m["sourceVersion"].(string)
			summary, _ := m["summary"].(string)
			snap, _ := m["snapshot"]
			deepDocs, _ := m["deepDocs"]
			ui.NewHeader(s, "Shr detail — "+unit.Identity.ID).Render()
			// Detail inside global margin (s.W) with theme, empty line before tip
			fmt.Fprintf(s.W, "  ID: %s\n", unit.Identity.ID)
			fmt.Fprintf(s.W, "  Level: %s  Project: %s  Version: %s\n", lvl, proj, ver)
			fmt.Fprintf(s.W, "  Title: %s\n", title)
			fmt.Fprintf(s.W, "  Description: %s\n", desc)
			fmt.Fprintf(s.W, "  Form: %s\n", unit.CanonicalIdentityForm)
			if summary != "" {
				fmt.Fprintf(s.W, "\n  Summary (%s): %s\n", lvl, summary)
			}
			if withDocs && deepDocs != nil {
				fmt.Fprintf(s.W, "\n  DeepDocs: %v\n", deepDocs)
			} else if snap != nil && lvl == "L2" {
				b, _ := json.Marshal(snap)
				snip := string(b)
				if len(snip) > 500 {
					snip = snip[:500] + "..."
				}
				fmt.Fprintf(s.W, "\n  Snapshot (L2): %s\n", snip)
			}
			fmt.Fprintln(s.W, "")
			fmt.Fprintln(s.W, s.Dim("Use --json for full payload, --with-docs for deepDocs (L2)"))
			return nil
		},
	}
	cmd.Flags().String("level", "", "strict filter L0|L1|L2")
	cmd.Flags().String("project", "", "strict filter project")
	cmd.Flags().String("version", "", "strict filter version")
	cmd.Flags().Bool("json", false, "machine JSON")
	cmd.Flags().Bool("with-docs", false, "include deepDocs for L2")
	return cmd
}
