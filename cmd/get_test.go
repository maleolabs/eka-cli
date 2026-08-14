package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/maleolabs/eka-core/runtime"
)

// seedGetRepo seeds a fresh workspace (EKA_HOME) with a copy of the
// view "valid" fixture through the Runtime (the store-backed setup of
// the get path: runtime.Ensure + Authoring.Sync), optionally adding
// extra authoring docs before the sync, and chdirs into the repo copy.
// Returns the repo path.
func seedGetRepo(t *testing.T, extra func(repo string)) string {
	t.Helper()
	t.Setenv("EKA_HOME", t.TempDir())
	repo := copyFixture(t, filepath.Join("..", "testdata", "view", "valid"))
	if extra != nil {
		extra(repo)
	}
	r, err := runtime.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if _, err := runtime.Authoring.Sync(r, repo, runtime.SyncOptions{Pull: true, Push: true}); err != nil {
		t.Fatal(err)
	}
	chdirInto(t, repo)
	return repo
}

// getDocument parses the stdout of a successful get run as a machine
// document.
func getDocument(t *testing.T, args ...string) map[string]any {
	t.Helper()
	code, out, errText := runIn(args)
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", code, out, errText)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("stdout must be a single parseable JSON document: %v\nstdout: %q", err, out)
	}
	return doc
}

// TestGetIdentityCanonicalFormGolden: identity lookup by the RSF
// canonical form produces the exact pinned machine JSON (byte-compare)
// and nothing else on stdout.
func TestGetIdentityCanonicalFormGolden(t *testing.T) {
	seedGetRepo(t, nil)
	code, out, errText := runIn([]string{"get", "eka-view-fixture/adr:001-login-serialization:1"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", code, out, errText)
	}
	if errText != "" {
		t.Errorf("stderr must be empty on success, got %q", errText)
	}
	want := `{
  "schema": "eka-cko-v2",
  "identity": {
    "namespace": "eka-view-fixture",
    "type": "adr",
    "id": "001-login-serialization",
    "instanceVersion": 1
  },
  "canonicalForm": "eka-view-fixture/adr:001-login-serialization:1",
  "engineeringDomain": "Architecture",
  "stratum": 2,
  "revision": 1,
  "author": "Engineering Architecture",
  "created": "2026-08-05",
  "updated": "2026-08-05",
  "stateVector": {
    "contentState": "accepted",
    "existenceState": "active"
  },
  "classification": {
    "dimension": "decisions",
    "domain": "Architecture"
  },
  "changeLog": [
    {
      "date": "2026-08-05",
      "domain": "existence-state",
      "from": "-",
      "to": "active",
      "by": "Engineering Architecture"
    },
    {
      "date": "2026-08-05",
      "domain": "content-state",
      "from": "proposed",
      "to": "accepted",
      "by": "Engineering Architecture"
    }
  ],
  "content": {
    "representation": "eka/structured-json/1",
    "fields": {
      "context": "Context body.",
      "decision": "Decision body.",
      "consequences": "Consequences body.",
      "alternativesConsidered": "Alternatives body."
    }
  },
  "objectHash": "2fc697ce15d080fdb9fc53eba70ef313eeef03c93064fec0f490d6b99cf96a36"
}
`
	if out != want {
		t.Errorf("stdout must be the pinned golden document:\ngot:\n%s\nwant:\n%s", out, want)
	}
}

// TestGetIdentityQualifiedLineFormHighestInstance: the qualified line
// form resolves to the highest instance-version of the line — the
// latest knowledge version (ADR-025). The fixture copy gains a second
// instance of the plan:roadmap-2026 line before the sync; the line
// form must return instance 2.
func TestGetIdentityQualifiedLineFormHighestInstance(t *testing.T) {
	seedGetRepo(t, func(repo string) {
		v2 := `---
namespace: eka-view-fixture
type: plan
id: roadmap-2026
instance-version: 2
revision: 2
content-state: draft
planning-state: draft
existence-state: active
phase: release
dimension: planning
author: Engineering Architecture
created: 2026-08-06
updated: 2026-08-06
supersedes: []
derives-from: []
depends-on: []
change-log:
  - date: 2026-08-06
    domain: existence-state
    from: "-"
    to: active
    by: Engineering Architecture
  - date: 2026-08-06
    domain: content-state
    from: "-"
    to: draft
    by: Engineering Architecture
  - date: 2026-08-06
    domain: planning-state
    from: "-"
    to: draft
    by: Engineering Architecture
  - date: 2026-08-06
    domain: phase
    from: "-"
    to: release
    by: Engineering Architecture
---
# Plan — Roadmap 2026 (v2)

## Objective

Objective v2.

## Scope

Scope v2.

## Out of Scope

Out of scope v2.
`
		if err := os.WriteFile(filepath.Join(repo, "docs", "planning", "plan-roadmap-2026-v2.md"), []byte(v2), 0o644); err != nil {
			t.Fatal(err)
		}
	})
	doc := getDocument(t, "get", "eka-view-fixture/plan:roadmap-2026")
	if got := doc["canonicalForm"]; got != "eka-view-fixture/plan:roadmap-2026:2" {
		t.Errorf("canonical_form = %v, want the highest instance (v2, the latest knowledge version)", got)
	}
	if got := doc["revision"]; got != float64(2) {
		t.Errorf("revision = %v, want 2", got)
	}
}

// TestGetUnregisteredRepoExitsOne: the repository-state gate runs
// first. Two refusal classes (ADR-018): a directory without eka.yaml
// is not an EKA repository — refused with the pinned gate message; a
// metadata repository that is not registered is refused with the
// existing byte-identical message. Both exit 1, no JSON. The workspace
// must exist (get never creates one): seed it with runtime.Ensure,
// then run from an unregistered directory.
func TestGetUnregisteredRepoExitsOne(t *testing.T) {
	t.Setenv("EKA_HOME", t.TempDir())
	r, err := runtime.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	chdirInto(t, t.TempDir())
	code, out, errText := runIn([]string{"get", "execution"})
	if code != 1 {
		t.Fatalf("exit = %d, want 1\nstdout: %s\nstderr: %s", code, out, errText)
	}
	if out != "" {
		t.Errorf("stdout must be empty (no JSON), got %q", out)
	}
	if !strings.Contains(errText, "eka: get refused:") {
		t.Errorf("stderr must carry the refusal, got %q", errText)
	}
	if !strings.Contains(errText, "is not an EKA repository (no eka.yaml)") ||
		!strings.Contains(errText, "run 'eka init' first") {
		t.Errorf("stderr must carry the pinned ADR-018 refusal, got %q", errText)
	}

	// A metadata repository that is not registered keeps the existing
	// byte-identical refusal message.
	meta := t.TempDir()
	writeEkaYAML(t, meta, filepath.Base(meta), filepath.Base(meta), "eka-view-fixture")
	chdirInto(t, meta)
	code, out, errText = runIn([]string{"get", "execution"})
	if code != 1 {
		t.Fatalf("unregistered metadata repo: exit = %d, want 1\nstdout: %s\nstderr: %s", code, out, errText)
	}
	if out != "" {
		t.Errorf("stdout must be empty (no JSON), got %q", out)
	}
	if !strings.Contains(errText, "eka: get refused: repository") {
		t.Errorf("stderr must carry the refusal, got %q", errText)
	}
	if !strings.Contains(errText, "not registered in the EKA workspace") {
		t.Errorf("stderr must explain the refusal, got %q", errText)
	}
	if !strings.Contains(errText, "eka sync") || !strings.Contains(errText, "eka project register") {
		t.Errorf("stderr must hint at sync/register, got %q", errText)
	}
}

// TestGetNoWorkspaceExitsOne: `eka get` never creates the workspace —
// a missing workspace.json is a deterministic refusal, exit 1.
func TestGetNoWorkspaceExitsOne(t *testing.T) {
	home := t.TempDir()
	t.Setenv("EKA_HOME", home)
	chdirInto(t, t.TempDir())
	code, out, errText := runIn([]string{"get", "execution"})
	if code != 1 {
		t.Fatalf("exit = %d, want 1\nstdout: %s\nstderr: %s", code, out, errText)
	}
	if out != "" {
		t.Errorf("stdout must be empty (no JSON), got %q", out)
	}
	if !strings.Contains(errText, "eka: get refused: no EKA workspace at") {
		t.Errorf("stderr must name the missing workspace, got %q", errText)
	}
	if !strings.Contains(errText, "run 'eka sync' first") {
		t.Errorf("stderr must hint at 'eka sync', got %q", errText)
	}
	// The refusal must not have created the workspace.
	if _, err := os.Stat(filepath.Join(home, "workspace.json")); !os.IsNotExist(err) {
		t.Error("get must never initialize the workspace")
	}
}

// TestGetInvalidDomainExitsTwo: a target without ":" that is not one
// of the five Engineering Domain tokens (and not the containers
// query) is a usage error listing the valid targets — exit 2, no
// workspace access.
func TestGetInvalidDomainExitsTwo(t *testing.T) {
	seedGetRepo(t, nil)
	code, out, errText := runIn([]string{"get", "bogus"})
	if code != 2 {
		t.Fatalf("exit = %d, want 2\nstdout: %s\nstderr: %s", code, out, errText)
	}
	if out != "" {
		t.Errorf("stdout must be empty (no JSON), got %q", out)
	}
	if !strings.Contains(errText, `unknown target "bogus"`) {
		t.Errorf("stderr must name the target, got %q", errText)
	}
	for _, token := range []string{"containers", "discovery", "architecture", "planning", "execution", "operations"} {
		if !strings.Contains(errText, token) {
			t.Errorf("stderr must list the %s target token, got %q", token, errText)
		}
	}
}

// TestGetUnknownIdentityExitsTwo: an identity that parses but does not
// exist is a deterministic usage-class error, exit 2.
func TestGetUnknownIdentityExitsTwo(t *testing.T) {
	seedGetRepo(t, nil)
	code, out, errText := runIn([]string{"get", "eka-view-fixture/sto:nonexistent"})
	if code != 2 {
		t.Fatalf("exit = %d, want 2\nstdout: %s\nstderr: %s", code, out, errText)
	}
	if out != "" {
		t.Errorf("stdout must be empty (no JSON), got %q", out)
	}
	if !strings.Contains(errText, `no knowledge object matches "eka-view-fixture/sto:nonexistent"`) {
		t.Errorf("stderr must name the missing identity, got %q", errText)
	}
}

// TestGetUnqualifiedIdentityExitsTwo: unqualified reference forms are
// refused with the expected forms listed (the Resolver contract) —
// exit 2. (A target without ":" is a domain query, not an identity:
// see TestGetInvalidDomainExitsTwo.)
func TestGetUnqualifiedIdentityExitsTwo(t *testing.T) {
	seedGetRepo(t, nil)
	for _, target := range []string{"sto:alpha", ":alpha"} {
		code, out, errText := runIn([]string{"get", target})
		if code != 2 {
			t.Errorf("target %q: exit = %d, want 2\nstdout: %s", target, code, out)
		}
		if out != "" {
			t.Errorf("target %q: stdout must be empty, got %q", target, out)
		}
		if !strings.Contains(errText, "<ns>/") {
			t.Errorf("target %q: stderr must list the expected qualified forms, got %q", target, errText)
		}
	}
}

// TestGetNoArgExitsTwo: `eka get` without a target is a usage error
// with the query-model summary on stderr (machine commands never print
// banners) — exit 2.
func TestGetNoArgExitsTwo(t *testing.T) {
	code, out, errText := runIn([]string{"get"})
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if out != "" {
		t.Errorf("stdout must be empty (no banner), got %q", out)
	}
	for _, want := range []string{
		"eka get <target>",
		"<ns>/<type>:<id>",
		"discovery | architecture | planning | execution | operations",
	} {
		if !strings.Contains(errText, want) {
			t.Errorf("stderr must carry the usage summary with %q, got %q", want, errText)
		}
	}
	// The usage error must not depend on a workspace.
	if strings.Contains(errText, "workspace") {
		t.Errorf("stderr must not touch the workspace, got %q", errText)
	}
}

// TestGetHelpExitsZero covers the help entry points: the long help
// documents the machine interface purpose, the query model, the stable
// schema, the stdout contract, the exit codes, the retrieval option
// flags and the token-saving example combos.
func TestGetHelpExitsZero(t *testing.T) {
	for _, args := range [][]string{{"get", "-h"}, {"get", "--help"}} {
		code, text, _ := runIn(args)
		if code != 0 {
			t.Errorf("args %v: exit = %d, want 0", args, code)
		}
		for _, want := range []string{
			"eka get",
			"machine-readable",
			"eka-cko-v2",
			"canonical form",
			"qualified line form",
			"discovery | architecture | planning | execution |",
			"operations",
			"stdout carries ONLY the JSON document",
			"Exit codes:",
			"eka view",
			// Retrieval option flags (documented with their behavior).
			"--compact",
			"--no-content",
			"--upstream",
			"--downstream",
			"--timeline",
			"--type",
			"--dimension",
			"--phase",
			// The token-saving example combos.
			"eka get feather/sto:publish-post --compact",
			"eka get architecture --compact --no-content --type adr",
			"eka get feather/adr:content-storage --upstream --downstream --no-content",
			"eka get feather/plan:roadmap-v1 --timeline --no-content",
			"eka get execution --phase mvp",
		} {
			if !strings.Contains(text, want) {
				t.Errorf("args %v: help missing %q:\n%s", args, want, text)
			}
		}
	}
}

// TestGetTooManyArgsExitsTwo: at most one target.
func TestGetTooManyArgsExitsTwo(t *testing.T) {
	code, _, _ := runIn([]string{"get", "execution", "architecture"})
	if code != 2 {
		t.Errorf("exit = %d, want 2", code)
	}
}

// TestGetStdoutPurity: the stdout of every success path is exactly one
// parseable JSON document — no extra lines, no banners.
func TestGetStdoutPurity(t *testing.T) {
	seedGetRepo(t, nil)
	for _, args := range [][]string{
		{"get", "eka-view-fixture/adr:001-login-serialization:1"},
		{"get", "eka-view-fixture/sto:alpha"},
		{"get", "architecture"},
		{"get", "execution"},
		{"get", "operations"},
	} {
		code, out, errText := runIn(args)
		if code != 0 {
			t.Fatalf("%v: exit = %d, want 0\nstderr: %s", args, code, errText)
		}
		// stdout carries ONLY the JSON document followed by a single
		// trailing newline: exactly one trailing newline, no banners,
		// no informational lines.
		if !strings.HasSuffix(out, "\n") || strings.HasSuffix(out, "\n\n") {
			t.Errorf("%v: stdout must carry exactly one trailing newline, got %q", args, out)
		}
		if !json.Valid([]byte(out)) {
			t.Errorf("%v: stdout must be valid JSON, got %q", args, out)
		}
		var doc map[string]any
		if err := json.Unmarshal([]byte(out), &doc); err != nil {
			t.Errorf("%v: stdout must parse as one JSON document: %v", args, err)
		}
		if doc["schema"] != "eka-cko-v2" {
			t.Errorf("%v: document must carry the stable schema, got %v", args, doc["schema"])
		}
	}
}

// TestGetDomainQueryCollection: a domain query produces the "domain"
// collection with the canonical domain name, the unit count and the
// units sorted by canonical form.
func TestGetDomainQueryCollection(t *testing.T) {
	seedGetRepo(t, nil)
	code, out, errText := runIn([]string{"get", "execution"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", code, out, errText)
	}
	var col struct {
		Schema     string           `json:"schema"`
		Collection string           `json:"collection"`
		Domain     string           `json:"domain"`
		Count      int              `json:"count"`
		Units      []map[string]any `json:"units"`
	}
	if err := json.Unmarshal([]byte(out), &col); err != nil {
		t.Fatal(err)
	}
	if col.Schema != "eka-cko-v2" || col.Collection != "domain" || col.Domain != "Execution" {
		t.Errorf("collection header = %+v, want schema eka-cko-v2 / collection domain / domain Execution", col)
	}
	// The fixture carries 20 Execution units: 2 containers, 9 ticket
	// projections, 6 work items.
	if col.Count != 20 || len(col.Units) != 20 {
		t.Errorf("count = %d (units %d), want 20 (17 + 3 cmt notes, ADR-019)", col.Count, len(col.Units))
	}
	// Sorted by canonical form.
	for i := 1; i < len(col.Units); i++ {
		prev := col.Units[i-1]["canonicalForm"].(string)
		cur := col.Units[i]["canonicalForm"].(string)
		if prev >= cur {
			t.Errorf("units not sorted at %d: %q >= %q", i, prev, cur)
		}
	}
	// Every unit carries the derived engineering domain and stratum.
	for _, u := range col.Units {
		if u["engineeringDomain"] != "Execution" || u["stratum"] != float64(4) {
			t.Errorf("unit %v: engineering_domain/stratum wrong", u["canonicalForm"])
		}
	}
}

// TestGetDeterministicCLI: two runs of each query produce
// byte-identical stdout.
func TestGetDeterministicCLI(t *testing.T) {
	seedGetRepo(t, nil)
	runOnce := func(args ...string) string {
		_, out, _ := runIn(args)
		return out
	}
	for _, args := range [][]string{
		{"get", "eka-view-fixture/adr:001-login-serialization:1"},
		{"get", "eka-view-fixture/sto:alpha"},
		{"get", "discovery"},
		{"get", "architecture"},
		{"get", "planning"},
		{"get", "execution"},
		{"get", "operations"},
	} {
		if a, b := runOnce(args...), runOnce(args...); a != b {
			t.Errorf("output differs between runs for %v", args)
		}
	}
}

// TestGetRetrievalOptionsDeterministicCLI: the retrieval options do
// not break the determinism contract — two runs of every option combo
// are byte-identical.
func TestGetRetrievalOptionsDeterministicCLI(t *testing.T) {
	seedGetRepo(t, nil)
	runOnce := func(args ...string) string {
		_, out, _ := runIn(args)
		return out
	}
	for _, args := range [][]string{
		{"get", "eka-view-fixture/adr:001-login-serialization:1", "--compact"},
		{"get", "eka-view-fixture/adr:001-login-serialization:1", "--no-content"},
		{"get", "eka-view-fixture/tkt:sto-beta-multi:1", "--upstream", "--downstream", "--timeline", "--compact", "--no-content"},
		{"get", "eka-view-fixture/sto:alpha:1", "--downstream", "--no-content"},
		{"get", "architecture", "--compact", "--no-content", "--type", "adr"},
		{"get", "architecture", "--dimension", "decisions"},
		{"get", "planning", "--phase", "mvp"},
	} {
		if a, b := runOnce(args...), runOnce(args...); a != b {
			t.Errorf("output differs between runs for %v", args)
		}
	}
}

// TestGetIdentityCompactGolden: --compact emits the same document as
// the pretty form as ONE line followed by a single trailing newline —
// the exact pinned bytes (the same field order, same values).
func TestGetIdentityCompactGolden(t *testing.T) {
	seedGetRepo(t, nil)
	code, out, errText := runIn([]string{"get", "eka-view-fixture/adr:001-login-serialization:1", "--compact"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", code, out, errText)
	}
	if errText != "" {
		t.Errorf("stderr must be empty on success, got %q", errText)
	}
	want := `{"schema":"eka-cko-v2","identity":{"namespace":"eka-view-fixture","type":"adr","id":"001-login-serialization","instanceVersion":1},"canonicalForm":"eka-view-fixture/adr:001-login-serialization:1","engineeringDomain":"Architecture","stratum":2,"revision":1,"author":"Engineering Architecture","created":"2026-08-05","updated":"2026-08-05","stateVector":{"contentState":"accepted","existenceState":"active"},"classification":{"dimension":"decisions","domain":"Architecture"},"changeLog":[{"date":"2026-08-05","domain":"existence-state","from":"-","to":"active","by":"Engineering Architecture"},{"date":"2026-08-05","domain":"content-state","from":"proposed","to":"accepted","by":"Engineering Architecture"}],"content":{"representation":"eka/structured-json/1","fields":{"context":"Context body.","decision":"Decision body.","consequences":"Consequences body.","alternativesConsidered":"Alternatives body."}},"objectHash":"2fc697ce15d080fdb9fc53eba70ef313eeef03c93064fec0f490d6b99cf96a36"}`
	// Single line plus a single trailing newline.
	want += "\n"
	if out != want {
		t.Errorf("stdout must be the pinned compact golden:\ngot:\n%s\nwant:\n%s", out, want)
	}
	if strings.Contains(strings.TrimSuffix(out, "\n"), "\n") {
		t.Errorf("compact stdout must be a single line, got %q", out)
	}
	// The compact form parses to the same document as the pretty form.
	code, pretty, _ := runIn([]string{"get", "eka-view-fixture/adr:001-login-serialization:1"})
	if code != 0 {
		t.Fatalf("pretty run: exit = %d, want 0", code)
	}
	var a, b map[string]any
	if err := json.Unmarshal([]byte(out), &a); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(pretty), &b); err != nil {
		t.Fatal(err)
	}
	pa, _ := json.Marshal(a)
	pb, _ := json.Marshal(b)
	if string(pa) != string(pb) {
		t.Errorf("compact and pretty must carry the same document")
	}
}

// TestGetNoContentIdentity: --no-content strips the content field from
// the identity document — the key is absent entirely.
func TestGetNoContentIdentity(t *testing.T) {
	seedGetRepo(t, nil)
	code, out, errText := runIn([]string{"get", "eka-view-fixture/adr:001-login-serialization:1", "--no-content"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", code, out, errText)
	}
	if strings.Contains(out, `"content"`) {
		t.Errorf("--no-content must strip the content key:\n%s", out)
	}
	if !strings.Contains(out, `"objectHash"`) || !strings.Contains(out, `"canonicalForm"`) {
		t.Errorf("--no-content must keep the rest of the schema:\n%s", out)
	}
}

// TestGetNoContentDomain: --no-content strips the content field from
// every unit document of a domain collection — and from traversal
// documents when combined with --upstream/--downstream.
func TestGetNoContentDomain(t *testing.T) {
	seedGetRepo(t, nil)
	for _, args := range [][]string{
		{"get", "architecture", "--no-content"},
		{"get", "execution", "--no-content", "--type", "tkt"},
		{"get", "eka-view-fixture/tkt:sto-beta-multi:1", "--upstream", "--downstream", "--no-content"},
	} {
		code, out, errText := runIn(args)
		if code != 0 {
			t.Fatalf("%v: exit = %d, want 0\nstderr: %s", args, code, errText)
		}
		if strings.Contains(out, `"content"`) {
			t.Errorf("%v: no document may carry the content key:\n%s", args, out)
		}
	}
}

// TestGetUpstream: --upstream resolves the units the target's
// relationships point at — the seeded fixture projections derive from
// their work items — sorted by canonical form, as full machine
// documents appended after object_hash.
func TestGetUpstream(t *testing.T) {
	seedGetRepo(t, nil)
	doc := getDocument(t, "get", "eka-view-fixture/tkt:sto-beta-multi:1", "--upstream", "--no-content")
	up := doc["upstream"].([]any)
	if len(up) != 3 {
		t.Fatalf("upstream = %d units, want 3 (ctr:wave-1, sto:beta, ts:gamma)", len(up))
	}
	wantForms := []string{
		"eka-view-fixture/ctr:wave-1:1",
		"eka-view-fixture/sto:beta:1",
		"eka-view-fixture/ts:gamma:1",
	}
	for i, u := range up {
		unit := u.(map[string]any)
		if got := unit["canonicalForm"]; got != wantForms[i] {
			t.Errorf("upstream[%d] = %v, want %s (sorted by canonical form)", i, got, wantForms[i])
		}
		// The traversal units are full machine documents.
		if unit["schema"] != "eka-cko-v2" || unit["objectHash"] == nil {
			t.Errorf("upstream[%d] must be a full machine document, got %v", i, unit)
		}
		if _, ok := unit["content"]; ok {
			t.Errorf("upstream[%d] must be stripped by --no-content", i)
		}
	}
}

// TestGetDownstream: --downstream resolves the units that reference
// the target across the workspace — both fixture projections derive
// from sto:alpha — sorted by canonical form.
func TestGetDownstream(t *testing.T) {
	seedGetRepo(t, nil)
	doc := getDocument(t, "get", "eka-view-fixture/sto:alpha:1", "--downstream", "--no-content")
	down := doc["downstream"].([]any)
	if len(down) != 2 {
		t.Fatalf("downstream = %d units, want 2 (tkt:sto-alpha-dup, tkt:sto-alpha)", len(down))
	}
	wantForms := []string{
		"eka-view-fixture/tkt:sto-alpha-dup:1",
		"eka-view-fixture/tkt:sto-alpha:1",
	}
	for i, u := range down {
		unit := u.(map[string]any)
		if got := unit["canonicalForm"]; got != wantForms[i] {
			t.Errorf("downstream[%d] = %v, want %s (sorted by canonical form)", i, got, wantForms[i])
		}
	}
	// An absent traversal flag stays absent: no upstream key here.
	if _, ok := doc["upstream"]; ok {
		t.Errorf("document must not carry upstream when only --downstream is given")
	}
}

// TestGetUpstreamAbsentWhenEmpty: a unit without outgoing relationships
// yields no upstream key at all (nil stays absent — additive contract).
func TestGetUpstreamAbsentWhenEmpty(t *testing.T) {
	seedGetRepo(t, nil)
	doc := getDocument(t, "get", "eka-view-fixture/sto:alpha:1", "--upstream", "--no-content")
	if _, ok := doc["upstream"]; ok {
		t.Errorf("upstream must stay absent for a unit without outgoing relationships:\n%v", doc)
	}
}

// TestGetTimeline: --timeline includes the line's instances as a
// "timeline" array — {canonical_form, instance_version, revision,
// object_hash, change_log}, ascending instance-version (the line's
// history order). The seeded v2 instance gives the line two entries.
func TestGetTimeline(t *testing.T) {
	seedGetRepo(t, func(repo string) {
		v2 := `---
namespace: eka-view-fixture
type: plan
id: roadmap-2026
instance-version: 2
revision: 2
content-state: draft
planning-state: draft
existence-state: active
phase: release
dimension: planning
author: Engineering Architecture
created: 2026-08-06
updated: 2026-08-06
supersedes: []
derives-from: []
depends-on: []
change-log:
  - date: 2026-08-06
    domain: existence-state
    from: "-"
    to: active
    by: Engineering Architecture
  - date: 2026-08-06
    domain: content-state
    from: "-"
    to: draft
    by: Engineering Architecture
  - date: 2026-08-06
    domain: planning-state
    from: "-"
    to: draft
    by: Engineering Architecture
  - date: 2026-08-06
    domain: phase
    from: "-"
    to: release
    by: Engineering Architecture
---
# Plan — Roadmap 2026 (v2)

## Objective

Objective v2.

## Scope

Scope v2.

## Out of Scope

Out of scope v2.
`
		if err := os.WriteFile(filepath.Join(repo, "docs", "planning", "plan-roadmap-2026-v2.md"), []byte(v2), 0o644); err != nil {
			t.Fatal(err)
		}
	})
	doc := getDocument(t, "get", "eka-view-fixture/plan:roadmap-2026", "--timeline", "--no-content")
	tl := doc["timeline"].([]any)
	if len(tl) != 2 {
		t.Fatalf("timeline = %d entries, want 2 (v1, v2)", len(tl))
	}
	for i, e := range tl {
		entry := e.(map[string]any)
		if got := entry["instanceVersion"]; got != float64(i+1) {
			t.Errorf("timeline[%d] instance_version = %v, want %d (ascending history order)", i, got, i+1)
		}
		for _, key := range []string{"canonicalForm", "instanceVersion", "revision", "objectHash", "changeLog"} {
			if _, ok := entry[key]; !ok {
				t.Errorf("timeline[%d] must carry %s (pinned entry fields), got %v", i, key, entry)
			}
		}
		if cl := entry["changeLog"].([]any); len(cl) == 0 {
			t.Errorf("timeline[%d] must carry the instance change log", i)
		}
		if want := "eka-view-fixture/plan:roadmap-2026:" + strconv.Itoa(i+1); entry["canonicalForm"] != want {
			t.Errorf("timeline[%d] canonical_form = %v, want %s", i, entry["canonicalForm"], want)
		}
	}
	// The timeline document keeps its own identity block and hash.
	if _, ok := doc["objectHash"]; !ok {
		t.Errorf("timeline document must keep object_hash")
	}
}

// TestGetDomainFilters: the domain-query filters narrow the collection
// by exact match — artifact type token, knowledge dimension and phase
// context attribute.
func TestGetDomainFilters(t *testing.T) {
	seedGetRepo(t, nil)
	// --type adr: only adr units of the architecture domain.
	code, out, errText := runIn([]string{"get", "architecture", "--type", "adr"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstderr: %s", code, errText)
	}
	var col struct {
		Count int              `json:"count"`
		Units []map[string]any `json:"units"`
	}
	if err := json.Unmarshal([]byte(out), &col); err != nil {
		t.Fatal(err)
	}
	if col.Count != 3 || len(col.Units) != 3 {
		t.Errorf("--type adr: count = %d, want 3", col.Count)
	}
	for _, u := range col.Units {
		if u["identity"].(map[string]any)["type"] != "adr" {
			t.Errorf("--type adr returned a non-adr unit: %v", u["canonicalForm"])
		}
	}
	// --dimension decisions: 3 adr + dec-001 = 4 units.
	doc := getDocument(t, "get", "architecture", "--dimension", "decisions")
	units := doc["units"].([]any)
	if len(units) != 4 {
		t.Errorf("--dimension decisions: %d units, want 4 (3 adr + dec-001)", len(units))
	}
	// --phase mvp: the single mvp-phase unit of the planning domain.
	doc = getDocument(t, "get", "planning", "--phase", "mvp")
	units = doc["units"].([]any)
	if len(units) != 1 {
		t.Errorf("--phase mvp: %d units, want 1", len(units))
	}
	if got := units[0].(map[string]any)["canonicalForm"]; got != "eka-view-fixture/scp:wave-2:1" {
		t.Errorf("--phase mvp unit = %v, want eka-view-fixture/scp:wave-2:1", got)
	}
}

// TestGetApplicabilityErrors: the flag/target applicability rules are
// deterministic usage errors — exact messages, exit 2, no JSON on
// stdout, no workspace access needed.
func TestGetApplicabilityErrors(t *testing.T) {
	seedGetRepo(t, nil)
	// Traversal flags with a domain target.
	for _, args := range [][]string{
		{"get", "execution", "--upstream"},
		{"get", "architecture", "--downstream"},
		{"get", "planning", "--timeline"},
		{"get", "execution", "--upstream", "--downstream", "--timeline"},
	} {
		code, out, errText := runIn(args)
		if code != 2 {
			t.Errorf("%v: exit = %d, want 2", args, code)
		}
		if out != "" {
			t.Errorf("%v: stdout must be empty, got %q", args, out)
		}
		want := "eka: get: --upstream, --downstream and --timeline require an identity target (a form containing ':')"
		if !strings.Contains(errText, want) {
			t.Errorf("%v: stderr must carry the exact message, got %q", args, errText)
		}
	}
	// Filter flags with an identity target.
	for _, args := range [][]string{
		{"get", "eka-view-fixture/sto:alpha:1", "--type", "adr"},
		{"get", "eka-view-fixture/sto:alpha:1", "--dimension", "decisions"},
		{"get", "eka-view-fixture/sto:alpha:1", "--phase", "mvp"},
		{"get", "eka-view-fixture/sto:alpha:1", "--type", "adr", "--dimension", "decisions", "--phase", "mvp"},
	} {
		code, out, errText := runIn(args)
		if code != 2 {
			t.Errorf("%v: exit = %d, want 2", args, code)
		}
		if out != "" {
			t.Errorf("%v: stdout must be empty, got %q", args, out)
		}
		want := "eka: get: --type, --dimension and --phase require a domain target (one of the five Engineering Domains)"
		if !strings.Contains(errText, want) {
			t.Errorf("%v: stderr must carry the exact message, got %q", args, errText)
		}
	}
}

// TestGetApplicabilityErrorsWithoutWorkspace: the applicability rules
// are pure flag/target validation — they fire before any workspace
// access (no EKA_HOME, no repository), exit 2.
func TestGetApplicabilityErrorsWithoutWorkspace(t *testing.T) {
	t.Setenv("EKA_HOME", t.TempDir())
	chdirInto(t, t.TempDir())
	code, out, errText := runIn([]string{"get", "execution", "--upstream"})
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (usage error precedes workspace state)", code)
	}
	if out != "" {
		t.Errorf("stdout must be empty, got %q", out)
	}
	if !strings.Contains(errText, "require an identity target") {
		t.Errorf("stderr must carry the applicability message, got %q", errText)
	}
	if strings.Contains(errText, "workspace") {
		t.Errorf("the applicability error must not touch the workspace, got %q", errText)
	}
}

// TestGetComboCompactNoContentUpstream: --compact + --no-content +
// --upstream combine on one identity lookup: one line, content absent
// from every document, upstream array present and parseable.
func TestGetComboCompactNoContentUpstream(t *testing.T) {
	seedGetRepo(t, nil)
	code, out, errText := runIn([]string{"get", "eka-view-fixture/tkt:sto-beta-multi:1", "--compact", "--no-content", "--upstream"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", code, out, errText)
	}
	// One line plus the single trailing newline.
	if strings.Contains(strings.TrimSuffix(out, "\n"), "\n") || !strings.HasSuffix(out, "\n") {
		t.Errorf("stdout must be a single line plus trailing newline, got %q", out)
	}
	if strings.Contains(out, `"content"`) {
		t.Errorf("no document may carry the content key:\n%s", out)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatal(err)
	}
	up, ok := doc["upstream"].([]any)
	if !ok || len(up) != 3 {
		t.Errorf("upstream must be a 3-unit array, got %v", doc["upstream"])
	}
}

// TestGetContainersGolden: `eka get containers` emits the exact
// indented containers collection — pinned field order, the container
// fields, count == the total container count, exactly one trailing
// newline, stdout-only (no banners, no stderr).
func TestGetContainersGolden(t *testing.T) {
	seedGetRepo(t, nil)
	code, out, errText := runIn([]string{"get", "containers"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", code, out, errText)
	}
	if errText != "" {
		t.Errorf("stderr must be empty on success, got %q", errText)
	}
	want := `{
  "schema": "eka-cko-v2",
  "collection": "containers",
  "count": 2,
  "containers": [
    {
      "canonicalForm": "eka-view-fixture/ctr:wave-0",
      "id": "wave-0",
      "items": 1,
      "tickets": 1,
      "startedAt": "2026-08-05",
      "containerState": "completed"
    },
    {
      "canonicalForm": "eka-view-fixture/ctr:wave-1",
      "id": "wave-1",
      "items": 5,
      "tickets": 8,
      "startedAt": "2026-08-05",
      "containerState": "active"
    }
  ]
}
`
	if out != want {
		t.Errorf("stdout must be the pinned golden collection:\ngot:\n%s\nwant:\n%s", out, want)
	}
	// The default output is the unpaged schema: no pagination field.
	if strings.Contains(out, "pagination") {
		t.Errorf("default containers output must not carry pagination:\n%s", out)
	}
}

// TestGetContainersCompact: --compact emits the same collection as one
// line plus a single trailing newline.
func TestGetContainersCompact(t *testing.T) {
	seedGetRepo(t, nil)
	code, out, errText := runIn([]string{"get", "containers", "--compact"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", code, out, errText)
	}
	if strings.Count(out, "\n") != 1 || !strings.HasSuffix(out, "\n") {
		t.Errorf("compact output must be one line plus a trailing newline, got %d newlines", strings.Count(out, "\n"))
	}
	if !strings.HasPrefix(out, `{"schema":"eka-cko-v2","collection":"containers"`) {
		t.Errorf("compact output must start with the pinned envelope, got %q", out)
	}
	pretty := getDocument(t, "get", "containers")
	if pretty["count"].(float64) != 2 {
		t.Errorf("compact and pretty must agree on the collection, got count %v", pretty["count"])
	}
}

// TestGetContainersActive: --active keeps only the active containers;
// count narrows to the filtered population.
func TestGetContainersActive(t *testing.T) {
	seedGetRepo(t, nil)
	doc := getDocument(t, "get", "containers", "--active")
	if doc["collection"] != "containers" {
		t.Fatalf("collection = %v, want containers", doc["collection"])
	}
	if doc["count"].(float64) != 1 {
		t.Errorf("count = %v, want 1 (the filtered population)", doc["count"])
	}
	containers := doc["containers"].([]any)
	if len(containers) != 1 {
		t.Fatalf("containers = %d, want 1 (completed excluded)", len(containers))
	}
	first := containers[0].(map[string]any)
	if first["canonicalForm"] != "eka-view-fixture/ctr:wave-1" || first["containerState"] != "active" {
		t.Errorf("active filter kept %v, want only the active wave-1", first)
	}
	// --current is the alias of --active.
	doc = getDocument(t, "get", "containers", "--current")
	if len(doc["containers"].([]any)) != 1 {
		t.Errorf("--current must keep only the active container, got %v", doc["containers"])
	}
}

// TestGetContainersContainerFilter: --container keeps the single
// matching container — bare id, ctr-<id>, ctr:<id> and the qualified
// canonical form all resolve; an unknown container is a usage error
// (exit 2) listing the available forms.
func TestGetContainersContainerFilter(t *testing.T) {
	seedGetRepo(t, nil)
	for _, target := range []string{"wave-0", "ctr-wave-0", "ctr:wave-0", "eka-view-fixture/ctr:wave-0"} {
		doc := getDocument(t, "get", "containers", "--container", target)
		containers := doc["containers"].([]any)
		if len(containers) != 1 {
			t.Fatalf("--container %s: containers = %d, want 1", target, len(containers))
		}
		first := containers[0].(map[string]any)
		if first["canonicalForm"] != "eka-view-fixture/ctr:wave-0" {
			t.Errorf("--container %s resolved %v, want wave-0", target, first["canonicalForm"])
		}
		if doc["count"].(float64) != 1 {
			t.Errorf("--container %s: count = %v, want 1 (the filtered population)", target, doc["count"])
		}
	}
	// Unknown container: exit 2, the available forms listed.
	code, out, errText := runIn([]string{"get", "containers", "--container", "wave-6"})
	if code != 2 {
		t.Fatalf("exit = %d, want 2\nstdout: %s\nstderr: %s", code, out, errText)
	}
	if out != "" {
		t.Errorf("stdout must be empty (no JSON), got %q", out)
	}
	if !strings.Contains(errText, `get: container "wave-6" not found`) {
		t.Errorf("stderr must name the missing container, got %q", errText)
	}
	if !strings.Contains(errText, "available containers: eka-view-fixture/ctr:wave-0, eka-view-fixture/ctr:wave-1") {
		t.Errorf("stderr must list the available forms, got %q", errText)
	}
}

// TestGetContainersPagination: --limit/--page/--offset window the
// containers list; the pagination object carries the effective window,
// count stays the total.
func TestGetContainersPagination(t *testing.T) {
	seedGetRepo(t, nil)
	doc := getDocument(t, "get", "containers", "--limit", "1")
	containers := doc["containers"].([]any)
	if len(containers) != 1 || containers[0].(map[string]any)["canonicalForm"] != "eka-view-fixture/ctr:wave-0" {
		t.Errorf("--limit 1 kept %v, want only wave-0", containers)
	}
	want := map[string]float64{"offset": 0, "limit": 1, "page": 1, "total": 2, "pages": 2}
	pag, ok := doc["pagination"].(map[string]any)
	if !ok {
		t.Fatal("--limit 1 must carry the pagination object")
	}
	for k, v := range want {
		if pag[k] != v {
			t.Errorf("pagination[%s] = %v, want %v", k, pag[k], v)
		}
	}
	if doc["count"].(float64) != 2 {
		t.Errorf("count = %v, want 2 (the total)", doc["count"])
	}
	// --limit 1 --page 2: the second page.
	doc = getDocument(t, "get", "containers", "--limit", "1", "--page", "2")
	containers = doc["containers"].([]any)
	if len(containers) != 1 || containers[0].(map[string]any)["canonicalForm"] != "eka-view-fixture/ctr:wave-1" {
		t.Errorf("--limit 1 --page 2 kept %v, want only wave-1", containers)
	}
	pag = doc["pagination"].(map[string]any)
	if pag["offset"] != float64(1) || pag["page"] != float64(2) {
		t.Errorf("pagination = %v, want offset 1 / page 2", pag)
	}
	// --offset without --limit: windows to the end. The pagination
	// page follows the effective-window formula (offset/limit+1 — the
	// note's "Page=1" holds while offset < limit, i.e. offset smaller
	// than half the collection).
	doc = getDocument(t, "get", "containers", "--offset", "1")
	containers = doc["containers"].([]any)
	if len(containers) != 1 || containers[0].(map[string]any)["canonicalForm"] != "eka-view-fixture/ctr:wave-1" {
		t.Errorf("--offset 1 kept %v, want only wave-1", containers)
	}
	pag = doc["pagination"].(map[string]any)
	if pag["offset"] != float64(1) || pag["limit"] != float64(1) || pag["page"] != float64(2) {
		t.Errorf("pagination = %v, want offset 1 / limit 1 (total-offset) / page 2", pag)
	}
	// --offset 0 without --limit: the common case — page 1, the full
	// remaining list.
	doc = getDocument(t, "get", "containers", "--offset", "0")
	containers = doc["containers"].([]any)
	if len(containers) != 2 {
		t.Errorf("--offset 0 kept %d containers, want 2 (the whole list)", len(containers))
	}
	pag = doc["pagination"].(map[string]any)
	if pag["offset"] != float64(0) || pag["limit"] != float64(2) || pag["page"] != float64(1) {
		t.Errorf("pagination = %v, want offset 0 / limit 2 / page 1", pag)
	}
}

// TestGetExecutionPagination: the execution domain pages like the
// containers query — the units are windowed, count stays the TOTAL and
// the pagination object carries the effective window; the default
// (no pagination flags) output stays byte-identical to the unpaged
// schema.
func TestGetExecutionPagination(t *testing.T) {
	seedGetRepo(t, nil)
	_, plain, _ := runIn([]string{"get", "execution"})
	if strings.Contains(plain, "pagination") {
		t.Errorf("default execution output must not carry pagination")
	}
	code, out, errText := runIn([]string{"get", "execution", "--limit", "2"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", code, out, errText)
	}
	var col struct {
		Count      int              `json:"count"`
		Units      []map[string]any `json:"units"`
		Pagination *struct {
			Offset int `json:"offset"`
			Limit  int `json:"limit"`
			Page   int `json:"page"`
			Total  int `json:"total"`
			Pages  int `json:"pages"`
		} `json:"pagination"`
	}
	if err := json.Unmarshal([]byte(out), &col); err != nil {
		t.Fatal(err)
	}
	if col.Count != 20 || len(col.Units) != 2 {
		t.Errorf("count/units = %d/%d, want 20/2 (count stays the total)", col.Count, len(col.Units))
	}
	if col.Pagination == nil || *col.Pagination != (struct {
		Offset int `json:"offset"`
		Limit  int `json:"limit"`
		Page   int `json:"page"`
		Total  int `json:"total"`
		Pages  int `json:"pages"`
	}{0, 2, 1, 20, 10}) {
		t.Errorf("pagination = %+v, want {0 2 1 20 10}", col.Pagination)
	}
	// --limit 2 --page 2: the second window starts at offset 2.
	doc := getDocument(t, "get", "execution", "--limit", "2", "--page", "2")
	units := doc["units"].([]any)
	if len(units) != 2 {
		t.Fatalf("--limit 2 --page 2 units = %d, want 2", len(units))
	}
	if units[0].(map[string]any)["canonicalForm"] != "eka-view-fixture/cmt:delta-implementation:1" {
		t.Errorf("page 2 must start at the 3rd unit (offset 2), got %v", units[0])
	}
	pag := doc["pagination"].(map[string]any)
	if pag["offset"] != float64(2) || pag["page"] != float64(2) || pag["pages"] != float64(10) {
		t.Errorf("pagination = %v, want offset 2 / page 2 / pages 10", pag)
	}
	// --offset without --limit on execution: still paginated.
	doc = getDocument(t, "get", "execution", "--offset", "18")
	units = doc["units"].([]any)
	if len(units) != 2 {
		t.Errorf("--offset 18 units = %d, want 2 (windowed to the end)", len(units))
	}
	pag = doc["pagination"].(map[string]any)
	if pag["offset"] != float64(18) || pag["limit"] != float64(2) {
		t.Errorf("pagination = %v, want offset 18 / limit 2 (total-offset)", pag)
	}
}

// TestGetPaginationFlagErrors: the pagination and container filter
// flags are validated deterministically — usage errors, exit 2, no
// JSON on stdout.
func TestGetPaginationFlagErrors(t *testing.T) {
	seedGetRepo(t, nil)
	cases := []struct {
		args []string
		want string
	}{
		// Value and combination rules.
		{[]string{"get", "execution", "--page", "2"}, "--page requires --limit"},
		{[]string{"get", "execution", "--limit", "2", "--offset", "1", "--page", "2"}, "--offset and --page are mutually exclusive"},
		{[]string{"get", "execution", "--limit", "0"}, "--limit must be >= 1"},
		{[]string{"get", "execution", "--offset", "-1"}, "--offset must be >= 0"},
		{[]string{"get", "execution", "--page", "0", "--limit", "2"}, "--page must be >= 1"},
		// Target applicability.
		{[]string{"get", "discovery", "--limit", "2"}, "pagination flags apply to the execution domain and containers targets only"},
		{[]string{"get", "eka-view-fixture/sto:alpha:1", "--limit", "2"}, "pagination flags require the execution domain or containers target"},
		{[]string{"get", "execution", "--active"}, "--active, --current and --container require the containers target"},
		{[]string{"get", "containers", "--active", "--container", "wave-0"}, "--active/--current and --container are mutually exclusive"},
		// Containers refuses the traversal and domain-only filter flags.
		{[]string{"get", "containers", "--upstream"}, "--upstream, --downstream and --timeline require an identity target (a form containing ':')"},
		{[]string{"get", "containers", "--type", "adr"}, "--type, --dimension and --phase require a domain target (one of the five Engineering Domains)"},
	}
	for _, c := range cases {
		code, out, errText := runIn(c.args)
		if code != 2 {
			t.Errorf("%v: exit = %d, want 2\nstdout: %s", c.args, code, out)
			continue
		}
		if out != "" {
			t.Errorf("%v: stdout must be empty, got %q", c.args, out)
		}
		if !strings.Contains(errText, c.want) {
			t.Errorf("%v: stderr must carry %q, got %q", c.args, c.want, errText)
		}
	}
}

// TestGetPaginationFlagErrorsWithoutWorkspace: the pagination flag
// rules are pure flag/target validation — they fire before any
// workspace access, exit 2.
func TestGetPaginationFlagErrorsWithoutWorkspace(t *testing.T) {
	t.Setenv("EKA_HOME", t.TempDir())
	chdirInto(t, t.TempDir())
	for _, c := range [][]string{
		{"get", "execution", "--limit", "0"},
		{"get", "containers", "--active", "--container", "wave-0"},
		{"get", "execution", "--active"},
	} {
		code, out, errText := runIn(c)
		if code != 2 {
			t.Errorf("%v: exit = %d, want 2 (usage error precedes workspace state)", c, code)
		}
		if out != "" {
			t.Errorf("%v: stdout must be empty, got %q", c, out)
		}
		if strings.Contains(errText, "workspace") {
			t.Errorf("%v: the usage error must not touch the workspace, got %q", c, errText)
		}
	}
}

// TestGetContainersDeterministicCLI: two runs of every containers and
// pagination query produce byte-identical stdout.
func TestGetContainersDeterministicCLI(t *testing.T) {
	seedGetRepo(t, nil)
	runOnce := func(args ...string) string {
		_, out, _ := runIn(args)
		return out
	}
	for _, args := range [][]string{
		{"get", "containers"},
		{"get", "containers", "--active"},
		{"get", "containers", "--container", "wave-1"},
		{"get", "containers", "--limit", "1"},
		{"get", "containers", "--limit", "1", "--page", "2"},
		{"get", "containers", "--offset", "1", "--compact"},
		{"get", "execution", "--limit", "3", "--page", "2"},
	} {
		if a, b := runOnce(args...), runOnce(args...); a != b {
			t.Errorf("output differs between runs for %v", args)
		}
	}
}

// TestGetContainersHelp: the long help documents the containers query,
// its fields, filters and pagination, and the example invocations.
func TestGetContainersHelp(t *testing.T) {
	for _, args := range [][]string{{"get", "-h"}, {"get", "--help"}} {
		code, text, _ := runIn(args)
		if code != 0 {
			t.Errorf("args %v: exit = %d, want 0", args, code)
		}
		for _, want := range []string{
			"containers",
			"canonicalForm",
			"plan",
			"items",
			"tickets",
			"startedAt",
			"endedAt",
			"containerState",
			"--active",
			"--current",
			"--container",
			"--offset",
			"--limit",
			"--page",
			"pagination",
			"eka get containers",
			"eka get containers --active",
			"eka get containers --container wave-6",
			"eka get execution --limit 10 --page 2",
			"eka get execution --limit 5 --offset 10",
		} {
			if !strings.Contains(text, want) {
				t.Errorf("args %v: help missing %q:\n%s", args, want, text)
			}
		}
	}
}
