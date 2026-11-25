package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/maleolabs/eka-core/metadata"
	"github.com/maleolabs/eka-core/runtime"
	"github.com/maleolabs/eka-core/workspace"
)

func shrAuthoringEnv(t *testing.T, ns string) (*workspace.Workspace, string) {
	t.Helper()
	gitIdentityEnv(t, "test-agent")
	t.Setenv("EKA_HOME", t.TempDir())
	w, err := workspace.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.Close() })
	repoDir := t.TempDir()
	proj := filepath.Base(repoDir)
	writeEkaYAML(t, repoDir, proj, proj, ns)
	m := metadata.Metadata{Version: 1, Project: proj, Name: proj, Namespace: ns}
	if _, _, _, err := w.RegisterRepoMetadata(repoDir, m); err != nil {
		t.Fatal(err)
	}
	t.Chdir(repoDir)
	return w, repoDir
}

func dimensionForType(typeToken string) string {
	switch typeToken {
	case "adr", "dec":
		return "decisions"
	case "req":
		return "requirements"
	case "spec":
		return "specifications"
	case "arc":
		return "architectures"
	case "vis":
		return "visions"
	case "str":
		return "strategies"
	case "std":
		return "standards"
	case "gls":
		return "glossary"
	case "fnd":
		return "findings"
	case "run", "rel":
		return "records"
	default:
		return ""
	}
}

func seedSourceCKO(t *testing.T, w *workspace.Workspace, repoDir, ns, typeToken, id string, content map[string]any) string {
	t.Helper()
	var bodyPath string
	if content != nil {
		bodyPath = filepath.Join(t.TempDir(), "src.json")
		b, _ := json.Marshal(content)
		if err := os.WriteFile(bodyPath, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	args := []string{"new", ns + "/" + typeToken + ":" + id}
	if bodyPath != "" {
		args = append(args, "--content-file", bodyPath)
	}
	if dim := dimensionForType(typeToken); dim != "" {
		args = append(args, "--dimension", dim)
	}
	if code, out, errText := runIn(args); code != 0 {
		t.Fatalf("seed new %s:%s: %d stdout:%s stderr:%s", typeToken, id, code, out, errText)
	}
	if code, out, errText := runIn([]string{"publish", ns + "/" + typeToken + ":" + id}); code != 0 {
		t.Fatalf("seed publish %s:%s: %d stdout:%s stderr:%s", typeToken, id, code, out, errText)
	}
	// Resolve canonical form to return.
	r, err := runtime.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	unit, ok, err := r.Resolver.Resolve(ns + "/" + typeToken + ":" + id)
	if err != nil || !ok {
		t.Fatalf("resolve seeded %s: %v %v", id, err, ok)
	}
	return unit.CanonicalIdentityForm
}

func TestShrBuildRequiresLevel(t *testing.T) {
	w, _ := shrAuthoringEnv(t, "eka")
	seedSourceCKO(t, w, mustAbs(t, "."), "eka", "adr", "src-1", map[string]any{
		"context": "ctx", "decision": "dec", "consequences": "cons", "alternativesConsidered": "alt",
	})
	code, _, errText := runIn([]string{"shr", "build", "eka/adr:src-1"})
	if code != 2 {
		t.Errorf("missing level exit = %d, want 2", code)
	}
	if !strings.Contains(errText, "--level is required") {
		t.Errorf("stderr must mention level required, got %q", errText)
	}
	if code == 0 {
		t.Error("should not create draft when level missing")
	}
	project := projectOf(t, w, mustAbs(t, "."))
	_ = project
	// No draft should be created for share-...
}

func TestShrBuildInvalidLevel(t *testing.T) {
	w, _ := shrAuthoringEnv(t, "eka")
	seedSourceCKO(t, w, mustAbs(t, "."), "eka", "adr", "src-1", map[string]any{
		"context": "ctx", "decision": "dec", "consequences": "cons", "alternativesConsidered": "alt",
	})
	code, _, errText := runIn([]string{"shr", "build", "eka/adr:src-1", "--level", "L3"})
	if code != 2 {
		t.Errorf("invalid level exit = %d, want 2", code)
	}
	if !strings.Contains(errText, "--level must be") {
		t.Errorf("stderr must mention level enum, got %q", errText)
	}
}

func TestShrBuildL0CreatesDraft(t *testing.T) {
	w, _ := shrAuthoringEnv(t, "eka")
	sourceForm := seedSourceCKO(t, w, mustAbs(t, "."), "eka", "adr", "src-l0", map[string]any{
		"context": "ctx", "decision": "dec", "consequences": "cons", "alternativesConsidered": "alt",
	})
	project := projectOf(t, w, mustAbs(t, "."))
	code, text, errText := runIn([]string{"shr", "build", sourceForm, "--level", "L0", "--id", "share-l0-test"})
	if code != 0 {
		t.Fatalf("shr build L0 exit = %d\nstdout: %s\nstderr: %s", code, text, errText)
	}
	data, err := os.ReadFile(draftFile(t, w, project, "shr", "share-l0-test"))
	if err != nil {
		t.Fatalf("shr draft not created: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("draft json: %v", err)
	}
	content := doc["content"].(map[string]any)
	for _, k := range []string{"title", "description", "level", "provenance", "sourceHash", "purpose", "content"} {
		if _, ok := content[k]; !ok {
			t.Errorf("L0 content missing %q", k)
		}
	}
	if content["level"] != "L0" {
		t.Errorf("level = %v, want L0", content["level"])
	}
	if content["provenance"] != "extracted" {
		t.Errorf("provenance = %v, want extracted", content["provenance"])
	}
	if _, ok := content["summary"]; ok {
		t.Error("L0 must not contain summary")
	}
	if _, ok := content["snapshot"]; ok {
		t.Error("L0 must not contain snapshot")
	}
	// Relationships
	rels := doc["relationships"].(map[string]any)
	derives, ok := rels["derivesFrom"].([]any)
	if !ok || len(derives) == 0 {
		t.Fatalf("derivesFrom missing: %v", rels)
	}
	if derives[0] != sourceForm {
		t.Errorf("derivesFrom = %v, want %s", derives[0], sourceForm)
	}
	// Validate draft
	if code, _, errText := runIn([]string{"draft", "validate", "eka/shr:share-l0-test"}); code != 0 {
		t.Errorf("L0 draft validate failed: %d %s", code, errText)
	}
}

func TestShrBuildL1AddsSummary(t *testing.T) {
	w, _ := shrAuthoringEnv(t, "eka")
	sourceForm := seedSourceCKO(t, w, mustAbs(t, "."), "eka", "req", "src-l1", map[string]any{
		"purpose": "p1", "content": "c1",
	})
	project := projectOf(t, w, mustAbs(t, "."))
	if code, _, errText := runIn([]string{"shr", "build", sourceForm, "--level", "L1", "--id", "share-l1-test"}); code != 0 {
		t.Fatalf("L1 build failed: %d %s", code, errText)
	}
	data, _ := os.ReadFile(draftFile(t, w, project, "shr", "share-l1-test"))
	var doc map[string]any
	json.Unmarshal(data, &doc)
	content := doc["content"].(map[string]any)
	if _, ok := content["summary"]; !ok {
		t.Error("L1 must contain summary")
	}
	if content["sourceHash"] == "" {
		t.Error("sourceHash must be non-empty")
	}
	if _, ok := content["snapshot"]; ok {
		t.Error("L1 must not contain snapshot")
	}
	if code, _, errText := runIn([]string{"draft", "validate", "eka/shr:share-l1-test"}); code != 0 {
		t.Errorf("L1 draft validate failed: %d %s", code, errText)
	}
}

func TestShrBuildL2AddsSnapshot(t *testing.T) {
	w, _ := shrAuthoringEnv(t, "eka")
	srcContent := map[string]any{"purpose": "p2", "content": "c2 full content for snapshot"}
	sourceForm := seedSourceCKO(t, w, mustAbs(t, "."), "eka", "spec", "src-l2", srcContent)
	project := projectOf(t, w, mustAbs(t, "."))
	if code, _, errText := runIn([]string{"shr", "build", sourceForm, "--level", "L2", "--id", "share-l2-test"}); code != 0 {
		t.Fatalf("L2 build failed: %d %s", code, errText)
	}
	data, _ := os.ReadFile(draftFile(t, w, project, "shr", "share-l2-test"))
	var doc map[string]any
	json.Unmarshal(data, &doc)
	content := doc["content"].(map[string]any)
	if _, ok := content["snapshot"]; !ok {
		t.Error("L2 must contain snapshot")
	}
	if _, ok := content["summary"]; !ok {
		t.Error("L2 must contain summary")
	}
	// sourceHash must pin actual object hash
	r, _ := runtime.Open()
	unit, _, _ := r.Resolver.Resolve(sourceForm)
	r.Close()
	if content["sourceHash"] != unit.Digest {
		t.Errorf("sourceHash mismatch: got %v want %s", content["sourceHash"], unit.Digest)
	}
	if code, _, errText := runIn([]string{"draft", "validate", "eka/shr:share-l2-test"}); code != 0 {
		t.Errorf("L2 draft validate failed: %d %s", code, errText)
	}
	// Publish and verify get + bundle self-contained via get/validate
	if code, _, errText := runIn([]string{"publish", "eka/shr:share-l2-test"}); code != 0 {
		t.Fatalf("publish L2 failed: %d %s", code, errText)
	}
	if code, text, errText := runIn([]string{"get", "eka/shr:share-l2-test:1"}); code != 0 {
		t.Fatalf("get after publish failed: %d %s %s", code, text, errText)
	} else {
		var doc map[string]any
		if err := json.Unmarshal([]byte(text), &doc); err != nil {
			t.Fatalf("get output not json: %v", err)
		}
		if _, ok := doc["content"]; !ok {
			t.Error("get shr must contain content")
		}
	}
	// Bundle self-contained check: get with upstream should include snapshot and still be valid JSON
	if code, text, errText := runIn([]string{"get", "eka/shr:share-l2-test:1", "--upstream"}); code != 0 {
		t.Fatalf("get --upstream failed: %d %s %s", code, text, errText)
	} else if !strings.Contains(text, "snapshot") {
		t.Errorf("L2 get --upstream should contain snapshot, got %s", text[:500])
	}
	// Validate repo still passes (file tree unchanged, shr is workspace-native)
	if code, _, errText := runIn([]string{"validate"}); code != 0 {
		t.Fatalf("validate after publish failed: %d %s", code, errText)
	}
}

func TestShrBuildSourceNotFound(t *testing.T) {
	shrAuthoringEnv(t, "eka")
	code, _, errText := runIn([]string{"shr", "build", "eka/adr:ghost", "--level", "L0"})
	if code != 2 {
		t.Errorf("nonexistent source exit = %d, want 2", code)
	}
	if !strings.Contains(errText, "not found") {
		t.Errorf("stderr must mention not found, got %q", errText)
	}
}

func TestShrBuildDefaultID(t *testing.T) {
	w, _ := shrAuthoringEnv(t, "eka")
	sourceForm := seedSourceCKO(t, w, mustAbs(t, "."), "eka", "adr", "src-def", map[string]any{
		"context": "c", "decision": "d", "consequences": "co", "alternativesConsidered": "a",
	})
	if code, _, errText := runIn([]string{"shr", "build", sourceForm, "--level", "L0"}); code != 0 {
		t.Fatalf("build with default id failed: %d %s", code, errText)
	}
	project := projectOf(t, w, mustAbs(t, "."))
	// Expect generated id share-src-def-l0
	expected := "share-src-def-l0"
	if _, err := os.Stat(draftFile(t, w, project, "shr", expected)); err != nil {
		t.Errorf("default id draft not found at %s: %v", expected, err)
	}
}
