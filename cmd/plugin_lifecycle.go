package cmd

// This file implements the plugin lifecycle commands: `eka plugin
// list`, `eka plugin remove` and `eka plugin update` — what is
// discovered and installed (with its trust tier), uninstalling an
// installed plugin, and re-downloading the latest verified release.
//
// Like plugin install, the lifecycle commands are distribution
// convenience: they never touch the EKA workspace or the canonical
// store.
//
// Trust tier (list): a plugin name is labeled "official" when the
// built-in registry (plugin.OfficialRegistry) carries it, else
// "third-party". The full two-tier consent model is a LATER work item
// (sto:plugin-trust-model); list only LABELS the tier — it never
// asks for consent, and update only ever touches official plugins
// (the registry is the filter).
//
// Exit codes:
//
//	list    0  always (an empty or broken plugin set is a visible,
//	           deterministic report, never a failure)
//	remove  0  removed
//	        1  refusal (plugin not installed)
//	        2  usage or internal error
//	update  0  updated (also: --all with nothing installed, or --all
//	           with every update successful)
//	        1  refusal (unknown plugin, plugin not installed,
//	           unresolved release, checksum mismatch, broken new
//	           manifest), or --all with at least one failed update
//	        2  usage or internal error (missing name without --all,
//	           --all with a name)

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/maleolabs/eka-cli/cmd/ui"
	"github.com/maleolabs/eka-cli/plugin"
	"github.com/spf13/cobra"
)

// newPluginListCommand builds `eka plugin list [--json]`: the
// deterministic plugin report (discovered + installed + trust tier).
func newPluginListCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List discovered and installed EKA plugins",
		Long: `List the plugins the CLI can see: every "eka-<name>" executable
discovered on PATH or in the plugin directory ($EKA_PLUGIN_DIR or
~/.eka/plugins), with the installed state, the manifest version and
source (where readable) and the trust tier — "official" for plugins
resolved through the CLI's built-in registry, "third-party"
otherwise.

A plugin whose manifest cannot be read stays visible with an
"unknown" version/source — a broken plugin is reported, never
silently skipped.

Ordering is deterministic (name ascending). Informational: exit 0
always, also when nothing is discovered or installed.

--json emits the same report as one machine-readable document
(schema eka-plugin-list-v1, deterministic key order).`,
		Example: `  eka plugin list         list plugins with the trust tier
  eka plugin list --json  emit the machine-readable report`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			asJSON, err := cmd.Flags().GetBool("json")
			if err != nil {
				return fmt.Errorf("plugin list failed: %w", err) // Exit 2: internal.
			}
			return runPluginList(cmd, asJSON)
		},
	}
	cmd.Flags().Bool("json", false, "emit the deterministic machine report (schema eka-plugin-list-v1)")
	return cmd
}

// pluginListEntry is one row of the plugin list report: a unique
// plugin name with its discovered executable, the manifest-derived
// version and source ("" = unreadable/broken manifest — the entry
// stays visible) and the trust tier.
type pluginListEntry struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	Version   string `json:"version"`
	Source    string `json:"source"`
	Trust     string `json:"trust"`
	Installed bool   `json:"installed"`
}

// pluginListDocument is the `eka plugin list --json` report (schema
// eka-plugin-list-v1): the sorted entries with the deterministic
// field order.
type pluginListDocument struct {
	Schema  string            `json:"schema"`
	Plugins []pluginListEntry `json:"plugins"`
}

// runPluginList renders the plugin list report: the deterministic
// table on stdout (or the eka-plugin-list-v1 JSON document with
// --json). Exit 0 always — an empty or broken plugin set is a
// visible report, never a failure.
func runPluginList(cmd *cobra.Command, asJSON bool) error {
	s := styleFor(cmd)
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("plugin list failed: %w", err) // Exit 2: internal.
	}
	entries := collectPluginListEntries(home)
	if asJSON {
		out, err := json.MarshalIndent(pluginListDocument{Schema: "eka-plugin-list-v1", Plugins: entries}, "", "  ")
		if err != nil {
			return fmt.Errorf("plugin list failed: %w", err) // Exit 2: internal.
		}
		out = append(out, '\n')
		if _, err := s.W.Write(out); err != nil {
			return fmt.Errorf("plugin list failed: %w", err) // Exit 2: internal.
		}
		return nil
	}
	renderPluginListTable(s, entries)
	return nil
}

// collectPluginListEntries gathers the plugin list report: the
// discovered plugins (PATH + plugin dirs, via plugin.Discover) plus
// any plugin-dir "eka-*" entry Discover skipped, one entry per
// unique name sorted by name. The manifest is read bounded (a hung
// plugin is reported unknown instead of wedging the list); a broken
// manifest keeps the entry visible.
func collectPluginListEntries(home string) []pluginListEntry {
	dir := plugin.PluginDir(home)
	plugins, _ := plugin.Discover(home)
	seen := map[string]bool{}
	entries := make([]pluginListEntry, 0, len(plugins))
	add := func(exe string) {
		name := strings.TrimPrefix(filepath.Base(exe), "eka-")
		if name == "" || name == "eka" || seen[name] {
			return
		}
		seen[name] = true
		m, err := readPluginManifest(exe)
		var version, source string
		if err == nil {
			version, source = m.Version, m.Source
		}
		entries = append(entries, pluginListEntry{
			Name:      name,
			Path:      exe,
			Version:   version,
			Source:    source,
			Trust:     pluginTrustTier(name),
			Installed: pluginInstalledIn(dir, name),
		})
	}
	for _, p := range plugins {
		add(p.Exe)
	}
	// Plugin-dir entries (the installed set) Discover missed. Discover
	// scans the plugin dir itself, so this is defensive: a name skipped
	// there stays visible here — the installed set is never silently
	// dropped from the report.
	if dir != "" {
		if entriesDir, err := os.ReadDir(dir); err == nil {
			for _, e := range entriesDir {
				if e.IsDir() || !strings.HasPrefix(e.Name(), "eka-") {
					continue
				}
				add(filepath.Join(dir, e.Name()))
			}
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return entries
}

// readPluginManifest runs a plugin's manifest, bounded by
// pluginSmokeCheckTimeout — a hung plugin is reported unknown
// instead of wedging the list. Errors are swallowed by design: the
// list reports the entry with an unknown version/source.
func readPluginManifest(exe string) (plugin.Manifest, error) {
	ctx, cancel := context.WithTimeout(context.Background(), pluginSmokeCheckTimeout)
	defer cancel()
	return (plugin.Plugin{Exe: exe}).ManifestContext(ctx)
}

// pluginTrustTier labels the plugin's trust tier from the built-in
// registry: "official" for registered plugins, "third-party"
// otherwise. The full consent model is a later work item
// (sto:plugin-trust-model) — here the tier is only a label.
func pluginTrustTier(name string) string {
	if plugin.OfficialRegistry.IsOfficial(name) {
		return "official"
	}
	return "third-party"
}

// pluginInstalledIn reports whether the plugin is installed in the
// plugin directory: the eka-<name> executable there (the .exe form
// on windows mirrors the asset suffix).
func pluginInstalledIn(dir, name string) bool {
	if dir == "" {
		return false
	}
	if fileExists(filepath.Join(dir, "eka-"+name)) {
		return true
	}
	return runtime.GOOS == "windows" && fileExists(filepath.Join(dir, "eka-"+name+".exe"))
}

// renderPluginListTable renders the deterministic column report: the
// accent heading, the aligned table (name, version, source, trust,
// installed, path — widths derived from the data, stable for a given
// plugin set), the summary counts. An empty set renders the
// informative line with the install hint.
func renderPluginListTable(s *ui.Style, entries []pluginListEntry) {
	fmt.Fprintln(s.W, s.Accent("Plugin"))
	if len(entries) == 0 {
		fmt.Fprintf(s.W, "\n%s\n", s.Info("No plugins discovered or installed. Run 'eka plugin install <name>' to install one."))
		return
	}
	tbl := ui.NewTable(s, "NAME", "VERSION", "SOURCE", "TRUST", "INSTALLED", "PATH")
	for _, e := range entries {
		version, source := e.Version, e.Source
		if version == "" {
			version = "unknown"
		}
		if source == "" {
			source = "unknown"
		}
		installed := "no"
		if e.Installed {
			installed = "yes"
		}
		tbl.AddRow([]string{e.Name, version, source, e.Trust, installed, e.Path}, nil)
	}
	tbl.Render()
	installed := 0
	for _, e := range entries {
		if e.Installed {
			installed++
		}
	}
	ui.NewSummary(s).
		Add("Plugins", plural(len(entries), "plugin", "plugins")).
		Add("Installed", plural(installed, "plugin", "plugins")).
		Render()
}

// newPluginRemoveCommand builds `eka plugin remove <name>`: deletes
// the installed binary from the plugin directory, together with any
// stale replacement marker (.old) an update left behind.
func newPluginRemoveCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <name>",
		Short: "Remove an installed EKA plugin",
		Long: `Remove an installed plugin: the "eka-<name>" executable (the .exe
form on windows) is deleted from the plugin directory ($EKA_PLUGIN_DIR
or ~/.eka/plugins), together with any stale eka-<name>.old marker a
previous update left behind.

A plugin that is not installed refuses with a clear error. Removal
is direct and non-interactive — the interactive consent UX belongs
to the later trust-model work item (sto:plugin-trust-model).`,
		Example: `  eka plugin remove mcp   remove the installed eka-mcp plugin`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			r, err := newPluginRemoveRunner()
			if err != nil {
				return err // Exit 2: internal.
			}
			return r.run(cmd, args[0])
		},
	}
}

// pluginRemoveRunner carries the injectable execution context of one
// `eka plugin remove` run: the plugin directory and the platform
// (the installed binary mirrors the asset suffix — .exe on windows).
type pluginRemoveRunner struct {
	pluginDir string
	goos      string
}

// newPluginRemoveRunner assembles the production runner: the plugin
// directory ($EKA_PLUGIN_DIR or <home>/.eka/plugins) and the build
// platform.
func newPluginRemoveRunner() (*pluginRemoveRunner, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("cannot resolve the home directory: %w", err)
	}
	dir := plugin.PluginDir(home)
	if dir == "" {
		// Defense in depth (mirrors the install runner): never fall
		// back to the current directory. With a resolved home this is
		// unreachable.
		return nil, errors.New("cannot resolve the plugin directory (set EKA_PLUGIN_DIR)")
	}
	return &pluginRemoveRunner{pluginDir: dir, goos: runtime.GOOS}, nil
}

// run executes one removal: refuse when not installed, delete the
// binary and its stale .old marker, print the confirmation.
func (r *pluginRemoveRunner) run(cmd *cobra.Command, name string) error {
	s := styleFor(cmd)
	sm := ui.NewSummary(s)
	target := filepath.Join(r.pluginDir, "eka-"+name)
	if r.goos == "windows" {
		target += ".exe"
	}
	if !fileExists(target) {
		return refuse(cmd, "plugin remove refused: plugin %q is not installed (no %s in %s)",
			name, filepath.Base(target), r.pluginDir)
	}
	// Stale marker cleanup: the atomic replace of `eka plugin update`
	// preserves the replaced binary as <target>.old until the smoke
	// check passes; a leftover .old is debris and must not survive the
	// removal. Best-effort: never a refusal.
	os.Remove(target + ".old")
	if err := os.Remove(target); err != nil {
		return refuse(cmd, "plugin remove refused: cannot remove %s: %s", target, err)
	}
	fmt.Fprintf(s.W, "%s\n", s.Success(ui.IconDone+" removed: "+target))
	sm.Add("Plugin", name)
	sm.Add("Removed", target)
	sm.Render()
	return nil
}

// newPluginUpdateCommand builds `eka plugin update [name|--all]`:
// re-downloads the latest verified release of an installed official
// plugin (or of every installed official plugin with --all) through
// the install flow's shared download+checksum+smoke-check path.
func newPluginUpdateCommand() *cobra.Command {
	f := &pluginUpdateFlags{}
	cmd := &cobra.Command{
		Use:   "update [name]",
		Short: "Update an installed EKA plugin to its latest release",
		Long: `Update an installed plugin to its latest release: the release is
resolved and the binary downloaded through the same verified path as
eka plugin install (registry resolution, latest release, SHA-256
verification against the tag-pinned SHA256SUMS.txt, manifest smoke
check) and replaced atomically — the old binary is preserved as
eka-<name>.old during the swap and a new binary that fails the smoke
check restores the old one.

The previous and the new version are printed. Unknown plugin names
refuse with the list of official plugins; a plugin that is not
installed refuses with the install hint.

  --all  update every installed official plugin (the registry is the
         filter — third-party plugins are never touched); each
         plugin is reported, and the run exits 1 when at least one
         update failed. Nothing installed is an informative empty
         result (exit 0).`,
		Example: `  eka plugin update mcp    update eka-mcp to its latest release
  eka plugin update --all  update every installed official plugin`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			r, err := newPluginInstallRunner()
			if err != nil {
				return err // Exit 2: internal.
			}
			if f.all {
				if len(args) > 0 {
					return errors.New("plugin update: --all takes no plugin name") // Exit 2: usage.
				}
				return r.runUpdateAll(cmd)
			}
			if len(args) == 0 {
				return errors.New("plugin update: missing plugin name (pass --all to update every installed official plugin)") // Exit 2: usage.
			}
			return r.runUpdate(cmd, args[0])
		},
	}
	cmd.Flags().BoolVar(&f.all, "all", false, "update every installed official plugin")
	return cmd
}

// pluginUpdateFlags carries the flag set of the plugin update
// command.
type pluginUpdateFlags struct {
	all bool
}

// runUpdate executes one plugin update: resolve the name through the
// registry, read the installed version, download the latest verified
// release through the install flow's shared path, replace the binary
// atomically (the old binary is preserved as <target>.old until the
// smoke check passes — a broken new binary restores the old one) and
// print the old → new version.
func (r *pluginInstallRunner) runUpdate(cmd *cobra.Command, name string) error {
	s := styleFor(cmd)
	sm := ui.NewSummary(s)

	repo, ok := r.resolve(name)
	if !ok {
		return refuse(cmd, "plugin update refused: unknown plugin %q — official plugins: %s",
			name, strings.Join(plugin.OfficialRegistry.Names(), ", "))
	}
	asset, err := platformAssetName("eka-"+name, r.goos, r.goarch)
	if err != nil {
		return refuse(cmd, "plugin update refused: %s", err)
	}
	target := r.installTarget(name)
	if !fileExists(target) {
		return refuse(cmd, "plugin update refused: plugin %q is not installed (no %s in %s); install it with: eka plugin install %s",
			name, filepath.Base(target), r.pluginDir, name)
	}
	oldVersion := r.installedVersion(target)
	tag, size, want, err := r.resolveLatestRelease(repo, asset)
	if err != nil {
		return refuse(cmd, "plugin update refused: %s", err)
	}
	r.renderUpdateHeader(s, name, repo, asset, tag, oldVersion)
	tmp, err := r.downloadVerified(s, repo, tag, asset, size, want)
	if err != nil {
		return refuse(cmd, "plugin update refused: %s", err)
	}
	// Leftover cleanup: after a successful rename the temp path no
	// longer exists and the deferred removal is a no-op.
	defer os.Remove(tmp)

	// Atomic replacement (the update command's rename dance, which
	// also works on Windows): the old binary moves to <target>.old and
	// stays there until the smoke check passes — any failure restores
	// it; if even the restore fails, the old binary is preserved as
	// <target>.old and the refusal says so (the recovery path is never
	// silent).
	old := target + ".old"
	os.Remove(old) // best-effort: a leftover .old must not block the dance
	if err := os.Rename(target, old); err != nil {
		return refuse(cmd, "plugin update refused: cannot replace %s: %s", target, err)
	}
	if err := os.Rename(tmp, target); err != nil {
		if rerr := os.Rename(old, target); rerr != nil {
			return refuse(cmd, "plugin update refused: cannot replace %s: %s; the old binary is preserved at %s — restore it with: mv %s %s",
				target, err, old, old, target)
		}
		return refuse(cmd, "plugin update refused: cannot replace %s: %s", target, err)
	}
	if err := r.smokeCheck(name, target); err != nil {
		if rerr := os.Rename(old, target); rerr != nil {
			fmt.Fprintf(s.W, "%s\n", s.Warning(fmt.Sprintf("warning: cannot restore the previous plugin at %s (preserved at %s): %s", target, old, rerr)))
		}
		return refuse(cmd, "plugin update refused: %s", err)
	}
	os.Remove(old) // debris cleanup; the update itself succeeded

	fmt.Fprintf(s.W, "%s\n", s.Success(ui.IconDone+" updated: "+target))
	sm.Add("Plugin", name)
	sm.Add("Repo", repo.String())
	sm.Add("Version", oldVersion+" → "+tag)
	sm.Add("Updated", target)
	sm.Render()
	return nil
}

// renderUpdateHeader prints the context header of an update (the
// single-operation interaction model): the accent heading, the
// Plugin/Repo/Version/Current/Asset labels and the pipeline line.
func (r *pluginInstallRunner) renderUpdateHeader(s *ui.Style, name string, repo plugin.Repo, asset, tag, current string) {
	fmt.Fprintln(s.W)
	fmt.Fprintln(s.W, s.Accent("Update"))
	fmt.Fprintf(s.W, "  %-7s   %s\n", s.Info("Plugin"), name)
	fmt.Fprintf(s.W, "  %-7s   %s\n", s.Info("Repo"), repo.String())
	fmt.Fprintf(s.W, "  %-7s   %s\n", s.Info("Version"), tag)
	fmt.Fprintf(s.W, "  %-7s   %s\n", s.Info("Current"), current)
	fmt.Fprintf(s.W, "  %-7s   %s\n", s.Info("Asset"), asset)
	fmt.Fprintf(s.W, "  %s\n", s.Accent("↓ Update"))
}

// installedVersion reads the installed binary's manifest version;
// "unknown" when the manifest is unreadable or carries no version (a
// broken plugin stays updateable — the new binary is verified by the
// smoke check anyway). Bounded: a hung plugin cannot wedge the
// update.
func (r *pluginInstallRunner) installedVersion(target string) string {
	ctx, cancel := context.WithTimeout(context.Background(), pluginSmokeCheckTimeout)
	defer cancel()
	m, err := (plugin.Plugin{Exe: target}).ManifestContext(ctx)
	if err != nil || m.Version == "" {
		return "unknown"
	}
	return m.Version
}

// runUpdateAll updates every installed official plugin, in sorted
// name order. A plugin whose update fails is reported (each refusal
// renders its own line) and the remaining plugins still update; the
// run exits 1 when at least one update failed. Nothing installed is
// an informative empty result, exit 0.
func (r *pluginInstallRunner) runUpdateAll(cmd *cobra.Command) error {
	s := styleFor(cmd)
	sm := ui.NewSummary(s)
	names := r.installedOfficialNames()
	if len(names) == 0 {
		sm.Add("Status", "no installed official plugins to update")
		sm.Add("Hint", "install one with: eka plugin install <name>")
		sm.Render()
		return nil
	}
	failed := false
	for _, name := range names {
		if err := r.runUpdate(cmd, name); err != nil {
			failed = true
			// The refusal already rendered its own "eka: ..." line.
		}
	}
	if failed {
		return &exitError{code: exitFail}
	}
	return nil
}

// installedOfficialNames lists the plugin-directory entries that are
// installed OFFICIAL plugins: "eka-<name>" executables (not the CLI
// itself) whose name resolves through the official registry, sorted.
// Only official names are updateable — the registry is the filter
// (third-party plugins are never touched by --all).
func (r *pluginInstallRunner) installedOfficialNames() []string {
	entries, err := os.ReadDir(r.pluginDir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), "eka-") {
			continue
		}
		name := strings.TrimPrefix(e.Name(), "eka-")
		if r.goos == "windows" {
			name = strings.TrimSuffix(name, ".exe")
		}
		if name == "" || name == "eka" || !plugin.OfficialRegistry.IsOfficial(name) {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
