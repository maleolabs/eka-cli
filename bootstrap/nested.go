package bootstrap

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"sort"
	"strings"

	"github.com/maleolabs/eka-core/exchange"
	"github.com/maleolabs/eka-core/pathglob"
)

// This file implements the path-scoped projection of the constraint index
// (M2, sto:path-scoped-projection): the nested AGENTS.md entry points an
// agent finds where it works. It reads the SAME committed snapshot the root
// block does (exchange.LoadSnapshot, never the workspace store), so laptop
// and CI derive the same projection from the same repository state.
//
// Nested-file rule (deterministic):
//   - Tracked files come from `git ls-files` (repository-relative, forward
//     slashed); the command supplies them. A generated AGENTS.md is dropped
//     from the SOURCE set (see sourceFiles) so committing the projection
//     never changes it.
//   - bindings(D) is the set of constraint lines (the latest active
//     instance per (type,id) of the Discovery + Architecture strata 1-2)
//     whose `appliesTo` scope matches at least one tracked file under D
//     (path == D or path has prefix D+"/").
//   - A directory D is a candidate when it directly contains a tracked
//     file (D == path.Dir(file)). Pure-ancestor directories are never
//     candidates, so a tree is not littered with stubs.
//   - The sealed transport tree (SnapshotDir, "exchange/snapshots") is
//     never a candidate: exchange.LoadSnapshot rejects unknown entries, so a
//     projection file written there corrupts the snapshot and breaks
//     agents-md-drift + snapshot-integrity. Its constraints still count for
//     the root block, which is untouched here.
//   - D/AGENTS.md is emitted iff bindings(D) is non-empty (AC4: an empty
//     projection writes no file). The root AGENTS.md is NOT touched here.
//   - The file carries the SAME marker-delimited managed block as the root
//     (BuildAgentsMDBlock), so the merge (MergeAgentsMD) and the drift
//     comparison are shared: same generator, same merge, byte-identical
//     regeneration.
//
// A unit with a malformed `appliesTo` is an error (reported, never
// silently ignored) — consistent with the contexts retrieval door.

// NestedEntry is one expected nested AGENTS.md entry point: the
// repository-relative directory, the constraint index bound to that
// subtree, and the managed block bytes BuildAgentsMDBlock produced.
type NestedEntry struct {
	Dir   string
	Index []IndexEntry
	Block []byte
}

// scopedConstraint is one constraint line (latest active instance of the
// strata 1-2 domains) carrying its declared `appliesTo` scope. A line with
// no scope binds no path (it stays in the root block only).
type scopedConstraint struct {
	typ      string
	id       string
	version  int
	patterns []pathglob.Pattern
}

// NestedEntriesFromSnapshot computes the nested entry points bound by the
// committed snapshot at snapshotDir for the given tracked-file set. A
// missing snapshot directory is not an error: it returns no entries and
// missing=true (the caller degrades to "no nested files"). Any other
// failure to open or verify the snapshot, or a malformed `appliesTo`, is
// an error.
func NestedEntriesFromSnapshot(snapshotDir, project, namespace string, tracked []string) (entries []NestedEntry, missing bool, err error) {
	if _, statErr := os.Stat(snapshotDir); statErr != nil {
		if os.IsNotExist(statErr) {
			return nil, true, nil
		}
		return nil, false, statErr
	}
	res, err := exchange.LoadSnapshot(snapshotDir)
	if err != nil {
		return nil, false, err
	}
	constraints, err := scopedConstraints(res.Package.Units)
	if err != nil {
		return nil, false, err
	}
	src := sourceFiles(tracked)
	dirs := nestedCandidateDirs(src, SnapshotDir)
	entries = make([]NestedEntry, 0, len(dirs))
	for _, dir := range dirs {
		index := boundIndex(constraints, src, dir)
		if IndexEntryCount(index) == 0 {
			continue // AC4: no binding → no file.
		}
		block, _ := BuildAgentsMDBlock(project, namespace, index)
		entries = append(entries, NestedEntry{Dir: dir, Index: index, Block: block})
	}
	return entries, false, nil
}

// sourceFiles drops generated AGENTS.md files from the tracked set used for
// path scoping. A generated nested file is an output, not a source: leaving
// it in the set would let a later commit of the projection feed back into
// its own bindings (a glob like "<dir>/**" matches the file). Filtering it
// keeps regeneration byte-identical across the write-then-commit boundary
// (AC1) and leaves hand-written AGENTS.md untouched.
//
// ponytail: only AGENTS.md is excluded; no other generated artifact exists
// today. Upgrade path: extend this predicate if another generated file joins
// the tracked set.
func sourceFiles(tracked []string) []string {
	out := make([]string, 0, len(tracked))
	for _, f := range tracked {
		if path.Base(f) == AgentsMDName {
			continue
		}
		out = append(out, f)
	}
	return out
}

// scopedConstraints reduces the snapshot units to the constraint lines of
// the projected domains, each carrying its latest instance's `appliesTo`
// scope, ordered by (stratum, type, id) so the derived index is stable.
func scopedConstraints(units []*exchange.Unit) ([]scopedConstraint, error) {
	type lineKey struct{ typ, id string }
	latest := map[lineKey]scopedConstraint{}
	for _, u := range units {
		if _, ok := constraintStratumOf(u.Identity.Type); !ok {
			continue
		}
		if u.StateVector.ExistenceState != "active" {
			continue
		}
		patterns, declared, err := unitAppliesTo(u)
		if err != nil {
			return nil, fmt.Errorf("%s: `appliesTo` %v", u.CanonicalIdentityForm, err)
		}
		if !declared {
			continue
		}
		k := lineKey{u.Identity.Type, u.Identity.ID}
		if prev, seen := latest[k]; seen && u.Identity.InstanceVersion <= prev.version {
			continue
		}
		latest[k] = scopedConstraint{
			typ:      u.Identity.Type,
			id:       u.Identity.ID,
			version:  u.Identity.InstanceVersion,
			patterns: patterns,
		}
	}
	out := make([]scopedConstraint, 0, len(latest))
	for _, c := range latest {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		si, sj := constraintStratum(out[i].typ), constraintStratum(out[j].typ)
		if si != sj {
			return si < sj
		}
		if out[i].typ != out[j].typ {
			return out[i].typ < out[j].typ
		}
		return out[i].id < out[j].id
	})
	return out, nil
}

// unitAppliesTo reads one unit's optional `appliesTo` declaration from its
// content payload. It reports declared=false when the payload is not a JSON
// object or carries no `appliesTo` key (absence is not a defect) — the same
// contract the contexts retrieval door applies to the store.
func unitAppliesTo(u *exchange.Unit) (patterns []pathglob.Pattern, declared bool, err error) {
	var content map[string]any
	if json.Unmarshal(u.ContentPayload, &content) != nil || content == nil {
		return nil, false, nil
	}
	raw, ok := content["appliesTo"]
	if !ok {
		return nil, false, nil
	}
	patterns, err = pathglob.Interpret(raw)
	if err != nil {
		return nil, false, err
	}
	return patterns, true, nil
}

// nestedCandidateDirs returns the sorted directories that directly contain
// a tracked file (D == path.Dir(file)), excluding the repository root and
// the sealed transport tree. A directory that only holds deeper directories
// is never a candidate.
func nestedCandidateDirs(tracked []string, sealed string) []string {
	seen := map[string]bool{}
	for _, f := range tracked {
		dir := path.Dir(f)
		if dir == "." || dir == "/" {
			continue
		}
		if dir == sealed || strings.HasPrefix(dir, sealed+"/") {
			continue // Sealed tree: never project into it (see SnapshotDir).
		}
		seen[dir] = true
	}
	dirs := make([]string, 0, len(seen))
	for d := range seen {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)
	return dirs
}

// boundIndex is the constraint index of one subtree: the lines whose scope
// matches a tracked file under dir, grouped by type exactly as the root
// index is (groupConstraintIndex), or nil when nothing binds.
func boundIndex(constraints []scopedConstraint, tracked []string, dir string) []IndexEntry {
	byType := map[string][]string{}
	for _, c := range constraints {
		if constraintBinds(c, tracked, dir) {
			byType[c.typ] = append(byType[c.typ], c.id)
		}
	}
	if len(byType) == 0 {
		return nil
	}
	return groupConstraintIndex(byType)
}

// constraintBinds reports whether any tracked file under dir matches one of
// the constraint's declared patterns.
func constraintBinds(c scopedConstraint, tracked []string, dir string) bool {
	for _, f := range tracked {
		if !underDir(f, dir) {
			continue
		}
		for _, p := range c.patterns {
			if p.Match(f) {
				return true
			}
		}
	}
	return false
}

// underDir reports whether file is dir itself or lives below it.
func underDir(file, dir string) bool {
	return file == dir || strings.HasPrefix(file, dir+"/")
}
