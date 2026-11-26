package bootstrap

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/maleolabs/eka-core/contexts"
	"github.com/maleolabs/eka-core/metadata"
)

// This file implements the AGENTS.md workflow-context management of
// `eka init` and `eka agents-md` — the Go-native port of
// scripts/agents-md-sync.sh (the shell script is retired: one language,
// testable merge, no repo-relative script dependency). The managed
// region is delimited by fixed markers; everything outside them is user
// content and is never touched.
//
// Merge contract (identical to the retired script):
//   - missing file → create it holding only the block;
//   - markers present → replace the marked region (drift reconciled);
//   - no markers → append the block after a blank line.
// The marked region is auto-managed: replacing it needs no
// confirmation, so re-running `eka init` (or `eka agents-md --write`)
// backfills a missing file and reconciles drift without ever touching
// user content.
//
// ONE generator serves both commands: BuildAgentsMDBlock, fed by the
// same ConstraintsFromSnapshot index (the committed snapshot at
// <repo>/exchange/snapshots). `eka init` passes that index too (nil on a
// fresh repository with no committed snapshot → the identity-only
// block); `eka agents-md` reads the same snapshot and regenerates. The
// same repository state produces byte-identical bytes from both, so
// `--check` passes right after `init` and a re-run of `init` never
// strips the index an earlier `agents-md --write` added.

// AgentsMDName is the file name of the agent context file.
const AgentsMDName = "AGENTS.md"

// AgentsMD markers delimit the auto-managed region.
const (
	agentsMDMarkerStart = "<!-- eka:workflow:start -->"
	agentsMDMarkerEnd   = "<!-- eka:workflow:end -->"
)

// AgentsMDBlockBudget is the byte budget of the managed block's
// constraint index: index lines are rendered in sorted order until the
// cap is hit, then truncated deterministically (see BuildAgentsMDBlock).
// The budget binds the INDEX; the fixed prose and the identity line are
// outside it (EKA identifiers are uncapped, so an over-long identity
// drops the index entirely rather than exceed this number with a
// dangling line — the proviso is documented on BuildAgentsMDBlock).
const AgentsMDBlockBudget = 3200

// AgentsMDTruncatedPrefix begins the deterministic truncation line the
// block carries when the constraint index does not fit the budget
// ("... +N more (eka status)"). Exported so callers and tests can
// recognise the marker without hardcoding it.
const AgentsMDTruncatedPrefix = "... +"

// agentsMDConstraintsHeading heads the constraint index. It is present
// only when the index carries at least one type group.
const agentsMDConstraintsHeading = "Constraints (Discovery + Architecture, stratum 1-2; index only — read one with `eka get <ns>/<type>:<id>`):"

// agentsMDIndexLine is one rendered constraint-index line with the
// number of (type,id) entries it carries (for the truncation count).
type agentsMDIndexLine struct {
	text    string
	entries int
}

// BuildAgentsMDBlock returns the exact managed block bytes for the
// given identity and constraint index: the marker-delimited workflow
// context. The second return value is the number of omitted index
// entries (0 when everything fit) — the real truncation signal; callers
// (e.g. `eka agents-md --json`) report it without re-deriving. The
// block carries NO trailing newline — the merge step owns line
// separation, so replacing a marked region never duplicates (or
// collapses) the user's surrounding blank lines.
//
// The prose is fixed; only the identity line and the constraint index
// vary. An empty index (no committed snapshot) omits the index heading
// and lines entirely — the command, not the block, reports the reason.
// The index is bounded by AgentsMDBlockBudget: lines are added in order
// until the cap, then truncated with one deterministic
// "... +N more (eka status)" line (N = omitted entries). When the fixed
// prose plus the identity leaves no room even for the heading and the
// truncation line, the index is omitted entirely (never a heading with
// an empty or dangling body); when individual lines are simply too long,
// the heading plus the truncation line is still rendered.
//
// Proviso: the budget bounds the INDEX, not the fixed prose+identity.
// EKA identifiers have no length cap (metadata.ValidIdent imposes none),
// so a pathological project/namespace can drive the identity-only block
// past AgentsMDBlockBudget; the index is then omitted rather than paying
// a dangling truncation line. Determinism (same inputs → same bytes) is
// the contract.
func BuildAgentsMDBlock(project, namespace string, index []contexts.ConstraintGroup) ([]byte, int) {
	var b bytes.Buffer
	b.WriteString(agentsMDMarkerStart + "\n")
	b.WriteString("## EKA Workflow Context (auto-managed; edit outside the markers only)\n")
	fmt.Fprintf(&b, "Project: %s | Namespace: %s | Position: `eka status`\n", project, namespace)
	b.WriteString("Knowledge lives in the canonical store, not repo files. Loop: Understand -> Context -> Reason -> Change -> Validate -> Publish.\n")
	b.WriteString("Read: `eka status` · `eka get <ns>/<type>:<id>` · `eka context <subject> --depth engineering` · `eka view <projection>`.\n")
	b.WriteString("Change: `eka new` -> `eka validate` -> `eka publish`; `eka transition <line> <to>`; evidence `eka note --role <role>`.\n")
	b.WriteString("Rules: never edit store data directly; transitions are gated and forward-only (read real state via `eka get`); after each transition run `eka sync push`.\n")
	b.WriteString("Skills: `eka-router` first, then `eka-orientation` (mental model), `eka-engineering-workflow` (loop), `eka-troubleshooting` (refusals).\n")
	lines := agentsMDIndexLines(index)
	kept, omitted, ok := agentsMDFitIndex(lines, b.Len()+len(agentsMDMarkerEnd))
	if !ok {
		// The fixed part left no room for the heading plus at least one
		// index line (or the truncation line): render no index at all.
		b.WriteString(agentsMDMarkerEnd)
		return b.Bytes(), 0
	}
	b.WriteString(agentsMDConstraintsHeading + "\n")
	for _, line := range kept {
		b.WriteString(line.text + "\n")
	}
	if omitted > 0 {
		fmt.Fprintf(&b, "%s%d more (eka status)\n", AgentsMDTruncatedPrefix, omitted)
	}
	b.WriteString(agentsMDMarkerEnd)
	return b.Bytes(), omitted
}

// agentsMDIndexLines renders the index to one line per type group;
// groups with no ids are skipped (defensive — they carry nothing).
func agentsMDIndexLines(index []contexts.ConstraintGroup) []agentsMDIndexLine {
	lines := make([]agentsMDIndexLine, 0, len(index))
	for _, e := range index {
		if len(e.IDs) == 0 {
			continue
		}
		lines = append(lines, agentsMDIndexLine{
			text:    e.Type + ": " + strings.Join(e.IDs, " "),
			entries: len(e.IDs),
		})
	}
	return lines
}

// agentsMDFitIndex selects the index lines that fit the budget, given
// the bytes already consumed before the heading (used). It keeps whole
// lines in order; when a line would overflow, it stops and reserves room
// for the truncation line, dropping kept lines until it fits. The second
// return value is the number of omitted entries (0 when everything fit).
//
// The third value reports whether the index can be rendered at all: false
// means the fixed prose plus the heading left no room even for the
// truncation line (an over-long identity edge), so the caller renders NO
// index — never a heading with an empty or dangling body. When the index
// is renderable all returns are true, even with zero kept lines (every
// line too long → heading plus one truncation line naming the omitted
// entries, a consistent truncated index).
func agentsMDFitIndex(lines []agentsMDIndexLine, used int) ([]agentsMDIndexLine, int, bool) {
	if len(lines) == 0 {
		return nil, 0, false
	}
	fill := used + len(agentsMDConstraintsHeading) + 1 // heading + newline
	kept := 0
	for kept < len(lines) && fill+len(lines[kept].text)+1 <= AgentsMDBlockBudget {
		fill += len(lines[kept].text) + 1
		kept++
	}
	if kept == len(lines) {
		return lines, 0, true
	}
	for kept > 0 {
		omitted := agentsMDOmittedEntries(lines[kept:])
		if fill+len(agentsMDTruncationLine(omitted))+1 <= AgentsMDBlockBudget {
			return lines[:kept], omitted, true
		}
		kept--
		fill -= len(lines[kept].text) + 1
	}
	// Every line overflowed the budget. A heading plus one truncation
	// line is still a consistent index (nothing shown, all omitted); if
	// even that does not fit, render no index at all.
	omitted := agentsMDOmittedEntries(lines)
	if fill+len(agentsMDTruncationLine(omitted))+1 <= AgentsMDBlockBudget {
		return nil, omitted, true
	}
	return nil, 0, false
}

// agentsMDOmittedEntries sums the entry counts of the given lines.
func agentsMDOmittedEntries(lines []agentsMDIndexLine) int {
	n := 0
	for _, line := range lines {
		n += line.entries
	}
	return n
}

// agentsMDTruncationLine renders the deterministic truncation line
// ("... +N more (eka status)") without its trailing newline.
func agentsMDTruncationLine(omitted int) string {
	return fmt.Sprintf("%s%d more (eka status)", AgentsMDTruncatedPrefix, omitted)
}

// RenderAgentsMDBlock reads the repository identity (eka.yaml) and the
// committed-snapshot constraint index at <root>/exchange/snapshots, and
// renders the managed AGENTS.md block — the ONE generator both `eka
// agents-md` and `eka verify` call, so the drift comparison always uses
// the same block the write path produces. It returns the block bytes,
// the number of omitted index entries (0 when everything fit), the
// number of index entries, and whether the snapshot directory is missing
// (the identity-only-block case; a MISSING snapshot is not an error).
//
// An unreadable or invalid eka.yaml, or a snapshot that fails to load, is
// returned as an error whose text already names the failing step
// ("cannot read eka.yaml", "invalid eka.yaml", "cannot read the committed
// snapshot"); callers add their own command prefix. The snapshot index is
// the index for reading only — callers never touch the workspace store.
func RenderAgentsMDBlock(root string) (block []byte, omitted, constraints int, missingSnapshot bool, err error) {
	raw, err := os.ReadFile(filepath.Join(root, "eka.yaml"))
	if err != nil {
		return nil, 0, 0, false, fmt.Errorf("cannot read eka.yaml: %w", err)
	}
	meta, err := metadata.Parse(raw)
	if err != nil {
		return nil, 0, 0, false, fmt.Errorf("invalid eka.yaml: %w", err)
	}
	index, missingSnapshot, err := ConstraintsFromSnapshot(filepath.Join(root, "exchange", "snapshots"))
	if err != nil {
		return nil, 0, 0, false, fmt.Errorf("cannot read the committed snapshot: %w", err)
	}
	block, omitted = BuildAgentsMDBlock(meta.Project, meta.Namespace, index)
	return block, omitted, IndexEntryCount(index), missingSnapshot, nil
}

// IndexEntryCount is the number of index entries (constraint lines) in
// an index: the sum of every group's ids.
func IndexEntryCount(index []contexts.ConstraintGroup) int {
	n := 0
	for _, e := range index {
		n += len(e.IDs)
	}
	return n
}

// HasManagedRegion reports whether content carries the marker-delimited
// managed region. Callers use it to tell a generated file (safe to
// reconcile or remove) from hand-written user content (never touched).
func HasManagedRegion(content []byte) bool {
	return bytes.Contains(content, []byte(agentsMDMarkerStart)) &&
		bytes.Contains(content, []byte(agentsMDMarkerEnd))
}

// MergeAgentsMD reconciles existing file bytes with the managed block:
// missing content yields the block plus one trailing newline; a marked
// region is replaced verbatim (surrounding bytes untouched); unmarked
// content gains the block appended after a blank line. A nil return
// means "already current" (reuse): `eka agents-md --check` maps nil to
// exit 0, non-nil to drift.
func MergeAgentsMD(existing []byte, block []byte) []byte {
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
//
// The block carries the SAME constraint index `eka agents-md` derives
// (bootstrap.ConstraintsFromSnapshot, reading the committed snapshot at
// <target>/exchange/snapshots), so init and agents-md never fight: a
// re-run on an indexed repository reconciles to that repository's own
// indexed block (Reuse when already current), not identity-only.
//
// A MISSING snapshot yields a nil index → the identity-only block (as
// before). A snapshot that EXISTS but fails to load also degrades to the
// identity-only block: init must not fail on a corrupt snapshot — that
// is a bootstrap concern, and the authoritative surface for a corrupt
// snapshot is `eka agents-md --check` (exit 2).
// ponytail: init degrades silently here; the corrupt-snapshot ceiling is
// covered by `eka agents-md --check` (exit 2), so no error plumbing is
// added to the plan path.
func planAgentsMD(plan []Action, d *Discovery, a Answers) []Action {
	if !a.AgentsMD {
		return plan
	}
	project, namespace := identityForAgentsMD(d, a)
	index, _, _ := ConstraintsFromSnapshot(filepath.Join(d.AbsTarget, "exchange", "snapshots"))
	block, _ := BuildAgentsMDBlock(project, namespace, index)
	path := filepath.Join(d.AbsTarget, AgentsMDName)
	existing, err := os.ReadFile(path)
	if err != nil {
		return append(plan, Action{Kind: ActionAgentsMDMerge, Path: AgentsMDName, Content: MergeAgentsMD(nil, block)})
	}
	if merged := MergeAgentsMD(existing, block); merged != nil {
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
