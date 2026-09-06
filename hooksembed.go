// Package hooksembed embeds the Layer-2 git hook templates into the
// binary so `eka capture --install-hooks` works without external paths.
//
// The files are vendored from eka-standard/templates/hooks
// (pre-commit, pre-push) into eka-cli/templates/hooks and embedded at
// compile time. This makes the release binary standalone and offline:
// no /home/... absolute paths, no sibling checkout required. Anvil/dev
// mode still prefers a real file on disk (symlink) when it exists; the
// embedded bytes are the fallback for release installs.
//
// Embedding pattern mirrors standardembed.go (ADR-023): a root-level
// package with //go:embed over the module root. This file shares the
// standardembed package (same directory) to satisfy Go's one-package-per-directory rule.
package standardembed

import _ "embed"

//go:embed templates/hooks/pre-commit
var PreCommit []byte

//go:embed templates/hooks/pre-push
var PrePush []byte

// Hooks returns the embedded hook templates keyed by hook name.
// The map is a fresh allocation on each call so callers may not mutate
// the underlying embedded bytes.
func Hooks() map[string][]byte {
	return map[string][]byte{
		"pre-commit": PreCommit,
		"pre-push":   PrePush,
	}
}

// HookBytes is an alias for Hooks for discoverability.
func HookBytes() map[string][]byte { return Hooks() }
