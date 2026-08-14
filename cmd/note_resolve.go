package cmd

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/maleolabs/eka-cli/cmd/ui"
	"github.com/maleolabs/eka-core/runtime"
	"github.com/spf13/cobra"
)

// This file implements `eka note reply` and `eka note resolve`
// (ADR-019 D8 revised): the explicit reply and resolution commands of
// the note model. Resolving a note is a documented action — the
// note-state advances open -> resolved with a change-log entry and an
// authority identity, optionally documented by a reply note. Both
// commands operate through the Authoring API (never direct store
// access).
//
// Exit codes (both commands):
//
//	0  success (including already-resolved no-ops)
//	1  refusal (subject/parent unresolvable, repository/workspace
//	   state)
//	2  usage or internal error
//
// --json emits the deterministic machine reports ("eka-note-v1" for
// reply, "eka-note-resolve-v1" for resolve).

// noteResolveJSON is the deterministic machine report of one resolve
// run (schema "eka-note-resolve-v1"; pinned field order).
type noteResolveJSON struct {
	Schema    string   `json:"schema"`
	OK        bool     `json:"ok"`
	Target    string   `json:"target,omitempty"`
	All       bool     `json:"all,omitempty"`
	Resolved  []string `json:"resolved,omitempty"`
	Already   []string `json:"alreadyResolved,omitempty"`
	Replies   []string `json:"replies,omitempty"`
	Published bool     `json:"published,omitempty"`
	Path      string   `json:"path,omitempty"`
	Reason    string   `json:"reason,omitempty"`
	Hint      string   `json:"hint,omitempty"`
}

// noteResolveSchema is the schema id of the resolve machine report.
const noteResolveSchema = "eka-note-resolve-v1"

// newNoteReplyCommand builds `eka note reply <parent> --body <text>`.
func newNoteReplyCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "reply <parent> --body <text>",
		Short: "Reply to one note (single-parent reply)",
		Long: `Attach a reply comment to exactly one parent note (ADR-019 D8
revised): a cmt- note with role "reply" ({role, body}) wired to the
parent through the replies-to relationship — single-parent, never
nested. The reply inherits its context from the parent and never
satisfies a transition gate on its own.

The parent is a note line: <type>:<id> (unqualified) or
<ns>/cmt:<id> (qualified — the namespace must equal the
repository's). The parent must exist as a draft or a published unit.

The reply body comes from --body (the text) or --content-file (a
text file read verbatim). --domain declares the reply's contextable
Engineering Domain (any of the five canonical domains).

Exit codes:
  0  reply draft created
  1  refused (parent unresolvable, repository or workspace state)
  2  usage or internal error`,
		Example: `  eka note reply cmt:12-implementation --body "Looks good, ship it."
  eka note reply cmt:12-implementation --content-file review.txt --by agent-x --by-kind agent`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			jsonOut, _ := cmd.Flags().GetBool("json")
			bodyFlag, _ := cmd.Flags().GetString("body")
			contentFile, _ := cmd.Flags().GetString("content-file")
			if bodyFlag != "" && contentFile != "" {
				return noteUsage(cmd, jsonOut, "note reply: --body and --content-file are mutually exclusive")
			}
			body := bodyFlag
			if contentFile != "" {
				data, err := os.ReadFile(contentFile)
				if err != nil {
					return noteUsage(cmd, jsonOut, fmt.Sprintf("note reply: cannot read content file %s: %v", contentFile, err))
				}
				body = strings.TrimSpace(string(data))
			}
			byFlag, _ := cmd.Flags().GetString("by")
			byKindFlag, _ := cmd.Flags().GetString("by-kind")
			by, err := runtime.BySource(byFlag, byKindFlag, ".")
			if err != nil {
				return noteUsage(cmd, jsonOut, err.Error())
			}
			domainFlag, _ := cmd.Flags().GetString("domain")
			r, err := openAuthoringRuntime(cmd)
			if err != nil {
				return err // Exit 2: workspace resolution.
			}
			defer r.Close()
			parent := args[0]
			if strings.HasPrefix(parent, "#") {
				resolved, rerr := resolveNumberTargetInRepo(r, ".", parent, "note")
				if rerr != nil {
					return noteUsage(cmd, jsonOut, rerr.Error())
				}
				parent = resolved
			}
			res, err := runtime.Authoring.NoteReply(r, runtime.NoteReplyRequest{
				RepoPath: ".",
				Parent:   parent,
				By:       by,
				Body:     body,
				Domain:   domainFlag,
			})
			if err != nil {
				var refusal *runtime.NoteRefusal
				if errors.As(err, &refusal) {
					return noteRefused(cmd, jsonOut, refusal.Reason, refusal.Hint)
				}
				return fmt.Errorf("note: %w", err) // Exit 2: usage/internal.
			}
			if jsonOut {
				return emitJSON(cmd, noteJSON{
					Schema: noteSchema,
					OK:     true,
					ID:     res.ID,
					Target: res.Parent,
					By:     by.Name,
					Draft:  res.Path,
				})
			}
			s := styleFor(cmd)
			ui.NewHeader(s, "Reply").
				Add("Parent", res.Parent).
				Add("By", authorLabel(s, by)).
				Pipeline("Draft").
				Render()
			ui.NewSummary(s).
				Add("Reply", "cmt:"+res.ID).
				Add("Path", res.Path).
				Add("Next", "eka publish to persist the reply; eka note resolve to resolve the parent").
				Render()
			return nil
		},
	}
	cmd.Flags().String("body", "", "reply body text (required, or --content-file)")
	cmd.Flags().String("content-file", "", "reply body read from a text file")
	cmd.Flags().String("domain", "", "contextable engineering domain of the reply: architecture, discovery, execution, operations, planning")
	cmd.Flags().String("by", "", "reply authority name (default: `git config user.name`)")
	cmd.Flags().String("by-kind", "", "author identity kind: user, agent, or worker (default: user)")
	cmd.Flags().Bool("json", false, "emit the deterministic machine report (schema eka-note-v1)")
	return cmd
}

// newNoteResolveCommand builds `eka note resolve <target> [--all]
// [--reply <text>]`.
func newNoteResolveCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "resolve <target> [--all]",
		Short: "Resolve one note, or all notes of one unit",
		Long: `Explicitly resolve note(s) (ADR-019 D8 revised): the note-state
advances open -> resolved with a change-log entry and the authority
identity. Draft notes are updated in place (the R13 transition gates
see the resolved draft immediately); published notes (immutable) are
resolved through the publish pipeline — a new instance of the line.

Target forms:
  eka note resolve cmt:<id>              one note line
  eka note resolve <type>:<id> --all     EVERY open note discussing
                                         the subject unit (draft +
                                         published) — the canonical
                                         scope of one unit

--reply <text> (or --reply-file <path>) optionally attaches a reply
note documenting the resolution before the status change (status-only
resolution without it).

Exit codes:
  0  success (including already-resolved no-ops)
  1  refused (note/subject unresolvable, repository or workspace
     state)
  2  usage or internal error`,
		Example: `  eka note resolve cmt:12-implementation
  eka note resolve cmt:12-implementation --reply "Verified on staging."
  eka note resolve sto:12 --all --by agent-x --by-kind agent`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			jsonOut, _ := cmd.Flags().GetBool("json")
			all, _ := cmd.Flags().GetBool("all")
			replyFlag, _ := cmd.Flags().GetString("reply")
			replyFile, _ := cmd.Flags().GetString("reply-file")
			if replyFlag != "" && replyFile != "" {
				return noteUsage(cmd, jsonOut, "note resolve: --reply and --reply-file are mutually exclusive")
			}
			replyBody := replyFlag
			if replyFile != "" {
				data, err := os.ReadFile(replyFile)
				if err != nil {
					return noteUsage(cmd, jsonOut, fmt.Sprintf("note resolve: cannot read reply file %s: %v", replyFile, err))
				}
				replyBody = strings.TrimSpace(string(data))
			}
			byFlag, _ := cmd.Flags().GetString("by")
			byKindFlag, _ := cmd.Flags().GetString("by-kind")
			by, err := runtime.BySource(byFlag, byKindFlag, ".")
			if err != nil {
				return noteUsage(cmd, jsonOut, err.Error())
			}
			r, err := openAuthoringRuntime(cmd)
			if err != nil {
				return err // Exit 2: workspace resolution.
			}
			defer r.Close()
			target := args[0]
			group := ""
			if all {
				// The subject scope: any numbered line of the unit.
				group = ""
			} else {
				// Resolve ONE note: the note group.
				group = "note"
			}
			if strings.HasPrefix(target, "#") {
				resolved, rerr := resolveNumberTargetInRepo(r, ".", target, group)
				if rerr != nil {
					return noteUsage(cmd, jsonOut, rerr.Error())
				}
				target = resolved
			}
			if !all {
				res, err := runtime.Authoring.ResolveNote(r, runtime.ResolveNoteRequest{
					RepoPath:  ".",
					Target:    target,
					By:        by,
					ReplyBody: replyBody,
				})
				if err != nil {
					var refusal *runtime.NoteRefusal
					if errors.As(err, &refusal) {
						return noteRefused(cmd, jsonOut, refusal.Reason, refusal.Hint)
					}
					return fmt.Errorf("note resolve: %w", err) // Exit 2.
				}
				if jsonOut {
					doc := noteResolveJSON{
						Schema: noteResolveSchema, OK: true, Target: res.Target,
						Published: res.Published, Path: res.Path,
					}
					if res.AlreadyResolved {
						doc.Already = []string{res.Target}
					} else {
						doc.Resolved = []string{res.Target}
					}
					if res.ReplyID != "" {
						doc.Replies = []string{res.ReplyID}
					}
					return emitJSON(cmd, doc)
				}
				s := styleFor(cmd)
				ui.NewHeader(s, "Resolve Note").
					Add("Target", res.Target).
					Add("By", authorLabel(s, by)).
					Pipeline("Resolve").
					Render()
				ui.NewSummary(s).
					Add("Note", res.Target)
				if res.AlreadyResolved {
					ui.NewSummary(s).Add("Status", "already resolved (no change)").Render()
				} else if res.Published {
					ui.NewSummary(s).Add("Status", "resolved — published instance advanced").Render()
				} else {
					ui.NewSummary(s).Add("Status", "resolved (draft)").Add("Path", res.Path).Render()
				}
				if res.ReplyID != "" {
					ui.NewSummary(s).Add("Reply", "cmt:"+res.ReplyID).Render()
				}
				return nil
			}
			res, err := runtime.Authoring.ResolveAllNotes(r, runtime.ResolveAllNotesRequest{
				RepoPath:  ".",
				Subject:   target,
				By:        by,
				ReplyBody: replyBody,
			})
			if err != nil {
				var refusal *runtime.NoteRefusal
				if errors.As(err, &refusal) {
					return noteRefused(cmd, jsonOut, refusal.Reason, refusal.Hint)
				}
				return fmt.Errorf("note resolve: %w", err) // Exit 2.
			}
			if jsonOut {
				return emitJSON(cmd, noteResolveJSON{
					Schema:   noteResolveSchema,
					OK:       true,
					Target:   args[0],
					All:      true,
					Resolved: res.Resolved,
					Already:  res.AlreadyResolved,
					Replies:  res.Replies,
				})
			}
			s := styleFor(cmd)
			ui.NewHeader(s, "Resolve Notes").
				Add("Subject", args[0]).
				Add("By", authorLabel(s, by)).
				Pipeline("Resolve").
				Render()
			ui.NewSummary(s).
				Add("Resolved", strings.Join(res.Resolved, ", ")).
				Add("Already resolved", strings.Join(res.AlreadyResolved, ", ")).
				Add("Replies", strings.Join(res.Replies, ", ")).
				Render()
			return nil
		},
	}
	cmd.Flags().Bool("all", false, "resolve every open note discussing the subject unit (draft + published)")
	cmd.Flags().String("reply", "", "attach a reply note documenting the resolution (body text)")
	cmd.Flags().String("reply-file", "", "attach a reply note documenting the resolution (body read from a file)")
	cmd.Flags().String("by", "", "resolution authority name (default: `git config user.name`)")
	cmd.Flags().String("by-kind", "", "author identity kind: user, agent, or worker (default: user)")
	cmd.Flags().Bool("json", false, "emit the deterministic machine report (schema eka-note-resolve-v1)")
	return cmd
}
