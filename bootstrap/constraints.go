package bootstrap

import (
	"os"
	"sort"

	"github.com/maleolabs/eka-core/conformance"
	"github.com/maleolabs/eka-core/exchange"
)

// This file implements the constraint index the managed AGENTS.md block
// carries: the repository's own Discovery + Architecture knowledge
// (authority strata 1-2), read from the COMMITTED snapshot — never the
// workspace store, so init, `eka agents-md` and CI all derive the same
// index from the same repository state (no environment divergence).

// ConstraintsFromSnapshot builds the bounded constraint index from the
// COMMITTED snapshot at snapshotDir. It selects the active (existence
// state "active") units of the Discovery and Architecture domains
// (authority strata 1-2, via conformance.Stratum — never hardcoded),
// collapses each (type, id) line to its latest instance version, then
// groups the ids by type token. Types are ordered by (stratum, token)
// and ids are sorted within a type, so the render order is
// deterministic.
//
// A missing snapshot directory is not an error: it returns a nil index
// and missing=true (the caller degrades to the identity-only block). Any
// other failure to open or verify the snapshot is an error.
func ConstraintsFromSnapshot(snapshotDir string) (index []IndexEntry, missing bool, err error) {
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
	type lineKey struct{ typ, id string }
	latest := map[lineKey]int{}
	for _, u := range res.Package.Units {
		if _, ok := constraintStratumOf(u.Identity.Type); !ok {
			continue
		}
		if u.StateVector.ExistenceState != "active" {
			continue
		}
		k := lineKey{u.Identity.Type, u.Identity.ID}
		if v, seen := latest[k]; !seen || u.Identity.InstanceVersion > v {
			latest[k] = u.Identity.InstanceVersion
		}
	}
	byType := make(map[string][]string, len(latest))
	for k := range latest {
		byType[k.typ] = append(byType[k.typ], k.id)
	}
	return groupConstraintIndex(byType), false, nil
}

// groupConstraintIndex turns a type→ids map into the render-ordered index:
// types by (stratum, token), ids sorted within a type. It is the ONE
// grouping the root index and every nested subtree index share.
func groupConstraintIndex(byType map[string][]string) []IndexEntry {
	types := make([]string, 0, len(byType))
	for t := range byType {
		types = append(types, t)
	}
	sort.Slice(types, func(i, j int) bool {
		si, sj := constraintStratum(types[i]), constraintStratum(types[j])
		if si != sj {
			return si < sj
		}
		return types[i] < types[j]
	})
	index := make([]IndexEntry, 0, len(types))
	for _, t := range types {
		ids := byType[t]
		sort.Strings(ids)
		index = append(index, IndexEntry{Type: t, IDs: ids})
	}
	return index
}

// constraintStratumOf resolves the authority stratum of an artifact type
// token and reports whether it is in the projected range (strata 1-2,
// Discovery + Architecture). Unknown tokens are not constraints.
func constraintStratumOf(token string) (int, bool) {
	d, ok := conformance.DomainForToken(token)
	if !ok {
		return 0, false
	}
	s := conformance.Stratum(d)
	if s != 1 && s != 2 {
		return 0, false
	}
	return s, true
}

// constraintStratum resolves the authority stratum of an artifact type
// token via the shared conformance mapping (0 for an unknown token).
func constraintStratum(token string) int {
	d, _ := conformance.DomainForToken(token)
	return conformance.Stratum(d)
}
