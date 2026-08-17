package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/maleolabs/eka-core/plugin"
)

// The B1 command-registration tests (sto:mcp-command-registration,
// ADR-031) are hermetic: fake plugin executables (shell scripts
// answering "manifest" with a B1-extended manifest) live in temp
// plugin directories with the G2 checksum sidecar a verified install
// writes. Registration is exercised end-to-end through Execute
// (runIn) and at the function level (dispatchPluginCommand), so both
// the registration path and the dispatch-time anti-TOCTOU check are
// covered without real binaries or network.

// registrationManifest builds a B1-extended manifest JSON for the
// given plugin name with the given commands (each: name, description,
// args).
func registrationManifest(pluginName string, commands ...pluginCommandSpec) string {
	var b strings.Builder
	b.WriteString(`{"contract":"v1","name":` + strconvQuote(pluginName) + `,"version":"1.0.0","description":"fake","artifacts":[],"capabilities":["install","mcp"],"source":"github.com/maleolabs/eka-mcp","commands":[`)
	for i, c := range commands {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`{"name":` + strconvQuote(c.Name) + `,"description":` + strconvQuote(c.Description) + `,"args":`)
		if len(c.Args) == 0 {
			b.WriteString(`[]`)
		} else {
			b.WriteString(`[`)
			for j, a := range c.Args {
				if j > 0 {
					b.WriteString(",")
				}
				b.WriteString(strconvQuote(a))
			}
			b.WriteString(`]`)
		}
		b.WriteString(`}`)
	}
	b.WriteString(`]}`)
	return b.String()
}

// strconvQuote wraps s in JSON double quotes (the manifest JSON is
// built by concatenation; the test manifests carry no quotes).
func strconvQuote(s string) string { return `"` + s + `"` }

// registrationPluginScript is a fake plugin "binary": it answers
// "manifest --json" with manifestJSON, records every invocation into
// counter (when non-empty — the probe/dispatch counter of the cache
// tests), echoes the arguments of any other subcommand to stdout
// (dispatch tests) and exits with exitCode when non-zero.
func registrationPluginScript(manifestJSON, counter string, exitCode int) []byte {
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	if counter != "" {
		b.WriteString("printf 'x\\n' >> " + shellQuote(counter) + "\n")
	}
	b.WriteString("case \"$1\" in\n")
	b.WriteString("  manifest) printf '%s' " + shellQuote(manifestJSON) + " ;;\n")
	if exitCode != 0 {
		b.WriteString("  *) exit " + string(rune('0'+exitCode)) + " ;;\n")
	} else {
		b.WriteString("  *) echo \"$@\" ;;\n")
	}
	b.WriteString("esac\n")
	return []byte(b.String())
}

// shellQuote wraps s in single quotes for embedding into the fake
// script (temp dirs carry no quotes).
func shellQuote(s string) string { return "'" + s + "'" }

// installRegistrationPlugin writes a fake plugin + its G2 sidecar into
// dir and returns the exe path.
func installRegistrationPlugin(t *testing.T, dir, exeName string, body []byte) string {
	t.Helper()
	exe := writeLifecyclePlugin(t, dir, exeName, body)
	writePluginSidecarFor(t, dir, strings.TrimPrefix(exeName, "eka-"), exe)
	return exe
}

// shrinkPluginProbeTimeout shrinks the registration probe deadline for
// the duration of the test.
func shrinkPluginProbeTimeout(t *testing.T, v time.Duration) {
	t.Helper()
	old := pluginProbeTimeout
	pluginProbeTimeout = v
	t.Cleanup(func() { pluginProbeTimeout = old })
}

// shrinkPluginManifestCacheTTL shrinks the registration cache TTL for
// the duration of the test.
func shrinkPluginManifestCacheTTL(t *testing.T, v time.Duration) {
	t.Helper()
	old := pluginManifestCacheTTL
	pluginManifestCacheTTL = v
	t.Cleanup(func() { pluginManifestCacheTTL = old })
}

// TestPluginRegistrationInHelp: an installed official plugin declaring
// commands registers them into the cobra tree — each appears in `eka
// --help` and the landing under the dynamic "Plugins" group (acceptance
// criterion: appears in eka --help with its group).
func TestPluginRegistrationInHelp(t *testing.T) {
	dir := t.TempDir()
	body := registrationPluginScript(
		registrationManifest("mcp", pluginCommandSpec{Name: "mcp-serve", Description: "run the eka-mcp MCP server", Args: []string{"serve"}}),
		"", 0)
	installRegistrationPlugin(t, dir, "eka-mcp", body)
	t.Setenv("EKA_PLUGIN_DIR", dir)
	t.Setenv("PATH", t.TempDir())

	code, out, errText := runIn([]string{"--help"})
	if code != 0 {
		t.Fatalf("--help: exit = %d, want 0\nstderr: %s", code, errText)
	}
	if errText != "" {
		t.Errorf("--help: stderr must be empty, got %q", errText)
	}
	// The dynamic Plugins group renders after the static groups (the
	// output container adds the uniform left margin).
	if !strings.Contains(out, "Plugins") {
		t.Errorf("root help missing the dynamic Plugins group:\n%s", out)
	}
	if !strings.Contains(out, "mcp-serve") || !strings.Contains(out, "run the eka-mcp MCP server") {
		t.Errorf("root help missing the registered plugin command:\n%s", out)
	}

	// The group model: the command carries the plugins GroupID and the
	// root registers the dynamic group (6 groups with a plugin).
	root := newRootCommand()
	if c := findCommand(root, "mcp-serve"); c == nil || c.GroupID != groupPlugins {
		t.Errorf("mcp-serve: GroupID = %v, want %q", c, groupPlugins)
	}
	titles := make([]string, 0, len(root.Groups()))
	for _, g := range root.Groups() {
		titles = append(titles, g.Title)
	}
	if got, want := strings.Join(titles, "|"),
		"Authoring|Repository & Exchange|Knowledge Access|Runtime & Workspace|Utility|Plugins"; got != want {
		t.Errorf("group titles = %q, want %q", got, want)
	}

	// The landing shows the same command list (shared renderer).
	code, out, errText = runIn(nil)
	if code != 0 {
		t.Fatalf("landing: exit = %d, want 0\nstderr: %s", code, errText)
	}
	if !strings.Contains(out, "Plugins") || !strings.Contains(out, "mcp-serve") {
		t.Errorf("landing missing the plugin command and group:\n%s", out)
	}
}

// TestPluginRegistrationNoCommandsDeclared: an official plugin whose
// manifest declares no commands registers nothing — no group, no
// commands, no stderr noise.
func TestPluginRegistrationNoCommandsDeclared(t *testing.T) {
	dir := t.TempDir()
	body := registrationPluginScript(`{"contract":"v1","name":"mcp","version":"1.0.0","description":"fake","artifacts":[],"capabilities":["install","mcp"],"source":"github.com/maleolabs/eka-mcp"}`, "", 0)
	installRegistrationPlugin(t, dir, "eka-mcp", body)
	t.Setenv("EKA_PLUGIN_DIR", dir)
	t.Setenv("PATH", t.TempDir())

	code, out, errText := runIn([]string{"--help"})
	if code != 0 {
		t.Fatalf("--help: exit = %d, want 0\nstderr: %s", code, errText)
	}
	if errText != "" {
		t.Errorf("stderr must be empty, got %q", errText)
	}
	if strings.Contains(out, "Plugins") {
		t.Errorf("a plugin without commands must not add the Plugins group:\n%s", out)
	}
}

// TestPluginDispatch: executing a registered command dispatches to the
// plugin executable with the declared args + the user's args, under
// the bounded env whitelist (the CLI's secrets are NOT inherited), and
// propagates the plugin's exit code (acceptance criteria: execution
// dispatches with bounded context; the args contract).
func TestPluginDispatch(t *testing.T) {
	dir := t.TempDir()
	body := registrationPluginScript(
		registrationManifest("mcp", pluginCommandSpec{Name: "mcp", Description: "run the server", Args: []string{"serve"}}),
		"", 0)
	installRegistrationPlugin(t, dir, "eka-mcp", body)
	t.Setenv("EKA_PLUGIN_DIR", dir)
	t.Setenv("PATH", t.TempDir())
	t.Setenv("GH_TOKEN", "sekrit")

	// The plugin echoes its args; the args contract is
	// "eka-mcp serve extra arg".
	code, out, errText := runIn([]string{"mcp", "extra", "arg"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (plugin exit code propagation)\nstderr: %s", code, errText)
	}
	if !strings.Contains(out, "serve extra arg") {
		t.Errorf("dispatch must pass the declared args + user args, got %q", out)
	}
	if strings.Contains(out, "sekrit") {
		t.Errorf("the bounded env must not inherit GH_TOKEN, got %q", out)
	}

	// Exit-code propagation: the plugin's exit code becomes the CLI's.
	body = registrationPluginScript(
		registrationManifest("mcp", pluginCommandSpec{Name: "fail", Description: "fails", Args: []string{"boom"}}),
		"", 3)
	installRegistrationPlugin(t, dir, "eka-mcp", body)
	code, _, _ = runIn([]string{"fail"})
	if code != 3 {
		t.Errorf("the plugin exit code must propagate, got %d, want 3", code)
	}
}

// TestPluginDispatchAntiTOCTOU: the binary's SHA-256 is recomputed at
// EVERY dispatch and compared against the checksum recorded at install
// — a binary swapped after registration refuses deterministically with
// exit 1 (acceptance criterion: hash verified at dispatch).
func TestPluginDispatchAntiTOCTOU(t *testing.T) {
	dir := t.TempDir()
	exe := writeLifecyclePlugin(t, dir, "eka-mcp", []byte("#!/bin/sh\nprintf '%s' ok\n"))
	// The sidecar records the ORIGINAL binary's checksum.
	writePluginSidecarFor(t, dir, "mcp", exe)
	sum, ok := readPluginChecksum(dir, "mcp")
	if !ok {
		t.Fatal("sidecar must be readable")
	}
	// Swap the binary AFTER the checksum was recorded (the TOCTOU
	// window between registration and dispatch).
	if err := os.WriteFile(exe, []byte("#!/bin/sh\necho tampered\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	var out, errb bytes.Buffer
	cmd := updateTestCommand(&out, &errb)
	err := dispatchPluginCommand(cmd, exe, "mcp", pluginCommandSpec{Name: "mcp", Args: []string{"serve"}}, sum, nil)
	if code := exitCodeOf(err); code != 1 {
		t.Fatalf("exit = %d, want 1 (refusal)\nstderr: %s", code, errb.String())
	}
	if !strings.Contains(errb.String(), "no longer matches the checksum recorded at install") {
		t.Errorf("the refusal must explain the checksum mismatch, got %q", errb.String())
	}
}

// TestPluginRegistrationSkipsTamperedBinary: a binary that does not
// match its recorded checksum at registration time never registers —
// skipped with a visible warning, the CLI still works (G2 defense in
// depth: a tampered binary is caught before it can even appear).
func TestPluginRegistrationSkipsTamperedBinary(t *testing.T) {
	dir := t.TempDir()
	body := registrationPluginScript(
		registrationManifest("mcp", pluginCommandSpec{Name: "mcp-serve", Description: "x"}),
		"", 0)
	exe := writeLifecyclePlugin(t, dir, "eka-mcp", body)
	// Record the checksum of the ORIGINAL content, then swap in a
	// DIFFERENT binary that still answers a valid manifest (so the
	// registration-time G2 check — not the manifest probe — is what
	// refuses it).
	writePluginSidecarFor(t, dir, "mcp", exe)
	// A DIFFERENT binary that still answers a valid manifest (the
	// shebang stays first; the extra comment changes the content, so
	// the registration-time G2 check — not the manifest probe — is
	// what refuses it).
	tampered := bytes.Replace(registrationPluginScript(
		registrationManifest("mcp", pluginCommandSpec{Name: "mcp-serve", Description: "x"}),
		"", 0), []byte("#!/bin/sh\n"), []byte("#!/bin/sh\n# tampered\n"), 1)
	if err := os.WriteFile(exe, tampered, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EKA_PLUGIN_DIR", dir)
	t.Setenv("PATH", t.TempDir())

	code, out, errText := runIn([]string{"--help"})
	if code != 0 {
		t.Fatalf("--help: exit = %d, want 0 (registration never breaks the CLI)\nstderr: %s", code, errText)
	}
	if !strings.Contains(errText, "does not match the checksum recorded at install") {
		t.Errorf("the skip must warn about the checksum mismatch, got %q", errText)
	}
	if strings.Contains(out, "mcp-serve") {
		t.Errorf("a tampered binary must not register commands:\n%s", out)
	}
}

// TestPluginPathOnlyRefused: a plugin found only on PATH without a
// registry install is refused with a deterministic message and never
// registers commands (G3: plugin dir over PATH; acceptance criterion:
// PATH-only plugins visibly refused).
func TestPluginPathOnlyRefused(t *testing.T) {
	pathDir := t.TempDir()
	body := registrationPluginScript(
		registrationManifest("mcp", pluginCommandSpec{Name: "mcp-serve", Description: "x"}),
		"", 0)
	writeLifecyclePlugin(t, pathDir, "eka-mcp", body)
	t.Setenv("EKA_PLUGIN_DIR", t.TempDir()) // plugin dir exists but is empty
	t.Setenv("PATH", pathDir)

	code, out, errText := runIn([]string{"--help"})
	if code != 0 {
		t.Fatalf("--help: exit = %d, want 0\nstderr: %s", code, errText)
	}
	if !strings.Contains(errText, "on PATH") || !strings.Contains(errText, "not installed in the plugin directory") ||
		!strings.Contains(errText, "eka plugin install mcp") {
		t.Errorf("the PATH-only refusal must be deterministic, got %q", errText)
	}
	if strings.Contains(out, "Plugins") || strings.Contains(out, "mcp-serve") {
		t.Errorf("a PATH-only plugin must not register commands:\n%s", out)
	}
}

// TestPluginPathShadowWarns: a PATH copy shadowing an installed plugin
// is ignored with a visible warning — the installed plugin (the plugin
// dir instance) is the one that registers and dispatches (G3).
func TestPluginPathShadowWarns(t *testing.T) {
	dir := t.TempDir()
	body := registrationPluginScript(
		registrationManifest("mcp", pluginCommandSpec{Name: "mcp-serve", Description: "x"}),
		"", 0)
	installRegistrationPlugin(t, dir, "eka-mcp", body)
	pathDir := t.TempDir()
	writeLifecyclePlugin(t, pathDir, "eka-mcp", []byte("#!/bin/sh\necho shadow\n"))
	t.Setenv("EKA_PLUGIN_DIR", dir)
	t.Setenv("PATH", pathDir)

	code, _, errText := runIn([]string{"--help"})
	if code != 0 {
		t.Fatalf("--help: exit = %d, want 0\nstderr: %s", code, errText)
	}
	if !strings.Contains(errText, "is also on PATH") || !strings.Contains(errText, "PATH copy is ignored") {
		t.Errorf("the shadow warning must be visible, got %q", errText)
	}
}

// TestPluginCollisionBuiltinRefused: a plugin command colliding with a
// built-in refuses deterministically — the built-in wins, the plugin
// command is not registered, and the refusal is a visible warning
// (acceptance criterion: collisions with built-ins refuse
// deterministically).
func TestPluginCollisionBuiltinRefused(t *testing.T) {
	dir := t.TempDir()
	body := registrationPluginScript(
		registrationManifest("mcp", pluginCommandSpec{Name: "validate", Description: "collides with the built-in"}),
		"", 0)
	installRegistrationPlugin(t, dir, "eka-mcp", body)
	t.Setenv("EKA_PLUGIN_DIR", dir)
	t.Setenv("PATH", t.TempDir())

	code, out, errText := runIn([]string{"--help"})
	if code != 0 {
		t.Fatalf("--help: exit = %d, want 0\nstderr: %s", code, errText)
	}
	if !strings.Contains(errText, `collides with the existing "validate" command`) ||
		!strings.Contains(errText, "not registered") {
		t.Errorf("the collision refusal must be deterministic, got %q", errText)
	}
	// The built-in validate stays; no plugin command was added (the
	// plugin registered nothing else).
	root := newRootCommand()
	if c := findCommand(root, "validate"); c == nil || c.GroupID != groupRepositoryExchange {
		t.Errorf("the built-in validate must be untouched: %v", c)
	}
	if strings.Contains(out, "collides with the built-in") {
		t.Errorf("the colliding command must not render in help:\n%s", out)
	}
}

// TestPluginCollisionCrossPluginRefused: two plugins declaring the
// same command name refuse deterministically — the first (sorted
// plugin name order) wins, the second is refused with a visible
// warning (acceptance criterion: collisions with other plugins refuse
// deterministically).
func TestPluginCollisionCrossPluginRefused(t *testing.T) {
	old := pluginRegistryOfficial
	pluginRegistryOfficial = func(name string) bool {
		return plugin.OfficialRegistry.IsOfficial(name) || name == "helper"
	}
	t.Cleanup(func() { pluginRegistryOfficial = old })

	dir := t.TempDir()
	shared := pluginCommandSpec{Name: "shared", Description: "both plugins want it"}
	installRegistrationPlugin(t, dir, "eka-helper",
		registrationPluginScript(registrationManifest("helper", shared), "", 0))
	installRegistrationPlugin(t, dir, "eka-mcp",
		registrationPluginScript(registrationManifest("mcp", shared), "", 0))
	t.Setenv("EKA_PLUGIN_DIR", dir)
	t.Setenv("PATH", t.TempDir())

	code, out, errText := runIn([]string{"--help"})
	if code != 0 {
		t.Fatalf("--help: exit = %d, want 0\nstderr: %s", code, errText)
	}
	// The winner is deterministic: plugins process in sorted name
	// order, so "helper" claims "shared" first and "mcp" is refused.
	if !strings.Contains(errText, `plugin "mcp" command "shared" collides`) {
		t.Errorf("the later plugin must be refused deterministically, got %q", errText)
	}
	// Exactly one "shared" command is registered.
	root := newRootCommand()
	count := 0
	for _, c := range root.Commands() {
		if c.Name() == "shared" {
			count++
			if c.GroupID != groupPlugins {
				t.Errorf("shared: GroupID = %q, want %q", c.GroupID, groupPlugins)
			}
		}
	}
	if count != 1 {
		t.Errorf("shared must be registered exactly once, got %d", count)
	}
	_ = out
}

// TestPluginRegistrationProbeTimeout: a plugin whose manifest hangs is
// killed after the probe deadline (<= 2s) and skipped with a visible
// warning — the CLI still works (acceptance criteria: probe timeout <=
// 2s; broken plugin skipped without breaking the CLI).
func TestPluginRegistrationProbeTimeout(t *testing.T) {
	if pluginProbeTimeout > 2*time.Second {
		t.Errorf("pluginProbeTimeout = %s, want <= 2s (acceptance criterion)", pluginProbeTimeout)
	}
	shrinkPluginProbeTimeout(t, 200*time.Millisecond)
	dir := t.TempDir()
	exe := writeLifecyclePlugin(t, dir, "eka-mcp", []byte("#!/bin/sh\nwhile true; do :; done\n"))
	writePluginSidecarFor(t, dir, "mcp", exe)
	t.Setenv("EKA_PLUGIN_DIR", dir)
	t.Setenv("PATH", t.TempDir())

	code, out, errText := runIn([]string{"--help"})
	if code != 0 {
		t.Fatalf("--help: exit = %d, want 0 (a hung plugin must not break the CLI)\nstderr: %s", code, errText)
	}
	if !strings.Contains(errText, "timed out") {
		t.Errorf("the skip must report the timeout, got %q", errText)
	}
	if strings.Contains(out, "Plugins") {
		t.Errorf("a hung plugin must not register commands:\n%s", out)
	}
}

// TestPluginRegistrationBrokenSkipped: a plugin whose manifest cannot
// be parsed is skipped with a visible warning — the CLI keeps working
// for every other command (acceptance criterion: broken plugin skipped
// with visible warning without breaking the CLI), and the warning is
// memoized per process (repeated root constructions print it once).
func TestPluginRegistrationBrokenSkipped(t *testing.T) {
	dir := t.TempDir()
	exe := writeLifecyclePlugin(t, dir, "eka-mcp", []byte("#!/bin/sh\nprintf '%s' 'this is not json'\n"))
	writePluginSidecarFor(t, dir, "mcp", exe)
	t.Setenv("EKA_PLUGIN_DIR", dir)
	t.Setenv("PATH", t.TempDir())

	code, out, errText := runIn([]string{"--help"})
	if code != 0 {
		t.Fatalf("--help: exit = %d, want 0\nstderr: %s", code, errText)
	}
	if !strings.Contains(errText, "is broken") || !strings.Contains(errText, "not registered") {
		t.Errorf("the skip must warn about the broken plugin, got %q", errText)
	}
	if strings.Contains(out, "Plugins") {
		t.Errorf("a broken plugin must not register commands:\n%s", out)
	}

	// The CLI keeps working: an unrelated command still runs.
	code, _, errText = runIn([]string{"version"})
	if code != 0 {
		t.Fatalf("version: exit = %d, want 0\nstderr: %s", code, errText)
	}
}

// TestPluginRegistrationUnverifiableSkipped: an official plugin
// without a recorded checksum (a legacy install predating B1) is
// skipped fail-closed with a visible warning and the reinstall hint —
// no command can run unverified.
func TestPluginRegistrationUnverifiableSkipped(t *testing.T) {
	dir := t.TempDir()
	body := registrationPluginScript(
		registrationManifest("mcp", pluginCommandSpec{Name: "mcp-serve", Description: "x"}),
		"", 0)
	writeLifecyclePlugin(t, dir, "eka-mcp", body) // NO sidecar.
	t.Setenv("EKA_PLUGIN_DIR", dir)
	t.Setenv("PATH", t.TempDir())

	code, out, errText := runIn([]string{"--help"})
	if code != 0 {
		t.Fatalf("--help: exit = %d, want 0\nstderr: %s", code, errText)
	}
	if !strings.Contains(errText, "no recorded checksum") || !strings.Contains(errText, "reinstall it with: eka plugin install mcp") {
		t.Errorf("the skip must explain the missing checksum and hint the reinstall, got %q", errText)
	}
	if strings.Contains(out, "mcp-serve") {
		t.Errorf("an unverifiable plugin must not register commands:\n%s", out)
	}
}

// TestPluginRegistrationInvalidCommandNameRefused: a manifest-declared
// command name outside the safe charset (uppercase, leading dash,
// path separator) is refused deterministically and never registered.
func TestPluginRegistrationInvalidCommandNameRefused(t *testing.T) {
	dir := t.TempDir()
	body := registrationPluginScript(
		registrationManifest("mcp", pluginCommandSpec{Name: "Bad-Name", Description: "x"}),
		"", 0)
	installRegistrationPlugin(t, dir, "eka-mcp", body)
	t.Setenv("EKA_PLUGIN_DIR", dir)
	t.Setenv("PATH", t.TempDir())

	code, out, errText := runIn([]string{"--help"})
	if code != 0 {
		t.Fatalf("--help: exit = %d, want 0\nstderr: %s", code, errText)
	}
	if !strings.Contains(errText, "not a valid command name") || !strings.Contains(errText, "not registered") {
		t.Errorf("the refusal must explain the invalid name, got %q", errText)
	}
	if strings.Contains(out, "Bad-Name") {
		t.Errorf("an invalid command name must not register:\n%s", out)
	}
}

// TestPluginRegistrationCacheTTLAndReinstall: the manifest probe is
// cached per process with a TTL and invalidated on reinstall — a
// second root construction reuses the cache (no re-probe), a rewritten
// binary (reinstall) re-probes, and an expired TTL re-probes
// (acceptance criteria: manifest cached with TTL and invalidated on
// reinstall).
func TestPluginRegistrationCacheTTLAndReinstall(t *testing.T) {
	dir := t.TempDir()
	counter := filepath.Join(t.TempDir(), "probe-count")
	manifest := registrationManifest("mcp", pluginCommandSpec{Name: "mcp-serve", Description: "x"})
	exe := installRegistrationPlugin(t, dir, "eka-mcp", registrationPluginScript(manifest, counter, 0))
	t.Setenv("EKA_PLUGIN_DIR", dir)
	t.Setenv("PATH", t.TempDir())

	count := func() int {
		b, err := os.ReadFile(counter)
		if err != nil {
			return 0
		}
		return strings.Count(string(b), "\n")
	}

	// First construction: cache miss, the probe runs once.
	newRootCommand()
	if got := count(); got != 1 {
		t.Fatalf("probe count after first construction = %d, want 1", got)
	}
	// Second construction: cache hit within the TTL — no re-probe
	// (per-process memoize).
	newRootCommand()
	if got := count(); got != 1 {
		t.Fatalf("probe count after cached construction = %d, want 1 (memoized)", got)
	}

	// Reinstall: a new binary (new identity) invalidates the cache —
	// the next construction re-probes.
	exe2 := installRegistrationPlugin(t, dir, "eka-mcp", registrationPluginScript(manifest, counter, 0))
	if exe2 != exe {
		t.Fatal("reinstall must write the same exe path")
	}
	newRootCommand()
	if got := count(); got != 2 {
		t.Fatalf("probe count after reinstall = %d, want 2 (invalidated on reinstall)", got)
	}

	// TTL expiry: with an expired TTL the next construction re-probes.
	shrinkPluginManifestCacheTTL(t, time.Nanosecond)
	newRootCommand()
	if got := count(); got != 3 {
		t.Fatalf("probe count after TTL expiry = %d, want 3", got)
	}
}

// TestPluginRegistrationDispatchMemoized: the dispatch decision (the
// expected checksum) is memoized per process — after registration, the
// dispatch of a registered command works even when the sidecar is
// removed (the checksum was bound into the command at registration;
// the anti-TOCTOU hash is still recomputed against it at every
// dispatch).
func TestPluginRegistrationDispatchMemoized(t *testing.T) {
	dir := t.TempDir()
	body := registrationPluginScript(
		registrationManifest("mcp", pluginCommandSpec{Name: "mcp-serve", Description: "x", Args: []string{"serve"}}),
		"", 0)
	exe := installRegistrationPlugin(t, dir, "eka-mcp", body)
	t.Setenv("EKA_PLUGIN_DIR", dir)
	t.Setenv("PATH", t.TempDir())

	// Registration reads the sidecar and binds the expected checksum
	// into the dispatch command (per-process memoize).
	root := newRootCommand()
	registered := findCommand(root, "mcp-serve")
	if registered == nil {
		t.Fatal("mcp-serve must be registered")
	}
	// Remove the sidecar: the dispatch decision is already memoized.
	if err := os.Remove(pluginSidecarPath(dir, "mcp")); err != nil {
		t.Fatal(err)
	}

	var out, errb bytes.Buffer
	registered.SetOut(&out)
	registered.SetErr(&errb)
	if err := registered.RunE(registered, []string{"extra"}); err != nil {
		t.Fatalf("dispatch after sidecar removal: %v\nstderr: %s", err, errb.String())
	}
	if !strings.Contains(out.String(), "serve extra") {
		t.Errorf("dispatch must pass the declared args + user args, got %q", out.String())
	}
	// The binary was not tampered — the memoized expected checksum
	// still matches (a tampered binary would refuse here).
	_ = exe
}
