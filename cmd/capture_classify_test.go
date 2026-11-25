package cmd

import "testing"

// TestCaptureTransportPathsExcluded pins the boundary that transport
// snapshots are not evidence about a source change. Snapshot paths carry
// names like dec-.../unit.json, so letting them through let a snapshot
// filename decide the artifact type of a pure code change.
func TestCaptureTransportPathsExcluded(t *testing.T) {
	in := []string{
		"cmd/status.go",
		"exchange/snapshots/units/eka/dec-foo-v1/unit.json",
		"exchange/snapshots/units/eka/spec-bar-v1/content",
		"feature/x/.eka/state.json",
		"feedback/store.json",
		"dist/pkg.ekapkg",
	}
	got := excludeTransportPaths(in)
	if len(got) != 1 || got[0] != "cmd/status.go" {
		t.Fatalf("expected only the source file, got %v", got)
	}
}

// TestCaptureClassifyIgnoresSnapshotFilenames pins the defect that a change
// confined to Go sources was classified as a knowledge artifact because a
// snapshot path contained "spec"/"plan".
func TestCaptureClassifyIgnoresSnapshotFilenames(t *testing.T) {
	// After filtering, only source files remain.
	files := excludeTransportPaths([]string{
		"cmd/render_execution.go",
		"cmd/status.go",
		"exchange/snapshots/units/eka/dec-foo-v1/unit.json",
		"exchange/snapshots/units/eka/plan-bar-v1/unit.json",
	})
	got, _, _ := classifyGitHeuristic(files, "tidy render helpers", 0.6)
	if got == "spec" || got == "adr" || got == "plan" {
		t.Fatalf("source-only change must not become a knowledge artifact, got %q from %v", got, files)
	}
}

// TestCaptureDimensionForKnowledgeTokens pins that every knowledge token
// declares the dimension its standard token table assigns. Without this the
// published draft fails R6 and can never be published.
func TestCaptureDimensionForKnowledgeTokens(t *testing.T) {
	want := map[string]string{
		"vis": "intent", "str": "intent", "req": "requirements", "fnd": "research",
		"arc": "architecture", "adr": "decisions", "dec": "decisions",
		"spec": "specifications", "std": "standards", "gls": "vocabulary",
		"scp": "planning", "epc": "planning", "plan": "planning", "trc": "planning",
		"rvw": "quality", "run": "operations", "rel": "records", "shr": "records",
	}
	for token, dim := range want {
		got, ok := dimensionForToken(token)
		if !ok || got != dim {
			t.Errorf("dimensionForToken(%q) = %q,%v; want %q,true", token, got, ok, dim)
		}
	}
	// Operating tokens own no dimension and must not emit one.
	for _, token := range []string{"ctr", "tkt", "sto", "ts", "bug", "td", "ch", "spk", "ses", "cmt", "mbr"} {
		if dim, ok := dimensionForToken(token); ok {
			t.Errorf("operating token %q must not declare a dimension, got %q", token, dim)
		}
	}
}

// TestCaptureConfidenceNeverContradictsThreshold pins the corrected
// arithmetic: the previous branch assigned threshold-0.05, which is still
// below the threshold it was meant to satisfy.
func TestCaptureConfidenceNeverContradictsThreshold(t *testing.T) {
	const threshold = 0.6
	for _, files := range [][]string{
		{"a.go"},
		{"a.go", "b.go", "c.go"},
		{"docs/README.md"},
	} {
		_, _, c := classifyGitHeuristic(files, "", threshold)
		if c < captureConfidenceFloor {
			t.Errorf("confidence %v below floor %v for %v", c, captureConfidenceFloor, files)
		}
	}
}

// TestCapturePathEvidenceNotDowngradedByThreshold pins the regression found
// while verifying this fix: a change to specification/overview.md carries
// explicit path evidence, so a low confidence score must not silently
// downgrade it to a chore. The threshold gates guesses, not evidence.
func TestCapturePathEvidenceNotDowngradedByThreshold(t *testing.T) {
	cases := []struct {
		files []string
		want  string
	}{
		{[]string{"specification/overview.md"}, "spec"},
		{[]string{"adr/001-decision.md"}, "adr"},
		{[]string{"plans/wave1.md"}, "plan"},
	}
	for _, c := range cases {
		got, _, _ := classifyGitHeuristic(c.files, "add something short", 0.6)
		if got != c.want {
			t.Errorf("classifyGitHeuristic(%v) = %q, want %q (path evidence must win)", c.files, got, c.want)
		}
	}
}
