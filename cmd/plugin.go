package cmd

// This file implements `eka plugin` and `eka plugin install`:
// installation of official EKA plugins from their GitHub releases.
//
// The plugin command is distribution convenience, like eka update: it
// never touches the EKA workspace or the canonical store.
//
// Install flow (fail-closed):
//
//  1. `eka plugin install <name>` resolves the name through the official
//     registry (plugin.OfficialRegistry). An unknown name refuses with
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
//     removes the partial download — never install unverified.
//  5. The verified binary is installed as eka-<name> (0755; .exe on
//     windows) into the plugin directory ($EKA_PLUGIN_DIR or
//     ~/.eka/plugins), matching the Discover "eka-*" executable
//     contract.
//  6. Smoke check: the installed binary is run with "manifest" (bounded
//     by pluginSmokeCheckTimeout — a hung plugin refuses) and the output
//     must parse into plugin.Manifest with a matching name; a broken
//     manifest removes the installed binary and refuses.
//  7. Re-install overwrites the previous binary cleanly (with a printed
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
//	   platform)
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
		Short: "Install and manage official EKA plugins",
		Long: `Install and manage official EKA plugins: verified extensions of the
CLI (the first one is the eka-mcp plugin, which provides MCP server
capabilities and artifact installation).

The plugin command is distribution convenience, like eka update: it
never touches the EKA workspace or the canonical store. Official
plugins are resolved through the CLI's built-in registry and every
downloaded binary is SHA-256 verified against the release's
SHA256SUMS.txt before it is installed (fail-closed: a mismatch
refuses).`,
	}
	cmd.AddCommand(newPluginInstallCommand())
	return cmd
}

// newPluginInstallCommand builds the `eka plugin install` command.
func newPluginInstallCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "install <name>",
		Short: "Install an official EKA plugin",
		Long: `Install an official EKA plugin: the plugin's latest release asset
(eka-<name>-<os>-<arch>[.exe]) is downloaded from GitHub, verified
against the release's SHA256SUMS.txt (fail-closed: a missing or
mismatched checksum refuses the install), installed as an executable
"eka-<name>" into the plugin directory ($EKA_PLUGIN_DIR or
~/.eka/plugins) and smoke-checked by running its manifest. A broken
manifest removes the installed binary and refuses.

Unknown plugin names refuse with the list of official plugins.
Re-installing an already-installed plugin overwrites it cleanly.`,
		Example: `  eka plugin install mcp   install the official eka-mcp plugin`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			r, err := newPluginInstallRunner()
			if err != nil {
				return err // Exit 2: internal.
			}
			return r.run(cmd, args[0])
		},
	}
}

// pluginInstallRunner carries the injectable execution context of one
// `eka plugin install` run: the registry lookup, the network endpoints
// (API + download root), the HTTP client, the plugin directory, the
// platform and the CLI version (User-Agent). Production builds it from
// the package defaults (newPluginInstallRunner); tests construct it
// directly against an httptest server.
type pluginInstallRunner struct {
	resolve      func(string) (plugin.Repo, bool)
	apiBase      string // GitHub API root ("https://api.github.com")
	downloadRoot string // GitHub download root ("https://github.com")
	client       *http.Client
	pluginDir    string
	goos         string
	goarch       string
	version      string // CLI version (User-Agent)
}

// newPluginInstallRunner assembles the production runner: the pinned
// GitHub endpoints, the standard client, the plugin directory
// ($EKA_PLUGIN_DIR or <home>/.eka/plugins) and the build
// platform/version.
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

// run executes one install: resolve the name through the registry,
// resolve the latest release, verify the checksum fail-closed, download,
// install and smoke-check the plugin.
func (r *pluginInstallRunner) run(cmd *cobra.Command, name string) error {
	s := styleFor(cmd)
	sm := ui.NewSummary(s)

	repo, ok := r.resolve(name)
	if !ok {
		return refuse(cmd, "plugin install refused: unknown plugin %q — official plugins: %s",
			name, strings.Join(plugin.OfficialRegistry.Names(), ", "))
	}
	asset, err := platformAssetName("eka-"+name, r.goos, r.goarch)
	if err != nil {
		return refuse(cmd, "plugin install refused: %s", err)
	}
	tag, err := r.latestTag(repo)
	if err != nil {
		return refuse(cmd, "plugin install refused: cannot resolve the latest release of %s: %s", repo, err)
	}
	sums, err := r.fetchChecksums(repo, tag)
	if err != nil {
		return refuse(cmd, "plugin install refused: cannot fetch SHA256SUMS.txt of %s: %s", repo, err)
	}
	want, ok := checksumForAsset(sums, asset)
	if !ok {
		return refuse(cmd, "plugin install refused: SHA256SUMS.txt of %s carries no entry for %s (fail-closed)", repo, asset)
	}
	target := filepath.Join(r.pluginDir, "eka-"+name)
	if r.goos == "windows" {
		target += ".exe" // the installed name mirrors the asset suffix
	}
	replacing := fileExists(target)
	size := r.assetSize(repo, tag, asset)

	r.renderHeader(s, name, repo, asset, tag)
	if replacing {
		fmt.Fprintln(s.W, s.Warning("replacing the existing eka-"+name+" installation"))
	}

	if err := os.MkdirAll(r.pluginDir, 0o755); err != nil {
		return refuse(cmd, "plugin install refused: cannot create %s: %s", r.pluginDir, err)
	}
	tmp, err := os.CreateTemp(r.pluginDir, ".eka-plugin-*")
	if err != nil {
		return refuse(cmd, "plugin install refused: cannot stage the download in %s: %s", r.pluginDir, err)
	}
	tmpName := tmp.Name()
	// Leftover cleanup: after a successful rename the temp path no
	// longer exists and the deferred removal is a no-op.
	defer os.Remove(tmpName)

	// A blank line releases the header before the progress bar (the
	// update command's layout).
	fmt.Fprintln(s.W)

	bar := ui.NewDownloadBar(s, asset, size)
	if err := r.downloadAsset(tmp, repo, tag, asset, bar); err != nil {
		tmp.Close()
		bar.Abort()
		return refuse(cmd, "plugin install refused: download of %s failed: %s", asset, err)
	}
	if fi, err := tmp.Stat(); err == nil && fi.Size() == 0 {
		tmp.Close()
		bar.Abort()
		return refuse(cmd, "plugin install refused: downloaded asset %s is empty", asset)
	}
	if err := tmp.Close(); err != nil {
		bar.Abort()
		return refuse(cmd, "plugin install refused: cannot finalize the download: %s", err)
	}

	// Fail-closed verification against the checksum resolved from the
	// SAME tag-pinned release as the download.
	got, err := sha256File(tmpName)
	if err != nil {
		bar.Abort()
		return refuse(cmd, "plugin install refused: cannot hash the downloaded %s: %s", asset, err)
	}
	if !strings.EqualFold(got, want) {
		bar.Abort()
		return refuse(cmd, "plugin install refused: checksum mismatch for %s (expected %s, got %s)", asset, want, got)
	}
	if err := os.Chmod(tmpName, 0o755); err != nil {
		bar.Abort()
		return refuse(cmd, "plugin install refused: cannot make %s executable: %s", tmpName, err)
	}
	bar.Finish()

	if err := os.Rename(tmpName, target); err != nil {
		return refuse(cmd, "plugin install refused: cannot install %s: %s%s", target, err, r.windowsInstallHint())
	}

	// Smoke check: the installed binary must answer "manifest" with a
	// parseable plugin.Manifest whose name matches. A broken plugin is
	// removed — an installed plugin that cannot describe itself must
	// never be left behind.
	if err := r.smokeCheck(name, target); err != nil {
		r.removeInstalled(s, target)
		return refuse(cmd, "plugin install refused: %s", err)
	}

	fmt.Fprintf(s.W, "%s\n", s.Success(ui.IconDone+" installed: "+target))
	sm.Add("Plugin", name)
	sm.Add("Repo", repo.String())
	sm.Add("Version", tag)
	sm.Add("Installed", target)
	sm.Render()
	return nil
}

// renderHeader prints the context header (the single-operation
// interaction model): the accent heading and the Plugin/Repo/Version/
// Asset labels followed by the pipeline line.
func (r *pluginInstallRunner) renderHeader(s *ui.Style, name string, repo plugin.Repo, asset, tag string) {
	fmt.Fprintln(s.W)
	fmt.Fprintln(s.W, s.Accent("Install"))
	fmt.Fprintf(s.W, "  %-7s   %s\n", s.Info("Plugin"), name)
	fmt.Fprintf(s.W, "  %-7s   %s\n", s.Info("Repo"), repo.String())
	fmt.Fprintf(s.W, "  %-7s   %s\n", s.Info("Version"), tag)
	fmt.Fprintf(s.W, "  %-7s   %s\n", s.Info("Asset"), asset)
	fmt.Fprintf(s.W, "  %s\n", s.Accent("↓ Install"))
}

// windowsInstallHint explains the Windows installation caveat on the
// platform where it applies.
func (r *pluginInstallRunner) windowsInstallHint() string {
	if r.goos == "windows" {
		return " (on Windows a file in use cannot always be overwritten; remove the existing eka-<name>.exe first)"
	}
	return ""
}

// smokeCheck runs the installed plugin's manifest (bounded by
// pluginSmokeCheckTimeout — a hung plugin refuses with a clear error
// instead of wedging the CLI) and verifies it parses into
// plugin.Manifest with the requested name.
func (r *pluginInstallRunner) smokeCheck(name, target string) error {
	ctx, cancel := context.WithTimeout(context.Background(), pluginSmokeCheckTimeout)
	defer cancel()
	m, err := (plugin.Plugin{Exe: target}).ManifestContext(ctx)
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("installed plugin %s timed out after %s answering \"manifest\" (killed)", target, pluginSmokeCheckTimeout)
		}
		return fmt.Errorf("installed plugin %s failed the manifest smoke check: %w", target, err)
	}
	if m.Name != name {
		return fmt.Errorf("installed plugin %s reports manifest name %q, want %q", target, m.Name, name)
	}
	return nil
}

// removeInstalled removes a failed install. A removal failure is
// surfaced as a warning — never silently ignored — but does not mask
// the original refusal.
func (r *pluginInstallRunner) removeInstalled(s *ui.Style, target string) {
	if err := os.Remove(target); err != nil {
		fmt.Fprintf(s.W, "%s\n", s.Warning(fmt.Sprintf("warning: cannot remove the broken plugin at %s: %s", target, err)))
	}
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
