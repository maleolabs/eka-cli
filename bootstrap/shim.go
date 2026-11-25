package bootstrap

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/maleolabs/eka-core/contexts"
)

// This file implements the per-host projection shims of M2: the rule files
// a host reads on demand for one subtree (Claude Code rules, Cursor rules).
// A shim is emitted for a DECLARED target only and only when that host is
// configured in the repository — a host whose config directory is absent is
// skipped, never approximated. opencode consumes nested AGENTS.md natively,
// so it needs no shim (an acknowledged no-op).
//
// Shim layout:
//   - claude → .claude/rules/<slug>.md   frontmatter `paths:`  → <dir>/**
//   - cursor → .cursor/rules/<slug>.mdc  frontmatter `globs:`  → <dir>/**
//   - <slug> = the directory with "/" replaced by "-", lowercased, every
//     other byte outside [a-z0-9-] dropped, edge "-" trimmed
//     ("src/api" → "src-api"). Stable and filesystem-safe.
//
// The body names the subtree's bound constraints and the exact `eka get`
// read hint, bounded by AgentsMDBlockBudget and byte-deterministic.

// Host targets accepted by `--host`.
const (
	HostClaude   = "claude"
	HostCursor   = "cursor"
	HostOpencode = "opencode"
)

// HostShim is one expected per-host rule file.
type HostShim struct {
	Host    string // claude | cursor
	Path    string // repository-relative slash path
	Target  string // the bound subtree pattern, "<dir>/**"
	Content []byte // generated file bytes (no user region: wholly managed)
}

// IsKnownHost reports whether host is a supported --host target.
func IsKnownHost(host string) bool {
	switch host {
	case HostClaude, HostCursor, HostOpencode:
		return true
	}
	return false
}

// HostConfigDir returns the repository-relative directory whose presence
// means the host is configured for this repository (".claude", ".cursor").
// It returns "" for a host that needs no config directory (opencode reads
// nested AGENTS.md natively).
func HostConfigDir(host string) string {
	switch host {
	case HostClaude:
		return ".claude"
	case HostCursor:
		return ".cursor"
	}
	return ""
}

// BuildHostShims renders the per-host shims for one declared host over the
// nested entries. opencode yields no shims (a documented no-op); an unknown
// host is an error.
func BuildHostShims(host, namespace string, entries []NestedEntry) ([]HostShim, error) {
	switch host {
	case HostOpencode:
		return nil, nil
	case HostClaude, HostCursor:
	default:
		return nil, fmt.Errorf("unknown host %q", host)
	}
	shims := make([]HostShim, 0, len(entries))
	for _, e := range entries {
		shims = append(shims, HostShim{
			Host:    host,
			Path:    hostShimPath(host, e.Dir),
			Target:  e.Dir + "/**",
			Content: hostShimContent(host, namespace, e.Dir, e.Index),
		})
	}
	return shims, nil
}

// hostShimPath is the repository-relative path of one host's rule file.
func hostShimPath(host, dir string) string {
	slug := shimSlug(dir)
	switch host {
	case HostClaude:
		return ".claude/rules/" + slug + ".md"
	case HostCursor:
		return ".cursor/rules/" + slug + ".mdc"
	}
	return ""
}

// shimSlug derives the deterministic, filesystem-safe shim name of a
// directory: "/" → "-", lowercased, every other byte outside [a-z0-9-]
// dropped, edge "-" trimmed. "src/api/v2" → "src-api-v2".
func shimSlug(dir string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(dir) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		case r == '/':
			b.WriteByte('-')
		}
	}
	slug := strings.Trim(b.String(), "-")
	if slug == "" {
		// ponytail: unreachable for a real directory; a safe fallback keeps
		// the path valid if one ever maps to nothing.
		return "root"
	}
	return slug
}

// hostShimContent renders one shim: YAML frontmatter naming the subtree
// pattern and a bounded body naming the bound constraints plus the read
// hint. Reuses the root index renderer and budget so the two never drift.
func hostShimContent(host, namespace, dir string, index []contexts.ConstraintGroup) []byte {
	var b bytes.Buffer
	b.WriteString("---\n")
	if host == HostCursor {
		b.WriteString("globs:\n  - " + dir + "/**\n")
	} else {
		b.WriteString("paths:\n  - " + dir + "/**\n")
	}
	b.WriteString("---\n")
	b.WriteString("Constraints binding " + dir + "/ (Discovery + Architecture, stratum 1-2; read one with `eka get " + namespace + "/<type>:<id>`):\n")
	lines := agentsMDIndexLines(index)
	kept, omitted, _ := agentsMDFitIndex(lines, b.Len())
	for _, line := range kept {
		b.WriteString(line.text + "\n")
	}
	if omitted > 0 {
		fmt.Fprintf(&b, "%s%d more (eka status)\n", AgentsMDTruncatedPrefix, omitted)
	}
	return b.Bytes()
}
