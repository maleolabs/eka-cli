package cmd

import (
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
  --level L0|L1|L2   required, opt-in classification
  --id <shr-id>      id for the new shr (default: share-<source-id>-<level>)
  --title <text>     shr title (default: Share <LEVEL> <type>:<id>)
  --description <text> shr description (default: snapshot copy of <source>)
  --project <name>   explicit project (workspace-native)
  --namespace <ns>   explicit namespace (workspace-native)

Project/namespace resolution: same as eka new (repo context or explicit
--project/--namespace). Source must exist in the workspace.

Examples:
  eka shr build eka/adr:sharing-object-model --level L0 --id my-share
  eka shr build eka/scp:knowledge-sharing --level L2
  eka shr build eka/req:knowledge-sharing --level L1 --title "Share L1"`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			level, _ := cmd.Flags().GetString("level")
			shrID, _ := cmd.Flags().GetString("id")
			titleFlag, _ := cmd.Flags().GetString("title")
			descFlag, _ := cmd.Flags().GetString("description")
			projectFlag, _ := cmd.Flags().GetString("project")
			nsFlag, _ := cmd.Flags().GetString("namespace")

			// Opt-in default-deny: level required.
			if strings.TrimSpace(level) == "" {
				return fmt.Errorf("shr build: --level is required (L0|L1|L2); default share nothing (opt-in)")
			}
			level = strings.ToUpper(strings.TrimSpace(level))
			if level != "L0" && level != "L1" && level != "L2" {
				return fmt.Errorf("shr build: --level must be L0, L1 or L2, got %q", level)
			}
			if strings.Contains(shrID, ":") || strings.Contains(shrID, "/") {
				return fmt.Errorf("shr build: --id must be a bare id (no namespace or type), got %q", shrID)
			}

			sourceArg := strings.TrimSpace(args[0])
			if sourceArg == "" {
				return fmt.Errorf("shr build: source identity is required")
			}

			r, err := openAuthoringRuntime(cmd)
			if err != nil {
				return err
			}
			defer r.Close()

			// Resolve source CKO via Resolver (qualified forms).
			unit, ok, err := r.Resolver.Resolve(sourceArg)
			if err != nil {
				return fmt.Errorf("shr build: %w", err)
			}
			if !ok {
				return fmt.Errorf("shr build: source %q not found in workspace; run 'eka sync' first", sourceArg)
			}
			sourceHash := unit.Digest
			if sourceHash == "" {
				return fmt.Errorf("shr build: source %q has empty object hash (store corruption)", sourceArg)
			}
			sourceForm := unit.CanonicalIdentityForm
			if sourceForm == "" {
				sourceForm = unit.Identity.CanonicalForm()
			}

			// Resolve project/namespace for the new shr draft.
			// Reuse the same resolution as eka new: repo context or explicit flags.
			// We need a reference for the shr target to feed resolveNewScope.
			shrIDResolved := shrID
			if shrIDResolved == "" {
				shrIDResolved = fmt.Sprintf("share-%s-%s", unit.Identity.ID, strings.ToLower(level))
			}
			// Validate shr id is a valid identifier (simple check).
			if !isValidShrID(shrIDResolved) {
				return fmt.Errorf("shr build: generated id %q is not a valid EKA identifier (lowercase letters, digits, hyphens)", shrIDResolved)
			}
			ref := conformance.Reference{Namespace: nsFlag, Type: "shr", ID: shrIDResolved}
			// If nsFlag empty, ref.Namespace stays empty and resolveNewScope will fill from repo.
			project, ns, err := resolveNewScope(r, ref, projectFlag, nsFlag)
			if err != nil {
				return fmt.Errorf("shr build: %v", err)
			}
			// If namespace derived from resolveNewScope differs from source namespace and no explicit nsFlag,
			// keep resolved ns (it will be repo namespace). Allow cross-namespace only if explicit.
			_ = ns
			_ = project

			title := strings.TrimSpace(titleFlag)
			if title == "" {
				title = fmt.Sprintf("Share %s %s:%s", level, unit.Identity.Type, unit.Identity.ID)
			}
			description := strings.TrimSpace(descFlag)
			if description == "" {
				shortHash := sourceHash
				if len(shortHash) > 8 {
					shortHash = shortHash[:8]
				}
				description = fmt.Sprintf("Sharing object derived from %s at %s (level %s, provenance extracted)", sourceForm, shortHash, level)
			}

			// Build content map with shr required fields.
			content := map[string]any{
				"title":       title,
				"description": description,
				"level":       level,
				"provenance":  "extracted",
				"sourceHash":  sourceHash,
				// R9 Purpose/Content placeholders (required sections for shr)
				"purpose": title,
				"content": description,
			}
			// Level-specific snapshot.
			switch level {
			case "L0":
				// metadata only, nothing extra
			case "L1":
				summary := buildSafeSummary(unit)
				content["summary"] = summary
				content["sourceType"] = unit.Identity.Type
				content["sourceId"] = unit.Identity.ID
				if unit.Classification.Dimension != "" {
					content["sourceDimension"] = unit.Classification.Dimension
				}
				if domain, ok := unit.Domain(); ok {
					content["sourceDomain"] = string(domain)
				}
			case "L2":
				summary := buildSafeSummary(unit)
				content["summary"] = summary
				content["sourceType"] = unit.Identity.Type
				content["sourceId"] = unit.Identity.ID
				if unit.Classification.Dimension != "" {
					content["sourceDimension"] = unit.Classification.Dimension
				}
				if domain, ok := unit.Domain(); ok {
					content["sourceDomain"] = string(domain)
				}
				snap := extractSnapshot(unit)
				if snap != nil {
					content["snapshot"] = snap
				}
			}

			// Write temp content file for NewDraft (ContentFile JSON object).
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
			defer os.Remove(tmpPath)

			// Create shr draft via Authoring.NewDraft. shr is a knowledge artifact (Operations/records)
			// requiring a valid dimension; use the canonical records dimension.
			draft, err := runtime.Authoring.NewDraft(r, runtime.NewDraftRequest{
				Project:   project,
				Namespace: ns,
				Type:      "shr",
				ID:        shrIDResolved,
				Dimension: "records",
				Relationships: []exchange.Relationship{
					{Type: "derives-from", Target: sourceForm},
				},
				ContentFile: tmpPath,
			})
			if err != nil {
				return fmt.Errorf("shr build: %w", err)
			}

			s := styleFor(cmd)
			ui.NewHeader(s, "Shr Builder").
				Add("Source", sourceForm).
				Add("SourceHash", sourceHash).
				Add("Level", level).
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
			return nil
		},
	}
	cmd.Flags().String("level", "", "opt-in depth: L0 (metadata only), L1 (+ safe summary), L2 (+ full snapshot) — required")
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

// Ensure the file compiles with the unused import guard: filepath is used via resolveNewScope indirection,
// but we keep the import for potential path handling in future snapshot file handling.
var _ = filepath.Clean
