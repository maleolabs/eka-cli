package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/maleolabs/eka-core/exchange"
)

// TestSnapshotRepairRegenerates: after a Git merge combined two
// snapshots (the aggregates carry the pre-merge bytes, the units/ tree
// is the union), repair regenerates the aggregates from the merged
// units/ tree — the snapshot verifies byte-exact afterwards, exactly
// as the next `eka sync` pull will verify it.
func TestSnapshotRepairRegenerates(t *testing.T) {
	t.Setenv("EKA_HOME", t.TempDir())
	repo := copySyncFixture(t)
	if code, _, errText := runIn([]string{"sync", repo}); code != 0 {
		t.Fatalf("sync: exit %d\n%s", code, errText)
	}
	t.Chdir(repo)

	snapshotDir := filepath.Join(repo, "exchange", "snapshots")
	// The post-merge state: aggregates corrupted (conflict markers or
	// stale ours bytes), units/ untouched.
	for _, name := range []string{"manifest.json", "integrity.json", "declarations.json"} {
		if err := os.WriteFile(filepath.Join(snapshotDir, name), []byte("<<<<<<< ours\nstale"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	code, text, errText := runIn([]string{"snapshot", "repair"})
	if code != 0 {
		t.Fatalf("repair: exit = %d, want 0\nstderr: %s", code, errText)
	}
	if !strings.Contains(text, "aggregates regenerated") {
		t.Errorf("repair must report the regeneration: %q", text)
	}
	if _, _, err := exchange.LoadPackageWithEntries(snapshotDir); err != nil {
		t.Errorf("repaired snapshot refused: %v", err)
	}
}

// TestSnapshotRepairRefusesOutsideRepository: without eka.yaml above
// the current directory there is nothing to repair — exit 1.
func TestSnapshotRepairRefusesOutsideRepository(t *testing.T) {
	t.Chdir(t.TempDir())
	code, _, errText := runIn([]string{"snapshot", "repair"})
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(errText, "not an EKA repository") {
		t.Errorf("stderr must explain the refusal: %q", errText)
	}
}

// TestSnapshotRepairRefusesCorruptUnits: an undecodable unit in the
// working tree (an unresolved unit-level conflict — the unit file
// itself carries conflict markers) refuses the regeneration — exit 1,
// nothing written.
func TestSnapshotRepairRefusesCorruptUnits(t *testing.T) {
	t.Setenv("EKA_HOME", t.TempDir())
	repo := copySyncFixture(t)
	if code, _, errText := runIn([]string{"sync", repo}); code != 0 {
		t.Fatalf("sync: exit %d\n%s", code, errText)
	}
	t.Chdir(repo)

	snapshotDir := filepath.Join(repo, "exchange", "snapshots")
	var unitJSON string
	if err := filepath.WalkDir(filepath.Join(snapshotDir, "units"),
		func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() && d.Name() == "unit.json" && unitJSON == "" {
				unitJSON = path
			}
			return nil
		}); err != nil {
		t.Fatal(err)
	}
	if unitJSON == "" {
		t.Fatal("fixture snapshot must carry unit.json entries")
	}
	if err := os.WriteFile(unitJSON, []byte("<<<<<<< ours\n{broken"), 0o644); err != nil {
		t.Fatal(err)
	}
	unitBefore, err := os.ReadFile(unitJSON)
	if err != nil {
		t.Fatal(err)
	}

	code, _, errText := runIn([]string{"snapshot", "repair"})
	if code != 1 {
		t.Fatalf("exit = %d, want 1 (not resolvable)", code)
	}
	if !strings.Contains(errText, "resolve unit-level conflicts first") {
		t.Errorf("stderr must point at the unit-level conflict: %q", errText)
	}
	unitAfter, err := os.ReadFile(unitJSON)
	if err != nil {
		t.Fatal(err)
	}
	if string(unitBefore) != string(unitAfter) {
		t.Error("no writes are allowed when a unit cannot be decoded")
	}
}

// TestSnapshotRepairHelp: the command documents the post-merge
// workflow and the keep-ours driver declaration.
func TestSnapshotRepairHelp(t *testing.T) {
	code, text, _ := runIn([]string{"snapshot", "repair", "-h"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	for _, want := range []string{"merge.ekasnap.driver", ".gitattributes", "regenerate", "eka sync"} {
		if !strings.Contains(text, want) {
			t.Errorf("repair help must document %q", want)
		}
	}
}
