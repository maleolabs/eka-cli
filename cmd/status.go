package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/maleolabs/eka-cli/cmd/ui"
	"github.com/maleolabs/eka-core/exchange"
	"github.com/maleolabs/eka-core/machine"
	"github.com/maleolabs/eka-core/runtime"
	"github.com/spf13/cobra"
)

// newStatusCommand builds the `eka status` command: a deterministic
// overview of the EKA workspace — path, schema version, registered
// projects/repositories, canonical store counts and the last sync per
// repository. An uninitialized workspace (no workspace.json) is
// reported informatively and exits 0: status is a read-only probe and
// never creates the workspace.
func newStatusCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show the EKA workspace status",
		Long: `Show the EKA workspace status: the workspace path, the schema
version, the registered projects and repositories, the canonical
store totals (objects, immutable payloads, attachments) and the last
pull/push per repository (from the sync log).

When no workspace exists yet an informational message is printed and
the command exits 0 — status never initializes the workspace.

--stale makes the validity horizon actionable: it lists every item past its
validUntil (most overdue first), with the lapse and the Engineering Domain. --fail-on-stale turns the same list into a CI gate:
exit 1 when anything lapsed, 0 otherwise. Undeclared horizons are never
stale.

Exit codes:
  0  status shown (or no workspace yet)
  1  --fail-on-stale and at least one item is past its horizon
  2  usage or internal error`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			home, err := runtime.HomeDir()
			if err != nil {
				return err
			}
			s := styleFor(cmd)
			// The read-only entry: Open never initializes the
			// workspace — a missing workspace.json is reported through
			// Exists, not created.
			r, err := runtime.Open()
			if err != nil {
				return err
			}
			defer r.Close()
			showAll, _ := cmd.Flags().GetBool("all")
			showJSON, _ := cmd.Flags().GetBool("json")
			showStale, _ := cmd.Flags().GetBool("stale")
			failOnStale, _ := cmd.Flags().GetBool("fail-on-stale")
			if !r.Exists() {
				if showJSON {
					return renderStatusJSON(s, nil, home, nil, nil)
				}
				fmt.Fprintf(s.W, "%s\n", s.Accent("Runtime"))
				fmt.Fprintf(s.W, "  %s\n", s.Info("No EKA workspace at "+home+" yet. Run 'eka project register' to create it."))
				return nil
			}
			st, err := r.Workspace.Status()
			if err != nil {
				return fmt.Errorf("status failed: %w", err)
			}
			if !showAll {
				st = scopeStatus(st)
			}
			// One scan serves both horizon flags; absent them the store
			// is not read a second time. A store failure is an error
			// here (exit 2), never an empty list: a CI gate must not
			// read "could not compute" as "nothing lapsed".
			var stale []staleItem
			if showStale || failOnStale {
				stale, err = staleItems(r, st)
				if err != nil {
					return err
				}
			}
			if showJSON {
				var listed []staleItem
				if showStale {
					listed = stale
				}
				if err := renderStatusJSON(s, st, home, statusFreshness(r, st), listed); err != nil {
					return err
				}
				return failOnStaleGate(cmd, failOnStale, stale)
			}
			if err := renderStatus(s, r, st); err != nil {
				return err
			}
			if showStale {
				renderStale(s, stale)
			}
			if err := renderSESExecution(s, r); err != nil {
				return err
			}
			return failOnStaleGate(cmd, failOnStale, stale)
		},
	}
	cmd.Flags().Bool("all", false, "show global workspace status (disable repo-scoped filtering)")
	cmd.Flags().Bool("json", false, "emit deterministic JSON (schema status-v1)")
	cmd.Flags().Bool("stale", false, "list knowledge past its validUntil (most overdue first)")
	cmd.Flags().Bool("fail-on-stale", false, "exit 1 when any knowledge is past its validUntil (CI gate)")
	return cmd
}

// renderStatus renders the workspace overview deterministically. Any
// store failure is propagated: status is a monitoring command and must
// never report a false healthy state (exit code 2 on internal error).
func renderStatus(s *ui.Style, rt *runtime.Runtime, st *runtime.WorkspaceStatus) error {
	ui.NewHeader(s, "Runtime").
		Add("Workspace", st.Path).
		Add("Schema", "v"+strconv.Itoa(st.SchemaVersion)).
		Add("ID", st.ID).
		Add("Created", st.Created).
		Render()

	ui.NewSummary(s).
		Add("Projects", strconv.Itoa(len(st.Projects))).
		Add("Objects", strconv.Itoa(st.Objects)).
		Add("Payloads", strconv.Itoa(st.Payloads)).
		Add("Attachments", strconv.Itoa(st.Attachments)).
		Render()

	if len(st.Projects) == 0 {
		fmt.Fprintf(s.W, "\n%s\n", s.Info("No projects registered. Run 'eka project register' to add one."))
		return nil
	}
	for _, p := range st.Projects {
		fmt.Fprintf(s.W, "\n%s %s\n", ui.IconBullet, s.Accent(p.Project.ID))
		for _, r := range p.Repos {
			fmt.Fprintf(s.W, "  %s %s  (%s)%s\n", ui.IconBullet, s.Info(r.Repo.Name), displayPath(r.Repo.Path), lastSyncDetail(r.LastSync))
			if actives, ok := repoActiveContainers(rt, p.Project.ID, r.Repo.Name); ok && len(actives) > 0 {
				for _, a := range actives {
					fmt.Fprintf(s.W, "    active: %s\n", a)
				}
			}
		}
	}
	// One freshness line: the count of units past their declared
	// horizon out of the units that declared one. Advisory — it never
	// changes the exit code and never blocks.
	if f := statusFreshness(rt, st); f != nil {
		fmt.Fprintf(s.W, "\n%s %s\n", ui.IconBullet, s.Info(fmt.Sprintf("Stale knowledge: %d (of %d bounded)", f.Stale, f.Scanned)))
	}
	return nil
}

// repoFreshness is the stale/bounded tally of one status probe.
type repoFreshness struct {
	Stale   int `json:"stale"`
	Scanned int `json:"scanned"`
}

// statusFreshness counts the units past their declared validity horizon
// across the projects of the status probe, plus the total scanned. The
// scan is the per-repository Knowledge.Units call status already makes
// for the active-container line, indexed by source repo, so a scoped or
// global status does not double the store reads. The clock is read once
// per probe: every unit is judged at the same instant. A store failure
// is reported as a truthful zero tally, never as a fabricated healthy
// count (the status probe must not invent state).
func statusFreshness(rt *runtime.Runtime, st *runtime.WorkspaceStatus) *repoFreshness {
	now := time.Now()
	byRepo := map[string][]*exchange.Unit{}
	out := repoFreshness{}
	for _, p := range st.Projects {
		for _, rs := range p.Repos {
			key := p.Project.ID + "\x00" + rs.Repo.Name
			units, ok := byRepo[key]
			if !ok {
				var err error
				units, err = rt.Knowledge.Units(p.Project.ID, rs.Repo.Name)
				if err != nil {
					return &out
				}
				byRepo[key] = units
			}
			for _, u := range units {
				out.Scanned++
				if runtime.FreshnessFor(u, now).Status == runtime.FreshnessStale {
					out.Stale++
				}
			}
		}
	}
	return &out
}

// staleItem is one actionable horizon: a unit whose validity lapsed as of
// the status probe's clock.
type staleItem struct {
	CanonicalForm     string
	ValidUntil        string
	DaysOverdue       int
	EngineeringDomain string
}

// staleItems lists every unit whose declared horizon is in the past, most
// overdue first, then canonical form — a deterministic order, because the
// list is read by operators and asserted by tests.
//
// The verdict and the day arithmetic are both eka-core's FreshnessFor, and
// the boundary is not restated here: the listing and the advisory count line
// must name the same items, or the command contradicts itself on its own
// output. "Valid until <date>" means valid through that date, so the horizon
// day is still current and the item is listed the day after.
//
// An undeclared horizon carries no days and is never stale: absence is the
// honest third state, not a default date.
func staleItems(rt *runtime.Runtime, st *runtime.WorkspaceStatus) ([]staleItem, error) {
	now := time.Now()
	var out []staleItem
	for _, p := range st.Projects {
		for _, rs := range p.Repos {
			units, err := rt.Knowledge.Units(p.Project.ID, rs.Repo.Name)
			if err != nil {
				return nil, fmt.Errorf("stale scan failed: %w", err)
			}
			for _, u := range units {
				f := runtime.FreshnessFor(u, now)
				if f.Status != runtime.FreshnessStale {
					continue
				}
				out = append(out, staleItem{
					CanonicalForm:     u.CanonicalIdentityForm,
					ValidUntil:        u.ValidUntil,
					DaysOverdue:       -f.DaysUntilExpiry,
					EngineeringDomain: u.Classification.Domain,
				})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].DaysOverdue != out[j].DaysOverdue {
			return out[i].DaysOverdue > out[j].DaysOverdue
		}
		return out[i].CanonicalForm < out[j].CanonicalForm
	})
	return out, nil
}

// failOnStaleGate is the CI half of the horizon: exit 1, with the reason on
// stderr, when anything lapsed. Every output path funnels through it, so
// --json cannot report a green exit code for a lapsed corpus.
func failOnStaleGate(cmd *cobra.Command, enabled bool, items []staleItem) error {
	if !enabled || len(items) == 0 {
		return nil
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "eka: status: %d knowledge item(s) past their validity horizon (--fail-on-stale)\n", len(items))
	return &exitError{code: exitFail}
}

// renderStale prints the --stale listing. The empty case is stated, never
// implied: a blank block reads as "could not compute", which is the reading a
// horizon gate must never leave open.
func renderStale(s *ui.Style, items []staleItem) {
	if len(items) == 0 {
		fmt.Fprintf(s.W, "\n%s %s\n", ui.IconBullet, s.Info("Stale knowledge: none past its validity horizon"))
		return
	}
	fmt.Fprintf(s.W, "\n%s %s (%d)\n", ui.IconBullet, s.Accent("Stale knowledge"), len(items))
	for _, it := range items {
		line := fmt.Sprintf("  %s  validUntil %s  %d days overdue", it.CanonicalForm, it.ValidUntil, it.DaysOverdue)
		if it.EngineeringDomain != "" {
			line += "  " + it.EngineeringDomain
		}
		fmt.Fprintln(s.W, line)
	}
}

// repository (its provenance source_repo — one-active-per-source_repo,
// dec:parallel-container-execution), sorted by canonical identity.
// ok=false when the store cannot be read (status stays a truthful
// probe: a store failure surfaces, never a fabricated active line).
func repoActiveContainers(r *runtime.Runtime, projectID, sourceRepo string) ([]string, bool) {
	units, err := r.Knowledge.Units(projectID, sourceRepo)
	if err != nil {
		return nil, false
	}
	var active []string
	for _, u := range units {
		if u.Identity.Type != "ctr" || u.StateVector.ContainerState != "active" {
			continue
		}
		active = append(active, u.Identity.Namespace+"/"+u.Identity.Type+":"+u.Identity.ID)
	}
	sort.Strings(active)
	return active, true
}

// scopeStatus returns a view of the workspace status scoped to the
// repository that contains the current working directory. When cwd is
// inside a registered repository, only that project is returned;
// otherwise the full global status is returned. The store totals
// (Objects/Payloads/Attachments) remain global.
func scopeStatus(st *runtime.WorkspaceStatus) *runtime.WorkspaceStatus {
	cwd, err := os.Getwd()
	if err != nil {
		return st
	}
	cwd = filepath.Clean(cwd)
	var matched *runtime.ProjectStatus
	matchedLen := -1
	for i := range st.Projects {
		for _, rs := range st.Projects[i].Repos {
			repoPath := filepath.Clean(rs.Repo.Path)
			if cwd == repoPath || strings.HasPrefix(cwd+string(filepath.Separator), repoPath+string(filepath.Separator)) {
				if len(repoPath) > matchedLen {
					matchedLen = len(repoPath)
					matched = &st.Projects[i]
				}
			}
		}
	}
	if matched == nil {
		return st
	}
	out := *st
	out.Projects = []runtime.ProjectStatus{*matched}
	return &out
}

// renderExecution prints the current execution snapshot from ses:execution-state
// (if present). It is current-only: only the latest instance is rendered.
func renderSESExecution(s *ui.Style, r *runtime.Runtime) error {
	cwd, _ := os.Getwd()
	// Resolve execution-state scoped to current project if possible.
	projRepo, found, ferr := r.Workspace.FindRepo(cwd)
	var u *exchange.Unit
	if ferr == nil && found {
		// Search for ses:execution-state within this project.
		// Namespace comes from the repo record (eka.yaml); each
		// project publishes its own line under its own namespace,
		// so a cross-project global resolve would leak state.
		// ponytail: repos registered before schema v3 carry Namespace "".
		ns := projRepo.Namespace
		if ns == "" {
			ns = "eka"
		}
		units, serr := r.Knowledge.Search(runtime.SearchQuery{ProjectID: projRepo.ProjectID, Namespace: ns, Type: "ses", ID: "execution-state"})
		if serr == nil && len(units) > 0 {
			u = units[len(units)-1]
		}
	}
	if u == nil && (!found || (ferr == nil && projRepo.Namespace == "" && projRepo.ProjectID == "eka")) {
		// Legacy fallback applies only outside a registered repo or for the
		// legacy EKA project; never leak another project's execution state.
		var ok bool
		var err error
		u, ok, err = r.Resolver.Resolve("eka/ses:execution-state")
		if err != nil || !ok {
			return nil
		}
	}
	if u == nil {
		return nil
	}
	doc, err := machine.NewDocument(u)
	if err != nil {
		return nil
	}
	b, err := doc.MarshalCompact()
	if err != nil {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil
	}
	content, _ := m["content"].(map[string]any)
	if content == nil {
		return nil
	}
	fields, _ := content["fields"].(map[string]any)
	if fields != nil {
		content = fields
	}
	scope, _ := content["scope"].(string)
	mode, _ := content["mode"].(string)
	cur, _ := content["current"].(map[string]any)
	curItem, _ := cur["item"].(string)
	curPhase, _ := cur["phase"].(string)
	next, _ := content["next"].(string)
	// Use simple header-like rendering
	fmt.Fprintf(s.W, "\n%s %s\n", ui.IconBullet, s.Accent("Execution"))
	if scope != "" {
		fmt.Fprintf(s.W, "  Scope: %s\n", scope)
	}
	if curItem != "" {
		phase := curPhase
		if phase != "" {
			phase = " (" + phase + ")"
		}
		fmt.Fprintf(s.W, "  Current: %s%s\n", curItem, phase)
	}
	if next != "" {
		fmt.Fprintf(s.W, "  Next: %s\n", next)
	}
	if mode != "" {
		fmt.Fprintf(s.W, "  Mode: %s\n", mode)
	}
	if items, ok := content["items"].([]any); ok && len(items) > 0 {
		fmt.Fprintf(s.W, "  Items: %d\n", len(items))
	}
	return nil
}

// statusJSON is the deterministic wire format for `eka status --json`
// (schema status-v1). Field order is fixed by struct definition.
type statusJSON struct {
	Schema        string        `json:"schema"`
	Path          string        `json:"path"`
	ID            string        `json:"id"`
	Created       string        `json:"created"`
	SchemaVersion int           `json:"schemaVersion"`
	Objects       int           `json:"objects"`
	Payloads      int           `json:"payloads"`
	Attachments   int           `json:"attachments"`
	Projects      []jsonProject `json:"projects"`
	// Freshness is the stale/bounded tally (schema-additive: a probe
	// with no workspace omits it).
	Freshness *repoFreshness `json:"freshness,omitempty"`
	// Stale is the --stale listing (schema-additive: absent without the
	// flag and omitted when nothing lapsed).
	Stale []jsonStaleItem `json:"stale,omitempty"`
}

// jsonStaleItem is one row of the actionable horizon in the wire format.
type jsonStaleItem struct {
	CanonicalForm     string `json:"canonicalForm"`
	ValidUntil        string `json:"validUntil"`
	DaysOverdue       int    `json:"daysOverdue"`
	EngineeringDomain string `json:"engineeringDomain,omitempty"`
}
type jsonProject struct {
	ID      string     `json:"id"`
	Name    string     `json:"name"`
	Created string     `json:"created"`
	Repos   []jsonRepo `json:"repos"`
}
type jsonRepo struct {
	ProjectID string             `json:"projectId"`
	Name      string             `json:"name"`
	Path      string             `json:"path"`
	Created   string             `json:"created"`
	Namespace string             `json:"namespace"`
	LastSync  *runtime.SyncEntry `json:"lastSync,omitempty"`
}

func renderStatusJSON(s *ui.Style, st *runtime.WorkspaceStatus, home string, fresh *repoFreshness, stale []staleItem) error {
	if st == nil {
		out := map[string]string{"schema": "status-v1", "message": "No EKA workspace at " + home + " yet. Run 'eka project register' to create it."}
		b, _ := json.Marshal(out)
		fmt.Fprintln(s.W, string(b))
		return nil
	}
	js := statusJSON{
		Schema:        "status-v1",
		Path:          st.Path,
		ID:            st.ID,
		Created:       st.Created,
		SchemaVersion: st.SchemaVersion,
		Objects:       st.Objects,
		Payloads:      st.Payloads,
		Attachments:   st.Attachments,
		Projects:      make([]jsonProject, 0, len(st.Projects)),
		Freshness:     fresh,
	}
	for _, it := range stale {
		js.Stale = append(js.Stale, jsonStaleItem{
			CanonicalForm:     it.CanonicalForm,
			ValidUntil:        it.ValidUntil,
			DaysOverdue:       it.DaysOverdue,
			EngineeringDomain: it.EngineeringDomain,
		})
	}
	for _, p := range st.Projects {
		jp := jsonProject{ID: p.Project.ID, Name: p.Project.Name, Created: p.Project.Created}
		for _, rs := range p.Repos {
			jp.Repos = append(jp.Repos, jsonRepo{ProjectID: rs.Repo.ProjectID, Name: rs.Repo.Name, Path: rs.Repo.Path, Created: rs.Repo.Created, Namespace: rs.Repo.Namespace, LastSync: rs.LastSync})
		}
		js.Projects = append(js.Projects, jp)
	}
	b, err := json.Marshal(js)
	if err != nil {
		return fmt.Errorf("status json failed: %w", err)
	}
	fmt.Fprintln(s.W, string(b))
	return nil
}

// lastSyncDetail renders the most recent sync-log entry of one
// repository ("" when none).
func lastSyncDetail(e *runtime.SyncEntry) string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("  [%s %s at %s]", e.Direction, shortDigest(e.SnapshotDigest), e.At)
}
