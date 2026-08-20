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
// "third-party" — the same classification the install consent model
// (sto:plugin-trust-model) enforces. list only LABELS the tier; the
// consent itself happens at install/update time. update --all only
// ever touches official plugins (the registry is the filter); a NAMED
// third-party update resolves the repository from the installed
// binary's manifest source and goes through the same consent flow.
//
// Exit codes:
//
//	list    0  always (an empty or broken plugin set is a visible,
//	           deterministic report, never a failure)
//	remove  0  removed
//	        1  refusal (invalid plugin name, plugin not installed)
//	        2  usage or internal error (filesystem failure while
//	           removing)
//	update  0  updated (also: --all with nothing installed, or --all
//	           with every update successful)
//	        1  refusal (invalid plugin name, unknown plugin, plugin
//	           not installed, unresolved release, checksum mismatch,
//	           broken new manifest, source swap, consent not given),
//	           or --all with at least one failed update
//	        2  usage or internal error (missing name without --all,
//	           --all with a name, binary replacement failure,
//	           unreadable plugin directory)

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
	"github.com/maleolabs/eka-core/plugin"
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
	entries := collectPluginListEntries(home, runtime.GOOS)
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
// unique name sorted by name. goos is the target platform (the
// installed binary mirrors the asset suffix — .exe on windows). The
// manifest is read bounded (a hung plugin is reported unknown instead
// of wedging the list); a broken manifest keeps the entry visible.
func collectPluginListEntries(home, goos string) []pluginListEntry {
	dir := plugin.PluginDir(home)
	plugins, _ := plugin.Discover(home)
	seen := map[string]bool{}
	entries := make([]pluginListEntry, 0, len(plugins))
	add := func(exe string) {
		name := strings.TrimPrefix(filepath.Base(exe), "eka-")
		if goos == "windows" {
			name = strings.TrimSuffix(name, ".exe") // mirrors the asset suffix
		}
		// A trailing ".old" is the preserved-old-binary marker of the
		// update command's atomic replace — debris, never a plugin.
		if name == "" || name == "eka" || strings.HasSuffix(name, ".old") || seen[name] {
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
			Installed: pluginInstalledIn(dir, name, goos),
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
// registry: "official" for registered plugins (full-trust, no prompt
// at install), "third-party" otherwise (explicit consent required).
// It is the label side of the two-tier trust model
// (sto:plugin-trust-model); install/update enforce the consent.
func pluginTrustTier(name string) string {
	if plugin.OfficialRegistry.IsOfficial(name) {
		return "official"
	}
	return "third-party"
}

// pluginInstalledIn reports whether the plugin is installed in the
// plugin directory: the eka-<name> executable there (the .exe form on
// windows mirrors the asset suffix).
func pluginInstalledIn(dir, name, goos string) bool {
	if dir == "" {
		return false
	}
	if fileExists(filepath.Join(dir, "eka-"+name)) {
		return true
	}
	return goos == "windows" && fileExists(filepath.Join(dir, "eka-"+name+".exe"))
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
is direct and non-interactive; it never consults the trust model.`,
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

// run executes one removal: validate the name (a plugin name is a
// single eka-<name> path segment — never a traversal), refuse when not
// installed (exit 1, the domain refusal), delete the binary and its
// stale .old marker, print the confirmation. A filesystem failure
// while removing is an internal error (exit 2, plain error) — exit 1
// is reserved for the domain refusals.
func (r *pluginRemoveRunner) run(cmd *cobra.Command, name string) error {
	s := styleFor(cmd)
	sm := ui.NewSummary(s)
	if !validPluginName(name) {
		return refuse(cmd, "plugin remove refused: invalid plugin name %q (want a single eka-<name> identifier)", name)
	}
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
	// The checksum sidecar travels with the binary (best-effort: debris
	// cleanup, never a refusal).
	removePluginChecksum(r.pluginDir, name)
	if err := os.Remove(target); err != nil {
		return fmt.Errorf("plugin remove failed: cannot remove %s: %w", target, err) // Exit 2: internal.
	}
	fmt.Fprintf(s.W, "%s\n", s.Success(ui.IconDone+" removed: "+target))
	sm.Add("Plugin", name)
	sm.Add("Removed", target)
	sm.Render()
	return nil
}

// newPluginUpdateCommand builds `eka plugin update [name|--all]
// [--yes]`: re-downloads the latest verified release of an installed
// plugin (or of every installed official plugin with --all) through
// the install flow's shared download+checksum+smoke-check path.
func newPluginUpdateCommand() *cobra.Command {
	f := &pluginUpdateFlags{}
	cmd := &cobra.Command{
		Use:   "update [name]",
		Short: "Update an installed EKA plugin to its latest release",
		Long: `Update an installed plugin to its latest release: the release is
resolved and the binary downloaded through the same verified path as
eka plugin install (registry resolution or the installed manifest
source, latest release, SHA-256 verification against the tag-pinned
SHA256SUMS.txt, manifest inspection) and replaced atomically — the
old binary is preserved as eka-<name>.old during the swap.

The previous and the new version are printed. Unknown plugin names
refuse with the list of official plugins; a plugin that is not
installed refuses with the install hint.

A named update of a THIRD-PARTY plugin (one not listed in the
registry) resolves the repository from the installed binary's
manifest source and applies the same consent flow as the install:
the source and capabilities are shown and explicit consent is
required — --yes consents non-interactively, and outside a terminal
--yes is required (the CLI never auto-consents silently). If the new
release's manifest claims a different source than the installed
binary, the update refuses (fail-closed).

Security boundary: the downloaded binary is executed once to read its
manifest BEFORE the consent decision. Plugin subprocesses run with a
minimal environment (PATH, HOME, EKA_PLUGIN_DIR) — never with the
CLI's secrets — and the staged file is 0700 until consent; it
becomes the 0755 installed executable only after finalize.

  --all  update every installed official plugin (the registry is the
         filter — third-party plugins are never touched); each
         plugin is reported, and the run exits 1 when at least one
         update failed. Nothing installed is an informative empty
         result (exit 0).`,
		Example: `  eka plugin update mcp            update eka-mcp to its latest release
  eka plugin update mybot --yes    update a third-party plugin with consent
  eka plugin update --all          update every installed official plugin`,
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
			return r.runUpdate(cmd, args[0], f.yes)
		},
	}
	cmd.Flags().BoolVar(&f.all, "all", false, "update every installed official plugin")
	cmd.Flags().BoolVar(&f.yes, "yes", false, "consent to a third-party update without the prompt")
	return cmd
}

// pluginUpdateFlags carries the flag set of the plugin update
// command.
type pluginUpdateFlags struct {
	all bool
	// yes consents to a third-party update without the interactive
	// prompt (official updates never prompt).
	yes bool
}

// runUpdate executes one plugin update: validate the name, resolve the
// source repository (the registry for an official name, the installed
// manifest source for a third-party one), read the installed version,
// download the latest verified release through the install flow's
// shared path, inspect the staged binary (a broken download refuses
// with the OLD binary untouched), refuse a source swap, obtain explicit
// consent for third-party plugins and replace the binary atomically
// (the old binary is preserved as <target>.old during the swap).
func (r *pluginInstallRunner) runUpdate(cmd *cobra.Command, name string, yes bool) error {
	s := styleFor(cmd)
	sm := ui.NewSummary(s)

	// Path-traversal guard: a plugin name is a single eka-<name> path
	// segment — anything else must never reach filepath.Join(pluginDir,
	// "eka-"+name). Refused before any network or filesystem use.
	if !validPluginName(name) {
		return refuse(cmd, "plugin update refused: invalid plugin name %q (want a single eka-<name> identifier)", name)
	}

	repo, thirdParty, err := r.resolveUpdateRepo(cmd, name)
	if err != nil {
		return err
	}

	// Determinism gate (mirrors `eka update`): a non-terminal run
	// without --yes cannot consent — refuse BEFORE any download or
	// staged execution (fail-closed, never auto-consent). The
	// interactive path is unchanged: source and capabilities are still
	// shown before the prompt.
	if thirdParty && !yes && !r.canPrompt(cmd, s) {
		return refuse(cmd, "plugin update refused: %q is a third-party plugin and requires explicit consent; pass --yes to consent non-interactively", name)
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
	// The release is resolved BEFORE the old binary is executed for
	// its version: a network failure refuses fast (no 30s hang when
	// the old plugin hangs AND the network is down), and only a
	// resolvable release proceeds to touch the installed binary.
	tag, size, want, err := r.resolveLatestRelease(repo, asset)
	if err != nil {
		return refuse(cmd, "plugin update refused: %s", err)
	}
	oldVersion := r.installedVersion(target)
	r.renderUpdateHeader(s, name, repo, asset, tag, oldVersion, thirdParty)
	tmp, err := r.downloadVerified(s, repo, tag, asset, size, want)
	if err != nil {
		return refuse(cmd, "plugin update refused: %s", err)
	}
	// Leftover cleanup: after a successful rename the temp path no
	// longer exists and the deferred removal is a no-op.
	defer os.Remove(tmp)

	// The STAGED binary is inspected BEFORE the atomic swap: a broken
	// or mismatched download refuses with the old binary untouched —
	// the rename dance no longer needs a smoke-check-failure restore
	// path. The manifest also supplies the capabilities/source the
	// third-party consent surfaces.
	m, err := r.inspectStaged(name, tmp)
	if err != nil {
		return refuse(cmd, "plugin update refused: %s", err)
	}

	// Source-swap refusal: the new binary's manifest must agree with
	// the repository the update resolved (the registry repo for an
	// official plugin, the installed manifest's source for a
	// third-party one). A release that claims a different source is
	// refused fail-closed — the old binary stays.
	if claimed, err := parsePluginSource(m.Source); err == nil && claimed != repo {
		return refuse(cmd, "plugin update refused: the new release of %q reports source %s, but the update resolved %s (source swap refused)", name, claimed, repo)
	}

	if thirdParty {
		r.renderThirdPartyInfo(s, repo, m, target)
		if !yes {
			ok, err := r.consent(cmd, s, name, "update")
			if err != nil {
				return err // Exit 2: internal (the prompt could not be read).
			}
			if !ok {
				return refuse(cmd, "plugin update refused: consent to update the third-party plugin %q declined — the existing installation is unchanged", name)
			}
		}
	}

	// Atomic replacement (the update command's rename dance, which
	// also works on Windows): the old binary moves to <target>.old and
	// stays there until the new one is in place. A rename failure is
	// an internal filesystem error (exit 2, plain error) — exit 1 is
	// reserved for the domain refusals above.
	old := target + ".old"
	os.Remove(old) // best-effort: a leftover .old must not block the dance
	if err := os.Rename(target, old); err != nil {
		return fmt.Errorf("plugin update failed: cannot replace %s: %w%s", target, err, r.windowsInstallHint()) // Exit 2: internal.
	}
	if err := os.Rename(tmp, target); err != nil {
		if rerr := os.Rename(old, target); rerr != nil {
			return fmt.Errorf("plugin update failed: cannot replace %s: %w; the old binary is preserved at %s — restore it with: mv %s %s%s",
				target, err, old, old, target, r.windowsInstallHint()) // Exit 2: internal.
		}
		return fmt.Errorf("plugin update failed: cannot replace %s: %w%s", target, err, r.windowsInstallHint()) // Exit 2: internal.
	}
	// Finalize the update: the staged file was 0700 during inspection;
	// the installed binary is 0755.
	if err := os.Chmod(target, 0o755); err != nil {
		return fmt.Errorf("plugin update failed: cannot make %s executable: %w%s", target, err, r.windowsInstallHint()) // Exit 2: internal.
	}
	// The dispatch-time verification record (G2 anti-TOCTOU, ADR-031):
	// the new binary's SHA-256 replaces the sidecar, so dispatch
	// re-verifies against the NEW checksum (the registration cache is
	// invalidated by the new binary identity).
	sum, err := sha256File(target)
	if err != nil {
		return fmt.Errorf("plugin update failed: cannot hash the installed %s: %w%s", target, err, r.windowsInstallHint()) // Exit 2: internal.
	}
	if err := writePluginChecksum(r.pluginDir, name, sum); err != nil {
		return fmt.Errorf("plugin update failed: cannot record the checksum of %s: %w%s", target, err, r.windowsInstallHint()) // Exit 2: internal.
	}
	os.Remove(old) // debris cleanup; the update itself succeeded

	fmt.Fprintf(s.W, "%s\n", s.Success(ui.IconDone+" updated: "+target))
	sm.Add("Plugin", name)
	sm.Add("Repo", repo.String())
	sm.Add("Version", sanitizeTerminal(oldVersion+" → "+tag))
	if thirdParty {
		sm.Add("Trust", "third-party (consent given)")
	}
	sm.Add("Updated", target)
	sm.Render()
	return nil
}

// resolveUpdateRepo classifies the update and resolves its source
// repository: an official name resolves through the registry; a
// third-party name's repository is read from the INSTALLED binary's
// manifest source (the source recorded at install time). A name that
// is neither registry-listed nor resolvable from an installed
// manifest refuses with the official list and the install hint.
func (r *pluginInstallRunner) resolveUpdateRepo(cmd *cobra.Command, name string) (plugin.Repo, bool, error) {
	if repo, ok := r.resolve(name); ok {
		return repo, false, nil // official: full-trust, no consent.
	}
	repo, err := r.repoFromInstalled(name)
	if err != nil {
		return plugin.Repo{}, true, refuse(cmd, "plugin update refused: %s; official plugins: %s",
			err, strings.Join(plugin.OfficialRegistry.Names(), ", "))
	}
	return repo, true, nil // third-party: consent flow applies.
}

// repoFromInstalled reads the installed binary's manifest source
// ("github.com/owner/name") and derives the source repository. An
// unreadable manifest or a source without a resolvable repository
// (legacy plugins) is a refusal with the reinstall hint.
func (r *pluginInstallRunner) repoFromInstalled(name string) (plugin.Repo, error) {
	target := r.installTarget(name)
	m, err := readPluginManifest(target)
	if err != nil {
		return plugin.Repo{}, fmt.Errorf("plugin %q is not registry-listed and its installed manifest is unreadable (reinstall it with --repo owner/name)", name)
	}
	repo, err := parsePluginSource(m.Source)
	if err != nil {
		return plugin.Repo{}, fmt.Errorf("plugin %q is not registry-listed and its manifest source %q is not a resolvable repository: %v (reinstall it with --repo owner/name)", name, m.Source, err)
	}
	return repo, nil
}

// parsePluginSource derives the source repository from a manifest
// Source value: a github.com repository URL ("https://github.com/
// owner/name" or "github.com/owner/name"). Only the github.com host is
// accepted — the CLI only ever resolves plugin sources from GitHub —
// and the owner/name segments are restricted to the safe charset
// (validRepoSegment). Any other shape is a refusal.
func parsePluginSource(source string) (plugin.Repo, error) {
	s := strings.TrimSpace(source)
	s = strings.TrimSuffix(s, "/")
	s = strings.TrimPrefix(s, "http://")
	s = strings.TrimPrefix(s, "https://")
	const host = "github.com/"
	if !strings.HasPrefix(s, host) {
		return plugin.Repo{}, fmt.Errorf("source %q is not a github.com repository URL", source)
	}
	parts := strings.Split(strings.TrimPrefix(s, host), "/")
	if len(parts) != 2 || !validRepoSegment(parts[0]) || !validRepoSegment(parts[1]) {
		return plugin.Repo{}, fmt.Errorf("source %q is not a github.com repository URL", source)
	}
	return plugin.Repo{Owner: parts[0], Name: parts[1]}, nil
}

// renderUpdateHeader prints the context header of an update (the
// single-operation interaction model): the accent heading, the
// Plugin/Repo/Version/Current/Asset labels and the pipeline line. A
// third-party update carries a "Trust third-party" row — the tier is
// visible before any download. The release tag and the current version
// are attacker-controlled (GitHub metadata / installed manifest) and
// are rendered sanitized.
func (r *pluginInstallRunner) renderUpdateHeader(s *ui.Style, name string, repo plugin.Repo, asset, tag, current string, thirdParty bool) {
	fmt.Fprintln(s.W)
	fmt.Fprintln(s.W, s.Accent("Update"))
	fmt.Fprintf(s.W, "  %-7s   %s\n", s.Info("Plugin"), name)
	fmt.Fprintf(s.W, "  %-7s   %s\n", s.Info("Repo"), repo.String())
	fmt.Fprintf(s.W, "  %-7s   %s\n", s.Info("Version"), sanitizeTerminal(tag))
	fmt.Fprintf(s.W, "  %-7s   %s\n", s.Info("Current"), sanitizeTerminal(current))
	fmt.Fprintf(s.W, "  %-7s   %s\n", s.Info("Asset"), asset)
	if thirdParty {
		fmt.Fprintf(s.W, "  %-7s   %s\n", s.Info("Trust"), "third-party")
	}
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
// run exits 1 when at least one update failed. Nothing installed (a
// missing plugin directory included) is an informative empty result,
// exit 0; an unreadable plugin directory is an internal error (exit
// 2).
func (r *pluginInstallRunner) runUpdateAll(cmd *cobra.Command) error {
	s := styleFor(cmd)
	sm := ui.NewSummary(s)
	names, err := r.installedOfficialNames()
	if err != nil {
		return fmt.Errorf("plugin update failed: %w", err) // Exit 2: internal.
	}
	if len(names) == 0 {
		sm.Add("Status", "no installed official plugins to update")
		sm.Add("Hint", "install one with: eka plugin install <name>")
		sm.Render()
		return nil
	}
	failed := false
	for _, name := range names {
		// --all only ever touches official plugins (installedOfficialNames
		// is the registry filter), which never prompt — no --yes needed.
		if err := r.runUpdate(cmd, name, false); err != nil {
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
// (third-party plugins are never touched by --all). A missing plugin
// directory is an empty set (nothing installed); any other read
// failure is an error.
func (r *pluginInstallRunner) installedOfficialNames() ([]string, error) {
	entries, err := os.ReadDir(r.pluginDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("cannot read the plugin directory %s: %w", r.pluginDir, err)
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
	return names, nil
}
