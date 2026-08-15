package cmd

// This file tests the two-tier trust model of the plugin install and
// update flows (eka/sto:plugin-trust-model) and its hardening:
//
//   - official (registry-listed) plugins install without any prompt;
//   - third-party plugins (--repo owner/name, or a non-listed name on
//     update) require explicit consent after their source and
//     capabilities are surfaced, and a non-terminal run without --yes
//     refuses BEFORE any download (never auto-consent silently);
//   - both tiers verify the release checksum fail-closed;
//   - a declined consent installs nothing and leaves no temp debris;
//   - plugin names are validated against a charset whitelist (no path
//     traversal through the filepath.Join sink);
//   - attacker-controlled render strings (manifest fields, release
//     tags) are terminal-sanitized;
//   - the manifest subprocess runs under a minimal environment that
//     never carries the CLI's secrets (GH_TOKEN etc.).
//
// The tests are hermetic (httptest release server, injected plugin
// directory, fake shell-script plugin "binaries") and the interactive
// consent decision is injected as a stub (ui.Select needs a real
// terminal; the production prompt's non-terminal gate is exercised
// through the early determinism gate).

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/maleolabs/eka-cli/cmd/ui"
	"github.com/maleolabs/eka-cli/plugin"
	"github.com/spf13/cobra"
)

// thirdPartyManifestScript is the downloaded "binary" of the
// third-party happy path: a shell script answering "manifest" with a
// valid manifest for the acme/eka-helper plugin (name "helper",
// capability "mcp", source "github.com/acme/eka-helper").
const thirdPartyManifestScript = `#!/bin/sh
case "$1" in
  manifest) printf '%s' '{"contract":"v1","name":"helper","version":"0.1.0","description":"a third-party helper","artifacts":[],"capabilities":["mcp"],"source":"github.com/acme/eka-helper"}' ;;
esac
`

// consentStub builds a runner consent stub returning the given
// decision (the interactive ui.Select path cannot run without a real
// terminal).
func consentStub(ok bool) func(*cobra.Command, *ui.Style, string, string) (bool, error) {
	return func(*cobra.Command, *ui.Style, string, string) (bool, error) { return ok, nil }
}

// TestPluginInstallOfficialNoPrompt: an official (registry-listed)
// install never consults the consent decision — the run succeeds in a
// non-terminal context without --yes, renders no third-party surface
// and never invokes the consent stub (which fails the test if
// called). Acceptance criterion 1: official install has no prompt.
func TestPluginInstallOfficialNoPrompt(t *testing.T) {
	body := []byte(pluginManifestScript)
	srv := newFakePluginReleaseServer(t, plugin.Repo{Owner: "maleolabs", Name: "eka-mcp"},
		"v1.0.0", "eka-mcp-linux-amd64", sha256Hex(body), body)
	dir := t.TempDir()

	r := testPluginInstallRunner(srv, dir)
	r.consent = func(*cobra.Command, *ui.Style, string, string) (bool, error) {
		t.Fatal("official installs must never prompt for consent")
		return false, nil
	}
	var out, errb bytes.Buffer
	// No --yes: the non-terminal run would be refused for a third-party
	// plugin — for an official one it proceeds without any prompt.
	if err := r.run(updateTestCommand(&out, &errb), "mcp", &pluginInstallFlags{}); err != nil {
		t.Fatalf("run: %v\nstderr: %s", err, errb.String())
	}
	if strings.Contains(out.String(), "Trust") || strings.Contains(out.String(), "third-party") ||
		strings.Contains(out.String(), "Declared capabilities") {
		t.Errorf("official install must not render the third-party surface:\n%s", out.String())
	}
}

// TestPluginInstallThirdPartyConsentYes: a third-party install with
// --yes consents non-interactively: the source, declared capabilities
// and install target are still surfaced, and the install completes.
// Acceptance criteria 2+3.
func TestPluginInstallThirdPartyConsentYes(t *testing.T) {
	body := []byte(thirdPartyManifestScript)
	srv := newFakePluginReleaseServer(t, plugin.Repo{Owner: "acme", Name: "eka-helper"},
		"v0.1.0", "eka-helper-linux-amd64", sha256Hex(body), body)
	dir := t.TempDir()

	r := testPluginInstallRunner(srv, dir)
	r.consent = func(*cobra.Command, *ui.Style, string, string) (bool, error) {
		t.Fatal("--yes must skip the consent prompt")
		return false, nil
	}
	var out, errb bytes.Buffer
	if err := r.run(updateTestCommand(&out, &errb), "helper", &pluginInstallFlags{repo: "acme/eka-helper", yes: true}); err != nil {
		t.Fatalf("run: %v\nstderr: %s", err, errb.String())
	}
	target := filepath.Join(dir, "eka-helper")
	got, err := os.ReadFile(target)
	if err != nil || !bytes.Equal(got, body) {
		t.Errorf("installed binary must equal the verified asset (err %v)", err)
	}
	if fi, err := os.Stat(target); err != nil || fi.Mode().Perm() != 0o755 {
		t.Errorf("installed binary mode = %v, want 0755 (err %v)", fi.Mode().Perm(), err)
	}
	for _, want := range []string{
		"Trust     third-party", "Third-party plugin",
		"Source" + strings.Repeat(" ", 18) + "https://github.com/acme/eka-helper",
		"Summary" + strings.Repeat(" ", 17) + "a third-party helper",
		"Declared capabilities   mcp",
		"Install" + strings.Repeat(" ", 17) + target,
		"✓ installed: " + target,
		"third-party (consent given)",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output missing %q:\n%s", want, out.String())
		}
	}
	if names := pluginDirEntries(t, dir); len(names) != 1 || names[0] != "eka-helper" {
		t.Errorf("plugin dir must hold only eka-helper, found %v", names)
	}
}

// TestPluginInstallThirdPartyConsentAccepted: without --yes, the
// interactive consent decision (stubbed here) accepts — the source and
// declared capabilities are surfaced and the install completes.
func TestPluginInstallThirdPartyConsentAccepted(t *testing.T) {
	body := []byte(thirdPartyManifestScript)
	srv := newFakePluginReleaseServer(t, plugin.Repo{Owner: "acme", Name: "eka-helper"},
		"v0.1.0", "eka-helper-linux-amd64", sha256Hex(body), body)
	dir := t.TempDir()

	r := testPluginInstallRunner(srv, dir)
	r.canPrompt = func(*cobra.Command, *ui.Style) bool { return true } // the gate sees a terminal
	r.consent = consentStub(true)
	var out, errb bytes.Buffer
	if err := r.run(updateTestCommand(&out, &errb), "helper", &pluginInstallFlags{repo: "acme/eka-helper"}); err != nil {
		t.Fatalf("run: %v\nstderr: %s", err, errb.String())
	}
	if !strings.Contains(out.String(), "https://github.com/acme/eka-helper") ||
		!strings.Contains(out.String(), "Declared capabilities   mcp") {
		t.Errorf("consent flow must surface source and declared capabilities:\n%s", out.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "eka-helper")); err != nil {
		t.Errorf("a consented install must complete (err %v)", err)
	}
}

// TestPluginInstallThirdPartyConsentDeclined: without --yes, a
// declined consent refuses (exit 1), installs nothing and removes the
// staged download.
func TestPluginInstallThirdPartyConsentDeclined(t *testing.T) {
	body := []byte(thirdPartyManifestScript)
	srv := newFakePluginReleaseServer(t, plugin.Repo{Owner: "acme", Name: "eka-helper"},
		"v0.1.0", "eka-helper-linux-amd64", sha256Hex(body), body)
	dir := t.TempDir()

	r := testPluginInstallRunner(srv, dir)
	r.canPrompt = func(*cobra.Command, *ui.Style) bool { return true } // the gate sees a terminal
	r.consent = consentStub(false)
	var out, errb bytes.Buffer
	err := r.run(updateTestCommand(&out, &errb), "helper", &pluginInstallFlags{repo: "acme/eka-helper"})
	if code := exitCodeOf(err); code != 1 {
		t.Fatalf("exit = %d, want 1 (refusal)\nstderr: %s", code, errb.String())
	}
	if !strings.Contains(errb.String(), "consent") || !strings.Contains(errb.String(), "declined") ||
		!strings.Contains(errb.String(), "nothing was installed") {
		t.Errorf("refusal must report the declined consent, got %q", errb.String())
	}
	if names := pluginDirEntries(t, dir); len(names) != 0 {
		t.Errorf("a declined consent must install nothing and clean the staged download, found %v", names)
	}
}

// TestPluginInstallThirdPartyNonTTYRefused: a non-terminal run without
// --yes cannot consent — it refuses (exit 1) with the --yes hint
// BEFORE any download or staged execution (fail-closed: never
// auto-consent silently). The nil server proves no network call
// happens.
func TestPluginInstallThirdPartyNonTTYRefused(t *testing.T) {
	dir := t.TempDir()
	r := testPluginInstallRunner(nil, dir) // nil server = a network call would fail the run differently
	var out, errb bytes.Buffer
	err := r.run(updateTestCommand(&out, &errb), "helper", &pluginInstallFlags{repo: "acme/eka-helper"})
	if code := exitCodeOf(err); code != 1 {
		t.Fatalf("exit = %d, want 1 (refusal)\nstderr: %s", code, errb.String())
	}
	if !strings.Contains(errb.String(), "requires explicit consent") || !strings.Contains(errb.String(), "--yes") {
		t.Errorf("refusal must demand consent and hint --yes, got %q", errb.String())
	}
	if names := pluginDirEntries(t, dir); len(names) != 0 {
		t.Errorf("a refused consent must install nothing, found %v", names)
	}
}

// TestPluginInstallThirdPartyChecksumMismatch: the checksum gate
// applies to third-party plugins too — a wrong hash refuses
// fail-closed before anything is installed (acceptance criterion 3).
func TestPluginInstallThirdPartyChecksumMismatch(t *testing.T) {
	body := []byte(thirdPartyManifestScript)
	srv := newFakePluginReleaseServer(t, plugin.Repo{Owner: "acme", Name: "eka-helper"},
		"v0.1.0", "eka-helper-linux-amd64", sha256Hex([]byte("the expected binary")), body)
	dir := t.TempDir()

	r := testPluginInstallRunner(srv, dir)
	var out, errb bytes.Buffer
	err := r.run(updateTestCommand(&out, &errb), "helper", &pluginInstallFlags{repo: "acme/eka-helper", yes: true})
	if code := exitCodeOf(err); code != 1 {
		t.Fatalf("exit = %d, want 1 (refusal)\nstderr: %s", code, errb.String())
	}
	if !strings.Contains(errb.String(), "checksum mismatch") {
		t.Errorf("refusal must report the checksum mismatch, got %q", errb.String())
	}
	if names := pluginDirEntries(t, dir); len(names) != 0 {
		t.Errorf("nothing must be installed, found %v", names)
	}
}

// TestPluginInstallRepoMalformed: a malformed --repo value refuses
// (exit 1) before any network access.
func TestPluginInstallRepoMalformed(t *testing.T) {
	r := testPluginInstallRunner(nil, t.TempDir())
	var out, errb bytes.Buffer
	err := r.run(updateTestCommand(&out, &errb), "helper", &pluginInstallFlags{repo: "not-a-repo"})
	if code := exitCodeOf(err); code != 1 {
		t.Fatalf("exit = %d, want 1 (refusal)\nstderr: %s", code, errb.String())
	}
	if !strings.Contains(errb.String(), "invalid --repo") || !strings.Contains(errb.String(), "owner/name") {
		t.Errorf("refusal must reject the malformed --repo, got %q", errb.String())
	}
}

// TestPluginInstallRepoIsThirdParty: --repo makes the name third-party
// BY DEFINITION — even a maleolabs repository pinned via --repo
// bypasses the registry and requires consent (a non-TTY run refuses
// before any download).
func TestPluginInstallRepoIsThirdParty(t *testing.T) {
	r := testPluginInstallRunner(nil, t.TempDir())
	var out, errb bytes.Buffer
	err := r.run(updateTestCommand(&out, &errb), "mcp", &pluginInstallFlags{repo: "maleolabs/eka-mcp"})
	if code := exitCodeOf(err); code != 1 {
		t.Fatalf("exit = %d, want 1 (the --repo path is third-party and non-TTY refuses)\nstderr: %s", code, errb.String())
	}
	if !strings.Contains(errb.String(), "requires explicit consent") {
		t.Errorf("--repo must route through the third-party consent, got %q", errb.String())
	}
}

// TestPluginUpdateThirdPartyConsentYes: a named update of a
// third-party plugin resolves the repository from the installed
// manifest source, surfaces the source/declared capabilities and (with
// --yes) completes.
func TestPluginUpdateThirdPartyConsentYes(t *testing.T) {
	body := []byte(thirdPartyManifestScript) // manifest version 0.1.0
	srv := newFakePluginReleaseServer(t, plugin.Repo{Owner: "acme", Name: "eka-helper"},
		"v0.1.0", "eka-helper-linux-amd64", sha256Hex(body), body)
	dir := t.TempDir()
	// The installed OLD binary (manifest version 0.0.9, same source).
	old := []byte(`#!/bin/sh
case "$1" in
  manifest) printf '%s' '{"contract":"v1","name":"helper","version":"0.0.9","description":"a third-party helper","artifacts":[],"capabilities":["mcp"],"source":"github.com/acme/eka-helper"}' ;;
esac
`)
	writeLifecyclePlugin(t, dir, "eka-helper", old)

	r := testPluginInstallRunner(srv, dir)
	var out, errb bytes.Buffer
	if err := r.runUpdate(updateTestCommand(&out, &errb), "helper", true); err != nil {
		t.Fatalf("run: %v\nstderr: %s", err, errb.String())
	}
	if !strings.Contains(out.String(), "Repo      acme/eka-helper") ||
		!strings.Contains(out.String(), "Trust     third-party") ||
		!strings.Contains(out.String(), "Third-party plugin") ||
		!strings.Contains(out.String(), "Declared capabilities   mcp") ||
		!strings.Contains(out.String(), "0.0.9 → v0.1.0") {
		t.Errorf("update output missing the third-party surface/version:\n%s", out.String())
	}
	got, err := os.ReadFile(filepath.Join(dir, "eka-helper"))
	if err != nil || !bytes.Equal(got, body) {
		t.Errorf("eka-helper must be the new verified asset (err %v)", err)
	}
	if fi, err := os.Stat(filepath.Join(dir, "eka-helper")); err != nil || fi.Mode().Perm() != 0o755 {
		t.Errorf("updated binary mode = %v, want 0755 (err %v)", fi.Mode().Perm(), err)
	}
}

// TestPluginUpdateThirdPartyConsentDeclined: a declined consent for a
// third-party update refuses (exit 1) and leaves the old binary
// untouched.
func TestPluginUpdateThirdPartyConsentDeclined(t *testing.T) {
	body := []byte(thirdPartyManifestScript)
	srv := newFakePluginReleaseServer(t, plugin.Repo{Owner: "acme", Name: "eka-helper"},
		"v0.1.0", "eka-helper-linux-amd64", sha256Hex(body), body)
	dir := t.TempDir()
	old := []byte(`#!/bin/sh
case "$1" in
  manifest) printf '%s' '{"contract":"v1","name":"helper","version":"0.0.9","description":"a third-party helper","artifacts":[],"capabilities":["mcp"],"source":"github.com/acme/eka-helper"}' ;;
esac
`)
	writeLifecyclePlugin(t, dir, "eka-helper", old)

	r := testPluginInstallRunner(srv, dir)
	r.canPrompt = func(*cobra.Command, *ui.Style) bool { return true } // the gate sees a terminal
	r.consent = consentStub(false)
	var out, errb bytes.Buffer
	err := r.runUpdate(updateTestCommand(&out, &errb), "helper", false)
	if code := exitCodeOf(err); code != 1 {
		t.Fatalf("exit = %d, want 1 (refusal)\nstderr: %s", code, errb.String())
	}
	if !strings.Contains(errb.String(), "consent") || !strings.Contains(errb.String(), "the existing installation is unchanged") {
		t.Errorf("refusal must report the declined consent, got %q", errb.String())
	}
	got, err := os.ReadFile(filepath.Join(dir, "eka-helper"))
	if err != nil || !bytes.Equal(got, old) {
		t.Errorf("a declined consent must keep the old binary intact (err %v)", err)
	}
	if names := pluginDirEntries(t, dir); len(names) != 1 || names[0] != "eka-helper" {
		t.Errorf("no .old or temp debris may remain, found %v", names)
	}
}

// TestPluginUpdateThirdPartyNonTTYRefused: a non-terminal third-party
// update without --yes refuses with the --yes hint BEFORE any download
// (nil server proves no network call) and leaves the old binary
// untouched.
func TestPluginUpdateThirdPartyNonTTYRefused(t *testing.T) {
	dir := t.TempDir()
	old := []byte(lifecycleManifestScript("0.0.9", "github.com/acme/eka-helper"))
	writeLifecyclePlugin(t, dir, "eka-helper", old)

	r := testPluginInstallRunner(nil, dir) // nil server = a network call would fail the run differently
	var out, errb bytes.Buffer
	err := r.runUpdate(updateTestCommand(&out, &errb), "helper", false)
	if code := exitCodeOf(err); code != 1 {
		t.Fatalf("exit = %d, want 1 (refusal)\nstderr: %s", code, errb.String())
	}
	if !strings.Contains(errb.String(), "requires explicit consent") || !strings.Contains(errb.String(), "--yes") {
		t.Errorf("refusal must demand consent and hint --yes, got %q", errb.String())
	}
	got, err := os.ReadFile(filepath.Join(dir, "eka-helper"))
	if err != nil || !bytes.Equal(got, old) {
		t.Errorf("a refused update must keep the old binary intact (err %v)", err)
	}
}

// TestPluginUpdateThirdPartyLegacyNoSource: a third-party plugin whose
// installed manifest carries no resolvable source (legacy) cannot be
// updated by name — it refuses with the reinstall hint.
func TestPluginUpdateThirdPartyLegacyNoSource(t *testing.T) {
	dir := t.TempDir()
	writeLifecyclePlugin(t, dir, "eka-helper", []byte(`#!/bin/sh
case "$1" in
  manifest) printf '%s' '{"contract":"v1","name":"helper","version":"0.0.9","artifacts":[]}' ;;
esac
`))
	r := testPluginInstallRunner(nil, dir)
	var out, errb bytes.Buffer
	err := r.runUpdate(updateTestCommand(&out, &errb), "helper", true)
	if code := exitCodeOf(err); code != 1 {
		t.Fatalf("exit = %d, want 1 (refusal)\nstderr: %s", code, errb.String())
	}
	if !strings.Contains(errb.String(), "not a github.com repository URL") || !strings.Contains(errb.String(), "--repo") {
		t.Errorf("refusal must explain the missing source and hint --repo, got %q", errb.String())
	}
}

// --- Name validation (HIGH: path traversal) ---------------------------

// TestValidPluginName: the plugin-name charset whitelist — a single
// eka-<name> path segment, never a traversal. Note: "a.b.c" is a
// single safe segment and is ACCEPTED (the regex
// ^[a-zA-Z0-9][a-zA-Z0-9._-]*$ allows dots after the first char).
func TestValidPluginName(t *testing.T) {
	accept := []string{"mcp", "a.b.c", "mcp-v2", "mcp_2", "MCP", "x9"}
	for _, name := range accept {
		if !validPluginName(name) {
			t.Errorf("validPluginName(%q) = false, want true", name)
		}
	}
	reject := []string{
		"", ".", "..", "...", "../../x", "a/b", `a\b`, "a b", "a.b/",
		"a\x1bb", "a\nb", ".hidden", "-dash", "ünïcode",
	}
	for _, name := range reject {
		if validPluginName(name) {
			t.Errorf("validPluginName(%q) = true, want false", name)
		}
	}
}

// TestPluginInstallInvalidName: a traversal or malformed name refuses
// before any network access (nil server) — the filepath.Join sink is
// never reached.
func TestPluginInstallInvalidName(t *testing.T) {
	for _, name := range []string{"../../x", "a/b", "..", ".", "a b", "a\x1bb"} {
		r := testPluginInstallRunner(nil, t.TempDir())
		var out, errb bytes.Buffer
		err := r.run(updateTestCommand(&out, &errb), name, &pluginInstallFlags{repo: "acme/eka-helper", yes: true})
		if code := exitCodeOf(err); code != 1 {
			t.Errorf("%q: exit = %d, want 1 (refusal)\nstderr: %s", name, code, errb.String())
			continue
		}
		if !strings.Contains(errb.String(), "invalid plugin name") {
			t.Errorf("%q: refusal must report the invalid name, got %q", name, errb.String())
		}
	}
}

// TestPluginUpdateInvalidName: the update flow applies the same name
// guard before any network or filesystem use.
func TestPluginUpdateInvalidName(t *testing.T) {
	r := testPluginInstallRunner(nil, t.TempDir())
	var out, errb bytes.Buffer
	err := r.runUpdate(updateTestCommand(&out, &errb), "../../x", true)
	if code := exitCodeOf(err); code != 1 {
		t.Fatalf("exit = %d, want 1 (refusal)\nstderr: %s", code, errb.String())
	}
	if !strings.Contains(errb.String(), "invalid plugin name") {
		t.Errorf("refusal must report the invalid name, got %q", errb.String())
	}
}

// TestPluginRemoveInvalidName: the remove flow applies the same name
// guard before any filesystem use.
func TestPluginRemoveInvalidName(t *testing.T) {
	r := &pluginRemoveRunner{pluginDir: t.TempDir(), goos: "linux"}
	var out, errb bytes.Buffer
	err := r.run(updateTestCommand(&out, &errb), "../../x")
	if code := exitCodeOf(err); code != 1 {
		t.Fatalf("exit = %d, want 1 (refusal)\nstderr: %s", code, errb.String())
	}
	if !strings.Contains(errb.String(), "invalid plugin name") {
		t.Errorf("refusal must report the invalid name, got %q", errb.String())
	}
}

// --- Terminal sanitization (M2) ----------------------------------------

// TestSanitizeTerminal: control bytes below 0x20 (except \t \n \r),
// DEL and C1 are replaced; plain text passes through unchanged.
func TestSanitizeTerminal(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"plain text", "plain text"},
		{"line\nfeed\ttab\r", "line\nfeed\ttab\r"},
		{"\x1b]52;l;x\x07", "\uFFFD]52;l;x\uFFFD"},
		{"\x00\x1f\x7f", "\uFFFD\uFFFD\uFFFD"},
		{"\xc2\x80\xc2\x9f", "\uFFFD\uFFFD"}, // C1 (U+0080-U+009F)
		{"safe \u20AC euro", "safe \u20AC euro"},
	}
	for _, c := range cases {
		if got := sanitizeTerminal(c.in); got != c.want {
			t.Errorf("sanitizeTerminal(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestPluginInstallThirdPartyAnsiSanitized: a malicious manifest's
// description and capabilities (OSC-52 clipboard exfil, screen clear,
// BEL) and a malicious release tag must not inject terminal sequences
// into the consent output. The manifest JSON carries \u001b escapes
// (valid JSON, decoded to real ESC bytes by json.Unmarshal), so the
// staged binary passes the smoke check and its self-reported strings
// reach the consent render — where sanitizeTerminal must neutralize
// them.
func TestPluginInstallThirdPartyAnsiSanitized(t *testing.T) {
	// Literal backslash-u escapes: valid JSON, decoded to 0x1b.
	manifest := `{"contract":"v1","name":"helper","version":"0.1.0","description":"\u001b]52;l;secret\u001bgood","artifacts":[],"capabilities":["mcp\u001b[2J","safe"],"source":"github.com/acme/eka-helper"}`
	body := []byte("#!/bin/sh\ncase \"$1\" in\n  manifest) printf '%s' '" + manifest + "' ;;\nesac\n")
	srv := newFakePluginReleaseServer(t, plugin.Repo{Owner: "acme", Name: "eka-helper"},
		"v0.1.0", "eka-helper-linux-amd64", sha256Hex(body), body)
	dir := t.TempDir()

	r := testPluginInstallRunner(srv, dir)
	var out, errb bytes.Buffer
	if err := r.run(updateTestCommand(&out, &errb), "helper", &pluginInstallFlags{repo: "acme/eka-helper", yes: true}); err != nil {
		t.Fatalf("run: %v\nstderr: %s", err, errb.String())
	}
	if strings.Contains(out.String(), "\x1b") {
		t.Errorf("consent output must not carry ESC sequences (sanitized):\n%q", out.String())
	}
	// The sanitized description and capabilities are still surfaced.
	for _, want := range []string{"good", "safe", "mcp\uFFFD[2J"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("consent output missing the sanitized value %q:\n%s", want, out.String())
		}
	}
}

// TestPluginRenderHeaderSanitizesTag: a malicious release tag is
// rendered sanitized in both headers.
func TestPluginRenderHeaderSanitizesTag(t *testing.T) {
	var out bytes.Buffer
	s := ui.NewStyle(&out, false)
	r := &pluginInstallRunner{}
	r.renderHeader(s, "mcp", plugin.Repo{Owner: "maleolabs", Name: "eka-mcp"}, "eka-mcp-linux-amd64", "v1.0.0\x1b[2J", false)
	if strings.Contains(out.String(), "\x1b") {
		t.Errorf("install header must sanitize the release tag:\n%q", out.String())
	}

	var out2 bytes.Buffer
	s2 := ui.NewStyle(&out2, false)
	r.renderUpdateHeader(s2, "mcp", plugin.Repo{Owner: "maleolabs", Name: "eka-mcp"}, "eka-mcp-linux-amd64", "v1.0.0\x1b]52;l;x\x07", "0.9.0\x1b[2J", false)
	if strings.Contains(out2.String(), "\x1b") {
		t.Errorf("update header must sanitize the release tag and current version:\n%q", out2.String())
	}
}

// --- Minimal subprocess environment (M3) -------------------------------

// TestPluginSubprocessEnvMinimal: the manifest subprocess runs under a
// minimal environment whitelist (PATH, HOME, EKA_PLUGIN_DIR) — the
// CLI's secrets (GH_TOKEN, SSH_AUTH_SOCK) are NOT inherited, so a
// third-party binary executed for its manifest before consent cannot
// read them.
func TestPluginSubprocessEnvMinimal(t *testing.T) {
	dir := t.TempDir()
	home := t.TempDir()
	t.Setenv("EKA_PLUGIN_DIR", dir)
	t.Setenv("HOME", home)
	t.Setenv("GH_TOKEN", "super-secret")
	t.Setenv("SSH_AUTH_SOCK", "/tmp/ssh-agent.sock")
	// The fake exe dumps its environment into a file under
	// EKA_PLUGIN_DIR (a whitelisted variable) while answering the
	// manifest.
	body := []byte(`#!/bin/sh
case "$1" in
  manifest) env > "$EKA_PLUGIN_DIR/env.txt"; printf '%s' '{"contract":"v1","name":"helper","version":"0.1.0","description":"fake","artifacts":[],"capabilities":["mcp"],"source":"github.com/acme/eka-helper"}' ;;
esac
`)
	srv := newFakePluginReleaseServer(t, plugin.Repo{Owner: "acme", Name: "eka-helper"},
		"v0.1.0", "eka-helper-linux-amd64", sha256Hex(body), body)
	r := testPluginInstallRunner(srv, dir)
	var out, errb bytes.Buffer
	if err := r.run(updateTestCommand(&out, &errb), "helper", &pluginInstallFlags{repo: "acme/eka-helper", yes: true}); err != nil {
		t.Fatalf("run: %v\nstderr: %s", err, errb.String())
	}
	envBytes, err := os.ReadFile(filepath.Join(dir, "env.txt"))
	if err != nil {
		t.Fatalf("the manifest subprocess must have written its env dump: %v", err)
	}
	envText := string(envBytes)
	if strings.Contains(envText, "GH_TOKEN") || strings.Contains(envText, "SSH_AUTH_SOCK") {
		t.Errorf("the subprocess environment must not carry the CLI's secrets, got:\n%s", envText)
	}
	for _, want := range []string{"PATH=", "HOME=" + home, "EKA_PLUGIN_DIR=" + dir} {
		if !strings.Contains(envText, want) {
			t.Errorf("the subprocess environment must keep %q, got:\n%s", want, envText)
		}
	}
}

// --- Source-swap surfacing (L3) ----------------------------------------

// TestPluginInstallSourceSwapWarns: a binary whose manifest claims a
// different source than the one it was downloaded from is surfaced as
// a warning (never silently) — the self-reported manifest is not
// grounds for refusal on install.
func TestPluginInstallSourceSwapWarns(t *testing.T) {
	body := []byte(`#!/bin/sh
case "$1" in
  manifest) printf '%s' '{"contract":"v1","name":"helper","version":"0.1.0","description":"fake","artifacts":[],"capabilities":["mcp"],"source":"github.com/evil/eka-helper"}' ;;
esac
`)
	srv := newFakePluginReleaseServer(t, plugin.Repo{Owner: "acme", Name: "eka-helper"},
		"v0.1.0", "eka-helper-linux-amd64", sha256Hex(body), body)
	dir := t.TempDir()

	r := testPluginInstallRunner(srv, dir)
	var out, errb bytes.Buffer
	if err := r.run(updateTestCommand(&out, &errb), "helper", &pluginInstallFlags{repo: "acme/eka-helper", yes: true}); err != nil {
		t.Fatalf("run: %v\nstderr: %s", err, errb.String())
	}
	if !strings.Contains(out.String(), "reports source") || !strings.Contains(out.String(), "evil/eka-helper") {
		t.Errorf("a source mismatch on install must be surfaced as a warning:\n%s", out.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "eka-helper")); err != nil {
		t.Errorf("a source-mismatch warning must not block the install (err %v)", err)
	}
}

// TestPluginUpdateThirdPartySourceSwapRefused: on update a new binary
// whose manifest claims a different source than the installed one is
// refused fail-closed (exit 1) — the old binary stays untouched.
func TestPluginUpdateThirdPartySourceSwapRefused(t *testing.T) {
	body := []byte(`#!/bin/sh
case "$1" in
  manifest) printf '%s' '{"contract":"v1","name":"helper","version":"0.1.0","description":"fake","artifacts":[],"capabilities":["mcp"],"source":"github.com/evil/eka-helper"}' ;;
esac
`)
	srv := newFakePluginReleaseServer(t, plugin.Repo{Owner: "acme", Name: "eka-helper"},
		"v0.1.0", "eka-helper-linux-amd64", sha256Hex(body), body)
	dir := t.TempDir()
	old := []byte(lifecycleManifestScript("0.0.9", "github.com/acme/eka-helper"))
	writeLifecyclePlugin(t, dir, "eka-helper", old)

	r := testPluginInstallRunner(srv, dir)
	var out, errb bytes.Buffer
	err := r.runUpdate(updateTestCommand(&out, &errb), "helper", true)
	if code := exitCodeOf(err); code != 1 {
		t.Fatalf("exit = %d, want 1 (refusal)\nstderr: %s", code, errb.String())
	}
	if !strings.Contains(errb.String(), "source swap refused") {
		t.Errorf("refusal must report the source swap, got %q", errb.String())
	}
	got, err := os.ReadFile(filepath.Join(dir, "eka-helper"))
	if err != nil || !bytes.Equal(got, old) {
		t.Errorf("a source-swap refusal must keep the old binary intact (err %v)", err)
	}
}

// --- Repo reference validation (M4) ------------------------------------

// TestParsePluginRepo: the --repo validation contract — owner/name
// charset, no ".", "..", ".git" suffix, no URL-structure characters.
func TestParsePluginRepo(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"acme/eka-helper", "acme/eka-helper", false},
		{"maleolabs/eka-mcp", "maleolabs/eka-mcp", false},
		{"a/b", "a/b", false},
		{"acme/foo.bar", "acme/foo.bar", false},
		{"not-a-repo", "", true},
		{"/nope", "", true},
		{"nope/", "", true},
		{"a/b/c", "", true},
		{"a b/c", "", true},
		{"acme/foo.git", "", true},
		{"acme/..", "", true},
		{"acme/.", "", true},
		{"a%2Fb/c", "", true},
		{"a#b/c", "", true},
		{"a?b/c", "", true},
		{`a\b/c`, "", true},
		{"", "", true},
	}
	for _, c := range cases {
		got, err := parsePluginRepo(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("%q: must refuse, got %v", c.in, got)
			}
			continue
		}
		if err != nil || got.String() != c.want {
			t.Errorf("%q: got %v, %v; want %s", c.in, got, err, c.want)
		}
	}
}

// TestParsePluginSource: the manifest-source derivation contract —
// only github.com/owner/name, safe charset (used by the third-party
// update path).
func TestParsePluginSource(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"github.com/acme/eka-helper", "acme/eka-helper", false},
		{"https://github.com/acme/eka-helper", "acme/eka-helper", false},
		{"http://github.com/acme/eka-helper", "acme/eka-helper", false},
		{"github.com/acme/eka-helper/", "acme/eka-helper", false},
		{"github.com/acme/foo.git", "", true},
		{"github.com/acme/..", "", true},
		{"github.com/acme/eka-helper/extra", "", true},
		{"gitlab.com/team/repo", "", true},
		{"https://evil.com/github.com/acme/eka-helper", "", true},
		{"", "", true},
		{"nope", "", true},
	}
	for _, c := range cases {
		got, err := parsePluginSource(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("%q: must refuse, got %v", c.in, got)
			}
			continue
		}
		if err != nil || got.String() != c.want {
			t.Errorf("%q: got %v, %v; want %s", c.in, got, err, c.want)
		}
	}
}
