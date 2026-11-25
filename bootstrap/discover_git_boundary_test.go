package bootstrap

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDetectGitRepoIgnoresRepositoryAtOrAboveHome pins the boundary of the
// repository walk: a ".git" at or above the home directory belongs to the
// user's environment, never to the target being scaffolded. Without the
// boundary a scratch directory under /tmp reports IsGitRepo=true whenever the
// home directory is itself a checkout, which made eka init believe a
// repository already existed and broke the bootstrap suite on such machines.
func TestDetectGitRepoIgnoresRepositoryAtOrAboveHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// The user's environment: a repository rooted at home.
	if err := os.Mkdir(filepath.Join(home, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	// A scratch target OUTSIDE home must not inherit it. t.TempDir() may
	// live under the real home, so use a directory outside it explicitly.
	scratch, err := os.MkdirTemp("/tmp", "eka-scratch-")
	if err != nil {
		t.Skipf("cannot create scratch dir outside home: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(scratch) })

	d := &Discovery{AbsTarget: scratch}
	detectGitRepo(d)
	if d.IsGitRepo {
		t.Errorf("scratch dir must not inherit the repository at home: %+v", d)
	}
	if d.GitRoot != "" {
		t.Errorf("GitRoot must be empty, got %q", d.GitRoot)
	}
}

// TestDetectGitRepoFindsNestedRepository keeps the legitimate case intact: a
// checkout nested inside another checkout resolves to its own root, and a
// directory below the home boundary still finds the repository above it.
func TestDetectGitRepoFindsNestedRepository(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	outer := filepath.Join(home, "project")
	inner := filepath.Join(outer, "module")
	if err := os.MkdirAll(inner, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(outer, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	// A directory inside the repository finds it.
	d := &Discovery{AbsTarget: inner}
	detectGitRepo(d)
	if !d.IsGitRepo || d.GitRoot != outer {
		t.Fatalf("nested dir must resolve to its repository: %+v (want root %q)", d, outer)
	}

	// A nested repository closer to the target wins.
	if err := os.Mkdir(filepath.Join(inner, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	d = &Discovery{AbsTarget: inner}
	detectGitRepo(d)
	if !d.IsGitRepo || d.GitRoot != inner {
		t.Fatalf("nearest repository must win: %+v (want root %q)", d, inner)
	}
}
