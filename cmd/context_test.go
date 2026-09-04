package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/maleolabs/eka-core/runtime"
)

// This file tests the `eka context` command: the Context Engine
// interface of the CLI. The seeds reuse the cmd package test helpers
// (seedGetRepo, runIn, chdirInto, copyFixture) and the view "valid"
// fixture; the golden tests pin the exact bytes the engine produces.

// TestContextHumanRenders runs the human projection (no --json): exit
// 0, the context header with the subject, the depth row, and the
// summary — with an empty stderr.
func TestContextHumanRenders(t *testing.T) {
	seedGetRepo(t, nil)
	code, out, errText := runIn([]string{"context", "eka-view-fixture/sto:alpha"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", code, out, errText)
	}
	if errText != "" {
		t.Errorf("stderr must be empty on success, got %q", errText)
	}
	for _, want := range []string{
		"Context",                    // the header object kind
		"eka-view-fixture/sto:alpha", // the subject form
		"Depth",                      // the header depth row
		"Summary:",                   // the summary block
	} {
		if !strings.Contains(out, want) {
			t.Errorf("human render missing %q, got:\n%s", want, out)
		}
	}
}

// TestContextJSONGolden: --depth local --json emits the pinned Context
// Object (eka-view-fixture/adr:001-login-serialization) byte-exactly —
// the golden captured from the engine — plus the trailing newline, and
// nothing on stderr. The --compact form parses to the same object.
func TestContextJSONGolden(t *testing.T) {
	seedGetRepo(t, nil)
	code, out, errText := runIn([]string{"context", "eka-view-fixture/adr:001-login-serialization", "--depth", "local", "--json"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", code, out, errText)
	}
	if errText != "" {
		t.Errorf("stderr must be empty on success, got %q", errText)
	}
	want := `{
  "schema": "eka-context-v1",
  "kind": "context",
  "focus": {
    "identity": {
      "namespace": "eka-view-fixture",
      "type": "adr",
      "id": "001-login-serialization",
      "instanceVersion": 1
    },
    "canonicalForm": "eka-view-fixture/adr:001-login-serialization:1",
    "lineForm": "eka-view-fixture/adr:001-login-serialization",
    "engineeringDomain": "Architecture",
    "stratum": 2,
    "revision": 1,
    "stateVector": {
      "contentState": "accepted",
      "existenceState": "active"
    },
    "classification": {
      "dimension": "decisions",
      "domain": "Architecture"
    },
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
  },
  "depth": "local",
  "summary": {
    "focus": 1,
    "units": 1,
    "sections": 0,
    "history": 1
  },
  "strata": [
    {
      "stratum": 2,
      "domain": "Architecture",
      "units": [
        {
          "canonicalForm": "eka-view-fixture/adr:001-login-serialization:1",
          "lineForm": "eka-view-fixture/adr:001-login-serialization",
          "type": "adr",
          "id": "001-login-serialization",
          "domain": "Architecture",
          "stratum": 2,
          "state": "accepted",
          "objectHash": "2fc697ce15d080fdb9fc53eba70ef313eeef03c93064fec0f490d6b99cf96a36"
        }
      ]
    }
  ],
  "sections": {
    "history": [
      {
        "canonicalForm": "eka-view-fixture/adr:001-login-serialization:1",
        "instanceVersion": 1,
        "revision": 1,
        "objectHash": "2fc697ce15d080fdb9fc53eba70ef313eeef03c93064fec0f490d6b99cf96a36",
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
        ]
      }
    ]
  }
}
`
	if out != want {
		t.Errorf("stdout must be the pinned golden object:\ngot:\n%s\nwant:\n%s", out, want)
	}
	if !strings.HasSuffix(out, "\n") {
		t.Error("stdout must end in a single trailing newline")
	}
	// The compact form carries the same object as a single line.
	code, compact, errText := runIn([]string{"context", "eka-view-fixture/adr:001-login-serialization", "--depth", "local", "--compact"})
	if code != 0 {
		t.Fatalf("compact: exit = %d, stderr %q", code, errText)
	}
	var a, b any
	if err := json.Unmarshal([]byte(out), &a); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(compact), &b); err != nil {
		t.Fatalf("compact must be a parseable single-line object: %v\n%q", err, compact)
	}
	if fmt.Sprintf("%v", a) != fmt.Sprintf("%v", b) {
		t.Error("--compact must parse to the same object as --json")
	}
}

// TestContextDeterministic: two runs produce byte-identical output.
func TestContextDeterministic(t *testing.T) {
	seedGetRepo(t, nil)
	_, out1, errText := runIn([]string{"context", "eka-view-fixture/ctr:wave-1", "--json"})
	if errText != "" {
		t.Fatalf("stderr must be empty, got %q", errText)
	}
	_, out2, _ := runIn([]string{"context", "eka-view-fixture/ctr:wave-1", "--json"})
	if out1 != out2 {
		t.Error("two runs must produce byte-identical context objects")
	}
}

// TestContextDepthFlag: --depth engineering constructs the bounded
// closure (exit 0); an unknown depth token is a usage error (exit 2)
// listing the three depths.
func TestContextDepthFlag(t *testing.T) {
	seedGetRepo(t, nil)
	code, out, errText := runIn([]string{"context", "eka-view-fixture/sto:alpha", "--depth", "engineering"})
	if code != 0 {
		t.Fatalf("engineering depth: exit = %d, stderr %q", code, errText)
	}
	if !strings.Contains(out, "engineering") {
		t.Errorf("engineering depth must render the depth, got:\n%s", out)
	}
	code, _, errText = runIn([]string{"context", "eka-view-fixture/sto:alpha", "--depth", "bogus"})
	if code != 2 {
		t.Fatalf("bogus depth: exit = %d, want 2", code)
	}
	for _, want := range []string{"local", "dependency", "engineering"} {
		if !strings.Contains(errText, want) {
			t.Errorf("depth usage error must list %q, got %q", want, errText)
		}
	}
}

// TestContextNoWorkspaceExitsOne: no EKA workspace — the refusal
// message and exit 1 (never created).
func TestContextNoWorkspaceExitsOne(t *testing.T) {
	t.Setenv("EKA_HOME", t.TempDir())
	chdirInto(t, t.TempDir())
	code, out, errText := runIn([]string{"context", "eka-view-fixture/sto:alpha"})
	if code != 1 {
		t.Fatalf("exit = %d, want 1\nstdout: %s\nstderr: %s", code, out, errText)
	}
	if !strings.Contains(errText, "no EKA workspace") {
		t.Errorf("stderr must carry the workspace refusal, got %q", errText)
	}
	if out != "" {
		t.Errorf("stdout must be empty on refusal, got %q", out)
	}
}

// TestContextNoRepositoryExitsOne: a workspace exists but the current
// directory is not an EKA repository (no eka.yaml) — exit 1.
func TestContextNoRepositoryExitsOne(t *testing.T) {
	t.Setenv("EKA_HOME", t.TempDir())
	r, err := runtime.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	r.Close()
	chdirInto(t, t.TempDir())
	code, _, errText := runIn([]string{"context", "eka-view-fixture/sto:alpha"})
	if code != 1 {
		t.Fatalf("exit = %d, want 1, stderr %q", code, errText)
	}
	if !strings.Contains(errText, "not an EKA repository") {
		t.Errorf("stderr must carry the repository refusal, got %q", errText)
	}
}

// TestContextUnregisteredRepoExitsOne: the directory carries eka.yaml
// but the repository is not registered in the workspace — exit 1.
func TestContextUnregisteredRepoExitsOne(t *testing.T) {
	t.Setenv("EKA_HOME", t.TempDir())
	r, err := runtime.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	r.Close()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "eka.yaml"),
		[]byte("version: 1\nproject: ghost\nname: ghost\nnamespace: ghost\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	chdirInto(t, dir)
	code, _, errText := runIn([]string{"context", "eka-view-fixture/sto:alpha"})
	if code != 1 {
		t.Fatalf("exit = %d, want 1, stderr %q", code, errText)
	}
	if !strings.Contains(errText, "not registered in the EKA workspace") {
		t.Errorf("stderr must carry the registration refusal, got %q", errText)
	}
}

// TestContextDomainTokenRefused: a domain token and the containers
// query are not subjects — usage error (exit 2) listing the identity
// grammar, checked BEFORE any workspace access.
func TestContextDomainTokenRefused(t *testing.T) {
	for _, subject := range []string{"execution", "architecture", "containers"} {
		code, _, errText := runIn([]string{"context", subject})
		if code != 2 {
			t.Errorf("context %s: exit = %d, want 2", subject, code)
		}
		if !strings.Contains(errText, "not a knowledge identity") {
			t.Errorf("context %s: stderr must list the identity grammar, got %q", subject, errText)
		}
	}
}

// TestContextNoArgExitsTwo: the bare command is a usage error with the
// subject grammar (exit 2).
func TestContextNoArgExitsTwo(t *testing.T) {
	code, _, errText := runIn([]string{"context"})
	if code != 2 {
		t.Fatalf("exit = %d, want 2, stderr %q", code, errText)
	}
	if !strings.Contains(errText, "usage: eka context <subject>") {
		t.Errorf("stderr must carry the usage summary, got %q", errText)
	}
}

// TestContextUnknownIdentityExitsTwo: a well-formed but unresolvable
// identity is exit 2 (the resolver not-found path).
func TestContextUnknownIdentityExitsTwo(t *testing.T) {
	seedGetRepo(t, nil)
	code, out, errText := runIn([]string{"context", "eka-view-fixture/sto:ghost"})
	if code != 2 {
		t.Fatalf("exit = %d, want 2\nstdout: %s\nstderr: %s", code, out, errText)
	}
	if !strings.Contains(errText, "no knowledge object matches") {
		t.Errorf("stderr must carry the not-found error, got %q", errText)
	}
}

// TestContextIssueNumber: "#<n>" resolves to the line's qualified
// form through the issue-number path (the per-group counters make a
// bare number ambiguous when several groups carry it — the test picks
// an unambiguous fixture number via the Runtime) and constructs the
// context of the right focus.
func TestContextIssueNumber(t *testing.T) {
	seedGetRepo(t, nil)
	r, err := runtime.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	// Find a number that resolves unambiguously across the per-group
	// counters (work items, tickets and notes count independently):
	// the pinned fixture lines are probed in order, the first with a
	// single LineByNumber match wins.
	number, wantForm := 0, ""
	for _, line := range []struct{ typeToken, id string }{
		{"tkt", "sto-alpha"}, {"tkt", "sto-beta"}, {"tkt", "ts-gamma"},
		{"bug", "delta"}, {"cmt", "delta-implementation"},
	} {
		n, err := r.Knowledge.NumberForLine("eka-view-fixture", "eka-view-fixture", line.typeToken, line.id)
		if err != nil || n == 0 {
			continue
		}
		matches, err := r.Knowledge.LineByNumber("eka-view-fixture", n)
		if err != nil {
			continue
		}
		if len(matches) == 1 {
			number, wantForm = n, matches[0].LineForm()
			break
		}
	}
	r.Close()
	if number == 0 {
		t.Fatal("no unambiguous issue number found in the fixture")
	}
	code, out, errText := runIn([]string{"context", fmt.Sprintf("#%d", number), "--json", "--depth", "local"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", code, out, errText)
	}
	var doc struct {
		Focus struct {
			CanonicalForm string `json:"canonicalForm"`
			LineForm      string `json:"lineForm"`
			Number        int    `json:"number"`
		} `json:"focus"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Focus.LineForm != wantForm {
		t.Errorf("focus line = %s, want %s", doc.Focus.LineForm, wantForm)
	}
	if doc.Focus.Number != number {
		t.Errorf("focus number = %d, want %d", doc.Focus.Number, number)
	}
}

// TestContextNoContent: --json --no-content strips the focus content
// payload — the "content" key is absent from the JSON.
func TestContextNoContent(t *testing.T) {
	seedGetRepo(t, nil)
	code, out, errText := runIn([]string{"context", "eka-view-fixture/adr:001-login-serialization", "--json", "--no-content"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0, stderr %q", code, errText)
	}
	var doc struct {
		Focus map[string]any `json:"focus"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatal(err)
	}
	if _, ok := doc.Focus["content"]; ok {
		t.Errorf("focus must not carry content with --no-content, got %v", doc.Focus["content"])
	}
	if doc.Focus["objectHash"] == "" {
		t.Error("focus must still carry the object hash")
	}
}
