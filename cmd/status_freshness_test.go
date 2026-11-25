package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// This file tests the actionable half of the validity horizon: `status
// --stale` and `status --fail-on-stale`. The fixture is the view "valid"
// repository seeded through seedGetRepo with three extra ADRs whose
// horizons bracket today (past / today / future) plus one that declares
// none — the map case a horizon gate must get right, since the failures
// that matter are "lists the wrong items" and "silently exits 0".

// isoDay renders an offset in days from today as an absolute YYYY-MM-DD
// horizon — the only date form the envelope accepts.
func isoDay(days int) string {
	return time.Now().AddDate(0, 0, days).Format("2006-01-02")
}

// writeHorizonADR writes a conformant ADR into the repo copy with an
// optional validity horizon ("" omits the field entirely, the undeclared
// case).
func writeHorizonADR(t *testing.T, repo, id, validUntil string) {
	t.Helper()
	horizon := ""
	if validUntil != "" {
		horizon = "validUntil: " + validUntil + "\n"
	}
	content := "---\nnamespace: eka-view-fixture\ntype: adr\nid: " + id + "\n" +
		"instance-version: 1\nrevision: 1\ncontent-state: accepted\nexistence-state: active\n" +
		"dimension: decisions\nauthor: Engineering Architecture\ncreated: 2026-08-05\nupdated: 2026-08-05\n" +
		horizon +
		"supersedes: []\nderives-from: []\ndepends-on: []\n" +
		"change-log:\n" +
		"  - date: 2026-08-05\n    domain: existence-state\n    from: \"-\"\n    to: active\n    by: Engineering Architecture\n" +
		"  - date: 2026-08-05\n    domain: content-state\n    from: proposed\n    to: accepted\n    by: Engineering Architecture\n" +
		"---\n# ADR — " + id + "\n\n## Context\n\nc\n\n## Decision\n\nd\n\n## Consequences\n\nc\n\n## Alternatives Considered\n\na\n"
	path := filepath.Join(repo, "docs", "decisions", "adr-horizon-"+id+".md")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// seedHorizonRepo seeds a workspace with the horizons the tests reason
// about: an optional overdue item at pastDays (0 omits it), an optional
// item expiring today (today), an optional future item at futureDays (0
// omits it), and always one undeclared declaration.
func seedHorizonRepo(t *testing.T, pastDays, futureDays int, today bool) {
	t.Helper()
	seedGetRepo(t, func(repo string) {
		if pastDays < 0 {
			writeHorizonADR(t, repo, "horizon-past", isoDay(pastDays))
		}
		if today {
			writeHorizonADR(t, repo, "horizon-today", isoDay(0))
		}
		if futureDays > 0 {
			writeHorizonADR(t, repo, "horizon-future", isoDay(futureDays))
		}
		writeHorizonADR(t, repo, "horizon-undeclared", "")
	})
}

// boundedLine extracts the advisory count line, so the tests can assert
// the listing flags never move it.
func boundedLine(t *testing.T, args ...string) string {
	t.Helper()
	code, out, errText := runIn(args)
	if code != 0 {
		t.Fatalf("args %v: exit = %d, want 0\nstdout: %s\nstderr: %s", args, code, out, errText)
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "Stale knowledge:") {
			return line
		}
	}
	t.Fatalf("args %v: no bounded-count line in:\n%s", args, out)
	return ""
}

// TestStatusStaleListsOnlyExpiredHorizons: the listing contains exactly the
// items whose horizon is in the past — the overdue one — and never the one
// expiring today, the future one, or the undeclared declaration. Today is
// pinned deliberately: "valid until <date>" means valid through that date, so
// an inclusive boundary here would make the listing contradict the
// bounded-count line printed right next to it. The wire form is asserted too,
// because that is what a script consumes.
func TestStatusStaleListsOnlyExpiredHorizons(t *testing.T) {
	seedHorizonRepo(t, -40, 30, true)

	code, out, errText := runIn([]string{"status", "--stale"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", code, out, errText)
	}
	if errText != "" {
		t.Errorf("stderr must be empty, got %q", errText)
	}
	for _, want := range []string{
		"eka-view-fixture/adr:horizon-past:1",
		"Architecture", // the Engineering Domain travels with the row
		"40 days overdue",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("listing missing %q, got:\n%s", want, out)
		}
	}
	for _, unwanted := range []string{
		"eka-view-fixture/adr:horizon-today:1",
		"eka-view-fixture/adr:horizon-future:1",
		"eka-view-fixture/adr:horizon-undeclared:1",
	} {
		if strings.Contains(out, unwanted) {
			t.Errorf("listing must not name %q:\n%s", unwanted, out)
		}
	}

	doc := getDocument(t, "status", "--stale", "--json")
	rows, ok := doc["stale"].([]any)
	if !ok {
		t.Fatalf("--stale --json must carry the stale array, got %v", doc["stale"])
	}
	if len(rows) != 1 {
		t.Fatalf("stale rows = %d, want 1 (the overdue item): %v", len(rows), rows)
	}
	row := rows[0].(map[string]any)
	if row["canonicalForm"] != "eka-view-fixture/adr:horizon-past:1" {
		t.Errorf("row = %v, want the overdue item", row["canonicalForm"])
	}
	if row["daysOverdue"] != float64(40) {
		t.Errorf("daysOverdue = %v, want 40", row["daysOverdue"])
	}

	// The listing and the advisory tally must name the same set: the horizon
	// day is current, so it is counted neither here nor there.
	plain, flagged := boundedLine(t, "status"), boundedLine(t, "status", "--stale")
	if plain != flagged {
		t.Errorf("--stale changed the bounded-count line: %q vs %q", plain, flagged)
	}
	if !strings.Contains(plain, "Stale knowledge: 1 ") {
		t.Errorf("bounded-count line = %q, want 1 stale item", plain)
	}
}

// TestStatusFailOnStaleExitsOne: the CI gate fires with the reason on
// stderr and exit 1 when anything is overdue.
func TestStatusFailOnStaleExitsOne(t *testing.T) {
	seedHorizonRepo(t, -1, 30, true)

	code, _, errText := runIn([]string{"status", "--fail-on-stale"})
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(errText, "past their validity horizon") {
		t.Errorf("stderr must name the reason, got %q", errText)
	}
	if !strings.Contains(errText, "--fail-on-stale") {
		t.Errorf("stderr must name the flag so CI logs are self-explaining, got %q", errText)
	}

	// The wire format must not exit green: a script consuming --json would
	// otherwise read a lapsed corpus as a passing gate.
	code, _, errText = runIn([]string{"status", "--fail-on-stale", "--json"})
	if code != 1 {
		t.Fatalf("status --fail-on-stale --json: exit = %d, want 1\nstderr: %s", code, errText)
	}
	if !strings.Contains(errText, "past their validity horizon") {
		t.Errorf("--json path must still print the reason, got %q", errText)
	}
}

// TestStatusFailOnStaleCleanExitsZero: nothing past its horizon (only a
// future declaration and an undeclared one) is a green gate — the
// undeclared item must not be able to fail CI, and must not be listed.
func TestStatusFailOnStaleCleanExitsZero(t *testing.T) {
	seedHorizonRepo(t, 0, 30, false)

	code, out, errText := runIn([]string{"status", "--fail-on-stale"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", code, out, errText)
	}
	if errText != "" {
		t.Errorf("stderr must be empty, got %q", errText)
	}
	if strings.Contains(out, "horizon-undeclared") {
		t.Errorf("an undeclared horizon is never stale:\n%s", out)
	}
	code, out, _ = runIn([]string{"status", "--stale"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s", code, out)
	}
	if !strings.Contains(out, "none past its validity horizon") {
		t.Errorf("empty listing must be stated, got:\n%s", out)
	}
}
