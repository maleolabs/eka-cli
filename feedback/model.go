// Package feedback implements the EKA feedback module (ADR-026): plain
// files under EKA_HOME/feedback/<id>.md carrying YAML frontmatter +
// markdown body, and the publish path that files a draft as a GitHub
// issue on the fixed target repository.
//
// The package is standalone by design: it imports neither store,
// workspace nor sync (the Runtime Kernel internals), and the CLI passes
// the home directory in explicitly. Feedback is meta-information about
// the tool addressed at the EKA maintainers — never a CKO, never a unit
// of the canonical store.
package feedback

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// Feedback type values (ADR-026 §Decision 1).
const (
	TypeBug         = "bug"
	TypeSuggestion  = "suggestion"
	TypeImprovement = "improvement"
	TypeQuestion    = "question"
)

// Feedback severity values.
const (
	SeverityLow    = "low"
	SeverityMedium = "medium"
	SeverityHigh   = "high"
)

// Feedback status values: draft (created locally, not yet filed) and
// published (filed as a GitHub issue, number + URL written by publish).
const (
	StatusDraft     = "draft"
	StatusPublished = "published"
)

// Feedback is one local feedback report: the triage record (ADR-026
// §Decision 1) plus the markdown body that becomes the GitHub issue
// body. Created is the report date (YYYY-MM-DD); issue_number and
// issue_url are written by publish.
type Feedback struct {
	ID          string
	Type        string
	Title       string
	Severity    string
	Source      string
	EkaVersion  string
	OS          string
	Command     string
	Status      string
	IssueURL    string
	IssueNumber int
	Created     string
	Body        string
}

// frontmatter is the deterministic YAML serialization of the triage
// record. Field order is fixed — it IS the serialization contract
// (ADR-026 §Decision 1): id, type, title, severity, source,
// eka_version, os, command, status, issue_url, issue_number, created.
// yaml.v3 struct marshaling emits fields in declaration order; the
// issue fields are omitted while the report is a draft.
type frontmatter struct {
	ID          string   `yaml:"id"`
	Type        string   `yaml:"type"`
	Title       string   `yaml:"title"`
	Severity    string   `yaml:"severity"`
	Source      string   `yaml:"source"`
	EkaVersion  string   `yaml:"eka_version"`
	OS          string   `yaml:"os"`
	Command     string   `yaml:"command"`
	Status      string   `yaml:"status"`
	IssueURL    string   `yaml:"issue_url,omitempty"`
	IssueNumber int      `yaml:"issue_number,omitempty"`
	Created     yamlDate `yaml:"created"`
}

// yamlDate renders a YYYY-MM-DD string as a plain (unquoted) YAML
// scalar. yaml.v3 quotes timestamp-shaped strings by default
// ("created: \"2026-08-12\""), but the ADR-026 serialization contract
// shows the bare date — the field is therefore emitted with the
// !!timestamp tag. Parsing such a scalar into a string field yields
// the literal text, so the roundtrip is exact.
type yamlDate string

func (d yamlDate) MarshalYAML() (any, error) {
	return yaml.Node{Kind: yaml.ScalarNode, Tag: "!!timestamp", Value: string(d)}, nil
}

// UnmarshalYAML accepts the literal text of any scalar for the date
// field (timestamp, quoted, or int-like "20260810" in a hand-edited
// file) — the field is a date string, never a parsed time.
func (d *yamlDate) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.ScalarNode {
		return fmt.Errorf("created must be a scalar date")
	}
	*d = yamlDate(node.Value)
	return nil
}

// frontmatterOf projects the Feedback onto the serialization struct.
func (f *Feedback) frontmatterOf() frontmatter {
	return frontmatter{
		ID:          f.ID,
		Type:        f.Type,
		Title:       f.Title,
		Severity:    f.Severity,
		Source:      f.Source,
		EkaVersion:  f.EkaVersion,
		OS:          f.OS,
		Command:     f.Command,
		Status:      f.Status,
		IssueURL:    f.IssueURL,
		IssueNumber: f.IssueNumber,
		Created:     yamlDate(f.Created),
	}
}

// Marshal renders the feedback file: the YAML frontmatter block
// (delimited by "---" lines) followed by the markdown body. The body
// always ends with exactly one trailing newline, so the serialization
// is a stable roundtrip (Parse(Marshal(f)) == f).
func (f *Feedback) Marshal() ([]byte, error) {
	fm := f.frontmatterOf()
	data, err := yaml.Marshal(&fm)
	if err != nil {
		return nil, fmt.Errorf("feedback: cannot encode the frontmatter: %w", err)
	}
	body := f.Body
	if body == "" {
		body = "\n"
	}
	if !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	var out strings.Builder
	out.WriteString("---\n")
	out.Write(data)
	out.WriteString("---\n")
	out.WriteString(body)
	return []byte(out.String()), nil
}

// Parse reads a feedback file (YAML frontmatter + markdown body) back
// into a Feedback. The body is everything after the closing "---"
// delimiter line, trailing newline preserved.
func Parse(data []byte) (*Feedback, error) {
	lines := strings.Split(string(data), "\n")
	if len(lines) == 0 || strings.TrimRight(lines[0], "\r") != "---" {
		return nil, fmt.Errorf("feedback: file must start with a YAML frontmatter block (---)")
	}
	closeIdx := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimRight(lines[i], "\r") == "---" {
			closeIdx = i
			break
		}
	}
	if closeIdx < 0 {
		return nil, fmt.Errorf("feedback: frontmatter block starts with --- but never closes")
	}
	var fm frontmatter
	if err := yaml.Unmarshal([]byte(strings.Join(lines[1:closeIdx], "\n")), &fm); err != nil {
		return nil, fmt.Errorf("feedback: frontmatter is not valid YAML: %w", err)
	}
	f := &Feedback{
		ID:          fm.ID,
		Type:        fm.Type,
		Title:       fm.Title,
		Severity:    fm.Severity,
		Source:      fm.Source,
		EkaVersion:  fm.EkaVersion,
		OS:          fm.OS,
		Command:     fm.Command,
		Status:      fm.Status,
		IssueURL:    fm.IssueURL,
		IssueNumber: fm.IssueNumber,
		Created:     string(fm.Created),
		Body:        strings.Join(lines[closeIdx+1:], "\n"),
	}
	return f, nil
}

// Validate enforces the closed value sets of the triage record:
// type, severity and status must be valid values. Title must be
// non-empty (the create path requires it; a hand-edited file without
// one must not silently publish).
func (f *Feedback) Validate() error {
	switch f.Type {
	case TypeBug, TypeSuggestion, TypeImprovement, TypeQuestion:
	default:
		return fmt.Errorf("invalid type %q (bug, suggestion, improvement, or question)", f.Type)
	}
	switch f.Severity {
	case SeverityLow, SeverityMedium, SeverityHigh:
	default:
		return fmt.Errorf("invalid severity %q (low, medium, or high)", f.Severity)
	}
	switch f.Status {
	case StatusDraft, StatusPublished:
	default:
		return fmt.Errorf("invalid status %q (draft or published)", f.Status)
	}
	if strings.TrimSpace(f.Title) == "" {
		return fmt.Errorf("title must not be empty")
	}
	return nil
}

// IssueBody renders the GitHub issue body of the report (ADR-026
// §Decision 4): the markdown report header built from the triage
// fields — type, severity, source, eka_version, os, command — then a
// blank line, then the feedback markdown body. Version/OS/command
// metadata is what makes a one-line agent report triageable.
func (f *Feedback) IssueBody() string {
	var b strings.Builder
	fmt.Fprintf(&b, "**Type:** %s\n", f.Type)
	fmt.Fprintf(&b, "**Severity:** %s\n", f.Severity)
	fmt.Fprintf(&b, "**Source:** %s\n", f.Source)
	fmt.Fprintf(&b, "**EKA version:** %s\n", f.EkaVersion)
	fmt.Fprintf(&b, "**OS:** %s\n", f.OS)
	fmt.Fprintf(&b, "**Command:** `%s`\n", f.Command)
	b.WriteString("\n")
	b.WriteString(f.Body)
	if !strings.HasSuffix(b.String(), "\n") {
		b.WriteString("\n")
	}
	return b.String()
}
