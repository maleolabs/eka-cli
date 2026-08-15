package standardembed

import (
	"bytes"
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

// TestVersionParses: Version returns the value of the Version X.Y line
// (e.g. "1.0") — the value the CLI reports as its standard corpus.
func TestVersionParses(t *testing.T) {
	v, err := Version()
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if v == "" {
		t.Fatal("Version must not be empty")
	}
	if !strings.Contains(v, ".") {
		t.Errorf("standard versions are two-component (major.minor), got %q", v)
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
