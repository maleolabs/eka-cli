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
