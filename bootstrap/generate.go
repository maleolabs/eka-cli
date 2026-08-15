package bootstrap

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// This file implements Stage 4 of the bootstrap model: Repository
// Generation. Apply executes a plan produced by BuildPlan. The generator
// never writes anything that the plan did not mark for writing, and it
// never overwrites silently: ActionOverwriteConfirm steps ask first
// (interactive) or are skipped (non-interactive).

// ApplyOptions configure one Apply run.
type ApplyOptions struct {
	// Interactive enables overwrite confirmation prompts (read from Stdin,
	// written to Stdout). When false, overwrite-confirm actions are
	// skipped and recorded.
	Interactive bool
	// Stdin feeds overwrite confirmations (interactive only).
	Stdin io.Reader
	// Stdout receives overwrite prompts.
	Stdout io.Writer
	// Stderr receives warnings (e.g. a failed git init).
	Stderr io.Writer
	// GitInit runs `git init` in a directory. Defaults to exec.Command
	// "git init" with inherited output streams. Injectable for tests.
	GitInit func(dir string, stdout, stderr io.Writer) error
	// ConfirmOverwrite asks for permission to replace an existing file.
	// Defaults to a y/N prompt on Stdout/Stdin. When nil and Interactive is
	// false, overwrites are skipped. Injectable for tests.
	ConfirmOverwrite func(path string) (bool, error)
}

// GenerationResult reports what Apply actually did.
type GenerationResult struct {
	CreatedDirs      []string
	CreatedFiles     []string
	ReusedFiles      []string
	OverwrittenFiles []string
	SkippedFiles     []string
	// GitStatus is "initialized", "existing" (already a git repository),
	// "skipped (<reason>)" or "failed (<error>)".
	GitStatus string
}

// Apply executes the plan against target. Errors are internal failures
// (unwritable paths); a failed git init is a warning, never an error —
// an EKA repository without git is still valid.
func Apply(target string, plan []Action, opts ApplyOptions) (*GenerationResult, error) {
	if opts.Stdin == nil {
		opts.Stdin = strings.NewReader("")
	}
	if opts.GitInit == nil {
		opts.GitInit = defaultGitInit
	}
	res := &GenerationResult{GitStatus: "skipped (no git action planned)"}
	sc := bufio.NewScanner(opts.Stdin)
	confirm := opts.ConfirmOverwrite
	if confirm == nil {
		confirm = func(path string) (bool, error) { return askOverwrite(sc, opts.Stdout, path) }
	}

	gitStatusSet := false
	for _, a := range plan {
		switch a.Kind {
		case ActionCreateDir:
			// The target-dir action carries the sentinel path "." so
			// joining is always correct (a target named "docs" must not
			// collide with a generated docs/ directory).
			if err := os.MkdirAll(filepath.Join(target, a.Path), 0o755); err != nil {
				return nil, fmt.Errorf("cannot create directory %s: %w", a.Path, err)
			}
			res.CreatedDirs = append(res.CreatedDirs, a.Path)

		case ActionGenerateEkaYAML, ActionGenerateEKA:
			if err := writeFile(filepath.Join(target, a.Path), a.Content); err != nil {
				return nil, fmt.Errorf("cannot write %s: %w", a.Path, err)
			}
			res.CreatedFiles = append(res.CreatedFiles, a.Path)

		case ActionReuse:
			res.ReusedFiles = append(res.ReusedFiles, a.Path)

		case ActionOverwriteConfirm:
			if !opts.Interactive {
				// Never replace silently: without confirmation the
				// existing file stays and the skip is reported.
				res.SkippedFiles = append(res.SkippedFiles, a.Path)
				continue
			}
			ok, err := confirm(a.Path)
			if err != nil {
				return nil, err
			}
			if !ok {
				res.SkippedFiles = append(res.SkippedFiles, a.Path)
				continue
			}
			if err := writeFile(filepath.Join(target, a.Path), a.Content); err != nil {
				return nil, fmt.Errorf("cannot write %s: %w", a.Path, err)
			}
			res.OverwrittenFiles = append(res.OverwrittenFiles, a.Path)

		case ActionGitInit:
			gitStatusSet = true
			if err := opts.GitInit(target, opts.Stdout, opts.Stderr); err != nil {
				fmt.Fprintf(opts.Stderr, "warning: git init failed in %s: %v (continuing without git)\n", target, err)
				res.GitStatus = "failed (" + err.Error() + ")"
			} else {
				res.GitStatus = "initialized"
			}

		case ActionGitSkip:
			gitStatusSet = true
			if a.Detail == "skipped (already a git repository)" {
				res.GitStatus = "existing"
			} else {
				res.GitStatus = a.Detail // e.g. "skipped (non-interactive mode)"
			}
		}
	}
	if !gitStatusSet {
		// Defensive: a hand-built plan without git actions.
		res.GitStatus = "skipped (no git action planned)"
	}
	return res, nil
}

// writeFile writes content with mode 0644, creating parent directories.
func writeFile(path string, content []byte) error {
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return os.WriteFile(path, content, 0o644)
}

// askOverwrite prompts for a y/N confirmation on stdout and reads the
// answer from stdin. The default is No: existing content is never replaced
// unless the user says so explicitly.
func askOverwrite(sc *bufio.Scanner, w io.Writer, path string) (bool, error) {
	if w != nil {
		fmt.Fprintf(w, "overwrite %s? [y/N]: ", path)
	}
	if !sc.Scan() {
		if w != nil {
			fmt.Fprintln(w)
		}
		return false, nil
	}
	if w != nil {
		fmt.Fprintln(w)
	}
	switch strings.ToLower(strings.TrimSpace(sc.Text())) {
	case "y", "yes":
		return true, nil
	default:
		return false, nil
	}
}

// defaultGitInit runs `git init` in dir with inherited output streams.
func defaultGitInit(dir string, stdout, stderr io.Writer) error {
	cmd := exec.Command("git", "init")
	cmd.Dir = dir
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}
