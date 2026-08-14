package cmd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/dustin/go-humanize"
	"github.com/maleolabs/eka-cli/cmd/ui"
	"github.com/spf13/cobra"
)

// This file implements `eka update`: self-update of the EKA CLI. The
// newer binary asset is downloaded from the GitHub Releases of this
// repository, verified against the release's SHA256SUMS.txt
// (fail-closed: a missing or mismatched checksum refuses the update)
// and atomically replaces the running binary.
//
// The command is distribution convenience, like eka install: it never
// touches the EKA workspace or the canonical store.
//
// Release contract (install.sh + .github/workflows/release.yml): every
// tagged release publishes the assets eka-<os>-<arch>[.exe] (linux /
// darwin / windows × amd64 / arm64) plus SHA256SUMS.txt, reachable
// through the /releases/latest/download/ redirect. Once the latest tag
// is resolved, every fetch is PINNED to the tagged release URL — the
// checksum and the asset always come from the same release. All
// network access is HTTPS-only.
//
// Exit codes:
//
//	0  already up to date, updated, or aborted
//	1  update available (--check), or a refusal (the latest release
//	   cannot be resolved, the checksum is missing/mismatched, the
//	   binary replacement failed)
//	2  usage or internal error (an update is available but the run is
//	   non-interactive without --yes)
//
// The network endpoints are package-level vars (not consts) so the
// same-package tests can point them at an httptest server; production
// always uses the pinned GitHub endpoints.
//
// Timeout design: a global Client.Timeout would bound the whole
// request INCLUDING the body read — a slow-but-progressing binary
// download would be killed mid-body (the original 60s cap did exactly
// that). Instead the transport bounds the connection phases (dial,
// TLS handshake, response headers — a dead host refuses fast) and
// each request carries its own deadline: metadata requests get
// updateRequestTimeout, the asset download gets updateDownloadTimeout
// (generous: the largest release asset at a slow link).
var (
	updateAPIURL       = "https://api.github.com/repos/maleolabs/eka-cli/releases/latest"
	updateDownloadBase = "https://github.com/maleolabs/eka-cli/releases/latest/download"
	updateClient       = &http.Client{Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: updateConnectTimeout}).DialContext,
		TLSHandshakeTimeout:   updateTLSHandshakeTimeout,
		ResponseHeaderTimeout: updateResponseHeaderTimeout,
	}}
	// Per-request deadlines (vars so the tests can shrink them).
	updateRequestTimeout  = 60 * time.Second
	updateDownloadTimeout = 10 * time.Minute
)

// Connection-phase timeouts of the update client: they bound dialing,
// the TLS handshake and the wait for response headers — never the
// body read.
const (
	updateConnectTimeout        = 10 * time.Second
	updateTLSHandshakeTimeout   = 10 * time.Second
	updateResponseHeaderTimeout = 30 * time.Second
)

// newUpdateCommand builds the `eka update` command.
func newUpdateCommand() *cobra.Command {
	f := &updateFlags{}
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Update the EKA CLI to the latest release",
		Long: `Update the EKA CLI to the latest release: the newer binary
asset is downloaded from GitHub Releases, verified against the
release's SHA256SUMS.txt (fail-closed: a missing or mismatched
checksum refuses the update) and atomically replaces the running
binary — the old binary is preserved as <binary>.old during the swap
and removed afterwards.

By default the command compares the running version against the
latest release and reports "already up to date" when nothing newer
is available. A dev build (version "dev") skips the comparison and
is always treated as outdated. On a terminal an interactive
confirmation offers "update now" / "abort" before anything is
downloaded; non-interactive runs (pipes, CI) require --yes.

  --check  check for an update without downloading anything; exits 1
           when an update is available (for scripts)
  --yes    skip the confirmation prompt (non-interactive updates)

Exit codes:
  0  already up to date, updated, or aborted
  1  update available (--check), or a refusal — the latest release
     cannot be resolved, the checksum is missing or mismatched, or
     the binary replacement failed
  2  usage or internal error (an update is available but the run is
     non-interactive without --yes)`,
		Example: `  eka update          update to the latest release
  eka update --check  check for an update without downloading
  eka update --yes    update without the confirmation prompt`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runUpdate(cmd, f)
		},
	}
	cmd.Flags().BoolVar(&f.check, "check", false, "check for an update without downloading")
	cmd.Flags().BoolVar(&f.yes, "yes", false, "skip the confirmation prompt")
	return cmd
}

// updateFlags carries the flag set of the update command.
type updateFlags struct {
	check bool
	yes   bool
}

// updateRunner carries the injectable execution context of one update
// run: the network endpoints, the HTTP client, the target binary
// path, the platform and the current version. Production builds it
// from the package defaults (newUpdateRunner); tests construct it
// directly against an httptest server.
type updateRunner struct {
	apiURL       string // GitHub API latest-release endpoint
	downloadBase string // /releases/latest/download base (the fallback probe)
	client       *http.Client
	exePath      string // target binary path (the running binary in production)
	goos         string
	goarch       string
	curVersion   string // current CLI version
}

// newUpdateRunner assembles the production runner: the pinned GitHub
// endpoints, the standard client, the running binary's path (symlinks
// resolved, so a symlinked install updates the real file) and the
// build platform/version.
func newUpdateRunner() (*updateRunner, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("cannot resolve the running binary: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return &updateRunner{
		apiURL:       updateAPIURL,
		downloadBase: updateDownloadBase,
		client:       updateClient,
		exePath:      exe,
		goos:         runtime.GOOS,
		goarch:       runtime.GOARCH,
		curVersion:   version,
	}, nil
}

// updateRelease is the resolved latest release: the version tag, the
// asset size (0 when unknown) and the expected SHA-256 checksum.
type updateRelease struct {
	tag      string
	size     int64
	checksum string
}

// currentVersionLabel renders the running version for the report; a
// dev build (version "dev") is labeled "eka dev (unknown)" — its
// version cannot be compared.
func currentVersionLabel(v string) string {
	if v == "dev" {
		return "eka dev (unknown)"
	}
	return "eka " + v
}

// runUpdate executes `eka update` with the production runner.
func runUpdate(cmd *cobra.Command, f *updateFlags) error {
	r, err := newUpdateRunner()
	if err != nil {
		return err // Exit 2: internal.
	}
	return r.run(cmd, f)
}

// run executes one update run: resolve the latest release, compare
// versions, confirm (interactively or via --yes), download, verify
// and atomically replace the binary.
func (r *updateRunner) run(cmd *cobra.Command, f *updateFlags) error {
	s := styleFor(cmd)
	sm := ui.NewSummary(s)

	asset, err := updateAssetName(r.goos, r.goarch)
	if err != nil {
		return refuse(cmd, "update refused: %s", err)
	}
	rel, err := r.resolveLatest(asset)
	if err != nil {
		return refuse(cmd, "update refused: cannot resolve the latest release: %s", err)
	}
	upToDate := r.curVersion != "dev" && compareVersions(r.curVersion, rel.tag) >= 0

	// Determinism gate, BEFORE any report output: a piped run with an
	// update available must never block on a prompt the user cannot
	// see — it refuses with the --yes hint (usage class, exit 2), so
	// an exit-2 run never emits partial stdout.
	if !upToDate && !f.check && !f.yes && !(s.TTY && isTTYReader(cmd.InOrStdin())) {
		return fmt.Errorf("an update is available (eka %s); pass --yes to update non-interactively", rel.tag)
	}

	r.renderHeader(s, f, asset, rel)

	if upToDate {
		sm.Add("Status", "already up to date")
		sm.Add("Current", currentVersionLabel(r.curVersion))
		sm.Add("Latest", "eka "+rel.tag)
		sm.Render()
		return nil
	}

	if f.check {
		sm.Add("Status", "update available")
		sm.Add("Current", currentVersionLabel(r.curVersion))
		sm.Add("Latest", "eka "+rel.tag)
		sm.Render()
		return &exitError{code: exitFail}
	}

	// Interactive confirmation — wired only when the flag is NOT set
	// and stdin is a real terminal (the ui.Select contract; the
	// non-TTY path was already refused by the determinism gate).
	if !f.yes {
		prompt := fmt.Sprintf("Update available: eka %s (current eka %s). Proceed?", rel.tag, r.curVersion)
		value, err := ui.Select(s, cmd.InOrStdin(), cmd.OutOrStdout(), prompt,
			[]ui.MenuItem{{Title: "update now", Value: "update"}, {Title: "abort", Value: "abort"}}, 0)
		if err != nil {
			if errors.Is(err, ui.ErrCancelled) {
				sm.Add("Status", "no changes")
				sm.Render()
				return nil
			}
			return err // Exit 2: internal.
		}
		if value != "update" {
			sm.Add("Status", "no changes")
			sm.Render()
			return nil
		}
	}

	return r.downloadAndReplace(cmd, s, sm, asset, rel)
}

// renderHeader prints the context header (the single-operation
// interaction model): the accent heading, the Current/Latest/Asset
// labels, the pipeline line and the dev-build warning.
func (r *updateRunner) renderHeader(s *ui.Style, f *updateFlags, asset string, rel *updateRelease) {
	fmt.Fprintln(s.W)
	fmt.Fprintln(s.W, s.Accent("Update"))
	fmt.Fprintf(s.W, "  %-7s   %s\n", s.Info("Current"), currentVersionLabel(r.curVersion))
	fmt.Fprintf(s.W, "  %-7s   %s\n", s.Info("Latest"), "eka "+rel.tag)
	assetLine := asset
	if rel.size > 0 {
		assetLine = fmt.Sprintf("%s (%s)", asset, humanize.Bytes(uint64(rel.size)))
	}
	fmt.Fprintf(s.W, "  %-7s   %s\n", s.Info("Asset"), assetLine)
	pipeline := "Update"
	if f.check {
		pipeline = "Check"
	}
	fmt.Fprintf(s.W, "  %s\n", s.Accent("↓ "+pipeline))
	if r.curVersion == "dev" {
		fmt.Fprintln(s.W, s.Warning("dev build: version comparison skipped — treating as outdated"))
	}
}

// updateAssetName maps a GOOS/GOARCH pair to the release asset name
// (the installer asset naming contract: eka-<os>-<arch>[.exe]).
// Unsupported platforms are a refusal.
func updateAssetName(goos, goarch string) (string, error) {
	switch goos {
	case "linux", "darwin":
	case "windows":
		switch goarch {
		case "amd64", "arm64":
			return "eka-windows-" + goarch + ".exe", nil
		}
		return "", fmt.Errorf("unsupported architecture %q on windows (supported: amd64, arm64)", goarch)
	default:
		return "", fmt.Errorf("unsupported OS %q (supported: linux, darwin, windows)", goos)
	}
	switch goarch {
	case "amd64", "arm64":
		return "eka-" + goos + "-" + goarch, nil
	}
	return "", fmt.Errorf("unsupported architecture %q (supported: amd64, arm64)", goarch)
}

// resolveLatest resolves the latest release for asset: the version
// tag (GitHub API, redirect fallback), the asset size (HEAD) and the
// expected checksum (SHA256SUMS.txt). Any failure refuses the run —
// a release that cannot be fully resolved is a broken release.
func (r *updateRunner) resolveLatest(asset string) (*updateRelease, error) {
	tag, err := r.latestTag()
	if err != nil {
		return nil, err
	}
	sums, err := r.fetchChecksums(r.taggedBase(tag))
	if err != nil {
		return nil, err
	}
	want, ok := checksumForAsset(sums, asset)
	if !ok {
		return nil, fmt.Errorf("SHA256SUMS.txt carries no entry for %s (fail-closed)", asset)
	}
	return &updateRelease{tag: tag, size: r.assetSize(asset, tag), checksum: want}, nil
}

// latestDownloadMarker is the /latest/ redirect segment replaced by
// the tag when a release is resolved.
const latestDownloadMarker = "/releases/latest/download"

// taggedBase pins the download base to a specific release tag, so the
// checksum and the asset always come from the SAME release: the
// /latest/ form redirects to the newest release, but each /latest/
// request resolves independently — a release published between two
// fetches would mix releases. Test/dev bases (no marker) are tagged
// by appending the tag as a path segment.
func (r *updateRunner) taggedBase(tag string) string {
	if i := strings.LastIndex(r.downloadBase, latestDownloadMarker); i >= 0 {
		return r.downloadBase[:i] + "/releases/download/" + tag
	}
	return r.downloadBase + "/" + tag
}

// do performs one request with a per-request deadline: metadata
// requests (API, probes, checksums) use updateRequestTimeout, the
// asset download updateDownloadTimeout. The transport bounds the
// connection phases; there is deliberately no Client.Timeout — a
// global cap would kill a slow-but-progressing download mid-body.
func (r *updateRunner) do(ctx context.Context, method, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		return nil, err
	}
	return r.client.Do(req)
}

// latestTag resolves the latest release version: the GitHub API
// metadata endpoint first (Accept: application/vnd.github+json,
// parse tag_name), falling back to the tag extracted from the
// SHA256SUMS.txt redirect (Location .../releases/download/<tag>/...)
// when the API is unavailable or rate-limited.
func (r *updateRunner) latestTag() (string, error) {
	tag, err := r.latestTagViaAPI()
	if err == nil {
		return tag, nil
	}
	fallback, ferr := resolveTagFromRedirect(r.client, r.downloadBase)
	if ferr != nil {
		return "", fmt.Errorf("%s (API: %s)", ferr, err)
	}
	return fallback, nil
}

// latestTagViaAPI fetches tag_name from the GitHub latest-release
// metadata endpoint.
func (r *updateRunner) latestTagViaAPI() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), updateRequestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.apiURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := r.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
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

// resolveTagFromRedirect extracts the release tag from the redirect
// of the /latest/ checksum URL: HEAD the checksum file and read the
// final URL (pattern .../releases/download/<tag>/SHA256SUMS.txt).
func resolveTagFromRedirect(client *http.Client, downloadBase string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), updateRequestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, downloadBase+"/SHA256SUMS.txt", nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("checksum file returned %s", resp.Status)
	}
	final := ""
	if resp.Request != nil {
		final = resp.Request.URL.String()
	}
	const marker = "/releases/download/"
	i := strings.Index(final, marker)
	if i < 0 {
		return "", fmt.Errorf("checksum URL does not redirect to a release tag (%s)", final)
	}
	tag, _, _ := strings.Cut(final[i+len(marker):], "/")
	if tag == "" || tag == "latest" {
		return "", fmt.Errorf("cannot extract a release tag from %s", final)
	}
	return tag, nil
}

// fetchChecksums downloads the release's SHA256SUMS.txt from the
// tag-pinned base, so checksum and asset always come from the same
// release.
func (r *updateRunner) fetchChecksums(base string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), updateRequestTimeout)
	defer cancel()
	resp, err := r.do(ctx, http.MethodGet, base+"/SHA256SUMS.txt")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("SHA256SUMS.txt returned %s", resp.Status)
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// checksumForAsset finds the SHA-256 checksum entry for asset in a
// SHA256SUMS.txt body. Entries may name the asset with a directory
// prefix ("binaries/eka-linux-amd64") or bare ("eka-linux-amd64") —
// the match is on the basename. A malformed entry counts as missing
// (fail-closed).
func checksumForAsset(sums, asset string) (string, bool) {
	for _, line := range strings.Split(sums, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		if filepath.Base(fields[1]) != asset {
			continue
		}
		if len(fields[0]) != 64 {
			return "", false // malformed entry: fail-closed
		}
		if _, err := hex.DecodeString(fields[0]); err != nil {
			return "", false
		}
		return strings.ToLower(fields[0]), true
	}
	return "", false
}

// assetSize probes the asset's Content-Length via HEAD. A failure is
// non-fatal: the size is only presentation (the progress bar falls
// back to the indeterminate form).
func (r *updateRunner) assetSize(asset, tag string) int64 {
	ctx, cancel := context.WithTimeout(context.Background(), updateRequestTimeout)
	defer cancel()
	resp, err := r.do(ctx, http.MethodHead, r.taggedBase(tag)+"/"+asset)
	if err != nil {
		return 0
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0
	}
	return resp.ContentLength
}

// downloadAndReplace downloads the asset into the target's directory
// (same filesystem, so the final rename is atomic), verifies its
// SHA-256 checksum fail-closed and swaps it in, preserving the old
// binary as <target>.old during the swap.
func (r *updateRunner) downloadAndReplace(cmd *cobra.Command, s *ui.Style, sm *ui.Summary, asset string, rel *updateRelease) error {
	target := r.exePath
	dir := filepath.Dir(target)
	base := r.taggedBase(rel.tag)

	tmp, err := os.CreateTemp(dir, ".eka-update-*")
	if err != nil {
		return refuse(cmd, "update refused: cannot stage the download in %s: %s", dir, err)
	}
	tmpName := tmp.Name()
	// Leftover cleanup: after a successful rename the temp path no
	// longer exists and the deferred removal is a no-op.
	defer os.Remove(tmpName)

	// The interactive select no longer renders its usage tip once the
	// option is confirmed, so a blank line releases the menu frame —
	// the progress bar must not stick to the option list. This blank
	// line applies to both the interactive and --yes paths.
	fmt.Fprintln(s.W)

	bar := ui.NewDownloadBar(s, asset, rel.size)
	err = r.downloadAsset(tmp, base, asset, bar)
	if err != nil {
		tmp.Close()
		bar.Abort()
		return refuse(cmd, "update refused: download of %s failed: %s", asset, err)
	}
	if fi, err := tmp.Stat(); err == nil && fi.Size() == 0 {
		tmp.Close()
		bar.Abort()
		return refuse(cmd, "update refused: downloaded asset %s is empty", asset)
	}
	if err := tmp.Close(); err != nil {
		bar.Abort()
		return refuse(cmd, "update refused: cannot finalize the download: %s", err)
	}

	// Fail-closed verification against the checksum resolved from the
	// SAME tag-pinned release as the download.
	got, err := sha256File(tmpName)
	if err != nil {
		bar.Abort()
		return refuse(cmd, "update refused: cannot hash the downloaded %s: %s", asset, err)
	}
	if !strings.EqualFold(got, rel.checksum) {
		bar.Abort()
		return refuse(cmd, "update refused: checksum mismatch for %s (expected %s, got %s)", asset, rel.checksum, got)
	}

	if err := os.Chmod(tmpName, 0o755); err != nil {
		bar.Abort()
		return refuse(cmd, "update refused: cannot make %s executable: %s", tmpName, err)
	}
	bar.Finish()

	// Atomic replacement: rename target away, swap the verified
	// binary in, then drop the old file. Any failure after the first
	// rename restores the old binary; if even the restore fails, the
	// old binary is preserved as <target>.old and the refusal says so
	// (the recovery path must never be silent).
	old := target + ".old"
	os.Remove(old) // best-effort: a leftover .old must not block the dance
	if err := os.Rename(target, old); err != nil {
		return refuse(cmd, "update refused: cannot replace %s: %s%s", target, err, r.windowsReplaceHint())
	}
	if err := os.Rename(tmpName, target); err != nil {
		if rerr := os.Rename(old, target); rerr != nil {
			return refuse(cmd, "update refused: cannot replace %s: %s; the old binary is preserved at %s — restore it with: mv %s %s%s",
				target, err, old, old, target, r.windowsReplaceHint())
		}
		return refuse(cmd, "update refused: cannot replace %s: %s%s", target, err, r.windowsReplaceHint())
	}
	os.Remove(old) // debris cleanup; the update itself succeeded

	fmt.Fprintf(s.W, "%s\n", s.Success(ui.IconDone+" updated: "+target))
	sm.Add("Current", currentVersionLabel(r.curVersion))
	sm.Add("Latest", "eka "+rel.tag)
	sm.Add("Result", "binary replaced at "+target)
	sm.Render()
	return nil
}

// windowsReplaceHint explains the Windows replacement caveat on the
// platform where it applies.
func (r *updateRunner) windowsReplaceHint() string {
	if r.goos == "windows" {
		return " (on Windows a running binary cannot always be replaced; install.sh remains the fallback path)"
	}
	return ""
}

// downloadAsset streams the asset to w with realtime progress: done
// bytes are reported to bar per read chunk, the total comes from the
// resolution step (Content-Length), 0 renders an indeterminate bar.
// The request carries the generous updateDownloadTimeout — the body
// read is bounded by it, never by a connection-phase timeout.
func (r *updateRunner) downloadAsset(w io.Writer, base, asset string, bar *ui.DownloadBar) error {
	ctx, cancel := context.WithTimeout(context.Background(), updateDownloadTimeout)
	defer cancel()
	resp, err := r.do(ctx, http.MethodGet, base+"/"+asset)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %s", resp.Status)
	}
	_, err = io.Copy(&barWriter{w: w, bar: bar}, resp.Body)
	return err
}

// barWriter counts the streamed bytes and reports them to the bar.
type barWriter struct {
	w   io.Writer
	bar *ui.DownloadBar
	n   int64
}

func (b *barWriter) Write(p []byte) (int, error) {
	n, err := b.w.Write(p)
	b.n += int64(n)
	b.bar.Set(b.n)
	return n, err
}

// sha256File returns the lowercase hex SHA-256 of the file.
func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// compareVersions compares two EKA release versions semver-ish,
// without a dependency: an optional "v" prefix, numeric dot-segments,
// and an optional prerelease suffix ("-alpha", "-beta", "-rc" —
// prefix-matched, so "-rc.1" counts as rc). A prerelease sorts below
// the release when the main segments are equal; alpha < beta < rc.
// "dev" parses as version 0.0.0 (a dev build is always outdated).
// A version that parses as neither falls back to plain string order,
// keeping the result deterministic. Returns -1, 0 or 1.
func compareVersions(a, b string) int {
	sa, pa, oka := parseVersion(a)
	sb, pb, okb := parseVersion(b)
	if !oka || !okb {
		return strings.Compare(a, b)
	}
	for i := 0; i < len(sa) || i < len(sb); i++ {
		va, vb := 0, 0
		if i < len(sa) {
			va = sa[i]
		}
		if i < len(sb) {
			vb = sb[i]
		}
		if va != vb {
			if va < vb {
				return -1
			}
			return 1
		}
	}
	if d := prereleaseRank(pa) - prereleaseRank(pb); d != 0 {
		if d < 0 {
			return -1
		}
		return 1
	}
	return 0
}

// parseVersion splits a version into its numeric dot-segments and its
// prerelease suffix ("" for a release). Unparseable versions return
// ok = false.
func parseVersion(v string) (segments []int, prerelease string, ok bool) {
	if v == "dev" {
		return []int{0}, "", true
	}
	v = strings.TrimPrefix(v, "v")
	if suffix := strings.SplitN(v, "-", 2); len(suffix) == 2 {
		prerelease = suffix[1]
		v = suffix[0]
	}
	parts := strings.Split(v, ".")
	segments = make([]int, len(parts))
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return nil, "", false // unparseable segment: fall back to string order
		}
		segments[i] = n
	}
	if prerelease != "" {
		// Normalize the prefix-matched suffix to its base state, so
		// "-rc", "-rc.1" and "-rc2" all rank as rc.
		switch {
		case strings.HasPrefix(prerelease, "alpha"):
			prerelease = "alpha"
		case strings.HasPrefix(prerelease, "beta"):
			prerelease = "beta"
		case strings.HasPrefix(prerelease, "rc"):
			prerelease = "rc"
		default:
			return nil, "", false // unknown suffix: fall back to string order
		}
	}
	return segments, prerelease, true
}

// prereleaseRank orders the prerelease states: alpha < beta < rc <
// release.
func prereleaseRank(p string) int {
	switch p {
	case "alpha":
		return 1
	case "beta":
		return 2
	case "rc":
		return 3
	default:
		return 4
	}
}
