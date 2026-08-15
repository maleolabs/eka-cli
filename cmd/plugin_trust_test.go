package cmd

// This file tests the two-tier trust model of the plugin install and
// update flows (eka/sto:plugin-trust-model):
//
//   - official (registry-listed) plugins install without any prompt;
//   - third-party plugins (--repo owner/name, or a non-listed name on
//     update) require explicit consent after their source and
//     capabilities are surfaced, and a non-terminal run without --yes
//     refuses (fail-closed — the CLI never auto-consents silently);
//   - both tiers verify the release checksum fail-closed;
//   - a declined consent installs nothing and leaves no temp debris.
//
// The tests are hermetic (httptest release server, injected plugin
// directory, fake shell-script plugin "binaries") and the interactive
// consent decision is injected as a stub (ui.Select needs a real
// terminal; the production prompt's non-terminal refusal is exercised
// directly).

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
func consentStub(ok bool) func(*cobra.Command, *ui.Style, string) (bool, error) {
	return func(*cobra.Command, *ui.Style, string) (bool, error) { return ok, nil }
}

// TestPluginInstallOfficialNoPrompt: an official (registry-listed)
// install never consults the consent decision — the run succeeds in a
// non-terminal context without --yes, renders no "third-party"
// surface and never invokes the consent stub (which fails the test if
// called). Acceptance criterion 1: official install has no prompt.
func TestPluginInstallOfficialNoPrompt(t *testing.T) {
	body := []byte(pluginManifestScript)
	srv := newFakePluginReleaseServer(t, plugin.Repo{Owner: "maleolabs", Name: "eka-mcp"},
		"v1.0.0", "eka-mcp-linux-amd64", sha256Hex(body), body)
	dir := t.TempDir()

	r := testPluginInstallRunner(srv, dir)
	r.consent = func(*cobra.Command, *ui.Style, string) (bool, error) {
		t.Fatal("official installs must never prompt for consent")
		return false, nil
	}
	var out, errb bytes.Buffer
	// No --yes: the non-terminal run would be refused for a third-party
	// plugin — for an official one it proceeds without any prompt.
	if err := r.run(updateTestCommand(&out, &errb), "mcp", &pluginInstallFlags{}); err != nil {
		t.Fatalf("run: %v\nstderr: %s", err, errb.String())
	}
	if strings.Contains(out.String(), "third-party") || strings.Contains(out.String(), "Capabilities") {
		t.Errorf("official install must not render the third-party surface:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "Trust") {
		t.Logf("note: official install carries no Trust row (full-trust)")
	}
}

// TestPluginInstallThirdPartyConsentYes: a third-party install with
// --yes consents non-interactively: the source and capabilities are
// still surfaced, and the install completes. Acceptance criteria 2+3.
func TestPluginInstallThirdPartyConsentYes(t *testing.T) {
	body := []byte(thirdPartyManifestScript)
	srv := newFakePluginReleaseServer(t, plugin.Repo{Owner: "acme", Name: "eka-helper"},
		"v0.1.0", "eka-helper-linux-amd64", sha256Hex(body), body)
	dir := t.TempDir()

	r := testPluginInstallRunner(srv, dir)
	r.consent = func(*cobra.Command, *ui.Style, string) (bool, error) {
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
	for _, want := range []string{
		"Trust     third-party", "Third-party plugin",
		"Source         https://github.com/acme/eka-helper",
		"Summary        a third-party helper",
		"Capabilities   mcp",
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
// capabilities are surfaced and the install completes.
func TestPluginInstallThirdPartyConsentAccepted(t *testing.T) {
	body := []byte(thirdPartyManifestScript)
	srv := newFakePluginReleaseServer(t, plugin.Repo{Owner: "acme", Name: "eka-helper"},
		"v0.1.0", "eka-helper-linux-amd64", sha256Hex(body), body)
	dir := t.TempDir()

	r := testPluginInstallRunner(srv, dir)
	r.consent = consentStub(true)
	var out, errb bytes.Buffer
	if err := r.run(updateTestCommand(&out, &errb), "helper", &pluginInstallFlags{repo: "acme/eka-helper"}); err != nil {
		t.Fatalf("run: %v\nstderr: %s", err, errb.String())
	}
	if !strings.Contains(out.String(), "https://github.com/acme/eka-helper") ||
		!strings.Contains(out.String(), "Capabilities   mcp") {
		t.Errorf("consent flow must surface source and capabilities:\n%s", out.String())
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
	r.consent = consentStub(false)
	var out, errb bytes.Buffer
	err := r.run(updateTestCommand(&out, &errb), "helper", &pluginInstallFlags{repo: "acme/eka-helper"})
	if code := exitCodeOf(err); code != 1 {
		t.Fatalf("exit = %d, want 1 (refusal)\nstderr: %s", code, errb.String())
	}
	if !strings.Contains(errb.String(), "consent") || !strings.Contains(errb.String(), "declined") {
		t.Errorf("refusal must report the declined consent, got %q", errb.String())
	}
	if names := pluginDirEntries(t, dir); len(names) != 0 {
		t.Errorf("a declined consent must install nothing and clean the staged download, found %v", names)
	}
}

// TestPluginInstallThirdPartyNonTTYRefused: a non-terminal run without
// --yes cannot consent — it refuses (exit 1) with the --yes hint,
// installs nothing and removes the staged download (fail-closed:
// never auto-consent silently).
func TestPluginInstallThirdPartyNonTTYRefused(t *testing.T) {
	body := []byte(thirdPartyManifestScript)
	srv := newFakePluginReleaseServer(t, plugin.Repo{Owner: "acme", Name: "eka-helper"},
		"v0.1.0", "eka-helper-linux-amd64", sha256Hex(body), body)
	dir := t.TempDir()

	r := testPluginInstallRunner(srv, dir) // production consent prompt
	var out, errb bytes.Buffer
	err := r.run(updateTestCommand(&out, &errb), "helper", &pluginInstallFlags{repo: "acme/eka-helper"})
	if code := exitCodeOf(err); code != 1 {
		t.Fatalf("exit = %d, want 1 (refusal)\nstderr: %s", code, errb.String())
	}
	if !strings.Contains(errb.String(), "requires explicit consent") || !strings.Contains(errb.String(), "--yes") {
		t.Errorf("refusal must demand consent and hint --yes, got %q", errb.String())
	}
	if names := pluginDirEntries(t, dir); len(names) != 0 {
		t.Errorf("a refused consent must install nothing and clean the staged download, found %v", names)
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
// bypasses the registry and requires consent.
func TestPluginInstallRepoIsThirdParty(t *testing.T) {
	body := []byte(pluginManifestScript)
	srv := newFakePluginReleaseServer(t, plugin.Repo{Owner: "maleolabs", Name: "eka-mcp"},
		"v1.0.0", "eka-mcp-linux-amd64", sha256Hex(body), body)
	dir := t.TempDir()

	r := testPluginInstallRunner(srv, dir)
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
// manifest source, surfaces the source/capabilities and (with --yes)
// completes.
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
		!strings.Contains(out.String(), "Capabilities   mcp") ||
		!strings.Contains(out.String(), "0.0.9 → v0.1.0") {
		t.Errorf("update output missing the third-party surface/version:\n%s", out.String())
	}
	got, err := os.ReadFile(filepath.Join(dir, "eka-helper"))
	if err != nil || !bytes.Equal(got, body) {
		t.Errorf("eka-helper must be the new verified asset (err %v)", err)
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
	r.consent = consentStub(false)
	var out, errb bytes.Buffer
	err := r.runUpdate(updateTestCommand(&out, &errb), "helper", false)
	if code := exitCodeOf(err); code != 1 {
		t.Fatalf("exit = %d, want 1 (refusal)\nstderr: %s", code, errb.String())
	}
	if !strings.Contains(errb.String(), "consent") {
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
// update without --yes refuses with the --yes hint and leaves the old
// binary untouched.
func TestPluginUpdateThirdPartyNonTTYRefused(t *testing.T) {
	body := []byte(thirdPartyManifestScript)
	srv := newFakePluginReleaseServer(t, plugin.Repo{Owner: "acme", Name: "eka-helper"},
		"v0.1.0", "eka-helper-linux-amd64", sha256Hex(body), body)
	dir := t.TempDir()
	old := []byte(lifecycleManifestScript("0.0.9", "github.com/acme/eka-helper"))
	writeLifecyclePlugin(t, dir, "eka-helper", old)

	r := testPluginInstallRunner(srv, dir)
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
	if !strings.Contains(errb.String(), "not a resolvable repository") || !strings.Contains(errb.String(), "--repo") {
		t.Errorf("refusal must explain the missing source and hint --repo, got %q", errb.String())
	}
}

// TestParsePluginRepo: the --repo validation contract.
func TestParsePluginRepo(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"acme/eka-helper", "acme/eka-helper", false},
		{"maleolabs/eka-mcp", "maleolabs/eka-mcp", false},
		{"a/b", "a/b", false},
		{"not-a-repo", "", true},
		{"/nope", "", true},
		{"nope/", "", true},
		{"a/b/c", "", true},
		{"a b/c", "", true},
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

// TestParsePluginSource: the manifest-source derivation contract
// (used by the third-party update path).
func TestParsePluginSource(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"github.com/acme/eka-helper", "acme/eka-helper", false},
		{"https://github.com/acme/eka-helper", "acme/eka-helper", false},
		{"http://github.com/acme/eka-helper", "acme/eka-helper", false},
		{"gitlab.com/team/repo", "team/repo", false},
		{"github.com/acme/eka-helper/", "acme/eka-helper", false},
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
