package cmd

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/maleolabs/eka-cli/cmd/ui"
	"github.com/maleolabs/eka-core/exchange"
	"github.com/maleolabs/eka-core/metadata"
	"github.com/maleolabs/eka-core/runtime"
	"github.com/maleolabs/eka-core/view"
	"github.com/spf13/cobra"
)

// newViewCommand builds the `eka view` command: project the Engineering
// Knowledge Model of the project owning the repository rooted at the
// current directory. All projection logic lives in the view package
// (the Knowledge Projection Engine); this command only resolves the
// workspace, reads the canonical units of the project from the store
// (the store-backed projection source of the Knowledge Runtime),
// renders the projection and maps the result to the exit code
// contract. No Markdown is read at projection time: authoring is
// compiled and seeded by `eka sync`.
//
// The projections are domain-first: discovery, architecture, planning,
// execution and operations render one Engineering Domain each; ticket
// renders a single ticket. The former sprint and wave projections
// remain registered as aliases of execution (identical output).
//
// Exit codes:
//
//	0  projection produced (including empty projections: no active
//	   container, no domain artifacts, no tickets, no synced knowledge)
//	1  repository-state refusal: the current directory is not an EKA
//	   repository (no eka.yaml) or the repository is not registered in
//	   the EKA workspace: no projection is produced (deterministic
//	   refusal message printed)
//	2  usage or internal error (unknown projection, missing or unknown
//	   ticket target, workspace or store failure)
func newViewCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "view [projection] [target]",
		Short: "Project the Engineering Knowledge Model",
		Long: `Project the Engineering Knowledge Model of the project owning the
repository rooted at the current directory: read-only views over the
complete Engineering Knowledge of the project — every registered
repository of the project — derived from the EKA workspace canonical
store (default ~/.eka, or $EKA_HOME), never from file text.

The repository must be an EKA repository — a directory tree carrying
eka.yaml (run 'eka init' to create one) — registered in the EKA
workspace and synced first: 'eka sync' compiles the authoring tree
through the Knowledge Compiler (conformance-gated) and seeds the
canonical store. At projection time no Markdown is read and no
conformance gate runs — the projection is built from the stored
Canonical Knowledge Objects only. A directory without eka.yaml (not
an EKA repository) or a repository that is not registered (or a
workspace that was never synced) is refused with a deterministic
message. Run this command inside the repository root.

Projections (domain-first):

  discovery    the Discovery domain: vis-, str-, req-, fnd- artifacts
               grouped by type with their content states
  architecture the Architecture domain: adr-, dec-, arc-, spec-, std-,
               gls- artifacts grouped by type with their content states
               (Decisions merge adr- and dec-)
  planning     the Planning domain: scp-, epc-, plan-, trc- artifacts
               — plans as milestone roots (created-date order) with the
               scope/epic lines deriving from them beneath; scope/epic
               lines without a plan render as orphans; traceability as
               footer
  execution    the active execution container: its tickets with the
               status projected from their work items, and its work
               items grouped by execution state
               (planned/todo/in-progress/in-review/done)
  board        every work item in the project across all execution
               containers, grouped by execution state, each item tagged
               with its container (unassigned when none) and its
               assignee (when assigned); --member filters to one
               member's scope plus the "No assignee" bucket
  containers   every execution container (ctr-) line: name, plan,
               items/tickets, started/ended and status — an aligned
               table
  operations   the Operations domain: run-, rel- artifacts grouped by
               type with their content states
  ticket       one execution item's projected status: for a ticket
               (tkt-) derived from its referenced work item, for a
               direct work item (sto-/ts-/bug-/td-/ch-/spk-) its own
               execution state (ticket body content is never read).
               tkt- units are GENERATED state projections (their files
               carry "Generated — State Projection. Do NOT edit state
               here; refresh on read.") — never edit a projection's
               state; transition the referenced work item instead
  document     ONE canonical document's detail of any type (the
               bare-argument resolution): identity, states,
               relationships, and the content sections

Aliases:

  sprint, wave resolve to the execution projection (identical output)

The target argument is required by the ticket and document projections
(a bare id, <type>:<id>, <type>-<id>, or the qualified
<ns>/<type>:<id>); the domain and execution projections ignore it.

A SINGLE argument that is not a registered projection resolves as a
DOCUMENT target of any canonical type (domain first, then the
specific document): 'eka view sto-alpha' is equivalent to 'eka view
document sto-alpha'. Tickets and board work items are documents too.
A document whose id collides with a projection name is still
reachable via the explicit 'document'/'ticket' projections.

With no arguments the available projections are listed.

Retrieval flags (board and containers projections only):

  --offset <n>  the 0-based offset into each column (board) or the
                container list (containers); default 0
  --limit <n>   the page size; default 0 = no limit
  --page <n>    the 1-based page number; requires --limit (default 1);
                --offset and --page are mutually exclusive
  The board renders the window of every column (with a "+N more"
  card when a column overflows) and a "Paged: ..." footer; the
  containers table renders its window with a "Page X/Y" footer.

Member filter (board projection only):

  --member <me|<mbr-id>>
                the advisory member-scoped board (ADR-029): the six
                state columns hold only the member's assigned work
                items, and every work item WITHOUT an assigned-to
                edge surfaces in a separate visible "No Assignee"
                bucket below the columns — never hidden, never
                interleaved (Decision 3). "me" resolves the operator
                identity from ` + "`git config user.name`" + ` (the same
                authority as --by) to exactly one mbr- line of the
                repository; zero or multiple matches refuse listing
                the available members. Any other value resolves like
                an assign target: <mbr-id>, mbr:<id>, mbr-<id>, or
                <ns>/mbr:<id> (the qualified form must stay inside the
                repository's namespace); an unresolvable id refuses
                listing the available members. The container-axis
                "Unassigned" insight is suppressed in member views —
                the "No assignee" bucket replaces it there.
                Terminology: "No assignee" is the MEMBER axis (a work
                item without an assigned-to edge); "unassigned" is the
                CONTAINER axis (a work item not referenced by any
                ticket container). The two never merge: an item can be
                container-unassigned AND member-assigned, or both.
                --member composes with pagination (filter first, then
                window).

Machine output (board projection only):

  --json      emit the deterministic machine document on stdout (one
              line, one trailing newline) instead of the human render.
              The repository-wide board emits schema "eka-board-v1":
              every column with its items, each item carrying the
              pinned "assignee" key (the canonical member line, absent
              without an assigned-to edge). The member-scoped board
              (--member) emits schema "eka-board-member-v1" with the
              pinned machine keys of the slice: "assignee" (the
              member's assigned items) and "no-assignee" (the 'No
              assignee' bucket) — the member-axis counterpart of the
              container-axis "unassigned". --json does not compose
              with pagination: the machine document always carries the
              full projection.

Containers filters (containers projection only):

  --active, --current  keep only the containers with container-state
                       active
  --container <id>     keep only the container whose id matches: a
                       bare id, ctr-<id>, ctr:<id>, or the qualified
                       <ns>/ctr:<id> form (an unknown container is a
                       usage error listing the available forms)

Exit codes:
  0  projection produced (including empty projections and projects
     with no synced knowledge)
  1  the current directory is not an EKA repository (no eka.yaml), or
     the repository is not registered in the EKA workspace (run
     'eka sync' first)
  2  usage or internal error (unknown projection, missing or unknown
     ticket/document target)`,
		Example: `  eka view
  eka view execution
  eka view planning
  eka view ticket tkt-sto-alpha
  eka view ticket sto-alpha
  eka view sto-alpha            (bare-argument document fallback)
  eka view feather/adr:content-storage (any canonical document)
  eka view containers
  eka view containers --active
  eka view containers --container wave-6
  eka view board --limit 6 --page 2`,
		Args: cobra.MaximumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				printViewLanding(styleFor(cmd))
				return nil
			}
			// Bare-argument document fallback: a SINGLE argument that
			// is not a registered projection/domain is resolved as a
			// DOCUMENT target of any canonical type — first win is
			// the domain, then the specific document (`eka view
			// <target>` ≡ `eka view document <target>`; tickets and
			// board work items included). A document whose id
			// collides with a projection name is still reachable via
			// the explicit `document`/`ticket` projections.
			if len(args) == 1 && !view.IsProjection(args[0]) {
				args = []string{"document", args[0]}
			}
			name, target, err := parseProjectionArgs(args)
			if err != nil {
				return err
			}
			s := styleFor(cmd)
			// Retrieval flags (board and containers projections only).
			offset, _ := cmd.Flags().GetInt(flagViewOffset)
			limit, _ := cmd.Flags().GetInt(flagViewLimit)
			pageValue, _ := cmd.Flags().GetInt(flagViewPage)
			active, _ := cmd.Flags().GetBool(flagViewActive)
			current, _ := cmd.Flags().GetBool(flagViewCurrent)
			container, _ := cmd.Flags().GetString(flagViewContainer)
			pagination := paginationFlags{
				offset:      offset,
				limit:       limit,
				page:        pageValue,
				offsetGiven: cmd.Flags().Changed(flagViewOffset),
				limitGiven:  cmd.Flags().Changed(flagViewLimit),
				pageGiven:   cmd.Flags().Changed(flagViewPage),
			}
			// Applicability rules (deterministic usage errors, exit 2):
			// pagination applies to the board and containers
			// projections, the container filters to containers only,
			// the member filter and --json to the board only. --json
			// never composes with pagination: the machine document
			// always carries the full projection.
			memberFlag, _ := cmd.Flags().GetString(flagViewMember)
			memberGiven := memberFlag != ""
			jsonGiven, _ := cmd.Flags().GetBool(flagViewJSON)
			paginated := pagination.offsetGiven || pagination.limitGiven || pagination.pageGiven
			filtered := active || current || container != ""
			switch name {
			case "board":
				if filtered {
					return fmt.Errorf("the board projection supports --offset/--limit/--page/--member/--json only")
				}
				if jsonGiven && paginated {
					return fmt.Errorf("view: board --json does not compose with pagination; the machine document carries the full projection")
				}
			case "containers":
				// Pagination and container filters both apply.
				if jsonGiven {
					return fmt.Errorf("view: --json is a board-projection flag")
				}
			default:
				if paginated || filtered || memberGiven || jsonGiven {
					return fmt.Errorf("the %s projection does not support these flags (board: pagination, --member and --json; containers: pagination and filters)", name)
				}
			}
			// The Runtime is the projection source: the repository must
			// be registered and synced first ('eka sync'); the
			// projection never reads Markdown.
			r, err := runtime.Ensure()
			if err != nil {
				return err // Exit 2: workspace resolution.
			}
			defer r.Close()
			abs, err := filepath.Abs(".")
			if err != nil {
				return fmt.Errorf("view failed: %w", err)
			}
			abs = filepath.Clean(abs)
			// The repository context gate (ADR-018): an EKA repository
			// is a directory tree carrying eka.yaml — without it the
			// tree is not an EKA repository and the refusal replaces
			// the not-registered branch (exit 1, the same refusal
			// class).
			meta, _, hasMeta, err := metadata.Find(abs)
			if err != nil {
				return fmt.Errorf("view failed: %w", err) // Exit 2: metadata read failure.
			}
			if !hasMeta {
				fmt.Fprintf(cmd.ErrOrStderr(), "eka: view refused: %s is not an EKA repository (no eka.yaml); run 'eka init' first\n", abs)
				return &exitError{code: exitFail}
			}
			repo, found, err := r.Workspace.FindRepo(abs)
			if err != nil {
				return fmt.Errorf("view failed: %w", err) // Exit 2: registry failure.
			}
			if !found {
				// Repository-state refusal: deterministic message and
				// exit 1 — no projection is produced.
				fmt.Fprintf(cmd.ErrOrStderr(), "eka: view refused: repository %s is not registered in the EKA workspace; run 'eka sync' (auto-registers) or 'eka project register' first\n", abs)
				return &exitError{code: exitFail}
			}
			// The projection source is the complete Engineering
			// Knowledge of the project: every registered repository's
			// units, decoded from the immutable payloads.
			units, err := r.Knowledge.UnitsByProject(repo.ProjectID)
			if err != nil {
				return fmt.Errorf("view failed: %w", err) // Exit 2: store failure.
			}
			if len(units) == 0 {
				// Empty projection: still rendered (exit 0), but the
				// missing knowledge is surfaced before the render.
				fmt.Fprintf(s.W, "%s\n", s.Info(fmt.Sprintf(
					"no synced knowledge for project %s; run 'eka sync' after editing docs", repo.ProjectID)))
			}
			// Issue-number targets (RFC): "#<n>" resolves to its line
			// before the projection build. The ticket projection
			// narrows to the ticket group; every other target shape
			// requires the number to be unambiguous across the groups.
			if strings.HasPrefix(target, "#") {
				group := ""
				if name == "ticket" {
					group = "ticket"
				}
				resolved, rerr := resolveNumberTarget(r, repo.ProjectID, target, group)
				if rerr != nil {
					return rerr
				}
				target = resolved
			}
			// One read, one graph: the projection engine is
			// synchronous and stateless, so a future loading state can
			// wrap the whole call without restructuring. The project's
			// issue numbers attach to the graph for "#<n>" displays.
			g := view.NewGraph(".", units)
			if numbers, nerr := r.Knowledge.NumbersByProject(repo.ProjectID); nerr == nil {
				g.AttachNumbers(numbers)
			}
			var proj view.Projection
			if memberGiven {
				// The member-scoped board (--member me|<mbr-id>): the
				// advisory member filter of ADR-029 Decision 3. The
				// member resolves within the repository's namespace
				// (the eka.yaml declaration wins, else the registered
				// default — the same resolution the authoring
				// commands use); BoardForMember builds the scoped
				// columns plus the dedicated 'No assignee' bucket.
				repoNS := meta.Namespace
				if repoNS == "" {
					repoNS = repo.Namespace
				}
				memberForm, rerr := resolveBoardMember(repoNS, units, memberFlag)
				if rerr != nil {
					return rerr // Exit 2: usage.
				}
				board, berr := view.BoardForMember(g, memberForm)
				if berr != nil {
					return fmt.Errorf("view failed: %w", berr) // Exit 2: internal.
				}
				proj = board
			} else {
				built, berr := view.Build(name, g, target)
				if berr != nil {
					if errors.Is(berr, view.ErrUnknownProjection) {
						return fmt.Errorf("unknown projection %q — available projections: %s",
							name, view.HelpList())
					}
					return berr // TargetNotFoundError etc. map to exit 2.
				}
				proj = built
			}
			// The document projection carries its issue number for the
			// "#<n>" display (the ticket projection reads the number
			// label from the graph directly).
			if doc, ok := proj.(*view.DocumentProjection); ok {
				doc.Document.Number = g.Number(doc.Document.Identity)
			}
			// The applied page window and its pre-rendered footer line
			// of a paged render (board + containers): computed here,
			// where the effective offset/limit and the population
			// sizes are known.
			var page *view.Page
			footer := ""
			switch p := proj.(type) {
			case *view.ContainersProjection:
				// Retrieval filters: --active/--current keep only the
				// active containers; --container keeps the single
				// match (an unknown container lists the available
				// forms as a usage error, exit 2).
				filterNote := ""
				if active || current {
					keep := make([]view.ContainerSummary, 0, len(p.Containers))
					for _, c := range p.Containers {
						if c.State == "active" {
							keep = append(keep, c)
						}
					}
					p.Containers = keep
					filterNote = "active only"
				}
				if container != "" {
					matched, ok := matchContainerSummary(p.Containers, container)
					if !ok {
						return fmt.Errorf("view: container %q not found — available containers: %s",
							container, strings.Join(summaryForms(p.Containers), ", "))
					}
					p.Containers = []view.ContainerSummary{matched}
					filterNote = "container " + matched.ID
				}
				// The page window applies to the filtered list; the
				// footer's "of T" is the filtered population (the
				// window's total — p.Total stays the full population
				// for the insight line).
				if ok, offset, limit := pagination.apply(len(p.Containers)); ok {
					total := len(p.Containers)
					pg := view.NewPage(offset, limit)
					page = &pg
					footer = containersFooter(pageNumberOf(offset, limit), pg, total, offset, limit)
					p.Page(offset, limit)
				}
				renderContainers(s, g, p, filterNote, footer)
				return nil
			case *view.BoardProjection:
				// The page window: every column renders only its
				// window (the full counts stay on the untouched
				// projection); the footer names the window.
				if ok, offset, limit := pagination.apply(p.Total); ok {
					pg := view.NewPage(offset, limit)
					page = &pg
					footer = fmt.Sprintf("Paged: %d per column · page %d (offset %d)",
						limit, pageNumberOf(offset, limit), offset)
				}
				if jsonGiven {
					return emitBoardMachine(cmd, p)
				}
				renderBoardProjection(s, g, p, page, footer)
				return nil
			}
			// The ticket notes flags: --with-note and --with-comments
			// are synonyms (both surface the notes discussing the
			// ticket and its work item).
			withNotes, _ := cmd.Flags().GetBool("with-note")
			if withComments, _ := cmd.Flags().GetBool("with-comments"); withComments {
				withNotes = true
			}
			renderView(s, g, proj, viewOptions{withNotes: withNotes})
			return nil
		},
	}
	cmd.Flags().Int(flagViewOffset, 0, "board/containers: 0-based offset into each column or the container list (default 0)")
	cmd.Flags().Int(flagViewLimit, 0, "board/containers: page size (default 0 = no limit)")
	cmd.Flags().Int(flagViewPage, 1, "board/containers: 1-based page number (requires --limit; default 1)")
	cmd.Flags().Bool(flagViewActive, false, "containers: keep only the containers with container-state active")
	cmd.Flags().Bool(flagViewCurrent, false, "containers: keep only the current (active) container")
	cmd.Flags().String(flagViewContainer, "", "containers: keep only the container whose id matches (bare id, ctr-<id>, ctr:<id> or <ns>/ctr:<id>)")
	cmd.Flags().String(flagViewMember, "", "board: filter to one member's assigned items plus the 'No assignee' bucket (me = `git config user.name`, or the mbr- line form)")
	cmd.Flags().Bool(flagViewJSON, false, "board: emit the deterministic machine document (schema eka-board-v1 / eka-board-member-v1) instead of the human render")
	// The ticket notes flags: --with-note and --with-comments are
	// synonyms — both surface the cmt- notes discussing the ticket and
	// its related work item in the ticket projection.
	cmd.Flags().Bool("with-note", false, "ticket projection: show the notes (comments) discussing the ticket and its work item")
	cmd.Flags().Bool("with-comments", false, "ticket projection: show the notes (comments) discussing the ticket and its work item")
	return cmd
}

// Flag names of the view retrieval options (declared once, shared by
// the help text and the flag lookups).
const (
	flagViewOffset    = "offset"
	flagViewLimit     = "limit"
	flagViewPage      = "page"
	flagViewActive    = "active"
	flagViewCurrent   = "current"
	flagViewContainer = "container"
	flagViewMember    = "member"
	flagViewJSON      = "json"
)

// resolveBoardMember resolves the --member target of the board
// projection (ADR-029 Decision 3 — the advisory member filter):
//
//	"me"        the operator identity from `git config user.name`
//	            (the same authority as --by) matched against the
//	            repository's member lines — a member matches when its
//	            author name or its bare id equals the identity.
//	            Zero or multiple matches are a deterministic refusal
//	            listing the available members.
//	<mbr-id>    <mbr-id>, mbr:<id>, mbr-<id>, or <ns>/mbr:<id> —
//	            resolved within the repository's namespace; a
//	            cross-namespace qualified target is rejected, and an
//	            unresolvable id is a deterministic refusal listing
//	            the available members (precedence: the unknown
//	            --container usage error).
//
// Errors are usage-class (exit 2). "me" is a reserved keyword; a
// member id must not be named "me" (req:team-collaboration §6).
func resolveBoardMember(ns string, units []*exchange.Unit, raw string) (string, error) {
	if raw == "me" {
		return resolveMeMember(ns, units)
	}
	form, err := parseMemberTarget(raw, ns)
	if err != nil {
		return "", fmt.Errorf("view: --member: %v", err)
	}
	if !memberLineExists(units, form) {
		return "", fmt.Errorf("view: member %q not found — available members: %s",
			raw, strings.Join(memberLinesInNS(units, ns), ", "))
	}
	return form, nil
}

// resolveMeMember resolves the "me" member target: the operator
// identity from `git config user.name` (the same authority as --by)
// matched against the member lines of the repository's namespace — a
// member matches when its author name or its bare id equals the
// identity. Exactly one match resolves; zero or multiple matches are a
// deterministic refusal listing the available members.
func resolveMeMember(ns string, units []*exchange.Unit) (string, error) {
	by, err := runtime.BySource("", "", ".")
	if err != nil {
		return "", fmt.Errorf("view: --member me: cannot resolve the operator identity: %v; pass --member <mbr-id>", err)
	}
	var matches []string
	for _, u := range units {
		if u.Identity.Type != "mbr" || u.Identity.Namespace != ns {
			continue
		}
		if u.Author.Name == by.Name || u.Identity.ID == by.Name {
			matches = append(matches, view.LineForm(u.Identity.Namespace, u.Identity.Type, u.Identity.ID))
		}
	}
	sort.Strings(matches)
	if len(matches) != 1 {
		return "", fmt.Errorf("view: --member me: the operator identity %q matches %d member line(s); pass --member <mbr-id> — available members: %s",
			by.Name, len(matches), strings.Join(memberLinesInNS(units, ns), ", "))
	}
	return matches[0], nil
}

// Board machine output (board --json; the pinned machine keys of the
// slice — req:team-collaboration §6). The repository-wide board emits
// schema "eka-board-v1" with every column and its items, each item
// carrying the pinned "assignee" key (the canonical member line the
// item's assigned-to edge resolves to, absent without one). The
// member-scoped board emits schema "eka-board-member-v1" with the
// pinned member-axis keys: "assignee" (the member's assigned items)
// and "no-assignee" (the dedicated 'No assignee' bucket — never
// "unassigned", which belongs to the container axis). Both documents
// are deterministic (fixed field order, sorted lists, no timestamps).

// boardSchema is the schema id of the repository-wide board machine
// report.
const boardSchema = "eka-board-v1"

// boardMemberSchema is the schema id of the member-scoped board machine
// report.
const boardMemberSchema = "eka-board-member-v1"

// boardJSON is the deterministic machine report of the repository-wide
// board (schema "eka-board-v1"; pinned field order).
type boardJSON struct {
	Schema         string            `json:"schema"`
	Total          int               `json:"total"`
	Unassigned     int               `json:"unassigned"`
	ContainerCount int               `json:"containerCount"`
	Columns        []boardColumnJSON `json:"columns"`
}

// boardColumnJSON is one execution-state column of the machine report.
type boardColumnJSON struct {
	State string          `json:"state"`
	Items []boardItemJSON `json:"items"`
}

// boardItemJSON is one board item of the machine report. Assignee is
// the pinned key: the canonical member line the item is assigned to,
// absent when the item carries no assigned-to edge (the item then
// belongs to the 'No assignee' bucket of a member-scoped view).
type boardItemJSON struct {
	Identity   string   `json:"identity"`
	State      string   `json:"state"`
	Assignee   string   `json:"assignee,omitempty"`
	Containers []string `json:"containers,omitempty"`
}

// boardMemberJSON is the deterministic machine report of the
// member-scoped board (schema "eka-board-member-v1"; pinned field
// order): the member line, its assigned items ("assignee") and the
// dedicated 'No assignee' bucket ("no-assignee") — both as canonical
// line identity forms in the board's display order.
type boardMemberJSON struct {
	Schema     string   `json:"schema"`
	Member     string   `json:"member"`
	Assignee   []string `json:"assignee"`
	NoAssignee []string `json:"no-assignee"`
}

// emitBoardMachine emits the deterministic board machine document of
// one board projection (--json): the member-scoped report when the
// projection carries a member, the repository-wide report otherwise.
// stdout carries ONLY the JSON document plus its single trailing
// newline.
func emitBoardMachine(cmd *cobra.Command, p *view.BoardProjection) error {
	if p.Member != "" {
		// The member-scoped document: the pinned assignee / no-assignee
		// keys (the member axis). "assignee" flattens the member's
		// scoped columns in the fixed execution-state order (the
		// board's display order); "no-assignee" is the dedicated bucket.
		var assignee []string
		for _, col := range p.Columns {
			for _, bi := range col.WorkItems {
				assignee = append(assignee, bi.Identity)
			}
		}
		noAssignee := make([]string, 0, len(p.NoAssignee))
		for _, bi := range p.NoAssignee {
			noAssignee = append(noAssignee, bi.Identity)
		}
		return emitJSON(cmd, boardMemberJSON{
			Schema:     boardMemberSchema,
			Member:     p.Member,
			Assignee:   assignee,
			NoAssignee: noAssignee,
		})
	}
	// The repository-wide document: every column with its items, each
	// item carrying the pinned per-item "assignee" key.
	cols := make([]boardColumnJSON, 0, len(p.Columns))
	for _, col := range p.Columns {
		items := make([]boardItemJSON, 0, len(col.WorkItems))
		for _, bi := range col.WorkItems {
			items = append(items, boardItemJSON{
				Identity:   bi.Identity,
				State:      bi.State,
				Assignee:   bi.Assignee,
				Containers: bi.Containers,
			})
		}
		cols = append(cols, boardColumnJSON{State: col.State, Items: items})
	}
	return emitJSON(cmd, boardJSON{
		Schema:         boardSchema,
		Total:          p.Total,
		Unassigned:     p.Unassigned,
		ContainerCount: p.ContainerCount,
		Columns:        cols,
	})
}

// parseProjectionArgs validates the projection+target argument pair
// shared by view and watch: the projection must be registered
// (canonical or alias) and the ticket projection requires its target.
// Errors are usage-class (exit 2) with the same helpful messages in
// both commands. It assumes args is non-empty (both commands guard
// the no-argument case before calling).
func parseProjectionArgs(args []string) (name, target string, err error) {
	name = args[0]
	if !view.IsProjection(name) {
		return "", "", fmt.Errorf("unknown projection %q — available projections: %s",
			name, view.HelpList())
	}
	if len(args) == 2 {
		target = args[1]
	}
	if (name == "ticket" || name == "document") && target == "" {
		return "", "", fmt.Errorf("the %s projection requires a target: eka view %s <target>", name, name)
	}
	return name, target, nil
}

// viewDescriptions are the one-line projection descriptions used by the
// no-argument landing.
var viewDescriptions = map[string]string{
	"discovery":    "Discovery domain artifacts (vis-, str-, req-, fnd-)",
	"architecture": "Architecture domain artifacts (adr-, dec-, arc-, spec-, std-, gls-)",
	"planning":     "Planning domain artifacts (scp-, epc-, plan-, trc-)",
	"execution":    "active container: tickets and work items by execution state",
	"board":        "all work items across every container, by execution state",
	"containers":   "all execution containers: plan, items/tickets, started/ended, status",
	"operations":   "Operations domain artifacts (run-, rel-)",
	"ticket":       "one execution item's projected status from its work item",
	"document":     "one canonical document's detail (identity, states, content)",
}

// printViewLanding renders the calm no-argument orientation: the
// available projections (canonical + aliases) and usage pointers.
// Informational output — exits 0, deterministic.
// printViewLanding renders the calm no-argument orientation: the
// available projections (canonical + aliases) and usage pointers,
// wrapped in the output container. Informational output — exits 0,
// deterministic.
func printViewLanding(s *ui.Style) {
	var b strings.Builder
	fmt.Fprintln(&b, s.Accent("Knowledge Projections"))
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "The EKA Knowledge Projection Engine: read-only views over the")
	fmt.Fprintln(&b, "Engineering Knowledge Model from the EKA workspace — the")
	fmt.Fprintln(&b, "complete knowledge of one project, projected by domain and state.")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "Projections")
	for _, name := range view.Projections() {
		fmt.Fprintf(&b, "  %-12s %s\n", name, viewDescriptions[name])
	}
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "Aliases")
	for _, alias := range view.Aliases() {
		fmt.Fprintf(&b, "  %-12s → %s\n", alias, view.AliasTarget(alias))
	}
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "Usage")
	fmt.Fprintln(&b, "  Run 'eka view <projection>' for a projection,")
	fmt.Fprintln(&b, "  'eka view ticket <tkt-id>' for one ticket, or")
	fmt.Fprintln(&b, "  'eka view <target>' for any document's detail")
	fmt.Fprintln(&b, "  (domain first, then the specific document).")
	fmt.Fprintln(&b, "  Run 'eka view <projection> --help' for details.")
	ui.Container(s, b.String())
}

// viewOptions carries the render-time options of one projection run.
type viewOptions struct {
	// withNotes surfaces the notes section of the ticket projection
	// (--with-note / --with-comments).
	withNotes bool
}

// renderView dispatches to the concrete projection renderer. The
// renderView dispatches to the concrete projection renderer. The
// registry is closed over the canonical projections; an unknown
// concrete type is a programming error, not user input. The board and
// containers projections render with their default (unpaged,
// unfiltered) presentation — the retrieval flags are applied by the
// command prologue before the dispatch; the ticket projection gates
// its notes section on the --with-note/--with-comments options.
func renderView(s *ui.Style, g *view.Graph, p view.Projection, opts viewOptions) {
	switch p := p.(type) {
	case *view.ExecutionProjection:
		renderExecution(s, g, p)
	case *view.TicketProjection:
		renderTicket(s, g, p, opts.withNotes)
	case *view.PlanningProjection:
		renderPlanning(s, g, p)
	case *view.ArchitectureProjection:
		renderArchitecture(s, g, p)
	case *view.DiscoveryProjection:
		renderDiscovery(s, g, p)
	case *view.OperationsProjection:
		renderOperations(s, g, p)
	case *view.BoardProjection:
		renderBoardProjection(s, g, p, nil, "")
	case *view.ContainersProjection:
		renderContainers(s, g, p, "", "")
	case *view.DocumentProjection:
		renderDocument(s, p.Document)
	default:
		fmt.Fprintln(s.W, s.Error("cannot render projection"))
	}
}

// matchContainerSummary resolves a user-supplied container filter
// target against the projection's containers: a bare id, "ctr-<id>",
// "ctr:<id>" (matched against the container id), or the qualified
// "<ns>/ctr:<id>" form (matched against the canonical identity).
func matchContainerSummary(containers []view.ContainerSummary, raw string) (view.ContainerSummary, bool) {
	if strings.Contains(raw, "/") {
		for _, c := range containers {
			if c.Identity == raw {
				return c, true
			}
		}
		return view.ContainerSummary{}, false
	}
	id := strings.TrimPrefix(strings.TrimPrefix(raw, "ctr-"), "ctr:")
	for _, c := range containers {
		if c.ID == id {
			return c, true
		}
	}
	return view.ContainerSummary{}, false
}

// summaryForms lists the canonical identities of the projection's
// containers — the available-targets list of the container-not-found
// usage error.
func summaryForms(containers []view.ContainerSummary) []string {
	out := make([]string, 0, len(containers))
	for _, c := range containers {
		out = append(out, c.Identity)
	}
	return out
}

// containersFooter renders the pagination footer line of the
// containers render: "Page X/Y · containers A–B of T" — X the
// effective page, Y the page count over total, A and B
// the 1-based shown range (B clamped to total; an empty
// window renders "containers 0 of T"). total is the filtered
// population the window was applied to. effLimit is the true
// effective window size (0 = windowed to the end of the list).
func containersFooter(pageNumber int, page view.Page, total, effOffset, effLimit int) string {
	if effOffset >= total {
		return fmt.Sprintf("Page %d/%d · containers 0 of %d", pageNumber, page.Pages(total), total)
	}
	b := effOffset + effLimit
	if effLimit <= 0 {
		b = total
	}
	if b > total {
		b = total
	}
	return fmt.Sprintf("Page %d/%d · containers %d–%d of %d",
		pageNumber, page.Pages(total), effOffset+1, b, total)
}

// stateColor returns the presentation color of an execution state
// value: planned dim, todo info, in-progress progress, in-review
// warning, done success. "unresolved" reads as a warning (amber).
func stateColor(s *ui.Style, state string) func(string) string {
	switch state {
	case "planned":
		return s.Dim
	case "todo":
		return s.Info
	case "in-progress":
		return s.Progress
	case "in-review":
		return s.Warning
	case "done":
		return s.Success
	case "canceled":
		// Canceled is the sanctioned exit state: presented with the
		// danger color (muted red) like a failure signal.
		return s.Error
	case "unresolved":
		return s.Warning
	default:
		return s.Dim
	}
}

// contentStateColor returns the presentation color of a content-state
// value: draft dim, review info, approved success, amended warning,
// proposed info, accepted success, superseded warning.
func contentStateColor(s *ui.Style, state string) func(string) string {
	switch state {
	case "draft":
		return s.Dim
	case "review":
		return s.Info
	case "approved":
		return s.Success
	case "amended":
		return s.Warning
	case "proposed":
		return s.Info
	case "accepted":
		return s.Success
	case "superseded":
		return s.Warning
	default:
		return s.Dim
	}
}

// planningStateColor returns the presentation color of a planning-state
// value: draft dim, approved success, immutable warning.
func planningStateColor(s *ui.Style, state string) func(string) string {
	switch state {
	case "draft":
		return s.Dim
	case "approved":
		return s.Success
	case "immutable":
		return s.Warning
	default:
		return s.Dim
	}
}

// stateIcon returns the icon of an execution state value: ✓ done,
// → in progress, • everything else. Icons decorate; the state word
// carries the meaning.
func stateIcon(state string) string {
	switch state {
	case "done":
		return ui.IconDone
	case "in-progress":
		return ui.IconArrow
	default:
		return ui.IconBullet
	}
}

// stateMark renders the colored state icon.
func stateMark(s *ui.Style, state string) string {
	return stateColor(s, state)(stateIcon(state))
}
