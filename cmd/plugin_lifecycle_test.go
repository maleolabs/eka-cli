package cmd

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/maleolabs/eka-core/plugin"
)

// The plugin lifecycle tests are hermetic: list/remove run against
// fake plugin executables (shell scripts answering "manifest") in
// controlled plugin/PATH directories; update runs against the same
// httptest release server as the install tests. No real network, no
// real binary.

// lifecycleManifestScript is the fake plugin "binary" answering
// "manifest" with a valid manifest whose version/source are
// parameterized (the mcp-shaped happy path).
func lifecycleManifestScript(version, source string) string {
	return `#!/bin/sh
case "$1" in
  manifest) printf '%s' '{"contract":"v1","name":"mcp","version":"` + version + `","description":"fake","artifacts":[],"capabilities":["install","mcp"],"source":"` + source + `"}' ;;
esac
`
}

// writeLifecyclePlugin writes a fake plugin executable into dir and
// returns its path.
func writeLifecyclePlugin(t *testing.T, dir, name string, body []byte) string {
	t.Helper()
	exe := filepath.Join(dir, name)
	if err := os.WriteFile(exe, body, 0o755); err != nil {
		t.Fatalf("write %s: %v", exe, err)
	}
	return exe
}

// TestPluginListDiscoveredInstalledTier: the report covers discovered
// plugins (PATH + plugin dir), the installed state, the manifest
// version/source and the trust tier (official vs third-party), in
// deterministic (name-ascending) order.
func TestPluginListDiscoveredInstalledTier(t *testing.T) {
	pluginDir := t.TempDir()
	// Installed official plugin (in the plugin dir).
	writeLifecyclePlugin(t, pluginDir, "eka-mcp", []byte(lifecycleManifestScript("1.0.0", "github.com/maleolabs/eka-mcp")))
	pathDir := t.TempDir()
	// Third-party plugin on PATH only (not installed).
	writeLifecyclePlugin(t, pathDir, "eka-helper", []byte(lifecycleManifestScript("0.2.0", "github.com/someone/eka-helper")))
	t.Setenv("EKA_PLUGIN_DIR", pluginDir)
	t.Setenv("PATH", pathDir)

	code, out, errText := runIn([]string{"plugin", "list"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstderr: %s", code, errText)
	}
	if errText != "" {
		t.Errorf("stderr must be empty, got %q", errText)
	}
	for _, want := range []string{
		"NAME", "VERSION", "SOURCE", "TRUST", "INSTALLED", "PATH",
		"helper", "0.2.0", "github.com/someone/eka-helper", "third-party", "no",
		"mcp", "1.0.0", "github.com/maleolabs/eka-mcp", "official", "yes",
		filepath.Join(pluginDir, "eka-mcp"), filepath.Join(pathDir, "eka-helper"),
	} {
		if !strings.Contains(out, want) {
			t.Errorf("list output missing %q:\n%s", want, out)
		}
	}
	// Determinism: two runs produce identical bytes.
	_, out2, _ := runIn([]string{"plugin", "list"})
	if out != out2 {
		t.Errorf("list output is not deterministic:\n%q\nvs\n%q", out, out2)
	}
}

// TestPluginListBrokenManifestVisible: a plugin whose manifest cannot
// be read stays visible with an "unknown" version/source — never
// silently skipped — and the list still exits 0.
func TestPluginListBrokenManifestVisible(t *testing.T) {
	pluginDir := t.TempDir()
	writeLifecyclePlugin(t, pluginDir, "eka-broken", []byte("#!/bin/sh\nprintf '%s' 'this is not json'\n"))
	t.Setenv("EKA_PLUGIN_DIR", pluginDir)
	t.Setenv("PATH", t.TempDir())

	code, out, errText := runIn([]string{"plugin", "list"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstderr: %s", code, errText)
	}
	for _, want := range []string{"broken", "unknown"} {
		if !strings.Contains(out, want) {
			t.Errorf("list output missing %q (broken plugin must stay visible):\n%s", want, out)
		}
	}
}

// TestPluginListEmpty: nothing discovered or installed is an
// informative report, exit 0.
func TestPluginListEmpty(t *testing.T) {
	t.Setenv("EKA_PLUGIN_DIR", t.TempDir())
	t.Setenv("PATH", t.TempDir())
	code, out, errText := runIn([]string{"plugin", "list"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstderr: %s", code, errText)
	}
	if !strings.Contains(out, "No plugins discovered or installed") {
		t.Errorf("empty list must render the informative line, got:\n%s", out)
	}
}

// TestPluginListJSON: --json emits the deterministic
// eka-plugin-list-v1 document with the trust tier and installed
// state; two runs are byte-identical.
func TestPluginListJSON(t *testing.T) {
	pluginDir := t.TempDir()
	writeLifecyclePlugin(t, pluginDir, "eka-mcp", []byte(lifecycleManifestScript("1.0.0", "github.com/maleolabs/eka-mcp")))
	pathDir := t.TempDir()
	writeLifecyclePlugin(t, pathDir, "eka-helper", []byte(lifecycleManifestScript("0.2.0", "github.com/someone/eka-helper")))
	t.Setenv("EKA_PLUGIN_DIR", pluginDir)
	t.Setenv("PATH", pathDir)

	code, out, errText := runIn([]string{"plugin", "list", "--json"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstderr: %s", code, errText)
	}
	if !strings.HasSuffix(out, "\n") {
		t.Errorf("--json output must end with a newline, got %q", out)
	}
	var doc pluginListDocument
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("--json output is not valid JSON: %v\n%s", err, out)
	}
	if doc.Schema != "eka-plugin-list-v1" {
		t.Errorf("schema = %q, want eka-plugin-list-v1", doc.Schema)
	}
	if len(doc.Plugins) != 2 {
		t.Fatalf("plugins = %d, want 2: %+v", len(doc.Plugins), doc.Plugins)
	}
	byName := map[string]pluginListEntry{}
	for _, e := range doc.Plugins {
		byName[e.Name] = e
	}
	if e := byName["mcp"]; e.Trust != "official" || !e.Installed || e.Version != "1.0.0" || e.Source != "github.com/maleolabs/eka-mcp" {
		t.Errorf("mcp entry = %+v, want official/installed/1.0.0", e)
	}
	if e := byName["helper"]; e.Trust != "third-party" || e.Installed {
		t.Errorf("helper entry = %+v, want third-party/not installed", e)
	}
	// Determinism.
	_, out2, _ := runIn([]string{"plugin", "list", "--json"})
	if out != out2 {
		t.Errorf("--json output is not deterministic:\n%q\nvs\n%q", out, out2)
	}
}

// TestPluginRemoveInstalled: removes the installed binary and any
// stale .old marker, prints the confirmation, and a second remove
// refuses (exit 1, not installed).
func TestPluginRemoveInstalled(t *testing.T) {
	dir := t.TempDir()
	writeLifecyclePlugin(t, dir, "eka-mcp", []byte("old-binary"))
	// Stale marker of a completed update.
	writeLifecyclePlugin(t, dir, "eka-mcp.old", []byte("stale"))
	r := &pluginRemoveRunner{pluginDir: dir, goos: "linux"}
	var out, errb bytes.Buffer
	if err := r.run(updateTestCommand(&out, &errb), "mcp"); err != nil {
		t.Fatalf("run: %v\nstderr: %s", err, errb.String())
	}
	target := filepath.Join(dir, "eka-mcp")
	for _, want := range []string{"✓ removed: " + target, "Removed"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("remove output missing %q:\n%s", want, out.String())
		}
	}
	if errb.String() != "" {
		t.Errorf("stderr must be empty, got %q", errb.String())
	}
	if names := pluginDirEntries(t, dir); len(names) != 0 {
		t.Errorf("the binary and the stale .old marker must be removed, found %v", names)
	}

	// Second remove: not installed → refusal.
	var out2, errb2 bytes.Buffer
	err := r.run(updateTestCommand(&out2, &errb2), "mcp")
	if code := exitCodeOf(err); code != 1 {
		t.Fatalf("second remove: exit = %d, want 1\nstderr: %s", code, errb2.String())
	}
	if !strings.Contains(errb2.String(), "not installed") {
		t.Errorf("refusal must explain the missing plugin, got %q", errb2.String())
	}
}

// TestPluginRemoveNotInstalled: removing a plugin that was never
// installed refuses with a clear error, exit 1.
func TestPluginRemoveNotInstalled(t *testing.T) {
	r := &pluginRemoveRunner{pluginDir: t.TempDir(), goos: "linux"}
	var out, errb bytes.Buffer
	err := r.run(updateTestCommand(&out, &errb), "nope")
	if code := exitCodeOf(err); code != 1 {
		t.Fatalf("exit = %d, want 1 (refusal)\nstderr: %s", code, errb.String())
	}
	if !strings.Contains(errb.String(), "not installed") || !strings.Contains(errb.String(), "nope") {
		t.Errorf("refusal must name the plugin and the missing binary, got %q", errb.String())
	}
}

// TestPluginRemoveWindowsExe: on windows the installed binary is
// eka-<name>.exe (mirroring the asset suffix) and is removed as such.
func TestPluginRemoveWindowsExe(t *testing.T) {
	dir := t.TempDir()
	writeLifecyclePlugin(t, dir, "eka-mcp.exe", []byte("old-binary"))
	r := &pluginRemoveRunner{pluginDir: dir, goos: "windows"}
	var out, errb bytes.Buffer
	if err := r.run(updateTestCommand(&out, &errb), "mcp"); err != nil {
		t.Fatalf("run: %v\nstderr: %s", err, errb.String())
	}
	if !strings.Contains(out.String(), "removed: "+filepath.Join(dir, "eka-mcp.exe")) {
		t.Errorf("remove output must name eka-mcp.exe, got:\n%s", out.String())
	}
	if names := pluginDirEntries(t, dir); len(names) != 0 {
		t.Errorf("the .exe binary must be removed, found %v", names)
	}
}

// TestPluginUpdateSingle: re-downloads the latest verified release
// through the install flow's shared path, prints the old → new
// version and leaves no temp or .old debris.
func TestPluginUpdateSingle(t *testing.T) {
	body := []byte(pluginManifestScript) // manifest version 1.0.0
	srv := newFakePluginReleaseServer(t, plugin.Repo{Owner: "maleolabs", Name: "eka-mcp"},
		"v1.0.0", "eka-mcp-linux-amd64", sha256Hex(body), body)
	dir := t.TempDir()
	// The installed OLD binary (manifest version 0.9.0).
	writeLifecyclePlugin(t, dir, "eka-mcp", []byte(lifecycleManifestScript("0.9.0", "github.com/maleolabs/eka-mcp")))

	r := testPluginInstallRunner(srv, dir)
	var out, errb bytes.Buffer
	if err := r.runUpdate(updateTestCommand(&out, &errb), "mcp", false); err != nil {
		t.Fatalf("run: %v\nstderr: %s", err, errb.String())
	}
	if errb.String() != "" {
		t.Errorf("stderr must be empty, got %q", errb.String())
	}
	for _, want := range []string{
		"Update", "Plugin    mcp", "Repo      maleolabs/eka-mcp", "Version   v1.0.0",
		"Current   0.9.0", "✓ updated: " + filepath.Join(dir, "eka-mcp"), "0.9.0 → v1.0.0",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("update output missing %q:\n%s", want, out.String())
		}
	}
	// The installed binary is the new verified asset, executable.
	got, err := os.ReadFile(filepath.Join(dir, "eka-mcp"))
	if err != nil || !bytes.Equal(got, body) {
		t.Errorf("installed binary must be the new verified asset (err %v)", err)
	}
	if fi, err := os.Stat(filepath.Join(dir, "eka-mcp")); err != nil || fi.Mode().Perm() != 0o755 {
		t.Errorf("updated binary mode = %v, want 0755 (err %v)", fi.Mode().Perm(), err)
	}
	// No temp or .old debris.
	for _, name := range pluginDirEntries(t, dir) {
		if strings.HasPrefix(name, ".eka-plugin-") || strings.HasSuffix(name, ".old") {
			t.Errorf("update must leave no debris, found %q", name)
		}
	}
}

// TestPluginUpdateUnknownName: a name that is neither registry-listed
// nor installed (no manifest source to resolve a repository from)
// refuses with the official list and the reinstall hint, before any
// network access.
func TestPluginUpdateUnknownName(t *testing.T) {
	r := testPluginInstallRunner(nil, t.TempDir())
	var out, errb bytes.Buffer
	err := r.runUpdate(updateTestCommand(&out, &errb), "nope", false)
	if code := exitCodeOf(err); code != 1 {
		t.Fatalf("exit = %d, want 1 (refusal)\nstderr: %s", code, errb.String())
	}
	if !strings.Contains(errb.String(), "\"nope\" is not registry-listed") || !strings.Contains(errb.String(), "mcp") || !strings.Contains(errb.String(), "--repo") {
		t.Errorf("refusal must name the plugin, list official plugins and hint the reinstall, got %q", errb.String())
	}
}

// TestPluginUpdateNotInstalled: updating a plugin that is not
// installed refuses with the install hint, exit 1.
func TestPluginUpdateNotInstalled(t *testing.T) {
	r := testPluginInstallRunner(nil, t.TempDir())
	var out, errb bytes.Buffer
	err := r.runUpdate(updateTestCommand(&out, &errb), "mcp", false)
	if code := exitCodeOf(err); code != 1 {
		t.Fatalf("exit = %d, want 1 (refusal)\nstderr: %s", code, errb.String())
	}
	if !strings.Contains(errb.String(), "not installed") || !strings.Contains(errb.String(), "eka plugin install mcp") {
		t.Errorf("refusal must explain the missing plugin and the install hint, got %q", errb.String())
	}
}

// TestPluginUpdateChecksumMismatchKeepsOld: a checksum mismatch
// refuses fail-closed, names the asset, cleans the partial download
// and leaves the OLD installed binary untouched.
func TestPluginUpdateChecksumMismatchKeepsOld(t *testing.T) {
	srv := newFakePluginReleaseServer(t, plugin.Repo{Owner: "maleolabs", Name: "eka-mcp"},
		"v1.0.0", "eka-mcp-linux-amd64", sha256Hex([]byte("the expected binary")), []byte("a tampered binary"))
	dir := t.TempDir()
	old := []byte(lifecycleManifestScript("0.9.0", "github.com/maleolabs/eka-mcp"))
	writeLifecyclePlugin(t, dir, "eka-mcp", old)

	r := testPluginInstallRunner(srv, dir)
	var out, errb bytes.Buffer
	err := r.runUpdate(updateTestCommand(&out, &errb), "mcp", false)
	if code := exitCodeOf(err); code != 1 {
		t.Fatalf("exit = %d, want 1 (refusal)\nstderr: %s", code, errb.String())
	}
	if !strings.Contains(errb.String(), "checksum mismatch") || !strings.Contains(errb.String(), "eka-mcp-linux-amd64") {
		t.Errorf("refusal must name the mismatch and the asset, got %q", errb.String())
	}
	got, err := os.ReadFile(filepath.Join(dir, "eka-mcp"))
	if err != nil || !bytes.Equal(got, old) {
		t.Errorf("a refused update must keep the old binary intact (err %v)", err)
	}
	if names := pluginDirEntries(t, dir); len(names) != 1 || names[0] != "eka-mcp" {
		t.Errorf("the partial download must be cleaned up, found %v", names)
	}
}

// TestPluginUpdateBrokenNewManifestKeepsOld: the new binary passes
// the checksum but fails the manifest inspection — the update
// refuses and the OLD binary is untouched (the staged download is
// inspected before the swap, so the old binary is never moved; no
// .old or temp debris remains).
func TestPluginUpdateBrokenNewManifestKeepsOld(t *testing.T) {
	body := []byte(`#!/bin/sh
printf '%s' 'this is not json'
`)
	srv := newFakePluginReleaseServer(t, plugin.Repo{Owner: "maleolabs", Name: "eka-mcp"},
		"v1.0.0", "eka-mcp-linux-amd64", sha256Hex(body), body)
	dir := t.TempDir()
	old := []byte(lifecycleManifestScript("0.9.0", "github.com/maleolabs/eka-mcp"))
	writeLifecyclePlugin(t, dir, "eka-mcp", old)

	r := testPluginInstallRunner(srv, dir)
	var out, errb bytes.Buffer
	err := r.runUpdate(updateTestCommand(&out, &errb), "mcp", false)
	if code := exitCodeOf(err); code != 1 {
		t.Fatalf("exit = %d, want 1 (refusal)\nstderr: %s", code, errb.String())
	}
	if !strings.Contains(errb.String(), "smoke check") {
		t.Errorf("refusal must report the failed smoke check, got %q", errb.String())
	}
	got, err := os.ReadFile(filepath.Join(dir, "eka-mcp"))
	if err != nil || !bytes.Equal(got, old) {
		t.Errorf("a broken new binary must restore the old one (err %v)", err)
	}
	if names := pluginDirEntries(t, dir); len(names) != 1 || names[0] != "eka-mcp" {
		t.Errorf("no .old or temp debris may remain, found %v", names)
	}
}

// TestPluginUpdateAll: --all updates every installed official plugin
// and never touches third-party plugins.
func TestPluginUpdateAll(t *testing.T) {
	body := []byte(pluginManifestScript)
	srv := newFakePluginReleaseServer(t, plugin.Repo{Owner: "maleolabs", Name: "eka-mcp"},
		"v1.0.0", "eka-mcp-linux-amd64", sha256Hex(body), body)
	dir := t.TempDir()
	writeLifecyclePlugin(t, dir, "eka-mcp", []byte(lifecycleManifestScript("0.9.0", "github.com/maleolabs/eka-mcp")))
	thirdParty := []byte(lifecycleManifestScript("0.2.0", "github.com/someone/eka-helper"))
	writeLifecyclePlugin(t, dir, "eka-helper", thirdParty)

	r := testPluginInstallRunner(srv, dir)
	var out, errb bytes.Buffer
	if err := r.runUpdateAll(updateTestCommand(&out, &errb)); err != nil {
		t.Fatalf("run: %v\nstderr: %s", err, errb.String())
	}
	if errb.String() != "" {
		t.Errorf("stderr must be empty, got %q", errb.String())
	}
	if !strings.Contains(out.String(), "✓ updated: "+filepath.Join(dir, "eka-mcp")) {
		t.Errorf("--all must update the installed official plugin:\n%s", out.String())
	}
	// mcp is the new verified asset; the third-party plugin is untouched.
	got, err := os.ReadFile(filepath.Join(dir, "eka-mcp"))
	if err != nil || !bytes.Equal(got, body) {
		t.Errorf("mcp must be updated (err %v)", err)
	}
	got, err = os.ReadFile(filepath.Join(dir, "eka-helper"))
	if err != nil || !bytes.Equal(got, thirdParty) {
		t.Errorf("a third-party plugin must never be touched by --all (err %v)", err)
	}
}

// TestPluginUpdateAllEmpty: --all with nothing installed is an
// informative empty result, exit 0.
func TestPluginUpdateAllEmpty(t *testing.T) {
	r := testPluginInstallRunner(nil, t.TempDir())
	var out, errb bytes.Buffer
	if err := r.runUpdateAll(updateTestCommand(&out, &errb)); err != nil {
		t.Fatalf("run: %v\nstderr: %s", err, errb.String())
	}
	if !strings.Contains(out.String(), "no installed official plugins to update") {
		t.Errorf("--all with nothing installed must report the empty result, got:\n%s", out.String())
	}
}

// TestPluginUpdateAllFailedExitsOne: --all with a failed update (the
// release cannot be resolved) exits 1 while rendering the refusal.
func TestPluginUpdateAllFailedExitsOne(t *testing.T) {
	dir := t.TempDir()
	writeLifecyclePlugin(t, dir, "eka-mcp", []byte(lifecycleManifestScript("0.9.0", "github.com/maleolabs/eka-mcp")))
	// nil server = dead endpoint (http://127.0.0.1:1), so the update
	// fails loudly; the client is a bare one (the dead dial refuses
	// fast).
	r := testPluginInstallRunner(nil, dir)
	r.client = &http.Client{}
	var out, errb bytes.Buffer
	err := r.runUpdateAll(updateTestCommand(&out, &errb))
	if code := exitCodeOf(err); code != 1 {
		t.Fatalf("exit = %d, want 1 (refusal)\nstderr: %s", code, errb.String())
	}
	if !strings.Contains(errb.String(), "plugin update refused") {
		t.Errorf("the per-plugin refusal must be rendered, got %q", errb.String())
	}
}

// TestPluginUpdateWindowsExe: on windows the installed binary is
// eka-<name>.exe and the update replaces it (the .old dance) with the
// .exe asset.
func TestPluginUpdateWindowsExe(t *testing.T) {
	body := []byte(pluginManifestScript)
	srv := newFakePluginReleaseServer(t, plugin.Repo{Owner: "maleolabs", Name: "eka-mcp"},
		"v1.0.0", "eka-mcp-windows-amd64.exe", sha256Hex(body), body)
	dir := t.TempDir()
	writeLifecyclePlugin(t, dir, "eka-mcp.exe", []byte(lifecycleManifestScript("0.9.0", "github.com/maleolabs/eka-mcp")))

	r := testPluginInstallRunner(srv, dir)
	r.goos = "windows"
	var out, errb bytes.Buffer
	if err := r.runUpdate(updateTestCommand(&out, &errb), "mcp", false); err != nil {
		t.Fatalf("run: %v\nstderr: %s", err, errb.String())
	}
	got, err := os.ReadFile(filepath.Join(dir, "eka-mcp.exe"))
	if err != nil || !bytes.Equal(got, body) {
		t.Errorf("eka-mcp.exe must be the new verified asset (err %v)", err)
	}
	if names := pluginDirEntries(t, dir); len(names) != 1 || names[0] != "eka-mcp.exe" {
		t.Errorf("no .old or temp debris may remain, found %v", names)
	}
}

// TestPluginLifecycleCommandTree: `eka plugin list/remove/update` are
// registered with the deterministic help contract, and the usage
// classes exit 2.
func TestPluginLifecycleCommandTree(t *testing.T) {
	code, out, errText := runIn([]string{"plugin", "--help"})
	if code != 0 {
		t.Fatalf("plugin --help: exit = %d\nstderr: %s", code, errText)
	}
	for _, want := range []string{
		"list        List discovered and installed EKA plugins",
		"remove      Remove an installed EKA plugin",
		"update      Update an installed EKA plugin to its latest release",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("plugin help missing %q:\n%s", want, out)
		}
	}

	code, out, errText = runIn([]string{"plugin", "list", "--help"})
	if code != 0 {
		t.Fatalf("plugin list --help: exit = %d\nstderr: %s", code, errText)
	}
	for _, want := range []string{"eka plugin list", "--json", "eka-plugin-list-v1", "official"} {
		if !strings.Contains(out, want) {
			t.Errorf("plugin list help missing %q:\n%s", want, out)
		}
	}

	code, out, errText = runIn([]string{"plugin", "remove", "--help"})
	if code != 0 {
		t.Fatalf("plugin remove --help: exit = %d\nstderr: %s", code, errText)
	}
	for _, want := range []string{"eka plugin remove <name>", "eka plugin remove mcp", "$EKA_PLUGIN_DIR"} {
		if !strings.Contains(out, want) {
			t.Errorf("plugin remove help missing %q:\n%s", want, out)
		}
	}

	code, out, errText = runIn([]string{"plugin", "update", "--help"})
	if code != 0 {
		t.Fatalf("plugin update --help: exit = %d\nstderr: %s", code, errText)
	}
	for _, want := range []string{"eka plugin update [name]", "--all", "eka plugin update mcp", "SHA256SUMS.txt"} {
		if !strings.Contains(out, want) {
			t.Errorf("plugin update help missing %q:\n%s", want, out)
		}
	}

	// Usage classes (exit 2): list with an argument, remove without a
	// name, update without a name, update --all with a name.
	for _, args := range [][]string{
		{"plugin", "list", "mcp"},
		{"plugin", "remove"},
		{"plugin", "update"},
		{"plugin", "update", "--all", "mcp"},
	} {
		code, _, errText = runIn(args)
		if code != 2 {
			t.Errorf("%v: exit = %d, want 2 (usage)\nstderr: %s", args, code, errText)
		}
	}
}

// --- Fix-loop tests (review findings on PR #12) ----------------------

// TestPluginListWindowsExeTier: on windows the installed binary is
// eka-<name>.exe; the list must derive the clean name (mcp, not
// mcp.exe) so the trust tier (official) and the installed state are
// computed from the clean name.
func TestPluginListWindowsExeTier(t *testing.T) {
	dir := t.TempDir()
	writeLifecyclePlugin(t, dir, "eka-mcp.exe", []byte(lifecycleManifestScript("1.0.0", "github.com/maleolabs/eka-mcp")))
	t.Setenv("EKA_PLUGIN_DIR", dir)
	t.Setenv("PATH", t.TempDir())

	entries := collectPluginListEntries("", "windows")
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1: %+v", len(entries), entries)
	}
	e := entries[0]
	if e.Name != "mcp" {
		t.Errorf("name = %q, want mcp (the .exe suffix must be stripped)", e.Name)
	}
	if e.Trust != "official" {
		t.Errorf("trust = %q, want official (tier must come from the clean name)", e.Trust)
	}
	if !e.Installed {
		t.Errorf("installed = false, want true (eka-mcp.exe is the installed binary)")
	}
	if e.Version != "1.0.0" {
		t.Errorf("version = %q, want 1.0.0", e.Version)
	}
}

// TestPluginListSkipsOldMarkers: an eka-<name>.old leftover of the
// update rename dance is debris, never a plugin — the list must not
// show a phantom "mcp.old" entry.
func TestPluginListSkipsOldMarkers(t *testing.T) {
	dir := t.TempDir()
	writeLifecyclePlugin(t, dir, "eka-mcp", []byte(lifecycleManifestScript("1.0.0", "github.com/maleolabs/eka-mcp")))
	writeLifecyclePlugin(t, dir, "eka-mcp.old", []byte("stale"))
	t.Setenv("EKA_PLUGIN_DIR", dir)
	t.Setenv("PATH", t.TempDir())

	code, out, errText := runIn([]string{"plugin", "list"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstderr: %s", code, errText)
	}
	if strings.Contains(out, "mcp.old") {
		t.Errorf("the stale .old marker must not appear as a plugin:\n%s", out)
	}
	if !strings.Contains(out, "mcp") {
		t.Errorf("the real plugin must still be listed:\n%s", out)
	}
}

// TestPluginRemoveInternalErrorExit2: a filesystem failure while
// removing (here: the target is a non-empty directory, so os.Remove
// fails) is an internal error — exit 2, not the domain refusal exit 1.
func TestPluginRemoveInternalErrorExit2(t *testing.T) {
	dir := t.TempDir()
	// A non-empty directory at the target: fileExists passes (Stat
	// succeeds) but os.Remove fails — the internal-error path.
	if err := os.MkdirAll(filepath.Join(dir, "eka-mcp", "inner"), 0o755); err != nil {
		t.Fatal(err)
	}
	r := &pluginRemoveRunner{pluginDir: dir, goos: "linux"}
	var out, errb bytes.Buffer
	err := r.run(updateTestCommand(&out, &errb), "mcp")
	if code := exitCodeOf(err); code != 2 {
		t.Fatalf("exit = %d, want 2 (internal)\nstderr: %s", code, errb.String())
	}
	if !strings.Contains(err.Error(), "cannot remove") {
		t.Errorf("the error must explain the failed removal, got %q", err.Error())
	}
	if strings.Contains(errb.String(), "refused") {
		t.Errorf("an internal failure must not render a refusal line, got %q", errb.String())
	}
}

// TestPluginUpdateReplaceFailureExit2: a rename failure inside the
// atomic replace (here: a non-empty directory blocks the <target>.old
// rename) is an internal error — exit 2, plain error — and the old
// binary stays untouched.
func TestPluginUpdateReplaceFailureExit2(t *testing.T) {
	body := []byte(pluginManifestScript)
	srv := newFakePluginReleaseServer(t, plugin.Repo{Owner: "maleolabs", Name: "eka-mcp"},
		"v1.0.0", "eka-mcp-linux-amd64", sha256Hex(body), body)
	dir := t.TempDir()
	old := []byte(lifecycleManifestScript("0.9.0", "github.com/maleolabs/eka-mcp"))
	writeLifecyclePlugin(t, dir, "eka-mcp", old)
	// A non-empty directory at the .old path blocks the first rename
	// of the dance (best-effort os.Remove cannot remove it).
	if err := os.MkdirAll(filepath.Join(dir, "eka-mcp.old", "inner"), 0o755); err != nil {
		t.Fatal(err)
	}

	r := testPluginInstallRunner(srv, dir)
	var out, errb bytes.Buffer
	err := r.runUpdate(updateTestCommand(&out, &errb), "mcp", false)
	if code := exitCodeOf(err); code != 2 {
		t.Fatalf("exit = %d, want 2 (internal)\nstderr: %s", code, errb.String())
	}
	if !strings.Contains(err.Error(), "cannot replace") {
		t.Errorf("the error must explain the failed replacement, got %q", err.Error())
	}
	if strings.Contains(errb.String(), "refused") {
		t.Errorf("an internal failure must not render a refusal line, got %q", errb.String())
	}
	got, err := os.ReadFile(filepath.Join(dir, "eka-mcp"))
	if err != nil || !bytes.Equal(got, old) {
		t.Errorf("a failed replacement must leave the old binary untouched (err %v)", err)
	}
}

// TestPluginUpdateAllUnreadableDirExit2: --all against an unreadable
// plugin directory (here: a regular file where the directory should
// be — ReadDir fails with ENOTDIR) is an internal error, exit 2 — it
// must not misreport "no installed official plugins" (exit 0).
func TestPluginUpdateAllUnreadableDirExit2(t *testing.T) {
	dir := t.TempDir()
	notADir := filepath.Join(dir, "not-a-dir")
	if err := os.WriteFile(notADir, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := testPluginInstallRunner(nil, notADir)
	var out, errb bytes.Buffer
	err := r.runUpdateAll(updateTestCommand(&out, &errb))
	if code := exitCodeOf(err); code != 2 {
		t.Fatalf("exit = %d, want 2 (internal)\nstderr: %s", code, errb.String())
	}
	if !strings.Contains(err.Error(), "cannot read the plugin directory") {
		t.Errorf("the error must explain the unreadable directory, got %q", err.Error())
	}
	if strings.Contains(out.String(), "no installed official plugins") {
		t.Errorf("an unreadable directory must not misreport the empty result:\n%s", out.String())
	}
}

// TestPluginUpdateAllMissingDirEmpty: --all with a missing plugin
// directory is nothing installed — the informative empty result,
// exit 0 (a missing directory is not an error).
func TestPluginUpdateAllMissingDirEmpty(t *testing.T) {
	r := testPluginInstallRunner(nil, filepath.Join(t.TempDir(), "missing"))
	var out, errb bytes.Buffer
	if err := r.runUpdateAll(updateTestCommand(&out, &errb)); err != nil {
		t.Fatalf("run: %v\nstderr: %s", err, errb.String())
	}
	if !strings.Contains(out.String(), "no installed official plugins to update") {
		t.Errorf("--all with a missing plugin directory must report the empty result, got:\n%s", out.String())
	}
}
