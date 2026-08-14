package feedback

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// feedbackStoreEnv returns a store rooted at a fresh temp home.
func feedbackStoreEnv(t *testing.T) *Store {
	t.Helper()
	return New(t.TempDir())
}

func TestSaveLoadRoundtrip(t *testing.T) {
	st := feedbackStoreEnv(t)
	f := sampleFeedback()
	if err := st.Save(f); err != nil {
		t.Fatal(err)
	}
	got, err := st.Load(f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if *got != *f {
		t.Errorf("loaded feedback differs:\ngot  %+v\nwant %+v", *got, *f)
	}
	// The id without the .md suffix resolves to the same file.
	got, err = st.Load(f.ID + ".md")
	if err != nil {
		t.Fatalf("load with .md suffix: %v", err)
	}
	if got.ID != f.ID {
		t.Errorf("id = %q, want %q", got.ID, f.ID)
	}
	// The file lands at <home>/feedback/<id>.md.
	path := filepath.Join(st.Dir, f.ID+".md")
	if _, err := os.Stat(path); err != nil {
		t.Errorf("file must exist at %s: %v", path, err)
	}
}

func TestSaveCreatesDirWithPrivatePerms(t *testing.T) {
	home := t.TempDir()
	st := New(home)
	if err := st.Save(sampleFeedback()); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(st.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o700 {
		t.Errorf("feedback dir mode = %v, want 0700 (mirroring the workspace)", fi.Mode().Perm())
	}
	ff, err := os.Stat(filepath.Join(st.Dir, sampleFeedback().ID+".md"))
	if err != nil {
		t.Fatal(err)
	}
	if ff.Mode().Perm() != 0o600 {
		t.Errorf("feedback file mode = %v, want 0600", ff.Mode().Perm())
	}
}

func TestSaveRefusesInvalid(t *testing.T) {
	st := feedbackStoreEnv(t)
	f := sampleFeedback()
	f.Type = "rant"
	if err := st.Save(f); err == nil {
		t.Error("Save must refuse an invalid feedback")
	}
	if _, err := os.Stat(filepath.Join(st.Dir, f.ID+".md")); !os.IsNotExist(err) {
		t.Error("no file may be written for an invalid feedback")
	}
}

func TestLoadNotFound(t *testing.T) {
	st := feedbackStoreEnv(t)
	if _, err := st.Load("fbk-20260101-nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing id: err = %v, want ErrNotFound", err)
	}
}

func TestLoadRejectsPathEscape(t *testing.T) {
	st := feedbackStoreEnv(t)
	if err := st.Save(sampleFeedback()); err != nil {
		t.Fatal(err)
	}
	// A user-supplied id must never escape the feedback directory.
	evil := filepath.Join("..", "..", "etc", "passwd")
	if _, err := st.Load(evil); err == nil || !strings.Contains(err.Error(), "invalid feedback id") {
		t.Errorf("id %q must be refused as invalid, got err = %v", evil, err)
	}
	if _, err := st.Load("../other"); err == nil {
		t.Error("traversal id must be refused")
	}
}

func TestListOrdering(t *testing.T) {
	st := feedbackStoreEnv(t)
	mk := func(id string) *Feedback {
		f := sampleFeedback()
		f.ID = id
		f.Created = id[len("fbk-"):][:8]
		return f
	}
	for _, id := range []string{
		"fbk-20260810-first",
		"fbk-20260812-newest",
		"fbk-20260811-middle",
	} {
		if err := st.Save(mk(id)); err != nil {
			t.Fatal(err)
		}
	}
	items, err := st.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 {
		t.Fatalf("len = %d, want 3", len(items))
	}
	want := []string{"fbk-20260812-newest", "fbk-20260811-middle", "fbk-20260810-first"}
	for i, w := range want {
		if items[i].ID != w {
			t.Errorf("items[%d].ID = %q, want %q (id descending)", i, items[i].ID, w)
		}
	}
}

func TestListEmpty(t *testing.T) {
	st := feedbackStoreEnv(t)
	items, err := st.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Errorf("len = %d, want 0 (a missing directory is an empty list)", len(items))
	}
}

func TestListFailsOnMalformedFile(t *testing.T) {
	st := feedbackStoreEnv(t)
	if err := st.Save(sampleFeedback()); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(st.Dir, "fbk-20260101-broken.md")
	if err := os.WriteFile(bad, []byte("no frontmatter here\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := st.List(); err == nil || !strings.Contains(err.Error(), "fbk-20260101-broken.md") {
		t.Errorf("List must fail deterministically naming the malformed file, got err = %v", err)
	}
}

func TestNewIDSlug(t *testing.T) {
	st := feedbackStoreEnv(t)
	created := time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC)
	for title, want := range map[string]string{
		"CLI refuses on empty repo":   "fbk-20260812-cli-refuses-on-empty-repo",
		"two  spaces--and_underscore": "fbk-20260812-two-spaces-and-underscore",
		"!!!":                         "fbk-20260812-untitled",
		"":                            "fbk-20260812-untitled",
		"  edge--dashes  ":            "fbk-20260812-edge-dashes",
	} {
		if got := st.NewID(title, created); got != want {
			t.Errorf("NewID(%q) = %q, want %q", title, got, want)
		}
	}
}

func TestNewIDCollision(t *testing.T) {
	st := feedbackStoreEnv(t)
	created := time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC)
	first := st.NewID("Same title", created)
	if err := st.Save(&Feedback{
		ID: first, Type: TypeBug, Title: "Same title", Severity: SeverityLow,
		Source: "human", EkaVersion: "dev", OS: "linux/amd64",
		Command: "eka", Status: StatusDraft, Created: "2026-08-12",
	}); err != nil {
		t.Fatal(err)
	}
	second := st.NewID("Same title", created)
	if second != "fbk-20260812-same-title-2" {
		t.Errorf("collision id = %q, want fbk-20260812-same-title-2", second)
	}
	if err := st.Save(&Feedback{
		ID: second, Type: TypeSuggestion, Title: "Same title", Severity: SeverityLow,
		Source: "human", EkaVersion: "dev", OS: "linux/amd64",
		Command: "eka", Status: StatusDraft, Created: "2026-08-12",
	}); err != nil {
		t.Fatal(err)
	}
	if third := st.NewID("Same title", created); third != "fbk-20260812-same-title-3" {
		t.Errorf("third id = %q, want fbk-20260812-same-title-3", third)
	}
}

func TestMarkPublished(t *testing.T) {
	st := feedbackStoreEnv(t)
	f := sampleFeedback()
	if err := st.Save(f); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkPublished(f.ID, 7, "https://github.com/maleolabs/eka-cli/issues/7"); err != nil {
		t.Fatal(err)
	}
	got, err := st.Load(f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusPublished || got.IssueNumber != 7 ||
		got.IssueURL != "https://github.com/maleolabs/eka-cli/issues/7" {
		t.Errorf("published record = %+v, want status published + issue fields", got)
	}
	// The body survives the rewrite.
	if got.Body != f.Body {
		t.Errorf("body lost on publish: %q", got.Body)
	}
	// No temp debris remains.
	entries, err := os.ReadDir(st.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("feedback dir must hold exactly the published file, got %v", entries)
	}
}

func TestMarkPublishedRefuses(t *testing.T) {
	st := feedbackStoreEnv(t)
	if err := st.MarkPublished("fbk-20260101-nope", 1, "url"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing id: err = %v, want ErrNotFound", err)
	}
	f := sampleFeedback()
	if err := st.Save(f); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkPublished(f.ID, 7, "url"); err != nil {
		t.Fatal(err)
	}
	// Idempotence: a second publish refuses.
	err := st.MarkPublished(f.ID, 8, "url2")
	if err == nil || !strings.Contains(err.Error(), "already published as #7") {
		t.Errorf("second publish: err = %v, want the already-published refusal", err)
	}
	got, err := st.Load(f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.IssueNumber != 7 {
		t.Errorf("issue number must stay 7 after the refused second publish, got %d", got.IssueNumber)
	}
}
