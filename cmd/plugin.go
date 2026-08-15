package cmd

// This file implements `eka plugin` and `eka plugin install`:
// installation of EKA plugins from their GitHub releases under the
// two-tier trust model (sto:plugin-trust-model).
//
// The plugin command is distribution convenience, like eka update: it
// never touches the EKA workspace or the canonical store.
//
// Trust classification: a plugin name that resolves through the
// built-in registry (plugin.OfficialRegistry — the maleolabs-maintained
// list) is OFFICIAL and full-trust: it installs without a prompt. Every
// other source is THIRD-PARTY and requires explicit consent after its
// source and capabilities are surfaced. `--repo <owner/name>` pins an
// arbitrary GitHub repository; with it the name is third-party by
// definition (the registry is bypassed, even for a maleolabs repo).
//
// Install flow (fail-closed, both tiers):
//
//  1. `eka plugin install <name> [--repo owner/name]` resolves the
//     source repository: the registry for an official name, the
//     --repo value for a third-party one. An unknown name refuses with
//     the list of known plugins before any network access.
//  2. The latest release of the plugin's repository is resolved via the
//     GitHub REST API (a minimal, well-formed User-Agent; a rate-limited
//     or unavailable API refuses with a clear message).
//  3. The release asset matching the platform binary naming contract
//     (eka-<name>-<os>-<arch>[.exe]) and SHA256SUMS.txt are fetched from
//     the SAME tag-pinned release, so the checksum and the asset always
//     come from one release.
//  4. The downloaded binary's SHA-256 is verified against the checksum
//     entry. ANY mismatch, missing entry or malformed entry refuses and
//     removes the partial download — never install unverified. The
//     checksum gate applies to BOTH tiers.
//  5. The STAGED binary's manifest is inspected before anything moves
//     into place (bounded by pluginSmokeCheckTimeout — a hung plugin
//     refuses): it must parse into plugin.Manifest with a matching
//     name. This verifies the download AND supplies the capabilities
//     the third-party consent surfaces.
//  6. A third-party plugin prints its source and capabilities and asks
//     for explicit consent (--yes consents non-interactively; outside a
//     terminal --yes is required — the CLI never auto-consents
//     silently). Declining removes the staged download and refuses.
//  7. The verified, consented binary is installed as eka-<name> (0755;
//     .exe on windows) into the plugin directory ($EKA_PLUGIN_DIR or
//     ~/.eka/plugins), matching the Discover "eka-*" executable
//     contract.
//  8. Re-install overwrites the previous binary cleanly (with a printed
//     notice).
//
// Hardening (review findings): all network reads are bounded
// (SHA256SUMS.txt at 1 MiB, the asset at 1 GiB via Content-Length
// agreement AND a streaming cap), the manifest subprocess output is
// capped at 1 MiB, redirects are HTTPS-only and capped at 10, and a
// GH_TOKEN is sent as Authorization: Bearer when set (raises the API
// rate limit).
//
// Exit codes:
//
//	0  installed
//	1  refusal (unknown plugin, unresolved release, checksum
//	   mismatch/missing/malformed, broken manifest, unsupported
//	   platform, consent not given)
//	2  usage or internal error
//
// The network endpoints are package-level vars (not consts) so the
// same-package tests can point them at an httptest server; production
// always uses the pinned GitHub endpoints.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/maleolabs/eka-cli/cmd/ui"
	"github.com/maleolabs/eka-cli/plugin"
	"github.com/spf13/cobra"
)

var (
	// pluginAPIBase is the GitHub REST API root; pluginDownloadRoot the
	// download root (the /releases/latest/download redirect lives under
	// it). The repository and the /releases path are appended per
	// resolved plugin.
	pluginAPIBase      = "https://api.github.com"
	pluginDownloadRoot = "https://github.com"
	// pluginClient bounds the connection phases (dial, TLS handshake,
	// response headers — a dead host refuses fast) and the redirect
	// chain (pluginCheckRedirect: HTTPS-only + capped). Per-request
	// deadlines (pluginRequestTimeout for metadata, pluginDownloadTimeout
	// for the asset body) are applied per request; there is deliberately
	// no Client.Timeout — a global cap would kill a slow-but-progressing
	// download mid-body (see the update command for the same design).
	pluginClient = &http.Client{
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           (&net.Dialer{Timeout: pluginConnectTimeout}).DialContext,
			TLSHandshakeTimeout:   pluginTLSHandshakeTimeout,
			ResponseHeaderTimeout: pluginResponseHeaderTimeout,
		},
		CheckRedirect: pluginCheckRedirect,
	}
	// Per-request deadlines (vars so the tests can shrink them).
	pluginRequestTimeout  = 60 * time.Second
	pluginDownloadTimeout = 10 * time.Minute
	// pluginSmokeCheckTimeout bounds the manifest smoke check of an
	// installed plugin: a hung plugin must refuse with a clear error
	// instead of wedging the CLI (var so tests can shrink it).
	pluginSmokeCheckTimeout = 30 * time.Second
	// pluginMaxAssetSize caps a downloaded plugin asset (1 GiB; a real
	// plugin binary is tens of MB). Var (not const) so tests can shrink
	// it. Both the Content-Length agreement and a streaming cap enforce
	// it — a lying or chunked server cannot push more.
	pluginMaxAssetSize = int64(1 << 30) // 1 GiB
)

// Connection-phase timeouts of the plugin client: they bound dialing,
// the TLS handshake and the wait for response headers — never the body
// read.
const (
	pluginConnectTimeout        = 10 * time.Second
	pluginTLSHandshakeTimeout   = 10 * time.Second
	pluginResponseHeaderTimeout = 30 * time.Second
)

// maxPluginRedirects caps the redirect chain of the install client.
// Setting CheckRedirect replaces Go's default cap, so the count limit
// is re-implemented explicitly here.
const maxPluginRedirects = 10

// maxChecksumsSize caps SHA256SUMS.txt (a few KiB in practice; anything
// larger is refused — bounded read, fail-closed).
const maxChecksumsSize = 1 << 20 // 1 MiB

// pluginCheckRedirect is the install client's redirect policy: refuse
// redirects to non-HTTPS schemes (a release response must never point
// the client at a local file or a plaintext endpoint) and cap the chain
// length (a redirect loop must not spin forever). Package-level so
// tests can apply it to their own clients.
func pluginCheckRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= maxPluginRedirects {
		return fmt.Errorf("stopped after %d redirects", maxPluginRedirects)
	}
	if req.URL.Scheme != "https" {
		return fmt.Errorf("refused redirect to non-HTTPS scheme %q", req.URL.Scheme)
	}
	return nil
}

// newPluginCommand builds the `eka plugin` command group.
func newPluginCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "plugin",
		Short: "Install and manage EKA plugins",
		Long: `Install and manage EKA plugins: verified extensions of the CLI
(the first one is the eka-mcp plugin, which provides MCP server
capabilities and artifact installation).

The plugin command is distribution convenience, like eka update: it
never touches the EKA workspace or the canonical store. Plugins are
classified by the two-tier trust model: names resolved through the
CLI's built-in registry are OFFICIAL and full-trust (no prompt), any
other plugin is THIRD-PARTY and requires explicit consent after its
source and capabilities are shown. Every downloaded binary — official
or third-party — is SHA-256 verified against the release's
SHA256SUMS.txt before it is installed (fail-closed: a mismatch
refuses).

  install <name>  install (or replace) a plugin's latest verified
                  release from GitHub
  list            list discovered and installed plugins with their
                  trust tier (official / third-party)
  remove <name>   remove an installed plugin
  update [name]   update an installed plugin to its latest verified
                  release (--all: every installed official plugin)`,
	}
	cmd.AddCommand(newPluginInstallCommand(), newPluginListCommand(),
		newPluginRemoveCommand(), newPluginUpdateCommand())
	return cmd
}

// newPluginInstallCommand builds `eka plugin install <name> [--repo
// owner/name] [--yes]`.
func newPluginInstallCommand() *cobra.Command {
	f := &pluginInstallFlags{}
	cmd := &cobra.Command{
		Use:   "install <name>",
		Short: "Install an official or third-party EKA plugin",
		Long: `Install a plugin: the plugin's latest release asset
(eka-<name>-<os>-<arch>[.exe]) is downloaded from GitHub, verified
against the release's SHA256SUMS.txt (fail-closed: a missing or
mismatched checksum refuses the install), inspected (its manifest must
parse and name the plugin), installed as an executable "eka-<name>"
into the plugin directory ($EKA_PLUGIN_DIR or ~/.eka/plugins).

Two-tier trust model: a name resolved through the CLI's built-in
registry is OFFICIAL and installs without a prompt. Every other
plugin is THIRD-PARTY: its source and capabilities are shown and
explicit consent is required before the install — --yes consents
non-interactively, and outside a terminal --yes is required (the CLI
never auto-consents silently).

--repo <owner/name> installs from an arbitrary GitHub repository; the
name is then third-party by definition (the registry is bypassed) and
the consent flow applies.

Unknown plugin names refuse with the list of official plugins.
Re-installing an already-installed plugin overwrites it cleanly.`,
		Example: `  eka plugin install mcp                       install the official eka-mcp plugin
  eka plugin install mybot --repo acme/eka-mybot   install a third-party plugin with consent`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			r, err := newPluginInstallRunner()
			if err != nil {
				return err // Exit 2: internal.
			}
			return r.run(cmd, args[0], f)
		},
	}
	cmd.Flags().StringVar(&f.repo, "repo", "", "install from an arbitrary GitHub repository (owner/name); the plugin is then third-party and requires consent")
	cmd.Flags().BoolVar(&f.yes, "yes", false, "consent to a third-party install without the prompt")
	return cmd
}

// pluginInstallFlags carries the flag set of `eka plugin install`.
type pluginInstallFlags struct {
	// repo pins an arbitrary GitHub source repository (owner/name);
	// with it the name is third-party by definition.
	repo string
	// yes consents to a third-party install without the interactive
	// prompt (the source/capabilities are still shown).
	yes bool
}

// pluginInstallRunner carries the injectable execution context of one
// `eka plugin install` run: the registry lookup, the network endpoints
// (API + download root), the HTTP client, the plugin directory, the
// platform, the CLI version (User-Agent) and the third-party consent
// decision. Production builds it from the package defaults
// (newPluginInstallRunner); tests construct it directly against an
// httptest server.
type pluginInstallRunner struct {
	resolve      func(string) (plugin.Repo, bool)
	apiBase      string // GitHub API root ("https://api.github.com")
	downloadRoot string // GitHub download root ("https://github.com")
	client       *http.Client
	pluginDir    string
	goos         string
	goarch       string
	version      string // CLI version (User-Agent)
	// consent decides whether a third-party install proceeds when --yes
	// was NOT given. Production (pluginConsentPrompt) enforces the
	// determinism gate (a non-terminal run refuses — never auto-consent)
	// and prompts interactively on a terminal; tests inject a
	// deterministic stub to exercise the consent branches without a real
	// terminal.
	consent func(*cobra.Command, *ui.Style, string) (bool, error)
}

// newPluginInstallRunner assembles the production runner: the pinned
// GitHub endpoints, the standard client, the plugin directory
// ($EKA_PLUGIN_DIR or <home>/.eka/plugins), the build
// platform/version and the production consent prompt.
func newPluginInstallRunner() (*pluginInstallRunner, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("cannot resolve the home directory: %w", err)
	}
	dir := plugin.PluginDir(home)
	if dir == "" {
		// Defense in depth: never install into the current directory.
		// PluginDir returns "" only when neither $EKA_PLUGIN_DIR nor a
		// home is available; with a resolved home this is unreachable.
		return nil, errors.New("cannot resolve the plugin directory (set EKA_PLUGIN_DIR)")
	}
	return &pluginInstallRunner{
		resolve:      plugin.OfficialRegistry.Lookup,
		apiBase:      pluginAPIBase,
		downloadRoot: pluginDownloadRoot,
		client:       pluginClient,
		pluginDir:    dir,
		goos:         runtime.GOOS,
		goarch:       runtime.GOARCH,
		version:      version,
		consent:      pluginConsentPrompt,
	}, nil
}

// apiURL is the GitHub API latest-release endpoint of a plugin's
// repository.
func (r *pluginInstallRunner) apiURL(repo plugin.Repo) string {
	return r.apiBase + "/repos/" + repo.String() + "/releases/latest"
}

// downloadBase is the /releases/latest/download base of a plugin's
// repository, from which the tagged download base is derived.
func (r *pluginInstallRunner) downloadBase(repo plugin.Repo) string {
	return r.downloadRoot + "/" + repo.String() + "/releases/latest/download"
}

// taggedBase pins the download base to a specific release tag, so the
// checksum and the asset always come from the SAME release: the
// /latest/ form redirects to the newest release, but each /latest/
// request resolves independently — a release published between two
// fetches would mix releases. Test bases (no marker) are tagged by
// appending the tag as a path segment.
func (r *pluginInstallRunner) taggedBase(repo plugin.Repo, tag string) string {
	base := r.downloadBase(repo)
	if i := strings.LastIndex(base, latestDownloadMarker); i >= 0 {
		return base[:i] + "/releases/download/" + tag
	}
	return base + "/" + tag
}

// run executes one install under the two-tier trust model: classify
// the plugin (registry name = official, --repo = third-party), resolve
// the latest release, verify the checksum fail-closed (both tiers),
// inspect the staged manifest, obtain explicit consent for third-party
// plugins and finalize the install.
func (r *pluginInstallRunner) run(cmd *cobra.Command, name string, f *pluginInstallFlags) error {
	s := styleFor(cmd)
	sm := ui.NewSummary(s)

	repo, thirdParty, err := r.resolveInstallRepo(cmd, name, f.repo)
	if err != nil {
		return err
	}
	asset, err := platformAssetName("eka-"+name, r.goos, r.goarch)
	if err != nil {
		return refuse(cmd, "plugin install refused: %s", err)
	}
	tag, size, want, err := r.resolveLatestRelease(repo, asset)
	if err != nil {
		return refuse(cmd, "plugin install refused: %s", err)
	}
	target := r.installTarget(name)
	replacing := fileExists(target)

	r.renderHeader(s, name, repo, asset, tag, thirdParty)
	if replacing {
		fmt.Fprintln(s.W, s.Warning("replacing the existing eka-"+name+" installation"))
	}

	tmp, err := r.downloadVerified(s, repo, tag, asset, size, want)
	if err != nil {
		return refuse(cmd, "plugin install refused: %s", err)
	}
	// Leftover cleanup: after a successful rename the temp path no
	// longer exists and the deferred removal is a no-op; a refusal
	// (consent declined, broken manifest) removes the staged download.
	defer os.Remove(tmp)

	// The STAGED binary's manifest is inspected BEFORE anything moves
	// into place: it verifies the verified download (both tiers) and
	// supplies the capabilities/source the third-party consent
	// surfaces. A broken or mismatched manifest refuses with the
	// staged download removed — the plugin directory is never touched
	// by a broken binary.
	m, err := r.inspectStaged(name, tmp)
	if err != nil {
		return refuse(cmd, "plugin install refused: %s", err)
	}

	if thirdParty {
		r.renderThirdPartyInfo(s, repo, m)
		if !f.yes {
			ok, err := r.consent(cmd, s, name)
			if err != nil {
				return refuse(cmd, "plugin install refused: %s", err)
			}
			if !ok {
				return refuse(cmd, "plugin install refused: consent to install the third-party plugin %q declined", name)
			}
		}
	}

	if err := os.Rename(tmp, target); err != nil {
		return refuse(cmd, "plugin install refused: cannot install %s: %s%s", target, err, r.windowsInstallHint())
	}

	fmt.Fprintf(s.W, "%s\n", s.Success(ui.IconDone+" installed: "+target))
	sm.Add("Plugin", name)
	sm.Add("Repo", repo.String())
	sm.Add("Version", tag)
	if thirdParty {
		sm.Add("Trust", "third-party (consent given)")
	}
	sm.Add("Installed", target)
	sm.Render()
	return nil
}

// resolveInstallRepo classifies the install and resolves its source
// repository: --repo pins an arbitrary GitHub repository (the name is
// third-party by definition — the registry is bypassed, even for a
// maleolabs repo), otherwise the name resolves through the official
// registry (official when listed; an unknown name refuses with the
// list of official plugins, before any network access).
func (r *pluginInstallRunner) resolveInstallRepo(cmd *cobra.Command, name, repoFlag string) (plugin.Repo, bool, error) {
	if repoFlag != "" {
		repo, err := parsePluginRepo(repoFlag)
		if err != nil {
			return plugin.Repo{}, true, refuse(cmd, "plugin install refused: %s", err)
		}
		return repo, true, nil // --repo: third-party by definition.
	}
	repo, ok := r.resolve(name)
	if !ok {
		return plugin.Repo{}, false, refuse(cmd, "plugin install refused: unknown plugin %q — official plugins: %s",
			name, strings.Join(plugin.OfficialRegistry.Names(), ", "))
	}
	return repo, false, nil // registry-listed: official, full-trust.
}

// parsePluginRepo parses and validates a --repo value: the canonical
// "owner/name" GitHub reference. Any other shape is a refusal.
func parsePluginRepo(s string) (plugin.Repo, error) {
	parts := strings.Split(s, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" ||
		strings.ContainsAny(parts[0], " \t\n") || strings.ContainsAny(parts[1], " \t\n") {
		return plugin.Repo{}, fmt.Errorf("invalid --repo %q (want owner/name)", s)
	}
	return plugin.Repo{Owner: parts[0], Name: parts[1]}, nil
}

// installTarget is the installed binary path of a plugin: the plugin
// directory with the eka-<name> executable contract (the .exe suffix
// on windows mirrors the asset name).
func (r *pluginInstallRunner) installTarget(name string) string {
	target := filepath.Join(r.pluginDir, "eka-"+name)
	if r.goos == "windows" {
		target += ".exe"
	}
	return target
}

// resolveLatestRelease resolves the latest release of a plugin's
// repository for the platform asset: the version tag (GitHub API),
// the asset's Content-Length (HEAD probe; 0 when unknown —
// presentation only) and the expected SHA-256 checksum from the SAME
// tag-pinned SHA256SUMS.txt. Any failure refuses the run — a release
// that cannot be fully resolved is a broken release.
func (r *pluginInstallRunner) resolveLatestRelease(repo plugin.Repo, asset string) (tag string, size int64, want string, err error) {
	tag, err = r.latestTag(repo)
	if err != nil {
		return "", 0, "", fmt.Errorf("cannot resolve the latest release of %s: %w", repo, err)
	}
	sums, err := r.fetchChecksums(repo, tag)
	if err != nil {
		return "", 0, "", fmt.Errorf("cannot fetch SHA256SUMS.txt of %s: %w", repo, err)
	}
	want, ok := checksumForAsset(sums, asset)
	if !ok {
		return "", 0, "", fmt.Errorf("SHA256SUMS.txt of %s carries no entry for %s (fail-closed)", repo, asset)
	}
	return tag, r.assetSize(repo, tag, asset), want, nil
}

// downloadVerified downloads the platform asset of a tag-pinned
// release, verifies its SHA-256 checksum fail-closed (the checksum
// resolved from the SAME tag-pinned release as the download), makes
// the staged binary executable and returns its temp path — the caller
// renames the staged file into place. Any failure removes the partial
// download; unverified bytes are never installed.
func (r *pluginInstallRunner) downloadVerified(s *ui.Style, repo plugin.Repo, tag, asset string, size int64, want string) (string, error) {
	if err := os.MkdirAll(r.pluginDir, 0o755); err != nil {
		return "", fmt.Errorf("cannot create %s: %w", r.pluginDir, err)
	}
	tmp, err := os.CreateTemp(r.pluginDir, ".eka-plugin-*")
	if err != nil {
		return "", fmt.Errorf("cannot stage the download in %s: %w", r.pluginDir, err)
	}
	tmpName := tmp.Name()
	fail := func(err error) (string, error) {
		tmp.Close()
		os.Remove(tmpName)
		return "", err
	}

	// A blank line releases the header before the progress bar (the
	// update command's layout).
	fmt.Fprintln(s.W)

	bar := ui.NewDownloadBar(s, asset, size)
	if err := r.downloadAsset(tmp, repo, tag, asset, bar); err != nil {
		bar.Abort()
		return fail(fmt.Errorf("download of %s failed: %w", asset, err))
	}
	if fi, err := tmp.Stat(); err == nil && fi.Size() == 0 {
		bar.Abort()
		return fail(fmt.Errorf("downloaded asset %s is empty", asset))
	}
	if err := tmp.Close(); err != nil {
		bar.Abort()
		return fail(fmt.Errorf("cannot finalize the download: %w", err))
	}

	// Fail-closed verification against the checksum resolved from the
	// SAME tag-pinned release as the download.
	got, err := sha256File(tmpName)
	if err != nil {
		bar.Abort()
		return fail(fmt.Errorf("cannot hash the downloaded %s: %w", asset, err))
	}
	if !strings.EqualFold(got, want) {
		bar.Abort()
		return fail(fmt.Errorf("checksum mismatch for %s (expected %s, got %s)", asset, want, got))
	}
	if err := os.Chmod(tmpName, 0o755); err != nil {
		bar.Abort()
		return fail(fmt.Errorf("cannot make %s executable: %w", tmpName, err))
	}
	bar.Finish()
	return tmpName, nil
}

// renderHeader prints the context header (the single-operation
// interaction model): the accent heading and the Plugin/Repo/Version/
// Asset labels followed by the pipeline line. A third-party install
// carries a "Trust third-party" row — the tier is visible before any
// download.
func (r *pluginInstallRunner) renderHeader(s *ui.Style, name string, repo plugin.Repo, asset, tag string, thirdParty bool) {
	fmt.Fprintln(s.W)
	fmt.Fprintln(s.W, s.Accent("Install"))
	fmt.Fprintf(s.W, "  %-7s   %s\n", s.Info("Plugin"), name)
	fmt.Fprintf(s.W, "  %-7s   %s\n", s.Info("Repo"), repo.String())
	fmt.Fprintf(s.W, "  %-7s   %s\n", s.Info("Version"), tag)
	fmt.Fprintf(s.W, "  %-7s   %s\n", s.Info("Asset"), asset)
	if thirdParty {
		fmt.Fprintf(s.W, "  %-7s   %s\n", s.Info("Trust"), "third-party")
	}
	fmt.Fprintf(s.W, "  %s\n", s.Accent("↓ Install"))
}

// renderThirdPartyInfo surfaces the trust surface of a third-party
// install — the source repository and the capabilities (plus the
// summary) the staged binary's manifest declares — immediately before
// the consent decision. It renders for --yes runs too: automation must
// still see what it consented to.
func (r *pluginInstallRunner) renderThirdPartyInfo(s *ui.Style, repo plugin.Repo, m plugin.Manifest) {
	fmt.Fprintln(s.W)
	fmt.Fprintln(s.W, s.Warning("Third-party plugin"))
	fmt.Fprintf(s.W, "  %-12s   %s\n", s.Info("Source"), "https://github.com/"+repo.String())
	if m.Description != "" {
		fmt.Fprintf(s.W, "  %-12s   %s\n", s.Info("Summary"), m.Description)
	}
	caps := m.Capabilities
	if len(caps) == 0 {
		caps = []string{"none declared"}
	}
	fmt.Fprintf(s.W, "  %-12s   %s\n", s.Info("Capabilities"), strings.Join(caps, ", "))
}

// windowsInstallHint explains the Windows installation caveat on the
// platform where it applies.
func (r *pluginInstallRunner) windowsInstallHint() string {
	if r.goos == "windows" {
		return " (on Windows a file in use cannot always be overwritten; remove the existing eka-<name>.exe first)"
	}
	return ""
}

// inspectStaged runs the STAGED binary's manifest (bounded by
// pluginSmokeCheckTimeout — a hung plugin refuses with a clear error
// instead of wedging the CLI), verifies it parses into plugin.Manifest
// with the requested name, and returns it. The staged binary is the
// checksum-verified download; a broken or mismatched manifest refuses
// BEFORE anything is moved into the plugin directory.
func (r *pluginInstallRunner) inspectStaged(name, tmp string) (plugin.Manifest, error) {
	ctx, cancel := context.WithTimeout(context.Background(), pluginSmokeCheckTimeout)
	defer cancel()
	m, err := (plugin.Plugin{Exe: tmp}).ManifestContext(ctx)
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return plugin.Manifest{}, fmt.Errorf("the downloaded plugin %q timed out after %s answering \"manifest\" (killed)", name, pluginSmokeCheckTimeout)
		}
		return plugin.Manifest{}, fmt.Errorf("the downloaded plugin %q failed the smoke check: %w", name, err)
	}
	if m.Name != name {
		return plugin.Manifest{}, fmt.Errorf("the downloaded plugin reports manifest name %q, want %q", m.Name, name)
	}
	return m, nil
}

// pluginConsentPrompt is the production third-party consent decision
// (the runner's consent default). A non-terminal run (pipes, CI)
// without --yes refuses — the CLI never auto-consents silently
// (fail-closed): the run stops with a refusal naming the --yes escape
// hatch. On a real terminal the user is prompted explicitly
// (ui.Select, defaulting to "abort"; Esc/q/Ctrl-C decline). It is
// only invoked when --yes was NOT given.
func pluginConsentPrompt(cmd *cobra.Command, s *ui.Style, name string) (bool, error) {
	if !(s.TTY && isTTYReader(cmd.InOrStdin())) {
		return false, fmt.Errorf("the third-party plugin %q requires explicit consent; pass --yes to consent non-interactively", name)
	}
	value, err := ui.Select(s, cmd.InOrStdin(), cmd.OutOrStdout(),
		fmt.Sprintf("Install the third-party plugin %q?", name),
		[]ui.MenuItem{{Title: "install", Value: "install"}, {Title: "abort", Value: "abort"}}, 1)
	if err != nil {
		if errors.Is(err, ui.ErrCancelled) {
			return false, nil // cancelled = declined
		}
		return false, fmt.Errorf("cannot read the consent: %w", err) // Exit 2: internal.
	}
	return value == "install", nil
}

// latestTag resolves the latest release version of the plugin's
// repository via the GitHub REST API. A rate-limited or unavailable API
// refuses with a clear message.
func (r *pluginInstallRunner) latestTag(repo plugin.Repo) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), pluginRequestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.apiURL(repo), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "eka-cli/"+r.version)
	// A GH_TOKEN raises the GitHub API rate limit; the header is only
	// sent when the environment provides it.
	if token := os.Getenv("GH_TOKEN"); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
		if resp.Header.Get("X-RateLimit-Remaining") == "0" {
			return "", errors.New("GitHub API rate limit exhausted — retry later (or authenticate with GH_TOKEN)")
		}
		return "", fmt.Errorf("GitHub API returned %s", resp.Status)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitHub API returned %s", resp.Status)
	}
	var meta struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&meta); err != nil {
		return "", fmt.Errorf("cannot parse the release metadata: %w", err)
	}
	if meta.TagName == "" {
		return "", errors.New("release metadata carries no tag_name")
	}
	return meta.TagName, nil
}

// fetchChecksums downloads the release's SHA256SUMS.txt from the
// tag-pinned base, so checksum and asset always come from the same
// release. The read is bounded at maxChecksumsSize — a larger file is
// refused (fail-closed).
func (r *pluginInstallRunner) fetchChecksums(repo plugin.Repo, tag string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), pluginRequestTimeout)
	defer cancel()
	resp, err := r.do(ctx, http.MethodGet, r.taggedBase(repo, tag)+"/SHA256SUMS.txt")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("SHA256SUMS.txt returned %s", resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxChecksumsSize+1))
	if err != nil {
		return "", err
	}
	if len(b) > maxChecksumsSize {
		return "", fmt.Errorf("SHA256SUMS.txt exceeds %d bytes (refusing)", maxChecksumsSize)
	}
	return string(b), nil
}

// assetSize probes the asset's Content-Length via HEAD. A failure is
// non-fatal: the size is only presentation (the progress bar falls back
// to the indeterminate form).
func (r *pluginInstallRunner) assetSize(repo plugin.Repo, tag, asset string) int64 {
	ctx, cancel := context.WithTimeout(context.Background(), pluginRequestTimeout)
	defer cancel()
	resp, err := r.do(ctx, http.MethodHead, r.taggedBase(repo, tag)+"/"+asset)
	if err != nil {
		return 0
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0
	}
	return resp.ContentLength
}

// do performs one request with a per-request deadline: metadata
// requests (API, probes, checksums) use pluginRequestTimeout, the asset
// download pluginDownloadTimeout. The transport bounds the connection
// phases; there is deliberately no Client.Timeout — a global cap would
// kill a slow-but-progressing download mid-body.
func (r *pluginInstallRunner) do(ctx context.Context, method, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		return nil, err
	}
	return r.client.Do(req)
}

// downloadAsset streams the asset to w with realtime progress: done
// bytes are reported to bar per read chunk, the total comes from the
// resolution step (Content-Length), 0 renders an indeterminate bar. The
// request carries the generous pluginDownloadTimeout — the body read is
// bounded by it, never by a connection-phase timeout. The body is
// additionally bounded at pluginMaxAssetSize: a server-declared
// Content-Length over the cap refuses before any byte is written, and a
// streaming cap stops a chunked or lying server at cap+1 bytes.
func (r *pluginInstallRunner) downloadAsset(w io.Writer, repo plugin.Repo, tag, asset string, bar *ui.DownloadBar) error {
	ctx, cancel := context.WithTimeout(context.Background(), pluginDownloadTimeout)
	defer cancel()
	resp, err := r.do(ctx, http.MethodGet, r.taggedBase(repo, tag)+"/"+asset)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %s", resp.Status)
	}
	// Content-Length agreement: a declared size over the cap refuses
	// before any byte is written.
	if resp.ContentLength > pluginMaxAssetSize {
		return fmt.Errorf("asset %s is %d bytes, exceeding the %d-byte limit", asset, resp.ContentLength, pluginMaxAssetSize)
	}
	// Streaming cap: even without (or despite) Content-Length, at most
	// cap+1 bytes are read.
	n, err := io.Copy(&barWriter{w: w, bar: bar}, io.LimitReader(resp.Body, pluginMaxAssetSize+1))
	if err != nil {
		return err
	}
	if n > pluginMaxAssetSize {
		return fmt.Errorf("asset %s exceeds the %d-byte limit (download aborted)", asset, pluginMaxAssetSize)
	}
	return nil
}

// fileExists reports whether path exists (a broken symlink or a
// permission error counts as not existing — the install continues and
// the rename overwrites).
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
