package bootstrap

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/maleolabs/eka-core/metadata"
)

// --- BuildPlan ----------------------------------------------------------

// TestBuildPlanNewDirectory pins the fresh-target plan: create dir +
// generate eka.yaml + git + validate — and nothing else (identity-only:
// no docs/ tree, no README, no skeleton files).
func TestBuildPlanNewDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "fresh")
	d, err := Discover(dir)
	if err != nil {
		t.Fatal(err)
	}
	plan := BuildPlan(dir, d, DefaultAnswers(d))

	if len(plan) != 4 {
		t.Fatalf("fresh plan must be create-dir + eka.yaml + git + validate, got %d actions: %v", len(plan), plan)
	}
	// 1. The target dir itself is created (sentinel path ".", target in
	// Detail).
	if plan[0].Kind != ActionCreateDir || plan[0].Path != "." || plan[0].Detail != dir {
		t.Errorf("first action must create the target dir: %+v", plan[0])
	}
	if plan[0].String() != "create dir: "+dir {
		t.Errorf("target-dir action renders wrong: %s", plan[0].String())
	}
	// 2. eka.yaml is generated at the repository root with the
	// deterministic identity content.
	if plan[1].Kind != ActionGenerateEkaYAML || plan[1].Path != "eka.yaml" {
		t.Errorf("second action must generate eka.yaml, got %+v", plan[1])
	}
	if !bytes.Equal(plan[1].Content, generatedEkaYAML(d, DefaultAnswers(d))) {
		t.Error("eka.yaml action must carry the deterministic identity content")
	}
	if len(plan[1].Content) == 0 {
		t.Error("eka.yaml action must carry content")
	}
	// 3. Git follows the identity file.
	if plan[2].Kind != ActionGitSkip {
		t.Errorf("third action must be the git step, got %+v", plan[2])
	}
	// 4. Validation closes the plan.
	if plan[3].Kind != ActionValidate {
		t.Errorf("last action must be validate, got %+v", plan[3])
	}
	// No skeleton artifact may be planned.
	for _, a := range plan {
		if strings.HasPrefix(a.Path, "docs/") || a.Path == "README.md" || a.Path == "docs" {
			t.Errorf("identity-only plan must not touch %q, got %+v", a.Path, a)
		}
	}
}

// TestBuildPlanExistingDirectory pins the adoption plan for an existing
// non-EKA directory: no create-dir (the target exists), eka.yaml +
// git + validate.
func TestBuildPlanExistingDirectory(t *testing.T) {
	dir := t.TempDir()
	d, err := Discover(dir)
	if err != nil {
		t.Fatal(err)
	}
	plan := BuildPlan(dir, d, DefaultAnswers(d))
	if len(plan) != 3 {
		t.Fatalf("existing-dir plan must be eka.yaml + git + validate, got %d actions: %v", len(plan), plan)
	}
	if plan[0].Kind != ActionGenerateEkaYAML || plan[0].Path != "eka.yaml" {
		t.Errorf("first action must generate eka.yaml, got %+v", plan[0])
	}
	if plan[1].Kind != ActionGitSkip {
		t.Errorf("second action must be the git step, got %+v", plan[1])
	}
	if plan[2].Kind != ActionValidate {
		t.Errorf("last action must be validate, got %+v", plan[2])
	}
	for _, a := range plan {
		if a.Kind == ActionCreateDir {
			t.Error("an existing directory must not be planned for creation")
		}
	}
}

func TestBuildPlanDeterministic(t *testing.T) {
	dir := t.TempDir()
	d, err := Discover(dir)
	if err != nil {
		t.Fatal(err)
	}
	a := DefaultAnswers(d)
	p1 := BuildPlan(dir, d, a)
	p2 := BuildPlan(dir, d, a)
	if len(p1) != len(p2) {
		t.Fatalf("plan lengths differ: %d vs %d", len(p1), len(p2))
	}
	for i := range p1 {
		if p1[i].String() != p2[i].String() {
			t.Errorf("plan differs at %d: %s vs %s", i, p1[i], p2[i])
		}
	}
}

func TestBuildPlanExistingEkaRepo(t *testing.T) {
	// A docs-marked legacy repository without eka.yaml is adopted
	// (ADR-018 Decision 3): the plan gains the eka.yaml generation
	// action between the reuse and the validate actions.
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "docs", "operating"), 0o755)
	os.MkdirAll(filepath.Join(dir, "docs", "exchange"), 0o755)
	os.WriteFile(filepath.Join(dir, "docs", "exchange", "validation.md"), []byte("v"), 0o644)
	os.WriteFile(filepath.Join(dir, "docs", "exchange", "transfer.md"), []byte("t"), 0o644)
	d, err := Discover(dir)
	if err != nil {
		t.Fatal(err)
	}
	a := DefaultAnswers(d)
	plan := BuildPlan(dir, d, a)
	if len(plan) != 3 {
		t.Fatalf("legacy repo without eka.yaml must plan reuse + generate eka.yaml + validate, got %d actions", len(plan))
	}
	if plan[0].Kind != ActionReuse {
		t.Errorf("first action must be reuse, got %+v", plan[0])
	}
	if plan[1].Kind != ActionGenerateEkaYAML || plan[1].Path != "eka.yaml" {
		t.Errorf("second action must generate eka.yaml, got %+v", plan[1])
	}
	if !bytes.Equal(plan[1].Content, generatedEkaYAML(d, a)) {
		t.Error("the adoption eka.yaml action must carry the deterministic identity content")
	}
	if plan[2].Kind != ActionValidate {
		t.Errorf("last action must be validate, got %+v", plan[2])
	}

	// A metadata repository that already carries the identical eka.yaml:
	// reuse + reuse eka.yaml + validate (the no-op run).
	os.WriteFile(filepath.Join(dir, "eka.yaml"), generatedEkaYAML(d, a), 0o644)
	d, err = Discover(dir)
	if err != nil {
		t.Fatal(err)
	}
	plan = BuildPlan(dir, d, a)
	if len(plan) != 3 {
		t.Fatalf("metadata repo must plan reuse + reuse eka.yaml + validate, got %d actions", len(plan))
	}
	if plan[1].Kind != ActionReuse || plan[1].Path != "eka.yaml" {
		t.Errorf("identical eka.yaml must be planned as reuse, got %+v", plan[1])
	}
	if plan[2].Kind != ActionValidate {
		t.Errorf("last action must be validate, got %+v", plan[2])
	}

	// A metadata repository whose eka.yaml differs from the
	// deterministic content: overwrite-confirm, never a silent replace.
	os.WriteFile(filepath.Join(dir, "eka.yaml"), []byte("version: 1\nproject: other\nname: other\nnamespace: other\n"), 0o644)
	d, err = Discover(dir)
	if err != nil {
		t.Fatal(err)
	}
	plan = BuildPlan(dir, d, a)
	if len(plan) != 3 {
		t.Fatalf("differing eka.yaml must keep the 3-action plan, got %d actions", len(plan))
	}
	if plan[1].Kind != ActionOverwriteConfirm || plan[1].Path != "eka.yaml" {
		t.Errorf("differing eka.yaml must be planned as overwrite-confirm, got %+v", plan[1])
	}
}

// TestBuildPlanEkaYAMLContent: the generated eka.yaml action's content
// parses with metadata.Parse and carries the decoupled identity:
// project == the wizard project id, namespace == the wizard namespace,
// name == the target directory basename.
func TestBuildPlanEkaYAMLContent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "my-repo")
	d, err := Discover(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Decoupled identity: the project and the namespace differ.
	a := Answers{Project: "atrium", Namespace: "atrium-api"}
	plan := BuildPlan(dir, d, a)
	var content []byte
	for _, act := range plan {
		if act.Kind == ActionGenerateEkaYAML {
			content = act.Content
		}
	}
	if content == nil {
		t.Fatal("plan must contain the generate-eka-yaml action")
	}
	m, err := metadata.Parse(content)
	if err != nil {
		t.Fatalf("generated eka.yaml must parse: %v\n%s", err, content)
	}
	if m.Version != 1 {
		t.Errorf("Version = %d, want 1", m.Version)
	}
	if m.Project != "atrium" {
		t.Errorf("Project = %q, want the wizard project id atrium", m.Project)
	}
	if m.Namespace != "atrium-api" {
		t.Errorf("Namespace = %q, want the wizard namespace atrium-api", m.Namespace)
	}
	if m.Name != "my-repo" {
		t.Errorf("Name = %q, want the target basename my-repo", m.Name)
	}
	if want := "version: 1\nproject: atrium\nname: my-repo\nnamespace: atrium-api\n"; string(content) != want {
		t.Errorf("eka.yaml bytes differ:\ngot:  %q\nwant: %q", content, want)
	}

	// Default answers keep project == namespace == the sanitized
	// basename.
	a = DefaultAnswers(d)
	content = nil
	for _, act := range BuildPlan(dir, d, a) {
		if act.Kind == ActionGenerateEkaYAML {
			content = act.Content
		}
	}
	m, err = metadata.Parse(content)
	if err != nil {
		t.Fatalf("default generated eka.yaml must parse: %v\n%s", err, content)
	}
	if m.Project != "my-repo" || m.Namespace != "my-repo" {
		t.Errorf("default identity must be project == namespace == my-repo, got %q/%q", m.Project, m.Namespace)
	}
}

// TestBuildPlanEkaYAMLReuseAndOverwrite: an existing identical eka.yaml
// is planned as reuse; a differing one as overwrite-confirm (never
// silently replaced).
func TestBuildPlanEkaYAMLReuseAndOverwrite(t *testing.T) {
	dir := t.TempDir()
	d, err := Discover(dir)
	if err != nil {
		t.Fatal(err)
	}
	a := DefaultAnswers(d)
	want := generatedEkaYAML(d, a)

	// Identical existing file: reuse.
	if err := os.WriteFile(filepath.Join(dir, "eka.yaml"), want, 0o644); err != nil {
		t.Fatal(err)
	}
	plan := BuildPlan(dir, d, a)
	for _, act := range plan {
		if act.Path == "eka.yaml" && act.Kind != ActionReuse {
			t.Errorf("identical eka.yaml must be planned as reuse, got %s", act.Kind)
		}
	}

	// Differing existing file: overwrite-confirm with the content.
	if err := os.WriteFile(filepath.Join(dir, "eka.yaml"), []byte("version: 1\nproject: other\nname: other\nnamespace: other\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	plan = BuildPlan(dir, d, a)
	for _, act := range plan {
		if act.Path == "eka.yaml" && act.Kind != ActionOverwriteConfirm {
			t.Errorf("differing eka.yaml must be planned as overwrite-confirm, got %s", act.Kind)
		}
	}
}

// TestEkaYAMLNameFallback: an unusable basename (filesystem root) falls
// back deterministically to the sanitized/fallback name so eka.yaml
// always passes metadata.Parse.
func TestEkaYAMLNameFallback(t *testing.T) {
	d := &Discovery{AbsTarget: "/"}
	if got := ekaYAMLName(d); got != fallbackName {
		t.Errorf("root basename must fall back to %q, got %q", fallbackName, got)
	}
	d = &Discovery{AbsTarget: filepath.Join("/tmp", "My Repo")}
	if got := ekaYAMLName(d); got != "my-repo" {
		t.Errorf("invalid basename must be sanitized, got %q", got)
	}
	d = &Discovery{AbsTarget: filepath.Join("/tmp", "my-repo")}
	if got := ekaYAMLName(d); got != "my-repo" {
		t.Errorf("valid basename must pass through, got %q", got)
	}
}

func TestBuildPlanGitSkipReasons(t *testing.T) {
	cases := []struct {
		name string
		d    *Discovery
		a    Answers
		want string
	}{
		{"existing repo", &Discovery{BaseName: "x", IsGitRepo: true}, Answers{}, "skipped (already a git repository)"},
		{"no git binary", &Discovery{BaseName: "x", GitAvailable: false}, Answers{InitGit: true}, "skipped (git not available)"},
		{"declined", &Discovery{BaseName: "x", GitAvailable: true}, Answers{InitGit: false, Interactive: true}, "skipped (declined)"},
		{"non-interactive", &Discovery{BaseName: "x", GitAvailable: true}, Answers{InitGit: false}, "skipped (non-interactive mode)"},
	}
	for _, tc := range cases {
		plan := BuildPlan("t", tc.d, tc.a)
		gitLine := ""
		for _, a := range plan {
			if a.Kind == ActionGitSkip {
				gitLine = a.Detail
			}
		}
		if gitLine != tc.want {
			t.Errorf("%s: git detail = %q, want %q", tc.name, gitLine, tc.want)
		}
	}
}

func TestBuildPlanGitInitPlanned(t *testing.T) {
	d := &Discovery{BaseName: "x", GitAvailable: true}
	a := Answers{InitGit: true, Interactive: true}
	plan := BuildPlan("t", d, a)
	found := false
	for _, act := range plan {
		if act.Kind == ActionGitInit {
			found = true
		}
	}
	if !found {
		t.Error("git init must be planned when requested and possible")
	}
}

func TestActionString(t *testing.T) {
	cases := []struct {
		a    Action
		want string
	}{
		{Action{Kind: ActionCreateDir, Path: "."}, "create dir: ."},
		{Action{Kind: ActionCreateDir, Path: ".", Detail: "myproj"}, "create dir: myproj"},
		{Action{Kind: ActionGenerateEkaYAML, Path: "eka.yaml"}, "generate file: eka.yaml (repository identity)"},
		{Action{Kind: ActionReuse, Path: "eka.yaml"}, "reuse: eka.yaml"},
		{Action{Kind: ActionReuse, Path: "t", Detail: "existing EKA repository (already initialized)"}, "reuse: t (existing EKA repository (already initialized))"},
		{Action{Kind: ActionOverwriteConfirm, Path: "eka.yaml"}, "overwrite confirm: eka.yaml"},
		{Action{Kind: ActionGitInit, Path: "t"}, "git init: t"},
		{Action{Kind: ActionGitSkip, Detail: "skipped (already a git repository)"}, "git init: skipped (already a git repository)"},
		{Action{Kind: ActionValidate, Path: "t", Detail: "after generation"}, "validate: t after generation"},
	}
	for _, tc := range cases {
		if got := tc.a.String(); got != tc.want {
			t.Errorf("Action.String() = %q, want %q", got, tc.want)
		}
	}
}

// --- Apply --------------------------------------------------------------

func applyOpts(interactive bool) ApplyOptions {
	return ApplyOptions{
		Interactive: interactive,
		Stdin:       strings.NewReader(""),
		Stdout:      &bytes.Buffer{},
		Stderr:      &bytes.Buffer{},
	}
}

func TestApplyNonInteractiveSkipsOverwrites(t *testing.T) {
	dir := t.TempDir()
	// A differing existing eka.yaml triggers the overwrite-confirm
	// contract; non-interactive apply must never replace it.
	os.WriteFile(filepath.Join(dir, "eka.yaml"), []byte("version: 1\nproject: other\nname: other\nnamespace: other\n"), 0o644)
	d, err := Discover(dir)
	if err != nil {
		t.Fatal(err)
	}
	plan := BuildPlan(dir, d, DefaultAnswers(d))
	res, err := Apply(dir, plan, applyOpts(false))
	if err != nil {
		t.Fatal(err)
	}
	content, _ := os.ReadFile(filepath.Join(dir, "eka.yaml"))
	if string(content) != "version: 1\nproject: other\nname: other\nnamespace: other\n" {
		t.Error("non-interactive apply must never replace an existing file")
	}
	found := false
	for _, p := range res.SkippedFiles {
		if p == "eka.yaml" {
			found = true
		}
	}
	if !found {
		t.Errorf("skipped file must be reported, got %v", res.SkippedFiles)
	}
}

func TestApplyInteractiveOverwrite(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "eka.yaml"), []byte("version: 1\nproject: other\nname: other\nnamespace: other\n"), 0o644)
	d, err := Discover(dir)
	if err != nil {
		t.Fatal(err)
	}
	plan := BuildPlan(dir, d, DefaultAnswers(d))
	var asked []string
	opts := applyOpts(true)
	opts.ConfirmOverwrite = func(path string) (bool, error) {
		asked = append(asked, path)
		return true, nil
	}
	res, err := Apply(dir, plan, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(asked) == 0 {
		t.Fatal("overwrite confirmation must be requested interactively")
	}
	content, _ := os.ReadFile(filepath.Join(dir, "eka.yaml"))
	if bytes.Equal(content, []byte("version: 1\nproject: other\nname: other\nnamespace: other\n")) {
		t.Error("confirmed overwrite must replace the file")
	}
	found := false
	for _, p := range res.OverwrittenFiles {
		if p == "eka.yaml" {
			found = true
		}
	}
	if !found {
		t.Errorf("overwritten file must be reported, got %v", res.OverwrittenFiles)
	}
}

func TestApplyInteractiveDecline(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "eka.yaml"), []byte("version: 1\nproject: other\nname: other\nnamespace: other\n"), 0o644)
	d, err := Discover(dir)
	if err != nil {
		t.Fatal(err)
	}
	plan := BuildPlan(dir, d, DefaultAnswers(d))
	opts := applyOpts(true)
	opts.ConfirmOverwrite = func(path string) (bool, error) { return false, nil }
	res, err := Apply(dir, plan, opts)
	if err != nil {
		t.Fatal(err)
	}
	content, _ := os.ReadFile(filepath.Join(dir, "eka.yaml"))
	if string(content) != "version: 1\nproject: other\nname: other\nnamespace: other\n" {
		t.Error("declined overwrite must preserve the file")
	}
	if len(res.OverwrittenFiles) != 0 {
		t.Errorf("no file may be reported overwritten, got %v", res.OverwrittenFiles)
	}
}

func TestApplyGitInit(t *testing.T) {
	dir := t.TempDir()
	plan := []Action{
		{Kind: ActionGitInit, Path: dir},
		{Kind: ActionValidate, Path: dir},
	}
	called := false
	opts := applyOpts(false)
	opts.GitInit = func(d string, stdout, stderr io.Writer) error {
		called = true
		if d != dir {
			t.Errorf("git init dir = %q, want %q", d, dir)
		}
		return nil
	}
	res, err := Apply(dir, plan, opts)
	if err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Error("git init must be executed when planned")
	}
	if res.GitStatus != "initialized" {
		t.Errorf("GitStatus = %q, want initialized", res.GitStatus)
	}
}

func TestApplyGitInitFailureIsWarning(t *testing.T) {
	dir := t.TempDir()
	plan := []Action{{Kind: ActionGitInit, Path: dir}}
	var errb bytes.Buffer
	opts := applyOpts(false)
	opts.Stderr = &errb
	opts.GitInit = func(d string, stdout, stderr io.Writer) error {
		return errors.New("git exploded")
	}
	res, err := Apply(dir, plan, opts)
	if err != nil {
		t.Fatalf("a failed git init must not fail the generation: %v", err)
	}
	if !strings.Contains(res.GitStatus, "failed") {
		t.Errorf("GitStatus = %q, want failed", res.GitStatus)
	}
	if !strings.Contains(errb.String(), "warning") {
		t.Errorf("stderr must carry a warning, got %q", errb.String())
	}
}

func TestApplyGitStatusExisting(t *testing.T) {
	plan := []Action{{Kind: ActionGitSkip, Detail: "skipped (already a git repository)"}}
	res, err := Apply(t.TempDir(), plan, applyOpts(false))
	if err != nil {
		t.Fatal(err)
	}
	if res.GitStatus != "existing" {
		t.Errorf("GitStatus = %q, want existing", res.GitStatus)
	}
}

func TestApplyCreateDirForFileConflict(t *testing.T) {
	dir := t.TempDir()
	// "docs" exists as a file: creating the directory must fail cleanly.
	os.WriteFile(filepath.Join(dir, "docs"), []byte("x"), 0o644)
	plan := []Action{{Kind: ActionCreateDir, Path: "docs"}}
	if _, err := Apply(dir, plan, applyOpts(false)); err == nil {
		t.Error("conflicting file must produce an error")
	}
}
