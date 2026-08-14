package bootstrap

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// This file implements Stage 1 of the bootstrap model: Workspace Discovery.
// It inspects the target directory and answers, with evidence, every
// question the later stages need. Detection results are informational where
// noted; nothing here modifies the filesystem.

// Discovery is the read-only result of inspecting the target directory.
type Discovery struct {
	// Target is the target as given by the caller (e.g. "." or "myproj").
	Target string
	// AbsTarget is the absolute form of Target, used internally.
	AbsTarget string
	// BaseName is the base name of the absolute target path; it is the
	// default project name ("" or "/" at the filesystem root).
	BaseName string

	// Exists reports whether the target path exists at all.
	Exists bool
	// IsDir reports whether the target is a directory (meaningful only when
	// Exists is true).
	IsDir bool

	// IsGitRepo reports whether the target is inside a git repository: a
	// ".git" entry (directory for a normal clone, file for a worktree) is
	// looked for in the target itself, then in every ancestor up to the
	// filesystem root.
	IsGitRepo bool
	// GitRoot is the directory whose ".git" entry was found ("" when none).
	GitRoot string

	// HasReadme reports whether the target contains a README.md or README
	// file (exact names, case-sensitive).
	HasReadme bool
	// ReadmePath is the name of the found README ("README.md" or "README").
	ReadmePath string
	// HasDocs reports whether the target contains a docs/ directory.
	HasDocs bool
	// IsEkaRepo reports whether the target already carries the EKA
	// repository markers: eka.yaml at the repository root (ADR-018
	// Decision 3) and/or docs/operating/ (directory) plus
	// docs/exchange/validation.md and docs/exchange/transfer.md (files).
	IsEkaRepo bool

	// ConfigFiles lists existing files matching the known config markers:
	// .gitignore, .editorconfig, .eka.* and eka.*. Informational only; the
	// bootstrap never reads or modifies them.
	ConfigFiles []string

	// GitAvailable reports whether a "git" executable is reachable via
	// PATH (exec.LookPath). Informational; used only to decide whether the
	// git-init question is offered.
	GitAvailable bool
}

// Discover inspects target and returns a Discovery. A target that does not
// exist is not an error: it simply yields Exists=false (the planner will
// create it). Non-directory targets are reported via IsDir=false.
func Discover(target string) (*Discovery, error) {
	abs, err := filepath.Abs(target)
	if err != nil {
		return nil, fmt.Errorf("cannot resolve target path: %w", err)
	}
	d := &Discovery{Target: target, AbsTarget: abs, BaseName: filepath.Base(abs)}

	if info, err := os.Stat(abs); err == nil {
		d.Exists = true
		d.IsDir = info.IsDir()
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("cannot stat target: %w", err)
	}

	if d.Exists && d.IsDir {
		detectGitRepo(d)
		detectReadme(d)
		detectDocsAndEka(d)
		detectConfigFiles(d)
	}

	if _, err := exec.LookPath("git"); err == nil {
		d.GitAvailable = true
	}
	return d, nil
}

// detectGitRepo walks from the target up to the filesystem root looking for
// a ".git" entry.
func detectGitRepo(d *Discovery) {
	dir := d.AbsTarget
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			d.IsGitRepo = true
			d.GitRoot = dir
			return
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return
		}
		dir = parent
	}
}

// detectReadme looks for README.md or README in the target directory.
func detectReadme(d *Discovery) {
	for _, name := range []string{"README.md", "README"} {
		if info, err := os.Stat(filepath.Join(d.AbsTarget, name)); err == nil && !info.IsDir() {
			d.HasReadme = true
			d.ReadmePath = name
			return
		}
	}
}

// detectDocsAndEka checks for docs/ and the EKA repository markers.
// eka.yaml at the repository root is a marker on its own (ADR-018
// Decision 3): a metadata-only repository (eka.yaml, no docs/ tree) is
// an EKA repository and must never be scaffolded with the legacy docs
// skeleton. The legacy markers (docs/operating/ + the exchange files)
// remain valid, so an existing docs-marked repository without eka.yaml
// is still detected and adopted by `eka init`.
func detectDocsAndEka(d *Discovery) {
	if info, err := os.Stat(filepath.Join(d.AbsTarget, "docs")); err == nil && info.IsDir() {
		d.HasDocs = true
	}
	if _, err := os.Stat(filepath.Join(d.AbsTarget, "eka.yaml")); err == nil {
		d.IsEkaRepo = true
		return
	}
	if !d.HasDocs {
		return
	}
	if info, err := os.Stat(filepath.Join(d.AbsTarget, "docs", "operating")); err != nil || !info.IsDir() {
		return
	}
	for _, rel := range []string{"docs/exchange/validation.md", "docs/exchange/transfer.md"} {
		if _, err := os.Stat(filepath.Join(d.AbsTarget, rel)); err != nil {
			return
		}
	}
	d.IsEkaRepo = true
}

// detectConfigFiles lists files matching the informational config markers.
func detectConfigFiles(d *Discovery) {
	entries, err := os.ReadDir(d.AbsTarget)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if name == ".gitignore" || name == ".editorconfig" ||
			strings.HasPrefix(name, ".eka.") || strings.HasPrefix(name, "eka.") {
			d.ConfigFiles = append(d.ConfigFiles, name)
		}
	}
	sort.Strings(d.ConfigFiles)
}
