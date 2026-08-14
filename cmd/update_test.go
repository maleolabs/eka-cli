package cmd

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/maleolabs/eka-cli/cmd/ui"
	"github.com/spf13/cobra"
)

// The update tests are hermetic: every network request goes to an
// httptest server serving the release material from memory (release
// metadata JSON, SHA256SUMS.txt, the binary asset), and the full
// update flow runs against an injected executable path in a
// temporary directory. No real network, no real binary, no fixtures
// on disk.

// sha256Hex returns the lowercase hex SHA-256 of b.
func sha256Hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// fakeReleaseServer serves one in-memory latest release: the release
// metadata (tag_name), the tag-pinned SHA256SUMS.txt (entry for asset
// with the given hash; "" = no entry), the tag-pinned binary asset,
// and the /latest/ checksum probe that redirects to the tag-pinned
// URL (mirroring the production redirect contract). With apiDown the
// metadata endpoint fails, forcing the redirect fallback; metaDelay
// and assetHeaderDelay stall the metadata (API + probe) and the asset
// response headers, exercising the per-request deadlines.
type fakeReleaseServer struct {
	srv          *httptest.Server
	apiURL       string
	downloadBase string
}

func newFakeReleaseServer(t *testing.T, tag, asset, hash string, body []byte) *fakeReleaseServer {
	return newFakeReleaseServerOpts(t, tag, asset, hash, body, false, 0, 0)
}

func newFakeReleaseServerOpts(t *testing.T, tag, asset, hash string, body []byte,
	apiDown bool, metaDelay, assetHeaderDelay time.Duration) *fakeReleaseServer {
	t.Helper()
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/api/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		if apiDown {
			http.Error(w, "rate limited", http.StatusForbidden)
			return
		}
		if metaDelay > 0 {
			time.Sleep(metaDelay)
		}
		if r.Header.Get("Accept") != "application/vnd.github+json" {
			t.Errorf("release metadata request must carry the GitHub API Accept header")
		}
		fmt.Fprintf(w, `{"tag_name": %q}`, tag)
	})
	mux.HandleFunc("/download/", func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/download/")
		if rest == "SHA256SUMS.txt" {
			// The /latest/ probe: redirect to the tag-pinned checksum
			// (the production redirect contract).
			if metaDelay > 0 {
				time.Sleep(metaDelay)
			}
			http.Redirect(w, r, srv.URL+"/releases/download/"+tag+"/SHA256SUMS.txt", http.StatusFound)
			return
		}
		parts := strings.SplitN(rest, "/", 2)
		if len(parts) != 2 || parts[0] != tag {
			http.NotFound(w, r)
			return
		}
		switch parts[1] {
		case "SHA256SUMS.txt":
			if hash != "" {
				fmt.Fprintf(w, "%s  binaries/%s\n", hash, asset)
			}
		case asset:
			if assetHeaderDelay > 0 {
				time.Sleep(assetHeaderDelay)
			}
			w.Header().Set("Content-Length", strconv.Itoa(len(body)))
			if r.Method == http.MethodHead {
				return
			}
			w.Write(body)
		default:
			http.NotFound(w, r)
		}
	})
	// The redirect target of the /latest/ probe (any 200 body works —
	// the probe only parses the URL).
	mux.HandleFunc("/releases/download/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &fakeReleaseServer{
		srv:          srv,
		apiURL:       srv.URL + "/api/releases/latest",
		downloadBase: srv.URL + "/download",
	}
}

// pointUpdateAt redirects the production update endpoints (the
// package-level vars runUpdate builds from) at the fake server.
func pointUpdateAt(t *testing.T, srv *fakeReleaseServer) {
	t.Helper()
	oldAPI, oldBase, oldClient := updateAPIURL, updateDownloadBase, updateClient
	updateAPIURL = srv.apiURL
	updateDownloadBase = srv.downloadBase
	updateClient = srv.srv.Client()
	t.Cleanup(func() { updateAPIURL, updateDownloadBase, updateClient = oldAPI, oldBase, oldClient })
}

// setVersion pins the CLI build version for the duration of the test.
func setVersion(t *testing.T, v string) {
	t.Helper()
	old := version
	version = v
	t.Cleanup(func() { version = old })
}

// updateTestCommand builds a bare command with the given streams for
// direct runner invocations (styleFor tolerates the missing verbose
// flag).
func updateTestCommand(out, errb *bytes.Buffer) *cobra.Command {
	cmd := &cobra.Command{}
	cmd.SetOut(out)
	cmd.SetErr(errb)
	cmd.SetIn(strings.NewReader(""))
	return cmd
}

// TestUpdateCheckUpToDate: --check against a release whose tag equals
// the current version exits 0 and reports "already up to date".
func TestUpdateCheckUpToDate(t *testing.T) {
	srv := newFakeReleaseServer(t, "v1.0.0", "eka-linux-amd64", sha256Hex([]byte("new")), []byte("new"))
	pointUpdateAt(t, srv)
	setVersion(t, "v1.0.0")

	code, out, errText := runIn([]string{"update", "--check"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", code, out, errText)
	}
	for _, want := range []string{
		"Update", "Current   eka v1.0.0", "Latest    eka v1.0.0",
		"Asset     eka-linux-amd64 (3 B)", "↓ Check", "already up to date",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	if errText != "" {
		t.Errorf("stderr must be empty, got %q", errText)
	}
}

// TestUpdateCheckUpdateAvailable: --check against a newer release
// exits 1, prints the Summary with status "update available" and
// emits no "eka:" stderr noise (exitError prints nothing).
func TestUpdateCheckUpdateAvailable(t *testing.T) {
	srv := newFakeReleaseServer(t, "v1.0.1", "eka-linux-amd64", sha256Hex([]byte("new")), []byte("new"))
	pointUpdateAt(t, srv)
	setVersion(t, "v1.0.0")

	code, out, errText := runIn([]string{"update", "--check"})
	if code != 1 {
		t.Fatalf("exit = %d, want 1 (update available)\nstdout: %s\nstderr: %s", code, out, errText)
	}
	if !strings.Contains(out, "update available") {
		t.Errorf("output must report the update status:\n%s", out)
	}
	if !strings.Contains(out, "↓ Check") {
		t.Errorf("--check must render the Check pipeline line:\n%s", out)
	}
	if errText != "" {
		t.Errorf("--check must not print stderr noise, got %q", errText)
	}
}

// TestUpdateFullFlow: the happy path — the runner downloads the
// verified asset, replaces the injected target binary, removes the
// .old file, leaves no temp debris and exits 0.
func TestUpdateFullFlow(t *testing.T) {
	newBinary := []byte("the new eka binary")
	srv := newFakeReleaseServer(t, "v1.0.1", "eka-linux-amd64", sha256Hex(newBinary), newBinary)
	dir := t.TempDir()
	target := filepath.Join(dir, "eka")
	if err := os.WriteFile(target, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}

	r := &updateRunner{
		apiURL: srv.apiURL, downloadBase: srv.downloadBase, client: srv.srv.Client(),
		exePath: target, goos: "linux", goarch: "amd64", curVersion: "v1.0.0",
	}
	var out, errb bytes.Buffer
	cmd := updateTestCommand(&out, &errb)
	if err := r.run(cmd, &updateFlags{yes: true}); err != nil {
		t.Fatalf("run: %v\nstderr: %s", err, errb.String())
	}

	got, err := os.ReadFile(target)
	if err != nil || !bytes.Equal(got, newBinary) {
		t.Errorf("target binary must be replaced with the verified asset (err %v)", err)
	}
	if _, err := os.Stat(target + ".old"); !os.IsNotExist(err) {
		t.Error(".old backup must be removed after a successful update")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "eka" {
		t.Errorf("directory must hold only the new binary, found %v", entries)
	}
	for _, want := range []string{
		"✓ updated: " + target, "Current", "Latest", "binary replaced at " + target,
		"↓ downloading eka-linux-amd64...", "✓ downloaded eka-linux-amd64 (18 B)",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output missing %q:\n%s", want, out.String())
		}
	}
	if !strings.Contains(out.String(), "  ↓ Update\n\n  ↓ downloading eka-linux-amd64...") {
		t.Errorf("output must contain a blank line between the header pipeline and the download line:\n%s", out.String())
	}
	if strings.Contains(out.String(), "\x1b") || strings.Contains(errb.String(), "\x1b") {
		t.Error("non-TTY output must not contain ANSI escapes")
	}
}

// TestUpdateChecksumMismatch: a wrong hash refuses the update with
// exit 1, names the asset, cleans the temp file and leaves the
// original binary untouched.
func TestUpdateChecksumMismatch(t *testing.T) {
	srv := newFakeReleaseServer(t, "v1.0.1", "eka-linux-amd64",
		sha256Hex([]byte("the expected binary")), []byte("a tampered binary"))
	dir := t.TempDir()
	target := filepath.Join(dir, "eka")
	original := []byte("old binary")
	if err := os.WriteFile(target, original, 0o755); err != nil {
		t.Fatal(err)
	}

	r := &updateRunner{
		apiURL: srv.apiURL, downloadBase: srv.downloadBase, client: srv.srv.Client(),
		exePath: target, goos: "linux", goarch: "amd64", curVersion: "v1.0.0",
	}
	var out, errb bytes.Buffer
	err := r.run(updateTestCommand(&out, &errb), &updateFlags{yes: true})
	if code := exitCodeOf(err); code != 1 {
		t.Fatalf("exit = %d, want 1 (refusal)\nstderr: %s", code, errb.String())
	}
	if !strings.Contains(errb.String(), "checksum mismatch") || !strings.Contains(errb.String(), "eka-linux-amd64") {
		t.Errorf("refusal must name the mismatch and the asset, got %q", errb.String())
	}
	if got, _ := os.ReadFile(target); !bytes.Equal(got, original) {
		t.Error("a refused update must leave the original binary untouched")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("the temp download must be cleaned up, found %v", entries)
	}
}

// TestUpdateMissingChecksumEntry: a SHA256SUMS.txt without the asset
// entry refuses fail-closed at resolution — before anything is
// downloaded.
func TestUpdateMissingChecksumEntry(t *testing.T) {
	srv := newFakeReleaseServer(t, "v1.0.1", "eka-linux-amd64", "", []byte("new"))
	dir := t.TempDir()
	target := filepath.Join(dir, "eka")
	if err := os.WriteFile(target, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}

	r := &updateRunner{
		apiURL: srv.apiURL, downloadBase: srv.downloadBase, client: srv.srv.Client(),
		exePath: target, goos: "linux", goarch: "amd64", curVersion: "v1.0.0",
	}
	var out, errb bytes.Buffer
	err := r.run(updateTestCommand(&out, &errb), &updateFlags{yes: true})
	if code := exitCodeOf(err); code != 1 {
		t.Fatalf("exit = %d, want 1 (refusal)\nstderr: %s", code, errb.String())
	}
	if !strings.Contains(errb.String(), "no entry") || !strings.Contains(errb.String(), "eka-linux-amd64") {
		t.Errorf("refusal must explain the missing entry, got %q", errb.String())
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("the temp download must be cleaned up, found %v", entries)
	}
}

// exitCodeOf maps a command error to its exit code (nil = 0).
func exitCodeOf(err error) int {
	if err == nil {
		return exitOK
	}
	var ee *exitError
	if errors.As(err, &ee) {
		return ee.code
	}
	return exitUsage
}

// TestUpdateNonTTYWithoutYes: a non-interactive run with an update
// available is a usage error (exit 2) carrying the --yes hint.
func TestUpdateNonTTYWithoutYes(t *testing.T) {
	srv := newFakeReleaseServer(t, "v1.0.1", "eka-linux-amd64", sha256Hex([]byte("new")), []byte("new"))
	pointUpdateAt(t, srv)
	setVersion(t, "v1.0.0")

	code, _, errText := runIn([]string{"update"})
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (usage)\nstderr: %s", code, errText)
	}
	if !strings.Contains(errText, "--yes") {
		t.Errorf("stderr must hint --yes, got %q", errText)
	}
}

// TestUpdateAssetName: the installer asset naming contract — linux /
// darwin / windows with .exe, and refusals for unsupported platforms.
func TestUpdateAssetName(t *testing.T) {
	cases := []struct {
		goos, goarch string
		want         string
		wantErr      bool
	}{
		{"linux", "amd64", "eka-linux-amd64", false},
		{"linux", "arm64", "eka-linux-arm64", false},
		{"darwin", "amd64", "eka-darwin-amd64", false},
		{"darwin", "arm64", "eka-darwin-arm64", false},
		{"windows", "amd64", "eka-windows-amd64.exe", false},
		{"windows", "arm64", "eka-windows-arm64.exe", false},
		{"freebsd", "amd64", "", true},
		{"linux", "386", "", true},
		{"windows", "386", "", true},
	}
	for _, c := range cases {
		got, err := updateAssetName(c.goos, c.goarch)
		if c.wantErr {
			if err == nil {
				t.Errorf("%s/%s: must refuse, got asset %q", c.goos, c.goarch, got)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("%s/%s: got %q, %v; want %q", c.goos, c.goarch, got, err, c.want)
		}
	}

	// The refusal path: an unsupported platform refuses before any
	// network access.
	r := &updateRunner{goos: "freebsd", goarch: "amd64"}
	var out, errb bytes.Buffer
	err := r.run(updateTestCommand(&out, &errb), &updateFlags{check: true})
	if code := exitCodeOf(err); code != 1 {
		t.Fatalf("exit = %d, want 1 (refusal)\nstderr: %s", code, errb.String())
	}
	if !strings.Contains(errb.String(), "unsupported OS") {
		t.Errorf("refusal must name the unsupported OS, got %q", errb.String())
	}
}

// TestUpdateCompareVersions: the semver-ish comparison helper.
func TestUpdateCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"v0.6.3", "v0.6.4", -1},
		{"v1.0.0", "v0.9.9", 1},
		{"v0.6.3-alpha", "v0.6.3", -1},
		{"v0.6.3-beta", "v0.6.3-alpha", 1},
		{"v0.6.3-rc.1", "v0.6.3-rc", 0}, // rc.1 == rc (ranked)
		{"v0.6.3-rc", "v0.6.3", -1},
		{"v1.2.3", "v1.2.3", 0},
		{"v1.2.3", "v1.2.4", -1},
		{"v0.10.0", "v0.9.9", 1},
		{"1.2.3", "v1.2.3", 0}, // v prefix optional
		{"dev", "v0.0.1", -1},
		{"dev", "dev", 0},
	}
	for _, c := range cases {
		if got := compareVersions(c.a, c.b); got != c.want {
			t.Errorf("compareVersions(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

// TestUpdateDownloadBarDeterminism: the non-TTY bar prints exactly
// the two deterministic lines, no control sequences, and Finish is
// idempotent. (The TTY redraw/throttle behavior is covered by the
// package ui tests in downloadbar_test.go.)
func TestUpdateDownloadBarDeterminism(t *testing.T) {
	var buf bytes.Buffer
	s := &ui.Style{Color: false, TTY: false, W: &buf}
	bar := ui.NewDownloadBar(s, "eka-linux-amd64", 12_000_000)
	bar.Set(3 * 1024 * 1024) // no-ops on non-TTY
	bar.Set(8 * 1024 * 1024)
	bar.Finish()
	bar.Finish() // idempotent

	out := buf.String()
	if strings.Contains(out, "\x1b") || strings.Contains(out, "\r") {
		t.Errorf("non-TTY output must not contain control sequences, got %q", out)
	}
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("exactly two deterministic lines expected, got %d: %q", len(lines), out)
	}
	if lines[0] != "↓ downloading eka-linux-amd64..." {
		t.Errorf("start line = %q, want %q", lines[0], "↓ downloading eka-linux-amd64...")
	}
	if lines[1] != "✓ downloaded eka-linux-amd64 (12 MB)" {
		t.Errorf("final line = %q, want %q", lines[1], "✓ downloaded eka-linux-amd64 (12 MB)")
	}
}

// TestUpdateHelpExitsZero: the update help exits 0 and documents the
// flags and the exit codes.
func TestUpdateHelpExitsZero(t *testing.T) {
	code, out, _ := runIn([]string{"update", "--help"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	for _, want := range []string{"--check", "--yes", "Exit codes:", "update available (--check)"} {
		if !strings.Contains(out, want) {
			t.Errorf("update help must document %q:\n%s", want, out)
		}
	}
}

// TestUpdateReplaceFailureKeepsOld: when the first rename of the
// atomic dance fails (a leftover <target>.old directory here), the
// run refuses with exit 1, the original binary stays untouched and
// the temp download is cleaned up.
func TestUpdateReplaceFailureKeepsOld(t *testing.T) {
	newBinary := []byte("the new eka binary")
	srv := newFakeReleaseServer(t, "v1.0.1", "eka-linux-amd64", sha256Hex(newBinary), newBinary)
	dir := t.TempDir()
	target := filepath.Join(dir, "eka")
	original := []byte("old binary")
	if err := os.WriteFile(target, original, 0o755); err != nil {
		t.Fatal(err)
	}
	// A NON-EMPTY directory at <target>.old makes both the best-effort
	// pre-removal and the first rename fail (renaming a file onto an
	// existing non-empty directory is refused).
	if err := os.Mkdir(target+".old", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target+".old/leftover", []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	r := &updateRunner{
		apiURL: srv.apiURL, downloadBase: srv.downloadBase, client: srv.srv.Client(),
		exePath: target, goos: "linux", goarch: "amd64", curVersion: "v1.0.0",
	}
	var out, errb bytes.Buffer
	err := r.run(updateTestCommand(&out, &errb), &updateFlags{yes: true})
	if code := exitCodeOf(err); code != 1 {
		t.Fatalf("exit = %d, want 1 (refusal)\nstderr: %s", code, errb.String())
	}
	if !strings.Contains(errb.String(), "cannot replace") {
		t.Errorf("refusal must explain the replace failure, got %q", errb.String())
	}
	if got, _ := os.ReadFile(target); !bytes.Equal(got, original) {
		t.Error("a failed replace must leave the original binary untouched")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 { // the binary + the .old directory (with its leftover)
		t.Errorf("the temp download must be cleaned up, found %v", entries)
	}
}

// TestUpdateZeroByteDownload: an asset whose checksum matches but is
// empty is refused — an empty binary can never be a valid update.
func TestUpdateZeroByteDownload(t *testing.T) {
	srv := newFakeReleaseServer(t, "v1.0.1", "eka-linux-amd64", sha256Hex([]byte("")), []byte(""))
	dir := t.TempDir()
	target := filepath.Join(dir, "eka")
	original := []byte("old binary")
	if err := os.WriteFile(target, original, 0o755); err != nil {
		t.Fatal(err)
	}

	r := &updateRunner{
		apiURL: srv.apiURL, downloadBase: srv.downloadBase, client: srv.srv.Client(),
		exePath: target, goos: "linux", goarch: "amd64", curVersion: "v1.0.0",
	}
	var out, errb bytes.Buffer
	err := r.run(updateTestCommand(&out, &errb), &updateFlags{yes: true})
	if code := exitCodeOf(err); code != 1 {
		t.Fatalf("exit = %d, want 1 (refusal)\nstderr: %s", code, errb.String())
	}
	if !strings.Contains(errb.String(), "empty") {
		t.Errorf("refusal must report the empty download, got %q", errb.String())
	}
	if got, _ := os.ReadFile(target); !bytes.Equal(got, original) {
		t.Error("a refused update must leave the original binary untouched")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("the temp download must be cleaned up, found %v", entries)
	}
}

// TestUpdateMalformedChecksumEntry: a SHA256SUMS.txt whose entry for
// the asset is malformed (wrong hash length, non-hex) refuses
// fail-closed at resolution.
func TestUpdateMalformedChecksumEntry(t *testing.T) {
	srv := newFakeReleaseServer(t, "v1.0.1", "eka-linux-amd64", "not-a-64-char-hash", []byte("new"))
	dir := t.TempDir()
	target := filepath.Join(dir, "eka")
	if err := os.WriteFile(target, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}

	r := &updateRunner{
		apiURL: srv.apiURL, downloadBase: srv.downloadBase, client: srv.srv.Client(),
		exePath: target, goos: "linux", goarch: "amd64", curVersion: "v1.0.0",
	}
	var out, errb bytes.Buffer
	err := r.run(updateTestCommand(&out, &errb), &updateFlags{yes: true})
	if code := exitCodeOf(err); code != 1 {
		t.Fatalf("exit = %d, want 1 (refusal)\nstderr: %s", code, errb.String())
	}
	if !strings.Contains(errb.String(), "no entry") {
		t.Errorf("a malformed entry must refuse fail-closed, got %q", errb.String())
	}
}

// TestUpdateChecksumForAsset: the SHA256SUMS.txt entry matcher —
// bare and directory-prefixed names, case-insensitive hashes,
// malformed entries refusing fail-closed.
func TestUpdateChecksumForAsset(t *testing.T) {
	hex64 := strings.Repeat("a", 64)
	cases := []struct {
		name string
		sums string
		want string
		ok   bool
	}{
		{"bare name", hex64 + "  eka-linux-amd64", hex64, true},
		{"prefixed name", hex64 + "  binaries/eka-linux-amd64", hex64, true},
		{"uppercase hash", strings.ToUpper(hex64) + "  eka-linux-amd64", hex64, true},
		{"other asset", hex64 + "  eka-darwin-arm64", "", false},
		{"wrong length", "abc  eka-linux-amd64", "", false},
		{"non-hex", strings.Repeat("z", 64) + "  eka-linux-amd64", "", false},
		{"garbage line", "not a checksum line", "", false},
		{"empty body", "", "", false},
	}
	for _, c := range cases {
		got, ok := checksumForAsset(c.sums, "eka-linux-amd64")
		if ok != c.ok || got != c.want {
			t.Errorf("%s: got (%q, %v), want (%q, %v)", c.name, got, ok, c.want, c.ok)
		}
	}
}

// TestUpdateRedirectFallback: when the GitHub API is unavailable
// (rate-limited), the /latest/ redirect probe resolves the tag and
// the run continues — --check still reports the update.
func TestUpdateRedirectFallback(t *testing.T) {
	srv := newFakeReleaseServerOpts(t, "v1.0.1", "eka-linux-amd64",
		sha256Hex([]byte("new")), []byte("new"), true, 0, 0) // apiDown
	pointUpdateAt(t, srv)
	setVersion(t, "v1.0.0")

	code, out, errText := runIn([]string{"update", "--check"})
	if code != 1 {
		t.Fatalf("exit = %d, want 1 (update available)\nstdout: %s\nstderr: %s", code, out, errText)
	}
	if !strings.Contains(out, "update available") || !strings.Contains(out, "eka v1.0.1") {
		t.Errorf("the redirect fallback must resolve the latest release:\n%s", out)
	}
	if strings.Contains(errText, "cannot resolve") {
		t.Errorf("the fallback must not refuse, got %q", errText)
	}
}

// setUpdateTimeout shrinks the metadata request deadline for the
// duration of the test (the download deadline is deliberately left
// untouched — it is the value under test).
func setUpdateTimeout(t *testing.T, v time.Duration) {
	t.Helper()
	old := updateRequestTimeout
	updateRequestTimeout = v
	t.Cleanup(func() { updateRequestTimeout = old })
}

// TestUpdateClientNoGlobalTimeout: the update client must not carry a
// Client.Timeout — a global cap would kill a slow-but-progressing
// download mid-body. The connection-phase timeouts live on the
// transport instead.
func TestUpdateClientNoGlobalTimeout(t *testing.T) {
	if updateClient.Timeout != 0 {
		t.Errorf("the client must not carry a global timeout, got %v", updateClient.Timeout)
	}
	tr, ok := updateClient.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("the client must use the phase-timed transport, got %T", updateClient.Transport)
	}
	if tr.ResponseHeaderTimeout != updateResponseHeaderTimeout {
		t.Errorf("ResponseHeaderTimeout = %v, want %v", tr.ResponseHeaderTimeout, updateResponseHeaderTimeout)
	}
	if tr.TLSHandshakeTimeout != updateTLSHandshakeTimeout {
		t.Errorf("TLSHandshakeTimeout = %v, want %v", tr.TLSHandshakeTimeout, updateTLSHandshakeTimeout)
	}
}

// TestUpdateDownloadExceedsRequestTimeout: the asset delays its
// response headers well past the (shrunk) metadata deadline. The
// download must still succeed — the download request carries
// updateDownloadTimeout, never the metadata cap. This is the exact
// regression of the old 60s Client.Timeout killing a download at
// >50% progress.
func TestUpdateDownloadExceedsRequestTimeout(t *testing.T) {
	setUpdateTimeout(t, 100*time.Millisecond)
	newBinary := []byte("the new eka binary")
	srv := newFakeReleaseServerOpts(t, "v1.0.1", "eka-linux-amd64",
		sha256Hex(newBinary), newBinary, false, 0, 400*time.Millisecond)
	dir := t.TempDir()
	target := filepath.Join(dir, "eka")
	if err := os.WriteFile(target, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}

	r := &updateRunner{
		apiURL: srv.apiURL, downloadBase: srv.downloadBase, client: srv.srv.Client(),
		exePath: target, goos: "linux", goarch: "amd64", curVersion: "v1.0.0",
	}
	var out, errb bytes.Buffer
	if err := r.run(updateTestCommand(&out, &errb), &updateFlags{yes: true}); err != nil {
		t.Fatalf("a download slower than the metadata deadline must still succeed: %v\nstderr: %s", err, errb.String())
	}
	got, err := os.ReadFile(target)
	if err != nil || !bytes.Equal(got, newBinary) {
		t.Errorf("target binary must be replaced with the verified asset (err %v)", err)
	}
}

// TestUpdateMetadataTimeoutRefuses: a dead metadata endpoint (API and
// fallback probe both stalled past the deadline) refuses fast with
// the deterministic message — connection failures are never a hang.
func TestUpdateMetadataTimeoutRefuses(t *testing.T) {
	setUpdateTimeout(t, 100*time.Millisecond)
	srv := newFakeReleaseServerOpts(t, "v1.0.1", "eka-linux-amd64",
		sha256Hex([]byte("new")), []byte("new"), false, 400*time.Millisecond, 0)
	pointUpdateAt(t, srv)
	setVersion(t, "v1.0.0")

	code, _, errText := runIn([]string{"update", "--check"})
	if code != 1 {
		t.Fatalf("exit = %d, want 1 (refusal)\nstderr: %s", code, errText)
	}
	if !strings.Contains(errText, "cannot resolve") {
		t.Errorf("a stalled metadata endpoint must refuse with the resolution error, got %q", errText)
	}
}
