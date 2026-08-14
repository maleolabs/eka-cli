package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The install tests are hermetic: every write goes through --dir into a
// temporary directory, and the plugin search is pinned to a temporary
// EKA_PLUGIN_DIR/PATH so no ambient eka-* executable on the machine can
// interfere. The artifact source is a fake "eka-mcp" plugin executable
// (a shell script) implementing the plugin contract: "manifest --json"
// and "install <kind> --dir <dir> [--dry-run] --json".

const fakePluginScript = `#!/bin/sh
# Fake EKA plugin (eka-mcp) implementing the plugin contract (v1) for
# the hermetic install tests: a deterministic manifest and a
# deterministic install that writes one marker file per artifact.
case "$1" in
  manifest)
    cat <<'EOF'
{"contract":"v1","name":"mcp","version":"9.9.9","description":"fake pack provider","artifacts":[{"kind":"skills","entries":["eka-orientation","eka-knowledge-authoring"]},{"kind":"commands","entries":["eka-discuss.md","eka-execute.md"]}]}
EOF
    ;;
  install)
    kind="$2"
    dir=""
    dry=""
    shift 2
    while [ "$#" -gt 0 ]; do
      case "$1" in
        --dir) dir="$2"; shift 2 ;;
        --dry-run) dry=1; shift ;;
        --json) shift ;;
        *) shift ;;
      esac
    done
    if [ -z "$dry" ]; then
      mkdir -p "$dir"
      case "$kind" in
        skills)
          mkdir -p "$dir/eka-orientation" "$dir/eka-knowledge-authoring"
          printf 'skill' > "$dir/eka-orientation/SKILL.md"
          printf 'skill' > "$dir/eka-knowledge-authoring/SKILL.md"
          ;;
        commands)
          printf 'command' > "$dir/eka-discuss.md"
          printf 'command' > "$dir/eka-execute.md"
          ;;
      esac
    fi
    case "$kind" in
      skills) printf '%s' '{"installed":["eka-knowledge-authoring","eka-orientation"],"version":"9.9.9"}' ;;
      commands) printf '%s' '{"installed":["eka-discuss.md","eka-execute.md"],"version":"9.9.9"}' ;;
    esac
    ;;
esac
`

// fakePluginEnv writes the fake eka-mcp plugin executable into a
// temporary bin directory and pins EKA_PLUGIN_DIR to it, prepending
// the bin directory to PATH so discovery finds exactly the fake plugin
// before anything ambient (the shell tools the script uses — cat,
// printf, mkdir — stay reachable on the original PATH).
func fakePluginEnv(t *testing.T) string {
	t.Helper()
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "eka-mcp"), []byte(fakePluginScript), 0o755); err != nil {
		t.Fatalf("write fake plugin: %v", err)
	}
	t.Setenv("EKA_PLUGIN_DIR", bin)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return bin
}

// emptyPluginEnv pins the plugin search to a directory with no plugin
// executables and strips any eka-* executable from the ambient PATH, so
// the refusal tests are hermetic even on a machine with eka plugins.
func emptyPluginEnv(t *testing.T) {
	t.Helper()
	t.Setenv("EKA_PLUGIN_DIR", t.TempDir())
	var kept []string
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if dir == "" {
			continue
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		hasPlugin := false
		for _, e := range entries {
			if !e.IsDir() && strings.HasPrefix(e.Name(), "eka-") {
				hasPlugin = true
				break
			}
		}
		if !hasPlugin {
			kept = append(kept, dir)
		}
	}
	t.Setenv("PATH", strings.Join(kept, string(os.PathListSeparator)))
}

func installDir(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "install")
}

func containsStr(s []string, want string) bool {
	for _, v := range s {
		if v == want {
			return true
		}
	}
	return false
}

// TestInstallSkillsDelegates: the CLI discovers the fake plugin, reads
// its manifest, and delegates the copy to it. The pack version in the
// report and in the state marker comes from the manifest, not from any
// embedded pack.
func TestInstallSkillsDelegates(t *testing.T) {
	fakePluginEnv(t)
	dir := installDir(t)
	code, text, errText := runIn([]string{"install", "skills", "--dir", dir})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", code, text, errText)
	}
	for _, want := range []string{
		"eka-ai-skill-pack 9.9.9", "↓ Install skills",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("output missing %q:\n%s", want, text)
		}
	}
	if !strings.Contains(text, "2 → "+dir+" (installed)") {
		t.Errorf("summary must report the install:\n%s", text)
	}
	for _, name := range []string{"eka-orientation", "eka-knowledge-authoring"} {
		if _, err := os.Stat(filepath.Join(dir, name, "SKILL.md")); err != nil {
			t.Errorf("skill %s not installed: %v", name, err)
		}
	}
	// State marker carries the manifest version for update detection.
	b, err := os.ReadFile(filepath.Join(dir, installStateFile))
	if err != nil {
		t.Fatalf("state marker missing: %v", err)
	}
	if !strings.Contains(string(b), `"version": "9.9.9"`) {
		t.Errorf("state marker version mismatch:\n%s", b)
	}
}

// TestInstallCommandsToDir: installs the command artifact family
// through the plugin.
func TestInstallCommandsToDir(t *testing.T) {
	fakePluginEnv(t)
	dir := installDir(t)
	code, text, errText := runIn([]string{"install", "commands", "--dir", dir})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", code, text, errText)
	}
	for _, name := range []string{"eka-discuss.md", "eka-execute.md"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("command %s not installed: %v", name, err)
		}
	}
	if !strings.Contains(text, "2 → "+dir+" (installed)") {
		t.Errorf("summary must report the install:\n%s", text)
	}
}

// TestInstallTargetDetection: with a HOME carrying an opencode config
// directory, auto-detection resolves the target and the plugin
// installs into it.
func TestInstallTargetDetection(t *testing.T) {
	fakePluginEnv(t)
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".config", "opencode"), 0o755); err != nil {
		t.Fatalf("mkdir config: %v", err)
	}
	t.Setenv("HOME", home)
	code, _, errText := runIn([]string{"install", "skills"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstderr: %s", code, errText)
	}
	dir := filepath.Join(home, ".config", "opencode", "skills")
	for _, name := range []string{"eka-orientation", "eka-knowledge-authoring"} {
		if _, err := os.Stat(filepath.Join(dir, name, "SKILL.md")); err != nil {
			t.Errorf("skill %s not installed: %v", name, err)
		}
	}
}

// TestInstallSkillsDryRun: the plan is deterministic (two runs are
// byte-identical), lists the plugin's planned entries, and writes
// nothing — neither the target directory nor the state marker.
func TestInstallSkillsDryRun(t *testing.T) {
	fakePluginEnv(t)
	dir := installDir(t)
	code, text, errText := runIn([]string{"install", "skills", "--dir", dir, "--dry-run"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", code, text, errText)
	}
	for _, want := range []string{
		"eka-ai-skill-pack 9.9.9", "↓ Install skills",
		"install to", "eka-orientation", "eka-knowledge-authoring",
		"2 artifact(s) planned for 1 target(s)",
		"Dry-run: no changes were written.",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("dry-run output missing %q:\n%s", want, text)
		}
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("dry-run must not create the target directory")
	}
	_, text2, _ := runIn([]string{"install", "skills", "--dir", dir, "--dry-run"})
	if text != text2 {
		t.Error("dry-run output differs between runs")
	}
}

// TestInstallIdempotent: re-installing the same plugin version reports
// "unchanged (refresh)" and leaves the installed files byte-identical.
func TestInstallIdempotent(t *testing.T) {
	fakePluginEnv(t)
	dir := installDir(t)
	if _, text, _ := runIn([]string{"install", "skills", "--dir", dir}); !strings.Contains(text, "(installed)") {
		t.Fatalf("first install must report installed:\n%s", text)
	}
	sample, _ := os.ReadFile(filepath.Join(dir, "eka-orientation", "SKILL.md"))
	code, text, errText := runIn([]string{"install", "skills", "--dir", dir})
	if code != 0 {
		t.Fatalf("re-install exit = %d\nstderr: %s", code, errText)
	}
	if !strings.Contains(text, "(unchanged (refresh))") {
		t.Errorf("re-install must report unchanged (refresh):\n%s", text)
	}
	after, _ := os.ReadFile(filepath.Join(dir, "eka-orientation", "SKILL.md"))
	if string(sample) != string(after) {
		t.Error("re-install must not change installed files")
	}
}

// TestInstallNoPluginRefusal: with the plugin search pinned to a
// directory without plugins, install refuses deterministically with
// exit 1 and the eka-mcp hint.
func TestInstallNoPluginRefusal(t *testing.T) {
	emptyPluginEnv(t)
	code, _, errText := runIn([]string{"install", "skills", "--dir", installDir(t)})
	if code != 1 {
		t.Errorf("exit = %d, want 1 (refusal)\nstderr: %s", code, errText)
	}
	if !strings.Contains(errText, "no plugin provides skills; install the eka-mcp plugin") {
		t.Errorf("refusal message missing:\n%s", errText)
	}
}

// TestInstallNoTargetRefusal: with no agent configuration directory
// and no --dir/--target, install refuses deterministically with exit 1
// (environment-state refusal class, mirroring the workspace refusals).
func TestInstallNoTargetRefusal(t *testing.T) {
	emptyPluginEnv(t)
	t.Setenv("HOME", t.TempDir())
	code, _, errText := runIn([]string{"install", "skills"})
	if code != 1 {
		t.Errorf("exit = %d, want 1 (refusal)\nstderr: %s", code, errText)
	}
	if !strings.Contains(errText, "no agent configuration directory detected") {
		t.Errorf("refusal message missing:\n%s", errText)
	}
}

// TestInstallUsageErrors: unknown target and the --target/--dir
// conflict are usage errors (exit 2).
func TestInstallUsageErrors(t *testing.T) {
	fakePluginEnv(t)
	code, _, _ := runIn([]string{"install", "skills", "--target", "bogus"})
	if code != 2 {
		t.Errorf("unknown target: exit = %d, want 2", code)
	}
	code, _, errText := runIn([]string{"install", "skills", "--target", "opencode", "--dir", t.TempDir()})
	if code != 2 || !strings.Contains(errText, "mutually exclusive") {
		t.Errorf("--target + --dir: exit = %d, want 2 with conflict message\nstderr: %s", code, errText)
	}
	code, _, _ = runIn([]string{"install", "bogus"})
	if code != 2 {
		t.Errorf("unknown subcommand: exit = %d, want 2", code)
	}
}

// TestInstallHelpExitsZero: the install family help exits 0.
func TestInstallHelpExitsZero(t *testing.T) {
	for _, args := range [][]string{{"install", "-h"}, {"install", "skills", "-h"}, {"install", "commands", "-h"}} {
		if code, _, _ := runIn(args); code != 0 {
			t.Errorf("args %v: exit = %d, want 0", args, code)
		}
	}
}

// TestResolveInstallDirs exercises the dir resolution directly: --dir,
// explicit targets, the --target/--dir conflict, and auto-detection.
func TestResolveInstallDirs(t *testing.T) {
	home := t.TempDir()
	f := &installFlags{dir: "/some/dir"}
	dirs, err := resolveInstallDirs(home, kindSkills, f)
	if err != nil || len(dirs) != 1 || dirs[0] != "/some/dir" {
		t.Errorf("--dir: dirs = %v, err = %v", dirs, err)
	}

	f = &installFlags{target: "opencode"}
	dirs, err = resolveInstallDirs(home, kindSkills, f)
	if err != nil || len(dirs) != 1 || dirs[0] != filepath.Join(home, ".config", "opencode", "skills") {
		t.Errorf("--target opencode: dirs = %v, err = %v", dirs, err)
	}

	f = &installFlags{target: "agents"}
	_, err = resolveInstallDirs(home, kindCommands, f)
	if err == nil || !strings.Contains(err.Error(), `target "agents" does not support commands`) {
		t.Errorf("agents commands must be refused, got err = %v", err)
	}

	f = &installFlags{target: "opencode", dir: "/x"}
	_, err = resolveInstallDirs(home, kindSkills, f)
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("--target + --dir must conflict, got err = %v", err)
	}

	// Auto-detection: only existing config directories are targets.
	configDir := filepath.Join(home, ".config", "opencode")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	f = &installFlags{}
	dirs, err = resolveInstallDirs(home, kindSkills, f)
	if err != nil || len(dirs) != 1 || dirs[0] != filepath.Join(configDir, "skills") {
		t.Errorf("auto-detection: dirs = %v, err = %v", dirs, err)
	}

	f = &installFlags{}
	_, err = resolveInstallDirs(t.TempDir(), kindSkills, f)
	if err == nil || !errors.Is(err, errNoTarget) {
		t.Errorf("no target detected must be refused, got err = %v", err)
	}
}
