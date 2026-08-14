package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/maleolabs/eka-core/exchange"
	"github.com/maleolabs/eka-core/store"
	"github.com/maleolabs/eka-core/workspace"
)

// seedIntegrityWorkspace creates a workspace with one consistent
// immutable unit and returns the store handle (for tampering).
//
// Test-only boundary (documented): this fixture crafts store state
// directly — the corruption/tamper tests must write behind the
// Runtime's back, so the test imports workspace/store by design. No
// production code in this package does.
func seedIntegrityWorkspace(t *testing.T) (*workspace.Workspace, *store.Store) {
	t.Helper()
	t.Setenv("EKA_HOME", t.TempDir())
	ws, err := workspace.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ws.Close() })
	u := &exchange.Unit{
		Identity:              exchange.Identity{Namespace: "ns", Type: "sto", ID: "x", InstanceVersion: 1},
		CanonicalIdentityForm: "ns/sto:x:1",
		Revision:              1,
		StateVector:           exchange.StateVector{ContentState: "draft", ExistenceState: "active"},
		ChangeLog:             []exchange.ChangeLogEntry{},
		Relationships:         []exchange.Relationship{},
		Classification:        exchange.Classification{},
		Content:               exchange.ContentRef{Representation: "eka/structured-text/1", File: "content"},
	}
	unitJSON, err := exchange.MarshalUnit(u)
	if err != nil {
		t.Fatal(err)
	}
	ref := store.Ref{
		Form: "ns/sto:x:1", ProjectID: "p", SourceRepo: "r",
		Namespace: "ns", Type: "sto", ID: "x", InstanceVersion: 1, Revision: 1,
		UpdatedAt: "2026-08-07T00:00:00Z",
	}
	if _, _, err := ws.Store().PutUnit(unitJSON, []byte("body"), ref); err != nil {
		t.Fatal(err)
	}
	return ws, ws.Store()
}

// TestIntegrityCheckClean: a clean workspace exits 0 with the
// deterministic report header and summary.
func TestIntegrityCheckClean(t *testing.T) {
	seedIntegrityWorkspace(t)
	code, text, errText := runIn([]string{"integrity", "check"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", code, text, errText)
	}
	for _, want := range []string{"Runtime", "Workspace", "Schema", "Payloads checked", "References checked", "Attachments checked", "History payloads", "Violations"} {
		if !strings.Contains(text, want) {
			t.Errorf("output must contain %q:\n%s", want, text)
		}
	}
	if errText != "" {
		t.Errorf("stderr must be empty on a clean check, got %q", errText)
	}
}

// TestIntegrityHelpExitsZero.
func TestIntegrityHelpExitsZero(t *testing.T) {
	for _, args := range [][]string{{"integrity", "-h"}, {"integrity", "--help"}, {"integrity", "check", "-h"}, {"integrity", "check", "--help"}} {
		code, text, _ := runIn(args)
		if code != 0 {
			t.Errorf("args %v: exit = %d, want 0", args, code)
		}
		if !strings.Contains(text, "eka integrity") {
			t.Errorf("args %v: help must mention eka integrity", args)
		}
	}
}

// TestIntegrityCheckTamperedPayload: modifying a payload row's bytes is
// detected as a payload-hash violation — exit 1 with the deterministic
// message and violation line.
func TestIntegrityCheckTamperedPayload(t *testing.T) {
	_, db := seedIntegrityWorkspace(t)
	rows, err := db.AllPayloads()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB().Exec(`UPDATE object_payloads SET content = ? WHERE object_hash = ?`, []byte("tampered"), rows[0].ObjectHash); err != nil {
		t.Fatal(err)
	}
	code, text, errText := runIn([]string{"integrity", "check"})
	if code != 1 {
		t.Fatalf("exit = %d, want 1\nstdout: %s\nstderr: %s", code, text, errText)
	}
	if !strings.Contains(errText, "eka: integrity check found 1 violation(s)") {
		t.Errorf("stderr must carry the deterministic violation message, got %q", errText)
	}
	if !strings.Contains(text, "payload-hash") || !strings.Contains(text, rows[0].ObjectHash) {
		t.Errorf("output must list the payload-hash violation:\n%s", text)
	}
}

// TestIntegrityCheckDroppedRef: a reference dropped behind the store's
// back turns its payload into retained history (counted, never a
// violation), and a forged reference pointing at a nonexistent payload
// is detected as a reference-target violation — exit 1.
func TestIntegrityCheckDroppedRef(t *testing.T) {
	_, db := seedIntegrityWorkspace(t)
	if _, err := db.DB().Exec(`DELETE FROM object_refs`); err != nil {
		t.Fatal(err)
	}
	// Forge a reference to a nonexistent payload (foreign keys off —
	// manual DB modification is detected, not prevented).
	if _, err := db.DB().Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB().Exec(`INSERT INTO object_refs (form, object_hash, project_id, source_repo, namespace, type, id, instance_version, revision, dimension, domain, phase, updated_at)
		VALUES ('ghost/sto:q:1', '` + strings.Repeat("0", 64) + `', 'p', 'r', 'ghost', 'sto', 'q', 1, 1, '', '', '', '2026-08-07T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	code, text, errText := runIn([]string{"integrity", "check"})
	if code != 1 {
		t.Fatalf("exit = %d, want 1\nstdout: %s\nstderr: %s", code, text, errText)
	}
	if !strings.Contains(errText, "eka: integrity check found 1 violation(s)") {
		t.Errorf("stderr must carry the deterministic violation message, got %q", errText)
	}
	if !strings.Contains(text, "reference-target") || !strings.Contains(text, "ghost/sto:q:1") {
		t.Errorf("output must list the reference-target violation:\n%s", text)
	}
	// The orphaned payload is history, not a violation.
	if !strings.Contains(text, "History payloads") {
		t.Errorf("output must report the history payloads:\n%s", text)
	}
}

// TestIntegrityCheckInternalError: an unusable workspace (EKA_HOME
// pointing at an existing FILE) is an internal error — exit 2, never a
// false "clean" verdict.
func TestIntegrityCheckInternalError(t *testing.T) {
	file := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EKA_HOME", file)
	code, _, errText := runIn([]string{"integrity", "check"})
	if code != 2 {
		t.Fatalf("exit = %d, want 2\nstderr: %s", code, errText)
	}
}
