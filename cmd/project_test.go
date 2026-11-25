package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/maleolabs/eka-core/metadata"
)

func TestProjectHelpExitsZero(t *testing.T) {
	for _, args := range [][]string{{"project", "-h"}, {"project", "register", "-h"}, {"project", "list", "-h"}} {
		code, text, _ := runIn(args)
		if code != 0 {
			t.Errorf("args %v: exit = %d, want 0", args, code)
		}
		if !strings.Contains(text, "eka project") {
			t.Errorf("args %v: help must mention eka project", args)
		}
	}
}

// TestProjectRegisterHappyPath: registering a repository exits 0 and
// reports the project/repository/status. The fixture carries eka.yaml,
// so the identity comes from the file (project and repository name =
// eka-sync-fixture, never the temp-dir basename).
func TestProjectRegisterHappyPath(t *testing.T) {
	t.Setenv("EKA_HOME", t.TempDir())
	repo := copySyncFixture(t)
	code, text, errText := runIn([]string{"project", "register", repo})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", code, text, errText)
	}
	for _, want := range []string{"Project", "Repository", "Path", "Status", "registered", "eka-sync-fixture"} {
		if !strings.Contains(text, want) {
			t.Errorf("output must contain %q:\n%s", want, text)
		}
	}
	// The identity comes from eka.yaml: the repository NAME is
	// eka-sync-fixture, never the temp-dir basename (the Path field
	// legitimately shows the temp dir).
	if strings.Contains(text, "Repository   "+filepath.Base(repo)) {
		t.Errorf("repository name must come from eka.yaml, not the basename %q:\n%s", filepath.Base(repo), text)
	}
}

// TestProjectRegisterTwiceReportsAlreadyRegistered.
func TestProjectRegisterTwiceReportsAlreadyRegistered(t *testing.T) {
	t.Setenv("EKA_HOME", t.TempDir())
	repo := copySyncFixture(t)
	if code, _, errText := runIn([]string{"project", "register", repo}); code != 0 {
		t.Fatalf("first register: exit %d\n%s", code, errText)
	}
	code, text, errText := runIn([]string{"project", "register", repo})
	if code != 0 {
		t.Fatalf("second register: exit = %d\nstdout: %s\nstderr: %s", code, text, errText)
	}
	if !strings.Contains(text, "already registered") {
		t.Errorf("second register must report already registered:\n%s", text)
	}
}

// TestProjectRegisterCustomProject: the project name comes from
// eka.yaml — a repository whose file records project "myproject" is
// registered under it (the metadata is the identity authority).
func TestProjectRegisterCustomProject(t *testing.T) {
	t.Setenv("EKA_HOME", t.TempDir())
	repo := t.TempDir()
	writeEkaYAML(t, repo, "myproject", "myrepo", "my-namespace")
	code, text, errText := runIn([]string{"project", "register", repo})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", code, text, errText)
	}
	if !strings.Contains(text, "myproject") || !strings.Contains(text, "myrepo") {
		t.Errorf("output must carry the metadata project and repository:\n%s", text)
	}
}

// TestProjectListEmpty: an empty workspace lists no projects and exits
// 0 with the informational message.
func TestProjectListEmpty(t *testing.T) {
	t.Setenv("EKA_HOME", t.TempDir())
	code, text, errText := runIn([]string{"project", "list"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", code, text, errText)
	}
	if !strings.Contains(text, "No projects registered yet") {
		t.Errorf("empty list must be informational:\n%s", text)
	}
}

// TestProjectListSorted: projects and repositories render sorted, with
// the workspace path in the header. Both fixtures carry eka.yaml; the
// second one records project "zproject" so the list carries two
// projects.
func TestProjectListSorted(t *testing.T) {
	home := t.TempDir()
	t.Setenv("EKA_HOME", home)
	repoA := copySyncFixture(t)
	repoB := copySyncFixture(t)
	writeEkaYAML(t, repoB, "zproject", "zrepo", "eka-sync-fixture")
	for _, args := range [][]string{
		{"project", "register", repoA},
		{"project", "register", repoB},
	} {
		if code, _, errText := runIn(args); code != 0 {
			t.Fatalf("register %v: exit %d\n%s", args, code, errText)
		}
	}
	code, text, errText := runIn([]string{"project", "list"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", code, text, errText)
	}
	if !strings.Contains(text, home) {
		t.Errorf("list must show the workspace path:\n%s", text)
	}
	// Both project names present; the repository names from eka.yaml
	// present (the fixture identity eka-sync-fixture and the second
	// repo's zrepo).
	for _, want := range []string{"eka-sync-fixture", "zrepo", "zproject"} {
		if !strings.Contains(text, want) {
			t.Errorf("list missing %q:\n%s", want, text)
		}
	}
	// Deterministic: same state, same bytes.
	_, text2, _ := runIn([]string{"project", "list"})
	if text != text2 {
		t.Error("project list output differs between runs")
	}
}

// TestProjectRegisterRejectsMissingPath: an unreadable path is a usage
// error (exit 2).
func TestProjectRegisterRejectsMissingPath(t *testing.T) {
	t.Setenv("EKA_HOME", t.TempDir())
	code, _, errText := runIn([]string{"project", "register", filepath.Join(t.TempDir(), "nope")})
	if code != 2 {
		t.Errorf("exit = %d, want 2\nstderr: %s", code, errText)
	}
	if errText == "" {
		t.Error("stderr must not be empty")
	}
}

// TestProjectRegisterRefusesWithoutEKA (ADR-018): registration requires
// an EKA repository — a directory without eka.yaml is refused with the
// pinned gate sentence, exit 2. There is no legacy registration path.
func TestProjectRegisterRefusesWithoutEKA(t *testing.T) {
	t.Setenv("EKA_HOME", t.TempDir())
	dir := t.TempDir()
	code, _, errText := runIn([]string{"project", "register", dir})
	if code != 2 {
		t.Fatalf("exit = %d, want 2\nstderr: %s", code, errText)
	}
	if !strings.Contains(errText, "is not an EKA repository (no eka.yaml)") ||
		!strings.Contains(errText, "run 'eka init' first") {
		t.Errorf("stderr must carry the pinned ADR-018 refusal, got %q", errText)
	}
	// Nothing was registered.
	w := mustWorkspace(t)
	t.Cleanup(func() { w.Close() })
	repos, err := w.Repos(filepath.Base(dir))
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 0 {
		t.Errorf("a refused register must not register anything, got %+v", repos)
	}
}

// TestProjectRegisterSubdirectoryRegistersRoot (BLOCKER regression):
// registering a SUBDIRECTORY of a metadata repository registers the
// walk-up repository root — the stored path is the root, never the
// argument subdir.
func TestProjectRegisterSubdirectoryRegistersRoot(t *testing.T) {
	t.Setenv("EKA_HOME", t.TempDir())
	repo := copySyncFixture(t)
	subdir := filepath.Join(repo, "docs")
	code, text, errText := runIn([]string{"project", "register", subdir})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", code, text, errText)
	}
	for _, want := range []string{"Project", "Repository", "Path", "registered", "eka-sync-fixture"} {
		if !strings.Contains(text, want) {
			t.Errorf("output must contain %q:\n%s", want, text)
		}
	}
	// The registry carries the ROOT path, never the subdir argument.
	w := mustWorkspace(t)
	t.Cleanup(func() { w.Close() })
	repos, err := w.Repos("eka-sync-fixture")
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 1 {
		t.Fatalf("expected 1 repository under eka-sync-fixture, got %d", len(repos))
	}
	if repos[0].Path != repo {
		t.Errorf("registered path = %q, want the repository root %q (never the subdir %q)", repos[0].Path, repo, subdir)
	}
}

// TestProjectRemoveHappyPath: removing a registered repository exits 0,
// reports the removed repository (name + path) with the store note, and
// the registry row is gone.
func TestProjectRemoveHappyPath(t *testing.T) {
	t.Setenv("EKA_HOME", t.TempDir())
	repo := copySyncFixture(t)
	if code, _, errText := runIn([]string{"project", "register", repo}); code != 0 {
		t.Fatalf("register: exit %d\n%s", code, errText)
	}
	code, text, errText := runIn([]string{"project", "remove", "eka-sync-fixture/eka-sync-fixture"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", code, text, errText)
	}
	for _, want := range []string{
		"Projects",
		"removed eka-sync-fixture",
		displayPath(repo),
		"Canonical knowledge objects remain in the workspace store; re-registering restores provenance access.",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("output must contain %q:\n%s", want, text)
		}
	}
	// The registry row is gone.
	w := mustWorkspace(t)
	t.Cleanup(func() { w.Close() })
	repos, err := w.Repos("eka-sync-fixture")
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 0 {
		t.Errorf("repos after removal = %+v, want none", repos)
	}
}

// TestProjectRemoveLastRepoRemovesProject: removing a project's LAST
// repository deletes the emptied project too — the list is empty again.
func TestProjectRemoveLastRepoRemovesProject(t *testing.T) {
	t.Setenv("EKA_HOME", t.TempDir())
	repo := copySyncFixture(t)
	if code, _, errText := runIn([]string{"project", "register", repo}); code != 0 {
		t.Fatalf("register: exit %d\n%s", code, errText)
	}
	if code, _, errText := runIn([]string{"project", "remove", "eka-sync-fixture/eka-sync-fixture"}); code != 0 {
		t.Fatalf("remove: exit %d\n%s", code, errText)
	}
	code, text, errText := runIn([]string{"project", "list"})
	if code != 0 {
		t.Fatalf("list: exit = %d\nstdout: %s\nstderr: %s", code, text, errText)
	}
	if !strings.Contains(text, "No projects registered yet") {
		t.Errorf("the emptied project must be gone:\n%s", text)
	}
}

// TestProjectRemoveUnknownTarget: an unknown project or repository is
// refused (exit 2) with the deterministic candidate listing.
func TestProjectRemoveUnknownTarget(t *testing.T) {
	t.Setenv("EKA_HOME", t.TempDir())
	repo := copySyncFixture(t)
	if code, _, errText := runIn([]string{"project", "register", repo}); code != 0 {
		t.Fatalf("register: exit %d\n%s", code, errText)
	}
	// Unknown project lists the registered projects.
	code, _, errText := runIn([]string{"project", "remove", "ghost/repo"})
	if code != 2 {
		t.Errorf("unknown project: exit = %d, want 2\nstderr: %s", code, errText)
	}
	if !strings.Contains(errText, `unknown project "ghost"`) ||
		!strings.Contains(errText, "available projects: eka-sync-fixture") {
		t.Errorf("unknown project must list candidates, got %q", errText)
	}
	// Unknown repository lists the project's repositories.
	code, _, errText = runIn([]string{"project", "remove", "eka-sync-fixture/ghost"})
	if code != 2 {
		t.Errorf("unknown repository: exit = %d, want 2\nstderr: %s", code, errText)
	}
	if !strings.Contains(errText, `unknown repository "ghost" in project "eka-sync-fixture"`) ||
		!strings.Contains(errText, "available repositories: eka-sync-fixture") {
		t.Errorf("unknown repository must list candidates, got %q", errText)
	}
}

// TestProjectRemoveBadArg: anything but exactly one `<project>/<name>`
// composite is a usage error (exit 2).
func TestProjectRemoveBadArg(t *testing.T) {
	t.Setenv("EKA_HOME", t.TempDir())
	for _, args := range [][]string{
		{"project", "remove"},
		{"project", "remove", "noslash"},
		{"project", "remove", "a/b/c"},
		{"project", "remove", "/leading"},
		{"project", "remove", "trailing/"},
	} {
		code, _, errText := runIn(args)
		if code != 2 {
			t.Errorf("args %v: exit = %d, want 2\nstderr: %s", args, code, errText)
			continue
		}
		// Zero args fails on cobra's argument count; one malformed
		// argument must carry the composite hint.
		if len(args) == 3 && !strings.Contains(errText, "must be <project>/<name>") {
			t.Errorf("args %v: stderr must carry the composite hint, got %q", args, errText)
		}
	}
}

// TestProjectRemoveHelpExitsZero: the remove command documents itself
// and the parent help mentions it.
func TestProjectRemoveHelpExitsZero(t *testing.T) {
	code, text, _ := runIn([]string{"project", "remove", "-h"})
	if code != 0 {
		t.Errorf("exit = %d, want 0", code)
	}
	if !strings.Contains(text, "<project>/<name>") {
		t.Errorf("help must document the composite target:\n%s", text)
	}
	_, parent, _ := runIn([]string{"project", "-h"})
	if !strings.Contains(parent, "remove") {
		t.Errorf("parent help must mention remove:\n%s", parent)
	}
}

// copyFixtureWithIdentity copies the sync fixture and rewrites its
// eka.yaml identity (project/name), so tests can register several
// repositories under one project.
func copyFixtureWithIdentity(t *testing.T, project, name string) string {
	t.Helper()
	dir := copySyncFixture(t)
	data, err := os.ReadFile(filepath.Join(dir, "eka.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	m, err := metadata.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	m.Project, m.Name = project, name
	if err := os.WriteFile(filepath.Join(dir, "eka.yaml"), m.Marshal(), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestProjectUnregisterHappyPath: unregistering a whole project with
// --force exits 0, reports every removed repository with the store
// note, and both the repos rows and the project row are gone while a
// sibling project survives.
func TestProjectUnregisterHappyPath(t *testing.T) {
	t.Setenv("EKA_HOME", t.TempDir())
	repoA := copySyncFixture(t)
	repoB := copyFixtureWithIdentity(t, "eka-sync-fixture", "second")
	for _, repo := range []string{repoA, repoB} {
		if code, _, errText := runIn([]string{"project", "register", repo}); code != 0 {
			t.Fatalf("register %s: exit %d\n%s", repo, code, errText)
		}
	}
	code, text, errText := runIn([]string{"project", "unregister", "eka-sync-fixture", "--force"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", code, text, errText)
	}
	for _, want := range []string{
		"Projects",
		"unregistered project eka-sync-fixture",
		"(2 repositories)",
		displayPath(repoA),
		displayPath(repoB),
		"Canonical knowledge objects remain in the workspace store; re-registering restores provenance access.",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("output must contain %q:\n%s", want, text)
		}
	}
	// The registry rows are gone.
	w := mustWorkspace(t)
	t.Cleanup(func() { w.Close() })
	repos, err := w.Repos("eka-sync-fixture")
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 0 {
		t.Errorf("repos after unregistration = %+v, want none", repos)
	}
	projects, err := w.Projects()
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 0 {
		t.Errorf("projects after unregistration = %+v, want none", projects)
	}
}

// TestProjectUnregisterRequiresForceOutsideTTY: without --force and
// outside a terminal the command refuses (exit 2) with the --force
// hint — a captured-output run must never block on an invisible
// prompt.
func TestProjectUnregisterRequiresForceOutsideTTY(t *testing.T) {
	t.Setenv("EKA_HOME", t.TempDir())
	repo := copySyncFixture(t)
	if code, _, errText := runIn([]string{"project", "register", repo}); code != 0 {
		t.Fatalf("register: exit %d\n%s", code, errText)
	}
	code, _, errText := runIn([]string{"project", "unregister", "eka-sync-fixture"})
	if code != 2 {
		t.Errorf("exit = %d, want 2\nstderr: %s", code, errText)
	}
	if !strings.Contains(errText, `has 1 repository`) ||
		!strings.Contains(errText, "eka project unregister eka-sync-fixture --force") {
		t.Errorf("refusal must carry the count and the --force hint, got %q", errText)
	}
	// The registry is untouched by the refusal.
	w := mustWorkspace(t)
	t.Cleanup(func() { w.Close() })
	repos, err := w.Repos("eka-sync-fixture")
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 1 {
		t.Errorf("repos after refusal = %+v, want the repository kept", repos)
	}
}

// TestProjectUnregisterUnknownProject: an unknown project is refused
// (exit 2) with the deterministic candidate listing; a composite
// <project>/<name> argument is a usage error.
func TestProjectUnregisterUnknownProject(t *testing.T) {
	t.Setenv("EKA_HOME", t.TempDir())
	repo := copySyncFixture(t)
	if code, _, errText := runIn([]string{"project", "register", repo}); code != 0 {
		t.Fatalf("register: exit %d\n%s", code, errText)
	}
	code, _, errText := runIn([]string{"project", "unregister", "ghost"})
	if code != 2 {
		t.Errorf("unknown project: exit = %d, want 2\nstderr: %s", code, errText)
	}
	if !strings.Contains(errText, `unknown project "ghost"`) ||
		!strings.Contains(errText, "available projects: eka-sync-fixture") {
		t.Errorf("unknown project must list candidates, got %q", errText)
	}
	code, _, errText = runIn([]string{"project", "unregister", "a/b"})
	if code != 2 {
		t.Errorf("composite arg: exit = %d, want 2\nstderr: %s", code, errText)
	}
	if !strings.Contains(errText, `the target must be <project>`) {
		t.Errorf("composite arg must carry the <project> hint, got %q", errText)
	}
}

// TestProjectUnregisterHelpExitsZero: the unregister command documents
// itself and the parent help mentions it alongside remove.
func TestProjectUnregisterHelpExitsZero(t *testing.T) {
	code, text, _ := runIn([]string{"project", "unregister", "-h"})
	if code != 0 {
		t.Errorf("exit = %d, want 0", code)
	}
	for _, want := range []string{"<project>", "--force"} {
		if !strings.Contains(text, want) {
			t.Errorf("help must document %q:\n%s", want, text)
		}
	}
	_, parent, _ := runIn([]string{"project", "-h"})
	if !strings.Contains(parent, "unregister") || !strings.Contains(parent, "remove") {
		t.Errorf("parent help must mention unregister and remove:\n%s", parent)
	}
}

// writeEkaYAML writes a repository identity file into dir.
func writeEkaYAML(t *testing.T, dir, project, name, ns string) {
	t.Helper()
	content := "version: 1\nproject: " + project + "\nname: " + name + "\nnamespace: " + ns + "\n"
	if err := os.WriteFile(filepath.Join(dir, "eka.yaml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestProjectRegisterFromMetadata (ADR-017 §5.3): inside a repository
// with eka.yaml the identity comes from the file — project and
// repository name are the metadata values (never the basename), and
// repos.namespace is written immediately.
func TestProjectRegisterFromMetadata(t *testing.T) {
	t.Setenv("EKA_HOME", t.TempDir())
	repo := t.TempDir()
	writeEkaYAML(t, repo, "atrium", "api", "atrium-api")
	code, text, errText := runIn([]string{"project", "register", repo})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", code, text, errText)
	}
	for _, want := range []string{"atrium", "api", "registered"} {
		if !strings.Contains(text, want) {
			t.Errorf("output must contain %q:\n%s", want, text)
		}
	}
	// The registry carries the metadata identity: project atrium,
	// repository name api (not the temp-dir basename), namespace
	// atrium-api.
	w := mustWorkspace(t)
	t.Cleanup(func() { w.Close() })
	repos, err := w.Repos("atrium")
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 1 {
		t.Fatalf("expected 1 repository under atrium, got %d", len(repos))
	}
	if repos[0].Name != "api" {
		t.Errorf("repo name = %q, want api (from eka.yaml, never the basename)", repos[0].Name)
	}
	if repos[0].Namespace != "atrium-api" {
		t.Errorf("repo namespace = %q, want atrium-api (written at registration)", repos[0].Namespace)
	}
}

// TestProjectRegisterMetadataNameConflict (ADR-017 §5.3): an explicit
// --name conflicting with the project recorded in eka.yaml is refused
// with a deterministic hint (exit 2).
func TestProjectRegisterMetadataNameConflict(t *testing.T) {
	t.Setenv("EKA_HOME", t.TempDir())
	repo := t.TempDir()
	writeEkaYAML(t, repo, "atrium", "api", "atrium-api")
	code, _, errText := runIn([]string{"project", "register", repo, "--name", "other"})
	if code != 2 {
		t.Errorf("exit = %d, want 2\nstderr: %s", code, errText)
	}
	if !strings.Contains(errText, "conflicts with the project atrium recorded in eka.yaml") ||
		!strings.Contains(errText, "the metadata is the identity authority") {
		t.Errorf("stderr must carry the conflict hint, got %q", errText)
	}
}

// TestProjectRegisterMetadataNameMatch: --name equal to the metadata
// project is accepted (the metadata identity applies).
func TestProjectRegisterMetadataNameMatch(t *testing.T) {
	t.Setenv("EKA_HOME", t.TempDir())
	repo := t.TempDir()
	writeEkaYAML(t, repo, "atrium", "api", "atrium-api")
	code, text, errText := runIn([]string{"project", "register", repo, "--name", "atrium"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", code, text, errText)
	}
	if !strings.Contains(text, "atrium") {
		t.Errorf("output must carry the metadata project:\n%s", text)
	}
	w := mustWorkspace(t)
	t.Cleanup(func() { w.Close() })
	repos, err := w.Repos("atrium")
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 1 || repos[0].Name != "api" {
		t.Errorf("metadata identity must win even with a matching --name, got %+v", repos)
	}
}

// TestStatusAfterRegisterNoObjects: status renders workspace, schema,
// project and zero counts.
func TestStatusAfterRegisterNoObjects(t *testing.T) {
	home := t.TempDir()
	t.Setenv("EKA_HOME", home)
	repo := copySyncFixture(t)
	if code, _, errText := runIn([]string{"project", "register", repo}); code != 0 {
		t.Fatalf("register: exit %d\n%s", code, errText)
	}
	code, text, errText := runIn([]string{"status"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", code, text, errText)
	}
	for _, want := range []string{"Runtime", home, "Schema", "Objects", "Payloads", "Attachments", "Projects"} {
		if !strings.Contains(text, want) {
			t.Errorf("status missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "pull at") || strings.Contains(text, "push at") {
		t.Error("no sync log entries expected before any sync")
	}
}

// TestStatusAfterSyncShowsLastSync: after a sync the status reports
// the last pull/push per repository.
func TestStatusAfterSyncShowsLastSync(t *testing.T) {
	home := t.TempDir()
	t.Setenv("EKA_HOME", home)
	repo := copySyncFixture(t)
	if code, _, errText := runIn([]string{"sync", repo}); code != 0 {
		t.Fatalf("sync: exit %d\n%s", code, errText)
	}
	code, text, errText := runIn([]string{"status"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", code, text, errText)
	}
	if !strings.Contains(text, "Objects") {
		t.Errorf("status must show counts:\n%s", text)
	}
	// The sync log renders the most recent entry (the push after a
	// full sync cycle).
	if !strings.Contains(text, "push") {
		t.Errorf("status must show the last sync entry:\n%s", text)
	}
	if !strings.Contains(text, "at 20") {
		t.Errorf("status must show the sync timestamp:\n%s", text)
	}
	// Deterministic output.
	_, text2, _ := runIn([]string{"status"})
	if text != text2 {
		t.Error("status output differs between runs")
	}
}

// TestProjectRegisterContentNamespaceMismatch (ADR-020): a non-TTY
// register whose docs content resolves to exactly ONE namespace
// differing from the declared eka.yaml namespace is refused with the
// pinned byte-exact sentence, exit 2 — nothing registered.
func TestProjectRegisterContentNamespaceMismatch(t *testing.T) {
	t.Setenv("EKA_HOME", t.TempDir())
	repo := copySyncFixture(t)
	writeEkaYAML(t, repo, "eka-sync-fixture", "eka-sync-fixture", "other")

	code, _, errText := runIn([]string{"project", "register", repo})
	if code != 2 {
		t.Fatalf("exit = %d, want 2\nstderr: %s", code, errText)
	}
	want := "eka: project register failed: the repository content namespace eka-sync-fixture differs from the registered repository namespace other; run 'eka project register --override' to align the repository identity to eka-sync-fixture"
	if !strings.Contains(errText, want) {
		t.Errorf("stderr must carry the pinned byte-exact refusal, got %q", errText)
	}
	// Nothing was registered.
	w := mustWorkspace(t)
	t.Cleanup(func() { w.Close() })
	repos, err := w.Repos("eka-sync-fixture")
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 0 {
		t.Errorf("a refused register must not register anything, got %+v", repos)
	}
}

// TestProjectRegisterOverrideAlignsIdentity (ADR-020): register
// --override aligns the identity to the content — exit 0, eka.yaml
// rewritten, repos.namespace aligned, the aligned note printed.
func TestProjectRegisterOverrideAlignsIdentity(t *testing.T) {
	t.Setenv("EKA_HOME", t.TempDir())
	repo := copySyncFixture(t)
	writeEkaYAML(t, repo, "eka-sync-fixture", "eka-sync-fixture", "other")

	code, text, errText := runIn([]string{"project", "register", "--override", repo})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", code, text, errText)
	}
	note := "repository namespace aligned: other → eka-sync-fixture (eka.yaml updated; identity frozen again)"
	if !strings.Contains(text, note) {
		t.Errorf("output must carry the aligned note, got:\n%s", text)
	}
	data, err := os.ReadFile(filepath.Join(repo, "eka.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "namespace: eka-sync-fixture") {
		t.Errorf("eka.yaml must be rewritten to the content namespace:\n%s", data)
	}
	if strings.Contains(string(data), "namespace: other") {
		t.Errorf("eka.yaml must not keep the old namespace:\n%s", data)
	}
	w := mustWorkspace(t)
	t.Cleanup(func() { w.Close() })
	repos, err := w.Repos("eka-sync-fixture")
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 1 || repos[0].Namespace != "eka-sync-fixture" {
		t.Errorf("repos.namespace = %+v, want the aligned eka-sync-fixture", repos)
	}
}

// TestProjectRegisterContentNamespaceMulti (ADR-020): docs content
// spanning MULTIPLE distinct namespaces is refused WITHOUT override —
// a repository is one platform, consolidate the content first.
func TestProjectRegisterContentNamespaceMulti(t *testing.T) {
	t.Setenv("EKA_HOME", t.TempDir())
	repo := copySyncFixture(t)
	// A second conformant artifact with a DIFFERENT namespace (see the
	// sync engine test for the same trick): the depends-on is dropped
	// because `sto:login-email` resolves within the artifact's own
	// namespace, which changed — an unresolved reference (R5) would
	// fail before the reconciliation.
	adr, err := os.ReadFile(filepath.Join(repo, "docs", "decisions", "adr-001-runtime.md"))
	if err != nil {
		t.Fatal(err)
	}
	second := strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(string(adr),
		"namespace: eka-sync-fixture", "namespace: eka-sync-fixture-b"),
		"id: 001-runtime", "id: 002-extra"),
		"depends-on:\n  - sto:login-email", "depends-on: []")
	if err := os.WriteFile(filepath.Join(repo, "docs", "decisions", "adr-002-extra.md"), []byte(second), 0o644); err != nil {
		t.Fatal(err)
	}
	writeEkaYAML(t, repo, "eka-sync-fixture", "eka-sync-fixture", "other")

	code, _, errText := runIn([]string{"project", "register", "--override", repo})
	if code != 2 {
		t.Fatalf("exit = %d, want 2\nstderr: %s", code, errText)
	}
	want := "eka: project register failed: the repository content spans multiple namespaces (eka-sync-fixture, eka-sync-fixture-b); a repository is one platform — consolidate the content"
	if !strings.Contains(errText, want) {
		t.Errorf("stderr must carry the pinned byte-exact multi-ns refusal, got %q", errText)
	}
	// Nothing was registered; eka.yaml untouched.
	w := mustWorkspace(t)
	t.Cleanup(func() { w.Close() })
	repos, err := w.Repos("eka-sync-fixture")
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 0 {
		t.Errorf("a refused register must not register anything, got %+v", repos)
	}
	data, err := os.ReadFile(filepath.Join(repo, "eka.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "namespace: other") {
		t.Errorf("eka.yaml must stay untouched after the refusal:\n%s", data)
	}
}
