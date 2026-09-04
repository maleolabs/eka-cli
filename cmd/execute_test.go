package cmd

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"unsafe"

	"github.com/maleolabs/eka-core/exchange"
	"github.com/maleolabs/eka-core/metadata"
)

// Execute() unit tests: argument handling, output contract and exit
// codes. Ported from the pre-Cobra cmd/eka/main_test.go (which tested
// run()) and extended with help/completion coverage.
//
// Exit code contract (documented in root.go):
//
//	0  fully compliant (warnings allowed)
//	1  blocking violations present
//	2  usage or internal error (unknown command, invalid path,
//	   unreadable scan root)

// runIn calls Execute with a non-interactive stdin (deterministic
// defaults for init) and returns the collected stdout/stderr.
func runIn(args []string) (int, string, string) {
	var out, errb bytes.Buffer
	code := Execute(args, strings.NewReader(""), &out, &errb)
	return code, out.String(), errb.String()
}

func TestLanding(t *testing.T) {
	code, out, errText := runIn(nil)
	if code != 0 {
		t.Errorf("no args: exit = %d, want 0 (landing)", code)
	}
	if errText != "" {
		t.Errorf("no args: stderr must be empty, got %q", errText)
	}
	for _, want := range []string{
		"Engineering Knowledge Architecture (EKA)",
		"Commands",
		"Help",
		"Version",
		"dev (EKA standard " + exchange.SpecificationVersion + ")",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("landing missing %q, got:\n%s", want, out)
		}
	}
	// The landing is deterministic: two runs produce identical bytes.
	_, out2, _ := runIn(nil)
	if out != out2 {
		t.Errorf("landing is not deterministic")
	}
}

func TestVersionCommand(t *testing.T) {
	code, out, _ := runIn([]string{"version"})
	if code != 0 {
		t.Errorf("version: exit = %d, want 0", code)
	}
	if !strings.Contains(out, "eka dev") || !strings.Contains(out, "EKA standard "+exchange.SpecificationVersion) {
		t.Errorf("version output incomplete: %q", out)
	}
	code, out, _ = runIn([]string{"version", "--help"})
	if code != 0 {
		t.Errorf("version --help: exit = %d, want 0", code)
	}
	if !strings.Contains(out, "Print the EKA version axes this CLI reports") {
		t.Errorf("version --help missing description: %q", out)
	}
}

func TestExitCodeUsage(t *testing.T) {
	code, _, errText := runIn([]string{"doctor"})
	if code != 2 {
		t.Errorf("unknown command: exit = %d, want 2", code)
	}
	if !strings.Contains(errText, "validate") {
		t.Errorf("unknown command message must mention the validate command, got %q", errText)
	}
	if !strings.Contains(errText, "init") {
		t.Errorf("unknown command message must mention the init command, got %q", errText)
	}
	// Cobra sorts command lists alphabetically (EnableCommandSorting);
	// the message must list exactly the registered commands.
	if !strings.Contains(errText, "available commands: assign, code-context, code-discover, code-get, context, discard, draft, edit, export, feedback, get, import, init, integrity, mcp, new, note, plugin, project, publish, reassign, relate, snapshot, status, sync, transition, unassign, update, validate, version, view, watch") {
		t.Errorf("unknown command message must list the available commands, got %q", errText)
	}
	code, _, _ = runIn([]string{"validate", "a", "b"})
	if code != 2 {
		t.Errorf("too many args: exit = %d, want 2", code)
	}
	// Unknown root-level flag is a usage error, not an unknown command.
	code, _, errText = runIn([]string{"--bogus"})
	if code != 2 {
		t.Errorf("unknown root flag: exit = %d, want 2", code)
	}
	if !strings.Contains(errText, "--bogus") {
		t.Errorf("unknown root flag: stderr must name the flag, got %q", errText)
	}
}

func TestExitCodeBadPath(t *testing.T) {
	code, _, _ := runIn([]string{"validate", filepath.Join(t.TempDir(), "nope")})
	if code != 2 {
		t.Errorf("invalid path: exit = %d, want 2", code)
	}
}

// TestHelpExitsZero covers the root help entry points (-h, --help, help)
// and the validate help flags. All must exit 0 and print to stdout.
// Subcommand help is `eka help <command>` (Cobra); `eka validate help`
// would be treated as a path argument, matching pre-Cobra behavior.
func TestHelpExitsZero(t *testing.T) {
	for _, args := range [][]string{{"-h"}, {"--help"}, {"help"}} {
		var out, errb bytes.Buffer
		if code := Execute(args, strings.NewReader(""), &out, &errb); code != 0 {
			t.Errorf("args %v: exit = %d, want 0", args, code)
		}
		text := out.String()
		if !strings.Contains(text, "Usage:") {
			t.Errorf("args %v: root help must contain Usage:", args)
		}
		for _, cmdName := range []string{"validate", "init", "export", "import", "view", "watch"} {
			if !strings.Contains(text, cmdName) {
				t.Errorf("args %v: root help must mention the %s command", args, cmdName)
			}
		}
	}
	for _, args := range [][]string{{"validate", "-h"}, {"validate", "--help"}} {
		var out, errb bytes.Buffer
		if code := Execute(args, strings.NewReader(""), &out, &errb); code != 0 {
			t.Errorf("args %v: exit = %d, want 0", args, code)
		}
		if !strings.Contains(out.String(), "eka validate") {
			t.Errorf("args %v: validate help text missing usage", args)
		}
	}
}

func TestValidateValidRepoExitsZero(t *testing.T) {
	code, text, errText := runIn([]string{"validate", filepath.Join("..", "testdata", "conformance", "valid")})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstderr: %s", code, errText)
	}
	if !strings.Contains(text, "PASS") {
		t.Errorf("output must contain PASS:\n%s", text)
	}
	if !strings.Contains(text, "Artifacts: 6") {
		t.Errorf("output must contain the artifact count:\n%s", text)
	}
	if !strings.Contains(text, "Verdict: PASS") {
		t.Errorf("output must contain the verdict line:\n%s", text)
	}
	if !strings.Contains(text, "↓ Validate") {
		t.Errorf("output must contain the validate pipeline header:\n%s", text)
	}
	if !strings.Contains(text, "Knowledge   EKA v"+exchange.SpecificationVersion) {
		t.Errorf("output must identify the knowledge standard:\n%s", text)
	}
}

func TestValidateInvalidRepoExitsOne(t *testing.T) {
	code, text, _ := runIn([]string{"validate", filepath.Join("..", "testdata", "conformance", "invalid-dimension")})
	if code != 1 {
		t.Fatalf("exit = %d, want 1\noutput: %s", code, text)
	}
	if !strings.Contains(text, "FAIL") {
		t.Errorf("output must contain FAIL:\n%s", text)
	}
	if !strings.Contains(text, "Verdict: FAIL") {
		t.Errorf("output must contain the verdict line:\n%s", text)
	}
	if !strings.Contains(text, "R6") {
		t.Errorf("output must list the R6 violations:\n%s", text)
	}
}

// TestWarningsDoNotAffectExitCode builds a repo whose only finding is a
// warning (a draft artifact with an unresolved reference) and verifies
// exit code 0.
func TestWarningsDoNotAffectExitCode(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "docs", "specifications")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := `---
namespace: eka-cli
type: spec
id: 001-draft
instance-version: 1
revision: 1
content-state: draft
existence-state: active
dimension: specifications
created: 2026-08-05
updated: 2026-08-05
supersedes: []
derives-from: []
depends-on:
  - sto:ghost
change-log:
  - date: 2026-08-05
    domain: existence-state
    from: "-"
    to: active
    by: Engineering
  - date: 2026-08-05
    domain: content-state
    from: "-"
    to: draft
    by: Engineering
---
# Spec

## Purpose

p

## Content

c
`
	if err := os.WriteFile(filepath.Join(dir, "spec-001-draft.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	code, text, _ := runIn([]string{"validate", root})
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (warnings must not block)\noutput:\n%s", code, text)
	}
	if !strings.Contains(text, "Warnings: 1") {
		t.Errorf("output must count the warning:\n%s", text)
	}
}

// TestDefaultPathIsCurrentDirectory verifies `eka validate` uses ".".
func TestDefaultPathIsCurrentDirectory(t *testing.T) {
	dir := t.TempDir()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(old)
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	// An empty directory has no docs/ knowledge tree: validation is
	// SKIPPED with a clean PASS (exit 0) and the deterministic skip note
	// (ADR-018 Decision 2).
	code, text, _ := runIn([]string{"validate"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstderr: %s", code, text)
	}
	if !strings.Contains(text, "Root: . — 0 authoring files") {
		t.Errorf("default root must be '.':\n%s", text)
	}
	if !strings.Contains(text, "no docs/ knowledge tree — nothing to validate (docs-in-repo is legacy authoring — EKA v2 keeps knowledge in the workspace)") {
		t.Errorf("a root without docs/ must print the deterministic skip note:\n%s", text)
	}
}

// TestOutputIsDeterministic verifies stable ordering in printed results.
func TestOutputIsDeterministic(t *testing.T) {
	path := filepath.Join("..", "testdata", "conformance", "invalid-projection")
	runOnce := func() string {
		_, text, _ := runIn([]string{"validate", path})
		return text
	}
	if a, b := runOnce(), runOnce(); a != b {
		t.Error("CLI output differs between runs")
	}
}

// TestCompletionCommand verifies the standard Cobra completion command:
// every shell subcommand generates a script on stdout and exits 0.
func TestCompletionCommand(t *testing.T) {
	for _, shell := range []string{"bash", "zsh", "fish", "powershell"} {
		var out, errb bytes.Buffer
		code := Execute([]string{"completion", shell}, strings.NewReader(""), &out, &errb)
		if code != 0 {
			t.Errorf("completion %s: exit = %d, want 0\nstderr: %s", shell, code, errb.String())
		}
		if out.Len() == 0 {
			t.Errorf("completion %s: output must not be empty", shell)
		}
	}
}

// --- eka init CLI-level tests -------------------------------------------

// chdirInto changes the working directory for the duration of the test.
func chdirInto(t *testing.T, dir string) {
	t.Helper()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(old) })
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
}

// copyFixture copies a fixture directory tree (files only) into a
// fresh temp dir and returns the copy path.
func copyFixture(t *testing.T, src string) string {
	t.Helper()
	dst := t.TempDir()
	err := filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	return dst
}

func TestInitHelpExitsZero(t *testing.T) {
	for _, args := range [][]string{{"init", "-h"}, {"init", "--help"}} {
		code, text, _ := runIn(args)
		if code != 0 {
			t.Errorf("args %v: exit = %d, want 0", args, code)
		}
		if !strings.Contains(text, "eka init") {
			t.Errorf("args %v: init help text missing usage", args)
		}
		// The identity options are documented.
		if !strings.Contains(text, "--project") || !strings.Contains(text, "--namespace") {
			t.Errorf("args %v: init help must document the identity flags", args)
		}
	}
}

func TestInitUnknownFlagExitsTwo(t *testing.T) {
	for _, flag := range []string{"-n", "--force", "-x"} {
		code, _, errText := runIn([]string{"init", flag})
		if code != 2 {
			t.Errorf("flag %q: exit = %d, want 2", flag, code)
		}
		if !strings.Contains(errText, flag) {
			t.Errorf("flag %q: stderr must name the unknown flag, got %q", flag, errText)
		}
	}
}

func TestInitTooManyArgumentsExitsTwo(t *testing.T) {
	code, _, _ := runIn([]string{"init", "a", "b"})
	if code != 2 {
		t.Errorf("exit = %d, want 2", code)
	}
}

func TestInitRejectsPathSeparators(t *testing.T) {
	code, _, errText := runIn([]string{"init", "a/b"})
	if code != 2 {
		t.Errorf("exit = %d, want 2", code)
	}
	if !strings.Contains(errText, "path separators") {
		t.Errorf("stderr must explain the separator rejection, got %q", errText)
	}
}

func TestInitRejectsFileTarget(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "occupied")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(old)
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	code, _, errText := runIn([]string{"init", "occupied"})
	if code != 2 {
		t.Errorf("exit = %d, want 2", code)
	}
	if !strings.Contains(errText, "not a directory") {
		t.Errorf("stderr must explain the file target rejection, got %q", errText)
	}
}

// TestInitDryRunWritesNothing verifies `eka init --dry-run` prints the plan
// and creates nothing.
func TestInitDryRunWritesNothing(t *testing.T) {
	dir := t.TempDir()
	chdirInto(t, dir)
	code, text, _ := runIn([]string{"init", "--dry-run"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if !strings.Contains(text, "Bootstrap plan (dry-run)") {
		t.Errorf("output must show the plan header:\n%s", text)
	}
	if !strings.Contains(text, "↓ Bootstrap") {
		t.Errorf("output must show the bootstrap pipeline header:\n%s", text)
	}
	// Identity-only plan: the eka.yaml generation is the first planned
	// write (the current directory exists, so no create-dir step).
	if !strings.Contains(text, "generate file: eka.yaml (repository identity)") {
		t.Errorf("output must contain the identity plan line:\n%s", text)
	}
	// The standard declaration is planned right after the identity file
	// (order: dir → eka.yaml → EKA → git → validate).
	if !strings.Contains(text, "generate file: EKA (standard declaration)") {
		t.Errorf("output must contain the standard declaration plan line:\n%s", text)
	}
	if !strings.Contains(text, "Dry-run: no changes were written.") {
		t.Errorf("output must state that nothing was written:\n%s", text)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("dry-run must not create files, found: %v", entries)
	}
}

// TestInitNamedDryRun verifies the flag works after the project name and
// that the named target is not created.
func TestInitNamedDryRun(t *testing.T) {
	dir := t.TempDir()
	chdirInto(t, dir)
	code, text, _ := runIn([]string{"init", "myproj", "--dry-run"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if !strings.Contains(text, "create dir: myproj") {
		t.Errorf("plan must target myproj:\n%s", text)
	}
	if _, err := os.Stat(filepath.Join(dir, "myproj")); !os.IsNotExist(err) {
		t.Error("dry-run must not create the project directory")
	}
}

// TestInitCurrentDirectory verifies `eka init` bootstraps "." and the
// result validates.
func TestInitCurrentDirectory(t *testing.T) {
	dir := t.TempDir()
	chdirInto(t, dir)
	code, text, errText := runIn([]string{"init"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstderr: %s\nstdout: %s", code, errText, text)
	}
	if !strings.Contains(text, "Validation: PASS") {
		t.Errorf("output must report PASS:\n%s", text)
	}
	// Identity-only: eka.yaml + the EKA standard declaration are
	// generated, nothing else.
	if _, err := os.Stat(filepath.Join(dir, "eka.yaml")); err != nil {
		t.Errorf("expected the generated identity file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "EKA")); err != nil {
		t.Errorf("expected the generated EKA standard declaration: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Errorf("generated tree must be eka.yaml + EKA only, found: %v", entries)
	}
	names := map[string]bool{}
	for _, e := range entries {
		names[e.Name()] = true
	}
	if !names["eka.yaml"] || !names["EKA"] {
		t.Errorf("generated tree must contain eka.yaml and EKA, found: %v", entries)
	}
	// The generated repository must pass `eka validate`.
	code, text, _ = runIn([]string{"validate", dir})
	if code != 0 {
		t.Errorf("generated repository must validate, exit = %d:\n%s", code, text)
	}
}

// TestInitNamedDirectory verifies `eka init <name>` creates the directory
// relative to the CWD.
func TestInitNamedDirectory(t *testing.T) {
	dir := t.TempDir()
	chdirInto(t, dir)
	code, text, _ := runIn([]string{"init", "myproj"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0:\n%s", code, text)
	}
	if _, err := os.Stat(filepath.Join(dir, "myproj", "eka.yaml")); err != nil {
		t.Errorf("expected myproj/eka.yaml to exist: %v", err)
	}
	if !strings.Contains(text, "Repository Type: new") {
		t.Errorf("output must classify the repo as new:\n%s", text)
	}
}

// TestInitWritesEkaYAML verifies `eka init` generates the repository
// identity file (ADR-017): the output lists eka.yaml with the identity
// line and the written file parses with metadata.Parse, carrying
// project == namespace and name == the target directory basename.
func TestInitWritesEkaYAML(t *testing.T) {
	dir := t.TempDir()
	chdirInto(t, dir)
	code, text, errText := runIn([]string{"init"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", code, text, errText)
	}
	if !strings.Contains(text, "eka.yaml") {
		t.Errorf("output must list eka.yaml:\n%s", text)
	}
	if !strings.Contains(text, "Identity:") {
		t.Errorf("output must render the identity line:\n%s", text)
	}
	data, err := os.ReadFile(filepath.Join(dir, "eka.yaml"))
	if err != nil {
		t.Fatalf("generated eka.yaml missing: %v", err)
	}
	m, err := metadata.Parse(data)
	if err != nil {
		t.Fatalf("generated eka.yaml must parse: %v\n%s", err, data)
	}
	if m.Project != m.Namespace {
		t.Errorf("project %q must equal namespace %q", m.Project, m.Namespace)
	}
	if m.Name != filepath.Base(dir) {
		t.Errorf("name = %q, want %q (target basename)", m.Name, filepath.Base(dir))
	}
	// The dry-run reports the same identity for the same inputs on a
	// fresh target (an already-initialized repo plans only reuse +
	// validate, which carries no identity line).
	fresh := t.TempDir()
	chdirInto(t, fresh)
	code, text, _ = runIn([]string{"init", "--dry-run"})
	if code != 0 {
		t.Fatalf("dry-run exit = %d, want 0", code)
	}
	if !strings.Contains(text, "generate file: eka.yaml (repository identity)") ||
		!strings.Contains(text, "Identity:") {
		t.Errorf("dry-run output must list eka.yaml and the identity line:\n%s", text)
	}
	if !strings.Contains(text, "project "+filepath.Base(fresh)+", name "+filepath.Base(fresh)) {
		t.Errorf("dry-run identity must match the real run's triple:\n%s", text)
	}
}

// TestInitWritesStandardDeclaration verifies `eka init` writes the root
// EKA standard declaration file (sto:init-standard-declaration): the
// compact consumer summary starting with "EKA STANDARD", carrying the
// correct `Version X.Y` line equal to the CLI's reported standard
// version, byte-identical across runs (deterministic).
func TestInitWritesStandardDeclaration(t *testing.T) {
	dir := t.TempDir()
	chdirInto(t, dir)
	code, text, errText := runIn([]string{"init"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", code, text, errText)
	}
	data, err := os.ReadFile(filepath.Join(dir, "EKA"))
	if err != nil {
		t.Fatalf("generated EKA declaration missing: %v", err)
	}
	if !strings.HasPrefix(string(data), "EKA STANDARD\nVersion "+standardVersion+"\n") {
		t.Errorf("EKA declaration must start with the EKA STANDARD header and the Version %s line:\n%s",
			standardVersion, data[:80])
	}
	// The dry-run reports the same file deterministically and writes
	// nothing.
	fresh := t.TempDir()
	chdirInto(t, fresh)
	_, dryRunOut, _ := runIn([]string{"init", "--dry-run"})
	if !strings.Contains(dryRunOut, "generate file: EKA (standard declaration)") {
		t.Errorf("dry-run output must plan the EKA generation:\n%s", dryRunOut)
	}
	if _, err := os.Stat(filepath.Join(fresh, "EKA")); !os.IsNotExist(err) {
		t.Error("dry-run must not write the EKA declaration")
	}
	// Deterministic: two real runs produce identical EKA bytes.
	_, _, _ = runIn([]string{"init", "--dry-run"})
	_, _, _ = runIn([]string{"init", "--dry-run"})
	if data2, _ := os.ReadFile(filepath.Join(dir, "EKA")); !bytes.Equal(data, data2) {
		t.Error("EKA declaration must be deterministic across runs")
	}
}

// TestInitBackfillsMissingDeclaration verifies the re-run contract on an
// already-initialized repository (sto:init-standard-declaration): a
// missing EKA declaration is backfilled; an identical one is reused (the
// tree stays byte-identical); a differing one is never replaced
// silently in non-interactive mode.
func TestInitBackfillsMissingDeclaration(t *testing.T) {
	dir := t.TempDir()
	chdirInto(t, dir)
	if code, _, errText := runIn([]string{"init"}); code != 0 {
		t.Fatalf("first init: exit = %d, stderr: %s", code, errText)
	}
	// Remove the declaration, then re-run: it must be backfilled.
	if err := os.Remove(filepath.Join(dir, "EKA")); err != nil {
		t.Fatal(err)
	}
	code, text, _ := runIn([]string{"init"})
	if code != 0 {
		t.Fatalf("backfill init: exit = %d, want 0", code)
	}
	if !strings.Contains(text, "already initialized") {
		t.Errorf("backfill run must report already initialized:\n%s", text)
	}
	if _, err := os.Stat(filepath.Join(dir, "EKA")); err != nil {
		t.Errorf("missing declaration must be backfilled: %v", err)
	}
	// A differing declaration is preserved (never replaced silently).
	if err := os.WriteFile(filepath.Join(dir, "EKA"), []byte("EKA STANDARD\nVersion 9.9\ncustom\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, _ = runIn([]string{"init"})
	if code != 0 {
		t.Fatalf("differing-declaration init: exit = %d, want 0", code)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "EKA")); string(data) != "EKA STANDARD\nVersion 9.9\ncustom\n" {
		t.Error("differing declaration must be preserved in non-interactive mode")
	}
}

// TestInitNonConformantExitsOne pre-places a malformed artifact and
// verifies the init exits 1 with a clear message.
func TestInitNonConformantExitsOne(t *testing.T) {
	dir := t.TempDir()
	workItems := filepath.Join(dir, "docs", "operating", "work-items")
	if err := os.MkdirAll(workItems, 0o755); err != nil {
		t.Fatal(err)
	}
	bad := "---\nnamespace: eka-cli\n"
	bad += "type: sto\n" // type without id violates the artifact rule (R0)
	bad += "---\n# Bad\n"
	if err := os.WriteFile(filepath.Join(workItems, "sto-bad.md"), []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	chdirInto(t, dir)
	code, text, errText := runIn([]string{"init"})
	if code != 1 {
		t.Fatalf("exit = %d, want 1\nstdout: %s\nstderr: %s", code, text, errText)
	}
	if !strings.Contains(errText, "non-conformant") {
		t.Errorf("stderr must explain the non-conformant result, got %q", errText)
	}
	if !strings.Contains(text, "Validation: FAIL") {
		t.Errorf("output must report FAIL:\n%s", text)
	}
	if !strings.Contains(text, "Validation findings:") {
		t.Errorf("output must show the findings under the failed validation stage:\n%s", text)
	}
	if !strings.Contains(text, "R0") {
		t.Errorf("findings must list the R0 violation:\n%s", text)
	}
}

// TestInitTwiceIsIdempotent verifies the second run is a no-op and the
// repository still validates.
func TestInitTwiceIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	chdirInto(t, dir)
	code, _, errText := runIn([]string{"init"})
	if code != 0 {
		t.Fatalf("first init: exit = %d, want 0\nstderr: %s", code, errText)
	}
	before, err := snapshot(dir)
	if err != nil {
		t.Fatal(err)
	}
	code, text, _ := runIn([]string{"init"})
	if code != 0 {
		t.Fatalf("second init: exit = %d, want 0:\n%s", code, text)
	}
	if !strings.Contains(text, "already initialized") {
		t.Errorf("second run must report already initialized:\n%s", text)
	}
	after, err := snapshot(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Errorf("second init changed the tree: %d files before, %d after", len(before), len(after))
	}
	for path, data := range before {
		if got, ok := after[path]; !ok || !bytes.Equal(got, data) {
			t.Errorf("second init modified %s", path)
		}
	}
	code, _, _ = runIn([]string{"validate", dir})
	if code != 0 {
		t.Errorf("repository must still validate after second init")
	}
}

// snapshot walks dir and returns relative path -> content.
func snapshot(dir string) (map[string][]byte, error) {
	out := map[string][]byte{}
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out[rel] = data
		return nil
	})
	return out, err
}

// TestInitIdentityFlags verifies the explicit identity options: `eka
// init --project atrium --namespace atrium-api` writes eka.yaml with
// project atrium / namespace atrium-api / name == the target basename,
// and the output renders the fixed identity.
func TestInitIdentityFlags(t *testing.T) {
	dir := t.TempDir()
	chdirInto(t, dir)
	code, text, errText := runIn([]string{"init", "--project", "atrium", "--namespace", "atrium-api"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", code, text, errText)
	}
	data, err := os.ReadFile(filepath.Join(dir, "eka.yaml"))
	if err != nil {
		t.Fatalf("generated eka.yaml missing: %v", err)
	}
	m, err := metadata.Parse(data)
	if err != nil {
		t.Fatalf("generated eka.yaml must parse: %v\n%s", err, data)
	}
	if m.Project != "atrium" {
		t.Errorf("project = %q, want atrium", m.Project)
	}
	if m.Namespace != "atrium-api" {
		t.Errorf("namespace = %q, want atrium-api", m.Namespace)
	}
	if m.Name != filepath.Base(dir) {
		t.Errorf("name = %q, want the target basename %q", m.Name, filepath.Base(dir))
	}
	// The configure stage and the identity line render the fixed values.
	if !strings.Contains(text, "project: atrium, namespace: atrium-api") {
		t.Errorf("configure stage must render the fixed identity:\n%s", text)
	}
	if !strings.Contains(text, "project atrium, name "+filepath.Base(dir)+", namespace atrium-api") {
		t.Errorf("identity line must render the fixed triple:\n%s", text)
	}

	// A dry-run previews the same fixed identity on a fresh target.
	fresh := t.TempDir()
	chdirInto(t, fresh)
	code, text, _ = runIn([]string{"init", "--project", "atrium", "--namespace", "atrium-api", "--dry-run"})
	if code != 0 {
		t.Fatalf("dry-run exit = %d, want 0", code)
	}
	if !strings.Contains(text, "project atrium, name "+filepath.Base(fresh)+", namespace atrium-api") {
		t.Errorf("dry-run identity must match the real run's triple:\n%s", text)
	}
}

// TestInitInvalidIdentityFlag: an invalid --project/--namespace value is
// a usage error (exit 2) that names the bad value and writes nothing.
func TestInitInvalidIdentityFlag(t *testing.T) {
	for _, args := range [][]string{
		{"init", "--namespace", "invalid_ns"},
		{"init", "--project", "Invalid_Id"},
		{"init", "--namespace", "with/slash"},
	} {
		dir := t.TempDir()
		chdirInto(t, dir)
		code, _, errText := runIn(args)
		if code != 2 {
			t.Errorf("args %v: exit = %d, want 2", args, code)
		}
		if !strings.Contains(errText, "invalid") {
			t.Errorf("args %v: stderr must explain the invalid value, got %q", args, errText)
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 0 {
			t.Errorf("args %v: invalid identity must write nothing, found %v", args, entries)
		}
	}
}

// TestInitInteractivePrompts drives the wizard through a real
// pseudo-terminal: stdin is a terminal, so the project id and namespace
// prompts are printed and answered interactively, and the typed identity
// lands in eka.yaml (git init declined).
func TestInitInteractivePrompts(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("PTY test is Linux-only (/dev/ptmx)")
	}
	dir := t.TempDir()
	chdirInto(t, dir)

	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		t.Skipf("cannot open /dev/ptmx: %v", err)
	}
	defer master.Close()

	// Resolve and unlock the slave: TIOCGPTN returns the pty number,
	// TIOCSPTLCK(0) unlocks it for opening (the argument is a pointer
	// to the lock flag, like glibc's unlockpt).
	var n uint32
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, master.Fd(), syscall.TIOCGPTN, uintptr(unsafe.Pointer(&n))); errno != 0 {
		t.Fatalf("TIOCGPTN failed: %v", errno)
	}
	lock := 0
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, master.Fd(), syscall.TIOCSPTLCK, uintptr(unsafe.Pointer(&lock))); errno != 0 {
		t.Fatalf("TIOCSPTLCK failed: %v", errno)
	}
	slave, err := os.OpenFile("/dev/pts/"+strconv.Itoa(int(n)), os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("cannot open the pty slave: %v", err)
	}
	defer slave.Close()

	// Type the answers: project id, namespace, decline git init.
	if _, err := master.WriteString("atrium\natrium-api\nn\n"); err != nil {
		t.Fatal(err)
	}

	var out, errb bytes.Buffer
	code := Execute([]string{"init"}, slave, &out, &errb)
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", code, out.String(), errb.String())
	}
	// The interactive prompts were printed.
	if !strings.Contains(out.String(), "Project id") || !strings.Contains(out.String(), "Namespace") {
		t.Errorf("interactive prompts missing from the output:\n%s", out.String())
	}
	// The typed identity landed in eka.yaml.
	data, err := os.ReadFile(filepath.Join(dir, "eka.yaml"))
	if err != nil {
		t.Fatalf("generated eka.yaml missing: %v", err)
	}
	m, err := metadata.Parse(data)
	if err != nil {
		t.Fatalf("generated eka.yaml must parse: %v\n%s", err, data)
	}
	if m.Project != "atrium" || m.Namespace != "atrium-api" {
		t.Errorf("typed identity = %q/%q, want atrium/atrium-api", m.Project, m.Namespace)
	}
	// The git question was offered (git available) and declined.
	if _, err := exec.LookPath("git"); err == nil {
		if !strings.Contains(out.String(), "skipped (declined)") {
			t.Errorf("declined git init must be reported:\n%s", out.String())
		}
	}
}

// --- eka export CLI-level tests -----------------------------------------

// exportFixtureAbs is the absolute path of the exchange test fixture (the
// export command always roots at the current directory).
func exportFixtureAbs(t *testing.T, name string) string {
	t.Helper()
	abs, err := filepath.Abs(filepath.Join("..", "testdata", "exchange", name))
	if err != nil {
		t.Fatal(err)
	}
	return abs
}

func TestExportHelpExitsZero(t *testing.T) {
	for _, args := range [][]string{{"export", "-h"}, {"export", "--help"}} {
		code, text, _ := runIn(args)
		if code != 0 {
			t.Errorf("args %v: exit = %d, want 0", args, code)
		}
		if !strings.Contains(text, "eka export") {
			t.Errorf("args %v: export help text missing usage", args)
		}
		if !strings.Contains(text, "--output") {
			t.Errorf("args %v: export help must document the --output flag", args)
		}
	}
}

func TestExportUnknownFlagExitsTwo(t *testing.T) {
	code, _, errText := runIn([]string{"export", "--bogus"})
	if code != 2 {
		t.Errorf("exit = %d, want 2", code)
	}
	if !strings.Contains(errText, "--bogus") {
		t.Errorf("stderr must name the unknown flag, got %q", errText)
	}
}

// TestExportHappyPath: exporting a valid repository with -o produces the
// package and reports the summary.
func TestExportHappyPath(t *testing.T) {
	dir := exportFixtureAbs(t, "valid")
	chdirInto(t, dir)
	out := filepath.Join(t.TempDir(), "cli.ekapkg")
	code, text, errText := runIn([]string{"export", "-o", out})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", code, text, errText)
	}
	for _, want := range []string{"Knowledge Package", "↓ Export", "rsf-repo-eka-valid-fixture-2", "Artifacts: 6", "Attachments: 1", "Output:", "PASS (0 errors"} {
		if !strings.Contains(text, want) {
			t.Errorf("output must contain %q:\n%s", want, text)
		}
	}
	// The loading state must carry a contextual message, never a bare
	// spinner (and never a spinner frame on non-TTY).
	if !strings.Contains(text, "Loading Engineering Knowledge...") {
		t.Errorf("output must show the contextual loading message:\n%s", text)
	}
	if strings.Contains(text, "⠋") {
		t.Errorf("non-TTY output must not contain spinner frames:\n%s", text)
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatalf("package file missing: %v", err)
	}
}

// TestExportRefusesInvalidRepo: blocking violations exit 1, print the
// report, and produce no package file.
func TestExportRefusesInvalidRepo(t *testing.T) {
	dir := exportFixtureAbs(t, "invalid")
	chdirInto(t, dir)
	out := filepath.Join(t.TempDir(), "refused.ekapkg")
	code, text, errText := runIn([]string{"export", "-o", out})
	if code != 1 {
		t.Fatalf("exit = %d, want 1\nstdout: %s\nstderr: %s", code, text, errText)
	}
	if !strings.Contains(text, "Verdict: FAIL") {
		t.Errorf("stdout must contain the validation report:\n%s", text)
	}
	if !strings.Contains(errText, "export refused") {
		t.Errorf("stderr must explain the refusal, got %q", errText)
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Error("no package file may be produced for an invalid repository")
	}
}

// TestExportBadTarget: malformed or unknown targets exit 2.
func TestExportBadTarget(t *testing.T) {
	dir := exportFixtureAbs(t, "valid")
	chdirInto(t, dir)
	code, _, errText := runIn([]string{"export", "sto:missing"})
	if code != 2 {
		t.Errorf("missing artifact: exit = %d, want 2", code)
	}
	if !strings.Contains(errText, "does not exist") {
		t.Errorf("stderr must explain the missing artifact, got %q", errText)
	}

	code, _, errText = runIn([]string{"export", "bogus:1"})
	if code != 2 {
		t.Errorf("unknown type: exit = %d, want 2", code)
	}
	if !strings.Contains(errText, "bogus") {
		t.Errorf("stderr must name the bad token, got %q", errText)
	}
}

// TestExportOutputFlag: --output works like -o.
func TestExportOutputFlag(t *testing.T) {
	dir := exportFixtureAbs(t, "valid")
	chdirInto(t, dir)
	out := filepath.Join(t.TempDir(), "long.ekapkg")
	code, _, errText := runIn([]string{"export", "--output", out})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstderr: %s", code, errText)
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatalf("package file missing: %v", err)
	}
}

// TestExportDirectoryOutput: --output naming an existing directory writes
// the directory layout.
func TestExportDirectoryOutput(t *testing.T) {
	dir := exportFixtureAbs(t, "valid")
	chdirInto(t, dir)
	outDir := filepath.Join(t.TempDir(), "layout")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatal(err)
	}
	code, text, errText := runIn([]string{"export", "-o", outDir})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", code, text, errText)
	}
	if !strings.Contains(text, "directory") {
		t.Errorf("output must report the directory mode:\n%s", text)
	}
	for _, want := range []string{"header.json", "manifest.json", "declarations.json", "integrity.json"} {
		if _, err := os.Stat(filepath.Join(outDir, want)); err != nil {
			t.Errorf("directory layout missing %s: %v", want, err)
		}
	}
}

// TestExportRefusesNonEKADirectory (ADR-018 gate): exporting from a
// directory whose walk-up finds no eka.yaml is refused deterministically
// with the pinned sentence naming the USER-INPUT directory (the working
// directory), exit 2 — the gate closes the ADR-018 contract on the
// export path, and the refusal names the directory the user ran in.
func TestExportRefusesNonEKADirectory(t *testing.T) {
	dir := t.TempDir()
	chdirInto(t, dir)
	code, _, errText := runIn([]string{"export"})
	if code != 2 {
		t.Fatalf("exit = %d, want 2\nstderr: %s", code, errText)
	}
	want := "export refused: " + dir + " is not an EKA repository (no eka.yaml); run 'eka init' first"
	if !strings.Contains(errText, want) {
		t.Errorf("stderr must carry the pinned ADR-018 refusal %q, got %q", want, errText)
	}
}

// TestExportDeterministicCLI: two CLI exports produce byte-identical
// packages.
func TestExportDeterministicCLI(t *testing.T) {
	dir := exportFixtureAbs(t, "valid")
	chdirInto(t, dir)
	tmp := t.TempDir()
	out1 := filepath.Join(tmp, "one.ekapkg")
	out2 := filepath.Join(tmp, "two.ekapkg")
	code, _, errText := runIn([]string{"export", "-o", out1})
	if code != 0 {
		t.Fatalf("first export: exit = %d\nstderr: %s", code, errText)
	}
	code, _, errText = runIn([]string{"export", "-o", out2})
	if code != 0 {
		t.Fatalf("second export: exit = %d\nstderr: %s", code, errText)
	}
	data1, err := os.ReadFile(out1)
	if err != nil {
		t.Fatal(err)
	}
	data2, err := os.ReadFile(out2)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data1, data2) {
		t.Error("two CLI exports of identical state must be byte-identical")
	}
}

// --- eka import CLI-level tests -----------------------------------------

// importPackageFixture exports the valid fixture and returns the package
// path plus a fresh EKA target repository (docs/ tree + eka.yaml — the
// repository-context gate of ADR-018 requires the marker).
func importPackageFixture(t *testing.T) (pkg string, repo string) {
	t.Helper()
	dir := exportFixtureAbs(t, "valid")
	chdirInto(t, dir)
	pkg = filepath.Join(t.TempDir(), "cli.ekapkg")
	if code, _, errText := runIn([]string{"export", "-o", pkg}); code != 0 {
		t.Fatalf("fixture export failed: exit %d\n%s", code, errText)
	}
	repo = t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeEkaYAML(t, repo, filepath.Base(repo), filepath.Base(repo), "eka-valid-fixture")
	return pkg, repo
}

func TestImportHelpExitsZero(t *testing.T) {
	for _, args := range [][]string{{"import", "-h"}, {"import", "--help"}} {
		code, text, _ := runIn(args)
		if code != 0 {
			t.Errorf("args %v: exit = %d, want 0", args, code)
		}
		if !strings.Contains(text, "eka import") {
			t.Errorf("args %v: import help text missing usage", args)
		}
		if !strings.Contains(text, "<package-path>") {
			t.Errorf("args %v: import help must document the positional argument", args)
		}
	}
}

func TestImportUnknownFlagExitsTwo(t *testing.T) {
	code, _, errText := runIn([]string{"import", "--bogus"})
	if code != 2 {
		t.Errorf("exit = %d, want 2", code)
	}
	if !strings.Contains(errText, "--bogus") {
		t.Errorf("stderr must name the unknown flag, got %q", errText)
	}
}

func TestImportMissingArgumentExitsTwo(t *testing.T) {
	code, _, errText := runIn([]string{"import"})
	if code != 2 {
		t.Errorf("exit = %d, want 2", code)
	}
	if !strings.Contains(errText, "accepts 1 arg(s)") {
		t.Errorf("stderr must explain the argument requirement, got %q", errText)
	}
}

// TestImportHappyPathExitsZero: import a valid package into a fresh
// repository — exit 0 with the deterministic summary.
func TestImportHappyPathExitsZero(t *testing.T) {
	pkg, repo := importPackageFixture(t)
	chdirInto(t, repo)
	code, text, errText := runIn([]string{"import", pkg})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", code, text, errText)
	}
	for _, want := range []string{"Knowledge Package", "↓ Import", "rsf-repo-eka-valid-fixture-2", "Imported: 6", "Skipped (no-op): 0", "Conflicts: 0", "Validation: PASS"} {
		if !strings.Contains(text, want) {
			t.Errorf("output must contain %q:\n%s", want, text)
		}
	}
	if _, err := os.Stat(filepath.Join(repo, "docs", "decisions", "adr-001-exchange.json")); err != nil {
		t.Errorf("imported artifact file missing: %v", err)
	}
	// The imported repository must validate.
	code, text, _ = runIn([]string{"validate", repo})
	if code != 0 {
		t.Errorf("imported repository must validate, exit = %d:\n%s", code, text)
	}
}

// TestImportTwiceExitsZero: re-importing the same package is a no-op —
// still exit 0, zero imported, zero conflicts.
func TestImportTwiceExitsZero(t *testing.T) {
	pkg, repo := importPackageFixture(t)
	chdirInto(t, repo)
	if code, _, errText := runIn([]string{"import", pkg}); code != 0 {
		t.Fatalf("first import: exit %d\n%s", code, errText)
	}
	code, text, errText := runIn([]string{"import", pkg})
	if code != 0 {
		t.Fatalf("second import: exit = %d, want 0\nstdout: %s\nstderr: %s", code, text, errText)
	}
	if !strings.Contains(text, "Imported: 0") {
		t.Errorf("second import must report 0 imported:\n%s", text)
	}
	if !strings.Contains(text, "Skipped (no-op): 6") {
		t.Errorf("second import must report 6 skipped no-ops:\n%s", text)
	}
}

// TestImportBadPackagePathExitsTwo: a missing/unreadable package is a
// usage-class failure (exit 2).
func TestImportBadPackagePathExitsTwo(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeEkaYAML(t, repo, filepath.Base(repo), filepath.Base(repo), "eka-valid-fixture")
	chdirInto(t, repo)
	code, _, errText := runIn([]string{"import", filepath.Join(t.TempDir(), "nope.ekapkg")})
	if code != 2 {
		t.Errorf("exit = %d, want 2", code)
	}
	if !strings.Contains(errText, "eka:") {
		t.Errorf("stderr must be a deterministic eka error, got %q", errText)
	}
}

// TestImportIntoNonEKADirExitsTwo (ADR-018): a destination without
// eka.yaml is not an EKA repository — the import is refused at the
// repository-context gate (exit 2, usage class). A destination that
// carries eka.yaml but has no docs/ tree reaches the engine's docs-tree
// check instead (exit 1, repository-side failure).
func TestImportIntoNonEKADirExitsTwo(t *testing.T) {
	pkg, _ := importPackageFixture(t)
	plain := t.TempDir() // no eka.yaml
	chdirInto(t, plain)
	code, _, errText := runIn([]string{"import", pkg})
	if code != 2 {
		t.Errorf("exit = %d, want 2", code)
	}
	if !strings.Contains(errText, "import failed:") ||
		!strings.Contains(errText, "is not an EKA repository (no eka.yaml)") ||
		!strings.Contains(errText, "run 'eka init' first") {
		t.Errorf("stderr must carry the pinned ADR-018 refusal, got %q", errText)
	}

	// eka.yaml present but no docs/ knowledge tree: the engine-level
	// docs-tree refusal (exit 1, reworded so it never collides with the
	// eka.yaml gate message).
	noDocs := t.TempDir()
	writeEkaYAML(t, noDocs, filepath.Base(noDocs), filepath.Base(noDocs), "eka-valid-fixture")
	chdirInto(t, noDocs)
	code, _, errText = runIn([]string{"import", pkg})
	if code != 1 {
		t.Errorf("no-docs destination: exit = %d, want 1", code)
	}
	if !strings.Contains(errText, "has no docs/ knowledge tree") {
		t.Errorf("stderr must carry the docs-tree refusal, got %q", errText)
	}
}

// TestImportConflictExitsOne: a conflicting identity aborts with exit 1,
// a per-identity summary, and no changes.
func TestImportConflictExitsOne(t *testing.T) {
	pkg, repo := importPackageFixture(t)
	// CWD is the fixture directory (importPackageFixture chdirs into it):
	// copy the full fixture tree so the modified artifact stays
	// referentially valid, then modify one artifact so the package
	// conflicts with the repository.
	err := filepath.WalkDir(".", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		dst := filepath.Join(repo, path)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		return os.WriteFile(dst, data, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	adr := filepath.Join(repo, "docs", "decisions", "adr-001-exchange.md")
	orig, err := os.ReadFile(adr)
	if err != nil {
		t.Fatal(err)
	}
	modified := strings.Replace(string(orig), "author: Engineering Architecture", "author: Someone Else", 1)
	if err := os.WriteFile(adr, []byte(modified), 0o644); err != nil {
		t.Fatal(err)
	}
	before, err := snapshot(repo)
	if err != nil {
		t.Fatal(err)
	}
	chdirInto(t, repo)
	code, text, errText := runIn([]string{"import", pkg})
	if code != 1 {
		t.Fatalf("exit = %d, want 1\nstdout: %s\nstderr: %s", code, text, errText)
	}
	if !strings.Contains(errText, "eka-valid-fixture/adr:001-exchange:1") {
		t.Errorf("conflict summary must name the identity, got %q", errText)
	}
	if !strings.Contains(errText, "metadata differs") {
		t.Errorf("conflict summary must list the differences, got %q", errText)
	}
	after, err := snapshot(repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Errorf("conflict import must not change the repository: %d files before, %d after", len(before), len(after))
	}
}

// TestImportRelationshipErrorExitsOne: a package carrying a declared
// external reference that cannot resolve in the target repository (a
// non-draft unit) fails at CLI level with exit 1 and the deterministic
// RelationshipError stderr. The package is crafted at CLI level: a
// line-scope export of the fixture's adr declares its depends-on sto
// target as external (the sto is not in the package), and the empty
// target repository cannot resolve it.
func TestImportRelationshipErrorExitsOne(t *testing.T) {
	dir := exportFixtureAbs(t, "valid")
	chdirInto(t, dir)
	pkg := filepath.Join(t.TempDir(), "line.ekapkg")
	if code, _, errText := runIn([]string{"export", "adr:001-exchange", "-o", pkg}); code != 0 {
		t.Fatalf("fixture export failed: exit %d\n%s", code, errText)
	}

	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeEkaYAML(t, repo, filepath.Base(repo), filepath.Base(repo), "eka-valid-fixture")
	chdirInto(t, repo)
	code, text, errText := runIn([]string{"import", pkg})
	if code != 1 {
		t.Fatalf("exit = %d, want 1\nstdout: %s\nstderr: %s", code, text, errText)
	}
	if text != "" {
		t.Errorf("stdout must be empty on a relationship failure, got %q", text)
	}
	// Deterministic stderr: the RelationshipError summary line plus the
	// single failing relationship detail, byte-exact.
	want := "eka: import refused: 1 unresolved relationship(s) outside draft tolerance; no changes written\n" +
		"  - eka-valid-fixture/adr:001-exchange:1 -> depends-on eka-valid-fixture/sto:login-email:1 (declared external reference does not resolve in the target repository)\n"
	if errText != want {
		t.Errorf("stderr must be deterministic:\ngot  %q\nwant %q", errText, want)
	}
}

// --- presentation contract tests ---------------------------------------

// TestNoANSIEscapesInNonTTYOutput verifies the determinism contract:
// every command run against a non-terminal writer emits plain text
// (plus UTF-8 icons) with zero ANSI escape sequences — in stdout and
// stderr alike.
func TestNoANSIEscapesInNonTTYOutput(t *testing.T) {
	valid := filepath.Join("..", "testdata", "conformance", "valid")
	type scenario struct {
		name string
		args []string
		want int
	}
	scenarios := []scenario{
		{"validate-pass", []string{"validate", valid}, 0},
		{"validate-fail", []string{"validate", filepath.Join("..", "testdata", "conformance", "invalid-dimension")}, 1},
		// Help paths (root help, the help command and subcommand help)
		// carry the same no-ANSI contract; they need no repository.
		{"root-help", []string{"--help"}, 0},
		{"help-command", []string{"help"}, 0},
		{"subcommand-help", []string{"validate", "--help"}, 0},
		{"nested-help", []string{"get", "ticket", "--help"}, 0},
	}
	// The validate paths are relative to the package directory: run them
	// before any chdir in this test.
	assertNoANSI := func(sc scenario) {
		t.Helper()
		var out, errb bytes.Buffer
		code := Execute(sc.args, strings.NewReader(""), &out, &errb)
		if strings.Contains(out.String(), "\x1b") || strings.Contains(errb.String(), "\x1b") {
			t.Errorf("%s: non-TTY output must not contain ANSI escapes:\nstdout: %q\nstderr: %q",
				sc.name, out.String(), errb.String())
		}
		if code != sc.want {
			t.Errorf("%s: exit = %d, want %d", sc.name, code, sc.want)
		}
	}
	assertNoANSI(scenarios[0])
	assertNoANSI(scenarios[1])

	// importPackageFixture resolves its fixture against the package
	// directory, so it must run before any chdir in this test; the
	// export fixture path is resolved here for the same reason.
	exportFixture := exportFixtureAbs(t, "valid")
	pkg, repo := importPackageFixture(t)
	chdirInto(t, repo)
	assertNoANSI(scenario{"import", []string{"import", pkg}, 0})

	dir := t.TempDir()
	chdirInto(t, dir)
	assertNoANSI(scenario{"init-dry-run", []string{"init", "--dry-run"}, 0})

	// Export roots at the current directory: chdir into the fixture.
	chdirInto(t, exportFixture)
	assertNoANSI(scenario{"export", []string{"export", "-o", filepath.Join(t.TempDir(), "noansi.ekapkg")}, 0})
}

// TestOutputDeterministicPerCommand verifies that two runs of each
// command against identical state produce byte-identical non-TTY
// output.
func TestOutputDeterministicPerCommand(t *testing.T) {
	// validate runs first: its path is relative to the package
	// directory, so it must run before any chdir in this test.
	path := filepath.Join("..", "testdata", "conformance", "invalid-projection")
	_, a, _ := runIn([]string{"validate", path})
	_, b, _ := runIn([]string{"validate", path})
	if a != b {
		t.Error("validate output differs between runs")
	}

	// Resolve absolute paths while the package directory is still the
	// working directory.
	exportFixture := exportFixtureAbs(t, "valid")
	pkg, _ := importPackageFixture(t)

	// init --dry-run (same directory, writes nothing)
	dir := t.TempDir()
	chdirInto(t, dir)
	_, a, _ = runIn([]string{"init", "--dry-run"})
	_, b, _ = runIn([]string{"init", "--dry-run"})
	if a != b {
		t.Error("init --dry-run output differs between runs")
	}

	// export (same fixture, same output path, stdout only)
	chdirInto(t, exportFixture)
	out := filepath.Join(t.TempDir(), "det.ekapkg")
	_, a, _ = runIn([]string{"export", "-o", out})
	_, b, _ = runIn([]string{"export", "-o", out})
	if a != b {
		t.Error("export output differs between runs")
	}

	// import (two identical fresh repositories)
	runImport := func() string {
		repo := t.TempDir()
		if err := os.MkdirAll(filepath.Join(repo, "docs"), 0o755); err != nil {
			t.Fatal(err)
		}
		writeEkaYAML(t, repo, filepath.Base(repo), filepath.Base(repo), "eka-valid-fixture")
		chdirInto(t, repo)
		_, text, _ := runIn([]string{"import", pkg})
		return text
	}
	if a, b := runImport(), runImport(); a != b {
		t.Error("import output differs between runs")
	}
}

// TestVerboseAddsDetail verifies the presentation-only --verbose flag:
// verbose output contains the per-unit detail lines and is strictly
// longer than the default concise output.
func TestVerboseAddsDetail(t *testing.T) {
	chdirInto(t, exportFixtureAbs(t, "valid"))
	tmp := t.TempDir()
	_, concise, _ := runIn([]string{"export", "-o", filepath.Join(tmp, "c.ekapkg")})
	_, verbose, _ := runIn([]string{"export", "-v", "-o", filepath.Join(tmp, "v.ekapkg")})

	if strings.Contains(concise, "eka-valid-fixture/adr:001-exchange:1") {
		t.Errorf("default output must not list per-unit identities:\n%s", concise)
	}
	if !strings.Contains(verbose, "eka-valid-fixture/adr:001-exchange:1") {
		t.Errorf("verbose output must list per-unit identities:\n%s", verbose)
	}
	if !strings.Contains(verbose, "Units:") {
		t.Errorf("verbose output must contain the Units section:\n%s", verbose)
	}
	if len(verbose) <= len(concise) {
		t.Errorf("verbose output must contain more detail than default (%d <= %d bytes)", len(verbose), len(concise))
	}
	if strings.Contains(verbose, "\x1b") {
		t.Errorf("verbose non-TTY output must stay ANSI-free:\n%s", verbose)
	}
}
