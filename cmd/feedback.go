package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	gort "runtime"
	"strconv"
	"strings"
	"time"

	"github.com/maleolabs/eka-cli/cmd/ui"
	"github.com/maleolabs/eka-cli/feedback"
	"github.com/maleolabs/eka-core/runtime"
	"github.com/spf13/cobra"
)

// This file implements `eka feedback` (ADR-026): report EKA feedback as
// a local draft (EKA_HOME/feedback/<id>.md — YAML frontmatter + markdown
// body) and publish it as a GitHub issue on the fixed target repository.
// The command is a thin Cobra layer: the home path resolves through
// runtime.HomeDir() and the feedback package owns the file format, the
// store and the GitHub client. Production code here imports neither
// store, workspace nor sync — the client-only boundary holds.
//
// Exit codes:
//
//	0  success (draft created, published, or the list rendered)
//	1  refusal (already published, empty/unbundled token, network, API
//	   error; a declined publish prompt)
//	2  usage or internal error (missing/invalid flags, unknown feedback
//	   id, a non-interactive publish without --yes, an unreadable list)
//
// --json emits the deterministic machine report (schemas
// "eka-feedback-new-v1", "eka-feedback-publish-v1",
// "eka-feedback-list-v1"); the default output is the human report in
// the CLI house style.

// Machine-report schemas of the feedback commands (the eka-note-v1
// convention; pinned field order).
const (
	feedbackNewSchema     = "eka-feedback-new-v1"
	feedbackPublishSchema = "eka-feedback-publish-v1"
	feedbackListSchema    = "eka-feedback-list-v1"
)

// feedbackNewJSON is the machine report of one `feedback new` run.
type feedbackNewJSON struct {
	Schema string `json:"schema"`
	OK     bool   `json:"ok"`
	ID     string `json:"id,omitempty"`
	Path   string `json:"path,omitempty"`
	Status string `json:"status,omitempty"`
}

// feedbackPublishJSON is the machine report of one `feedback publish`
// run.
type feedbackPublishJSON struct {
	Schema      string `json:"schema"`
	OK          bool   `json:"ok"`
	ID          string `json:"id,omitempty"`
	IssueNumber int    `json:"issueNumber,omitempty"`
	IssueURL    string `json:"issueUrl,omitempty"`
}

// feedbackListItemJSON is one feedback entry of the list machine
// report: the full triage record without the markdown body.
type feedbackListItemJSON struct {
	ID          string `json:"id"`
	Type        string `json:"type"`
	Title       string `json:"title"`
	Severity    string `json:"severity"`
	Source      string `json:"source"`
	EkaVersion  string `json:"ekaVersion"`
	OS          string `json:"os"`
	Command     string `json:"command"`
	Status      string `json:"status"`
	IssueURL    string `json:"issueUrl,omitempty"`
	IssueNumber int    `json:"issueNumber,omitempty"`
	Created     string `json:"created"`
}

// feedbackListJSON is the machine report of `feedback list`.
type feedbackListJSON struct {
	Schema   string                 `json:"schema"`
	OK       bool                   `json:"ok"`
	Feedback []feedbackListItemJSON `json:"feedback"`
}

// feedbackFailJSON is the machine report of a refused/usage-class
// feedback run (ok: false, reason on the failure).
type feedbackFailJSON struct {
	Schema string `json:"schema"`
	OK     bool   `json:"ok"`
	Reason string `json:"reason,omitempty"`
}

// feedbackIssueTimeout bounds one publish run's network phase (issue
// creation is a single POST; the connection phases are already bounded
// by the transport).
const feedbackIssueTimeout = 60 * time.Second

// newFeedbackCommand builds the `eka feedback` command group.
func newFeedbackCommand() *cobra.Command {
	feedbackCmd := &cobra.Command{
		Use:   "feedback",
		Short: "Report EKA feedback (draft → publish as a GitHub issue)",
		Long: `Report EKA feedback to the EKA maintainers: create a local
draft under EKA_HOME/feedback (<id>.md — YAML frontmatter + markdown
body) and publish it as a GitHub issue on the fixed target repository
(maleolabs/eka-cli). Feedback is
meta-information about the tool — it never enters the canonical store
and never becomes engineering knowledge.

Subcommands:
  new       create a feedback draft (writes EKA_HOME/feedback/<id>.md)
  publish   file a draft as a GitHub issue (requires a release binary)
  list      show all local feedback (drafts and published)`,
	}
	feedbackCmd.AddCommand(newFeedbackNewCommand(), newFeedbackPublishCommand(), newFeedbackListCommand())
	return feedbackCmd
}

// newFeedbackNewCommand builds `eka feedback new --type <t> --title
// "<t>" [--severity <s>] [--source <s>] [--command <c>]
// [--content-file <f>] [--json]`.
func newFeedbackNewCommand() *cobra.Command {
	var (
		typeFlag     string
		titleFlag    string
		severityFlag string
		sourceFlag   string
		commandFlag  string
		contentFile  string
		jsonOut      bool
	)
	cmd := &cobra.Command{
		Use:   "new --type <type> --title <title>",
		Short: "Create a feedback draft",
		Long: `Create a feedback draft under EKA_HOME/feedback/<id>.md: the
YAML frontmatter carries the full triage record (id, type, title,
severity, source, eka_version, os, command, status, created — ADR-026)
and the file body is the markdown report that becomes the GitHub issue
body at publish time.

--type and --title are required. --severity is low, medium or high
(default low); --source is human or agent (default human). The triage
metadata is auto-injected: eka_version from the CLI build version, os
from GOOS/GOARCH, command from the invoking command line (override with
--command), created from today.

Without --content-file the per-type empty body is scaffolded: a bug
report carries "## Steps to reproduce / ## Expected / ## Actual", the
other types a "## Description" section.

Exit codes:
  0  draft created
  2  usage or internal error (missing/invalid flags, unreadable file)`,
		Example: `  eka feedback new --type bug --title "eka sync refuses on empty repo" --content-file report.md
  eka feedback new --type suggestion --title "Add a --filter flag to eka list" --severity medium --command "eka list --all"
  eka feedback new --type question --title "How is feedback triaged?" --json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runFeedbackNew(cmd, feedbackNewFlags{
				typ: typeFlag, title: titleFlag, severity: severityFlag,
				source: sourceFlag, command: commandFlag,
				contentFile: contentFile, jsonOut: jsonOut,
			})
		},
	}
	cmd.Flags().StringVar(&typeFlag, "type", "", "feedback type: bug, suggestion, improvement, or question (required)")
	cmd.Flags().StringVar(&titleFlag, "title", "", "feedback title (required)")
	cmd.Flags().StringVar(&severityFlag, "severity", feedback.SeverityLow, "feedback severity: low, medium, or high")
	cmd.Flags().StringVar(&sourceFlag, "source", "human", "feedback source: human or agent")
	cmd.Flags().StringVar(&commandFlag, "command", "", "the invoked command recorded in the report (default: the full command line)")
	cmd.Flags().StringVar(&contentFile, "content-file", "", "markdown feedback body (default: the per-type scaffold)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit the deterministic machine report (schema eka-feedback-new-v1)")
	return cmd
}

// feedbackNewFlags carries the flag set of `feedback new`.
type feedbackNewFlags struct {
	typ, title, severity, source, command, contentFile string
	jsonOut                                            bool
}

// runFeedbackNew executes `eka feedback new`.
func runFeedbackNew(cmd *cobra.Command, f feedbackNewFlags) error {
	if f.typ == "" {
		return feedbackUsage(cmd, feedbackNewSchema, f.jsonOut, "feedback requires --type (bug, suggestion, improvement, or question)")
	}
	if f.title == "" {
		return feedbackUsage(cmd, feedbackNewSchema, f.jsonOut, "feedback requires --title")
	}
	switch f.severity {
	case feedback.SeverityLow, feedback.SeverityMedium, feedback.SeverityHigh:
	default:
		return feedbackUsage(cmd, feedbackNewSchema, f.jsonOut, fmt.Sprintf("invalid --severity %q (low, medium, or high)", f.severity))
	}
	switch f.source {
	case "human", "agent":
	default:
		return feedbackUsage(cmd, feedbackNewSchema, f.jsonOut, fmt.Sprintf("invalid --source %q (human or agent)", f.source))
	}
	body, err := feedbackBody(f.contentFile, f.typ)
	if err != nil {
		return feedbackUsage(cmd, feedbackNewSchema, f.jsonOut, err.Error())
	}
	home, err := runtime.HomeDir()
	if err != nil {
		return err // Exit 2: workspace resolution.
	}
	command := f.command
	if command == "" {
		command = strings.Join(os.Args, " ")
	}
	now := time.Now()
	fb := &feedback.Feedback{
		Type:       f.typ,
		Title:      f.title,
		Severity:   f.severity,
		Source:     f.source,
		EkaVersion: version,
		OS:         gort.GOOS + "/" + gort.GOARCH,
		Command:    command,
		Status:     feedback.StatusDraft,
		Created:    now.Format("2006-01-02"),
		Body:       body,
	}
	st := feedback.New(home)
	fb.ID = st.NewID(fb.Title, now)
	if err := st.Save(fb); err != nil {
		return fmt.Errorf("feedback: %w", err) // Exit 2: internal.
	}
	path := filepath.Join(st.Dir, fb.ID+".md")
	if f.jsonOut {
		return emitJSON(cmd, feedbackNewJSON{
			Schema: feedbackNewSchema,
			OK:     true,
			ID:     fb.ID,
			Path:   path,
			Status: fb.Status,
		})
	}
	s := styleFor(cmd)
	ui.NewHeader(s, "Feedback").
		Add("Type", fb.Type).
		Add("Title", fb.Title).
		Add("Status", fb.Status).
		Pipeline("New").
		Render()
	ui.NewSummary(s).
		Add("ID", fb.ID).
		Add("Path", path).
		Add("Next", "eka feedback publish "+fb.ID+" to file the GitHub issue").
		Render()
	return nil
}

// feedbackBody resolves the markdown body: the --content-file contents,
// or the per-type empty scaffold (bug: reproduce/expected/actual; the
// other types: description).
func feedbackBody(contentFile, typ string) (string, error) {
	if contentFile != "" {
		data, err := os.ReadFile(contentFile)
		if err != nil {
			return "", fmt.Errorf("cannot read --content-file: %w", err)
		}
		return string(data), nil
	}
	if typ == feedback.TypeBug {
		return "## Steps to reproduce\n\n## Expected\n\n## Actual\n", nil
	}
	return "## Description\n\n", nil
}

// newFeedbackPublishCommand builds `eka feedback publish <id> [--yes]
// [--json]`.
func newFeedbackPublishCommand() *cobra.Command {
	var yes bool
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "publish <id>",
		Short: "Publish a feedback draft as a GitHub issue",
		Long: `Publish a feedback draft as a GitHub issue on the fixed target
repository (maleolabs/eka-cli), then rewrite
the draft file with status: published plus the issue number and URL.
The publish credential is bundled into the binary at build time — a
dev/test build refuses with a release-binary hint.

The operation is idempotent: an already-published feedback refuses
instead of creating a duplicate issue.

On a terminal the title and the target repository are confirmed before
anything is sent; non-interactive runs (pipes, CI) require --yes — a
piped run never blocks on an invisible prompt.

Exit codes:
  0  published
  1  refusal (already published, no bundled token, network or API error)
  2  usage or internal error (unknown id, non-interactive without --yes)`,
		Example: `  eka feedback publish fbk-20260812-refactor-suggestion
  eka feedback publish fbk-20260812-refactor-suggestion --yes
  eka feedback publish fbk-20260812-refactor-suggestion --json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runFeedbackPublish(cmd, args[0], yes, jsonOut)
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "publish without the confirmation prompt (non-interactive runs)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit the deterministic machine report (schema eka-feedback-publish-v1)")
	return cmd
}

// runFeedbackPublish executes `eka feedback publish <id>`.
func runFeedbackPublish(cmd *cobra.Command, id string, yes, jsonOut bool) error {
	home, err := runtime.HomeDir()
	if err != nil {
		return err // Exit 2: workspace resolution.
	}
	st := feedback.New(home)
	f, err := st.Load(id)
	if err != nil {
		if errors.Is(err, feedback.ErrNotFound) {
			return feedbackUsage(cmd, feedbackPublishSchema, jsonOut, fmt.Sprintf("unknown feedback %q", id))
		}
		return feedbackUsage(cmd, feedbackPublishSchema, jsonOut, err.Error()) // Malformed id: usage class.
	}
	if f.Status == feedback.StatusPublished {
		return feedbackRefused(cmd, feedbackPublishSchema, jsonOut,
			fmt.Sprintf("already published as #%d %s", f.IssueNumber, f.IssueURL))
	}
	s := styleFor(cmd)
	// Determinism gate, BEFORE any publish work: a piped run without
	// --yes must never block on a prompt the user cannot see (ADR-024
	// pattern) — it refuses with the --yes hint (usage class, exit 2).
	if !yes && !(s.TTY && isTTYReader(cmd.InOrStdin())) {
		return feedbackUsage(cmd, feedbackPublishSchema, jsonOut, "publish requires --yes outside a terminal")
	}
	// Interactive confirmation — wired only when --yes is NOT set and
	// stdin is a real terminal (the ui.Select contract; the non-TTY
	// path was already refused by the determinism gate).
	if !yes {
		prompt := fmt.Sprintf("Publish feedback %q as a GitHub issue on maleolabs/eka-cli?", f.Title)
		value, perr := ui.Select(s, cmd.InOrStdin(), cmd.OutOrStdout(), prompt,
			[]ui.MenuItem{{Title: "publish", Value: "publish"}, {Title: "abort", Value: "abort"}}, 0)
		if perr != nil {
			if errors.Is(perr, ui.ErrCancelled) {
				return feedbackRefused(cmd, feedbackPublishSchema, jsonOut, "publish aborted")
			}
			return fmt.Errorf("feedback: %w", perr) // Exit 2: internal.
		}
		if value != "publish" {
			return feedbackRefused(cmd, feedbackPublishSchema, jsonOut, "publish aborted")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), feedbackIssueTimeout)
	defer cancel()
	published, err := feedback.Publish(ctx, home, id)
	if err != nil {
		// Refusal class (exit 1): already-published and token refusals
		// were pre-flighted above; the remaining failures are network,
		// API and write errors.
		return feedbackRefused(cmd, feedbackPublishSchema, jsonOut, err.Error())
	}
	if jsonOut {
		return emitJSON(cmd, feedbackPublishJSON{
			Schema:      feedbackPublishSchema,
			OK:          true,
			ID:          published.ID,
			IssueNumber: published.IssueNumber,
			IssueURL:    published.IssueURL,
		})
	}
	fmt.Fprintf(s.W, "%s\n", s.Success(ui.IconDone+" Published: #"+strconv.Itoa(published.IssueNumber)+" "+published.IssueURL))
	ui.NewSummary(s).
		Add("ID", published.ID).
		Add("Issue", fmt.Sprintf("#%d %s", published.IssueNumber, published.IssueURL)).
		Render()
	return nil
}

// newFeedbackListCommand builds `eka feedback list [--json]`: the
// deterministic feedback table (id, type, title, status, issue URL),
// newest first. Informational: exit 0 even when there is no feedback.
func newFeedbackListCommand() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List local feedback drafts and published reports",
		Long: `List all local feedback under EKA_HOME/feedback in the themed CLI
style (icons + color on a TTY, plain text otherwise): each row shows
the id, type, title, status and, for published reports, the GitHub
issue link.

Ordering is deterministic: id descending (newest first — ids embed
the report date).

Exit codes:
  0  informational (also when there is no feedback)
  2  internal error (unreadable feedback file)`,
		Example: `  eka feedback list
  eka feedback list --json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			home, err := runtime.HomeDir()
			if err != nil {
				return err // Exit 2: workspace resolution.
			}
			items, err := feedback.New(home).List()
			if err != nil {
				return fmt.Errorf("feedback: %w", err) // Exit 2: internal (first malformed file).
			}
			if jsonOut {
				list := make([]feedbackListItemJSON, 0, len(items))
				for _, f := range items {
					list = append(list, feedbackListItemJSON{
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
						Created:     f.Created,
					})
				}
				return emitJSON(cmd, feedbackListJSON{Schema: feedbackListSchema, OK: true, Feedback: list})
			}
			renderFeedbackList(cmd, items)
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit the deterministic machine report (schema eka-feedback-list-v1)")
	return cmd
}

// renderFeedbackList renders the feedback table deterministically: the
// accent heading, the aligned table (id, type, title — truncated —,
// status, issue URL when published) and the summary counts. An empty
// list renders the informative line with the create hint.
func renderFeedbackList(cmd *cobra.Command, items []*feedback.Feedback) {
	s := styleFor(cmd)
	fmt.Fprintln(s.W, s.Accent("Feedback"))
	if len(items) == 0 {
		fmt.Fprintf(s.W, "\n%s\n", s.Info("No feedback yet. Run 'eka feedback new' to create a draft."))
		return
	}
	tbl := ui.NewTable(s, "ID", "Type", "Title", "Status", "Issue")
	for _, f := range items {
		issue := ""
		if f.Status == feedback.StatusPublished {
			issue = fmt.Sprintf("#%d %s", f.IssueNumber, f.IssueURL)
		}
		tbl.AddRow([]string{f.ID, f.Type, truncateRunes(f.Title, 48), f.Status, issue}, nil)
	}
	tbl.Render()
	ui.NewSummary(s).
		Add("Feedback", plural(len(items), "report", "reports")).
		Render()
}

// feedbackUsage renders a usage-class failure (exit 2) of a feedback
// command: the machine report (with --json) and the single-line human
// error on stderr.
func feedbackUsage(cmd *cobra.Command, schema string, jsonOut bool, message string) error {
	if jsonOut {
		_ = emitJSON(cmd, feedbackFailJSON{Schema: schema, OK: false, Reason: message})
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "eka: %s\n", message)
	return &exitError{code: exitUsage}
}

// feedbackRefused renders a deterministic refusal (exit 1) of a
// feedback command: the machine report (with --json) and the
// single-line human refusal on stderr.
func feedbackRefused(cmd *cobra.Command, schema string, jsonOut bool, message string) error {
	if jsonOut {
		_ = emitJSON(cmd, feedbackFailJSON{Schema: schema, OK: false, Reason: message})
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "eka: feedback refused: %s\n", message)
	return &exitError{code: exitFail}
}
