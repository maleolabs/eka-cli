package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/maleolabs/eka-core/v2/conformance"
	"github.com/maleolabs/eka-core/v2/exchange"
	"github.com/maleolabs/eka-core/v2/store"
)

// This file holds the shared line-addressing helpers of the authoring
// command surface: the highest-instance / pending-draft resolution of a
// line, the generic JSON-draft relationships reader, and the CKO-level
// store resolver.
//
// The work-ownership commands (`eka assign`, `eka reassign`,
// `eka unassign`) were removed (ownership deprecated in place). These
// helpers survive because `eka unrelate` and `eka retire` still use
// them (line resolution, draft reads and CKO validation). Read
// tolerance for existing assigned-to / mbr data stays — the helpers
// only read.

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
