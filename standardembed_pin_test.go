package standardembed

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"gopkg.in/yaml.v3"
)

// vendoredPin is the parsed form of vendored.yaml: the eka-standard release
// the vendored bytes came from and the expected sha256 of each artifact.
type vendoredPin struct {
	StandardRelease string            `yaml:"standardRelease"`
	Standard        map[string]string `yaml:"standard"`
}

// loadVendoredPin reads and parses vendored.yaml from the module root.
func loadVendoredPin(t *testing.T) vendoredPin {
	t.Helper()
	raw, err := os.ReadFile("vendored.yaml")
	if err != nil {
		t.Fatalf("vendored.yaml is missing: %v", err)
	}
	var pin vendoredPin
	if err := yaml.Unmarshal(raw, &pin); err != nil {
		t.Fatalf("vendored.yaml is malformed: %v", err)
	}
	if pin.StandardRelease == "" {
		t.Fatal("vendored.yaml must declare standardRelease")
	}
	if len(pin.Standard) == 0 {
		t.Fatal("vendored.yaml must pin at least one standard artifact")
	}
	return pin
}

// TestVendoredStandardMatchesPin fails when a vendored eka-standard artifact
// no longer matches the digest recorded in vendored.yaml. The pinned digests
// correspond to the eka-standard release named in the failure message, so a
// reader knows which release to compare the bytes against.
//
// This is the enforcement the shape and version-consistency tests cannot
// provide: those accept any content that parses correctly, so a hand-edited
// declaration would pass them while diverging from the published standard.
func TestVendoredStandardMatchesPin(t *testing.T) {
	pin := loadVendoredPin(t)
	paths := make([]string, 0, len(pin.Standard))
	for p := range pin.Standard {
		paths = append(paths, p)
	}
	sort.Strings(paths) // deterministic failure order
	for _, rel := range paths {
		want := pin.Standard[rel]
		got, err := digestFile(filepath.FromSlash(rel))
		if err != nil {
			t.Errorf("vendored artifact %s is unreadable: %v", rel, err)
			continue
		}
		if got != want {
			t.Errorf(
				"vendored artifact %s does not match eka-standard release %s: sha256 = %s, expected %s.\n"+
					"The file was edited without refreshing it from eka-standard, or the pin is stale.\n"+
					"Refresh from the standard and re-pin: go run ./scripts/pin-vendored -write",
				rel, pin.StandardRelease, got, want)
		}
	}
}

// TestVendoredPinCoversEmbeddedArtifacts fails when an embedded standard
// artifact has no digest recorded, so a newly vendored file cannot silently
// bypass the integrity pin.
func TestVendoredPinCoversEmbeddedArtifacts(t *testing.T) {
	pin := loadVendoredPin(t)
	embedded := []string{"EKA", "templates/hooks/pre-commit", "templates/hooks/pre-push"}
	for _, rel := range embedded {
		if _, ok := pin.Standard[rel]; !ok {
			t.Errorf("vendored.yaml does not pin %s, which is embedded into the binary", rel)
		}
	}
}

// digestFile returns the lowercase hex sha256 of one file's bytes.
func digestFile(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// fmtDigest is the canonical rendering used by the refresh script, kept here
// so the test and the script agree on the format.
func fmtDigest(path string) (string, error) {
	d, err := digestFile(path)
	if err != nil {
		return "", fmt.Errorf("cannot digest %s: %w", path, err)
	}
	return d, nil
}
