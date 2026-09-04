package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/maleolabs/eka-core/codegraph"
)

func TestCodeContextValidation(t *testing.T) {
	for _, args := range [][]string{
		{"code-context", "--depth", "bad"},
		{"code-context", "--level", "4"},
		{"code-context", "--limit", "0"},
	} {
		var out, errOut bytes.Buffer
		if code := Execute(args, strings.NewReader(""), &out, &errOut); code != exitUsage {
			t.Errorf("Execute(%v) code = %d, want %d", args, code, exitUsage)
		}
	}
}

func TestCodeContextCachePath(t *testing.T) {
	path, err := codeContextCachePath("/tmp/project")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) == "" || filepath.Dir(path) == "." {
		t.Fatalf("invalid cache path %q", path)
	}
	path2, _ := codeContextCachePath("/tmp/project")
	if path != path2 {
		t.Fatalf("cache path is not deterministic: %q != %q", path, path2)
	}
}

func TestCodeContextRepoRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "eka.yaml"), []byte("project: test\n"), 0600); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(root, "src", "pkg")
	if err := os.MkdirAll(nested, 0700); err != nil {
		t.Fatal(err)
	}
	got, err := codeContextRepoRoot(nested)
	if err != nil || got != root {
		t.Fatalf("codeContextRepoRoot = %q, %v; want %q", got, err, root)
	}
}

func TestCodeContextResponseJSON(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\nfunc Main() {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	idx, err := codegraph.Build(root)
	if err != nil {
		t.Fatal(err)
	}
	response, err := codegraph.Serve(idx, codegraph.Request{Focus: "Main", Depth: codegraph.DepthLocal, Level: 1, NoContent: true})
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(response)
	if err != nil || !json.Valid(data) {
		t.Fatalf("response JSON invalid: %v", err)
	}
}

func TestCodeContextContractLevelsDepthAndBounds(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"main.go":      "package main\nimport \"fmt\"\ntype Server struct{}\nfunc Run() { fmt.Println(\"run\") }\n",
		"main_test.go": "package main\nfunc TestRun() {}\n",
		"notes.txt":    "inventory only\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	idx, err := codegraph.Build(root)
	if err != nil {
		t.Fatal(err)
	}

	responses := make([]codegraph.Response, 4)
	for level := 0; level <= 3; level++ {
		responses[level], err = codegraph.Serve(idx, codegraph.Request{Focus: "Run", Depth: codegraph.DepthLocal, Level: level})
		if err != nil {
			t.Fatalf("level %d: %v", level, err)
		}
		if level == 0 && (len(responses[level].Symbols) != 0 || len(responses[level].Refs) != 0) {
			t.Fatalf("L0 leaked graph data: %+v", responses[level])
		}
		if level < 2 && len(responses[level].Refs) != 0 {
			t.Fatalf("L%d leaked refs: %+v", level, responses[level].Refs)
		}
		if level < 3 && len(responses[level].Units) > 0 && responses[level].Units[0].Content != "" {
			t.Fatalf("L%d leaked source content", level)
		}
	}
	if len(responses[1].Symbols) == 0 || len(responses[2].Refs) == 0 || responses[3].Units[0].Content == "" {
		t.Fatalf("L0-L3 contract incomplete: %+v", responses)
	}

	for _, depth := range []codegraph.Depth{codegraph.DepthLocal, codegraph.DepthDependency, codegraph.DepthEngineering} {
		got, err := codegraph.Serve(idx, codegraph.Request{Focus: "Run", Depth: depth, Level: 2, NoContent: true})
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Units) > codegraph.MaxUnits || len(got.Symbols) > codegraph.MaxSymbols || len(got.Refs) > codegraph.MaxUnits {
			t.Fatalf("depth %q exceeded bounds: %+v", depth, got)
		}
	}
	first, err := codegraph.Serve(idx, codegraph.Request{Focus: "Run", Depth: codegraph.DepthDependency, Level: 3, NoContent: true})
	if err != nil {
		t.Fatal(err)
	}
	second, err := codegraph.Serve(idx, codegraph.Request{Focus: "Run", Depth: codegraph.DepthDependency, Level: 3, NoContent: true})
	if err != nil {
		t.Fatal(err)
	}
	left, _ := json.Marshal(first)
	right, _ := json.Marshal(second)
	if !bytes.Equal(left, right) {
		t.Fatalf("non-deterministic output:\n%s\n%s", left, right)
	}
}

func TestCodeContextUnsupportedAndFallback(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("# notes\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "script.unknown"), []byte("whatever"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "app.go"), []byte("package app\nfunc Hello() {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	idx, err := codegraph.Build(root)
	if err != nil {
		t.Fatal(err)
	}
	// Unsupported language still appears as inventory unit, no symbols
	foundMD := false
	for _, f := range idx.Files {
		if f.Path == "README.md" && f.Language != "go" {
			foundMD = true
		}
		if f.Path == "README.md" && len(f.Symbols) != 0 {
			t.Fatalf("unsupported file leaked symbols: %+v", f)
		}
	}
	if !foundMD {
		t.Fatalf("unsupported file not inventoried: %+v", idx.Files)
	}
	// Symbol miss returns engineering fallback when depth engineering (bounded inventory), local returns empty focus
	missLocal, err := codegraph.Serve(idx, codegraph.Request{Focus: "DoesNotExist", Depth: codegraph.DepthLocal, Level: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(missLocal.Symbols) != 0 {
		t.Fatalf("miss must not return symbols on local: %+v", missLocal.Symbols)
	}
	missEng, err := codegraph.Serve(idx, codegraph.Request{Focus: "DoesNotExist", Depth: codegraph.DepthEngineering, Level: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(missEng.Units) == 0 {
		t.Fatalf("engineering miss must fallback to bounded inventory: %+v", missEng)
	}
	if missEng.Provenance.Method == "" || missEng.Provenance.Confidence == 0 {
		t.Fatalf("fallback must carry provenance/confidence: %+v", missEng.Provenance)
	}
	// RAG is never primary: provenance source is always local-index
	if missEng.Provenance.Source != "local-index" {
		t.Fatalf("RAG became primary source: %+v", missEng.Provenance)
	}
}

func TestCodeContextInvalidInputs(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("package a\nfunc A() {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	idx, err := codegraph.Build(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := codegraph.Serve(idx, codegraph.Request{Depth: "bad", Level: 1}); err == nil {
		t.Fatalf("bad depth must be refused")
	}
	if _, err := codegraph.Serve(idx, codegraph.Request{Depth: codegraph.DepthLocal, Level: 99}); err == nil {
		t.Fatalf("bad level must be refused")
	}
	// CLI validation layer also refuses
	for _, args := range [][]string{
		{"code-context", "--depth", "bad"},
		{"code-context", "--level", "-1"},
		{"code-context", "--level", "4"},
		{"code-context", "--limit", "0"},
		{"code-context", "--limit", "999"},
	} {
		var out, errOut bytes.Buffer
		if code := Execute(args, strings.NewReader(""), &out, &errOut); code != exitUsage {
			t.Errorf("Execute(%v) = %d, want %d (%q)", args, code, exitUsage, errOut.String())
		}
	}
}

func TestCodeContextParityAndDeterminism(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "eka.yaml"), []byte("version: 1\nproject: p\nname: p\nnamespace: p\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// Create many files to exercise bounds
	for i := 0; i < 40; i++ {
		content := "package p\nfunc Foo" + string(rune('A'+i%26)) + string(rune('0'+i%10)) + "() {}\n"
		if err := os.WriteFile(filepath.Join(root, "file"+string(rune('0'+i%10))+"_"+string(rune('A'+i))+".go"), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	idx, err := codegraph.Build(root)
	if err != nil {
		t.Fatal(err)
	}
	// Bounded output
	resp, err := codegraph.Serve(idx, codegraph.Request{Focus: "", Depth: codegraph.DepthEngineering, Level: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Symbols) > codegraph.MaxSymbols || len(resp.Units) > codegraph.MaxUnits || len(resp.Refs) > codegraph.MaxUnits {
		t.Fatalf("bounds violated: symbols %d units %d refs %d", len(resp.Symbols), len(resp.Units), len(resp.Refs))
	}
	// Determinism
	a, _ := json.Marshal(resp)
	bData, err := codegraph.Serve(idx, codegraph.Request{Focus: "", Depth: codegraph.DepthEngineering, Level: 2})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(bData)
	if !bytes.Equal(a, b) {
		t.Fatalf("non-deterministic serve")
	}
	// CLI compact vs pretty parse to same object and no stderr
	workDir := t.TempDir()
	// Run CLI via Execute chdir simulation: create eka.yaml in workDir and copy one file
	if err := os.WriteFile(filepath.Join(workDir, "eka.yaml"), []byte("version: 1\nproject: p2\nname: p2\nnamespace: p2\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "main.go"), []byte("package main\nfunc Hello() {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	orig, _ := os.Getwd()
	if err := os.Chdir(workDir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(orig)
	var out, errOut bytes.Buffer
	if code := Execute([]string{"code-context", "Hello", "--depth", "dependency", "--level", "2"}, strings.NewReader(""), &out, &errOut); code != 0 {
		t.Fatalf("CLI exit %d: %q", code, errOut.String())
	}
	if errOut.Len() != 0 {
		t.Fatalf("stderr must be empty on success, got %q", errOut.String())
	}
	var cliResp codegraph.Response
	if err := json.Unmarshal(out.Bytes(), &cliResp); err != nil {
		t.Fatalf("CLI JSON invalid: %v", err)
	}
	// Build same index and compare core serve matches CLI output (parity)
	idx2, _ := codegraph.Build(workDir)
	coreResp, _ := codegraph.Serve(idx2, codegraph.Request{Focus: "Hello", Depth: codegraph.DepthDependency, Level: 2})
	cliJSON, _ := json.Marshal(cliResp)
	coreJSON, _ := json.Marshal(coreResp)
	if !bytes.Equal(cliJSON, coreJSON) {
		t.Fatalf("CLI/MCP parity violated:\ncli: %s\ncore: %s", cliJSON, coreJSON)
	}
	// Cache invalidation: modify file, rebuild digest changes
	cachePath := filepath.Join(t.TempDir(), "cache.json")
	first, _, err := codegraph.LoadOrBuild(workDir, cachePath)
	if err != nil {
		t.Fatal(err)
	}
	firstDigest := first.Digest
	if err := os.WriteFile(filepath.Join(workDir, "main.go"), []byte("package main\nfunc Hello2() {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	second, hit, err := codegraph.LoadOrBuild(workDir, cachePath)
	if err != nil {
		t.Fatal(err)
	}
	if hit {
		t.Fatalf("cache must invalidate after file change")
	}
	if second.Digest == firstDigest {
		t.Fatalf("digest must change after edit")
	}
	// Token reduction: --no-content omits source but keeps identity
	noContentResp, _ := codegraph.Serve(idx2, codegraph.Request{Focus: "Hello", Depth: codegraph.DepthEngineering, Level: 3, NoContent: true})
	if len(noContentResp.Units) > 0 && noContentResp.Units[0].Content != "" {
		t.Fatalf("no-content must strip source")
	}
}
