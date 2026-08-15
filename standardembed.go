// Package standardembed embeds the EKA standard declaration file (the
// root `EKA` compact consumer summary) into the binary and exposes it to
// the rest of the codebase.
//
// The file is vendored from the eka-standard release asset
// (github.com/maleolabs/eka-standard, releases/download/v1.0/EKA) and is
// the single canonical copy the CLI distributes: the bootstrap engine
// writes its bytes into every generated repository, and the CLI's
// reported standard version is derived from its `Version X.Y` line —
// never from a hardcoded constant.
//
// Embedding is the ADR-023 distribution pattern (a root-level package
// with //go:embed over the module root): the binary stays standalone and
// offline — `eka init` never fetches anything — and dry-run stays
// deterministic because the content is a compile-time resource.
//
// The build-time version-consistency test (standardembed_test.go) locks
// the embedded `Version X.Y` line to the standard version the CLI
// conformance rules implement (exchange.SpecificationVersion).
package standardembed

import (
	"bytes"
	_ "embed"
	"fmt"
)

//go:embed EKA
var declaration []byte

// Declaration returns the embedded EKA standard declaration file bytes.
// The returned slice is the exact content written into generated
// repositories (deterministic: identical bytes on every run and every
// build of the same source tree).
func Declaration() []byte {
	return declaration
}

// headerLine is the first line of the standard declaration file: the
// header marking the compact consumer summary. The version line must
// follow it immediately (line 2 of the file) — the shape is part of the
// vendored asset contract.
const headerLine = "EKA STANDARD"

// versionLinePrefix marks the version line of the standard declaration
// file: "Version X.Y" (the two-component scheme of a standard — no
// patch line). The line is the single source the CLI's standard-version
// reporting derives from. It must appear as the second line, directly
// after the EKA STANDARD header.
const versionLinePrefix = "Version "

// Version parses the `Version X.Y` line of the embedded declaration and
// returns the version value (e.g. "1.0"). The parse is anchored to the
// file shape: line 1 must be the EKA STANDARD header and line 2 the
// Version X.Y line — any other shape is rejected deterministically. The
// embedded file is a compile-time resource, so a failure here means the
// vendored asset is broken and the binary must not claim a version.
func Version() (string, error) {
	lines := bytes.Split(declaration, []byte("\n"))
	if len(lines) < 2 {
		return "", fmt.Errorf("standard declaration is missing the %q header line", headerLine)
	}
	if got := string(bytes.TrimSpace(lines[0])); got != headerLine {
		return "", fmt.Errorf("standard declaration must start with the %q header line, got %q", headerLine, got)
	}
	line := bytes.TrimSpace(lines[1])
	if !bytes.HasPrefix(line, []byte(versionLinePrefix)) {
		return "", fmt.Errorf("standard declaration must carry the %q line as its second line, got %q", versionLinePrefix, line)
	}
	v := string(bytes.TrimSpace(line[len(versionLinePrefix):]))
	if v == "" {
		return "", fmt.Errorf("standard declaration version line %q is empty", line)
	}
	return v, nil
}

// MustVersion returns Version(), panicking when the embedded declaration
// does not carry a valid `Version X.Y` line. It is used for values that
// are structurally fixed at build time: a missing version line is a
// broken vendored asset and must fail loudly rather than silently report
// a wrong standard version.
func MustVersion() string {
	v, err := Version()
	if err != nil {
		panic("standardembed: " + err.Error())
	}
	return v
}
