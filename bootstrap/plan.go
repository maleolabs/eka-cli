package bootstrap

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"github.com/maleolabs/eka-core/metadata"
)

// This file implements Stage 2 of the bootstrap model: Bootstrap Planning.
// BuildPlan derives a deterministic action list from discovery results and
// wizard answers. The plan is the single source of truth for both the
// dry-run preview and the generator; nothing writes outside the plan.
//
// `eka init` is identity-only (ADR-020 Phase B1): it generates eka.yaml
// and never scaffolds the legacy docs-in-repo skeleton (no docs/ tree, no
// README, no skeleton file copies). Knowledge lives in the EKA workspace;
// docs-in-repo is legacy backward-compat, never generated. The generated
// repository contains eka.yaml only (+ optional git init).

// ActionKind is the kind of one planned action.
type ActionKind string

// Action kinds. Each kind has one deterministic rendering (see String).
const (
	// ActionCreateDir creates a directory (mode 0755).
	ActionCreateDir ActionKind = "create-dir"
	// ActionGenerateEkaYAML writes eka.yaml with the repository identity
	// (project, name, namespace — ADR-017): the portable identity file
	// every EKA repository carries at its root.
	ActionGenerateEkaYAML ActionKind = "generate-eka-yaml"
	// ActionReuse leaves an existing file untouched (identical content).
	ActionReuse ActionKind = "reuse"
	// ActionOverwriteConfirm marks an existing file whose content differs
	// from the generated one: it needs explicit confirmation (interactive)
	// or is skipped (non-interactive).
	ActionOverwriteConfirm ActionKind = "overwrite-confirm"
	// ActionGitInit runs `git init` in the target.
	ActionGitInit ActionKind = "git-init"
	// ActionGitSkip records that git init will not run and why.
	ActionGitSkip ActionKind = "git-skip"
	// ActionValidate runs conformance validation over the target.
	ActionValidate ActionKind = "validate"
)

// Action is one deterministic plan step.
type Action struct {
	// Kind selects the step.
	Kind ActionKind
	// Path is the affected path: the target itself for git/validate, a
	// forward-slash relative path (e.g. "eka.yaml") for files. The
	// target-dir creation action uses the sentinel "." with the target
	// name in Detail.
	Path string
	// Detail is optional context rendered in parentheses.
	Detail string
	// Content is the exact bytes the action will write, resolved at plan
	// time so the plan is self-contained (dry-run preview and generation
	// always agree). Only file-writing actions carry it.
	Content []byte
}

// String renders the action as a stable plan line.
func (a Action) String() string {
	switch a.Kind {
	case ActionCreateDir:
		// The target-dir action uses the sentinel path "." with the target
		// name in Detail.
		p := a.Path
		if p == "." && a.Detail != "" {
			p = a.Detail
		}
		return "create dir: " + p
	case ActionGenerateEkaYAML:
		return "generate file: " + a.Path + " (repository identity)"
	case ActionReuse:
		if a.Detail != "" {
			return "reuse: " + a.Path + " (" + a.Detail + ")"
		}
		return "reuse: " + a.Path
	case ActionOverwriteConfirm:
		return "overwrite confirm: " + a.Path
	case ActionGitInit:
		return "git init: " + a.Path
	case ActionGitSkip:
		return "git init: " + a.Detail
	case ActionValidate:
		if a.Detail != "" {
			return "validate: " + a.Path + " " + a.Detail
		}
		return "validate: " + a.Path
	default:
		return string(a.Kind) + ": " + a.Path
	}
}

// BuildPlan derives the deterministic plan for target from discovery d and
// answers a. Ordering is stable: target directory, identity file, git,
// validation. A target that is already an EKA repository yields a reuse +
// validate plan only — nothing is ever planned to be overwritten
// silently, and an existing eka.yaml follows the same
// reuse/overwrite-confirm contract as everything else. A legacy
// repository (docs markers, no eka.yaml) is adopted: the plan gains the
// eka.yaml generation action (ADR-018 Decision 3) between the reuse and
// the validate actions, so the identity file is written and validated in
// the same run.
func BuildPlan(target string, d *Discovery, a Answers) []Action {
	if d.Exists && d.IsEkaRepo {
		plan := []Action{
			{Kind: ActionReuse, Path: target, Detail: "existing EKA repository (already initialized)"},
		}
		// Adoption: a docs-marked repository without eka.yaml gets the
		// identity file generated; an existing eka.yaml follows the same
		// reuse/overwrite-confirm contract as everything else — an
		// identical file is reused, a differing one is never replaced
		// silently (the overwrite-confirm case is nearly impossible for
		// deterministic per-basename content, but is handled correctly).
		ekaPath := filepath.Join(d.AbsTarget, "eka.yaml")
		ekaContent := generatedEkaYAML(d, a)
		switch {
		case !pathExists(ekaPath):
			plan = append(plan, Action{Kind: ActionGenerateEkaYAML, Path: "eka.yaml", Content: ekaContent})
		case fileMatches(ekaPath, ekaContent):
			plan = append(plan, Action{Kind: ActionReuse, Path: "eka.yaml"})
		default:
			plan = append(plan, Action{Kind: ActionOverwriteConfirm, Path: "eka.yaml", Content: ekaContent})
		}
		plan = append(plan, Action{Kind: ActionValidate, Path: target})
		return plan
	}

	plan := []Action{}

	// The target directory is created only when it does not exist yet;
	// an existing directory is adopted as-is.
	if !d.Exists {
		// The target dir uses the sentinel path "." (see Action.String);
		// joining it onto the target always yields the target itself.
		plan = append(plan, Action{Kind: ActionCreateDir, Path: ".", Detail: target})
	}

	// Repository identity (eka.yaml, ADR-017): generated at the
	// repository root. Same reuse/overwrite-confirm contract as
	// everything else — an existing eka.yaml is never replaced silently.
	ekaPath := filepath.Join(d.AbsTarget, "eka.yaml")
	ekaContent := generatedEkaYAML(d, a)
	switch {
	case !pathExists(ekaPath):
		plan = append(plan, Action{Kind: ActionGenerateEkaYAML, Path: "eka.yaml", Content: ekaContent})
	case fileMatches(ekaPath, ekaContent):
		plan = append(plan, Action{Kind: ActionReuse, Path: "eka.yaml"})
	default:
		plan = append(plan, Action{Kind: ActionOverwriteConfirm, Path: "eka.yaml", Content: ekaContent})
	}

	// Git.
	switch {
	case d.IsGitRepo:
		plan = append(plan, Action{Kind: ActionGitSkip, Detail: "skipped (already a git repository)"})
	case !d.GitAvailable:
		plan = append(plan, Action{Kind: ActionGitSkip, Detail: "skipped (git not available)"})
	case !a.InitGit:
		if a.Interactive {
			plan = append(plan, Action{Kind: ActionGitSkip, Detail: "skipped (declined)"})
		} else {
			plan = append(plan, Action{Kind: ActionGitSkip, Detail: "skipped (non-interactive mode)"})
		}
	default:
		plan = append(plan, Action{Kind: ActionGitInit, Path: target})
	}

	// Validation.
	plan = append(plan, Action{Kind: ActionValidate, Path: target, Detail: "after generation"})
	return plan
}

// pathExists reports whether path exists (any type).
func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// fileMatches reports whether the file at path exists and contains exactly
// the given bytes.
func fileMatches(path string, want []byte) bool {
	got, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return bytes.Equal(got, want)
}

// ekaYAMLName derives the repository identity name for eka.yaml: the
// target directory basename (the spec's rule: name = directory
// basename at init time). A basename that is empty, unusable or fails
// IsValidIdent is sanitized (so eka.yaml always passes metadata.Parse);
// when nothing usable remains the fallback name applies. Shared by the
// plan content builder and Outcome.Identity so the two can never
// diverge.
func ekaYAMLName(d *Discovery) string {
	base := filepath.Base(d.AbsTarget)
	if IsValidIdent(base) {
		return base
	}
	if ns := sanitizeNamespace(base); ns != "" {
		return ns
	}
	return fallbackName
}

// generatedEkaYAML returns the exact bytes of the generated eka.yaml:
// the repository identity (project/name/namespace) per ADR-017 §3. The
// schema version comes from metadata.SchemaVersion — the single
// canonical source — so the written file always carries the accepted
// schema version.
// project is the wizard project id, namespace is the wizard namespace —
// the two are equal by default and decoupled when the user overrides one
// (the user edits the file freely before the first sync); name is the
// directory basename.
func generatedEkaYAML(d *Discovery, a Answers) []byte {
	return []byte(fmt.Sprintf("version: %d\nproject: %s\nname: %s\nnamespace: %s\n",
		metadata.SchemaVersion, a.Project, ekaYAMLName(d), a.Namespace))
}
