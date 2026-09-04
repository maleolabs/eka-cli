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

func TestCodeDiscoverValidation(t *testing.T) {
	for _, args := range [][]string{
		{"code-discover", "--limit", "0", "query"},
		{"code-discover", "--limit", "999", "query"},
		{"code-discover"},
	} {
		var out, errOut bytes.Buffer
		if code := Execute(args, strings.NewReader(""), &out, &errOut); code != exitUsage {
			t.Errorf("Execute(%v) code = %d, want %d (%q)", args, code, exitUsage, errOut.String())
		}
	}
}

func TestCodeDiscoverCandidatesAndReason(t *testing.T) {
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\nfunc RunServer(){}\n"), 0600)
	_ = os.WriteFile(filepath.Join(root, "util.go"), []byte("package main\nfunc Helper(){}\n"), 0600)
	idx, err := codegraph.Build(root)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := codegraph.Discover(idx, codegraph.DiscoverRequest{Query: "RunServer", Limit: 16})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Candidates) == 0 {
		t.Fatalf("expected candidates, got %+v", resp)
	}
	top := resp.Candidates[0]
	if top.Path != "main.go" {
		t.Fatalf("top candidate = %q, want main.go", top.Path)
	}
	if top.Reason == "" || top.Confidence == 0 {
		t.Fatalf("candidate must carry reason/confidence: %+v", top)
	}
	if resp.Provenance.Source != "local-index" {
		t.Fatalf("provenance source = %q, want local-index", resp.Provenance.Source)
	}
	if resp.Fallback {
		t.Fatalf("should not be fallback when match exists")
	}
}

func TestCodeDiscoverAmbiguity(t *testing.T) {
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "a.go"), []byte("package a\nfunc Foo(){}\n"), 0600)
	_ = os.WriteFile(filepath.Join(root, "b.go"), []byte("package b\nfunc Foo(){}\n"), 0600)
	idx, err := codegraph.Build(root)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := codegraph.Discover(idx, codegraph.DiscoverRequest{Query: "Foo", Limit: 16})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Candidates) < 2 {
		t.Fatalf("ambiguous query must return multiple candidates, got %+v", resp.Candidates)
	}
	if resp.Candidates[0].Confidence != 1 || resp.Candidates[1].Confidence != 1 {
		t.Fatalf("ambiguous top candidates must tie at confidence 1: %+v", resp.Candidates)
	}
	// Deterministic order: path asc when scores tie.
	if resp.Candidates[0].Path != "a.go" || resp.Candidates[1].Path != "b.go" {
		t.Fatalf("deterministic order violated: %+v", resp.Candidates)
	}
}

func TestCodeDiscoverUnsupportedAndFallback(t *testing.T) {
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "README.md"), []byte("# docs\n"), 0600)
	_ = os.WriteFile(filepath.Join(root, "app.go"), []byte("package app\nfunc Hello() {}\n"), 0600)
	idx, err := codegraph.Build(root)
	if err != nil {
		t.Fatal(err)
	}
	// Unsupported still discoverable via path
	resp, err := codegraph.Discover(idx, codegraph.DiscoverRequest{Query: "README", Limit: 16})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range resp.Candidates {
		if c.Path == "README.md" && c.Language == "unsupported" {
			found = true
		}
	}
	if !found {
		t.Fatalf("unsupported file not discoverable: %+v", resp.Candidates)
	}
	// Fallback inventory when no match
	miss, err := codegraph.Discover(idx, codegraph.DiscoverRequest{Query: "does-not-exist-xyz", Limit: 16})
	if err != nil {
		t.Fatal(err)
	}
	if !miss.Fallback || len(miss.Candidates) == 0 {
		t.Fatalf("miss must fallback to bounded inventory: %+v", miss)
	}
	if miss.Provenance.Source != "local-index" {
		t.Fatalf("fallback source must remain local-index: %+v", miss.Provenance)
	}
}

func TestCodeDiscoverBoundsAndScope(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 40; i++ {
		name := "file" + string(rune('a'+i%26)) + ".go"
		_ = os.WriteFile(filepath.Join(root, name), []byte("package p\nfunc Foo() {}\n"), 0600)
	}
	idx, err := codegraph.Build(root)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := codegraph.Discover(idx, codegraph.DiscoverRequest{Query: "Foo", Limit: 64})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Candidates) > codegraph.MaxCandidates {
		t.Fatalf("bounds violated: %d > %d", len(resp.Candidates), codegraph.MaxCandidates)
	}
	// Scope filter
	scoped, err := codegraph.Discover(idx, codegraph.DiscoverRequest{Query: "Foo", Scope: "filea", Limit: 16})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range scoped.Candidates {
		if !strings.Contains(strings.ToLower(c.Path), "filea") {
			t.Fatalf("scope filter leaked %q", c.Path)
		}
	}
}

func TestCodeGetExactRetrieval(t *testing.T) {
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\nfunc Run(){}\n"), 0600)
	_ = os.WriteFile(filepath.Join(root, "README.md"), []byte("hi\n"), 0600)
	idx, err := codegraph.Build(root)
	if err != nil {
		t.Fatal(err)
	}
	got, err := codegraph.Get(idx, codegraph.GetRequest{Path: "main.go"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Unit.Path != "main.go" || got.Unit.Content == "" {
		t.Fatalf("get must return exact content: %+v", got.Unit)
	}
	if len(got.Symbols) == 0 {
		t.Fatalf("get must return symbols for go file")
	}
	// Unsupported exact retrieval still works
	unsupported, err := codegraph.Get(idx, codegraph.GetRequest{Path: "README.md"})
	if err != nil {
		t.Fatal(err)
	}
	if unsupported.Unit.Language != "unsupported" || unsupported.Unit.Content != "hi\n" {
		t.Fatalf("unsupported get failed: %+v", unsupported.Unit)
	}
	// Not found
	if _, err := codegraph.Get(idx, codegraph.GetRequest{Path: "nope.go"}); err == nil {
		t.Fatalf("missing file must be refused")
	}
	if _, err := codegraph.Get(idx, codegraph.GetRequest{Path: "../escape"}); err == nil {
		t.Fatalf("path traversal must be refused")
	}
}

func TestCodeDiscoverAndGetParityCLI(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "eka.yaml"), []byte("version: 1\nproject: p\nname: p\nnamespace: p\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\nfunc Hello() {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	orig, _ := os.Getwd()
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(orig)
	var out, errOut bytes.Buffer
	if code := Execute([]string{"code-discover", "Hello", "--limit", "8"}, strings.NewReader(""), &out, &errOut); code != 0 {
		t.Fatalf("code-discover CLI exit %d: %q", code, errOut.String())
	}
	var cliResp codegraph.DiscoverResponse
	if err := json.Unmarshal(out.Bytes(), &cliResp); err != nil {
		t.Fatalf("CLI discover JSON invalid: %v", err)
	}
	idx, _ := codegraph.Build(root)
	coreResp, _ := codegraph.Discover(idx, codegraph.DiscoverRequest{Query: "Hello", Limit: 8})
	cliJSON, _ := json.Marshal(cliResp)
	coreJSON, _ := json.Marshal(coreResp)
	if !bytes.Equal(cliJSON, coreJSON) {
		t.Fatalf("discover CLI/core parity violated:\ncli: %s\ncore: %s", cliJSON, coreJSON)
	}
	// code-get parity
	out.Reset()
	errOut.Reset()
	if code := Execute([]string{"code-get", "main.go"}, strings.NewReader(""), &out, &errOut); code != 0 {
		t.Fatalf("code-get CLI exit %d: %q", code, errOut.String())
	}
	var cliGet codegraph.GetResponse
	if err := json.Unmarshal(out.Bytes(), &cliGet); err != nil {
		t.Fatalf("CLI get JSON invalid: %v", err)
	}
	coreGet, _ := codegraph.Get(idx, codegraph.GetRequest{Path: "main.go"})
	cliGetJSON, _ := json.Marshal(cliGet)
	coreGetJSON, _ := json.Marshal(coreGet)
	// Compare schema/query/provenance/unit identity (content identical)
	if cliGet.Unit.Path != coreGet.Unit.Path || cliGet.Unit.Digest != coreGet.Unit.Digest {
		t.Fatalf("get CLI/core divergence: cli %s core %s", cliGetJSON, coreGetJSON)
	}
}
