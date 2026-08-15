package standardembed

import (
	"bytes"
	"regexp"
	"strings"
	"testing"

	"github.com/maleolabs/eka-core/exchange"
)

// standardembed_test.go covers the embedded EKA standard declaration:
// the vendored release asset carries the expected compact consumer
// summary, its Version X.Y line parses, and — the build-time
// version-consistency test — the embedded version equals the standard
// version the CLI conformance rules implement (exchange.SpecificationVersion).

// TestDeclarationShape pins the structure of the embedded file: the
// compact consumer summary starting with the EKA STANDARD header and the
// Version X.Y line, ending with the END OF EKA STANDARD marker.
func TestDeclarationShape(t *testing.T) {
	d := Declaration()
	if len(d) == 0 {
		t.Fatal("embedded declaration must not be empty")
	}
	head := d[:len("EKA STANDARD")]
	if string(head) != "EKA STANDARD" {
		t.Errorf("declaration must start with the EKA STANDARD header, got %q", head)
	}
	// The Version X.Y line follows on the second line.
	lines := strings.Split(string(d), "\n")
	if len(lines) < 2 || !strings.HasPrefix(lines[1], "Version ") {
		t.Errorf("second line must be the Version X.Y line, got %q", lines[1])
	}
	if !bytes.HasSuffix(d, []byte("END OF EKA STANDARD 1.0 (SUMMARY)\n")) {
		t.Errorf("declaration must end with the END OF EKA STANDARD marker")
	}
}

// twoComponentVersion matches the version value of a standard: exactly
// two dot-separated numeric components (major.minor) — a standard,
// unlike a tool, has no patch line, so "1.0.0" is not a valid standard
// version.
var twoComponentVersion = regexp.MustCompile(`^\d+\.\d+$`)

// TestVersionParses: Version returns the value of the Version X.Y line
// (e.g. "1.0") — the value the CLI reports as its standard corpus. The
// value must be strictly two-component (major.minor).
func TestVersionParses(t *testing.T) {
	v, err := Version()
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if v == "" {
		t.Fatal("Version must not be empty")
	}
	if !twoComponentVersion.MatchString(v) {
		t.Errorf("standard versions are strictly two-component (major.minor), got %q", v)
	}
	// Deterministic: two parses agree.
	v2, err := Version()
	if err != nil {
		t.Fatal(err)
	}
	if v != v2 {
		t.Errorf("Version is not deterministic: %q vs %q", v, v2)
	}
	if got := MustVersion(); got != v {
		t.Errorf("MustVersion = %q, want %q", got, v)
	}
}

// withDeclaration swaps the embedded declaration for the duration of the
// test, so malformed shapes can be exercised deterministically. The
// original bytes are restored on cleanup.
func withDeclaration(t *testing.T, content string) {
	t.Helper()
	old := declaration
	declaration = []byte(content)
	t.Cleanup(func() { declaration = old })
}

// TestVersionAnchoredToShape pins the anchored parse: the Version X.Y
// line must be the second line, directly after the EKA STANDARD header.
// Any other shape is rejected deterministically — a Version line
// appearing elsewhere in the file is not picked up.
func TestVersionAnchoredToShape(t *testing.T) {
	// Canonical shape: header on line 1, version on line 2.
	withDeclaration(t, "EKA STANDARD\nVersion 1.0\n\nbody\n")
	v, err := Version()
	if err != nil {
		t.Fatalf("canonical shape must parse: %v", err)
	}
	if v != "1.0" {
		t.Errorf("Version = %q, want 1.0", v)
	}

	// A "Version " line later in the body must NOT satisfy the parse.
	withDeclaration(t, "EKA STANDARD\n\nSome body\nVersion 9.9\n")
	if _, err := Version(); err == nil {
		t.Error("a Version line outside line 2 must be rejected")
	}

	// Broken shapes, each rejected deterministically: empty file,
	// wrong header, version line before the header, missing version
	// line, empty version value.
	withDeclaration(t, "")
	if _, err := Version(); err == nil {
		t.Error("an empty declaration must be rejected")
	}
	withDeclaration(t, "NOT THE HEADER\nVersion 1.0\n")
	if _, err := Version(); err == nil {
		t.Error("a missing header must be rejected")
	}
	withDeclaration(t, "Version 1.0\nEKA STANDARD\n")
	if _, err := Version(); err == nil {
		t.Error("a version line before the header must be rejected")
	}
	withDeclaration(t, "EKA STANDARD\nOther line\n")
	if _, err := Version(); err == nil {
		t.Error("a missing version line on line 2 must be rejected")
	}
	withDeclaration(t, "EKA STANDARD\nVersion\n")
	if _, err := Version(); err == nil {
		t.Error("an empty version value must be rejected")
	}
}

// TestMustVersionFailsOnMalformedShape: MustVersion must panic when the
// embedded declaration shape is broken — the binary must never silently
// report a version it cannot derive.
func TestMustVersionFailsOnMalformedShape(t *testing.T) {
	withDeclaration(t, "EKA STANDARD\nNo version here\n")
	defer func() {
		if recover() == nil {
			t.Error("MustVersion must panic on a malformed declaration")
		}
	}()
	_ = MustVersion()
}

// TestDeclarationDeterministic: the embedded bytes are stable across
// calls — the same bytes the bootstrap engine writes into every
// generated repository.
func TestDeclarationDeterministic(t *testing.T) {
	a := Declaration()
	b := Declaration()
	if !bytes.Equal(a, b) {
		t.Error("Declaration must return identical bytes on every call")
	}
}

// TestEmbeddedVersionMatchesConformanceRules is the build-time
// version-consistency test (sto:init-standard-declaration): the
// embedded Version X.Y line must equal the standard version the CLI
// conformance rules implement. The vendored EKA asset and the enforced
// rules cannot drift: when the eka-standard release bumps the corpus
// version, the vendored file must be updated together with the
// conformance rules (and vice versa).
func TestEmbeddedVersionMatchesConformanceRules(t *testing.T) {
	embedded, err := Version()
	if err != nil {
		t.Fatalf("embedded version line unreadable: %v", err)
	}
	if embedded != exchange.SpecificationVersion {
		t.Fatalf(
			"embedded EKA declaration version %q != supported standard version %q (exchange.SpecificationVersion): "+
				"the vendored standard file and the CLI conformance rules drifted",
			embedded, exchange.SpecificationVersion)
	}
}
