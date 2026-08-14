package cmd

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/maleolabs/eka-core/runtime"
)

// This file implements the issue-number reference resolution (RFC:
// per-group incremental numbers, GitHub-style — "#<n>" addresses a
// line of the project; work items, tickets and notes count
// independently). Every command accepting a target (view, get, note,
// reply, resolve, transition) resolves "#<n>" to its qualified line
// form before the regular target pipeline.

// parseNumberTarget reports whether raw is an issue-number reference
// ("#<n>", n >= 1) and returns its number.
func parseNumberTarget(raw string) (int, bool) {
	if !strings.HasPrefix(raw, "#") || len(raw) < 2 {
		return 0, false
	}
	n, err := strconv.Atoi(raw[1:])
	if err != nil || n < 1 {
		return 0, false
	}
	return n, true
}

// resolveNumberTarget resolves an issue-number reference to its
// qualified line form ("<namespace>/<type>:<id>"). group narrows the
// lookup ("" = any group — the result must then be unique, since the
// groups count independently). Deterministic errors: invalid number
// (usage), no match, ambiguous match (the candidate lines are listed
// with the narrowing hint).
func resolveNumberTarget(r *runtime.Runtime, projectID, raw, group string) (string, error) {
	n, ok := parseNumberTarget(raw)
	if !ok {
		return "", fmt.Errorf("invalid issue number %q (expected #<n>)", raw)
	}
	if group != "" {
		line, found, err := r.Knowledge.LineByNumberGroup(projectID, group, n)
		if err != nil {
			return "", fmt.Errorf("number lookup failed: %w", err)
		}
		if !found {
			return "", fmt.Errorf("no %s with number %s in this project", group, raw)
		}
		return line.LineForm(), nil
	}
	lines, err := r.Knowledge.LineByNumber(projectID, n)
	if err != nil {
		return "", fmt.Errorf("number lookup failed: %w", err)
	}
	if len(lines) == 0 {
		return "", fmt.Errorf("no item with number %s in this project", raw)
	}
	if len(lines) > 1 {
		forms := make([]string, 0, len(lines))
		for _, l := range lines {
			forms = append(forms, l.LineForm())
		}
		return "", fmt.Errorf("issue number %s is ambiguous: %s; narrow it with a projection or type (e.g. 'eka view ticket %s')",
			raw, strings.Join(forms, ", "), raw)
	}
	return lines[0].LineForm(), nil
}

// resolveNumberTargetInRepo resolves an issue-number reference from a
// repository directory: the repository's project scopes the lookup.
func resolveNumberTargetInRepo(r *runtime.Runtime, repoPath, raw, group string) (string, error) {
	abs, err := filepath.Abs(repoPath)
	if err != nil {
		return "", fmt.Errorf("cannot resolve the repository path: %w", err)
	}
	abs = filepath.Clean(abs)
	repo, found, err := r.Workspace.FindRepo(abs)
	if err != nil {
		return "", fmt.Errorf("cannot resolve the repository: %w", err)
	}
	if !found {
		return "", fmt.Errorf("repository %s is not registered in the EKA workspace; snip run 'eka sync' first", abs)
	}
	return resolveNumberTarget(r, repo.ProjectID, raw, group)
}
