package cmd

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/maleolabs/eka-cli/plugin"
)

// The plugin install tests are hermetic: every network request goes to
// an httptest server serving the release material from memory (release
// metadata JSON, SHA256SUMS.txt, the binary asset), and the install
// runs against an injected plugin directory in a temporary directory.
// The downloaded "binary" is a shell script implementing the plugin
// contract, so the smoke check (running "manifest") works without a
// real plugin. No real network, no real binary, no fixtures on disk.

// pluginManifestScript is the downloaded "binary" of the happy-path
// tests: a shell script answering "manifest" with a valid plugin
// manifest in the eka-mcp shape (contract pin: capabilities + source).
const pluginManifestScript = `#!/bin/sh
case "$1" in
  manifest)
    printf '%s' '{"contract":"v1","name":"mcp","version":"1.0.0","description":"fake","artifacts":[],"capabilities":["install","mcp"],"source":"github.com/maleolabs/eka-mcp"}'
    ;;
esac
`

// fakePluginReleaseServer serves one in-memory latest release of a
// plugin repository: the release metadata (tag_name) — asserting the
// GitHub API's well-formed User-Agent — the tag-pinned SHA256SUMS.txt
// (entry for asset with the given hash; "" = no entry) and the
// tag-pinned binary asset.
type fakePluginReleaseServer struct {
	srv          *httptest.Server
	apiBase      string
	downloadRoot string
}

func newFakePluginReleaseServer(t *testing.T, repo plugin.Repo, tag, asset, hash string, body []byte) *fakePluginReleaseServer {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/"+repo.String()+"/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		if ua := r.Header.Get("User-Agent"); ua == "" || !strings.HasPrefix(ua, "eka-cli/") {
			t.Errorf("release metadata request must carry a well-formed eka-cli User-Agent, got %q", ua)
		}
		if r.Header.Get("Accept") != "application/vnd.github+json" {
			t.Errorf("release metadata request must carry the GitHub API Accept header")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"tag_name":` + strconv.Quote(tag) + `}`))
	})
	mux.HandleFunc("/"+repo.String()+"/releases/download/", func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/"+repo.String()+"/releases/download/")
		parts := strings.SplitN(rest, "/", 2)
		if len(parts) != 2 || parts[0] != tag {
			http.NotFound(w, r)
			return
		}
		switch parts[1] {
		case "SHA256SUMS.txt":
			if hash != "" {
				w.Write([]byte(hash + "  binaries/" + asset + "\n"))
			}
		case asset:
			w.Header().Set("Content-Length", strconv.Itoa(len(body)))
			if r.Method == http.MethodHead {
				return
			}
			w.Write(body)
		default:
			http.NotFound(w, r)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &fakePluginReleaseServer{srv: srv, apiBase: srv.URL, downloadRoot: srv.URL}
}

// testPluginInstallRunner builds a runner against the fake server (nil
// server = a dead endpoint, so an accidental network access fails
// loudly) with a pinned linux/amd64 platform and the given plugin dir.
func testPluginInstallRunner(srv *fakePluginReleaseServer, pluginDir string) *pluginInstallRunner {
	apiBase, downloadRoot := "http://127.0.0.1:1", "http://127.0.0.1:1"
	var client *http.Client
	if srv != nil {
		apiBase, downloadRoot = srv.apiBase, srv.downloadRoot
		if srv.srv != nil {
			client = srv.srv.Client()
		}
	}
	return &pluginInstallRunner{
		resolve:      plugin.OfficialRegistry.Lookup,
		apiBase:      apiBase,
		downloadRoot: downloadRoot,
		client:       client,
		pluginDir:    pluginDir,
		goos:         "linux",
		goarch:       "amd64",
		version:      "dev",
	}
}

// pluginDirEntries lists the files of a plugin directory ("" when the
// directory does not exist).
func pluginDirEntries(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// TestPluginInstallHappyPath: the full flow — resolve mcp through the
// registry, download the verified asset, install it as an executable
// eka-mcp, smoke-check the manifest and leave no temp debris. The
// installed plugin is then discoverable via plugin.Discover (acceptance
// criterion 1).
func TestPluginInstallHappyPath(t *testing.T) {
	body := []byte(pluginManifestScript)
	srv := newFakePluginReleaseServer(t, plugin.Repo{Owner: "maleolabs", Name: "eka-mcp"},
		"v1.0.0", "eka-mcp-linux-amd64", sha256Hex(body), body)
	dir := t.TempDir()

	r := testPluginInstallRunner(srv, dir)
	var out, errb bytes.Buffer
	if err := r.run(updateTestCommand(&out, &errb), "mcp"); err != nil {
		t.Fatalf("run: %v\nstderr: %s", err, errb.String())
	}

	target := filepath.Join(dir, "eka-mcp")
	got, err := os.ReadFile(target)
	if err != nil || !bytes.Equal(got, body) {
		t.Errorf("installed binary must equal the verified asset (err %v)", err)
	}
	fi, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o755 {
		t.Errorf("installed binary mode = %v, want 0755", fi.Mode().Perm())
	}
	if names := pluginDirEntries(t, dir); len(names) != 1 || names[0] != "eka-mcp" {
		t.Errorf("plugin dir must hold only eka-mcp, found %v", names)
	}
	for _, want := range []string{
		"Install", "Plugin    mcp", "Repo      maleolabs/eka-mcp", "Version   v1.0.0",
		"Asset     eka-mcp-linux-amd64", "✓ installed: " + target, "Installed",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output missing %q:\n%s", want, out.String())
		}
	}
	if errb.String() != "" {
		t.Errorf("stderr must be empty, got %q", errb.String())
	}

	// Acceptance criterion 1: the installed plugin is discoverable by
	// the Discover contract (EKA_PLUGIN_DIR is the plugin dir).
	t.Setenv("EKA_PLUGIN_DIR", dir)
	t.Setenv("PATH", t.TempDir())
	plugins, err := plugin.Discover("")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	found := false
	for _, p := range plugins {
		if filepath.Base(p.Exe) == "eka-mcp" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("installed plugin must be discoverable via plugin.Discover, got %+v", plugins)
	}
}

// TestPluginInstallUnknownName: an unregistered name refuses with the
// list of official plugins, before any network access.
func TestPluginInstallUnknownName(t *testing.T) {
	r := testPluginInstallRunner(nil, t.TempDir())
	var out, errb bytes.Buffer
	err := r.run(updateTestCommand(&out, &errb), "nope")
	if code := exitCodeOf(err); code != 1 {
		t.Fatalf("exit = %d, want 1 (refusal)\nstderr: %s", code, errb.String())
	}
	if !strings.Contains(errb.String(), "unknown plugin \"nope\"") || !strings.Contains(errb.String(), "mcp") {
		t.Errorf("refusal must name the plugin and list official plugins, got %q", errb.String())
	}
}

// TestPluginInstallChecksumMismatch: a wrong hash refuses fail-closed,
// names the asset, cleans the partial download and installs nothing.
func TestPluginInstallChecksumMismatch(t *testing.T) {
	srv := newFakePluginReleaseServer(t, plugin.Repo{Owner: "maleolabs", Name: "eka-mcp"},
		"v1.0.0", "eka-mcp-linux-amd64", sha256Hex([]byte("the expected binary")), []byte("a tampered binary"))
	dir := t.TempDir()

	r := testPluginInstallRunner(srv, dir)
	var out, errb bytes.Buffer
	err := r.run(updateTestCommand(&out, &errb), "mcp")
	if code := exitCodeOf(err); code != 1 {
		t.Fatalf("exit = %d, want 1 (refusal)\nstderr: %s", code, errb.String())
	}
	if !strings.Contains(errb.String(), "checksum mismatch") || !strings.Contains(errb.String(), "eka-mcp-linux-amd64") {
		t.Errorf("refusal must name the mismatch and the asset, got %q", errb.String())
	}
	if names := pluginDirEntries(t, dir); len(names) != 0 {
		t.Errorf("the partial download must be cleaned up, found %v", names)
	}
}

// TestPluginInstallMissingChecksumEntry: a SHA256SUMS.txt without the
// asset entry refuses fail-closed before anything is downloaded.
func TestPluginInstallMissingChecksumEntry(t *testing.T) {
	srv := newFakePluginReleaseServer(t, plugin.Repo{Owner: "maleolabs", Name: "eka-mcp"},
		"v1.0.0", "eka-mcp-linux-amd64", "", []byte("new"))
	dir := t.TempDir()

	r := testPluginInstallRunner(srv, dir)
	var out, errb bytes.Buffer
	err := r.run(updateTestCommand(&out, &errb), "mcp")
	if code := exitCodeOf(err); code != 1 {
		t.Fatalf("exit = %d, want 1 (refusal)\nstderr: %s", code, errb.String())
	}
	if !strings.Contains(errb.String(), "no entry") || !strings.Contains(errb.String(), "eka-mcp-linux-amd64") {
		t.Errorf("refusal must explain the missing entry, got %q", errb.String())
	}
	if names := pluginDirEntries(t, dir); len(names) != 0 {
		t.Errorf("nothing must be installed, found %v", names)
	}
}

// TestPluginInstallMalformedChecksumEntry: a malformed checksum entry
// (wrong length, non-hex) refuses fail-closed — never install
// unverified.
func TestPluginInstallMalformedChecksumEntry(t *testing.T) {
	srv := newFakePluginReleaseServer(t, plugin.Repo{Owner: "maleolabs", Name: "eka-mcp"},
		"v1.0.0", "eka-mcp-linux-amd64", "not-a-64-char-hash", []byte("new"))
	dir := t.TempDir()

	r := testPluginInstallRunner(srv, dir)
	var out, errb bytes.Buffer
	err := r.run(updateTestCommand(&out, &errb), "mcp")
	if code := exitCodeOf(err); code != 1 {
		t.Fatalf("exit = %d, want 1 (refusal)\nstderr: %s", code, errb.String())
	}
	if !strings.Contains(errb.String(), "no entry") {
		t.Errorf("a malformed entry must refuse fail-closed, got %q", errb.String())
	}
	if names := pluginDirEntries(t, dir); len(names) != 0 {
		t.Errorf("nothing must be installed, found %v", names)
	}
}

// TestPluginInstallBrokenManifest: the checksum verifies but the
// installed binary cannot produce a parseable manifest — the smoke
// check refuses and the installed binary is removed.
func TestPluginInstallBrokenManifest(t *testing.T) {
	body := []byte(`#!/bin/sh
printf '%s' 'this is not json'
`)
	srv := newFakePluginReleaseServer(t, plugin.Repo{Owner: "maleolabs", Name: "eka-mcp"},
		"v1.0.0", "eka-mcp-linux-amd64", sha256Hex(body), body)
	dir := t.TempDir()

	r := testPluginInstallRunner(srv, dir)
	var out, errb bytes.Buffer
	err := r.run(updateTestCommand(&out, &errb), "mcp")
	if code := exitCodeOf(err); code != 1 {
		t.Fatalf("exit = %d, want 1 (refusal)\nstderr: %s", code, errb.String())
	}
	if !strings.Contains(errb.String(), "smoke check") {
		t.Errorf("refusal must report the failed smoke check, got %q", errb.String())
	}
	if names := pluginDirEntries(t, dir); len(names) != 0 {
		t.Errorf("a broken plugin must be removed after the failed smoke check, found %v", names)
	}
}

// TestPluginInstallManifestNameMismatch: the installed binary's
// manifest reports a different name — the smoke check refuses and
// removes the binary.
func TestPluginInstallManifestNameMismatch(t *testing.T) {
	body := []byte(`#!/bin/sh
case "$1" in
  manifest) printf '%s' '{"contract":"v1","name":"other","version":"1.0.0","artifacts":[]}' ;;
esac
`)
	srv := newFakePluginReleaseServer(t, plugin.Repo{Owner: "maleolabs", Name: "eka-mcp"},
		"v1.0.0", "eka-mcp-linux-amd64", sha256Hex(body), body)
	dir := t.TempDir()

	r := testPluginInstallRunner(srv, dir)
	var out, errb bytes.Buffer
	err := r.run(updateTestCommand(&out, &errb), "mcp")
	if code := exitCodeOf(err); code != 1 {
		t.Fatalf("exit = %d, want 1 (refusal)\nstderr: %s", code, errb.String())
	}
	if !strings.Contains(errb.String(), "name \"other\", want \"mcp\"") {
		t.Errorf("refusal must report the name mismatch, got %q", errb.String())
	}
	if names := pluginDirEntries(t, dir); len(names) != 0 {
		t.Errorf("a mismatched plugin must be removed, found %v", names)
	}
}

// TestPluginInstallRateLimited: the GitHub API refusing with
// X-RateLimit-Remaining: 0 yields a clear rate-limit error.
func TestPluginInstallRateLimited(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		http.Error(w, "rate limited", http.StatusForbidden)
	}))
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	r := testPluginInstallRunner(&fakePluginReleaseServer{apiBase: srv.URL, downloadRoot: srv.URL}, dir)
	r.client = srv.Client()

	var out, errb bytes.Buffer
	err := r.run(updateTestCommand(&out, &errb), "mcp")
	if code := exitCodeOf(err); code != 1 {
		t.Fatalf("exit = %d, want 1 (refusal)\nstderr: %s", code, errb.String())
	}
	if !strings.Contains(errb.String(), "rate limit") {
		t.Errorf("refusal must explain the rate limit, got %q", errb.String())
	}
	if names := pluginDirEntries(t, dir); len(names) != 0 {
		t.Errorf("nothing must be installed, found %v", names)
	}
}

// TestPluginInstallReinstallOverwrites: re-installing over an existing
// plugin overwrites it cleanly with a printed notice and restores the
// verified content.
func TestPluginInstallReinstallOverwrites(t *testing.T) {
	body := []byte(pluginManifestScript)
	srv := newFakePluginReleaseServer(t, plugin.Repo{Owner: "maleolabs", Name: "eka-mcp"},
		"v1.0.0", "eka-mcp-linux-amd64", sha256Hex(body), body)
	dir := t.TempDir()

	r := testPluginInstallRunner(srv, dir)
	var first, errb1 bytes.Buffer
	if err := r.run(updateTestCommand(&first, &errb1), "mcp"); err != nil {
		t.Fatalf("first install: %v\nstderr: %s", err, errb1.String())
	}

	target := filepath.Join(dir, "eka-mcp")
	if err := os.WriteFile(target, []byte("tampered"), 0o755); err != nil {
		t.Fatal(err)
	}
	var second, errb2 bytes.Buffer
	if err := r.run(updateTestCommand(&second, &errb2), "mcp"); err != nil {
		t.Fatalf("reinstall: %v\nstderr: %s", err, errb2.String())
	}
	if !strings.Contains(second.String(), "replacing the existing eka-mcp installation") {
		t.Errorf("reinstall must print the replacement notice:\n%s", second.String())
	}
	got, err := os.ReadFile(target)
	if err != nil || !bytes.Equal(got, body) {
		t.Errorf("reinstall must restore the verified asset (err %v)", err)
	}
}

// TestPluginInstallUnsupportedPlatform: an unsupported platform refuses
// before any network access.
func TestPluginInstallUnsupportedPlatform(t *testing.T) {
	r := testPluginInstallRunner(nil, t.TempDir())
	r.goos, r.goarch = "freebsd", "amd64"
	var out, errb bytes.Buffer
	err := r.run(updateTestCommand(&out, &errb), "mcp")
	if code := exitCodeOf(err); code != 1 {
		t.Fatalf("exit = %d, want 1 (refusal)\nstderr: %s", code, errb.String())
	}
	if !strings.Contains(errb.String(), "unsupported OS") {
		t.Errorf("refusal must name the unsupported OS, got %q", errb.String())
	}
}

// TestPluginAssetName: the plugin asset naming contract —
// eka-<name>-<os>-<arch>[.exe], sharing the platform validation of the
// update command.
func TestPluginAssetName(t *testing.T) {
	cases := []struct {
		prefix, goos, goarch string
		want                 string
		wantErr              bool
	}{
		{"eka-mcp", "linux", "amd64", "eka-mcp-linux-amd64", false},
		{"eka-mcp", "linux", "arm64", "eka-mcp-linux-arm64", false},
		{"eka-mcp", "darwin", "amd64", "eka-mcp-darwin-amd64", false},
		{"eka-mcp", "darwin", "arm64", "eka-mcp-darwin-arm64", false},
		{"eka-mcp", "windows", "amd64", "eka-mcp-windows-amd64.exe", false},
		{"eka-mcp", "windows", "arm64", "eka-mcp-windows-arm64.exe", false},
		{"eka-mcp", "freebsd", "amd64", "", true},
		{"eka-mcp", "linux", "386", "", true},
		{"eka-mcp", "windows", "386", "", true},
	}
	for _, c := range cases {
		got, err := platformAssetName(c.prefix, c.goos, c.goarch)
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
}

// TestPluginCommandTree: `eka plugin` and `eka plugin install` are
// registered with the deterministic help contract.
func TestPluginCommandTree(t *testing.T) {
	code, out, errText := runIn([]string{"plugin", "--help"})
	if code != 0 {
		t.Fatalf("plugin --help: exit = %d\nstderr: %s", code, errText)
	}
	for _, want := range []string{"Install and manage official EKA plugins", "install     Install an official EKA plugin"} {
		if !strings.Contains(out, want) {
			t.Errorf("plugin help missing %q:\n%s", want, out)
		}
	}

	code, out, errText = runIn([]string{"plugin", "install", "--help"})
	if code != 0 {
		t.Fatalf("plugin install --help: exit = %d\nstderr: %s", code, errText)
	}
	for _, want := range []string{"eka plugin install <name>", "eka plugin install mcp", "SHA256SUMS.txt", "$EKA_PLUGIN_DIR", "fail-closed"} {
		if !strings.Contains(out, want) {
			t.Errorf("plugin install help missing %q:\n%s", want, out)
		}
	}

	// Usage: `eka plugin install` without a name is a usage error.
	code, _, errText = runIn([]string{"plugin", "install"})
	if code != 2 {
		t.Fatalf("plugin install without a name: exit = %d, want 2\nstderr: %s", code, errText)
	}
}
