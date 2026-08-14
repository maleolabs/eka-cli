package cmd

import (
	"errors"
	"fmt"
	"strings"

	"github.com/maleolabs/eka-cli/cmd/ui"
	"github.com/maleolabs/eka-core/runtime"
	"github.com/spf13/cobra"
)

// This file implements `eka note` (ADR-019 D8, revised): create one
// cmt- note as a DRAFT under EKA_HOME/drafts — the repository docs tree
// is legacy authoring. The command is a thin Cobra layer over the
// Authoring API (runtime.Authoring.NoteDraft).
//
// Exit codes:
//
//	0  note draft created
//	1  refusal (subject unresolvable, repository/workspace state)
//	2  usage or internal error (unknown role, malformed target,
//	   missing --by source, unresolvable namespace)
//
// --json emits the deterministic machine report (schema "eka-note-v1");
// the default output is the human report in the CLI house style.

// noteJSON is the deterministic machine report of one note run
// (schema "eka-note-v1"; pinned field order).
type noteJSON struct {
	Schema string `json:"schema"`
	OK     bool   `json:"ok"`
	ID     string `json:"id,omitempty"`
	Target string `json:"target,omitempty"`
	// SubjectState is "" for store subjects, "draft" for draft
	// tolerance; absent from the report unless the subject resolved
	// as a draft.
	SubjectState string `json:"subjectState,omitempty"`
	By           string `json:"by,omitempty"`
	Draft        string `json:"draft,omitempty"`
	Reason       string `json:"reason,omitempty"`
	Hint         string `json:"hint,omitempty"`
}

// noteSchema is the schema id of the note machine report.
const noteSchema = "eka-note-v1"

// newNoteCommand builds `eka note <target> --role <r> [--by <name>]
// [--json] [--content-file <file>]`.
func newNoteCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "note <target> --role <role>",
		Short: "Create a note draft (comment) on an artifact",
		Long: `Create a cmt- note draft under EKA_HOME/drafts (ADR-019 D8): the
draft carries the ` + "`discusses`" + ` relationship to the subject
target and the role content (ADR-019 D7). The note is NOT published
automatically — ` + "`eka publish`" + ` persists it when it becomes
evidence, and the R13 transition gates already see the draft (a draft
edited to ` + "`note-state: resolved`" + ` gate-satisfies
immediately).

The target is the note's subject line: <type>:<id> (unqualified — the
repository namespace applies) or <ns>/<type>:<id> (qualified — the
namespace must equal the repository's). The subject must be a unit of
the workspace store (run 'eka sync' first) OR a draft of the same
project (draft tolerance: notes record evidence before the subject is
approved — scaffold the subject with 'eka new' and the note can
discuss it right away).

The role is a field of the structured content (ADR-019 D7), NOT
` + "`classification.domain`" + `:

  implementation  {role, summary, changes[], tests[]}   evidence of work
  review          {role, verdict, notes[]}              review verdict
  fix             {role, addresses[], detail}           fix addressing notes

The note's ` + "`classification.domain`" + ` is CONTEXTABLE (ADR-019 D8
revised): --domain declares the Engineering Domain the note's
discussion is contexted in — one of the five canonical domains,
given as the canonical name or the lowercase query token
(architecture|discovery|execution|operations|planning,
case-insensitive). The declared domain is stored canonically (e.g.
"Architecture"); without --domain the domain derives from the cmt-
type token (Execution).

--content-file supplies the note content as a JSON object (merged
over the per-role template; the role field always equals --role).
Without it the empty per-role template is scaffolded.

The change-log authority (by) comes from --by, or defaults to
` + "`git config user.name`" + `.

Exit codes:
  0  note draft created
  1  refused (subject unresolvable, repository or workspace state)
  2  usage or internal error (unknown role, malformed target)`,
		Example: `  eka note sto:12 --role review --content-file review.json
  eka note atrium-api/sto:12 --role implementation --by agent-x
  eka note sto:12 --role fix --json
  eka note adr:content-storage --role review --domain architecture`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			role, _ := cmd.Flags().GetString("role")
			byFlag, _ := cmd.Flags().GetString("by")
			contentFile, _ := cmd.Flags().GetString("content-file")
			jsonOut, _ := cmd.Flags().GetBool("json")
			domainFlag, _ := cmd.Flags().GetString("domain")
			if role == "" {
				return noteUsage(cmd, jsonOut, "note requires --role (implementation, review, or fix)")
			}
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
			if strings.HasPrefix(target, "#") {
				resolved, rerr := resolveNumberTargetInRepo(r, ".", target, "")
				if rerr != nil {
					return noteUsage(cmd, jsonOut, rerr.Error())
				}
				target = resolved
			}
			res, err := runtime.Authoring.NoteDraft(r, runtime.NoteDraftRequest{
				RepoPath:    ".",
				Target:      target,
				Role:        role,
				Domain:      domainFlag,
				By:          by,
				ContentFile: contentFile,
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
					Schema:       noteSchema,
					OK:           true,
					ID:           res.ID,
					Target:       res.Target,
					SubjectState: res.SubjectState,
					By:           by.Name,
					Draft:        res.Path,
				})
			}
			s := styleFor(cmd)
			// A draft-resolved subject is reported explicitly (draft
			// tolerance): the note discusses a subject that is not yet
			// in the store.
			targetLabel := res.Target
			if res.SubjectState == "draft" {
				targetLabel += " (draft)"
			}
			ui.NewHeader(s, "Note").
				Add("Target", targetLabel).
				Add("Role", role).
				Add("By", authorLabel(s, by)).
				Pipeline("Draft").
				Render()
			ui.NewSummary(s).
				Add("Draft", "cmt:"+res.ID).
				Add("Path", res.Path).
				Add("Next", "eka publish to persist the note; set note-state: resolved when addressed").
				Render()
			return nil
		},
	}
	cmd.Flags().String("role", "", "note role: implementation, review, or fix (required)")
	cmd.Flags().String("domain", "", "contextable engineering domain of the note: architecture, discovery, execution, operations, planning (canonical name or lowercase token)")
	cmd.Flags().String("by", "", "change-log authority name (default: `git config user.name`)")
	cmd.Flags().String("by-kind", "", "author identity kind: user, agent, or worker (default: user)")
	cmd.Flags().Bool("json", false, "emit the deterministic machine report (schema eka-note-v1)")
	cmd.Flags().String("content-file", "", "note content as a JSON object (merged over the per-role template)")
	cmd.AddCommand(newNoteReplyCommand(), newNoteResolveCommand())
	return cmd
}

// noteUsage renders a usage-class failure (exit 2) of the note command.
func noteUsage(cmd *cobra.Command, jsonOut bool, message string) error {
	if jsonOut {
		_ = emitJSON(cmd, noteJSON{Schema: noteSchema, OK: false, Reason: message})
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "eka: %s\n", message)
	return &exitError{code: exitUsage}
}

// noteRefused renders a deterministic refusal (exit 1) of the note
// command: the single-line human refusal on stderr, and the machine
// refusal document on stdout with --json.
func noteRefused(cmd *cobra.Command, jsonOut bool, reason, hint string) error {
	if jsonOut {
		_ = emitJSON(cmd, noteJSON{Schema: noteSchema, OK: false, Reason: reason, Hint: hint})
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "eka: note refused: %s; %s\n", reason, hint)
	return &exitError{code: exitFail}
}
