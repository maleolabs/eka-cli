package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/maleolabs/eka-cli/feedback"
)

// This file tests `eka feedback` at CLI level (ADR-026): the draft is
// created under EKA_HOME/feedback/<id>.md with the full triage
// frontmatter and the per-type body scaffold, the list renders
// deterministically, and publish maps every refusal class to its exit
// code and rewrites the file on success.

// feedbackHomeEnv sets a fresh EKA_HOME and returns the feedback
// directory path.
func feedbackHomeEnv(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("EKA_HOME", home)
	return filepath.Join(home, "feedback")
}

// feedbackDraftID reads the single feedback id under the given
// directory (the tests create exactly one draft).
func feedbackDraftID(t *testing.T, dir string) string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, "fbk-*.md"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("want exactly one draft in %s, got %v (err %v)", dir, matches, err)
	}
	return strings.TrimSuffix(filepath.Base(matches[0]), ".md")
}

// fakeIssueServer serves issue creation with the given status/body and
// returns the server plus the API URL it answers.
func fakeIssueServer(t *testing.T, status int, body string) (*httptest.Server, string) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/maleolabs/eka-cli/issues", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("Authorization = %q, want Bearer test-token", got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		w.Write([]byte(body))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, srv.URL + "/repos/maleolabs/eka-cli/issues"
}

func TestFeedbackNewWritesDraft(t *testing.T) {
	dir := feedbackHomeEnv(t)
	code, out, errText := runIn([]string{
		"feedback", "new", "--type", "bug", "--title", "CLI refuses on an empty repo", "--command", "eka sync",
	})
	if code != 0 {
		t.Fatalf("feedback new: exit = %d, stderr %q", code, errText)
	}
	id := feedbackDraftID(t, dir)
	if !strings.Contains(id, "cli-refuses-on-an-empty-repo") {
		t.Errorf("id = %q, want the slugified title", id)
	}
	if !strings.Contains(out, "Feedback") || !strings.Contains(out, id) || !strings.Contains(out, dir) {
		t.Errorf("stdout = %q, want the themed report with id and path", out)
	}
	data, err := os.ReadFile(filepath.Join(dir, id+".md"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	// Auto-injected triage metadata (ADR-026 §Decision 1).
	for _, want := range []string{
		"id: " + id,
		"type: bug",
		"title: CLI refuses on an empty repo",
		"severity: low",
		"source: human",
		"eka_version: dev",
		"os: " + runtime.GOOS + "/" + runtime.GOARCH,
		"command: eka sync",
		"status: draft",
		"created: 2026-",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("frontmatter must contain %q:\n%s", want, text)
		}
	}
	// The bug scaffold body.
	if !strings.Contains(text, "## Steps to reproduce\n\n## Expected\n\n## Actual\n") {
		t.Errorf("bug draft must carry the reproduce/expected/actual scaffold:\n%s", text)
	}
}

func TestFeedbackNewScaffoldsByType(t *testing.T) {
	dir := feedbackHomeEnv(t)
	if code, _, errText := runIn([]string{"feedback", "new", "--type", "suggestion", "--title", "Add a flag", "--command", "x"}); code != 0 {
		t.Fatalf("feedback new: exit = %d, stderr %q", code, errText)
	}
	data, err := os.ReadFile(filepath.Join(dir, feedbackDraftID(t, dir)+".md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "## Description\n") {
		t.Errorf("non-bug draft must carry the description scaffold:\n%s", data)
	}
	if strings.Contains(string(data), "## Steps to reproduce") {
		t.Errorf("non-bug draft must not carry the bug scaffold:\n%s", data)
	}
}

func TestFeedbackNewContentFile(t *testing.T) {
	dir := feedbackHomeEnv(t)
	body := filepath.Join(t.TempDir(), "report.md")
	if err := os.WriteFile(body, []byte("## Observed\n\nIt broke.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, errText := runIn([]string{"feedback", "new", "--type", "bug", "--title", "t", "--content-file", body, "--command", "x"}); code != 0 {
		t.Fatalf("feedback new: exit = %d, stderr %q", code, errText)
	}
	data, err := os.ReadFile(filepath.Join(dir, feedbackDraftID(t, dir)+".md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "## Observed\n\nIt broke.\n") {
		t.Errorf("draft must carry the content-file body:\n%s", data)
	}
}

func TestFeedbackNewUsage(t *testing.T) {
	feedbackHomeEnv(t)
	for name, args := range map[string][]string{
		"missing-type":  {"feedback", "new", "--title", "t"},
		"missing-title": {"feedback", "new", "--type", "bug"},
		"bad-type":      {"feedback", "new", "--type", "rant", "--title", "t"},
		"bad-severity":  {"feedback", "new", "--type", "bug", "--title", "t", "--severity", "critical"},
		"bad-source":    {"feedback", "new", "--type", "bug", "--title", "t", "--source", "robot"},
	} {
		code, _, errText := runIn(args)
		if code != 2 {
			t.Errorf("%s: exit = %d, want 2 (usage)", name, code)
		}
		if !strings.Contains(errText, "eka:") {
			t.Errorf("%s: stderr must be the deterministic eka error, got %q", name, errText)
		}
	}
	// Nothing was written.
	if entries, err := os.ReadDir(feedbackDirOf(t)); err == nil && len(entries) != 0 {
		t.Errorf("usage failures must write nothing, found %v", entries)
	}
}

// feedbackDirOf returns the feedback directory of the current EKA_HOME.
func feedbackDirOf(t *testing.T) string {
	t.Helper()
	return filepath.Join(os.Getenv("EKA_HOME"), "feedback")
}

func TestFeedbackNewJSON(t *testing.T) {
	dir := feedbackHomeEnv(t)
	code, out, errText := runIn([]string{"feedback", "new", "--type", "question", "--title", "How is triage done?", "--command", "x", "--json"})
	if code != 0 {
		t.Fatalf("feedback new --json: exit = %d, stderr %q", code, errText)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("--json output is not valid JSON: %v", err)
	}
	if doc["schema"] != "eka-feedback-new-v1" || doc["ok"] != true || doc["status"] != "draft" {
		t.Errorf("--json = %v, want the pinned eka-feedback-new-v1 document", doc)
	}
	if id, _ := doc["id"].(string); id == "" {
		t.Errorf("--json = %v, want the id field", doc)
	}
	if path, _ := doc["path"].(string); path != filepath.Join(dir, feedbackDraftID(t, dir)+".md") {
		t.Errorf("--json path = %q, want the written draft path", path)
	}
	// A usage failure carries the machine document too.
	code, out, _ = runIn([]string{"feedback", "new", "--title", "t", "--json"})
	if code != 2 || !strings.Contains(out, `"schema":"eka-feedback-new-v1"`) || !strings.Contains(out, `"ok":false`) {
		t.Errorf("usage --json: exit = %d, out = %q, want the machine refusal document", code, out)
	}
}

func TestFeedbackListShowsDraft(t *testing.T) {
	feedbackHomeEnv(t)
	if code, _, errText := runIn([]string{"feedback", "new", "--type", "suggestion", "--title", "A very long title that should be truncated in the table display", "--command", "x"}); code != 0 {
		t.Fatalf("feedback new: exit = %d, stderr %q", code, errText)
	}
	code, out, errText := runIn([]string{"feedback", "list"})
	if code != 0 {
		t.Fatalf("feedback list: exit = %d, stderr %q", code, errText)
	}
	if !strings.Contains(out, "Feedback") || !strings.Contains(out, "suggestion") || !strings.Contains(out, "draft") {
		t.Errorf("list must render the feedback table:\n%s", out)
	}
	// Deterministic: two runs produce identical bytes.
	_, out2, _ := runIn([]string{"feedback", "list"})
	if out != out2 {
		t.Error("feedback list is not deterministic")
	}
	// The long title is truncated with the ellipsis.
	if !strings.Contains(out, "…") {
		t.Errorf("long title must be truncated:\n%s", out)
	}
}

func TestFeedbackListEmpty(t *testing.T) {
	feedbackHomeEnv(t)
	code, out, errText := runIn([]string{"feedback", "list"})
	if code != 0 {
		t.Fatalf("empty list: exit = %d, stderr %q", code, errText)
	}
	if !strings.Contains(out, "No feedback yet. Run 'eka feedback new' to create a draft.") {
		t.Errorf("empty list must show the informative line with the create hint:\n%s", out)
	}
}

func TestFeedbackListJSON(t *testing.T) {
	feedbackHomeEnv(t)
	if code, _, errText := runIn([]string{"feedback", "new", "--type", "bug", "--title", "t", "--command", "x"}); code != 0 {
		t.Fatalf("feedback new: exit = %d, stderr %q", code, errText)
	}
	code, out, errText := runIn([]string{"feedback", "list", "--json"})
	if code != 0 {
		t.Fatalf("feedback list --json: exit = %d, stderr %q", code, errText)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("--json output is not valid JSON: %v", err)
	}
	if doc["schema"] != "eka-feedback-list-v1" || doc["ok"] != true {
		t.Errorf("--json = %v, want the pinned eka-feedback-list-v1 document", doc)
	}
	items, ok := doc["feedback"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("--json feedback = %v, want one entry", doc["feedback"])
	}
	entry := items[0].(map[string]any)
	if entry["type"] != "bug" || entry["status"] != "draft" || entry["title"] != "t" {
		t.Errorf("--json entry = %v, want the triage record", entry)
	}
}

func TestFeedbackPublishRefusals(t *testing.T) {
	dir := feedbackHomeEnv(t)
	if code, _, errText := runIn([]string{"feedback", "new", "--type", "bug", "--title", "t", "--command", "x"}); code != 0 {
		t.Fatalf("feedback new: exit = %d, stderr %q", code, errText)
	}
	id := feedbackDraftID(t, dir)

	// Unknown id: usage class, exit 2.
	if code, _, errText := runIn([]string{"feedback", "publish", "fbk-20260101-nope", "--yes"}); code != 2 || !strings.Contains(errText, "unknown feedback") {
		t.Errorf("unknown id: exit = %d, stderr = %q, want 2", code, errText)
	}
	// Empty (unbundled) token: refusal, exit 1.
	feedback.SetIssueToken("")
	if code, _, errText := runIn([]string{"feedback", "publish", id, "--yes"}); code != 1 || !strings.Contains(errText, "issue token not bundled — use a release binary") {
		t.Errorf("empty token: exit = %d, stderr = %q, want 1 + release-binary hint", code, errText)
	}
	// Non-TTY without --yes: the determinism gate, exit 2.
	if code, _, errText := runIn([]string{"feedback", "publish", id}); code != 2 || !strings.Contains(errText, "publish requires --yes outside a terminal") {
		t.Errorf("non-TTY without --yes: exit = %d, stderr = %q, want 2", code, errText)
	}
	// The draft survives every refusal.
	data, err := os.ReadFile(filepath.Join(dir, id+".md"))
	if err != nil || !strings.Contains(string(data), "status: draft") {
		t.Errorf("refused publish must leave the draft untouched (err %v):\n%s", err, data)
	}
}

func TestFeedbackPublishSuccess(t *testing.T) {
	dir := feedbackHomeEnv(t)
	if code, _, errText := runIn([]string{"feedback", "new", "--type", "suggestion", "--title", "Add a filter flag", "--command", "eka list --all"}); code != 0 {
		t.Fatalf("feedback new: exit = %d, stderr %q", code, errText)
	}
	id := feedbackDraftID(t, dir)

	srv, apiURL := fakeIssueServer(t, http.StatusCreated,
		`{"number": 7, "html_url": "https://github.com/maleolabs/eka-cli/issues/7"}`)
	feedback.SetIssueAPIURL(apiURL)
	feedback.SetIssueToken("test-token")
	t.Cleanup(func() { feedback.SetIssueToken(""); _ = srv })

	code, out, errText := runIn([]string{"feedback", "publish", id, "--yes"})
	if code != 0 {
		t.Fatalf("feedback publish: exit = %d, stderr %q", code, errText)
	}
	if !strings.Contains(out, "✓ Published: #7 https://github.com/maleolabs/eka-cli/issues/7") {
		t.Errorf("stdout = %q, want the deterministic publish report", out)
	}
	// The file was rewritten with the issue record.
	data, err := os.ReadFile(filepath.Join(dir, id+".md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"status: published",
		"issue_url: https://github.com/maleolabs/eka-cli/issues/7",
		"issue_number: 7",
	} {
		if !strings.Contains(string(data), want) {
			t.Errorf("published file must contain %q:\n%s", want, data)
		}
	}
	// A second publish refuses (idempotent, exit 1).
	code, _, errText = runIn([]string{"feedback", "publish", id, "--yes"})
	if code != 1 || !strings.Contains(errText, "already published as #7") {
		t.Errorf("second publish: exit = %d, stderr = %q, want 1", code, errText)
	}
}

func TestFeedbackPublishSuccessJSON(t *testing.T) {
	dir := feedbackHomeEnv(t)
	if code, _, errText := runIn([]string{"feedback", "new", "--type", "bug", "--title", "t", "--command", "x"}); code != 0 {
		t.Fatalf("feedback new: exit = %d, stderr %q", code, errText)
	}
	id := feedbackDraftID(t, dir)

	srv, apiURL := fakeIssueServer(t, http.StatusCreated,
		`{"number": 12, "html_url": "https://github.com/maleolabs/eka-cli/issues/12"}`)
	feedback.SetIssueAPIURL(apiURL)
	feedback.SetIssueToken("test-token")
	t.Cleanup(func() { feedback.SetIssueToken(""); _ = srv })

	code, out, errText := runIn([]string{"feedback", "publish", id, "--yes", "--json"})
	if code != 0 {
		t.Fatalf("feedback publish --json: exit = %d, stderr %q", code, errText)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("--json output is not valid JSON: %v", err)
	}
	if doc["schema"] != "eka-feedback-publish-v1" || doc["ok"] != true ||
		doc["id"] != id || doc["issueNumber"] != float64(12) ||
		doc["issueUrl"] != "https://github.com/maleolabs/eka-cli/issues/12" {
		t.Errorf("--json = %v, want the pinned eka-feedback-publish-v1 document", doc)
	}
}

func TestFeedbackHelp(t *testing.T) {
	code, out, _ := runIn([]string{"feedback", "--help"})
	if code != 0 {
		t.Fatalf("feedback --help: exit = %d, want 0", code)
	}
	for _, want := range []string{"new", "publish", "list", "eka feedback"} {
		if !strings.Contains(out, want) {
			t.Errorf("help must mention %q:\n%s", want, out)
		}
	}
}
