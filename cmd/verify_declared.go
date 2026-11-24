package cmd

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/maleolabs/eka-core/contexts"
	"github.com/maleolabs/eka-core/exchange"
	"github.com/maleolabs/eka-core/pathglob"
	"github.com/maleolabs/eka-core/runtime"
)

// This file implements stage B of sto:verify-changed-gate — the EKA Standard
// 1.7 declared checks (sto:standard-check-declarations). A constraint Artifact
// declares its own checks through the optional `checks` content field; this
// file loads them, runs each at the repository root, and maps the outcome to a
// verifyCheckResult so a violation names the standard that declared it. The
// built-in registry stays in verify.go; declared checks are appended after it.
//
// Non-vacuity (AC4): a check runs its command at the repository root, so the
// gate catches exactly what the standard declares — and removing the
// declaration makes the gate pass the same change again.

// standardChecksID is the id of the checklist-only reporting check (AC3).
const standardChecksID = "standard-checks"

// standardChecksPaths is the repository-relative path set that makes the
// checklist-only report relevant under --changed: the artifact tree.
var standardChecksPaths = []string{"docs"}

// standardChecksRequirement / standardChecksFix are the informational
// render lines of the checklist-only report (never a blocking obligation).
const (
	standardChecksRequirement = "each constraint artifact should declare the checks that verify it; checklist-only is allowed but reported"
	standardChecksFix         = "add a `checks` declaration (eka get eka/std:eka-standard-1.0.0)"
)

// declaredCheckIDPrefix is the stable id prefix of a declared check:
// "declared:<canonicalForm>". A check resolves to the constraint id (canonical
// form) of the artifact that declared it; several checks declared by one
// artifact share this id — the artifact is the enforcement unit, and each
// finding names its own command.
const declaredCheckIDPrefix = "declared:"

// constraintTypeTokens is the set of artifact types the declared-checks field is
// designed for (the constraint families: std-, spec-, adr-, req-). The field is
// not restricted by type, but the checklist-only report names these carriers.
var constraintTypeTokens = map[string]bool{"std": true, "spec": true, "adr": true, "req": true}

// declaredCheckInfo is the repository-scoped result of one declared-check read:
// the repository's units (for checklist-only reporting) and the checks they
// declare (for execution).
type declaredCheckInfo struct {
	units    []*exchange.Unit
	declared []contexts.DeclaredCheck
}

// loadDeclaredChecks reads the declared checks of the repository rooted at root
// through the Context Engine (repository-scoped: project id + repository name).
// It degrades to no checks on a non-EKA tree — no workspace, an unregistered
// repository, or a repository that cannot be read — never an error there. A
// malformed declaration is an error (the Context Engine reports it, consistent
// with `eka context --for`), so it surfaces as a blocking finding.
func loadDeclaredChecks(root string) (declaredCheckInfo, error) {
	r, err := runtime.Open()
	if err != nil {
		return declaredCheckInfo{}, nil // No workspace: no declared checks.
	}
	defer r.Close()
	if !r.Exists() {
		return declaredCheckInfo{}, nil
	}
	repo, found, err := r.Workspace.FindRepo(root)
	if err != nil || !found {
		return declaredCheckInfo{}, nil // Not a registered EKA repository.
	}
	units, err := r.Knowledge.Units(repo.ProjectID, repo.Name)
	if err != nil {
		return declaredCheckInfo{}, fmt.Errorf("declared checks: %w", err)
	}
	declared, err := contexts.New(r).DeclaredChecks(repo.ProjectID, repo.Name)
	if err != nil {
		return declaredCheckInfo{}, err
	}
	return declaredCheckInfo{units: units, declared: declared}, nil
}

// standardChecksResult is the checklist-only report (AC3): for each constraint
// artifact of the repository, whether it is enforced (declares at least one
// check) or checklist-only (declares none). Warnings never block — a repo with
// no declared checks anywhere still passes; the report just names how each
// standard is enforced.
func standardChecksResult(info declaredCheckInfo) verifyCheckResult {
	res := verifyCheckResult{
		ID:         standardChecksID,
		Constraint: "eka/std:eka-standard-1.0.0",
		Verdict:    verdictPass, // informational: findings are warnings.
		Findings:   []verifyFinding{},

		requirement: standardChecksRequirement,
		fix:         standardChecksFix,
	}
	enforced := map[string]int{}
	for _, dc := range info.declared {
		enforced[dc.CanonicalForm]++
	}
	for _, u := range info.units {
		if !constraintTypeTokens[u.Identity.Type] {
			continue
		}
		msg := "checklist-only"
		if n := enforced[u.CanonicalIdentityForm]; n > 0 {
			msg = fmt.Sprintf("enforced: %d check(s)", n)
		}
		res.Findings = append(res.Findings, verifyFinding{
			File:     u.CanonicalIdentityForm,
			Severity: severityWarning,
			Message:  msg,
		})
	}
	return res
}

// runDeclaredCheck runs one declared check at root and maps its outcome to a
// verifyCheckResult. With --changed, a check whose scope does not intersect the
// changed set is SKIPPED; an empty scope matches nothing, so such a check runs
// only without --changed. The command runs through `sh -c` at root with its
// combined output captured. A non-zero exit is a blocking error finding
// carrying the command and the captured output; a zero exit is pass.
func runDeclaredCheck(dc contexts.DeclaredCheck, root string, changed bool, changedPaths []string) verifyCheckResult {
	res := verifyCheckResult{
		ID:          declaredCheckIDPrefix + dc.CanonicalForm,
		Constraint:  dc.CanonicalForm,
		Findings:    []verifyFinding{},
		requirement: "the declared check must exit zero: " + dc.Command,
		fix:         "run the declared check: " + dc.Command,
	}
	if changed && !scopeIntersectsChanged(dc.Scope, changedPaths) {
		res.Verdict = verdictSkipped
		return res
	}
	out, err := runDeclaredCommand(root, dc.Command)
	if err != nil {
		msg := "declared check failed: " + dc.Command
		if trimmed := strings.TrimSpace(out); trimmed != "" {
			msg += "\n" + trimmed
		}
		res.Findings = []verifyFinding{{
			File:     dc.CanonicalForm,
			Severity: severityError,
			Message:  msg,
		}}
		res.Verdict = verdictFail
		return res
	}
	res.Verdict = verdictPass
	return res
}

// runDeclaredCommand runs a declared command through `sh -c` at root and returns
// its combined stdout+stderr.
func runDeclaredCommand(root, command string) (string, error) {
	c := exec.Command("sh", "-c", command)
	c.Dir = root
	out, err := c.CombinedOutput()
	return string(out), err
}

// scopeIntersectsChanged reports whether any scope pattern matches any changed
// path (repository-relative, forward-slash). The scope uses the same glob
// grammar as `appliesTo`, so this reuses pathglob.Pattern.Match — an empty scope
// matches nothing.
func scopeIntersectsChanged(scope []pathglob.Pattern, changed []string) bool {
	for _, p := range scope {
		for _, c := range changed {
			if p.Match(c) {
				return true
			}
		}
	}
	return false
}
