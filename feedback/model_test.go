package feedback

import (
	"strings"
	"testing"
)

// sampleFeedback builds the canonical draft fixture shared by the
// model tests.
func sampleFeedback() *Feedback {
	return &Feedback{
		ID:         "fbk-20260812-cli-refusal",
		Type:       TypeBug,
		Title:      "eka sync refuses on an empty repository",
		Severity:   SeverityHigh,
		Source:     "agent",
		EkaVersion: "0.6.9",
		OS:         "linux/amd64",
		Command:    "eka sync",
		Status:     StatusDraft,
		Created:    "2026-08-12",
		Body:       "## Steps to reproduce\n\nRun eka sync on an empty repo.\n\n## Expected\n\nIt works.\n\n## Actual\n\nIt refuses.\n",
	}
}

func TestMarshalFormat(t *testing.T) {
	f := sampleFeedback()
	data, err := f.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.HasPrefix(text, "---\n") {
		t.Errorf("marshal must open the frontmatter block, got:\n%s", text)
	}
	// The ADR-026 §Decision 1 key order is the serialization contract:
	// id, type, title, severity, source, eka_version, os, command,
	// status, issue_url, issue_number, created.
	wantOrder := []string{
		"id: fbk-20260812-cli-refusal",
		"type: bug",
		"title: eka sync refuses on an empty repository",
		"severity: high",
		"source: agent",
		"eka_version: 0.6.9",
		"os: linux/amd64",
		"command: eka sync",
		"status: draft",
		"created: 2026-08-12",
	}
	pos := -1
	for _, want := range wantOrder {
		i := strings.Index(text, want)
		if i < 0 {
			t.Errorf("marshal must contain %q, got:\n%s", want, text)
			continue
		}
		if i < pos {
			t.Errorf("frontmatter key order broken: %q appears after a later key", want)
		}
		pos = i
	}
	// Draft: the issue fields are omitted; the body follows the closing
	// delimiter.
	if strings.Contains(text, "issue_url") || strings.Contains(text, "issue_number") {
		t.Errorf("a draft must not carry issue fields:\n%s", text)
	}
	if !strings.Contains(text, "---\n## Steps to reproduce") {
		t.Errorf("the body must follow the closing delimiter:\n%s", text)
	}
}

func TestMarshalParseRoundtrip(t *testing.T) {
	f := sampleFeedback()
	data, err := f.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	got, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if *got != *f {
		t.Errorf("roundtrip mismatch:\ngot  %+v\nwant %+v", *got, *f)
	}
	// The serialization is stable: a second roundtrip is identical.
	data2, err := got.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if string(data2) != string(data) {
		t.Errorf("roundtrip is not byte-stable:\nfirst:\n%s\nsecond:\n%s", data, data2)
	}
}

func TestParsePublishedFields(t *testing.T) {
	data := `---
id: fbk-20260812-cli-refusal
type: bug
title: t
severity: high
source: agent
eka_version: 0.6.9
os: linux/amd64
command: eka sync
status: published
issue_url: https://github.com/maleolabs/eka-cli/issues/7
issue_number: 7
created: 2026-08-12
---
## Steps to reproduce
`
	f, err := Parse([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	if f.Status != StatusPublished || f.IssueNumber != 7 ||
		f.IssueURL != "https://github.com/maleolabs/eka-cli/issues/7" {
		t.Errorf("parsed published record = %+v, want status published + issue fields", f)
	}
	if f.Body != "## Steps to reproduce\n" {
		t.Errorf("body = %q, want the body after the closing delimiter", f.Body)
	}
}

func TestParseErrors(t *testing.T) {
	for name, data := range map[string]string{
		"no-frontmatter": "just a body\n",
		"unclosed":       "---\nid: x\n",
		"bad-yaml":       "---\nid: [unclosed\n---\nbody\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(data)); err == nil {
				t.Errorf("Parse(%q) must fail", data)
			}
		})
	}
}

func TestValidate(t *testing.T) {
	for name, mutate := range map[string]func(*Feedback){
		"type":        func(f *Feedback) { f.Type = "rant" },
		"severity":    func(f *Feedback) { f.Severity = "critical" },
		"status":      func(f *Feedback) { f.Status = "archived" },
		"empty-title": func(f *Feedback) { f.Title = "  " },
	} {
		t.Run(name, func(t *testing.T) {
			f := sampleFeedback()
			mutate(f)
			if err := f.Validate(); err == nil {
				t.Errorf("Validate must reject the %s mutation", name)
			}
		})
	}
	// The canonical fixture validates.
	if err := sampleFeedback().Validate(); err != nil {
		t.Errorf("the sample feedback must validate: %v", err)
	}
}

func TestValidateAcceptedValues(t *testing.T) {
	types := []string{TypeBug, TypeSuggestion, TypeImprovement, TypeQuestion}
	severities := []string{SeverityLow, SeverityMedium, SeverityHigh}
	statuses := []string{StatusDraft, StatusPublished}
	for _, typ := range types {
		for _, sev := range severities {
			for _, st := range statuses {
				f := sampleFeedback()
				f.Type, f.Severity, f.Status = typ, sev, st
				if err := f.Validate(); err != nil {
					t.Errorf("type %q severity %q status %q must validate: %v", typ, sev, st, err)
				}
			}
		}
	}
}

func TestIssueBody(t *testing.T) {
	f := sampleFeedback()
	body := f.IssueBody()
	for _, want := range []string{
		"**Type:** bug",
		"**Severity:** high",
		"**Source:** agent",
		"**EKA version:** 0.6.9",
		"**OS:** linux/amd64",
		"**Command:** `eka sync`",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("issue body must carry %q, got:\n%s", want, body)
		}
	}
	// The header is followed by a blank line and the report body
	// (ADR-026 §Decision 4).
	if !strings.Contains(body, "`eka sync`\n\n## Steps to reproduce") {
		t.Errorf("issue body must separate header and body with a blank line:\n%s", body)
	}
}
