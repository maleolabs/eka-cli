package feedback

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// issueToken is the GitHub fine-grained PAT bundled into the binary at
// build time (ADR-026 §Decision 3) via
//
//	go build -ldflags "-X github.com/maleolabs/eka-cli/feedback.issueToken=<token>"
//
// It stays empty ("") on dev/test/CI builds, and publish then refuses
// deterministically with a release-binary hint — the ADR-024
// version == "dev" analogue. The token is scoped to exactly one
// repository with issues: write only; it is public by construction
// (extractable from a release binary), which is accepted because the
// blast radius is deliberately minimal. It never appears in logs or
// repository text.
var issueToken = ""

// SetIssueToken sets the bundled issue token. It is exported for two
// callers: the ldflags injection path (see issueToken) and tests, which
// point the publish flow at an httptest server with a fake token.
func SetIssueToken(t string) {
	issueToken = t
}

// issueAPIURL is the fixed target of issue creation (ADR-026 §Decision
// 3): the GitHub REST issues endpoint of the EKA repository. It is a
// var (not a const) so in-package tests can point it at an httptest
// server; production always uses the pinned endpoint. SetIssueAPIURL is
// the exported test/dev override the CLI tests use for the same reason.
var issueAPIURL = "https://api.github.com/repos/maleolabs/eka-cli/issues"

// SetIssueAPIURL overrides the issue-creation endpoint. Exported for
// tests (the cmd package end-to-end tests point the publish flow at an
// httptest server); production callers never touch it.
func SetIssueAPIURL(url string) {
	issueAPIURL = url
}

// issueClient is the standard HTTP client of the publish flow: it
// mirrors the update client (cmd/update.go, ADR-024) — proxy from the
// environment and bounded connection phases (dial, TLS handshake,
// response headers). There is deliberately no global Client.Timeout:
// the request carries its own deadline through the context.
var issueClient = &http.Client{Transport: &http.Transport{
	Proxy:                 http.ProxyFromEnvironment,
	DialContext:           (&net.Dialer{Timeout: issueConnectTimeout}).DialContext,
	TLSHandshakeTimeout:   issueTLSHandshakeTimeout,
	ResponseHeaderTimeout: issueResponseHeaderTimeout,
}}

// Connection-phase timeouts of the issue client.
const (
	issueConnectTimeout        = 10 * time.Second
	issueTLSHandshakeTimeout   = 10 * time.Second
	issueResponseHeaderTimeout = 30 * time.Second
)

// IssueClient creates GitHub issues on the fixed target repository with
// the bundled token.
type IssueClient struct {
	// Token is the fine-grained PAT (issues: write only).
	Token string
	// HTTP is the transport; production uses the package default
	// (issueClient), tests inject their own client when needed.
	HTTP *http.Client
}

// issueRequest is the POST body of issue creation. Deliberately no
// labels: an unknown label fails the whole request (ADR-026 §Decision
// 3 — the repository stays in control of labels).
type issueRequest struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

// issueResponse is the created-issue payload subset we need: the issue
// number and its HTML URL.
type issueResponse struct {
	Number  int    `json:"number"`
	HTMLURL string `json:"html_url"`
}

// APIError is a non-2xx GitHub API response: the HTTP status and the
// API's message (e.g. "Bad credentials" on 401). The CLI maps it to the
// refusal class (exit 1).
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string {
	text := http.StatusText(e.Status)
	if e.Message == "" {
		return fmt.Sprintf("GitHub API returned %d %s", e.Status, text)
	}
	return fmt.Sprintf("GitHub API returned %d %s: %s", e.Status, text, e.Message)
}

// CreateIssue creates one GitHub issue on the fixed target repository
// and returns its number and html_url. Any non-2xx response surfaces as
// *APIError carrying the status and the API's message. Network and
// transport failures are returned unwrapped.
func (c *IssueClient) CreateIssue(ctx context.Context, title, body string) (number int, htmlURL string, err error) {
	client := c.HTTP
	if client == nil {
		client = issueClient
	}
	payload, err := json.Marshal(issueRequest{Title: title, Body: body})
	if err != nil {
		return 0, "", fmt.Errorf("cannot encode the issue request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, issueAPIURL, bytes.NewReader(payload))
	if err != nil {
		return 0, "", fmt.Errorf("cannot build the issue request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return 0, "", fmt.Errorf("cannot create the issue: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return 0, "", apiErrorFromResponse(resp)
	}
	var created issueResponse
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		return 0, "", fmt.Errorf("cannot parse the created issue: %w", err)
	}
	if created.Number == 0 || created.HTMLURL == "" {
		return 0, "", fmt.Errorf("GitHub returned an issue without number or html_url")
	}
	return created.Number, created.HTMLURL, nil
}

// apiErrorFromResponse extracts the APIError from a non-2xx response:
// the status plus the API's "message" field (or the raw body as a
// fallback). The body read is bounded — an unbounded error body could
// be abused.
func apiErrorFromResponse(resp *http.Response) error {
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if err == nil {
		var apiErr struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(body, &apiErr) == nil && apiErr.Message != "" {
			return &APIError{Status: resp.StatusCode, Message: apiErr.Message}
		}
		if msg := strings.TrimSpace(string(body)); msg != "" {
			return &APIError{Status: resp.StatusCode, Message: msg}
		}
	}
	return &APIError{Status: resp.StatusCode}
}
