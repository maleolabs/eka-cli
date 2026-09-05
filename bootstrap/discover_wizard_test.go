package bootstrap

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// --- Workspace Discovery ------------------------------------------------

func TestDiscoverNonexistentTarget(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nope")
	d, err := Discover(dir)
	if err != nil {
		t.Fatal(err)
	}
	if d.Exists {
		t.Error("target must not exist")
	}
	if d.BaseName != "nope" {
		t.Errorf("BaseName = %q, want %q", d.BaseName, "nope")
	}
}

func TestDiscoverEmptyDir(t *testing.T) {
	dir := t.TempDir()
	d, err := Discover(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !d.Exists || !d.IsDir {
		t.Error("temp dir must exist as a directory")
	}
	if d.IsGitRepo || d.HasReadme || d.HasDocs || d.IsEkaRepo {
		t.Errorf("empty dir must have no features: %+v", d)
	}
	if len(d.ConfigFiles) != 0 {
		t.Errorf("empty dir must have no config files: %v", d.ConfigFiles)
	}
}

func TestDiscoverGitRepoInTarget(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	d, err := Discover(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !d.IsGitRepo {
		t.Error("target with .git must be detected as a git repository")
	}
	if d.GitRoot != dir {
		t.Errorf("GitRoot = %q, want %q", d.GitRoot, dir)
	}
}

func TestDiscoverGitRepoInAncestor(t *testing.T) {
	parent := t.TempDir()
	if err := os.Mkdir(filepath.Join(parent, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(parent, "sub", "deeper")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}
	d, err := Discover(child)
	if err != nil {
		t.Fatal(err)
	}
	if !d.IsGitRepo {
		t.Error(".git in an ancestor must be detected")
	}
	if d.GitRoot != parent {
		t.Errorf("GitRoot = %q, want %q", d.GitRoot, parent)
	}
}

func TestDiscoverGitWorktreeFile(t *testing.T) {
	dir := t.TempDir()
	// A worktree has .git as a file, not a directory.
	if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: /x"), 0o644); err != nil {
		t.Fatal(err)
	}
	d, err := Discover(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !d.IsGitRepo {
		t.Error("a .git file must also count as a git repository")
	}
}

func TestDiscoverReadme(t *testing.T) {
	for _, name := range []string{"README.md", "README"} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, name), []byte("# hi"), 0o644); err != nil {
			t.Fatal(err)
		}
		d, err := Discover(dir)
		if err != nil {
			t.Fatal(err)
		}
		if !d.HasReadme || d.ReadmePath != name {
			t.Errorf("name %s: HasReadme=%v ReadmePath=%q", name, d.HasReadme, d.ReadmePath)
		}
	}
}

func TestDiscoverEkaRepoMarkers(t *testing.T) {
	// The legacy marker set (docs/ tree) without eka.yaml.
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "docs", "operating"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "docs", "exchange"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "docs", "exchange", "validation.md"), []byte("v"), 0o644)
	os.WriteFile(filepath.Join(dir, "docs", "exchange", "transfer.md"), []byte("t"), 0o644)

	d, err := Discover(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !d.HasDocs || !d.IsEkaRepo {
		t.Errorf("full marker set must be detected: %+v", d)
	}

	// Remove one marker: no longer an EKA repo.
	os.Remove(filepath.Join(dir, "docs", "exchange", "transfer.md"))
	d, err = Discover(dir)
	if err != nil {
		t.Fatal(err)
	}
	if d.IsEkaRepo {
		t.Error("missing transfer.md must disqualify the docs markers")
	}

	// eka.yaml alone marks a repository (ADR-018 Decision 3): a
	// metadata-only directory (eka.yaml, no complete docs tree) is an
	// EKA repository.
	os.WriteFile(filepath.Join(dir, "eka.yaml"), []byte("version: 1\nproject: p\nname: p\nnamespace: p\n"), 0o644)
	d, err = Discover(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !d.IsEkaRepo {
		t.Errorf("eka.yaml alone must mark the repository: %+v", d)
	}

	// A plain directory (no eka.yaml, no docs markers) is not an EKA
	// repository.
	plain := t.TempDir()
	d, err = Discover(plain)
	if err != nil {
		t.Fatal(err)
	}
	if d.IsEkaRepo {
		t.Errorf("plain dir must not be an EKA repository: %+v", d)
	}
}

func TestDiscoverConfigFiles(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{".gitignore", ".editorconfig", ".eka.json", "eka.yaml", "notes.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	d, err := Discover(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{".editorconfig", ".eka.json", ".gitignore", "eka.yaml"}
	if !reflect.DeepEqual(d.ConfigFiles, want) {
		t.Errorf("ConfigFiles = %v, want %v", d.ConfigFiles, want)
	}
}

func TestDiscoverTargetIsFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	d, err := Discover(file)
	if err != nil {
		t.Fatal(err)
	}
	if !d.Exists || d.IsDir {
		t.Errorf("file target: Exists=%v IsDir=%v", d.Exists, d.IsDir)
	}
}

// --- Wizard: identifier rules ------------------------------------------

func TestIsValidNamespace(t *testing.T) {
	valid := []string{"eka", "my-project", "a1b2", "x-y-9"}
	for _, s := range valid {
		if !isValidNamespace(s) {
			t.Errorf("isValidNamespace(%q) = false, want true", s)
		}
	}
	invalid := []string{"", "My-Project", "my_project", "my/project", "my:project", "my project", "my.project", "-edge", "a--b"}
	for _, s := range invalid {
		if isValidNamespace(s) {
			t.Errorf("isValidNamespace(%q) = true, want false", s)
		}
	}
}

// TestIsValidIdent pins the exported identifier rule against the
// regex ^[a-z0-9]+(-[a-z0-9]+)*$ (the single source of truth; the
// metadata package applies the same pattern by design).
func TestIsValidIdent(t *testing.T) {
	for _, s := range []string{"eka", "my-project", "a1b2", "x-y-9", "a-b-c"} {
		if !IsValidIdent(s) {
			t.Errorf("IsValidIdent(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"", "-edge", "edge-", "a--b", "My-Project", "my_project", "my/project", "my:project", "my project", "my.project", "a-"} {
		if IsValidIdent(s) {
			t.Errorf("IsValidIdent(%q) = true, want false", s)
		}
	}
}

func TestSanitizeNamespace(t *testing.T) {
	cases := map[string]string{
		"My Project":     "my-project",
		"Foo/Bar:Baz":    "foo-bar-baz",
		"  spaced  out ": "spaced-out",
		"already-good":   "already-good",
		"UPPER":          "upper",
		"a--b":           "a-b",
		"---":            "",
		"":               "",
		"123":            "123",
	}
	for in, want := range cases {
		if got := sanitizeNamespace(in); got != want {
			t.Errorf("sanitizeNamespace(%q) = %q, want %q", in, got, want)
		}
	}
}

// --- Wizard: adaptivity -------------------------------------------------

// TestNeededQuestions pins the fixed question set: the project id and
// the namespace are ALWAYS asked, in that order, followed by the git
// question when the target is not already a git repository and git is
// available. No other question exists in the identity-only wizard.
func TestNeededQuestions(t *testing.T) {
	base := &Discovery{BaseName: "my-project", HasReadme: true, IsGitRepo: true, GitAvailable: true}
	qs := NeededQuestions(base)
	if len(qs) != 2 {
		t.Fatalf("project + namespace questions expected, got %d: %v", len(qs), qs)
	}
	if qs[0].Kind != QProject || qs[0].Prompt != "Project id" {
		t.Errorf("first question must be the project id, got %+v", qs[0])
	}
	if qs[1].Kind != QNamespace {
		t.Errorf("second question must be the namespace, got %+v", qs[1])
	}
	// The defaults decouple the two only when the user overrides: by
	// default both derive from the sanitized basename.
	if qs[0].Default != "my-project" || qs[1].Default != "my-project" {
		t.Errorf("defaults must both be the sanitized basename, got %q/%q", qs[0].Default, qs[1].Default)
	}
}

// TestNeededQuestionsUnusableBaseName: even an unusable base name
// (filesystem root, empty) keeps the fixed project + namespace
// questions; their defaults fall back deterministically.
func TestNeededQuestionsUnusableBaseName(t *testing.T) {
	for _, base := range []string{"", "/"} {
		qs := NeededQuestions(&Discovery{BaseName: base, HasReadme: true, IsGitRepo: true})
		if len(qs) != 2 || qs[0].Kind != QProject || qs[1].Kind != QNamespace {
			t.Errorf("BaseName %q: project + namespace questions must always be asked, got %v", base, qs)
		}
		if qs[0].Default != fallbackName || qs[1].Default != fallbackName {
			t.Errorf("BaseName %q: defaults must fall back to %q, got %q/%q", base, fallbackName, qs[0].Default, qs[1].Default)
		}
	}
}

func TestNeededQuestionsGit(t *testing.T) {
	// No question when already a git repository.
	if has := hasQuestion(NeededQuestions(&Discovery{BaseName: "x", HasReadme: true, IsGitRepo: true, GitAvailable: true}), QGit); has {
		t.Error("git question must be absent inside an existing git repository")
	}
	// No question when git is not available.
	if has := hasQuestion(NeededQuestions(&Discovery{BaseName: "x", HasReadme: true, IsGitRepo: false, GitAvailable: false}), QGit); has {
		t.Error("git question must be absent when git is unavailable")
	}
	// Question asked only when both conditions allow it.
	if !hasQuestion(NeededQuestions(&Discovery{BaseName: "x", HasReadme: true, IsGitRepo: false, GitAvailable: true}), QGit) {
		t.Error("git question must be asked when git is available and no repo exists")
	}
}

// TestNeededQuestionsFixedOrder pins the ordering contract: project,
// then namespace, then git — the git question always comes last.
func TestNeededQuestionsFixedOrder(t *testing.T) {
	qs := NeededQuestions(&Discovery{BaseName: "x", HasReadme: true, IsGitRepo: false, GitAvailable: true})
	if len(qs) != 3 || qs[0].Kind != QProject || qs[1].Kind != QNamespace || qs[2].Kind != QGit {
		t.Errorf("question order must be project, namespace, git, got %v", qs)
	}
}

func hasQuestion(qs []Question, kind QuestionKind) bool {
	for _, q := range qs {
		if q.Kind == kind {
			return true
		}
	}
	return false
}

// --- Wizard: deterministic defaults -------------------------------------

func TestDefaultAnswers(t *testing.T) {
	d := &Discovery{BaseName: "my-project", HasReadme: false, IsGitRepo: false, GitAvailable: true}
	a := DefaultAnswers(d)
	if a.Project != "my-project" || a.Namespace != "my-project" {
		t.Errorf("defaults: %+v", a)
	}
	if a.InitGit {
		t.Error("non-interactive runs must never init git")
	}
	if a.Interactive {
		t.Error("defaults are non-interactive")
	}

	// A base name that needs sanitizing: both defaults derive from the
	// sanitized basename — equal BY DEFAULT, decoupled only when the
	// user overrides one.
	a = DefaultAnswers(&Discovery{BaseName: "My Project", HasReadme: true, IsGitRepo: true})
	if a.Project != "my-project" || a.Namespace != "my-project" {
		t.Errorf("sanitized defaults: %+v", a)
	}

	// Unusable base name falls back deterministically.
	a = DefaultAnswers(&Discovery{BaseName: "", HasReadme: true, IsGitRepo: true})
	if a.Project != fallbackName || a.Namespace != fallbackName {
		t.Errorf("fallback defaults: %+v", a)
	}
}

// --- Wizard: interactive Ask --------------------------------------------

// TestAskPipedAnswers: the project id is validated (invalid answers are
// re-prompted) and the namespace defaults to the answered project id.
func TestAskPipedAnswers(t *testing.T) {
	d := &Discovery{BaseName: "My Project", HasReadme: false, IsGitRepo: false, GitAvailable: true}
	// Answers: project id (first invalid, then valid), namespace, git no.
	input := "bad_project\natrium\natrium-api\nn\n"
	var out strings.Builder
	a, err := Ask(d, strings.NewReader(input), &out, PreAnswers{})
	if err != nil {
		t.Fatal(err)
	}
	if a.Project != "atrium" {
		t.Errorf("Project = %q, want atrium (invalid answer must be re-prompted)", a.Project)
	}
	if a.Namespace != "atrium-api" {
		t.Errorf("Namespace = %q, want atrium-api", a.Namespace)
	}
	if a.InitGit {
		t.Error("InitGit must honor the 'n' answer")
	}
	if !strings.Contains(out.String(), "invalid project id") {
		t.Errorf("re-prompt message expected in output:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "Project id") {
		t.Errorf("project prompt must be printed:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "Namespace") {
		t.Errorf("namespace prompt must be printed:\n%s", out.String())
	}
}

// TestAskNamespaceDefaultsToProjectAnswer pins the sequential
// adaptivity: an empty namespace answer adopts the answered project id
// (the namespace default is the project answer, not the discovery
// default).
func TestAskNamespaceDefaultsToProjectAnswer(t *testing.T) {
	d := &Discovery{BaseName: "discovery-name", HasReadme: true, IsGitRepo: true, GitAvailable: true}
	// Project overridden to atrium; namespace left empty.
	input := "atrium\n\n"
	var out strings.Builder
	a, err := Ask(d, strings.NewReader(input), &out, PreAnswers{})
	if err != nil {
		t.Fatal(err)
	}
	if a.Project != "atrium" {
		t.Errorf("Project = %q, want atrium", a.Project)
	}
	if a.Namespace != "atrium" {
		t.Errorf("Namespace = %q, want the answered project id atrium", a.Namespace)
	}
}

// TestAskDefaultsByDiscovery: with no answers at all (EOF), the wizard
// falls back to the deterministic discovery defaults.
func TestAskEofFallsBackToDefaults(t *testing.T) {
	d := &Discovery{BaseName: "my-project", HasReadme: true, IsGitRepo: true, GitAvailable: true}
	a, err := Ask(d, strings.NewReader(""), &strings.Builder{}, PreAnswers{})
	if err != nil {
		t.Fatal(err)
	}
	want := DefaultAnswers(d)
	if a.Project != want.Project || a.Namespace != want.Namespace {
		t.Errorf("EOF must yield defaults, got %+v", a)
	}
}

// --- Wizard: flag pre-answers -------------------------------------------

// TestAskPreAnswersFixAnswers: a non-empty PreAnswers value fixes the
// answer and the corresponding question is skipped — no prompt is
// printed for it.
func TestAskPreAnswersFixAnswers(t *testing.T) {
	d := &Discovery{BaseName: "discovery-name", HasReadme: true, IsGitRepo: true, GitAvailable: true}
	var out strings.Builder
	// Only the git answer is supplied; project/namespace are fixed.
	a, err := Ask(d, strings.NewReader("n\n"), &out, PreAnswers{Project: "atrium", Namespace: "atrium-api"})
	if err != nil {
		t.Fatal(err)
	}
	if a.Project != "atrium" || a.Namespace != "atrium-api" {
		t.Errorf("fixed answers must win: %+v", a)
	}
	text := out.String()
	if strings.Contains(text, "Project id") {
		t.Errorf("project question must be skipped when pre-answered:\n%s", text)
	}
	if strings.Contains(text, "Namespace") {
		t.Errorf("namespace question must be skipped when pre-answered:\n%s", text)
	}
}

// TestAskPreAnswersProjectOnly: fixing only the project id still asks
// the namespace question, with the fixed project as its default.
func TestAskPreAnswersProjectOnly(t *testing.T) {
	d := &Discovery{BaseName: "discovery-name", HasReadme: true, IsGitRepo: true, GitAvailable: true}
	var out strings.Builder
	// Namespace answered explicitly.
	a, err := Ask(d, strings.NewReader("atrium-api\n"), &out, PreAnswers{Project: "atrium"})
	if err != nil {
		t.Fatal(err)
	}
	if a.Project != "atrium" || a.Namespace != "atrium-api" {
		t.Errorf("answers: %+v", a)
	}
	if !strings.Contains(out.String(), "Namespace") {
		t.Errorf("namespace question must still be asked:\n%s", out.String())
	}
	// Leaving the namespace empty adopts the fixed project id.
	a, err = Ask(d, strings.NewReader("\n"), &out, PreAnswers{Project: "atrium"})
	if err != nil {
		t.Fatal(err)
	}
	if a.Namespace != "atrium" {
		t.Errorf("Namespace = %q, want the fixed project id atrium", a.Namespace)
	}
}

// TestRunInvalidFlagValues: invalid --project/--namespace flag values
// are usage errors at Run (the wizard's re-prompt loop would enforce
// the same rule interactively).
func TestRunInvalidFlagValues(t *testing.T) {
	for _, tc := range []struct {
		name  string
		opts  Options
		embed string
	}{
		{"invalid project", Options{Project: "Invalid_Id"}, "invalid project id"},
		{"invalid namespace", Options{Namespace: "bad_namespace"}, "invalid namespace"},
		{"invalid project from discovery default path", Options{Project: "-edge"}, "invalid project id"},
	} {
		dir := t.TempDir()
		opts, _, _ := runOpts(dir, "")
		opts.Project = tc.opts.Project
		opts.Namespace = tc.opts.Namespace
		if _, err := Run(opts); err == nil {
			t.Errorf("%s: Run must reject the invalid flag value", tc.name)
		} else if !strings.Contains(err.Error(), tc.embed) {
			t.Errorf("%s: error must explain the invalid value, got %v", tc.name, err)
		}
		// Nothing may be written for an invalid identity.
		if entries, _ := os.ReadDir(dir); len(entries) != 0 {
			t.Errorf("%s: invalid flags must not write anything, found %v", tc.name, entries)
		}
	}
}

// TestRunFlagPreAnswersFixAnswers: non-empty Options.Project/Namespace
// fix the answers end-to-end on the default path — the wizard is
// skipped (non-interactive stdin), yet the identity carries the flags.
func TestRunFlagPreAnswersFixAnswers(t *testing.T) {
	dir := t.TempDir()
	opts, _, _ := runOpts(dir, "")
	opts.Project = "atrium"
	opts.Namespace = "atrium-api"
	outcome, err := Run(opts)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Project != "atrium" || outcome.Namespace != "atrium-api" {
		t.Errorf("Outcome: %+v", outcome)
	}
	eka, err := os.ReadFile(filepath.Join(dir, "eka.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if want := "version: 1\nproject: atrium\nname: " + filepath.Base(dir) + "\nnamespace: atrium-api\ncapture:\n  enabled: true\n  threshold: 0.6\n  dedupeWindow: 24h\n  provenanceFilterDefault: all\n"; string(eka) != want {
		t.Errorf("eka.yaml bytes:\ngot:  %q\nwant: %q", eka, want)
	}
}
