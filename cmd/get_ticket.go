package cmd

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/maleolabs/eka-core/exchange"
	"github.com/maleolabs/eka-core/machine"
	"github.com/maleolabs/eka-core/metadata"
	"github.com/maleolabs/eka-core/runtime"
	"github.com/maleolabs/eka-core/view"
	"github.com/spf13/cobra"
)

// This file implements `eka get ticket <target>`: the machine-readable
// ticket view (schema "eka-ticket-v1"). Where `eka view ticket`
// renders the human detail card, this subcommand emits the
// deterministic canonical JSON consumed by scripts, MCP, Atrium, VS
// Code and AI agents — the projected ticket (identity, projected
// status, work item, container, references) plus, with
// --with-notes/--with-comments, the cmt- notes discussing the ticket
// and its related work item as eka-cko-v2 Documents.
//
// Target resolution and the status projection are reused from the
// projection engine (view.Build ticket) — one source of truth for the
// target forms and the work-item derivation. The machine package never
// renders: Documents in, canonical JSON out.
//
// Exit codes:
//
//	0  JSON document produced
//	1  workspace/repository-state refusal (no workspace, the current
//	   directory is not an EKA repository — no eka.yaml — or the
//	   repository is not registered)
//	2  usage or internal error (missing or unknown target, resolver
//	   or store failure)

// ticketSchema is the schema id of the ticket machine report.
const ticketSchema = "eka-ticket-v1"

// ticketJSON is the deterministic machine report of one ticket lookup
// (schema "eka-ticket-v1"; pinned field order).
type ticketJSON struct {
	Schema string       `json:"schema"`
	Ticket ticketObject `json:"ticket"`
	// Notes carries the eka-cko-v2 Documents of the cmt- notes
	// discussing the ticket and its work item — present only with
	// --with-notes/--with-comments (additive contract).
	Notes []*machine.Document `json:"notes,omitempty"`
}

// ticketObject is the projected ticket of the machine report.
type ticketObject struct {
	Identity  string `json:"identity"`
	Type      string `json:"type"`
	ID        string `json:"id"`
	Projected string `json:"projected"`
	WorkItem  string `json:"workItem,omitempty"`
	Container string `json:"container,omitempty"`
	// References are the derives-from relationship targets in stored
	// (type, target) order.
	References []string `json:"references,omitempty"`
}

// newGetTicketCommand builds `eka get ticket <target> [--with-notes]
// [--with-comments] [--compact]`, registered as a subcommand of
// `eka get`.
func newGetTicketCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ticket <target>",
		Short: "Retrieve one ticket as machine-readable JSON",
		Long: `Retrieve one ticket's projected status as machine-readable JSON
(schema "eka-ticket-v1") — the machine counterpart of 'eka view
ticket'. The document carries the projected ticket: its identity,
the projected status (derived from the referenced work item's
execution state, "unresolved" without one), the work item and
container, and the derives-from references.

  --with-notes / --with-comments (synonyms) append the "notes"
  array: the eka-cko-v2 Documents of every cmt- note discussing
  the ticket or its related work item, sorted by canonical form.

The target accepts the same forms as 'eka view ticket': a bare id,
"tkt-<id>", "tkt:<id>", a typed line ("sto:<id>", "<ns>/<type>:<id>")
or a direct work item id of any work item type.

The repository must be an EKA repository — a directory tree carrying
eka.yaml (run 'eka init' to create one) — registered in the EKA
workspace and synced first ('eka sync').

Output contract: stdout carries ONLY the JSON document followed by
a single trailing newline. Errors go to stderr, one 'eka: ...' line
per error.

Exit codes:
  0  JSON document produced
  1  workspace/repository-state refusal (no EKA workspace,
     repository not registered in the workspace)
  2  usage or internal error (missing or unknown target,
     resolver failure)`,
		Example: `  eka get ticket sto-draft-autosave --with-notes
  eka get ticket tkt-sto-publish-post --with-comments --compact
  eka get ticket feather/sto:publish-post`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			withNotes, _ := cmd.Flags().GetBool("with-notes")
			if withComments, _ := cmd.Flags().GetBool("with-comments"); withComments {
				withNotes = true
			}
			compact, _ := cmd.Flags().GetBool("compact")
			// The resolution prologue: open (never create) the Runtime,
			// then gate on workspace and repository state (the same
			// prologue as `eka get`).
			r, err := runtime.Open()
			if err != nil {
				return err // Exit 2: workspace resolution.
			}
			defer r.Close()
			if !r.Exists() {
				// Workspace-state refusal: `eka get` never creates a
				// workspace — deterministic message, exit 1.
				fmt.Fprintf(cmd.ErrOrStderr(), "eka: get refused: no EKA workspace at %s; run 'eka sync' first\n", r.Path())
				return &exitError{code: exitFail}
			}
			abs, err := filepath.Abs(".")
			if err != nil {
				return fmt.Errorf("get failed: %w", err)
			}
			abs = filepath.Clean(abs)
			_, _, hasMeta, err := metadata.Find(abs)
			if err != nil {
				return fmt.Errorf("get failed: %w", err) // Exit 2: metadata read failure.
			}
			if !hasMeta {
				fmt.Fprintf(cmd.ErrOrStderr(), "eka: get refused: %s is not an EKA repository (no eka.yaml); run 'eka init' first\n", abs)
				return &exitError{code: exitFail}
			}
			repo, found, err := r.Workspace.FindRepo(abs)
			if err != nil {
				return fmt.Errorf("get failed: %w", err) // Exit 2: registry failure.
			}
			if !found {
				fmt.Fprintf(cmd.ErrOrStderr(), "eka: get refused: repository %s is not registered in the EKA workspace; run 'eka sync' (auto-registers) or 'eka project register' first\n", abs)
				return &exitError{code: exitFail}
			}
			// The projection source: every registered repository's
			// units, decoded from the immutable payloads.
			units, err := r.Knowledge.UnitsByProject(repo.ProjectID)
			if err != nil {
				return fmt.Errorf("get failed: %w", err) // Exit 2: store failure.
			}
			g := view.NewGraph(".", units)
			proj, err := view.Build("ticket", g, args[0])
			if err != nil {
				// TargetNotFoundError and unknown-projection both map
				// to the usage class (exit 2).
				return fmt.Errorf("get: %w", err)
			}
			tp, ok := proj.(*view.TicketProjection)
			if !ok {
				return fmt.Errorf("get failed: unexpected projection %T", proj) // Exit 2: internal.
			}
			doc := ticketJSON{
				Schema: ticketSchema,
				Ticket: ticketObject{
					Identity:  tp.Ticket.Identity,
					Type:      tp.Ticket.Type,
					ID:        tp.Ticket.ID,
					Projected: tp.Projected,
					// The machine contract resolves every reference to
					// its qualified line form (the human projection
					// renders the authoring convention instead).
					References: qualifiedReferences(g, tp.Ticket.Identity),
				},
			}
			if tp.WorkItem != nil {
				doc.Ticket.WorkItem = tp.WorkItem.Identity
			}
			if tp.Container != nil {
				doc.Ticket.Container = tp.Container.Identity
			}
			if withNotes {
				notes, err := ticketNoteDocuments(tp.Notes)
				if err != nil {
					return fmt.Errorf("get failed: %w", err) // Exit 2: internal.
				}
				doc.Notes = notes
			}
			var out []byte
			if compact {
				out, err = json.Marshal(doc)
			} else {
				out, err = json.MarshalIndent(doc, "", "  ")
			}
			if err != nil {
				return fmt.Errorf("get failed: %w", err) // Exit 2: internal.
			}
			out = append(out, '\n')
			if _, err := cmd.OutOrStdout().Write(out); err != nil {
				return fmt.Errorf("get failed: %w", err)
			}
			return nil
		},
	}
	cmd.Flags().Bool("with-notes", false, "append the \"notes\" array: the eka-cko-v2 Documents of the cmt- notes discussing the ticket and its work item")
	cmd.Flags().Bool("with-comments", false, "append the \"notes\" array (synonym of --with-notes)")
	cmd.Flags().Bool("compact", false, "emit the JSON as a single line (plus trailing newline)")
	return cmd
}

// qualifiedReferences resolves the derives-from relationship targets of
// the ticket unit to their qualified line forms (the machine contract:
// "<namespace>/<type>:<id>", the instance-version suffix dropped), in
// stored (type, target) order. The authoring-convention rendering of
// the projection stays view-side.
func qualifiedReferences(g *view.Graph, identity string) []string {
	u := g.ByLineForm(identity)
	if u == nil {
		return nil
	}
	var out []string
	for _, r := range u.Relationships {
		if r.Type != "derives-from" {
			continue
		}
		out = append(out, qualifiedLine(r.Target))
	}
	return out
}

// qualifiedLine normalizes a stored relationship target to its
// qualified line form: "<namespace>/<type>:<id>" — the optional
// instance-version suffix (":<digits>") is dropped.
func qualifiedLine(target string) string {
	if i := strings.LastIndex(target, ":"); i >= 0 {
		suffix := target[i+1:]
		if suffix != "" {
			allDigits := true
			for _, r := range suffix {
				if r < '0' || r > '9' {
					allDigits = false
					break
				}
			}
			if allDigits {
				return target[:i]
			}
		}
	}
	return target
}

// ticketNoteDocuments maps the projected cmt- notes and their
// single-level replies to machine Documents (eka-cko-v2), in
// projection order: each parent note followed by its replies
// (canonical identity — deterministic).
func ticketNoteDocuments(notes []view.TicketNote) ([]*machine.Document, error) {
	if len(notes) == 0 {
		return []*machine.Document{}, nil
	}
	out := make([]*machine.Document, 0, len(notes))
	add := func(u *exchange.Unit) error {
		d, err := machine.NewDocument(u)
		if err != nil {
			return err
		}
		out = append(out, d)
		return nil
	}
	for _, n := range notes {
		if err := add(n.Note); err != nil {
			return nil, err
		}
		for _, r := range n.Replies {
			if err := add(r); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}
