package standardembed

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/maleolabs/eka-core/exchange"
)

// readmeVersionClaims extracts the version axes stated on the README's
// module-path line: "CLI `vX.Y.Z` · EKA Standard `X.Y` · `eka-core vX.Y.Z`".
//
// The README restates values that already have a single source in code. That
// restatement is useful to a reader and dangerous to leave unchecked, which is
// exactly how it drifted before: the line advertised CLI v1.10.0 / Standard
// 1.1 / eka-core v1.7.0 while the module graph required eka-core v1.10.5 and
// the embedded standard reported 1.3.
type readmeClaims struct {
	cli, standard, ekaCore string
}

var readmeClaimPattern = regexp.MustCompile(
	"CLI `v([^`]+)` · EKA Standard `([^`]+)` · `eka-core v([^`]+)`")

// parseReadmeClaims reads the module-path line from README.md.
func parseReadmeClaims(t *testing.T) readmeClaims {
	t.Helper()
	raw, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatalf("README.md is unreadable: %v", err)
	}
	m := readmeClaimPattern.FindStringSubmatch(string(raw))
	if m == nil {
		t.Fatal("README.md no longer states its version axes as \"CLI `vX.Y.Z` · EKA Standard `X.Y` · `eka-core vX.Y.Z`\"; restore the shape so the claim can be verified")
	}
	return readmeClaims{cli: m[1], standard: m[2], ekaCore: m[3]}
}

// TestReadmeStatesEmbeddedStandardVersion fails when the README's advertised
// standard version disagrees with the embedded declaration — the single source
// of truth for that axis.
func TestReadmeStatesEmbeddedStandardVersion(t *testing.T) {
	claims := parseReadmeClaims(t)
	want, err := Version()
	if err != nil {
		t.Fatalf("embedded version line unreadable: %v", err)
	}
	if claims.standard != want {
		t.Errorf("README advertises EKA Standard %s, embedded declaration says %s", claims.standard, want)
	}
}

// TestReadmeStatesRequiredEkaCoreVersion fails when the README's eka-core
// version disagrees with the version go.mod actually requires.
func TestReadmeStatesRequiredEkaCoreVersion(t *testing.T) {
	claims := parseReadmeClaims(t)
	want := requiredModuleVersion(t, "github.com/maleolabs/eka-core")
	if claims.ekaCore != want {
		t.Errorf("README advertises eka-core v%s, go.mod requires v%s", claims.ekaCore, want)
	}
}

// TestReadmeStandardVersionMatchesConformanceRules fails when the standard
// version the README advertises disagrees with the version the conformance
// rules implement. It restates TestEmbeddedVersionMatchesConformanceRules from
// the reader's point of view, so a failure names the document rather than only
// the embedded file.
func TestReadmeStandardVersionMatchesConformanceRules(t *testing.T) {
	claims := parseReadmeClaims(t)
	if claims.standard != exchange.SpecificationVersion {
		t.Errorf("README advertises EKA Standard %s, conformance rules implement %s",
			claims.standard, exchange.SpecificationVersion)
	}
}

// TestReadmeStatesProjectVersion fails when the README's CLI claim
// disagrees with the version anvil.yaml releases. anvil.yaml is the single
// source of truth for the tag the release workflow builds and checks the tag
// against; the README was the only place restating it with nothing verifying
// the restatement, which is how it stayed at v1.10.5 while the released line
// reached v1.20.1 and kept climbing.
func TestReadmeStatesProjectVersion(t *testing.T) {
	claims := parseReadmeClaims(t)
	raw, err := os.ReadFile("anvil.yaml")
	if err != nil {
		t.Fatalf("anvil.yaml is unreadable: %v", err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.SplitN(strings.TrimSpace(line), ":", 2)
		if len(fields) == 2 && fields[0] == "version" {
			released := strings.TrimSpace(fields[1])
			if claims.cli != released {
				t.Errorf("README advertises CLI v%s, anvil.yaml releases %s", claims.cli, released)
			}
			return
		}
	}
	t.Fatal("anvil.yaml states no project version")
}

// requiredModuleVersion returns the version go.mod requires for module path.
func requiredModuleVersion(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile("go.mod")
	if err != nil {
		t.Fatalf("go.mod is unreadable: %v", err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		// A require line inside the block reads "<path> v<version>".
		if len(fields) == 2 && fields[0] == path {
			return strings.TrimPrefix(fields[1], "v")
		}
	}
	t.Fatalf("go.mod does not require %s", path)
	return ""
}
