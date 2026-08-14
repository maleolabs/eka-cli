package bootstrap

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/maleolabs/eka-core/conformance"
	"github.com/maleolabs/eka-core/metadata"
)

// runOpts builds Options with captured buffers and a non-interactive stdin.
func runOpts(target string, stdin string) (Options, *bytes.Buffer, *bytes.Buffer) {
	var out, errb bytes.Buffer
	return Options{
		Target: target,
		Stdin:  strings.NewReader(stdin),
		Stdout: &out,
		Stderr: &errb,
	}, &out, &errb
}

// contains reports whether xs contains s.
func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// walkFiles returns sorted relative file paths under dir.
func walkFiles(t *testing.T, dir string) []string {
	t.Helper()
	var files []string
	filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			rel, err := filepath.Rel(dir, path)
			if err != nil {
				return err
			}
			files = append(files, filepath.ToSlash(rel))
		}
		return nil
	})
	return files
}

// Scenario 1: empty directory init. Answers are piped via strings.Reader,
// but a non-terminal stdin means the wizard is skipped: the piped answers
// must be ignored and deterministic defaults used.
func TestRunEmptyDirectory(t *testing.T) {
	dir := t.TempDir()
	// Answers that would change the outcome if they were consumed.
	opts, _, _ := runOpts(dir, "piped-name\npiped-ns\npiped description\ny\ny\n")
	outcome, err := Run(opts)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Project != filepath.Base(dir) {
		t.Errorf("Project = %q, want %q (piped answers must be ignored)", outcome.Project, filepath.Base(dir))
	}
	if outcome.Namespace != filepath.Base(dir) {
		t.Errorf("Namespace = %q, want %q", outcome.Namespace, filepath.Base(dir))
	}
	if outcome.RepoType != "existing-dir" {
		t.Errorf("RepoType = %q, want existing-dir", outcome.RepoType)
	}
	if outcome.Report == nil || !outcome.Report.Pass() {
		t.Fatalf("generated repo must validate, report: %+v", outcome.Report)
	}
	// Identity-only: exactly eka.yaml is created, nothing else.
	if len(outcome.CreatedFiles) != 1 || !contains(outcome.CreatedFiles, "eka.yaml") {
		t.Errorf("created files must be exactly [eka.yaml], got %v", outcome.CreatedFiles)
	}
	// The generated eka.yaml parses and carries the repository
	// identity: project == namespace == wizard namespace, name == the
	// target directory basename.
	eka, err := os.ReadFile(filepath.Join(dir, "eka.yaml"))
	if err != nil {
		t.Fatalf("eka.yaml missing: %v", err)
	}
	m, err := metadata.Parse(eka)
	if err != nil {
		t.Fatalf("generated eka.yaml must parse: %v\n%s", err, eka)
	}
	if m.Name != filepath.Base(dir) {
		t.Errorf("eka.yaml name = %q, want %q (target basename)", m.Name, filepath.Base(dir))
	}
	if m.Project != outcome.Namespace || m.Namespace != outcome.Namespace {
		t.Errorf("eka.yaml project/namespace = %q/%q, want %q", m.Project, m.Namespace, outcome.Namespace)
	}
	if len(outcome.CreatedDirs) != 0 {
		t.Errorf("existing target must not create dirs, got %v", outcome.CreatedDirs)
	}
	if outcome.GitStatus != "skipped (non-interactive mode)" {
		t.Errorf("GitStatus = %q, want skipped (non-interactive mode)", outcome.GitStatus)
	}
	if len(outcome.SkippedFiles) != 0 || len(outcome.OverwrittenFiles) != 0 {
		t.Errorf("empty dir must not skip or overwrite anything: %+v", outcome)
	}
}

// Scenario 2: existing non-empty directory without EKA is adopted; custom
// files survive and no existing file is replaced silently.
func TestRunAdoptsExistingDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("keep me"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "docs", "exchange"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "docs", "exchange", "validation.md"), []byte("custom"), 0o644); err != nil {
		t.Fatal(err)
	}

	opts, _, _ := runOpts(dir, "")
	outcome, err := Run(opts)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.RepoType != "existing-dir" {
		t.Errorf("RepoType = %q, want existing-dir", outcome.RepoType)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "notes.txt")); string(data) != "keep me" {
		t.Error("custom file must be preserved")
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "docs", "exchange", "validation.md")); string(data) != "custom" {
		t.Error("existing docs file must be left alone (never merged, never scaffolded)")
	}
	// Nothing is planned for the existing docs file: no write, no skip.
	if contains(outcome.SkippedFiles, "docs/exchange/validation.md") {
		t.Errorf("existing docs file must not be planned at all, got skipped: %v", outcome.SkippedFiles)
	}
	if !contains(outcome.CreatedFiles, "eka.yaml") {
		t.Errorf("eka.yaml must be created, got %v", outcome.CreatedFiles)
	}
	if outcome.Report == nil || !outcome.Report.Pass() {
		t.Errorf("adopted repo must still validate: %+v", outcome.Report)
	}
}

// Scenario 3: existing git repository → no git question, no git init.
func TestRunExistingGitRepository(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	gitCalled := false
	opts, _, _ := runOpts(dir, "")
	opts.GitInit = func(d string, stdout, stderr io.Writer) error {
		gitCalled = true
		return nil
	}
	outcome, err := Run(opts)
	if err != nil {
		t.Fatal(err)
	}
	if gitCalled {
		t.Error("git init must not run inside an existing git repository")
	}
	if outcome.GitStatus != "existing" {
		t.Errorf("GitStatus = %q, want existing", outcome.GitStatus)
	}
	// The wizard must not offer the git question.
	d, err := Discover(dir)
	if err != nil {
		t.Fatal(err)
	}
	if hasQuestion(NeededQuestions(d), QGit) {
		t.Error("git question must be absent inside an existing git repository")
	}
	if outcome.Report == nil || !outcome.Report.Pass() {
		t.Error("repo must validate")
	}
}

// Scenario 4: an existing README is left alone — `eka init` never
// touches it (identity-only; README generation no longer exists).
func TestRunPreservesExistingReadme(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Mine\n\ncustom\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	opts, _, _ := runOpts(dir, "")
	outcome, err := Run(opts)
	if err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "README.md")); string(data) != "# Mine\n\ncustom\n" {
		t.Error("existing README must be preserved")
	}
	// No README may be planned in any direction: no write, no reuse,
	// no skip, no overwrite.
	for _, group := range [][]string{outcome.CreatedFiles, outcome.ReusedFiles, outcome.SkippedFiles, outcome.OverwrittenFiles} {
		if contains(group, "README.md") {
			t.Errorf("README.md must not appear in any plan group, got %v", group)
		}
	}
	// The README question no longer exists in the identity-only wizard.
	if hasQuestion(NeededQuestions(&Discovery{BaseName: filepath.Base(dir)}), "readme") {
		t.Error("no readme question may exist in the identity-only wizard")
	}
}

// Scenario 5: existing EKA repository (docs markers, no eka.yaml) →
// adoption: reuse + generate eka.yaml + validate, eka.yaml created with
// the deterministic default identity, nothing else written.
func TestRunExistingEkaRepo(t *testing.T) {
	dir := t.TempDir()
	makeEkaRepo(t, dir)
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	opts, out, _ := runOpts(dir, "")
	outcome, err := Run(opts)
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.AlreadyInitialized {
		t.Error("existing EKA repo must be detected as already initialized")
	}
	if outcome.RepoType != "existing-eka" {
		t.Errorf("RepoType = %q, want existing-eka", outcome.RepoType)
	}
	if len(outcome.Plan) != 3 {
		t.Errorf("plan must be reuse + generate eka.yaml + validate, got %d actions", len(outcome.Plan))
	}
	if len(outcome.CreatedDirs) != 0 || len(outcome.OverwrittenFiles) != 0 || len(outcome.SkippedFiles) != 0 {
		t.Errorf("adoption must create no dirs and skip/overwrite nothing: %+v", outcome)
	}
	if !reflect.DeepEqual(outcome.CreatedFiles, []string{"eka.yaml"}) {
		t.Errorf("adoption must create exactly eka.yaml, got %v", outcome.CreatedFiles)
	}
	// The adopted identity file exists, parses and carries the
	// deterministic default identity: name == basename, project ==
	// namespace == the default project derived from the basename.
	eka, err := os.ReadFile(filepath.Join(dir, "eka.yaml"))
	if err != nil {
		t.Fatalf("adopted eka.yaml missing: %v", err)
	}
	m, err := metadata.Parse(eka)
	if err != nil {
		t.Fatalf("adopted eka.yaml must parse: %v\n%s", err, eka)
	}
	if m.Name != filepath.Base(dir) {
		t.Errorf("eka.yaml name = %q, want %q (target basename)", m.Name, filepath.Base(dir))
	}
	want := DefaultAnswers(&Discovery{BaseName: filepath.Base(dir)})
	if m.Project != want.Project || m.Namespace != want.Namespace {
		t.Errorf("eka.yaml project/namespace = %q/%q, want %q", m.Project, m.Namespace, want.Project)
	}
	// The run reports the same identity it wrote (editable in eka.yaml
	// before the first sync).
	if outcome.Identity == nil {
		t.Fatal("adoption run must report the repository identity")
	}
	if outcome.Identity.Name != filepath.Base(dir) || outcome.Identity.Project != want.Project || outcome.Identity.Namespace != want.Namespace {
		t.Errorf("Identity = %+v, want name %q project/namespace %q", outcome.Identity, filepath.Base(dir), want.Project)
	}
	if outcome.GitStatus != "existing" {
		t.Errorf("GitStatus = %q, want existing (discovered .git)", outcome.GitStatus)
	}
	if outcome.Report == nil || !outcome.Report.Pass() {
		t.Error("existing repo must validate")
	}
	// No wizard prompts may be emitted for an already-initialized repo.
	if out.Len() != 0 {
		t.Errorf("already-initialized repo must not prompt, got output:\n%s", out.String())
	}
}

func TestRunExistingEkaRepoWithoutGit(t *testing.T) {
	dir := t.TempDir()
	makeEkaRepo(t, dir)
	opts, _, _ := runOpts(dir, "")
	outcome, err := Run(opts)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.GitStatus != "skipped (no git action planned)" {
		t.Errorf("GitStatus = %q, want skipped (no git action planned)", outcome.GitStatus)
	}
	// The adoption run still creates eka.yaml without git.
	if _, err := os.Stat(filepath.Join(dir, "eka.yaml")); err != nil {
		t.Errorf("adoption must create eka.yaml: %v", err)
	}
	if !contains(outcome.CreatedFiles, "eka.yaml") {
		t.Errorf("eka.yaml must be in the created files, got %v", outcome.CreatedFiles)
	}
}

// Scenario 6: a metadata-only repository (eka.yaml present, no docs/
// tree) is already initialized (ADR-018 Decision 3): the run is a
// reuse-only no-op and NEVER scaffolds the legacy docs skeleton into a
// v2 repository. Validation skips cleanly (no docs/ knowledge tree).
func TestRunMetadataOnlyRepoIsNoop(t *testing.T) {
	dir := t.TempDir()
	d := &Discovery{AbsTarget: dir, BaseName: filepath.Base(dir)}
	identity := generatedEkaYAML(d, DefaultAnswers(d))
	if err := os.WriteFile(filepath.Join(dir, "eka.yaml"), identity, 0o644); err != nil {
		t.Fatal(err)
	}
	opts, out, _ := runOpts(dir, "")
	outcome, err := Run(opts)
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.AlreadyInitialized {
		t.Error("metadata-only repo must be detected as already initialized")
	}
	if outcome.RepoType != "existing-eka" {
		t.Errorf("RepoType = %q, want existing-eka", outcome.RepoType)
	}
	if len(outcome.Plan) != 3 {
		t.Errorf("plan must be reuse + reuse eka.yaml + validate, got %d actions", len(outcome.Plan))
	}
	if outcome.Plan[1].Kind != ActionReuse || outcome.Plan[1].Path != "eka.yaml" {
		t.Errorf("existing eka.yaml must be planned as reuse, got %+v", outcome.Plan[1])
	}
	// No legacy skeleton may be scaffolded into a v2 repository.
	if _, err := os.Stat(filepath.Join(dir, "docs")); !os.IsNotExist(err) {
		t.Error("metadata-only repo must not gain a docs/ tree")
	}
	if len(outcome.CreatedDirs) != 0 || len(outcome.CreatedFiles) != 0 ||
		len(outcome.OverwrittenFiles) != 0 || len(outcome.SkippedFiles) != 0 {
		t.Errorf("no-op run must write nothing: %+v", outcome)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "eka.yaml")); !bytes.Equal(data, identity) {
		t.Error("existing eka.yaml must be reused byte-identically")
	}
	// Validation skips cleanly: no docs/ knowledge tree.
	if outcome.Report == nil || !outcome.Report.Pass() {
		t.Fatalf("skipped validation must pass, report: %+v", outcome.Report)
	}
	if outcome.Report.FilesScanned != 0 {
		t.Errorf("FilesScanned = %d, want 0 (docs/ absent)", outcome.Report.FilesScanned)
	}
	if outcome.Identity != nil {
		t.Error("a reuse-only run must not report a generated identity")
	}
	if out.Len() != 0 {
		t.Errorf("already-initialized repo must not prompt, got output:\n%s", out.String())
	}
}

// TestRunAdoptionDeterministicIdentity: a legacy docs-marked repository
// whose basename needs sanitization adopts eka.yaml whose identity is
// fully deterministic — project == namespace == the sanitized default
// namespace, name == the sanitized basename.
func TestRunAdoptionDeterministicIdentity(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "My Repo")
	if err := os.MkdirAll(filepath.Join(dir, "docs", "operating"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "docs", "exchange"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "docs", "exchange", "validation.md"), []byte("# v"), 0o644)
	os.WriteFile(filepath.Join(dir, "docs", "exchange", "transfer.md"), []byte("# t"), 0o644)

	opts, _, _ := runOpts(dir, "")
	outcome, err := Run(opts)
	if err != nil {
		t.Fatal(err)
	}
	eka, err := os.ReadFile(filepath.Join(dir, "eka.yaml"))
	if err != nil {
		t.Fatalf("adopted eka.yaml missing: %v", err)
	}
	m, err := metadata.Parse(eka)
	if err != nil {
		t.Fatalf("adopted eka.yaml must parse: %v\n%s", err, eka)
	}
	if m.Name != "my-repo" {
		t.Errorf("name = %q, want the sanitized basename my-repo", m.Name)
	}
	if m.Project != "my-repo" || m.Namespace != "my-repo" {
		t.Errorf("project/namespace = %q/%q, want the sanitized default namespace my-repo", m.Project, m.Namespace)
	}
	if outcome.Identity == nil || outcome.Identity.Name != "my-repo" || outcome.Identity.Project != "my-repo" {
		t.Errorf("Identity = %+v, want name/project my-repo", outcome.Identity)
	}
	if outcome.Report == nil || !outcome.Report.Pass() {
		t.Errorf("adopted repo must validate: %+v", outcome.Report)
	}
}

func makeEkaRepo(t *testing.T, dir string) {
	t.Helper()
	os.MkdirAll(filepath.Join(dir, "docs", "operating"), 0o755)
	os.MkdirAll(filepath.Join(dir, "docs", "exchange"), 0o755)
	os.WriteFile(filepath.Join(dir, "docs", "exchange", "validation.md"), []byte("# v"), 0o644)
	os.WriteFile(filepath.Join(dir, "docs", "exchange", "transfer.md"), []byte("# t"), 0o644)
}

// Scenario 7: repeated initialization — the second run is a no-op and the
// repository still validates.
func TestRunTwiceIsNoop(t *testing.T) {
	dir := t.TempDir()
	opts, _, _ := runOpts(dir, "")
	if _, err := Run(opts); err != nil {
		t.Fatal(err)
	}
	before := walkFiles(t, dir)
	second, err := Run(opts)
	if err != nil {
		t.Fatal(err)
	}
	if !second.AlreadyInitialized {
		t.Error("second run must detect the existing EKA repository")
	}
	if len(second.CreatedFiles) != 0 || len(second.OverwrittenFiles) != 0 || len(second.SkippedFiles) != 0 {
		t.Errorf("second run must write nothing: %+v", second)
	}
	after := walkFiles(t, dir)
	if !reflect.DeepEqual(before, after) {
		t.Errorf("second run changed the tree:\nbefore: %v\nafter:  %v", before, after)
	}
	if second.Report == nil || !second.Report.Pass() {
		t.Error("repo must still validate after the second run")
	}
}

// Scenario 8: dry-run mode — plan printed, nothing written, exit path 0.
func TestRunDryRun(t *testing.T) {
	dir := t.TempDir()
	opts, _, _ := runOpts(dir, "")
	opts.DryRun = true
	outcome, err := Run(opts)
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.DryRun {
		t.Error("outcome must mark the dry run")
	}
	if outcome.Report != nil {
		t.Error("dry run must not validate")
	}
	if len(outcome.Plan) == 0 {
		t.Fatal("dry run must produce a plan")
	}
	// Stable ordering: the plan starts with the identity file (the
	// target exists) and ends with validate.
	if outcome.Plan[len(outcome.Plan)-1].Kind != ActionValidate {
		t.Errorf("plan must end with validate, got %+v", outcome.Plan[len(outcome.Plan)-1])
	}
	first := outcome.Plan[0]
	if first.Kind != ActionGenerateEkaYAML {
		t.Errorf("plan must start with the eka.yaml generation, got %+v", first)
	}
	// No writes: the target still has no files.
	if got := walkFiles(t, dir); len(got) != 0 {
		t.Errorf("dry run must not write anything, found: %v", got)
	}
}

// Scenario 9: failed validation. Unit level: injected failing validator
// surfaces through Run and through RunValidation.
func TestRunValidationFailingValidator(t *testing.T) {
	dir := t.TempDir()
	fail := conformance.Report{
		Root: dir,
		Results: []conformance.Result{{
			Severity: conformance.SeverityError,
			Rule:     conformance.RuleStructural,
			File:     "docs/x.md",
			Message:  "injected failure",
		}},
	}
	opts, _, _ := runOpts(dir, "")
	opts.Validate = func(root string) (*conformance.Report, error) { return &fail, nil }
	outcome, err := Run(opts)
	if err != nil {
		t.Fatalf("a failing validation must not be a Run error: %v", err)
	}
	if outcome.Report == nil || outcome.Report.Pass() {
		t.Error("outcome must carry the failing report")
	}

	// The stage component itself.
	report, err := RunValidation(dir, func(root string) (*conformance.Report, error) { return &fail, nil })
	if err != nil {
		t.Fatal(err)
	}
	if report.Pass() || report.ErrorCount() != 1 {
		t.Errorf("RunValidation must propagate the failing report: %+v", report)
	}
}

// Scenario 10: successful validation on the default path.
func TestRunSuccessfulValidation(t *testing.T) {
	dir := t.TempDir()
	opts, _, _ := runOpts(dir, "")
	outcome, err := Run(opts)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Report == nil {
		t.Fatal("validation must run after generation")
	}
	if !outcome.Report.Pass() {
		t.Errorf("identity-only repo must validate, got %d errors", outcome.Report.ErrorCount())
	}
	// The conformance scan covers the docs/ knowledge tree only
	// (ADR-018 Decision 2): an identity-only repository has no docs/
	// tree, so validation is skipped with zero files scanned.
	if outcome.Report.FilesScanned != 0 {
		t.Errorf("scanned %d files, want 0 (no docs/ tree)", outcome.Report.FilesScanned)
	}
	if outcome.Report.Artifacts != 0 {
		t.Errorf("identity-only repo must contain no artifacts, got %d", outcome.Report.Artifacts)
	}
	// Exactly eka.yaml was created.
	if !reflect.DeepEqual(outcome.CreatedFiles, []string{"eka.yaml"}) {
		t.Errorf("created files must be exactly [eka.yaml], got %v", outcome.CreatedFiles)
	}
}

// --- Mode tests: eka init / eka init . / eka init <name> ----------------

func TestRunTargetCurrentDir(t *testing.T) {
	dir := t.TempDir()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(old)
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"", "."} {
		opts, _, _ := runOpts(target, "")
		outcome, err := Run(opts)
		if err != nil {
			t.Fatalf("target %q: %v", target, err)
		}
		if outcome.Report == nil || !outcome.Report.Pass() {
			t.Errorf("target %q: must validate", target)
		}
		// An empty target normalizes to ".".
		want := target
		if want == "" {
			want = "."
		}
		if outcome.Target != want {
			t.Errorf("Target = %q, want %q", outcome.Target, want)
		}
	}
}

func TestRunCreatesNewProjectDir(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, "myproj")
	opts, _, _ := runOpts(target, "")
	outcome, err := Run(opts)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.RepoType != "new" {
		t.Errorf("RepoType = %q, want new", outcome.RepoType)
	}
	if _, err := os.Stat(filepath.Join(target, "eka.yaml")); err != nil {
		t.Errorf("project dir must be created and bootstrapped: %v", err)
	}
	// The target dir itself must not be nested (eka-named/eka-named bug).
	if info, err := os.Stat(filepath.Join(target, "myproj")); err == nil && info.IsDir() {
		t.Error("target dir must not be nested inside itself")
	}
	if outcome.Project != "myproj" {
		t.Errorf("Project = %q, want myproj", outcome.Project)
	}
}

func TestRunTargetIsFileError(t *testing.T) {
	file := filepath.Join(t.TempDir(), "f")
	os.WriteFile(file, []byte("x"), 0o644)
	opts, _, _ := runOpts(file, "")
	if _, err := Run(opts); err == nil {
		t.Error("file target must be an error")
	}
}

// TestRunTargetNamedDocs guards against the target-name/dir collision:
// `eka init docs` must create docs/ with the identity file INSIDE it,
// not misplace anything at docs/docs.
func TestRunTargetNamedDocs(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, "docs")
	opts, _, _ := runOpts(target, "")
	outcome, err := Run(opts)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.RepoType != "new" {
		t.Errorf("RepoType = %q, want new", outcome.RepoType)
	}
	if _, err := os.Stat(filepath.Join(target, "eka.yaml")); err != nil {
		t.Errorf("identity file must live at the target root: %v", err)
	}
	if info, err := os.Stat(filepath.Join(target, "docs", "docs")); err == nil && info.IsDir() {
		t.Error("no double-nesting allowed")
	}
	if outcome.Report == nil || !outcome.Report.Pass() {
		t.Error("repo must validate")
	}
}

// --- Identity-only contract ---------------------------------------------

// TestRunIdentityOnly pins the Phase B1 contract: the generated tree is
// eka.yaml only — no docs/ tree, no README, no skeleton file copies.
// Flags fix the identity; defaults equal the sanitized basename.
func TestRunIdentityOnly(t *testing.T) {
	// Defaults: project == namespace == the sanitized basename.
	dir := t.TempDir()
	opts, _, _ := runOpts(dir, "")
	outcome, err := Run(opts)
	if err != nil {
		t.Fatal(err)
	}
	if got := walkFiles(t, dir); !reflect.DeepEqual(got, []string{"eka.yaml"}) {
		t.Errorf("generated tree must be eka.yaml only, got %v", got)
	}
	if outcome.Project != filepath.Base(dir) || outcome.Namespace != filepath.Base(dir) {
		t.Errorf("default identity: project/namespace = %q/%q, want %q", outcome.Project, outcome.Namespace, filepath.Base(dir))
	}
	eka, err := os.ReadFile(filepath.Join(dir, "eka.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	m, err := metadata.Parse(eka)
	if err != nil {
		t.Fatalf("generated eka.yaml must parse: %v\n%s", err, eka)
	}
	if m.Project != filepath.Base(dir) || m.Namespace != filepath.Base(dir) || m.Name != filepath.Base(dir) {
		t.Errorf("default identity triple = %q/%q/%q, want %q", m.Project, m.Namespace, m.Name, filepath.Base(dir))
	}

	// Flags fix the identity on the default path.
	flagged := t.TempDir()
	opts, _, _ = runOpts(flagged, "")
	opts.Project = "atrium"
	opts.Namespace = "atrium-api"
	outcome, err = Run(opts)
	if err != nil {
		t.Fatal(err)
	}
	eka, err = os.ReadFile(filepath.Join(flagged, "eka.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	m, err = metadata.Parse(eka)
	if err != nil {
		t.Fatalf("flagged eka.yaml must parse: %v\n%s", err, eka)
	}
	if m.Project != "atrium" || m.Namespace != "atrium-api" {
		t.Errorf("flagged identity = %q/%q, want atrium/atrium-api", m.Project, m.Namespace)
	}
	if m.Name != filepath.Base(flagged) {
		t.Errorf("name = %q, want the target basename %q", m.Name, filepath.Base(flagged))
	}
	if outcome.Project != "atrium" || outcome.Namespace != "atrium-api" {
		t.Errorf("outcome identity = %q/%q, want atrium/atrium-api", outcome.Project, outcome.Namespace)
	}
	if outcome.Identity == nil || outcome.Identity.Project != "atrium" || outcome.Identity.Namespace != "atrium-api" {
		t.Errorf("reported identity must carry the flags: %+v", outcome.Identity)
	}

	// A basename that needs sanitizing: the defaults derive from the
	// sanitized basename.
	sanitized := filepath.Join(t.TempDir(), "My Repo")
	if err := os.MkdirAll(sanitized, 0o755); err != nil {
		t.Fatal(err)
	}
	opts, _, _ = runOpts(sanitized, "")
	outcome, err = Run(opts)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Project != "my-repo" || outcome.Namespace != "my-repo" {
		t.Errorf("sanitized defaults = %q/%q, want my-repo", outcome.Project, outcome.Namespace)
	}
}

// TestRunLeavesExistingDocsUntouched pins the adoption rule of Phase
// B1: an existing docs/ tree in an adopted directory is LEFT ALONE —
// never merged, never scaffolded, never modified. Only eka.yaml is
// generated.
func TestRunLeavesExistingDocsUntouched(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "docs", "custom"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "docs", "custom", "mine.md"), []byte("# mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "docs", "exchange"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "docs", "exchange", "validation.md"), []byte("custom"), 0o644); err != nil {
		t.Fatal(err)
	}
	opts, _, _ := runOpts(dir, "")
	outcome, err := Run(opts)
	if err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "docs", "custom", "mine.md")); string(data) != "# mine" {
		t.Error("custom docs file must be preserved")
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "docs", "exchange", "validation.md")); string(data) != "custom" {
		t.Error("existing docs file must be preserved byte-identically")
	}
	// No skeleton scaffolding into the docs tree.
	if _, err := os.Stat(filepath.Join(dir, "docs", "operating")); !os.IsNotExist(err) {
		t.Error("existing docs/ must not be scaffolded with skeleton dirs")
	}
	// No docs/ path may appear in any plan group: the tree is never
	// merged, never planned.
	for _, group := range [][]string{outcome.CreatedFiles, outcome.ReusedFiles, outcome.SkippedFiles, outcome.OverwrittenFiles, outcome.CreatedDirs} {
		for _, p := range group {
			if strings.HasPrefix(p, "docs") {
				t.Errorf("no docs/ path may be planned, got %q in %v", p, group)
			}
		}
	}
	// The identity file is still generated.
	if _, err := os.Stat(filepath.Join(dir, "eka.yaml")); err != nil {
		t.Errorf("eka.yaml must be generated: %v", err)
	}
	// The legacy tree is scanned as-is and must not block the run.
	if outcome.Report == nil || !outcome.Report.Pass() {
		t.Errorf("untouched docs tree must not block validation: %+v", outcome.Report)
	}
}

// --- Non-interactive determinism ----------------------------------------

func TestRunDeterministicAcrossPipedInput(t *testing.T) {
	dir := t.TempDir()
	runOnce := func(stdin string) string {
		opts, out, _ := runOpts(dir, stdin)
		if _, err := Run(opts); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}
	// Different piped answers must not change non-interactive behavior.
	a := runOnce("first-answer\nsecond-answer\n")
	b := runOnce("")
	if a != b {
		t.Error("non-interactive runs must be byte-identical regardless of piped input")
	}
}

// TestRunStdinDevNullNonInteractive is the regression test for the
// char-device bug: /dev/null is a char device, so the old ModeCharDevice
// heuristic misclassified it as interactive, printed prompts to stdout and
// ran `git init` after EOF fell back to the default "y". A true terminal
// check (term.IsTerminal) must classify /dev/null as non-interactive: no
// prompts, deterministic defaults, no .git directory, exit path success.
func TestRunStdinDevNullNonInteractive(t *testing.T) {
	if runtime.GOOS == "windows" {
		// /dev/null does not exist on Windows; the bug only manifests on
		// Unix char devices. The pipe-based test below keeps coverage on
		// Windows-compatible platforms.
		t.Skip("os.Open(/dev/null) is Unix-only")
	}
	devnull, err := os.Open("/dev/null")
	if err != nil {
		t.Skipf("cannot open /dev/null: %v", err)
	}
	defer devnull.Close()

	dir := t.TempDir()
	var out bytes.Buffer
	opts := Options{
		Target: dir,
		Stdin:  devnull,
		Stdout: &out,
		Stderr: io.Discard,
	}
	outcome, err := Run(opts)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.GitStatus != "skipped (non-interactive mode)" {
		t.Errorf("GitStatus = %q, want skipped (non-interactive mode): git init must never run for /dev/null stdin", outcome.GitStatus)
	}
	for _, a := range outcome.Plan {
		if a.Kind == ActionGitInit {
			t.Error("non-interactive run must not plan git init")
		}
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); !os.IsNotExist(err) {
		t.Errorf(".git must not exist after `eka init < /dev/null`, stat err = %v", err)
	}
	if out.Len() != 0 {
		t.Errorf("no prompts may be printed for /dev/null stdin, got:\n%s", out.String())
	}
	if outcome.Report == nil || !outcome.Report.Pass() {
		t.Fatalf("generated repo must validate, report: %+v", outcome.Report)
	}
}

// TestRunStdinPipeNonInteractive proves that a real *os.File pipe fd (the
// `echo | eka init` shape) stays non-interactive under the fd-based
// terminal check: no prompts, deterministic defaults, no git init.
func TestRunStdinPipeNonInteractive(t *testing.T) {
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	// Answers that would flip the git default if they were consumed.
	if _, err := pw.WriteString("piped-name\npiped-ns\ndescription\ny\ny\n"); err != nil {
		t.Fatal(err)
	}
	pw.Close()
	defer pr.Close()

	dir := t.TempDir()
	var out bytes.Buffer
	opts := Options{
		Target: dir,
		Stdin:  pr,
		Stdout: &out,
		Stderr: io.Discard,
	}
	outcome, err := Run(opts)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.GitStatus != "skipped (non-interactive mode)" {
		t.Errorf("GitStatus = %q, want skipped (non-interactive mode)", outcome.GitStatus)
	}
	if outcome.Project != filepath.Base(dir) {
		t.Errorf("Project = %q, want %q (piped answers must be ignored)", outcome.Project, filepath.Base(dir))
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); !os.IsNotExist(err) {
		t.Errorf(".git must not exist after piped init, stat err = %v", err)
	}
	if out.Len() != 0 {
		t.Errorf("no prompts may be printed for a piped stdin, got:\n%s", out.String())
	}
	if outcome.Report == nil || !outcome.Report.Pass() {
		t.Fatalf("generated repo must validate, report: %+v", outcome.Report)
	}
}
