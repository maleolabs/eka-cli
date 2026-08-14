package feedback

import (
	"context"
	"errors"
	"fmt"
)

// Publish files a feedback draft as a GitHub issue on the fixed target
// repository and rewrites the local file with status: published plus
// the issue number and URL (ADR-026 §Decision 2/3).
//
// Refusals are deterministic:
//
//   - a missing id propagates ErrNotFound (the CLI maps it to the usage
//     class, exit 2)
//   - an already-published feedback refuses (idempotent — a second
//     publish must never create a duplicate issue)
//   - an empty token refuses with the release-binary hint (dev/test/CI
//     builds ship no token — the ADR-024 version == "dev" analogue)
//   - network and API failures refuse with the transport/APIError
//     wrapped message (the CLI maps the APIError class to exit 1)
//
// Returns the updated feedback (status published, number + URL set).
func Publish(ctx context.Context, home, id string) (*Feedback, error) {
	st := New(home)
	f, err := st.Load(id)
	if err != nil {
		return nil, err
	}
	if err := f.Validate(); err != nil {
		return nil, err
	}
	if f.Status == StatusPublished {
		return nil, fmt.Errorf("already published as #%d %s", f.IssueNumber, f.IssueURL)
	}
	if issueToken == "" {
		return nil, errors.New("issue token not bundled — use a release binary")
	}
	client := &IssueClient{Token: issueToken}
	number, htmlURL, err := client.CreateIssue(ctx, f.Title, f.IssueBody())
	if err != nil {
		return nil, err
	}
	if err := st.MarkPublished(id, number, htmlURL); err != nil {
		return nil, err
	}
	f.Status = StatusPublished
	f.IssueNumber = number
	f.IssueURL = htmlURL
	return f, nil
}
