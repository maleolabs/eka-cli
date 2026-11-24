package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// This file tests the path-to-constraint mode of `eka context --for`: the
// retrieval door returning the units whose declared `appliesTo` scope binds
// a repository-relative path (schema "eka-constraints-v1"). The fixture
// reuses the get/view repo (seedGetRepo) and adds three JSON-native units
// carrying `appliesTo` declarations across two authority strata; the
// retrieval ordering (stratum ascending, then canonical form), the empty
// set and the path normalization are pinned here.

// contextForDoc builds a minimal conformant JSON-native artifact declaring
// the given `appliesTo` patterns, for the fixture's repo namespace. The
// knowledge dimension (R6) is required on JSON knowledge artifacts. Modeled
// on the fixture's own JSON unit (docs/operating/notes/cmt-*.json).
func contextForDoc(typeToken, id, dimension string, patterns ...string) string {
	applies := make([]any, len(patterns))
	for i, p := range patterns {
		applies[i] = p
	}
	doc := map[string]any{
		"namespace":       "eka-view-fixture",
		"type":            typeToken,
		"id":              id,
		"instanceVersion": 1,
		"revision":        1,
		"author":          "Engineering Architecture",
		"created":         "2026-08-05",
		"updated":         "2026-08-05",
		"dimension":       dimension,
		"state":           map[string]any{"contentState": "draft", "existenceState": "active"},
		"changeLog": []any{
			map[string]any{"date": "2026-08-05", "domain": "contentState", "from": "-", "to": "draft", "by": "Engineering Architecture"},
			map[string]any{"date": "2026-08-05", "domain": "existenceState", "from": "-", "to": "active", "by": "Engineering Architecture"},
		},
		"content": map[string]any{"purpose": "p", "content": "c", "appliesTo": applies},
	}
	b, err := json.Marshal(doc)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// seedContextForRepo seeds the get/view repo with three applying units
// across two authority strata: req-aaa (Discovery, stratum 1) and std-aaa /
// std-zzz (Architecture, stratum 2), all binding src/**. std-zzz also binds
// docs/*.md.
func seedContextForRepo(t *testing.T) string {
	t.Helper()
	return seedGetRepo(t, func(repo string) {
		writeContextForFile(t, repo, "docs/requirements/req-aaa.json",
			contextForDoc("req", "aaa", "requirements", "src/**"))
		writeContextForFile(t, repo, "docs/standards/std-aaa.json",
			contextForDoc("std", "aaa", "standards", "src/**"))
		writeContextForFile(t, repo, "docs/standards/std-zzz.json",
			contextForDoc("std", "zzz", "standards", "src/**", "docs/*.md"))
	})
}

// writeContextForFile writes one file into the seeded repository tree.
func writeContextForFile(t *testing.T, repo, rel, content string) {
	t.Helper()
	full := filepath.Join(repo, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// constraintsDoc is the parse target of the --for machine document.
type constraintsDoc struct {
	Schema      string `json:"schema"`
	Path        string `json:"path"`
	Constraints []struct {
		CanonicalForm string `json:"canonicalForm"`
		Type          string `json:"type"`
		ID            string `json:"id"`
		Domain        string `json:"domain"`
		Stratum       int    `json:"stratum"`
		Role          string `json:"role"`
	} `json:"constraints"`
}

// TestContextForHumanRenders: the human projection lists the path header
// and one line per constraint in the engine's deterministic order, exit 0
// with empty stderr.
func TestContextForHumanRenders(t *testing.T) {
	seedContextForRepo(t)
	code, out, errText := runIn([]string{"context", "--for", "src/main.go"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", code, out, errText)
	}
	if errText != "" {
		t.Errorf("stderr must be empty on success, got %q", errText)
	}
	// The header carries the normalized path; the three constraints render
	// in stratum-then-identity order.
	want := []string{
		"Constraints for src/main.go",
		"eka-view-fixture/req:aaa:1",
		"eka-view-fixture/std:aaa:1",
		"eka-view-fixture/std:zzz:1",
	}
	last := -1
	for _, w := range want {
		i := strings.Index(out, w)
		if i < 0 {
			t.Fatalf("human render missing %q, got:\n%s", w, out)
		}
		if i < last {
			t.Errorf("human render out of order at %q:\n%s", w, out)
		}
		last = i
	}
}

// TestContextForJSONSchema: --json/--compact emit ONLY the constraints
// object (schema eka-constraints-v1), and the compact form parses to the
// same object.
func TestContextForJSONSchema(t *testing.T) {
	seedContextForRepo(t)
	code, out, errText := runIn([]string{"context", "--for", "src/main.go", "--json"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", code, out, errText)
	}
	if errText != "" {
		t.Errorf("stderr must be empty on success, got %q", errText)
	}
	doc := parseConstraints(t, out)
	if doc.Schema != "eka-constraints-v1" {
		t.Errorf("schema = %q, want eka-constraints-v1", doc.Schema)
	}
	if doc.Path != "src/main.go" {
		t.Errorf("path = %q, want src/main.go", doc.Path)
	}
	var forms []string
	for _, c := range doc.Constraints {
		forms = append(forms, c.CanonicalForm)
		if c.Role != "appliesTo" {
			t.Errorf("%s role = %q, want appliesTo", c.CanonicalForm, c.Role)
		}
	}
	want := []string{
		"eka-view-fixture/req:aaa:1",
		"eka-view-fixture/std:aaa:1",
		"eka-view-fixture/std:zzz:1",
	}
	if len(forms) != len(want) {
		t.Fatalf("constraints = %v, want %v", forms, want)
	}
	for i := range want {
		if forms[i] != want[i] {
			t.Fatalf("constraints = %v, want %v", forms, want)
		}
	}
	// --compact carries the same object on one line.
	code, compact, errText := runIn([]string{"context", "--for", "src/main.go", "--compact"})
	if code != 0 {
		t.Fatalf("compact: exit = %d, stderr %q", code, errText)
	}
	var a, b any
	if err := json.Unmarshal([]byte(out), &a); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(compact), &b); err != nil {
		t.Fatalf("compact must be parseable: %v\n%q", err, compact)
	}
	if len(compact) == 0 || compact[len(compact)-1] != '\n' {
		t.Error("compact output must end in a trailing newline")
	}
}

// TestContextForEmptySet: a path matched by no declaration is exit 0 with a
// clear human message, and an empty (never null) constraints array in JSON.
func TestContextForEmptySet(t *testing.T) {
	seedContextForRepo(t)
	code, out, errText := runIn([]string{"context", "--for", "unrelated/file.txt"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", code, out, errText)
	}
	if errText != "" {
		t.Errorf("stderr must be empty on an empty set, got %q", errText)
	}
	if strings.Index(out, "no constraints bind unrelated/file.txt") < 0 {
		t.Errorf("empty set must render the clear message, got:\n%s", out)
	}
	code, jsonOut, _ := runIn([]string{"context", "--for", "unrelated/file.txt", "--json"})
	if code != 0 {
		t.Fatalf("json: exit = %d, want 0", code)
	}
	if !strings.Contains(jsonOut, `"constraints": []`) {
		t.Errorf("empty set must serialize as an empty array, got:\n%s", jsonOut)
	}
	doc := parseConstraints(t, jsonOut)
	if len(doc.Constraints) != 0 {
		t.Errorf("empty set constraints = %v, want empty", doc.Constraints)
	}
}

// TestContextForEmptyPathAllowed: an empty path and "." are allowed and
// bind only what the declarations match (here, nothing).
func TestContextForEmptyPathAllowed(t *testing.T) {
	seedContextForRepo(t)
	for _, p := range []string{"", "."} {
		code, out, errText := runIn([]string{"context", "--for", p, "--json"})
		if code != 0 {
			t.Fatalf("--for %q: exit = %d, want 0, stderr %q", p, code, errText)
		}
		if errText != "" {
			t.Errorf("--for %q: stderr must be empty, got %q", p, errText)
		}
		if doc := parseConstraints(t, out); len(doc.Constraints) != 0 {
			t.Errorf("--for %q: constraints = %v, want empty", p, doc.Constraints)
		}
	}
}

// TestContextForSubjectExclusiveExitsTwo: a subject together with --for is a
// usage error (exit 2) — the modes are mutually exclusive.
func TestContextForSubjectExclusiveExitsTwo(t *testing.T) {
	seedContextForRepo(t)
	code, out, errText := runIn([]string{"context", "eka-view-fixture/sto:alpha", "--for", "src/main.go"})
	if code != 2 {
		t.Fatalf("exit = %d, want 2\nstdout: %s\nstderr: %s", code, out, errText)
	}
	if !strings.Contains(errText, "mutually exclusive") {
		t.Errorf("stderr must explain the exclusivity, got %q", errText)
	}
}

// TestContextForPathNormalization: an absolute path inside the repository
// matches exactly like its relative form; an absolute path outside the
// repository is refused (usage, exit 2). A relative path with a leading
// './' normalizes to the same match.
func TestContextForPathNormalization(t *testing.T) {
	repo := seedContextForRepo(t)

	// Relative form (the baseline).
	_, relOut, _ := runIn([]string{"context", "--for", "src/main.go", "--json"})
	relDoc := parseConstraints(t, relOut)

	// Absolute path inside the repository → the same set, same normalized
	// repository-relative path.
	abs := filepath.Join(repo, "src", "main.go")
	code, absOut, errText := runIn([]string{"context", "--for", abs, "--json"})
	if code != 0 {
		t.Fatalf("absolute inside: exit = %d, stderr %q", code, errText)
	}
	absDoc := parseConstraints(t, absOut)
	if absDoc.Path != "src/main.go" {
		t.Errorf("absolute inside: path = %q, want src/main.go", absDoc.Path)
	}
	if len(absDoc.Constraints) != len(relDoc.Constraints) {
		t.Errorf("absolute inside matched %d, relative matched %d", len(absDoc.Constraints), len(relDoc.Constraints))
	}

	// A leading './' normalizes away.
	code, dotOut, _ := runIn([]string{"context", "--for", "./src/main.go", "--json"})
	if code != 0 {
		t.Fatalf("leading ./: exit = %d", code)
	}
	if doc := parseConstraints(t, dotOut); doc.Path != "src/main.go" {
		t.Errorf("leading ./: path = %q, want src/main.go", doc.Path)
	}

	// Absolute path outside the repository → refused, exit 2.
	outside := filepath.Join(t.TempDir(), "outside.go")
	code, out, errText := runIn([]string{"context", "--for", outside, "--json"})
	if code != 2 {
		t.Fatalf("absolute outside: exit = %d, want 2\nstdout: %s\nstderr: %s", code, out, errText)
	}
	if !strings.Contains(errText, "outside the repository") {
		t.Errorf("absolute outside: stderr must carry the refusal, got %q", errText)
	}
	if out != "" {
		t.Errorf("absolute outside: stdout must be empty, got %q", out)
	}
}

// parseConstraints parses the --for machine document.
func parseConstraints(t *testing.T, out string) constraintsDoc {
	t.Helper()
	var doc constraintsDoc
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("stdout must be a parseable constraints object: %v\nstdout: %q", err, out)
	}
	return doc
}
