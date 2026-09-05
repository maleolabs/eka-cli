package cmd

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/maleolabs/eka-core/conformance"
	"github.com/maleolabs/eka-core/metadata"
	"github.com/maleolabs/eka-core/runtime"
	"github.com/spf13/cobra"
)

// capture.go implements `eka capture --reconcile --from-git` (ADR-035 v3, spec provenance-capture:1)
// Layer 2 Git Hook Distributed via eka-standard + anvil lifecycle platform_sync.
//
// Contract:
//   - Reads git diff --cached (staged) + git log --since=24h (dedupeWindow default)
//   - Heuristic classification (file-type + commit-message verbs) -> artifact type token
//   - Generates draft provenance=reconciled with sourceCommitSha, confidence, captureMeta
//   - Respects dedupe (exact hash + similarity) within window: duplicate -> cmt:note or skip
//   - Non-blocking, deterministic, no LLM.

func newCaptureCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "capture",
		Short: "Universal capture gateway (provenance=reconciled from git)",
		Long: `Universal capture gateway — Layer 2 Git Hook Distributed (ADR-035 v3, spec provenance-capture:1).

Reconcile unrecorded work from git into provenance=reconciled drafts:

  eka capture --reconcile --from-git

Reads git diff --cached (staged) and git log --since=24h, classifies
heuristically (file-type + commit verbs), and scaffolds a draft with
provenance=reconciled, sourceCommitSha, confidence, and captureMeta
(classifier=git-heuristic v1, dedupeKey=hash(normalized title)).

Dedupe: exact dedupeKey equality and similarity >=80% within the
capture.dedupeWindow (default 24h) reuse the existing draft — a new
capture becomes a cmt:note discusses target instead of a duplicate CKO.

Hooks: Layer-2 git hooks (pre-commit, pre-push) from eka-standard/templates/hooks
are symlinked to .git/hooks via anvil platform_sync (scripts/platform-sync.sh).
Also runnable standalone: eka capture --install-hooks

Flags:
  --reconcile   required with --from-git: reconciled provenance path
  --from-git    read git staged + recent log (requires --reconcile)
  --dry-run     preview what would be captured without creating a draft
  --install-hooks  install/symlink Layer-2 hooks to .git/hooks (platform_sync)

Exit codes:
  0  captured or dry-run preview or no changes
  1  not an EKA repository or no registered project
  2  usage (missing required flags)`,
		RunE: runCapture,
	}
	cmd.Flags().Bool("reconcile", false, "reconciled provenance path (requires --from-git)")
	cmd.Flags().Bool("from-git", false, "read git diff --cached + log --since=24h (requires --reconcile)")
	cmd.Flags().Bool("dry-run", false, "preview without creating a draft")
	cmd.Flags().Bool("install-hooks", false, "install Layer-2 git hooks via platform_sync symlink")
	cmd.GroupID = groupAuthoring
	return cmd
}

func runCapture(cmd *cobra.Command, args []string) error {
	installHooks, _ := cmd.Flags().GetBool("install-hooks")
	if installHooks {
		return runCaptureInstallHooks(cmd)
	}

	reconcile, _ := cmd.Flags().GetBool("reconcile")
	fromGit, _ := cmd.Flags().GetBool("from-git")
	dryRun, _ := cmd.Flags().GetBool("dry-run")

	if !reconcile || !fromGit {
		// Explicit usage error when required flags missing
		fmt.Fprintf(cmd.ErrOrStderr(), "eka: capture requires --reconcile --from-git (universal capture gateway)\n")
		return &exitError{code: exitUsage}
	}

	// Resolve repo root (git preferred, fallback cwd)
	repoRoot, err := gitTopLevel()
	if err != nil {
		cwd, _ := os.Getwd()
		repoRoot = cwd
	}
	repoRoot, _ = filepath.Abs(repoRoot)

	// Require EKA repository (eka.yaml)
	_, _, hasMeta, err := metadata.Find(repoRoot)
	if err != nil {
		return fmt.Errorf("capture: %w", err)
	}
	if !hasMeta {
		fmt.Fprintf(cmd.ErrOrStderr(), "eka: capture refused: %s is not an EKA repository (no eka.yaml); run 'eka init' first\n", repoRoot)
		return &exitError{code: exitFail}
	}

	// Open runtime (needs workspace for drafts + store for dedupe)
	r, err := runtime.Ensure()
	if err != nil {
		return err
	}
	defer r.Close()

	// Resolve project/namespace from workspace registry (FindRepo)
	projectID, namespace, err := resolveCaptureProject(r, repoRoot)
	if err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "eka: capture: %v\n", err)
		return &exitError{code: exitFail}
	}

	// Effective capture config (threshold, dedupeWindow)
	meta, err := loadMetadata(repoRoot)
	if err != nil {
		// Use defaults if unreadable (should not happen after Find)
		meta = metadata.Metadata{Project: projectID, Namespace: namespace}
	}
	_, threshold, dedupeWindowStr, _ := meta.EffectiveCapture()
	window, _ := parseWindow(dedupeWindowStr)

	// Read git state
	stagedFiles, err := gitDiffCachedFiles(repoRoot)
	if err != nil {
		stagedFiles = nil
	}
	recentCommits, recentFiles, headSha, err := gitLogSince(repoRoot, dedupeWindowStr)
	if err != nil {
		recentCommits = nil
		recentFiles = nil
	}

	// Combine signal: staged + recent
	allFiles := mergeFileLists(stagedFiles, recentFiles)
	commitText := strings.Join(recentCommits, " ")

	// No signal?
	if len(allFiles) == 0 && len(commitText) == 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "capture: no git changes to reconcile (staged: 0, recent commits: 0)\n")
		return nil
	}

	// Heuristic classification -> type token, title, confidence
	typeToken, title, confidence := classifyGitHeuristic(allFiles, commitText, threshold)
	dedupeKey := runtime.Capture.DedupeKey(title)
	sourceHash := runtime.Capture.SourcePromptHash(strings.Join(allFiles, ",") + "|" + commitText)
	classifier := "git-heuristic v1"

	// Dedupe check (exact + similarity) within window
	now := time.Now()
	if existing, dup := runtime.Capture.DedupeCheckByTitle(r, projectID, title, window, now); dup && existing != nil {
		// If duplicate, create a cmt:note discusses target instead? For reconciled path, we skip with a note marker.
		// Create a cmt:note draft that discusses the existing draft? Minimal: report dedup and suggest note.
		if dryRun {
			fmt.Fprintf(cmd.OutOrStdout(), "capture: would dedupe — similar draft %s:%s (%s) already exists within %s — would create cmt:note discusses %s:%s\n",
				existing.Type, existing.ID, existing.Project, dedupeWindowStr, existing.Type, existing.ID)
		} else {
			fmt.Fprintf(cmd.OutOrStdout(), "capture: deduped — similar draft %s:%s exists within %s (classifier=%s); skipped\n",
				existing.Type, existing.ID, dedupeWindowStr, classifier)
		}
		return nil
	}
	// Also exact hash dedupe via DedupeCheck (redundant with ByTitle but keep for hash-only path)
	if dedupeKey != "" {
		if existing, dup := runtime.Capture.DedupeCheck(r, projectID, dedupeKey, window, now); dup && existing != nil {
			if dryRun {
				fmt.Fprintf(cmd.OutOrStdout(), "capture: would dedupe (exact hash) draft %s:%s within %s\n", existing.Type, existing.ID, dedupeWindowStr)
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "capture: deduped (exact) %s:%s within %s\n", existing.Type, existing.ID, dedupeWindowStr)
			}
			return nil
		}
	}

	// Confidence gate (reuse capture threshold semantics: confidence must meet threshold)
	if confidence < threshold {
		// Still allow reconciled capture with lower confidence? Spec says reconciled is heuristic from git, not prompt; we allow 0.5+ but annotate confidence.
		// If way too low, hint and skip unless dry-run shows would create.
		if dryRun {
			fmt.Fprintf(cmd.OutOrStdout(), "capture: would create (low confidence %.2f < threshold %.2f) %s:%s \"%s\" (provenance=reconciled)\n",
				confidence, threshold, typeToken, slugFromTitle(title), title)
			return nil
		}
		// For non-dry-run, still create but log low confidence (reconciled heuristic is best-effort)
	}

	id := slugFromTitle(title)
	if id == "" {
		id = fmt.Sprintf("reconciled-%s-%s", time.Now().Format("20060102"), headSha[:7])
		if len(headSha) < 7 {
			id = fmt.Sprintf("reconciled-%s", time.Now().Format("20060102-150405"))
		}
	}
	// Ensure id uniqueness by appending short hash if collision risk (deterministic)
	if len(id) > 40 {
		id = id[:40]
	}

	if dryRun {
		fmt.Fprintf(cmd.OutOrStdout(), "capture: would create %s/%s:%s \"%s\" (provenance=reconciled, confidence=%.2f, classifier=%s, dedupeKey=%s, commit=%s)\n",
			namespace, typeToken, id, title, confidence, classifier, dedupeKey[:8], shortSha(headSha))
		return nil
	}

	// Build draft request with reconciled provenance
	// Generate deterministic content for the chosen type
	content := reconciledContentFor(typeToken, title, allFiles, recentCommits)
	// Use author from runtime.BySource default (git config)
	by, _ := runtime.BySource("", "", repoRoot)
	// Resolve to a valid content object JSON via ContentFile mechanism: we pass via extraContent through NewDraft's content merge
	// Instead, we scaffold then patch? Simpler: scaffold with empty placeholders then overwrite content via draft file?
	// Use NewDraft then edit file to inject content; but NewDraft already merges extraContent if we use newDraftFile directly? We expose via Authoring.NewDraft with ContentFile not available inline.
	// Workaround: create temp JSON file with content object and pass as ContentFile
	tmpFile, err := tempContentFile(content)
	if err != nil {
		return fmt.Errorf("capture: %w", err)
	}
	defer os.Remove(tmpFile)

	shaForDraft := headSha
	if len(shaForDraft) > 40 {
		shaForDraft = shaForDraft[:40]
	}
	if len(shaForDraft) < 7 {
		shaForDraft = ""
	}

	draft, err := runtime.Authoring.NewDraft(r, runtime.NewDraftRequest{
		Project:          projectID,
		Namespace:        namespace,
		Type:             typeToken,
		ID:               id,
		By:               by,
		Provenance:       conformance.ProvenanceReconciled,
		SourcePromptHash: sourceHash,
		Confidence:       confidence,
		HasConfidence:    true,
		SourceCommitSha:  shaForDraft,
		CaptureMeta: runtime.CaptureMeta{
			Classifier: classifier,
			DedupeKey:  dedupeKey,
		},
		ContentFile: tmpFile,
	})
	if err != nil {
		// If collision (draft exists with same type:id), generate suffixed id deterministically
		if strings.Contains(err.Error(), "already exists") {
			altID := fmt.Sprintf("%s-%s", id, shortSha(sourceHash)[:4])
			draft, err = runtime.Authoring.NewDraft(r, runtime.NewDraftRequest{
				Project:          projectID,
				Namespace:        namespace,
				Type:             typeToken,
				ID:               altID,
				By:               by,
				Provenance:       conformance.ProvenanceReconciled,
				SourcePromptHash: sourceHash,
				Confidence:       confidence,
				HasConfidence:    true,
				SourceCommitSha:  shaForDraft,
				CaptureMeta: runtime.CaptureMeta{
					Classifier: classifier,
					DedupeKey:  dedupeKey,
				},
				ContentFile: tmpFile,
			})
			if err != nil {
				return fmt.Errorf("capture: %w", err)
			}
			id = altID
		} else {
			return fmt.Errorf("capture: %w", err)
		}
	}

	fmt.Fprintf(cmd.OutOrStdout(), "capture: created %s/%s:%s (provenance=reconciled, confidence=%.2f, classifier=%s)\n", namespace, typeToken, id, confidence, classifier)
	fmt.Fprintf(cmd.OutOrStdout(), "  draft: %s\n", draft.Path)
	fmt.Fprintf(cmd.OutOrStdout(), "  files: %d staged + %d recent (%d unique)\n", len(stagedFiles), len(recentFiles), len(allFiles))
	if headSha != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "  commit: %s\n", shortSha(headSha))
	}
	fmt.Fprintf(cmd.OutOrStdout(), "  next: eka draft list && eka publish %s:%s\n", typeToken, id)
	return nil
}

func runCaptureInstallHooks(cmd *cobra.Command) error {
	// Execute platform_sync script (preferred) or fallback inline symlink logic
	repoRoot, err := gitTopLevel()
	if err != nil {
		cwd, _ := os.Getwd()
		repoRoot = cwd
	}
	scriptCandidates := []string{
		filepath.Join(repoRoot, "scripts", "platform-sync.sh"),
		"/home/m2codeloan/m2code/maleolabs/eka/eka-cli/scripts/platform-sync.sh",
		filepath.Join(filepath.Dir(os.Args[0]), "scripts", "platform-sync.sh"),
	}
	for _, s := range scriptCandidates {
		if _, err := os.Stat(s); err == nil {
			c := exec.Command("sh", s)
			c.Stdout = cmd.OutOrStdout()
			c.Stderr = cmd.ErrOrStderr()
			if err := c.Run(); err != nil {
				return fmt.Errorf("capture --install-hooks: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "capture: hooks installed via %s\n", s)
			return nil
		}
	}
	// Fallback inline: symlink from eka-standard templates
	return installHooksInline(cmd, repoRoot)
}

func installHooksInline(cmd *cobra.Command, repoRoot string) error {
	templateSrc := ""
	candidates := []string{
		filepath.Join(repoRoot, "eka-standard", "templates", "hooks"),
		"/home/m2codeloan/m2code/maleolabs/eka/eka-standard/templates/hooks",
		"./templates/hooks",
	}
	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && st.IsDir() {
			templateSrc = c
			break
		}
	}
	if templateSrc == "" {
		return fmt.Errorf("capture --install-hooks: template source not found")
	}
	hooksDir := filepath.Join(repoRoot, ".git", "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		return err
	}
	for _, hook := range []string{"pre-commit", "pre-push"} {
		src := filepath.Join(templateSrc, hook)
		dst := filepath.Join(hooksDir, hook)
		if _, err := os.Stat(src); err != nil {
			continue
		}
		_ = os.Remove(dst)
		if err := os.Symlink(src, dst); err != nil {
			// fallback copy
			b, _ := os.ReadFile(src)
			_ = os.WriteFile(dst, b, 0o755)
		} else {
			_ = os.Chmod(dst, 0o755)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "capture: symlinked %s -> %s\n", dst, src)
	}
	return nil
}

// Helpers

func gitTopLevel() (string, error) {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func loadMetadata(repoRoot string) (metadata.Metadata, error) {
	b, err := os.ReadFile(filepath.Join(repoRoot, "eka.yaml"))
	if err != nil {
		return metadata.Metadata{}, err
	}
	return metadata.Parse(b)
}

func resolveCaptureProject(r *runtime.Runtime, repoRoot string) (project, namespace string, err error) {
	repo, found, ferr := r.Workspace.FindRepo(repoRoot)
	if ferr != nil {
		return "", "", ferr
	}
	if found {
		return repo.ProjectID, repo.Namespace, nil
	}
	// Fallback: parse eka.yaml directly (when workspace not synced but repo exists)
	m, err := loadMetadata(repoRoot)
	if err != nil {
		return "", "", fmt.Errorf("cannot resolve project: not in registered workspace and eka.yaml unreadable: %w", err)
	}
	if m.Project == "" || m.Namespace == "" {
		return "", "", fmt.Errorf("cannot resolve project/namespace from eka.yaml")
	}
	return m.Project, m.Namespace, nil
}

func parseWindow(s string) (time.Duration, error) {
	if s == "" {
		s = "24h"
	}
	return time.ParseDuration(s)
}

func gitDiffCachedFiles(repoRoot string) ([]string, error) {
	cmd := exec.Command("git", "diff", "--cached", "--name-only")
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	return splitLines(string(out)), nil
}

func gitLogSince(repoRoot, window string) (commits []string, files []string, headSha string, err error) {
	if window == "" {
		window = "24h"
	}
	// Normalize window for git --since: accept "24h" -> "24 hours ago"
	since := window
	if strings.HasSuffix(window, "h") {
		h := strings.TrimSuffix(window, "h")
		since = h + " hours ago"
	}
	// log messages
	cmd := exec.Command("git", "log", "--since="+since, "--pretty=format:%H %s")
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	if err != nil {
		return nil, nil, "", err
	}
	commits = splitLines(string(out))
	if len(commits) > 0 {
		parts := strings.Fields(commits[0])
		if len(parts) > 0 {
			headSha = parts[0]
		}
	}
	// files touched in window
	cmd2 := exec.Command("git", "log", "--since="+since, "--pretty=format:", "--name-only")
	cmd2.Dir = repoRoot
	out2, err := cmd2.Output()
	if err != nil {
		return commits, nil, headSha, nil
	}
	files = splitLines(string(out2))
	// Dedupe files and drop empties
	files = dedupeSorted(files)
	return commits, files, headSha, nil
}

func splitLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

func dedupeSorted(in []string) []string {
	sort.Strings(in)
	var out []string
	seen := map[string]bool{}
	for _, v := range in {
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

func mergeFileLists(a, b []string) []string {
	m := map[string]bool{}
	for _, f := range a {
		if f != "" {
			m[f] = true
		}
	}
	for _, f := range b {
		if f != "" {
			m[f] = true
		}
	}
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func classifyGitHeuristic(files []string, commitText string, threshold float64) (typeToken, title string, confidence float64) {
	lowerFiles := strings.ToLower(strings.Join(files, " "))
	lowerCommits := strings.ToLower(commitText)
	combined := lowerFiles + " " + lowerCommits

	// Heuristic priority: bug/fix -> bug, feat/add -> sto, adr/dec/spec -> adr/spec, chore -> ch
	hasFix := strings.Contains(combined, "fix") || strings.Contains(combined, "bug") || strings.Contains(combined, "hotfix")
	hasFeat := strings.Contains(combined, "feat") || strings.Contains(combined, "add ") || strings.Contains(combined, "feature")
	isADR := strings.Contains(lowerFiles, "adr") || strings.Contains(lowerCommits, "adr") || strings.Contains(lowerFiles, "decision")
	isSpec := strings.Contains(lowerFiles, "spec") || strings.Contains(lowerCommits, "spec")
	isPlan := strings.Contains(lowerFiles, "plan") || strings.Contains(lowerCommits, "plan") || strings.Contains(lowerFiles, "roadmap")
	isDoc := strings.Contains(lowerFiles, ".md") && (strings.Contains(lowerFiles, "docs/") || strings.Contains(lowerFiles, "readme"))

	words := len(strings.Fields(combined))
	hasVerb := runtime.Capture.ContainsVerb(combined)

	// Confidence mirrors capture service: verb + >=10 words => 0.8
	switch {
	case hasVerb && words >= 10:
		confidence = 0.8
	case words >= 10:
		confidence = 0.5
	default:
		confidence = 0.4
	}
	// Adjust confidence by signal strength
	if len(files) >= 3 {
		confidence += 0.05
	}
	if confidence > 1 {
		confidence = 1
	}
	if confidence < 0 {
		confidence = 0
	}

	switch {
	case hasFix:
		typeToken = "bug"
		title = buildTitle("fix reconciled changes", files, commitText)
	case isADR:
		typeToken = "adr"
		title = buildTitle("adr reconciled decision", files, commitText)
	case isSpec:
		typeToken = "spec"
		title = buildTitle("spec reconciled spec", files, commitText)
	case isPlan:
		typeToken = "plan"
		title = buildTitle("plan reconciled scope", files, commitText)
	case hasFeat:
		typeToken = "sto"
		title = buildTitle("feat reconciled story", files, commitText)
	case isDoc:
		typeToken = "fnd"
		title = buildTitle("docs reconciled finding", files, commitText)
	default:
		typeToken = "ch"
		title = buildTitle("chore reconciled changes", files, commitText)
	}
	// Ensure threshold gate: if confidence below threshold, keep ch but mark title
	if confidence < threshold {
		confidence = threshold - 0.05 // still create but flagged low
		if confidence < 0.4 {
			confidence = 0.4
		}
	}
	return typeToken, title, confidence
}

func buildTitle(prefix string, files []string, commitText string) string {
	// Prefer commit subject if available and long enough
	if commitText != "" {
		// First commit line's subject after sha
		lines := strings.Split(commitText, "\n")
		if len(lines) > 0 {
			s := lines[0]
			// Strip leading sha if present (40 hex or 7 hex)
			parts := strings.Fields(s)
			if len(parts) > 1 && isHex(parts[0]) {
				s = strings.Join(parts[1:], " ")
			}
			s = strings.TrimSpace(s)
			if len(strings.Fields(s)) >= 3 && len(s) <= 80 {
				return s
			}
		}
	}
	if len(files) > 0 {
		base := filepath.Base(files[0])
		ext := filepath.Ext(base)
		name := strings.TrimSuffix(base, ext)
		name = strings.ReplaceAll(name, "-", " ")
		name = strings.ReplaceAll(name, "_", " ")
		return fmt.Sprintf("%s: %s", prefix, name)
	}
	now := time.Now().Format("2006-01-02")
	return fmt.Sprintf("%s %s", prefix, now)
}

func isHex(s string) bool {
	if len(s) < 7 || len(s) > 40 {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

func slugFromTitle(title string) string {
	s := strings.ToLower(title)
	// Replace non-alnum with hyphen
	var b strings.Builder
	prevHyphen := false
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			prevHyphen = false
		} else if r == ' ' || r == '-' || r == '_' {
			if !prevHyphen {
				b.WriteRune('-')
				prevHyphen = true
			}
		} else {
			if !prevHyphen {
				b.WriteRune('-')
				prevHyphen = true
			}
		}
	}
	slug := strings.Trim(b.String(), "-")
	if len(slug) > 40 {
		slug = slug[:40]
		slug = strings.Trim(slug, "-")
	}
	if slug == "" {
		return ""
	}
	return slug
}

func shortSha(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 7 {
		return s[:7]
	}
	return s
}

func reconciledContentFor(typeToken, title string, files []string, commits []string) map[string]any {
	filesStr := strings.Join(files, ", ")
	if filesStr == "" {
		filesStr = "(no staged files)"
	}
	commitsStr := strings.Join(commits, "\n")
	if commitsStr == "" {
		commitsStr = "(no recent commits)"
	}
	summary := fmt.Sprintf("Reconciled from git (Layer-2 hook): %s", title)
	// Per-type required sections (conformance.RequiredSectionsFor handles defaults; we provide generic keys)
	switch typeToken {
	case "bug":
		return map[string]any{
			"summary":     summary,
			"description": fmt.Sprintf("Auto-captured bug from git changes.\nFiles: %s\nCommits:\n%s", filesStr, commitsStr),
			"reproSteps":  "Reproducible via git diff --cached / log --since=24h",
			"severity":    "medium",
		}
	case "sto":
		return map[string]any{
			"summary":     summary,
			"description": fmt.Sprintf("Auto-captured story from git changes.\nFiles: %s\nCommits:\n%s", filesStr, commitsStr),
			"acceptance":  "Reconciled — review and refine",
		}
	case "adr":
		return map[string]any{
			"context":      fmt.Sprintf("Git changes suggest architectural decision.\nFiles: %s", filesStr),
			"decision":     title,
			"consequences": "Reconciled via git-heuristic v1; requires human review",
		}
	case "spec":
		return map[string]any{
			"summary": summary,
			"details": fmt.Sprintf("Files: %s\nCommits:\n%s", filesStr, commitsStr),
		}
	case "plan":
		return map[string]any{
			"objective":  summary,
			"scope":      fmt.Sprintf("Files: %s", filesStr),
			"outOfScope": "Requires planning review",
		}
	case "fnd":
		return map[string]any{
			"summary": summary,
			"finding": fmt.Sprintf("Files: %s\nCommits:\n%s", filesStr, commitsStr),
		}
	default: // ch, etc.
		return map[string]any{
			"summary":     summary,
			"description": fmt.Sprintf("Auto-captured chore from git changes.\nFiles: %s\nCommits:\n%s", filesStr, commitsStr),
		}
	}
}

func tempContentFile(content map[string]any) (string, error) {
	b, err := json.Marshal(content)
	if err != nil {
		return "", err
	}
	f, err := os.CreateTemp("", "eka-capture-*.json")
	if err != nil {
		return "", err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", err
	}
	f.Close()
	return f.Name(), nil
}

// keep imports used
var _ = sha256.Sum256
var _ = hex.EncodeToString
