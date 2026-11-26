package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/maleolabs/eka-core/conformance"
	"github.com/maleolabs/eka-core/exchange"
	"github.com/maleolabs/eka-core/store"
	"github.com/maleolabs/eka-core/view"
)

// This file holds the shared line-addressing helpers of the authoring
// command surface: the highest-instance / pending-draft resolution of a
// line, the member (mbr-) target parsing and listing used by the
// member-scoped view, the generic JSON-draft relationships reader, and
// the CKO-level store resolver.
//
// The work-ownership commands (`eka assign`, `eka reassign`,
// `eka unassign`) were removed (ownership deprecated in place). These
// helpers survive because other commands still use them: `eka view
// --member` (member resolution), `eka unrelate` and `eka retire` (line
// resolution, draft reads and CKO validation). Read tolerance for
// existing assigned-to / mbr data stays — the helpers only read.

// currentLineUnit returns the highest instance of the item line among
// the project units, or nil when the line has no published instance.
func currentLineUnit(units []*exchange.Unit, ref conformance.Reference) *exchange.Unit {
	var best *exchange.Unit
	for _, u := range units {
		if u.Identity.Namespace != ref.Namespace || u.Identity.Type != ref.Type || u.Identity.ID != ref.ID {
			continue
		}
		if best == nil || u.Identity.InstanceVersion > best.Identity.InstanceVersion {
			best = u
		}
	}
	return best
}

// pendingDraftPath resolves the pending draft file of one line:
// <workspace>/drafts/<project>/<type>-<id>.json, or the legacy .md
// form. It returns the JSON draft path ("" when none), and whether ANY
// pending draft (JSON or legacy Markdown) exists — the existence
// signal the no-op guards need.
func pendingDraftPath(wsRoot, project, typeToken, id string) (string, bool, error) {
	root := filepath.Join(wsRoot, "drafts", project)
	jsonPath := filepath.Join(root, typeToken+"-"+id+".json")
	if _, err := os.Stat(jsonPath); err == nil {
		return jsonPath, true, nil
	}
	if _, err := os.Stat(filepath.Join(root, typeToken+"-"+id+".md")); err == nil {
		return "", true, nil
	}
	return "", false, nil
}

// memberTargetRefusal is a deterministic refusal (exit 1) of the member
// (mbr-) target parsing: reason + hint, nothing was written.
type memberTargetRefusal struct {
	reason string
	hint   string
}

// Error renders the deterministic refusal message.
func (e *memberTargetRefusal) Error() string {
	return fmt.Sprintf("%s; %s", e.reason, e.hint)
}

// parseMemberTarget parses a member target into the candidate canonical
// member line form within the item's namespace. Accepted forms:
// <mbr-id>, mbr:<id>, mbr-<id>, and the qualified <ns>/mbr:<id>. A
// qualified target must name an mbr- line of the ITEM'S namespace —
// cross-namespace (cross-repository) targets are refused. The errors
// are *memberTargetRefusal: the member-scoped view renders the reason
// text as its usage class (exit 2).
func parseMemberTarget(raw, itemNS string) (string, error) {
	t := strings.TrimSpace(raw)
	if t == "" {
		return "", &memberTargetRefusal{reason: "a member target is required", hint: "pass --to <mbr-id>, mbr:<id>, mbr-<id>, or <ns>/mbr:<id>"}
	}
	if strings.Contains(t, "/") {
		ref, err := conformance.ParseReference(t, itemNS, "mbr")
		if err != nil {
			return "", &memberTargetRefusal{reason: fmt.Sprintf("invalid member target %q", t), hint: "use <mbr-id>, mbr:<id>, mbr-<id>, or <ns>/mbr:<id>"}
		}
		if ref.Type != "mbr" {
			return "", &memberTargetRefusal{reason: fmt.Sprintf("%q is not a member (mbr-) line", t), hint: "the target points to a member line"}
		}
		if ref.Namespace != itemNS {
			return "", &memberTargetRefusal{
				reason: fmt.Sprintf("member %s originates outside the work item's repository (%s); cross-repository targets are refused",
					view.LineForm(ref.Namespace, "mbr", ref.ID), itemNS),
				hint: "use only members of the same repository",
			}
		}
		return view.LineForm(ref.Namespace, "mbr", ref.ID), nil
	}
	id := strings.TrimPrefix(strings.TrimPrefix(t, "mbr:"), "mbr-")
	if id == "" {
		return "", &memberTargetRefusal{reason: fmt.Sprintf("invalid member target %q", t), hint: "use <mbr-id>, mbr:<id>, mbr-<id>, or <ns>/mbr:<id>"}
	}
	return view.LineForm(itemNS, "mbr", id), nil
}

// memberLines lists the distinct member (mbr-) lines of the project
// units, sorted by canonical identity form.
func memberLines(units []*exchange.Unit) []string {
	seen := make(map[string]bool)
	var out []string
	for _, u := range units {
		if u.Identity.Type != "mbr" {
			continue
		}
		form := view.LineForm(u.Identity.Namespace, u.Identity.Type, u.Identity.ID)
		if seen[form] {
			continue
		}
		seen[form] = true
		out = append(out, form)
	}
	sort.Strings(out)
	return out
}

// memberLinesInNS lists the distinct member lines of one namespace —
// the known members of the item's repository.
func memberLinesInNS(units []*exchange.Unit, ns string) []string {
	var out []string
	for _, form := range memberLines(units) {
		if strings.HasPrefix(form, ns+"/mbr:") {
			out = append(out, form)
		}
	}
	return out
}

// memberLineExists reports whether the canonical member form is a known
// member line of the project.
func memberLineExists(units []*exchange.Unit, form string) bool {
	for _, u := range units {
		if u.Identity.Type == "mbr" && view.LineForm(u.Identity.Namespace, u.Identity.Type, u.Identity.ID) == form {
			return true
		}
	}
	return false
}

// draftRelationshipsOf extracts the edge set of a generic JSON draft
// document's relationships block (camelCase field names -> targets),
// in canonical field order.
func draftRelationshipsOf(doc map[string]any) []exchange.Relationship {
	var out []exchange.Relationship
	raw, ok := doc["relationships"].(map[string]any)
	if !ok {
		return out
	}
	for _, field := range conformance.RelationshipFieldNames() {
		targets, ok := raw[conformance.StateKeyCamel(field)].([]any)
		if !ok {
			continue
		}
		for _, t := range targets {
			if s, ok := t.(string); ok {
				out = append(out, exchange.Relationship{Type: field, Target: s})
			}
		}
	}
	return out
}

// assignmentStoreResolver is the CKO-level relationship resolution
// callback over the canonical store — the mirror of the runtime's
// storeResolver (runtime/draft.go): a line-level reference resolves
// when the line has instances, a versioned reference only when the
// exact instance exists. Store failures resolve as "unresolved" (the
// conservative answer) but are never silent: every failed lookup is
// recorded and surfaced by Findings.
type assignmentStoreResolver struct {
	st   *store.Store
	errs map[string]error // referenced line key -> the first store error
}

// Resolve implements the conformance resolution callback.
func (r *assignmentStoreResolver) Resolve(ref conformance.Reference) bool {
	units, err := r.st.UnitsByLine(ref.Namespace, ref.Type, ref.ID)
	if err != nil {
		key := ref.Namespace + "/" + ref.Type + ":" + ref.ID
		if r.errs == nil {
			r.errs = make(map[string]error)
		}
		if _, seen := r.errs[key]; !seen {
			r.errs[key] = err
		}
		return false
	}
	if !ref.HasVersion {
		return len(units) > 0
	}
	for _, u := range units {
		if u.Identity.InstanceVersion == ref.Version {
			return true
		}
	}
	return false
}

// Findings converts the recorded store failures into report results in
// deterministic order (by referenced line), mirroring the runtime's
// Findings: a failed existence check is a warning while content-state
// is draft, an error otherwise (Rule 5's draft tolerance).
func (r *assignmentStoreResolver) Findings(file, contentState string) []conformance.Result {
	if len(r.errs) == 0 {
		return nil
	}
	keys := make([]string, 0, len(r.errs))
	for k := range r.errs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]conformance.Result, 0, len(keys))
	for _, k := range keys {
		sev := conformance.SeverityError
		if contentState == "draft" {
			sev = conformance.SeverityWarning
		}
		out = append(out, conformance.Result{
			File:     file,
			Rule:     conformance.Rule5,
			Severity: sev,
			Message:  fmt.Sprintf("reference %s could not be checked against the store: %v", k, r.errs[k]),
		})
	}
	return out
}
