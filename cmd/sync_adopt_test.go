package cmd

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/maleolabs/eka-core/conformance"
	"github.com/maleolabs/eka-core/exchange"
	"github.com/maleolabs/eka-core/store"
	"github.com/maleolabs/eka-core/workspace"
)

// This file tests the `eka sync adopt` command and the `eka sync push
// --adopt` flag (ADR-032 Option C2): the re-attribution of
// workspace-native units (source_repo = "runtime") to the repository
// provenance, so the next push carries them into the snapshot.

// seedCLIRuntimeUnit persists one workspace-native unit (source_repo =
// "runtime") into the project of the current test's EKA_HOME and
// returns its canonical form.
func seedCLIRuntimeUnit(t *testing.T, projectID, ns, typeToken, id string, version int, content string) string {
	t.Helper()
	ws, err := workspace.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ws.Close() })
	u := &exchange.Unit{
		Identity: exchange.Identity{Namespace: ns, Type: typeToken, ID: id, InstanceVersion: version},
		Revision: 1,
		StateVector: exchange.StateVector{
			ExecutionState: "planned",
			ExistenceState: "active",
		},
		ChangeLog: []exchange.ChangeLogEntry{
			{Date: "2026-08-07", Domain: "execution-state", From: "-", To: "planned", By: conformance.User("Engineering")},
			{Date: "2026-08-07", Domain: "existence-state", From: "-", To: "active", By: conformance.User("Engineering")},
		},
		Relationships:  []exchange.Relationship{},
		Classification: exchange.Classification{},
		Content:        exchange.ContentRef{Representation: exchange.ContentRepresentation, File: "content"},
		ContentPayload: []byte(content),
	}
	u.CanonicalIdentityForm = u.Identity.CanonicalForm()
	unitJSON, err := exchange.MarshalUnit(u)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := ws.Store().PutUnit(unitJSON, u.ContentPayload, store.Ref{
		Form:            u.CanonicalIdentityForm,
		ProjectID:       projectID,
		SourceRepo:      workspace.ReservedRepoName,
		Namespace:       ns,
		Type:            typeToken,
		ID:              id,
		InstanceVersion: version,
		Revision:        u.Revision,
		UpdatedAt:       "2026-08-07T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	return u.CanonicalIdentityForm
}

// adoptCLIEnv builds the adopt CLI environment: a synced fixture
// repository (registered, 4 repo-attributed units) plus one
// workspace-native unit in the same project. It returns the repo path.
func adoptCLIEnv(t *testing.T) string {
	t.Helper()
	t.Setenv("EKA_HOME", t.TempDir())
	repo := copySyncFixture(t)
	if code, _, errText := runIn([]string{"sync", repo}); code != 0 {
		t.Fatalf("settling sync: exit %d\n%s", code, errText)
	}
	seedCLIRuntimeUnit(t, "eka-sync-fixture", "eka-sync-fixture", "sto", "runtime-only", 1, "# runtime-only\n\n## Description\n\nadopt me\n\n## Acceptance Criteria\n\nac\n")
	return repo
}

// TestSyncAdoptHelpExitsZero: the adopt help exits 0 and documents the
// ADR-032 model.
func TestSyncAdoptHelpExitsZero(t *testing.T) {
	code, text, _ := runIn([]string{"sync", "adopt", "-h"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	for _, want := range []string{"eka sync adopt", "ADR-032", "workspace-native", "source_repo = \"runtime\"", "--dry-run", "target"} {
		if !strings.Contains(text, want) {
			t.Errorf("adopt help must document %q:\n%s", want, text)
		}
	}
}

// TestSyncPushHelpDocumentsAdopt: the push help documents the --adopt
// flag.
func TestSyncPushHelpDocumentsAdopt(t *testing.T) {
	code, text, _ := runIn([]string{"sync", "push", "-h"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	for _, want := range []string{"--adopt", "ADR-032", "workspace-native"} {
		if !strings.Contains(text, want) {
			t.Errorf("push help must document %q:\n%s", want, text)
		}
	}
}

// TestSyncAdoptDryRun: --dry-run reports the adoptable count without
// changing the store — the workspace-native reference stays.
func TestSyncAdoptDryRun(t *testing.T) {
	repo := adoptCLIEnv(t)

	code, text, errText := runIn([]string{"sync", "adopt", repo, "--dry-run"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", code, text, errText)
	}
	for _, want := range []string{"Adopt", "dry run (no changes)", "1 unit"} {
		if !strings.Contains(text, want) {
			t.Errorf("output must contain %q:\n%s", want, text)
		}
	}
	ws := mustWorkspace(t)
	t.Cleanup(func() { ws.Close() })
	refs, err := ws.Store().Refs("eka-sync-fixture", workspace.ReservedRepoName)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 1 {
		t.Errorf("workspace-native refs after dry run = %d, want 1 (no mutation)", len(refs))
	}
}

// TestSyncAdoptChangesProvenance: `eka sync adopt` re-attributes the
// workspace-native unit to the repository provenance — the store then
// resolves it under the repository.
func TestSyncAdoptChangesProvenance(t *testing.T) {
	repo := adoptCLIEnv(t)

	code, text, errText := runIn([]string{"sync", "adopt", repo})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", code, text, errText)
	}
	for _, want := range []string{"Adopt", "adopted", "1 unit"} {
		if !strings.Contains(text, want) {
			t.Errorf("output must contain %q:\n%s", want, text)
		}
	}
	ws := mustWorkspace(t)
	t.Cleanup(func() { ws.Close() })
	runtimeRefs, err := ws.Store().Refs("eka-sync-fixture", workspace.ReservedRepoName)
	if err != nil {
		t.Fatal(err)
	}
	if len(runtimeRefs) != 0 {
		t.Errorf("workspace-native refs after adopt = %d, want 0", len(runtimeRefs))
	}
	ref, ok, err := ws.Store().Ref("eka-sync-fixture/sto:runtime-only:1")
	if err != nil || !ok {
		t.Fatalf("adopted reference missing: ok = %v, err = %v", ok, err)
	}
	if ref.SourceRepo != "eka-sync-fixture" {
		t.Errorf("adopted reference source repo = %q, want eka-sync-fixture", ref.SourceRepo)
	}
}

// TestSyncAdoptTarget: a target adopts only the matching
// workspace-native unit.
func TestSyncAdoptTarget(t *testing.T) {
	repo := adoptCLIEnv(t)
	seedCLIRuntimeUnit(t, "eka-sync-fixture", "eka-sync-fixture", "sto", "runtime-second", 1, "# runtime-second\n\n## Description\n\nadopt me too\n\n## Acceptance Criteria\n\nac\n")

	code, text, errText := runIn([]string{"sync", "adopt", repo, "eka-sync-fixture/sto:runtime-only"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", code, text, errText)
	}
	if !strings.Contains(text, "1 unit") {
		t.Errorf("targeted adopt must report 1 unit:\n%s", text)
	}
	ws := mustWorkspace(t)
	t.Cleanup(func() { ws.Close() })
	runtimeRefs, err := ws.Store().Refs("eka-sync-fixture", workspace.ReservedRepoName)
	if err != nil {
		t.Fatal(err)
	}
	if len(runtimeRefs) != 1 || runtimeRefs[0].Form != "eka-sync-fixture/sto:runtime-second:1" {
		t.Errorf("workspace-native refs after targeted adopt = %+v, want only the second unit", runtimeRefs)
	}
}

// TestSyncAdoptRefusalsExitTwo: invalid targets, missing units and
// namespace mismatches are refused with exit 2 and the deterministic
// sentences.
func TestSyncAdoptRefusalsExitTwo(t *testing.T) {
	repo := adoptCLIEnv(t)

	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"sync", "adopt", repo, "no-type-token"}, "invalid target"},
		{[]string{"sync", "adopt", repo, "eka-sync-fixture/sto:missing"}, "no workspace-native unit"},
		{[]string{"sync", "adopt", repo, "other-ns/sto:runtime-only"}, "differs from the repository namespace"},
	} {
		code, _, errText := runIn(tc.args)
		if code != 2 {
			t.Errorf("%v: exit = %d, want 2\nstderr: %s", tc.args, code, errText)
		}
		if !strings.Contains(errText, "eka: sync adopt refused:") || !strings.Contains(errText, tc.want) {
			t.Errorf("%v: stderr must carry the deterministic refusal, got %q", tc.args, errText)
		}
	}
	// The refused runs wrote nothing.
	ws := mustWorkspace(t)
	t.Cleanup(func() { ws.Close() })
	refs, err := ws.Store().Refs("eka-sync-fixture", workspace.ReservedRepoName)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 1 {
		t.Errorf("workspace-native refs after refusals = %d, want 1 (no mutation)", len(refs))
	}
}

// TestSyncAdoptRefusesWithoutEKA (ADR-018): adopt on a directory whose
// walk-up finds no eka.yaml is refused with the pinned sentence, exit
// 2.
func TestSyncAdoptRefusesWithoutEKA(t *testing.T) {
	t.Setenv("EKA_HOME", t.TempDir())
	dir := t.TempDir()

	code, _, errText := runIn([]string{"sync", "adopt", dir})
	if code != 2 {
		t.Fatalf("exit = %d, want 2\nstderr: %s", code, errText)
	}
	if !strings.Contains(errText, "eka: sync adopt refused:") ||
		!strings.Contains(errText, "is not an EKA repository (no eka.yaml)") ||
		!strings.Contains(errText, "run 'eka init' first") {
		t.Errorf("stderr must carry the pinned ADR-018 refusal, got %q", errText)
	}
}

// TestSyncPushAdoptIncludesRuntimeUnits: a plain push excludes the
// workspace-native unit (snapshot isolation); `push --adopt` adopts it
// first and the snapshot then carries it.
func TestSyncPushAdoptIncludesRuntimeUnits(t *testing.T) {
	repo := adoptCLIEnv(t)

	// Plain push: the workspace-native unit stays out of the snapshot.
	if code, _, errText := runIn([]string{"sync", "push", repo}); code != 0 {
		t.Fatalf("plain push: exit %d\n%s", code, errText)
	}
	res, err := exchange.LoadSnapshot(filepath.Join(repo, "exchange", "snapshots"))
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range res.Package.Units {
		if strings.Contains(u.CanonicalIdentityForm, "runtime-only") {
			t.Error("a plain push must not carry the workspace-native unit")
		}
	}

	// push --adopt: the unit is adopted before the push and enters the
	// snapshot.
	code, text, errText := runIn([]string{"sync", "push", repo, "--adopt"})
	if code != 0 {
		t.Fatalf("push --adopt: exit = %d\nstdout: %s\nstderr: %s", code, text, errText)
	}
	if !strings.Contains(text, "adopted before push: 1 unit") {
		t.Errorf("push --adopt output must report the adopted unit:\n%s", text)
	}
	res, err = exchange.LoadSnapshot(filepath.Join(repo, "exchange", "snapshots"))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, u := range res.Package.Units {
		if u.CanonicalIdentityForm == "eka-sync-fixture/sto:runtime-only:1" {
			found = true
		}
	}
	if !found {
		t.Error("the snapshot after push --adopt must carry the adopted unit")
	}
	// The workspace-native provenance is empty afterwards.
	ws := mustWorkspace(t)
	t.Cleanup(func() { ws.Close() })
	refs, err := ws.Store().Refs("eka-sync-fixture", workspace.ReservedRepoName)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 0 {
		t.Errorf("workspace-native refs after push --adopt = %d, want 0", len(refs))
	}
}

// TestSyncAdoptOutputDeterministic: two identical adopt runs produce
// byte-identical output.
func TestSyncAdoptOutputDeterministic(t *testing.T) {
	repo := adoptCLIEnv(t)
	run := func() string {
		_, text, _ := runIn([]string{"sync", "adopt", repo, "--dry-run"})
		return text
	}
	first := run()
	second := run()
	if first != second {
		t.Error("adopt output differs between identical runs")
	}
}

// TestSyncAdoptNoRuntimeUnits: a repository with no workspace-native
// units adopts zero units and exits 0.
func TestSyncAdoptNoRuntimeUnits(t *testing.T) {
	t.Setenv("EKA_HOME", t.TempDir())
	repo := copySyncFixture(t)
	if code, _, errText := runIn([]string{"sync", repo}); code != 0 {
		t.Fatalf("settling sync: exit %d\n%s", code, errText)
	}

	code, text, errText := runIn([]string{"sync", "adopt", repo})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", code, text, errText)
	}
	if !strings.Contains(text, "no workspace-native units") {
		t.Errorf("output must report no workspace-native units:\n%s", text)
	}
	if !strings.Contains(text, "0 units") {
		t.Errorf("output must report 0 units:\n%s", text)
	}
}

// TestSyncAdoptFromSubdirectory (ADR-018): an adopt run from a
// subdirectory adopts the walk-up repository root.
func TestSyncAdoptFromSubdirectory(t *testing.T) {
	repo := adoptCLIEnv(t)
	subdir := filepath.Join(repo, "docs")

	code, _, errText := runIn([]string{"sync", "adopt", subdir})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstderr: %s", code, errText)
	}
	ws := mustWorkspace(t)
	t.Cleanup(func() { ws.Close() })
	ref, ok, err := ws.Store().Ref("eka-sync-fixture/sto:runtime-only:1")
	if err != nil || !ok {
		t.Fatalf("adopted reference missing: ok = %v, err = %v", ok, err)
	}
	if ref.SourceRepo != "eka-sync-fixture" {
		t.Errorf("adopted reference source repo = %q, want eka-sync-fixture", ref.SourceRepo)
	}
}
