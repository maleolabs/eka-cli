package feedback

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeIssuesServer serves the issue-creation endpoint (the fixed target
// repo path) from memory: status/body selectable per test, request
// assertions wired through a callback.
func fakeIssuesServer(t *testing.T, status int, respBody string, check func(r *http.Request, payload map[string]any)) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/maleolabs/eka-cli/issues", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("issue creation must POST, got %s", r.Method)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("Authorization = %q, want Bearer test-token", got)
		}
		if got := r.Header.Get("Accept"); got != "application/vnd.github+json" {
			t.Errorf("Accept = %q, want application/vnd.github+json", got)
		}
		if got := r.Header.Get("X-GitHub-Api-Version"); got != "2022-11-28" {
			t.Errorf("X-GitHub-Api-Version = %q, want 2022-11-28", got)
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("request body is not valid JSON: %v", err)
		}
		if _, hasTitle := payload["title"]; !hasTitle {
			t.Errorf("request body must carry a title, got %v", payload)
		}
		if _, hasBody := payload["body"]; !hasBody {
			t.Errorf("request body must carry a body, got %v", payload)
		}
		if _, hasLabels := payload["labels"]; hasLabels {
			t.Errorf("request body must NOT carry labels (an unknown label fails the whole request)")
		}
		if check != nil {
			check(r, payload)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		w.Write([]byte(respBody))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// issueAPIOf points the package endpoint at the fake server and returns
// the API URL.
func issueAPIOf(srv *httptest.Server) string {
	return srv.URL + "/repos/maleolabs/eka-cli/issues"
}

// publishEnv points the package's issue endpoint at the given server
// and bundles a token, restoring both after the test.
func publishEnv(t *testing.T, srv *httptest.Server) {
	t.Helper()
	SetIssueAPIURL(issueAPIOf(srv))
	SetIssueToken("test-token")
	t.Cleanup(func() { SetIssueToken("") })
}

// savedDraft persists the sample feedback under a fresh home and
// returns the store and the home.
func savedDraft(t *testing.T) (*Store, string) {
	t.Helper()
	home := t.TempDir()
	st := New(home)
	if err := st.Save(sampleFeedback()); err != nil {
		t.Fatal(err)
	}
	return st, home
}

func TestPublishSuccess(t *testing.T) {
	var gotTitle, gotBody string
	srv := fakeIssuesServer(t, http.StatusCreated,
		`{"number": 7, "html_url": "https://github.com/maleolabs/eka-cli/issues/7"}`,
		func(_ *http.Request, payload map[string]any) {
			gotTitle, _ = payload["title"].(string)
			gotBody, _ = payload["body"].(string)
		})
	publishEnv(t, srv)

	st, home := savedDraft(t)
	published, err := Publish(context.Background(), home, sampleFeedback().ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotTitle != sampleFeedback().Title {
		t.Errorf("issue title = %q, want %q", gotTitle, sampleFeedback().Title)
	}
	if !strings.Contains(gotBody, "**Type:** bug") || !strings.Contains(gotBody, sampleFeedback().Body) {
		t.Errorf("issue body must be the rendered report:\n%s", gotBody)
	}
	if published.Status != StatusPublished || published.IssueNumber != 7 ||
		published.IssueURL != "https://github.com/maleolabs/eka-cli/issues/7" {
		t.Errorf("published feedback = %+v, want status published + issue fields", published)
	}
	// The local file was rewritten with the issue record.
	onDisk, err := st.Load(sampleFeedback().ID)
	if err != nil {
		t.Fatal(err)
	}
	if onDisk.Status != StatusPublished || onDisk.IssueNumber != 7 {
		t.Errorf("file not rewritten: %+v", onDisk)
	}
}

func TestPublishAlreadyPublished(t *testing.T) {
	srv := fakeIssuesServer(t, http.StatusCreated, `{"number": 1, "html_url": "u"}`, nil)
	publishEnv(t, srv)

	st, home := savedDraft(t)
	if err := st.MarkPublished(sampleFeedback().ID, 3, "https://example.com/issues/3"); err != nil {
		t.Fatal(err)
	}
	_, err := Publish(context.Background(), home, sampleFeedback().ID)
	if err == nil || !strings.Contains(err.Error(), "already published as #3") {
		t.Errorf("err = %v, want the already-published refusal", err)
	}
}

func TestPublishMissing(t *testing.T) {
	srv := fakeIssuesServer(t, http.StatusCreated, `{"number": 1, "html_url": "u"}`, nil)
	publishEnv(t, srv)
	_, err := Publish(context.Background(), t.TempDir(), "fbk-20260101-nope")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestPublishEmptyToken(t *testing.T) {
	srv := fakeIssuesServer(t, http.StatusCreated, `{"number": 1, "html_url": "u"}`, nil)
	SetIssueAPIURL(issueAPIOf(srv))
	SetIssueToken("")
	t.Cleanup(func() { SetIssueToken("") })

	st, home := savedDraft(t)
	_, err := Publish(context.Background(), home, sampleFeedback().ID)
	if err == nil || !strings.Contains(err.Error(), "issue token not bundled — use a release binary") {
		t.Errorf("err = %v, want the release-binary token refusal", err)
	}
	// The draft is untouched.
	onDisk, lerr := st.Load(sampleFeedback().ID)
	if lerr != nil {
		t.Fatal(lerr)
	}
	if onDisk.Status != StatusDraft {
		t.Errorf("refused publish must not touch the draft, got status %q", onDisk.Status)
	}
}

func TestPublishAPIError(t *testing.T) {
	srv := fakeIssuesServer(t, http.StatusUnauthorized, `{"message": "Bad credentials"}`, nil)
	publishEnv(t, srv)

	_, home := savedDraft(t)
	_, err := Publish(context.Background(), home, sampleFeedback().ID)
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v, want *APIError", err)
	}
	if apiErr.Status != http.StatusUnauthorized || apiErr.Message != "Bad credentials" {
		t.Errorf("APIError = %+v, want status 401 + message", apiErr)
	}
	if !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "Bad credentials") {
		t.Errorf("APIError.Error() = %q, want the status and message", err.Error())
	}
}

func TestCreateIssueNetworkError(t *testing.T) {
	// A closed server: the transport fails, the error is wrapped.
	closed := httptest.NewServer(http.NotFoundHandler())
	url := closed.URL
	closed.Close()
	SetIssueAPIURL(url + "/repos/maleolabs/eka-cli/issues")
	SetIssueToken("test-token")
	t.Cleanup(func() { SetIssueToken("") })

	client := &IssueClient{Token: "test-token"}
	if _, _, err := client.CreateIssue(context.Background(), "t", "b"); err == nil {
		t.Error("CreateIssue against a dead server must fail")
	}
}
