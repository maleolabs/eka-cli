package bootstrap

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"github.com/maleolabs/eka-core/metadata"
)

// This file implements the AGENTS.md workflow-context management of
// `eka init` — the Go-native port of scripts/agents-md-sync.sh (the
// shell script is retired: one language, testable merge, no
// repo-relative script dependency). The managed region is delimited by
// fixed markers; everything outside them is user content and is never
// touched.
//
// Merge contract (identical to the retired script):
//   - missing file → create it holding only the block;
//   - markers present → replace the marked region (drift reconciled);
//   - no markers → append the block after a blank line.
// The marked region is auto-managed: replacing it needs no
// confirmation, so re-running `eka init` backfills a missing file and
// reconciles drift without ever touching user content.

// AgentsMDName is the file name of the agent context file.
const AgentsMDName = "AGENTS.md"

// AgentsMD markers delimit the auto-managed region.
const (
	agentsMDMarkerStart = "<!-- eka:workflow:start -->"
	agentsMDMarkerEnd   = "<!-- eka:workflow:end -->"
)

// buildAgentsMDBlock returns the exact managed block bytes for the
// given identity: the marker-delimited workflow context the retired
// shell script wrote. The block carries NO trailing newline — the
// merge step owns line separation, so replacing a marked region never
// duplicates (or collapses) the user's surrounding blank lines. The
// active container is always "none" here — init runs before any
// workspace/store exists, so no container can be resolved;
// determinism (same inputs → same bytes) beats a fragile live lookup.
func buildAgentsMDBlock(project, namespace string) []byte {
	var b bytes.Buffer
	b.WriteString(agentsMDMarkerStart + "\n")
	b.WriteString("## EKA Workflow Context (auto-managed, do not edit between markers)\n")
	fmt.Fprintf(&b, "Project: %s | Namespace: %s | Active ctr: none\n", project, namespace)
	b.WriteString("Commands: eka get <form>, eka context <subject>, eka transition <target> <to>, eka new/publish, eka sync push\n")
	b.WriteString("Skills: eka-orientation, eka-knowledge-retrieval, eka-knowledge-authoring, eka-engineering-workflow\n")
	b.WriteString(agentsMDMarkerEnd)
	return b.Bytes()
}

// mergeAgentsMD reconciles existing file bytes with the managed block:
// missing content yields the block plus one trailing newline; a marked
// region is replaced verbatim (surrounding bytes untouched); unmarked
// content gains the block appended after a blank line. A nil return
// with no error means "already current" (reuse).
func mergeAgentsMD(existing []byte, block []byte) []byte {
	if len(existing) == 0 {
		return append(append([]byte(nil), block...), '\n')
	}
	start := bytes.Index(existing, []byte(agentsMDMarkerStart))
	end := bytes.Index(existing, []byte(agentsMDMarkerEnd))
	if start >= 0 && end > start {
		end += len(agentsMDMarkerEnd)
		// Verbatim splice: the block carries no trailing newline, so
		// whatever followed the end marker is preserved exactly and a
		// current file reconciles to itself (fixed point).
		merged := append([]byte(nil), existing[:start]...)
		merged = append(merged, block...)
		merged = append(merged, existing[end:]...)
		if bytes.Equal(merged, existing) {
			return nil
		}
		return merged
	}
	merged := append([]byte(nil), bytes.TrimRight(existing, "\n")...)
	merged = append(merged, "\n\n"...)
	merged = append(merged, block...)
	merged = append(merged, '\n')
	if bytes.Equal(merged, existing) {
		return nil
	}
	return merged
}

// planAgentsMD appends the AGENTS.md step to plan when enabled: missing
// files and drifted/foreign blocks plan a merge write, current files
// plan reuse. The Content carries the exact merged bytes so the plan
// stays self-contained (dry-run preview and generation agree).
func planAgentsMD(plan []Action, d *Discovery, a Answers) []Action {
	if !a.AgentsMD {
		return plan
	}
	project, namespace := identityForAgentsMD(d, a)
	path := filepath.Join(d.AbsTarget, AgentsMDName)
	existing, err := os.ReadFile(path)
	if err != nil {
		return append(plan, Action{Kind: ActionAgentsMDMerge, Path: AgentsMDName, Content: mergeAgentsMD(nil, buildAgentsMDBlock(project, namespace))})
	}
	if merged := mergeAgentsMD(existing, buildAgentsMDBlock(project, namespace)); merged != nil {
		return append(plan, Action{Kind: ActionAgentsMDMerge, Path: AgentsMDName, Content: merged})
	}
	return append(plan, Action{Kind: ActionReuse, Path: AgentsMDName})
}

// identityForAgentsMD resolves the identity the managed block carries:
// the existing eka.yaml is the authority when present and parseable
// (the retired shell script grepped the same fields) — re-runs then
// reconcile against the frozen identity instead of the derived one;
// otherwise the plan answers apply (fresh targets).
func identityForAgentsMD(d *Discovery, a Answers) (project, namespace string) {
	if raw, err := os.ReadFile(filepath.Join(d.AbsTarget, "eka.yaml")); err == nil {
		if m, perr := metadata.Parse(raw); perr == nil && m.Project != "" && m.Namespace != "" {
			return m.Project, m.Namespace
		}
	}
	return a.Project, a.Namespace
}
