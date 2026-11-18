// Command pin-vendored refreshes the digests in vendored.yaml from the
// eka-standard artifacts currently present in this module tree.
//
// Refreshing a vendored file and re-pinning it must be one operation: a
// separate manual edit of the digests invites the exact drift the pin exists
// to catch. Copy the file from eka-standard, then run with -write.
//
//	go run ./scripts/pin-vendored          # report drift, change nothing
//	go run ./scripts/pin-vendored -write   # record the current digests
//
// Exit codes: 0 all digests match (or were written), 1 drift detected in
// report mode, 2 usage or I/O error.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"strings"
)

// artifacts are the files vendored from eka-standard, relative to the module
// root. The list is explicit rather than discovered: a new vendored file must
// be a deliberate decision, and the pin test asserts this list is covered.
var artifacts = []string{
	"EKA",
	"templates/hooks/pre-commit",
	"templates/hooks/pre-push",
}

const pinPath = "vendored.yaml"

func main() {
	write := flag.Bool("write", false, "record the current digests instead of reporting drift")
	flag.Parse()

	raw, err := os.ReadFile(pinPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pin-vendored: cannot read %s: %v\n", pinPath, err)
		os.Exit(2)
	}
	current := parseDigests(string(raw))

	drift := false
	for _, rel := range artifacts {
		got, err := digest(rel)
		if err != nil {
			fmt.Fprintf(os.Stderr, "pin-vendored: %v\n", err)
			os.Exit(2)
		}
		if want, ok := current[rel]; !ok {
			fmt.Printf("new      %s %s (not pinned)\n", got, rel)
			drift = true
		} else if want != got {
			fmt.Printf("drift    %s -> %s %s\n", want, got, rel)
			drift = true
		} else {
			fmt.Printf("ok       %s %s\n", got, rel)
		}
	}

	if !*write {
		if drift {
			fmt.Fprintln(os.Stderr, "\npin-vendored: vendored artifacts drifted from the pin; run with -write after refreshing them from eka-standard")
			os.Exit(1)
		}
		return
	}
	if !drift {
		fmt.Println("\npin-vendored: nothing to write")
		return
	}
	if err := rewrite(raw); err != nil {
		fmt.Fprintf(os.Stderr, "pin-vendored: %v\n", err)
		os.Exit(2)
	}
	fmt.Printf("\npin-vendored: rewrote %s\n", pinPath)
}

// digest returns the lowercase hex sha256 of one artifact's bytes.
func digest(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("cannot read %s: %w", path, err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// parseDigests extracts the pinned digests from the yaml text. A full parser
// would be the honest tool, but this file owns the format and the shape is
// two-space indented "path: digest" lines under the `standard:` key, so a
// line scan keeps the refresh path dependency-free.
func parseDigests(text string) map[string]string {
	out := map[string]string{}
	inStandard := false
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "standard:" {
			inStandard = true
			continue
		}
		if !inStandard || trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if !strings.HasPrefix(line, "  ") {
			inStandard = false // left the block
			continue
		}
		key, value, ok := strings.Cut(trimmed, ":")
		if !ok {
			continue
		}
		out[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	return out
}

// rewrite replaces the digest values in place, preserving every comment and
// the file's structure.
func rewrite(raw []byte) error {
	lines := strings.Split(string(raw), "\n")
	inStandard := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "standard:" {
			inStandard = true
			continue
		}
		if !inStandard || trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if !strings.HasPrefix(line, "  ") {
			inStandard = false
			continue
		}
		key, _, ok := strings.Cut(trimmed, ":")
		if !ok {
			continue
		}
		rel := strings.TrimSpace(key)
		d, err := digest(rel)
		if err != nil {
			return err
		}
		lines[i] = "  " + rel + ": " + d
	}
	return os.WriteFile(pinPath, []byte(strings.Join(lines, "\n")), 0o644)
}
