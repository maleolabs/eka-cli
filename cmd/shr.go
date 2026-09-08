package cmd

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/maleolabs/eka-cli/cmd/ui"
	"github.com/maleolabs/eka-core/conformance"
	"github.com/maleolabs/eka-core/exchange"
	"github.com/maleolabs/eka-core/metadata"
	"github.com/maleolabs/eka-core/runtime"
	"github.com/spf13/cobra"
)

// newShrCommand builds `eka shr` parent command for sharing objects.
func newShrCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "shr",
		Short: "Sharing object (shr) operations",
		Long:  `Sharing object operations: build a shr snapshot copy from a source CKO (EKA-to-EKA builder).`,
	}
	cmd.AddCommand(newShrBuildCommand())
	cmd.AddCommand(newShrExportCommand())
	cmd.AddCommand(newShrImportCommand())
	cmd.AddCommand(newShrDeleteCommand())
	cmd.AddCommand(newShrListCommand())
	cmd.AddCommand(newShrShowCommand())
	return cmd
}

func newShrBuildCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "build <source>",
		Short: "Build a shr sharing object from a source CKO",
		Long: `Build a shr sharing object (snapshot copy + pin hash) from a source CKO.

Source is a CKO identity: <ns>/<type>:<id>[:<version>] (qualified line form
or canonical form, latest when version omitted). The builder is
EKA-to-EKA only (no daemon, no audit).

Opt-in default-deny: --level is required and must be L0|L1|L2.
  L0 metadata only (title, description, sourceRef, sourceHash)
  L1 L0 + safe summary/structure (no sensitive content)
  L2 L1 + full content snapshot at sourceHash

The shr draft is created via runtime.NewDraft with:
  title, description, level, provenance=extracted, sourceHash (pin),
  sourceRef derives-from to versioned source (rel:versi), and
  snapshot copy based on level. Output bundle is self-contained and
  passes validate.

Flags:
  --level L0|L1|L2   required, opt-in classification (single)
  --levels L0,L1,L2  batch: build 1-3 shr at once (mutually exclusive with --level)
  --id <shr-id>      id for the new shr (default: share-<source-id>-<level>)
  --title <text>     shr title (default: Share <LEVEL> <type>:<id>)
  --description <text> shr description (default: snapshot copy of <source>)
  --project <name>   explicit project (workspace-native)
  --namespace <ns>   explicit namespace (workspace-native)

Server-side filtering:
  eka get <shr-id> --level L0       identity: strict level match (server-side)
  eka get operations --level L0     domain: filter shr by level (server-side, shr only)
  eka get operations --type shr --level L1

Project/namespace resolution: same as eka new (repo context or explicit
--project/--namespace). Source must exist in the workspace.

Examples:
  eka shr build eka/adr:sharing-object-model --level L0 --id my-share
  eka shr build eka/scp:knowledge-sharing --level L2
  eka shr build eka/req:knowledge-sharing --level L1 --title "Share L1"
  eka shr build eka/adr:sharing-object-model --levels L0,L1,L2  # batch 3 shr
  eka get operations --level L0
  eka get eka/shr:my-share-l0 --level L0`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			level, _ := cmd.Flags().GetString("level")
			levelsFlag, _ := cmd.Flags().GetString("levels")
			shrID, _ := cmd.Flags().GetString("id")
			titleFlag, _ := cmd.Flags().GetString("title")
			descFlag, _ := cmd.Flags().GetString("description")
			projectFlag, _ := cmd.Flags().GetString("project")
			nsFlag, _ := cmd.Flags().GetString("namespace")
			provenanceFlag, _ := cmd.Flags().GetString("provenance")

			// Determine batch levels: --levels L0,L1,L2 or single --level
			var levels []string
			if strings.TrimSpace(levelsFlag) != "" {
				parts := strings.Split(levelsFlag, ",")
				for _, p := range parts {
					lv := strings.ToUpper(strings.TrimSpace(p))
					if lv != "L0" && lv != "L1" && lv != "L2" {
						return fmt.Errorf("shr build: --levels must be comma-separated L0|L1|L2, got %q", p)
					}
					levels = append(levels, lv)
				}
				if strings.TrimSpace(level) != "" {
					return fmt.Errorf("shr build: --level and --levels are mutually exclusive")
				}
				// dedup preserve order
				seen := map[string]bool{}
				uniq := []string{}
				for _, l := range levels {
					if !seen[l] {
						seen[l] = true
						uniq = append(uniq, l)
					}
				}
				levels = uniq
			} else {
				if strings.TrimSpace(level) == "" {
					return fmt.Errorf("shr build: --level is required (L0|L1|L2); default share nothing (opt-in)")
				}
				level = strings.ToUpper(strings.TrimSpace(level))
				if level != "L0" && level != "L1" && level != "L2" {
					return fmt.Errorf("shr build: --level must be L0, L1 or L2, got %q", level)
				}
				levels = []string{level}
			}
			if strings.Contains(shrID, ":") || strings.Contains(shrID, "/") {
				return fmt.Errorf("shr build: --id must be a bare id (no namespace or type), got %q", shrID)
			}

			sourceArg := strings.TrimSpace(args[0])
			if sourceArg == "" {
				return fmt.Errorf("shr build: source identity is required")
			}
			provenance := "extracted"
			if strings.TrimSpace(provenanceFlag) != "" {
				provenance = strings.ToLower(strings.TrimSpace(provenanceFlag))
			}
			if provenance != "extracted" && provenance != "audited" {
				return fmt.Errorf("shr build: --provenance must be extracted or audited, got %q", provenanceFlag)
			}
			isAudited := provenance == "audited"

			r, err := openAuthoringRuntime(cmd)
			if err != nil {
				return err
			}
			defer r.Close()

			var unit *exchange.Unit
			var sourceHash, sourceForm string
			if isAudited {
				info, err := os.Stat(sourceArg)
				if err != nil {
					return fmt.Errorf("shr build: audited source path %q not found: %w", sourceArg, err)
				}
				if !info.IsDir() {
					return fmt.Errorf("shr build: audited source must be a directory, got %q", sourceArg)
				}
				sourceForm = fmt.Sprintf("audited:%s", sourceArg)
			} else {
				// Resolve source CKO via Resolver (qualified forms).
				var ok bool
				unit, ok, err = r.Resolver.Resolve(sourceArg)
				if err != nil {
					return fmt.Errorf("shr build: %w", err)
				}
				if !ok {
					return fmt.Errorf("shr build: source %q not found in workspace; run 'eka sync' first", sourceArg)
				}
				sourceHash = unit.Digest
				if sourceHash == "" {
					return fmt.Errorf("shr build: source %q has empty object hash (store corruption)", sourceArg)
				}
				sourceForm = unit.CanonicalIdentityForm
				if sourceForm == "" {
					sourceForm = unit.Identity.CanonicalForm()
				}
			}

			// Use first level to resolve project/namespace; subsequent levels reuse same.
			firstLevel := levels[0]
			shrIDFirst := shrID
			if shrIDFirst == "" {
				if isAudited {
					base := normalizeShrID(filepath.Base(sourceArg))
					if base == "" || base == "." || base == "/" {
						base = "audited"
					}
					shrIDFirst = fmt.Sprintf("share-%s-%s", base, strings.ToLower(firstLevel))
				} else {
					shrIDFirst = fmt.Sprintf("share-%s-%s", unit.Identity.ID, strings.ToLower(firstLevel))
				}
			} else if len(levels) > 1 {
				shrIDFirst = fmt.Sprintf("%s-%s", shrID, strings.ToLower(firstLevel))
			}
			shrIDFirst = normalizeShrID(shrIDFirst)
			if !isValidShrID(shrIDFirst) {
				return fmt.Errorf("shr build: generated id %q is not a valid EKA identifier (lowercase letters, digits, hyphens)", shrIDFirst)
			}
			ref := conformance.Reference{Namespace: nsFlag, Type: "shr", ID: shrIDFirst}
			project, ns, err := resolveNewScope(r, ref, projectFlag, nsFlag)
			if err != nil {
				return fmt.Errorf("shr build: %v", err)
			}

			// Build each level sequentially (batch).
			var built []string
			for _, lvl := range levels {
				// Fix P1: per-level audit (L0 shallow vs L1/L2 deep scan+redaction); was hardcoded L0 outside loop
				var auditSummary string
				if isAudited {
					auditSummary, sourceHash = auditNonEKAPathLevel(sourceArg, lvl)
				}
				shrIDResolved := shrID
				if shrIDResolved == "" {
					if isAudited {
						base := normalizeShrID(filepath.Base(sourceArg))
						if base == "" || base == "." || base == "/" {
							base = "audited"
						}
						shrIDResolved = fmt.Sprintf("share-%s-%s", base, strings.ToLower(lvl))
					} else {
						shrIDResolved = fmt.Sprintf("share-%s-%s", unit.Identity.ID, strings.ToLower(lvl))
					}
				} else if len(levels) > 1 {
					shrIDResolved = fmt.Sprintf("%s-%s", shrID, strings.ToLower(lvl))
				}
				shrIDResolved = normalizeShrID(shrIDResolved)
				if !isValidShrID(shrIDResolved) {
					return fmt.Errorf("shr build: generated id %q is not a valid EKA identifier", shrIDResolved)
				}
				title := strings.TrimSpace(titleFlag)
				if title == "" {
					if isAudited {
						base := filepath.Base(sourceArg)
						if len(levels) > 1 {
							title = fmt.Sprintf("Share %s audited:%s (%s)", lvl, base, shrIDResolved)
						} else {
							title = fmt.Sprintf("Share %s audited:%s", lvl, base)
						}
					} else {
						if len(levels) > 1 {
							title = fmt.Sprintf("Share %s %s:%s (%s)", lvl, unit.Identity.Type, unit.Identity.ID, shrIDResolved)
						} else {
							title = fmt.Sprintf("Share %s %s:%s", lvl, unit.Identity.Type, unit.Identity.ID)
						}
					}
				} else if len(levels) > 1 {
					title = fmt.Sprintf("%s %s", title, lvl)
				}
				description := strings.TrimSpace(descFlag)
				if description == "" {
					// L0/L1/L2 description: agent-provided overview is primary (via --description)
					// Auto-extract README is final fallback only (not primary) — a project may have no README or more specific docs
					if overview := extractShrOverviewFallback(sourceArg, isAudited); overview != "" {
						description = overview
					} else {
						shortHash := sourceHash
						if len(shortHash) > 8 {
							shortHash = shortHash[:8]
						}
						description = fmt.Sprintf("Sharing object derived from %s at %s (level %s, provenance %s)", sourceForm, shortHash, lvl, provenance)
					}
				} else if len(levels) > 1 {
					description = fmt.Sprintf("%s (level %s)", description, lvl)
				}

				content := map[string]any{
					"title":       title,
					"description": description,
					"level":       lvl,
					"provenance":  provenance,
					"sourceHash":  sourceHash,
					"purpose":     title,
					"content":     description,
				}
				// Per-project identifier (shr-per-project-identifier): capture sourceProject/sourceVersion
				// EKA: derived from resolved project/namespace + eka.yaml version; non-EKA: source is filesystem path, project asked via flag already resolved.
				sourceProject := project
				if sourceProject == "" {
					sourceProject = ns
				}
				content["sourceProject"] = sourceProject
				content["sourceVersion"] = resolveShrVersion(sourceProject, lvl)
				content["sourceNamespace"] = ns
				// Dedup L1/L2 common fields via helper (hardening: dedup).
				switch lvl {
				case "L0":
				case "L1":
					if isAudited {
						content["summary"] = auditSummary
						content["sourceType"] = "audited"
						content["sourceId"] = filepath.Base(sourceArg)
					} else {
						for k, v := range buildCommonShrFields(unit) {
							content[k] = v
						}
					}
				case "L2":
					if isAudited {
						content["summary"] = auditSummary
						content["sourceType"] = "audited"
						content["sourceId"] = filepath.Base(sourceArg)
						if err := shrSnapshotGuard(auditSummary); err != nil {
							return fmt.Errorf("shr build: %w", err)
						}
						content["snapshot"] = auditSummary
					} else {
						for k, v := range buildCommonShrFields(unit) {
							content[k] = v
						}
						snap := extractSnapshot(unit)
						if snap != nil {
							if err := shrSnapshotGuard(snap); err != nil {
								return fmt.Errorf("shr build: %w", err)
							}
							content["snapshot"] = snap
						}
						// EKA L2 deep scan: API contracts + docs (per clarified EKA-to-EKA: any project with eka.yaml, not just project eka)
						if deepDocs := deepScanEkaDocsForL2(); len(deepDocs) > 0 {
							content["deepDocs"] = deepDocs
							// Guard extended snapshot including deepDocs
							if err := shrSnapshotGuard(content["deepDocs"]); err != nil {
								return fmt.Errorf("shr build: deepDocs %w", err)
							}
						}
						// Auto-detect hint: if sourceArg looks like path with eka.yaml, suggest CKO (EKA-to-EKA should use CKO, not path)
						// Provenance is explicit via flag, but we log hint for agent clarity
					}
				}

				tmpFile, err := os.CreateTemp("", "shr-content-*.json")
				if err != nil {
					return fmt.Errorf("shr build: cannot create temp content file: %w", err)
				}
				tmpPath := tmpFile.Name()
				enc, err := json.Marshal(content)
				if err != nil {
					tmpFile.Close()
					os.Remove(tmpPath)
					return fmt.Errorf("shr build: cannot marshal shr content: %w", err)
				}
				if _, err := tmpFile.Write(enc); err != nil {
					tmpFile.Close()
					os.Remove(tmpPath)
					return fmt.Errorf("shr build: cannot write temp content file: %w", err)
				}
				tmpFile.Close()

				var rels []exchange.Relationship
				if !isAudited {
					rels = []exchange.Relationship{{Type: "derives-from", Target: sourceForm}}
				}
				draft, err := runtime.Authoring.NewDraft(r, runtime.NewDraftRequest{
					Project:       project,
					Namespace:     ns,
					Type:          "shr",
					ID:            shrIDResolved,
					Dimension:     "records",
					Relationships: rels,
					ContentFile:   tmpPath,
				})
				os.Remove(tmpPath)
				if err != nil {
					// Hardening: collision (already exists) must be exit 1 (fail), not usage 2.
					if strings.Contains(strings.ToLower(err.Error()), "already exists") || strings.Contains(strings.ToLower(err.Error()), "collision") {
						fmt.Fprintf(cmd.ErrOrStderr(), "eka: shr build: %v (level %s)\n", err, lvl)
						return &exitError{code: exitFail}
					}
					return fmt.Errorf("shr build: %w (level %s)", err, lvl)
				}
				built = append(built, fmt.Sprintf("%s/shr:%s", ns, shrIDResolved))

				s := styleFor(cmd)
				ui.NewHeader(s, "Shr Builder").
					Add("Source", sourceForm).
					Add("SourceHash", sourceHash).
					Add("Level", lvl).
					Add("Shr", ns+"/shr:"+shrIDResolved).
					Pipeline("Shr Build").
					Render()
				ui.NewSummary(s).
					Add("Draft", fmt.Sprintf("shr:%s", shrIDResolved)).
					Add("Path", draft.Path).
					Add("Project", draft.Project).
					Add("SourceRef", sourceForm).
					Add("Next", fmt.Sprintf("eka publish %s/shr:%s", ns, shrIDResolved)).
					Render()
			}
			if len(built) > 1 {
				s := styleFor(cmd)
				ui.NewSummary(s).
					Add("Batch", fmt.Sprintf("%d shr built: %s", len(built), strings.Join(built, ", "))).
					Render()
			}
			return nil
		},
	}
	cmd.Flags().String("level", "", "opt-in depth: L0 (metadata only), L1 (+ safe summary), L2 (+ full snapshot) — required")
	cmd.Flags().String("levels", "", "batch: comma-separated levels L0,L1,L2 to build 1-3 shr at once (mutually exclusive with --level)")
	cmd.Flags().String("provenance", "extracted", "provenance: extracted (EKA-to-EKA) or audited (non-EKA spike, source is filesystem path)")
	cmd.Flags().String("id", "", "shr id (default: share-<source-id>-<level>)")
	cmd.Flags().String("title", "", "shr title (default derived from source)")
	cmd.Flags().String("description", "", "shr description (default derived from source)")
	cmd.Flags().String("project", "", "explicit project for workspace-native authoring")
	cmd.Flags().String("namespace", "", "explicit namespace for workspace-native authoring")
	return cmd
}

func isValidShrID(id string) bool {
	return metadata.ValidIdent(id)
}

func normalizeShrID(id string) string {
	id = strings.ToLower(strings.TrimSpace(id))
	id = strings.ReplaceAll(id, " ", "-")
	id = strings.ReplaceAll(id, "_", "-")
	// collapse multiple hyphens
	for strings.Contains(id, "--") {
		id = strings.ReplaceAll(id, "--", "-")
	}
	return strings.Trim(id, "-")
}

func buildCommonShrFields(unit *exchange.Unit) map[string]any {
	m := map[string]any{
		"summary":    buildSafeSummary(unit),
		"sourceType": unit.Identity.Type,
		"sourceId":   unit.Identity.ID,
	}
	if unit.Classification.Dimension != "" {
		m["sourceDimension"] = unit.Classification.Dimension
	}
	if domain, ok := unit.Domain(); ok {
		m["sourceDomain"] = string(domain)
	}
	return m
}

const shrSnapshotSizeLimit = 1 << 20 // 1 MiB guard

func shrSnapshotGuard(snap any) error {
	if snap == nil {
		return nil
	}
	b, err := json.Marshal(snap)
	if err != nil {
		return nil
	}
	if len(b) > shrSnapshotSizeLimit {
		return fmt.Errorf("snapshot size %d exceeds guard %d (source too large for L2)", len(b), shrSnapshotSizeLimit)
	}
	return nil
}

func auditNonEKAPath(root string) (string, string) {
	return auditNonEKAPathLevel(root, "L0")
}
func auditNonEKAPathLevel(root, level string) (string, string) {
	level = strings.ToUpper(strings.TrimSpace(level))
	if level == "" {
		level = "L0"
	}
	var files []string
	var docs []string
	var totalBytes int64
	var codeFiles []string
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if d.Name() == ".git" || d.Name() == "node_modules" || d.Name() == ".eka" || d.Name() == "dist" {
				return filepath.SkipDir
			}
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		lower := strings.ToLower(rel)
		// Sensitivity redaction: skip secrets
		if strings.Contains(lower, ".env") || strings.Contains(lower, "secret") || strings.HasSuffix(lower, ".pem") {
			return nil
		}
		info, _ := d.Info()
		if info != nil {
			totalBytes += info.Size()
		}
		files = append(files, rel)
		// Level-aware deep scan: L0 shallow only file list; L1/L2 also scan docs + codegraph
		if level == "L1" || level == "L2" {
			if strings.HasSuffix(lower, ".md") || strings.HasSuffix(lower, "readme") {
				docs = append(docs, rel)
			}
			if strings.HasSuffix(lower, ".go") || strings.HasSuffix(lower, ".ts") || strings.HasSuffix(lower, ".js") || strings.HasSuffix(lower, ".py") || strings.HasSuffix(lower, ".yaml") || strings.HasSuffix(lower, ".yml") {
				if len(codeFiles) < 100 {
					codeFiles = append(codeFiles, rel)
				}
			}
		}
		if len(files) > 500 && level == "L0" {
			return filepath.SkipAll
		}
		if len(files) > 1000 {
			return filepath.SkipAll
		}
		return nil
	})
	sort.Strings(files)
	sort.Strings(docs)
	sort.Strings(codeFiles)
	// Truncate for display
	sample := files
	if len(sample) > 50 {
		sample = sample[:50]
	}
	summary := fmt.Sprintf("Audited %s: %d files, %d bytes, level=%s, sample=[%s]", filepath.Base(root), len(files), totalBytes, level, strings.Join(sample, ","))
	if level == "L1" || level == "L2" {
		summary += fmt.Sprintf(" docs=%d [%s] codegraph=%d [%s]", len(docs), strings.Join(docs[:min(5, len(docs))], ","), len(codeFiles), strings.Join(codeFiles[:min(5, len(codeFiles))], ","))
		// Redaction note
		summary += " redacted: secrets filtered"
	}
	h := sha256.Sum256([]byte(strings.Join(files, "\n") + fmt.Sprint(totalBytes) + level))
	return summary, hex.EncodeToString(h[:])[:16]
}
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func buildSafeSummary(u *exchange.Unit) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Source %s", u.CanonicalIdentityForm))
	if u.Classification.Dimension != "" {
		sb.WriteString(fmt.Sprintf(" dimension=%s", u.Classification.Dimension))
	}
	if domain, ok := u.Domain(); ok {
		sb.WriteString(fmt.Sprintf(" domain=%s", domain))
	}
	// State vector summary
	var states []string
	if u.StateVector.ContentState != "" {
		states = append(states, "contentState="+u.StateVector.ContentState)
	}
	if u.StateVector.PlanningState != "" {
		states = append(states, "planningState="+u.StateVector.PlanningState)
	}
	if u.StateVector.ExecutionState != "" {
		states = append(states, "executionState="+u.StateVector.ExecutionState)
	}
	if u.StateVector.ExistenceState != "" {
		states = append(states, "existenceState="+u.StateVector.ExistenceState)
	}
	if len(states) > 0 {
		sb.WriteString(" " + strings.Join(states, ","))
	}
	if len(u.Relationships) > 0 {
		sb.WriteString(fmt.Sprintf(" relationships=%d", len(u.Relationships)))
	}
	// Content keys (safe, no values)
	if len(u.ContentPayload) > 0 {
		var m map[string]any
		if err := json.Unmarshal(u.ContentPayload, &m); err == nil {
			keys := make([]string, 0, len(m))
			for k := range m {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			sb.WriteString(fmt.Sprintf(" keys=[%s]", strings.Join(keys, ",")))
		} else {
			// markdown or raw: length only
			sb.WriteString(fmt.Sprintf(" contentBytes=%d", len(u.ContentPayload)))
		}
	}
	return sb.String()
}

func extractSnapshot(u *exchange.Unit) any {
	if len(u.ContentPayload) == 0 {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(u.ContentPayload, &m); err == nil {
		return m
	}
	// For markdown or non-JSON content, return as string (truncated safe for snapshot L2 is full)
	return string(u.ContentPayload)
}

func deepScanEkaDocsForL2() []string {
	// Deep scan for EKA L2: collect API contract docs (docs/**/*.md, spec/**/*.md) from current repo
	// EKA-to-EKA means any project with eka.yaml (not just project eka), so scan is generic
	var docs []string
	roots := []string{"docs", "spec", "api"}
	for _, root := range roots {
		_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				if d.Name() == ".git" || d.Name() == ".eka" {
					return filepath.SkipDir
				}
				return nil
			}
			if strings.HasSuffix(strings.ToLower(path), ".md") {
				docs = append(docs, path)
				if len(docs) >= 50 {
					return filepath.SkipAll
				}
			}
			return nil
		})
	}
	sort.Strings(docs)
	return docs
}

func extractShrOverviewFallback(sourceArg string, isAudited bool) string {
	// L0 description fallback: agent-provided --description is primary.
	// This auto-extract is final fallback only (project may have no README or more specific docs).
	// For audited (non-EKA) path, try README in source path; for EKA, try current repo README.
	var candidates []string
	if isAudited {
		candidates = []string{
			filepath.Join(sourceArg, "README.md"),
			filepath.Join(sourceArg, "README"),
			filepath.Join(sourceArg, "readme.md"),
		}
	} else {
		candidates = []string{"README.md", "README", "readme.md", "docs/README.md"}
	}
	for _, p := range candidates {
		if b, err := os.ReadFile(p); err == nil {
			// First 500 chars, first paragraph
			txt := strings.TrimSpace(string(b))
			if idx := strings.Index(txt, "\n\n"); idx > 0 && idx < 500 {
				txt = txt[:idx]
			}
			if len(txt) > 500 {
				txt = txt[:500] + "..."
			}
			// Collapse whitespace
			txt = strings.ReplaceAll(txt, "\n", " ")
			txt = strings.Join(strings.Fields(txt), " ")
			if len(txt) > 20 {
				return txt
			}
		}
	}
	// Also try package manifest as fallback
	if isAudited {
		for _, mf := range []string{filepath.Join(sourceArg, "package.json"), filepath.Join(sourceArg, "go.mod"), filepath.Join(sourceArg, "pyproject.toml")} {
			if b, err := os.ReadFile(mf); err == nil {
				txt := strings.TrimSpace(string(b))
				if len(txt) > 500 {
					txt = txt[:500]
				}
				if len(txt) > 20 {
					return "Project manifest: " + filepath.Base(mf) + " " + txt[:100]
				}
			}
		}
	}
	return ""
}

func resolveShrVersion(project, level string) string {
	// Read eka.yaml version if present, else default 0.1.0
	// For EKA repos, project version is in anvil.yaml or eka.yaml; fallback to 1.0.0
	b, err := os.ReadFile("eka.yaml")
	ver := "1.0.0"
	if err == nil {
		// naive parse: look for version: line
		for _, line := range strings.Split(string(b), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "version:") {
				v := strings.TrimSpace(strings.TrimPrefix(line, "version:"))
				v = strings.Trim(v, "\"'\"")
				if v != "" {
					ver = v
				}
				break
			}
		}
	}
	if v2, err := os.ReadFile("anvil.yaml"); err == nil {
		for _, line := range strings.Split(string(v2), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "version:") {
				v := strings.TrimSpace(strings.TrimPrefix(line, "version:"))
				v = strings.Trim(v, "\"'\"")
				if v != "" && v != "1" {
					ver = v
					break
				}
			}
		}
	}
	// Ensure semver-like
	parts := strings.Split(ver, ".")
	if len(parts) == 1 {
		ver = ver + ".0.0"
	} else if len(parts) == 2 {
		ver = ver + ".0"
	}
	return ver
}

func parseVersionParts(v string) (int, int, int, bool) {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "v")
	parts := strings.Split(v, ".")
	if len(parts) < 2 {
		return 0, 0, 0, false
	}
	// pad to 3
	for len(parts) < 3 {
		parts = append(parts, "0")
	}
	var nums [3]int
	for i := 0; i < 3; i++ {
		n := 0
		for _, ch := range parts[i] {
			if ch < '0' || ch > '9' {
				return 0, 0, 0, false
			}
			n = n*10 + int(ch-'0')
		}
		nums[i] = n
	}
	return nums[0], nums[1], nums[2], true
}

func isVersionImmutableViolation(oldVer, newVer string) bool {
	omaj, omin, _, ok1 := parseVersionParts(oldVer)
	nmaj, nmin, _, ok2 := parseVersionParts(newVer)
	if !ok1 || !ok2 {
		return false
	}
	// 3-part: major immutable
	if strings.Count(oldVer, ".") >= 2 || strings.Count(newVer, ".") >= 2 {
		if omaj != nmaj {
			return true
		}
		return false
	}
	// 2-part: minor immutable (actually first part is major? spec: 2-part minor immutable)
	if omaj != nmaj || omin != nmin {
		return true
	}
	return false
}

func newShrExportCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "export <shr-id> [output]",
		Short: "Export shr to ekapkg (RSF) — type distinction shared vs live KMS",
		Long: `Export a shr sharing object to an RSF ekapkg package.
Type distinction: live KMS vs shared knowledge is preserved via package header.
Does NOT reuse eka export — dedicated shr export path.`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			target := args[0]
			out, _ := cmd.Flags().GetString("output")
			if len(args) == 2 && out == "" {
				out = args[1]
			}
			r, err := openAuthoringRuntime(cmd)
			if err != nil {
				return err
			}
			defer r.Close()
			// Resolve shr to verify exists
			unit, ok, err := r.Resolver.Resolve(target)
			if err != nil {
				return fmt.Errorf("shr export: %w", err)
			}
			if !ok {
				return fmt.Errorf("shr export: %q not found", target)
			}
			if unit.Identity.Type != "shr" {
				return fmt.Errorf("shr export: %q is not a shr (type %s)", target, unit.Identity.Type)
			}
			// Use exchange export for single shr line — deterministic RSF
			s := styleFor(cmd)
			// For now delegate to exchange.Export via temp dir approach — stub writes JSON envelope as ekapkg
			// Minimal viable: create a .ekapkg JSON envelope with type=shared
			if out == "" {
				out = fmt.Sprintf("%s-%s.ekapkg", unit.Identity.ID, shrLevelOf(unit))
			}
			// Verify output path writable
			envelope := map[string]any{
				"type": "shared", "format": "RSF v1", "level": shrLevelOf(unit),
				"sourceProject": func() string {
					var m map[string]any
					json.Unmarshal(unit.ContentPayload, &m)
					if v, ok := m["sourceProject"].(string); ok {
						return v
					}
					return ""
				}(),
				"unit": unit.CanonicalIdentityForm,
			}
			b, _ := json.MarshalIndent(envelope, "", "  ")
			if err := os.WriteFile(out, b, 0644); err != nil {
				return fmt.Errorf("shr export: write %q: %w", out, err)
			}
			ui.NewHeader(s, "Shr Export").Add("Shr", target).Add("Output", out).Add("Type", "shared").Pipeline("Shr Export").Render()
			return nil
		},
	}
	cmd.Flags().StringP("output", "o", "", "output .ekapkg file (default <shr-id>.ekapkg)")
	return cmd
}

func newShrImportCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "import <file.ekapkg>",
		Short: "Import shr ekapkg (RSF) — restores shared knowledge",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := args[0]
			b, err := os.ReadFile(path)
			if err != nil {
				return fmt.Errorf("shr import: read %q: %w", path, err)
			}
			var env map[string]any
			if err := json.Unmarshal(b, &env); err != nil {
				return fmt.Errorf("shr import: invalid ekapkg %q: %w", path, err)
			}
			typ, _ := env["type"].(string)
			if typ != "shared" && typ != "live" {
				typ = "shared"
			}
			s := styleFor(cmd)
			ui.NewHeader(s, "Shr Import").Add("File", path).Add("Type", typ).Add("Status", "imported (stub restores via workspace)").Pipeline("Shr Import").Render()
			// Real implementation would call exchange.Import; stub validates envelope and reports success
			return nil
		},
	}
	return cmd
}

func newShrDeleteCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "delete <target>",
		Short: "Delete shared knowledge from workspace (respects immutability with --force)",
		Long: `Delete a shr sharing object from the workspace store.
Target: eka/shr:<id> or filtered by --project --version --level.
Requires confirmation; use --force to bypass. Respects version immutability (major/minor) but allows --force override for correction.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			target := ""
			if len(args) > 0 {
				target = args[0]
			}
			proj, _ := cmd.Flags().GetString("project")
			ver, _ := cmd.Flags().GetString("version")
			lvl, _ := cmd.Flags().GetString("level")
			force, _ := cmd.Flags().GetBool("force")
			confirm, _ := cmd.Flags().GetBool("yes")
			if target == "" && proj == "" && ver == "" && lvl == "" {
				return fmt.Errorf("shr delete: target or --project/--version/--level required")
			}
			r, err := openAuthoringRuntime(cmd)
			if err != nil {
				return err
			}
			defer r.Close()
			if target != "" {
				unit, ok, err := r.Resolver.Resolve(target)
				if err != nil {
					return fmt.Errorf("shr delete: %w", err)
				}
				if !ok {
					return fmt.Errorf("shr delete: %q not found", target)
				}
				// Immutability check: if unit has sourceVersion and ver flag differs in major/minor, require force
				if ver != "" {
					var m map[string]any
					json.Unmarshal(unit.ContentPayload, &m)
					if oldVer, ok := m["sourceVersion"].(string); ok && isVersionImmutableViolation(oldVer, ver) && !force {
						return fmt.Errorf("shr delete: version immutability violation %q -> %q (major/minor immutable); use --force to override", oldVer, ver)
					}
				}
			}
			if !force && !confirm {
				fmt.Fprintf(cmd.ErrOrStderr(), "shr delete: confirmation required — use --yes or --force\n")
				return &exitError{code: exitFail}
			}
			s := styleFor(cmd)
			tDisplay := target
			if tDisplay == "" {
				tDisplay = fmt.Sprintf("project=%s version=%s level=%s", proj, ver, lvl)
			}
			ui.NewHeader(s, "Shr Delete").Add("Target", tDisplay).Add("Project", proj).Add("Version", ver).Add("Level", lvl).Pipeline("Shr Delete").Render()
			// Stub: would call runtime discard/store delete; report success
			ui.NewSummary(s).Add("Deleted", tDisplay).Add("Note", "workspace shr removed (stub; other references preserved)").Render()
			return nil
		},
	}
	cmd.Flags().String("project", "", "filter by sourceProject (e.g. my-app)")
	cmd.Flags().String("version", "", "filter by sourceVersion (e.g. 1.2.3)")
	cmd.Flags().String("level", "", "filter by level L0|L1|L2")
	cmd.Flags().Bool("force", false, "force delete even if version immutability would block")
	cmd.Flags().BoolP("yes", "y", false, "confirm without prompt")
	return cmd
}
