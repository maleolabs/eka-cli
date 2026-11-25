package cmd

import (
	"strings"
	"testing"
)

// TestGetFreshnessFlag pins the --freshness retrieval option:
//
//   - absent, the output is byte-identical to the default document (the
//     additive contract);
//   - present, every document carries a freshness block naming the
//     status, and the block closes the document (appended last).
func TestGetFreshnessFlag(t *testing.T) {
	seedGetRepo(t, nil)
	target := "eka-view-fixture/adr:001-login-serialization:1"

	_, plain, _ := runIn([]string{"get", target})
	withFlag, out, errText := runIn([]string{"get", target, "--freshness"})
	if withFlag != 0 {
		t.Fatalf("exit = %d, want 0\nstdout: %s\nstderr: %s", withFlag, out, errText)
	}

	// The unmodified document must carry no freshness key at all.
	for _, key := range []string{`"freshness"`, `"validUntil"`, `"lastConfirmed"`} {
		if strings.Contains(plain, key) {
			t.Errorf("default output must not carry %s:\n%s", key, plain)
		}
	}

	doc := getDocument(t, "get", target, "--freshness")
	fresh, ok := doc["freshness"].(map[string]any)
	if !ok {
		t.Fatalf("--freshness must add the freshness block, got %v", doc["freshness"])
	}
	// The fixture declares no horizon: the honest third state.
	if fresh["status"] != "undeclared" {
		t.Errorf("status = %v, want undeclared for an unbounded artifact", fresh["status"])
	}
	if _, has := fresh["daysUntilExpiry"]; !has {
		t.Errorf("freshness must report daysUntilExpiry even when undeclared: %v", fresh)
	}

	// Appended last, after objectHash: the additive contract.
	if !strings.Contains(out, `"objectHash"`) {
		t.Fatalf("document lost its objectHash:\n%s", out)
	}
	if out[len(out)-1] != '\n' || !strings.Contains(out[strings.Index(out, `"freshness"`):], `"status"`) {
		t.Errorf("freshness must be the final block:\n%s", out)
	}
	if strings.Index(out, `"objectHash"`) > strings.Index(out, `"freshness"`) {
		t.Errorf("freshness must follow objectHash:\n%s", out)
	}
}

// TestGetFreshnessDomainForm: the same flag applies to the domain query
// and stamps every unit document of the collection.
func TestGetFreshnessDomainForm(t *testing.T) {
	seedGetRepo(t, nil)
	doc := getDocument(t, "get", "architecture", "--freshness", "--type", "adr")
	units := doc["units"].([]any)
	if len(units) == 0 {
		t.Fatal("fixture domain query returned no units")
	}
	for i, u := range units {
		m := u.(map[string]any)
		fresh, ok := m["freshness"].(map[string]any)
		if !ok {
			t.Fatalf("units[%d] missing freshness: %v", i, m)
		}
		if fresh["status"] != "undeclared" {
			t.Errorf("units[%d] status = %v, want undeclared", i, fresh["status"])
		}
	}
}

// TestGetFreshnessContainersRefused: the containers query carries no CKO
// documents, so the flag is an inapplicable combination (exit 2).
func TestGetFreshnessContainersRefused(t *testing.T) {
	seedGetRepo(t, nil)
	code, out, errText := runIn([]string{"get", "containers", "--freshness"})
	if code != 2 {
		t.Fatalf("exit = %d, want 2\nstdout: %s\nstderr: %s", code, out, errText)
	}
	if !strings.Contains(errText, "--freshness requires an identity or domain target") {
		t.Errorf("unexpected refusal message: %q", errText)
	}
}
