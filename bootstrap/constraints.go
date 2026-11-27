package bootstrap

import (
	"os"

	"github.com/maleolabs/eka-core/v2/contexts"
	"github.com/maleolabs/eka-core/v2/exchange"
)

// This file implements the constraint index the managed AGENTS.md block
// carries: the repository's own Discovery + Architecture knowledge
// (authority strata 1-2), read from the COMMITTED snapshot — never the
// workspace store, so init, `eka agents-md` and CI all derive the same
// index from the same repository state (no environment divergence).
//
// The selection/grouping is the core projection (contexts.ConstraintIndex);
// this file keeps only the CLI's snapshot IO, so the store and the snapshot
// share ONE derivation.

// SnapshotDir is the repository-relative committed-snapshot directory — the
// SEALED source-only transport tree that exchange.LoadSnapshot strictly
// validates (it rejects any unknown entry). Writing a projection file inside
// it corrupts the snapshot and breaks agents-md-drift and snapshot-integrity,
// so the nested projection never descends into it.
const SnapshotDir = "exchange/snapshots"

// ConstraintsFromSnapshot builds the bounded constraint index from the
// COMMITTED snapshot at snapshotDir by delegating to the core projection
// (contexts.ConstraintIndex): the active units of the Discovery and
// Architecture domains (authority strata 1-2), collapsed to the latest
// instance version per (type, id) line and grouped by type token, ordered by
// (stratum, token) with ids sorted — so the render order is deterministic.
//
// A missing snapshot directory is not an error: it returns a nil index
// and missing=true (the caller degrades to the identity-only block). Any
// other failure to open or verify the snapshot is an error.
func ConstraintsFromSnapshot(snapshotDir string) (index []contexts.ConstraintGroup, missing bool, err error) {
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
	return contexts.ConstraintIndex(res.Package.Units), false, nil
}
