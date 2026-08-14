package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/maleolabs/eka-cli/cmd/ui"
	"github.com/maleolabs/eka-core/conformance"
	"github.com/maleolabs/eka-core/exchange"
	"github.com/maleolabs/eka-core/metadata"
	"github.com/maleolabs/eka-core/runtime"
	"github.com/spf13/cobra"
)

// This file implements the batch forms of the authoring commands:
// `eka new --file <batch.json>` (scaffold a set of related drafts in
// one invocation) and `eka publish --all` / `eka publish --pending`
// (publish every pending draft of the project in topological order,
// referenced drafts first).
//
// The batch draft graph: the pending drafts of the project are the
// nodes; a relationship target that addresses another pending draft is
// an edge (the referenced draft must be published first). The batch
// publish refuses deterministically before publishing anything on a
// cycle or on a reference that resolves neither to a pending draft nor
// to a published object; the publish loop itself stays per-draft atomic
// (Publish's all-or-nothing contract), so a draft failing CKO-level
// validation stops the run with the already-published objects kept.
//
// Version note: this command layer implements the batch semantics
// against the pinned eka-core v1.0.0 API (Authoring.Drafts, Publish,
// conformance.ScanFile, Runtime.Resolver) because the CLI ships against
// the pinned release. The same semantics live in the eka-core batch
// Authoring API (runtime.NewDraftBatch / PublishBatch) for the next
// core release; when the CLI bumps to it, this file's orchestration is
// replaced by those two calls and the local ordering helper goes away.

// batchFile is the deterministic `eka new --file` schema. The top-level
// object carries exactly one key: "drafts", the array of targets.
type batchFile struct {
	Drafts []batchTarget `json:"drafts"`
}

// batchTarget is one target of the batch: type + id (required),
// dimension/phase (optional, same rules as the single-target flags),
// the five relationship keys in the §3.2 camelCase spelling, and the
// inline content object merged over the type's required-section
// placeholders.
type batchTarget struct {
	Type          string              `json:"type"`
	ID            string              `json:"id"`
	Dimension     string              `json:"dimension,omitempty"`
	Phase         string              `json:"phase,omitempty"`
	Relationships map[string][]string `json:"relationships,omitempty"`
	Content       map[string]any      `json:"content,omitempty"`
}

// batchRelationshipKeys are the relationship keys the batch schema
// accepts, in the canonical field order of the §3.2 authoring spelling.
var batchRelationshipKeys = []string{"dependsOn", "derivesFrom", "validates", "supersedes", "amends"}

// readBatchFile reads and validates the batch file with a strict,
// deterministic decode: unknown top-level or per-target fields are
// refused (json.Decoder.DisallowUnknownFields reports the first unknown
// field in document order), a non-array "drafts" or a non-object
// "content" is refused, the relationship keys must be exactly the five
// canonical ones, the type must be a known EKA token, the id non-empty,
// and batch identities unique.
func readBatchFile(path string) (*batchFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("cannot read batch file %s: %w", path, err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var batch batchFile
	if err := dec.Decode(&batch); err != nil {
		return nil, fmt.Errorf("batch file %s is not valid: %v", path, err)
	}
	// Trailing data after the first JSON document is refused
	// (deterministic).
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return nil, fmt.Errorf("batch file %s carries trailing data after the first JSON document", path)
	}
	if len(batch.Drafts) == 0 {
		return nil, fmt.Errorf("batch file %s carries no drafts (\"drafts\" must be a non-empty array)", path)
	}
	seen := make(map[string]int, len(batch.Drafts)) // "type:id" -> 1-based position
	for i, t := range batch.Drafts {
		if _, ok := conformance.DomainForToken(t.Type); !ok {
			return nil, fmt.Errorf("batch draft %d of %d: unknown artifact type %q; expected one of the 27 EKA type tokens",
				i+1, len(batch.Drafts), t.Type)
		}
		if t.ID == "" {
			return nil, fmt.Errorf("batch draft %d of %d: \"id\" must be a non-empty string", i+1, len(batch.Drafts))
		}
		if prev, dup := seen[t.Type+":"+t.ID]; dup {
			return nil, fmt.Errorf("batch drafts %d and %d share the identity %s:%s; batch identities must be unique",
				prev, i+1, t.Type, t.ID)
		}
		seen[t.Type+":"+t.ID] = i + 1
		for key := range t.Relationships {
			if !containsString(batchRelationshipKeys, key) {
				return nil, fmt.Errorf("batch draft %d of %d (%s:%s): unknown relationship key %q; expected %s",
					i+1, len(batch.Drafts), t.Type, t.ID, key, strings.Join(batchRelationshipKeys, ", "))
			}
		}
	}
	return &batch, nil
}

// containsString reports whether values contains v.
func containsString(values []string, v string) bool {
	for _, x := range values {
		if x == v {
			return true
		}
	}
	return false
}

// resolveBatchScope resolves the project and namespace of the batch
// commands from the repository alone (the same resolution `eka new`
// uses for an unqualified target, spec §3.2 + ADR-018): the project is
// the repository's project, the namespace is the repository's default
// namespace. Outside an EKA repository the batch is refused
// deterministically.
func resolveBatchScope(r *runtime.Runtime) (project, ns string, err error) {
	abs, aerr := filepath.Abs(".")
	if aerr != nil {
		return "", "", fmt.Errorf("cannot resolve the current directory: %w", aerr)
	}
	abs = filepath.Clean(abs)
	repo, found, ferr := r.Workspace.FindRepo(abs)
	if ferr != nil {
		return "", "", ferr
	}
	if !found {
		// The repository context gate (ADR-018): without eka.yaml the
		// tree is not an EKA repository — deterministic refusal.
		_, _, hasMeta, merr := metadata.Find(abs)
		if merr != nil {
			return "", "", merr
		}
		if !hasMeta {
			return "", "", fmt.Errorf("refused: %s is not an EKA repository (no eka.yaml); run 'eka init' first", abs)
		}
		return "", "", fmt.Errorf("cannot resolve a project here; run inside a registered repository or run 'eka sync' once to resolve the repository identity from eka.yaml")
	}
	if repo.ProjectID == "" {
		return "", "", fmt.Errorf("cannot resolve a project here; run inside a registered repository")
	}
	if repo.Namespace == "" {
		return "", "", fmt.Errorf("cannot resolve a namespace here; run inside a registered repository")
	}
	return repo.ProjectID, repo.Namespace, nil
}

// --- eka new --file ----------------------------------------------------

// runNewBatch scaffolds the drafts of a batch file in declaration
// order, all-or-nothing: when any target cannot be scaffolded (a
// collision, an unknown type, a tkt-/ctr- guard violation, an invalid
// phase), the run refuses and removes the drafts it created.
func runNewBatch(cmd *cobra.Command, r *runtime.Runtime, by conformance.AuthorIdentity, path string) error {
	batch, err := readBatchFile(path)
	if err != nil {
		return refuse(cmd, "new: %v", err)
	}
	project, ns, err := resolveBatchScope(r)
	if err != nil {
		return refuse(cmd, "new: %v", err)
	}

	created := make([]*runtime.Draft, 0, len(batch.Drafts))
	for i, t := range batch.Drafts {
		// The pinned core API merges content from a file path only, so
		// the batch's inline content object is staged in a temp file
		// (removed right after the scaffold; never part of the output).
		contentFile := ""
		if len(t.Content) > 0 {
			contentFile, err = stageBatchContent(t.Content)
			if err != nil {
				return refuse(cmd, "new: %v", fmt.Errorf("batch draft %d of %d (%s:%s): cannot stage its content: %v",
					i+1, len(batch.Drafts), t.Type, t.ID, err))
			}
		}
		d, err := runtime.Authoring.NewDraft(r, runtime.NewDraftRequest{
			Project:       project,
			Namespace:     ns,
			Type:          t.Type,
			ID:            t.ID,
			Dimension:     t.Dimension,
			Phase:         t.Phase,
			By:            by,
			Relationships: batchRelationships(t),
			ContentFile:   contentFile,
		})
		if contentFile != "" {
			_ = os.Remove(contentFile)
		}
		if err != nil {
			// All-or-nothing (spec §5.1 applied to the set): remove the
			// drafts this run created (best-effort; the refusal is the
			// verdict).
			for _, c := range created {
				_ = os.Remove(c.Path)
			}
			return refuse(cmd, "new: %v",
				fmt.Errorf("batch draft %d of %d (%s:%s) cannot be scaffolded: %v; the %d draft(s) created by this run were removed",
					i+1, len(batch.Drafts), t.Type, t.ID, err, len(created)))
		}
		created = append(created, d)
	}
	renderBatchNew(styleFor(cmd), project, ns, created)
	return nil
}

// stageBatchContent writes the inline batch content object to a temp
// JSON file (the pinned core API's ContentFile input) and returns its
// path; the caller removes it after the scaffold.
func stageBatchContent(content map[string]any) (string, error) {
	data, err := json.Marshal(content)
	if err != nil {
		return "", err
	}
	f, err := os.CreateTemp("", "eka-batch-content-*.json")
	if err != nil {
		return "", err
	}
	path := f.Name()
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(path)
		return "", err
	}
	if err := f.Close(); err != nil {
		os.Remove(path)
		return "", err
	}
	return path, nil
}

// batchRelationships converts a target's camelCase relationship map
// into the canonical exchange.Relationship list (the same field order
// collectRelationships uses; the values are the raw references).
func batchRelationships(t batchTarget) []exchange.Relationship {
	var out []exchange.Relationship
	for _, key := range batchRelationshipKeys {
		for _, target := range t.Relationships[key] {
			target = strings.TrimSpace(target)
			if target == "" {
				continue
			}
			out = append(out, exchange.Relationship{Type: camelToKebab(key), Target: target})
		}
	}
	return out
}

// camelToKebab renders the §3.2 camelCase relationship key in the
// internal kebab spelling (dependsOn -> depends-on), mirroring the
// conformance helper without importing it.
func camelToKebab(s string) string {
	var b strings.Builder
	for i, r := range s {
		if i > 0 && r >= 'A' && r <= 'Z' {
			b.WriteByte('-')
		}
		b.WriteRune(r)
	}
	return strings.ToLower(b.String())
}

// renderBatchNew renders the deterministic batch-scaffold summary:
// the header (project + count), the created identities in declaration
// order, then the summary with the next step.
func renderBatchNew(s *ui.Style, project, ns string, created []*runtime.Draft) {
	ui.NewHeader(s, "Draft").
		Add("Project", project).
		Add("Namespace", ns).
		Add("Batch", fmt.Sprintf("%d drafts", len(created))).
		Pipeline("New").
		Render()
	for _, d := range created {
		fmt.Fprintf(s.W, "  %s %s:%s\n", ui.IconBullet, d.Type, d.ID)
	}
	ui.NewSummary(s).
		Add("Drafts", fmt.Sprint(len(created))).
		Add("Next", "eka publish --all to persist the set").
		Render()
}

// --- eka publish --all / --pending ------------------------------------

// cliBatchNode is one pending draft in the batch publish graph.
type cliBatchNode struct {
	draft    runtime.Draft
	artifact *conformance.Artifact
	deps     map[string]bool
}

// cliBatchKey renders the deterministic identity key of one draft line:
// "<namespace>/<type>:<id>".
func cliBatchKey(ns, typeToken, id string) string {
	return ns + "/" + typeToken + ":" + id
}

// runPublishBatch publishes every pending draft of the repository's
// project in topological order (referenced drafts first). Pre-flight
// refusals — a cycle among the pending drafts, or a draft referencing a
// target that is neither pending nor published — publish nothing. The
// publish loop is per-draft atomic: a draft failing CKO-level
// validation stops the run (already-published objects stay, the
// remaining drafts stay pending). An empty backlog is informational.
func runPublishBatch(cmd *cobra.Command) error {
	r, err := openAuthoringRuntime(cmd)
	if err != nil {
		return err
	}
	defer r.Close()
	project, _, err := resolveBatchScope(r)
	if err != nil {
		return refuse(cmd, "publish: %v", err)
	}
	drafts, err := runtime.Authoring.Drafts(r, project)
	if err != nil {
		return fmt.Errorf("publish: %w", err)
	}
	s := styleFor(cmd)
	if len(drafts) == 0 {
		ui.NewHeader(s, "Draft").
			Add("Project", project).
			Add("Pending", "0").
			Pipeline("Publish").
			Render()
		ui.NewSummary(s).
			Add("Published", "0").
			Add("Next", "eka new to author, eka draft list to inspect").
			Render()
		return nil
	}

	// The pending graph: nodes are keyed by the frontmatter identity
	// (the source of truth, the same rule Publish enforces per draft).
	nodes := make(map[string]*cliBatchNode, len(drafts))
	for _, d := range drafts {
		a, serr := conformance.ScanFile(d.Path)
		if serr != nil {
			return refuse(cmd, "publish: draft %s:%s cannot be read for batch ordering: %v", d.Type, d.ID, serr)
		}
		if a == nil {
			return refuse(cmd, "publish: draft %s:%s is not a knowledge artifact (missing type/id frontmatter)", d.Type, d.ID)
		}
		key := cliBatchKey(a.Namespace, a.Type, a.ID)
		if _, dup := nodes[key]; dup {
			return refuse(cmd, "publish: drafts in project %s share the identity %s", project, key)
		}
		nodes[key] = &cliBatchNode{draft: d, artifact: a, deps: map[string]bool{}}
	}

	// Edges: a relationship target addressing a pending draft is a
	// dependency (referenced first); a target addressing nothing is a
	// pre-flight refusal unless the line already exists in the store.
	for _, node := range nodes {
		for _, field := range conformance.RelationshipFieldNames() {
			for _, raw := range node.artifact.Relations[field] {
				ref, perr := conformance.ParseReference(raw, node.artifact.Namespace, node.artifact.Type)
				if perr != nil {
					return refuse(cmd, "publish: %v", &unresolvedRefusal{
						Draft: node.artifact.Type + ":" + node.artifact.ID, Target: raw, Detail: perr.Error(),
					})
				}
				key := cliBatchKey(ref.Namespace, ref.Type, ref.ID)
				if ref.Namespace == node.artifact.Namespace && ref.Type == node.artifact.Type && ref.ID == node.artifact.ID {
					// A self-reference is a length-1 cycle: reported by
					// the ordering pass below.
					node.deps[key] = true
					continue
				}
				if _, pending := nodes[key]; pending {
					node.deps[key] = true
					continue
				}
				units, rerr := r.Resolver.ResolveLine(ref.Namespace, ref.Type, ref.ID)
				if rerr != nil {
					return refuse(cmd, "publish: cannot check reference %q of draft %s against the store: %v",
						raw, node.artifact.Type+":"+node.artifact.ID, rerr)
				}
				if len(units) == 0 {
					return refuse(cmd, "publish: %v", &unresolvedRefusal{
						Draft: node.artifact.Type + ":" + node.artifact.ID, Target: raw,
					})
				}
			}
		}
	}

	order, cycle := cliBatchOrder(nodes)
	if len(cycle) > 0 {
		return refuse(cmd, "publish: cycle among pending drafts: %s (referenced drafts must be published first)",
			strings.Join(cycle, ", "))
	}

	// Publish in topological order; per-draft atomic (spec §5.1).
	ui.NewHeader(s, "Draft").
		Add("Project", project).
		Add("Pending", fmt.Sprint(len(nodes))).
		Pipeline("Publish").
		Render()
	var published []*runtime.PublishResult
	for _, key := range order {
		node := nodes[key]
		target := node.artifact.Type + ":" + node.artifact.ID
		res, perr := runtime.Authoring.Publish(r, target, runtime.PublishOptions{Project: project})
		if perr != nil {
			var pe *runtime.PublishError
			if errors.As(perr, &pe) {
				fmt.Fprintf(s.W, "  %s %s %s\n", ui.IconBullet, s.Error(target), s.Dim("failed validation"))
				renderPublishBatchFailure(cmd, s, project, published, order, nodes, key, pe)
				return &exitError{code: exitFail}
			}
			var se *conformance.ScanError
			if errors.As(perr, &se) {
				fmt.Fprintf(s.W, "  %s %s %s\n", ui.IconBullet, s.Error(target), s.Dim("malformed"))
				renderPublishBatchFailure(cmd, s, project, published, order, nodes, key, perr)
				return &exitError{code: exitFail}
			}
			var dne *runtime.DraftNotFoundError
			if errors.As(perr, &dne) {
				fmt.Fprintf(cmd.ErrOrStderr(), "eka: %v\n", perr)
				return &exitError{code: exitFail}
			}
			return fmt.Errorf("publish: %w", perr)
		}
		published = append(published, res)
		fmt.Fprintf(s.W, "  %s %s -> %s (%s)\n",
			ui.IconBullet, target, s.Info(res.Form), res.ObjectHash[:min(len(res.ObjectHash), 12)])
	}
	ui.NewSummary(s).
		Add("Published", fmt.Sprint(len(published))).
		Add("Next", "eka get "+published[len(published)-1].Form).
		Render()
	return nil
}

// renderPublishBatchFailure completes the batch publish output after a
// refused draft: the summary (published so far + drafts still pending),
// then the deterministic failure report on stdout and the verdict line
// on stderr (the same split the single-draft publish uses).
func renderPublishBatchFailure(cmd *cobra.Command, s *ui.Style, project string, published []*runtime.PublishResult, order []string, nodes map[string]*cliBatchNode, failedKey string, err error) {
	remaining := len(order) - len(published) - 1
	if remaining < 0 {
		remaining = 0
	}
	ui.NewSummary(s).
		Add("Published", fmt.Sprint(len(published))).
		Add("Remaining", fmt.Sprint(remaining)).
		Render()
	var pe *runtime.PublishError
	if errors.As(err, &pe) {
		printCKOReport(s, failedKey, pe.Report)
		fmt.Fprintf(cmd.ErrOrStderr(), "eka: publish: draft %s failed validation; %d published, %d remain pending in project %s\n",
			failedKey, len(published), remaining, project)
		return
	}
	var se *conformance.ScanError
	if errors.As(err, &se) {
		printScanReport(s, failedKey, se)
		fmt.Fprintf(cmd.ErrOrStderr(), "eka: publish: draft %s is malformed; %d published, %d remain pending in project %s\n",
			failedKey, len(published), remaining, project)
		return
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "eka: publish: draft %s could not be published: %v\n", failedKey, err)
}

// cliBatchOrder orders the pending drafts so every draft is published
// after the drafts it references (Kahn's algorithm; the ready set is
// consumed in sorted identity order, so the order is byte-deterministic
// for a given pending set). The returned cycle list names the drafts
// that cannot be ordered (the cycle participants plus every draft that
// depends on them), sorted deterministically; it is empty on success.
func cliBatchOrder(nodes map[string]*cliBatchNode) (order []string, cycle []string) {
	indegree := make(map[string]int, len(nodes))
	consumers := make(map[string][]string, len(nodes)) // key -> dependents
	for key, node := range nodes {
		indegree[key] = len(node.deps)
		for dep := range node.deps {
			consumers[dep] = append(consumers[dep], key)
		}
	}
	for _, dependents := range consumers {
		sort.Strings(dependents)
	}

	ready := make([]string, 0, len(nodes))
	for key, n := range indegree {
		if n == 0 {
			ready = append(ready, key)
		}
	}
	sort.Strings(ready)

	order = make([]string, 0, len(nodes))
	for len(ready) > 0 {
		key := ready[0]
		ready = ready[1:]
		order = append(order, key)
		for _, dependent := range consumers[key] {
			indegree[dependent]--
			if indegree[dependent] == 0 {
				ready = append(ready, dependent)
				sort.Strings(ready)
			}
		}
	}
	if len(order) != len(nodes) {
		leftover := make([]string, 0, len(nodes)-len(order))
		for key := range nodes {
			if indegree[key] > 0 {
				leftover = append(leftover, key)
			}
		}
		sort.Strings(leftover)
		cycle = make([]string, 0, len(leftover))
		for _, key := range leftover {
			cycle = append(cycle, nodes[key].artifact.Type+":"+nodes[key].artifact.ID)
		}
		sort.Strings(cycle)
		return nil, cycle
	}
	return order, nil
}

// unresolvedRefusal renders the deterministic dangling-reference
// refusal of the batch publish gate.
type unresolvedRefusal struct {
	Draft  string
	Target string
	Detail string
}

// Error renders the deterministic refusal message.
func (e *unresolvedRefusal) Error() string {
	if e.Detail != "" {
		return fmt.Sprintf("draft %s references %q: %s", e.Draft, e.Target, e.Detail)
	}
	return fmt.Sprintf("draft %s references %q, which is neither a pending draft nor a published object", e.Draft, e.Target)
}
