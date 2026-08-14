package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestGetMetadataFiltersCombined: the metadata filters compose — a
// domain query filtered by type, dimension AND phase in one invocation
// returns exactly the matching units.
func TestGetMetadataFiltersCombined(t *testing.T) {
	seedGetRepo(t, nil)
	doc := getDocument(t, "get", "planning", "--type", "plan", "--dimension", "planning", "--phase", "release")
	if doc["collection"] != "domain" || doc["domain"] != "Planning" {
		t.Fatalf("collection envelope = %v/%v", doc["collection"], doc["domain"])
	}
	units := doc["units"].([]any)
	if len(units) != 1 {
		t.Fatalf("combined filters must yield exactly the plan unit, got %d units", len(units))
	}
	first := units[0].(map[string]any)
	if first["canonicalForm"] != "eka-view-fixture/plan:roadmap-2026:1" {
		t.Errorf("combined filters resolved %v, want the roadmap plan", first["canonicalForm"])
	}
}

// TestGetFilterNoMatchZeroResults: a metadata filter value that matches
// nothing returns an empty collection, exit 0 — the pass-through
// contract (a typo yields zero results, never an error).
func TestGetFilterNoMatchZeroResults(t *testing.T) {
	seedGetRepo(t, nil)
	code, out, errText := runIn([]string{"get", "planning", "--phase", "nonexistent-phase"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", code, out, errText)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("stdout must be parseable JSON: %v", err)
	}
	if doc["count"].(float64) != 0 {
		t.Errorf("count = %v, want 0 for a no-match filter", doc["count"])
	}
	units, ok := doc["units"].([]any)
	if !ok || len(units) != 0 {
		t.Errorf("units = %v, want an empty array", doc["units"])
	}
}

// TestGetTimelineSingleInstance: a single-instance line (the common
// case) yields exactly one timeline entry.
func TestGetTimelineSingleInstance(t *testing.T) {
	seedGetRepo(t, nil)
	doc := getDocument(t, "get", "eka-view-fixture/sto:alpha", "--timeline")
	tl, ok := doc["timeline"].([]any)
	if !ok || len(tl) != 1 {
		t.Fatalf("timeline = %v, want exactly one entry for a single-instance line", doc["timeline"])
	}
	entry := tl[0].(map[string]any)
	if entry["canonicalForm"] != "eka-view-fixture/sto:alpha:1" || entry["instanceVersion"].(float64) != 1 {
		t.Errorf("timeline entry = %v, want sto:alpha instance 1", entry)
	}
	if _, ok := entry["changeLog"]; !ok {
		t.Errorf("timeline entry must carry its change_log, got %v", entry)
	}
}

// TestGetUpstreamDraftTolerance: relationship targets that do not
// resolve (draft tolerance at the authoring gate) are skipped by the
// traversal — a unit whose every target is unresolvable yields no
// upstream array at all.
func TestGetUpstreamDraftTolerance(t *testing.T) {
	seedGetRepo(t, func(repo string) {
		// A conformant draft artifact whose depends-on target does not
		// exist: the authoring gate tolerates it (draft), the runtime
		// traversal must skip it. fnd- owns content-state (draft is
		// valid) + existence-state and lives in research/.
		ghost := `---
namespace: eka-view-fixture
type: fnd
id: draft-ghost
instance-version: 1
revision: 1
content-state: draft
existence-state: active
dimension: research
author: Engineering Architecture
created: 2026-08-05
updated: 2026-08-05
supersedes: []
derives-from: []
depends-on:
  - sto:ghost
change-log:
  - date: 2026-08-05
    domain: existence-state
    from: "-"
    to: active
    by: Engineering Architecture
  - date: 2026-08-05
    domain: content-state
    from: "-"
    to: draft
    by: Engineering Architecture
---
# Research Finding — Draft with Ghost Dependency

## Purpose

Draft.

## Content

Draft.

## Investigation Summary

Draft.

## Conclusion

Draft.
`
		dir := filepath.Join(repo, "docs", "research")
		if err := os.WriteFile(filepath.Join(dir, "fnd-draft-ghost.md"), []byte(ghost), 0o644); err != nil {
			t.Fatal(err)
		}
	})
	doc := getDocument(t, "get", "eka-view-fixture/fnd:draft-ghost", "--upstream")
	if _, ok := doc["upstream"]; ok {
		t.Errorf("upstream must be absent when every target is unresolvable, got %v", doc["upstream"])
	}
	if doc["canonicalForm"] != "eka-view-fixture/fnd:draft-ghost:1" {
		t.Errorf("document = %v, want the ghost finding itself", doc["canonicalForm"])
	}
}

// TestGetDomainCompactGolden: --compact emits the domain collection as
// a single JSON line plus one trailing newline — the piping form —
// with identical content to the pretty form.
func TestGetDomainCompactGolden(t *testing.T) {
	seedGetRepo(t, nil)
	code, out, errText := runIn([]string{"get", "planning", "--compact"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", code, out, errText)
	}
	if strings.Count(out, "\n") != 1 || !strings.HasSuffix(out, "\n") {
		t.Errorf("compact output must be one line plus a trailing newline, got %d newlines", strings.Count(out, "\n"))
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSuffix(out, "\n")), &doc); err != nil {
		t.Fatalf("compact stdout must be a single parseable JSON document: %v", err)
	}
	if doc["count"].(float64) != 4 {
		t.Errorf("count = %v, want 4 (plan/scp/epc/trc)", doc["count"])
	}
	// Content equality with the pretty form: both parse to the same
	// document (schema, collection envelope, unit order).
	pretty := getDocument(t, "get", "planning")
	if pretty["count"].(float64) != doc["count"].(float64) {
		t.Errorf("compact and pretty must agree on the collection, got %v vs %v", doc["count"], pretty["count"])
	}
}
