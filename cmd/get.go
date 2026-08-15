package cmd

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/maleolabs/eka-core/exchange"
	"github.com/maleolabs/eka-core/machine"
	"github.com/maleolabs/eka-core/metadata"
	"github.com/maleolabs/eka-core/runtime"
	"github.com/spf13/cobra"
)

// newGetCommand builds the `eka get` command: the machine interface of
// the EKA Runtime. It retrieves Canonical Knowledge Objects of the
// project owning the repository rooted at the current directory and
// emits them as the deterministic canonical JSON of the machine
// package (schema "eka-cko-v2"). No rendering, no Markdown, no
// banners: stdout carries ONLY the JSON document followed by a single
// trailing newline — machine consumers parse stdout verbatim.
//
// Query model (knowledge-shaped):
//
//	target containing ":"  identity lookup — the RSF canonical form
//	                       ("<ns>/<type>:<id>:<v>", exact instance) or
//	                       the qualified line form
//	                       ("<ns>/<type>:<id>", highest instance — the
//	                       latest knowledge version, ADR-025). The
//	                       namespace is required (the Runtime resolves
//	                       globally; unqualified forms are refused).
//	target without ":"     domain query — one of the five Engineering
//	                       Domain tokens (discovery|architecture|
//	                       planning|execution|operations): the
//	                       "domain" collection of every matching unit.
//	"containers"           the containers query: every execution
//	                       container line as a "containers" collection
//	                       (plan, items/tickets, started/ended,
//	                       container state) — see the Long help.
//
// Retrieval options (all additive, ADR-015: the default output is
// byte-identical to the pre-option schema):
//
//	--compact            one-line JSON instead of the indented form
//	--no-content         content stripped from every Document
//	--upstream/--downstream/--timeline
//	                     identity lookups only: relationship
//	                     traversal and instance-line history, appended
//	                     as "upstream"/"downstream"/"timeline" arrays
//	--type/--dimension/--phase
//	                     domain queries only: exact-match filters
//	                     (artifact type token, knowledge dimension,
//	                     phase context attribute)
//	--offset/--limit/--page
//	                     execution domain + containers query only:
//	                     the page window, appended as a "pagination"
//	                     object when applied
//	--active/--current/--container
//	                     containers query only: filter the containers
//
// Exit codes:
//
//	0  JSON document produced
//	1  workspace/repository-state refusal (no workspace, the current
//	   directory is not an EKA repository — no eka.yaml — or the
//	   repository is not registered)
//	2  usage or internal error (invalid target or domain, unknown
//	   identity, inapplicable flag combination, resolver/store
//	   failure)
func newGetCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get <target>",
		Short: "Retrieve knowledge as machine-readable CKO JSON",
		Long: `Retrieve Engineering Knowledge as machine-readable Canonical
Knowledge Object (CKO) JSON — the machine interface of the EKA
Runtime. Where 'eka view' renders human projections for reading,
'eka get' emits the deterministic canonical JSON consumed by
scripts, MCP, Atrium, VS Code and AI agents. The JSON schema is
"eka-cko-v2" and stable across minor releases; output is
deterministic (fixed field order, units sorted by canonical form,
no timestamps, no host-dependent values).

The target is either a knowledge identity, an Engineering Domain, or
the containers query:

  identity  <ns>/<type>:<id>[:<instance-version>]
            the RSF canonical form (the exact instance) or the
            qualified line form (the highest instance-version of the
            line — the latest knowledge version, ADR-025). The
            namespace is required: the Runtime resolves
            globally, so unqualified forms are refused.
  domain    one of the five Engineering Domain tokens:
            discovery | architecture | planning | execution |
            operations
            the response is a "domain" collection of every matching
            unit of the project owning the current repository,
            sorted by canonical form, carrying the canonical
            Engineering Domain name (e.g. "Execution").
  containers
            the containers query: every execution container (ctr-)
            line of the project as a "containers" collection, sorted
            by canonical form. Each container carries:
              canonicalForm   the canonical line form ("<ns>/ctr:<id>",
                              highest instance of the line)
              id              the bare container id
              plan            the first depends-on target, stored form
                              verbatim ("" when none)
              items           the work items of the container,
                              deduplicated by identity line
                              (relationship-only membership through
                              its tickets' derives-from)
              tickets         the tkt- units deriving from the
                              container
              startedAt       the container unit's created date
              endedAt         the change-log date of container-state
                              active -> completed (absent while
                              active)
              containerState  "active" or "completed"

Retrieval options (additive, schema-stable):

  --compact            emit the JSON as a single line (plus trailing
                       newline) instead of the indented form — same
                       document, same field order, fewer bytes.
  --no-content         omit the "content" field from every Document
                       in the response (identity, collection and
                       traversal documents alike). Token-saving for
                       consumers that only need identity, state and
                       relationships.
  --upstream           identity lookup: include the resolved upstream
                       units — the units the target's relationships
                       point at, sorted by canonical form — as an
                       "upstream" array of Documents appended after
                       "object_hash".
  --downstream         identity lookup: include the units that
                       reference the target (workspace-wide), sorted
                       by canonical form, as a "downstream" array of
                       Documents appended after "object_hash".
  --timeline           identity lookup: include the line's instances
                       as a "timeline" array appended after
                       "object_hash" — each entry
                       {canonical_form, instance_version, revision,
                       object_hash, change_log}, ascending
                       instance-version (the line's history order).
  --type <token>       domain query: filter by artifact type token
                       (e.g. adr, sto, ctr).
  --dimension <token>  domain query: filter by primary knowledge
                       dimension.
  --phase <value>      domain query: filter by phase context
                       attribute.

Containers filters (containers query only):

  --active, --current  keep only the containers whose container-state
                       is "active".
  --container <id>     keep only the container whose id matches: a
                       bare id, ctr-<id>, ctr:<id>, or the qualified
                       <ns>/ctr:<id> canonical form. An unknown
                       container is a usage error listing the
                       available forms.

Pagination (execution domain and containers query only):

  --offset <n>  the 0-based offset into the collection (default 0).
  --limit <n>   the page size (default 0 = no limit).
  --page <n>    the 1-based page number; requires --limit (default
                1). --offset and --page are mutually exclusive.
  A paginated response appends a "pagination" object
  {offset, limit, page, total, pages}; without pagination flags the
  output is byte-identical to the unpaged schema. --offset without
  --limit windows to the end of the collection.

Applicability: --upstream, --downstream and --timeline require an
identity target; --type, --dimension and --phase require a domain
target (the containers query refuses them); --offset, --limit and
--page apply to the execution domain and the containers query only;
--active, --current and --container apply to the containers query
only. Any other combination is allowed: --compact and --no-content
apply everywhere; traversal flags combine with each other and with
--timeline on the same identity lookup.

The ticket subcommand ('eka get ticket <target>') retrieves one
ticket's projected status (schema "eka-ticket-v1"). tkt- units are
GENERATED state projections — their files carry the header "Generated
— State Projection. Do NOT edit state here; refresh on read." — so
never edit a projection's state: transition the referenced work item
instead ('eka transition <work-item-id> <state>'). Run 'eka get
ticket --help' for the full reference.

The repository must be an EKA repository — a directory tree carrying
eka.yaml (run 'eka init' to create one) — registered in the EKA
workspace and synced first ('eka sync'). Run this command inside the
repository root.

Output contract: stdout carries ONLY the JSON document — one
document for an identity lookup, one collection for a domain query,
one collection for the containers query — followed by a single
trailing newline. No banners, no informational lines: machine
consumers parse stdout verbatim.
Errors go to stderr, one 'eka: ...' line per error (the bare
'eka get' usage summary is the exception and also goes to stderr).

Exit codes:
  0  JSON document produced
  1  workspace/repository-state refusal (no EKA workspace,
     repository not registered in the workspace)
  2  usage or internal error (invalid target or domain, unknown
     identity, inapplicable flag combination, resolver failure)`,
		Example: `  eka get feather/sto:publish-post --compact
  eka get architecture --compact --no-content --type adr
  eka get feather/adr:content-storage --upstream --downstream --no-content
  eka get feather/plan:roadmap-v1 --timeline --no-content
  eka get execution --phase mvp
  eka get containers
  eka get containers --active
  eka get containers --container wave-6
  eka get execution --limit 10 --page 2
  eka get execution --limit 5 --offset 10
  eka get ticket sto-publish-post --with-notes   (subcommand: the projected ticket as JSON)`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			compact, _ := cmd.Flags().GetBool(flagGetCompact)
			noContent, _ := cmd.Flags().GetBool(flagGetNoContent)
			withUpstream, _ := cmd.Flags().GetBool(flagGetUpstream)
			withDownstream, _ := cmd.Flags().GetBool(flagGetDownstream)
			withTimeline, _ := cmd.Flags().GetBool(flagGetTimeline)
			typeFilter, _ := cmd.Flags().GetString(flagGetType)
			dimFilter, _ := cmd.Flags().GetString(flagGetDimension)
			phaseFilter, _ := cmd.Flags().GetString(flagGetPhase)
			offset, _ := cmd.Flags().GetInt(flagGetOffset)
			limit, _ := cmd.Flags().GetInt(flagGetLimit)
			page, _ := cmd.Flags().GetInt(flagGetPage)
			active, _ := cmd.Flags().GetBool(flagGetActive)
			current, _ := cmd.Flags().GetBool(flagGetCurrent)
			container, _ := cmd.Flags().GetString(flagGetContainer)
			pagination := paginationFlags{
				offset:      offset,
				limit:       limit,
				page:        page,
				offsetGiven: cmd.Flags().Changed(flagGetOffset),
				limitGiven:  cmd.Flags().Changed(flagGetLimit),
				pageGiven:   cmd.Flags().Changed(flagGetPage),
			}
			if len(args) == 0 {
				// Machine commands never print banners: the no-argument
				// case is a usage error with the query-model summary on
				// stderr, exit 2.
				fmt.Fprintln(cmd.ErrOrStderr(), "eka: get: usage: eka get <target>")
				fmt.Fprintln(cmd.ErrOrStderr(), "eka: get:   identity  <ns>/<type>:<id>[:<instance-version>]  (canonical form or qualified line form)")
				fmt.Fprintln(cmd.ErrOrStderr(), "eka: get:   domain    discovery | architecture | planning | execution | operations")
				fmt.Fprintln(cmd.ErrOrStderr(), "eka: get:   containers  every execution container: plan, items/tickets, started/ended, status")
				fmt.Fprintln(cmd.ErrOrStderr(), "eka: get: run 'eka get --help' for the full reference")
				return &exitError{code: exitUsage}
			}
			target := args[0]
			isContainers := target == "containers"
			// Applicability rules (deterministic usage errors, exit 2):
			// traversal flags are identity-only, filter flags are
			// domain-only (containers is not a domain target),
			// pagination flags are execution/containers-only, container
			// filters are containers-only. Checked before any workspace
			// access — pure flag/target validation.
			if strings.Contains(target, ":") {
				if typeFilter != "" || dimFilter != "" || phaseFilter != "" {
					return fmt.Errorf("get: --type, --dimension and --phase require a domain target (one of the five Engineering Domains)")
				}
			} else {
				if withUpstream || withDownstream || withTimeline {
					return fmt.Errorf("get: --upstream, --downstream and --timeline require an identity target (a form containing ':')")
				}
			}
			if isContainers && (typeFilter != "" || dimFilter != "" || phaseFilter != "") {
				return fmt.Errorf("get: --type, --dimension and --phase require a domain target (one of the five Engineering Domains)")
			}
			// Pagination flag value and combination rules (pure flag
			// validation — before the target applicability checks).
			if err := pagination.validate(); err != nil {
				return fmt.Errorf("get: %w", err)
			}
			paginated := pagination.offsetGiven || pagination.limitGiven || pagination.pageGiven
			if strings.Contains(target, ":") {
				if paginated {
					return fmt.Errorf("get: pagination flags require the execution domain or containers target")
				}
			} else if !isContainers && !executionDomainToken(target) {
				if paginated {
					return fmt.Errorf("get: pagination flags apply to the execution domain and containers targets only")
				}
			}
			if !isContainers && (active || current || container != "") {
				return fmt.Errorf("get: --active, --current and --container require the containers target")
			}
			if isContainers && (active || current) && container != "" {
				return fmt.Errorf("get: --active/--current and --container are mutually exclusive")
			}
			// The resolution prologue: open (never create) the Runtime,
			// then gate on workspace and repository state.
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
			// The repository context gate (ADR-018): an EKA repository
			// is a directory tree carrying eka.yaml — without it the
			// tree is not an EKA repository and the refusal replaces
			// the not-registered branch (exit 1, the same refusal
			// class).
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
				// Repository-state refusal: deterministic message and
				// exit 1 — no JSON is produced.
				fmt.Fprintf(cmd.ErrOrStderr(), "eka: get refused: repository %s is not registered in the EKA workspace; run 'eka sync' (auto-registers) or 'eka project register' first\n", abs)
				return &exitError{code: exitFail}
			}
			opts := getOptions{
				compact:     compact,
				noContent:   noContent,
				upstream:    withUpstream,
				downstream:  withDownstream,
				timeline:    withTimeline,
				typeFilter:  typeFilter,
				dimFilter:   dimFilter,
				phaseFilter: phaseFilter,
				active:      active,
				current:     current,
				container:   container,
				pagination:  pagination,
			}
			// Issue-number targets (RFC): "#<n>" resolves to its line
			// (unambiguous across the per-group counters) before the
			// retrieval.
			if strings.HasPrefix(target, "#") {
				resolved, rerr := resolveNumberTarget(r, repo.ProjectID, target, "")
				if rerr != nil {
					return rerr
				}
				target = resolved
			}
			var out []byte
			switch {
			case isContainers:
				out, err = getContainers(r, repo, opts)
			case strings.Contains(target, ":"):
				out, err = getIdentity(r, repo, target, opts)
			default:
				out, err = getDomain(r, repo, target, opts)
			}
			if err != nil {
				return err
			}
			// Output contract: stdout carries ONLY the JSON document
			// plus its single trailing newline (Marshal emits it) —
			// written verbatim, never re-rendered.
			if _, err := cmd.OutOrStdout().Write(out); err != nil {
				return fmt.Errorf("get failed: %w", err)
			}
			return nil
		},
	}
	cmd.Flags().Bool(flagGetCompact, false, "emit the JSON as a single line (plus trailing newline)")
	cmd.Flags().Bool(flagGetNoContent, false, "omit the content field from every Document in the response")
	cmd.Flags().Bool(flagGetUpstream, false, "identity lookup: include the resolved upstream units (the units the target's relationships point at) as an \"upstream\" array")
	cmd.Flags().Bool(flagGetDownstream, false, "identity lookup: include the units that reference the target as a \"downstream\" array")
	cmd.Flags().Bool(flagGetTimeline, false, "identity lookup: include the line's instances as a \"timeline\" array ({canonical_form, instance_version, revision, object_hash, change_log})")
	cmd.Flags().String(flagGetType, "", "domain query: filter by artifact type token (e.g. adr, sto, ctr)")
	cmd.Flags().String(flagGetDimension, "", "domain query: filter by primary knowledge dimension")
	cmd.Flags().String(flagGetPhase, "", "domain query: filter by phase context attribute")
	cmd.Flags().Int(flagGetOffset, 0, "containers/execution: 0-based offset into the collection (default 0)")
	cmd.Flags().Int(flagGetLimit, 0, "containers/execution: page size (default 0 = no limit)")
	cmd.Flags().Int(flagGetPage, 1, "containers/execution: 1-based page number (requires --limit; default 1)")
	cmd.Flags().Bool(flagGetActive, false, "containers: keep only the containers with container-state active")
	cmd.Flags().Bool(flagGetCurrent, false, "containers: keep only the current (active) container")
	cmd.Flags().String(flagGetContainer, "", "containers: keep only the container whose id matches (bare id, ctr-<id>, ctr:<id> or <ns>/ctr:<id>)")
	// The ticket subcommand: the machine-readable ticket view
	// (schema "eka-ticket-v1").
	cmd.AddCommand(newGetTicketCommand())
	return cmd
}

// Flag names of the get retrieval options (declared once, shared by
// the help text and the flag lookups).
const (
	flagGetCompact    = "compact"
	flagGetNoContent  = "no-content"
	flagGetUpstream   = "upstream"
	flagGetDownstream = "downstream"
	flagGetTimeline   = "timeline"
	flagGetType       = "type"
	flagGetDimension  = "dimension"
	flagGetPhase      = "phase"
	// Containers query and pagination options (Feature: containers
	// list).
	flagGetOffset    = "offset"
	flagGetLimit     = "limit"
	flagGetPage      = "page"
	flagGetActive    = "active"
	flagGetCurrent   = "current"
	flagGetContainer = "container"
)

// getOptions carries the retrieval options of one get run, already
// validated for target applicability by the command prologue.
type getOptions struct {
	compact     bool
	noContent   bool
	upstream    bool
	downstream  bool
	timeline    bool
	typeFilter  string
	dimFilter   string
	phaseFilter string
	// Containers query filters (the containers target only).
	active    bool
	current   bool
	container string
	// Pagination (the execution domain and the containers target).
	pagination paginationFlags
}

// getIdentity resolves the identity target and builds its retrieval
// Document: the resolved unit, optionally stripped of content, with
// the requested traversal (--upstream/--downstream) and instance-line
// history (--timeline) appended as additive arrays, marshaled
// compactly or in the indented form. All runtime failures and
// inapplicable combinations surface as deterministic errors (exit 2).
func getIdentity(r *runtime.Runtime, repo runtime.Repo, target string, o getOptions) ([]byte, error) {
	// Identity lookup: canonical form (exact instance) or qualified
	// line form (highest instance — the latest knowledge version) —
	// the Resolver contract. Unqualified
	// forms are refused by the Resolver with the expected forms listed.
	unit, ok, err := r.Resolver.Resolve(target)
	if err != nil {
		return nil, fmt.Errorf("get: %w", err) // Exit 2: usage.
	}
	if !ok {
		return nil, fmt.Errorf("get: no knowledge object matches %q", target) // Exit 2.
	}
	doc, err := machine.NewDocument(unit)
	if err != nil {
		return nil, fmt.Errorf("get failed: %w", err) // Exit 2: internal.
	}
	// The line's issue number (RFC): additive "number" field — 0 (no
	// number) omits it, so the default document stays byte-identical.
	if number, nerr := r.Knowledge.NumberForLine(repo.ProjectID,
		unit.Identity.Namespace, unit.Identity.Type, unit.Identity.ID); nerr == nil {
		doc.Number = number
	}
	if o.noContent {
		doc.StripContent()
	}
	if o.upstream || o.downstream {
		// Traversal: resolve the unit sets through the Relations
		// service and attach them — nil when the flag is absent, so an
		// unrequested traversal stays absent from the JSON (additive
		// contract).
		var upstream, downstream []*machine.Document
		if o.upstream {
			units, err := r.Relations.Upstream(unit.CanonicalIdentityForm)
			if err != nil {
				return nil, fmt.Errorf("get failed: %w", err) // Exit 2: store failure.
			}
			upstream, err = machineDocuments(units, o.noContent)
			if err != nil {
				return nil, fmt.Errorf("get failed: %w", err) // Exit 2: internal.
			}
		}
		if o.downstream {
			units, err := r.Relations.Downstream(unit.CanonicalIdentityForm)
			if err != nil {
				return nil, fmt.Errorf("get failed: %w", err) // Exit 2: store failure.
			}
			downstream, err = machineDocuments(units, o.noContent)
			if err != nil {
				return nil, fmt.Errorf("get failed: %w", err) // Exit 2: internal.
			}
		}
		doc.AddRelated(upstream, downstream)
	}
	if o.timeline {
		// Timeline: the instance-line history of the identity (its
		// namespace/type/id across the workspace), ascending
		// instance-version.
		entries, err := r.Timeline.Line(unit.Identity.Namespace, unit.Identity.Type, unit.Identity.ID)
		if err != nil {
			return nil, fmt.Errorf("get failed: %w", err) // Exit 2: store failure.
		}
		tl := make([]machine.TimelineEntry, 0, len(entries))
		for _, e := range entries {
			tl = append(tl, machine.TimelineEntry{
				CanonicalForm:   e.Form,
				InstanceVersion: e.InstanceVersion,
				Revision:        e.Revision,
				ObjectHash:      e.ObjectHash,
				ChangeLog:       machineChangeLog(e.ChangeLog),
			})
		}
		doc.AddTimeline(tl)
	}
	if o.compact {
		return doc.MarshalCompact()
	}
	return doc.Marshal()
}

// dedupLinesLatest collapses a unit set to one unit per identity line
// — the highest (latest) instance-version of each (namespace, type,
// id) line, ADR-025 — sorted by canonical form (deterministic). The
// domain query contract: `count` = the number of unique lines, and
// every unit is the newest knowledge version of its line; the history
// of a line stays reachable through the explicit versioned form.
func dedupLinesLatest(units []*exchange.Unit) []*exchange.Unit {
	byKey := make(map[string]*exchange.Unit)
	for _, u := range units {
		key := u.Identity.Namespace + "/" + u.Identity.Type + ":" + u.Identity.ID
		cur, ok := byKey[key]
		if !ok || u.Identity.InstanceVersion > cur.Identity.InstanceVersion {
			byKey[key] = u
		}
	}
	out := make([]*exchange.Unit, 0, len(byKey))
	for _, u := range byKey {
		out = append(out, u)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CanonicalIdentityForm < out[j].CanonicalIdentityForm
	})
	return out
}

// getDomain runs the domain query: every unit of the Engineering
// Domain matching the exact-match filters (--type/--dimension/--phase)
// as a "domain" collection, sorted by canonical form, optionally
// stripped of content, windowed (execution domain only — the
// pagination flags), marshaled compactly or in the indented form.
func getDomain(r *runtime.Runtime, repo runtime.Repo, target string, o getOptions) ([]byte, error) {
	name, ok := domainTokenName(target)
	if !ok {
		available := append([]string{"containers"}, domainTokenList()...)
		return nil, fmt.Errorf("get: unknown target %q — available targets: %s", target, strings.Join(available, ", "))
	}
	units, err := r.Knowledge.Search(runtime.SearchQuery{
		ProjectID: repo.ProjectID,
		Domain:    name,
		Type:      o.typeFilter,
		Dimension: o.dimFilter,
		Phase:     o.phaseFilter,
	})
	if err != nil {
		return nil, fmt.Errorf("get failed: %w", err) // Exit 2: store failure.
	}
	units = dedupLinesLatest(units)
	col, err := machine.NewCollection(name, units)
	if err != nil {
		return nil, fmt.Errorf("get failed: %w", err) // Exit 2: internal.
	}
	if o.noContent {
		// NewCollection builds the unit documents internally; strip
		// each after the fact (the retrieval document builder contract:
		// StripContent before marshaling).
		for _, d := range col.Units {
			d.StripContent()
		}
	}
	// Pagination (execution domain only): the effective window is
	// applied BEFORE marshaling; the default (no pagination flags)
	// stays byte-identical to the unpaged schema (no "pagination"
	// field, count == the full unit count).
	if ok, offset, limit := o.pagination.apply(len(col.Units)); ok {
		col.Page(offset, limit)
	}
	if o.compact {
		return col.MarshalCompact()
	}
	return col.Marshal()
}

// getContainers runs the containers query: every execution container
// (ctr-) line of the project as a "containers" collection, sorted by
// canonical form, with the plan, work items/tickets and lifecycle
// dates, optionally filtered (--active/--current, --container) and
// windowed (--offset/--limit/--page), marshaled compactly or in the
// indented form.
func getContainers(r *runtime.Runtime, repo runtime.Repo, o getOptions) ([]byte, error) {
	units, err := r.Knowledge.Search(runtime.SearchQuery{
		ProjectID: repo.ProjectID,
		Domain:    "Execution",
	})
	if err != nil {
		return nil, fmt.Errorf("get failed: %w", err) // Exit 2: store failure.
	}
	col, err := machine.NewContainerCollection(units)
	if err != nil {
		return nil, fmt.Errorf("get failed: %w", err) // Exit 2: internal.
	}
	if o.active || o.current {
		col.FilterActive()
	}
	if o.container != "" {
		form, ok := containerFormByTarget(col, o.container)
		if !ok {
			return nil, fmt.Errorf("get: container %q not found — available containers: %s",
				o.container, strings.Join(containerForms(col), ", ")) // Exit 2: usage.
		}
		col.FilterContainer(form)
	}
	// The page window applies to the filtered list; Count stays the
	// total container count.
	if ok, offset, limit := o.pagination.apply(len(col.Containers)); ok {
		col.Page(offset, limit)
	}
	if o.compact {
		return col.MarshalCompact()
	}
	return col.Marshal()
}

// containerFormByTarget resolves a user-supplied container target
// against the collection: a bare id, "ctr-<id>", "ctr:<id>" (matched
// against the container id), or the qualified "<ns>/ctr:<id>" form
// (matched against the canonical form). It returns the matched
// canonical form.
func containerFormByTarget(col *machine.ContainerCollection, raw string) (string, bool) {
	if strings.Contains(raw, "/") {
		for _, c := range col.Containers {
			if c.CanonicalForm == raw {
				return c.CanonicalForm, true
			}
		}
		return "", false
	}
	id := strings.TrimPrefix(strings.TrimPrefix(raw, "ctr-"), "ctr:")
	for _, c := range col.Containers {
		if c.ID == id {
			return c.CanonicalForm, true
		}
	}
	return "", false
}

// containerForms lists the canonical forms of the collection's
// containers — the deterministic available-targets list of the
// container-not-found usage error.
func containerForms(col *machine.ContainerCollection) []string {
	out := make([]string, 0, len(col.Containers))
	for _, c := range col.Containers {
		out = append(out, c.CanonicalForm)
	}
	return out
}

// machineDocuments maps resolved units to their machine Documents
// (stripContent applies to each — the --no-content contract covers
// traversal documents too). The input order is preserved: the
// Relations service already sorts by canonical form.
func machineDocuments(units []*exchange.Unit, stripContent bool) ([]*machine.Document, error) {
	out := make([]*machine.Document, 0, len(units))
	for _, u := range units {
		d, err := machine.NewDocument(u)
		if err != nil {
			return nil, err
		}
		if stripContent {
			d.StripContent()
		}
		out = append(out, d)
	}
	return out, nil
}

// machineChangeLog maps runtime change-log entries (exchange payload
// order) to the machine change-log entries of the timeline projection.
func machineChangeLog(entries []exchange.ChangeLogEntry) []machine.ChangeLogEntry {
	if len(entries) == 0 {
		return nil
	}
	out := make([]machine.ChangeLogEntry, 0, len(entries))
	for _, e := range entries {
		out = append(out, machine.ChangeLogEntry{Date: e.Date, Domain: e.Domain, From: e.From, To: e.To, By: e.By})
	}
	return out
}

// domainTokens are the five Engineering Domain query tokens in stratum
// order, mapped to the canonical Engineering Domain names (the values
// carried by Classification.Domain of stored units and the machine
// JSON). Deterministic — never derived from map iteration.
var domainTokens = []struct {
	token string
	name  string
}{
	{"discovery", "Discovery"},
	{"architecture", "Architecture"},
	{"planning", "Planning"},
	{"execution", "Execution"},
	{"operations", "Operations"},
}

// domainTokenName maps a query token to its canonical Engineering
// Domain name; the second return value is false for unknown tokens.
func domainTokenName(token string) (string, bool) {
	for _, d := range domainTokens {
		if d.token == token {
			return d.name, true
		}
	}
	return "", false
}

// executionDomainToken reports whether the target is the execution
// domain query token — the only domain the pagination flags apply to.
func executionDomainToken(target string) bool {
	name, ok := domainTokenName(target)
	return ok && name == "Execution"
}

// domainTokenList renders the five query tokens as the deterministic
// "a | b | c" usage list of the domain usage error.
func domainTokenList() []string {
	out := make([]string, 0, len(domainTokens))
	for _, d := range domainTokens {
		out = append(out, d.token)
	}
	return out
}
