package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/maleolabs/eka-core/exchange"
	"github.com/maleolabs/eka-core/machine"
	"github.com/maleolabs/eka-core/metadata"
)

// version_test.go covers the `eka version` contract (sto:version-clarity):
// every version axis is reported from its single canonical source — the
// exported constants of eka-core — and the CLI never hardcodes or
// re-derives a version value. The JSON report is the machine contract;
// the plain output is the deterministic human-readable view.

// TestVersionStandardDerivesFromCore: the CLI's standardVersion must
// equal the eka-core single source (exchange.SpecificationVersion). This
// is a compile-time constant derivation (const standardVersion =
// exchange.SpecificationVersion); the assertion locks the link.
func TestVersionStandardDerivesFromCore(t *testing.T) {
	if standardVersion != exchange.SpecificationVersion {
		t.Errorf("standardVersion = %q, want exchange.SpecificationVersion = %q (single source)", standardVersion, exchange.SpecificationVersion)
	}
}

// TestVersionJSON: `eka version --json` reports all six version axes —
// standard corpus, RSF serialization, exchange spec, machine schema,
// eka.yaml schema, CLI — from the eka-core exported constants, in a
// deterministic key order.
func TestVersionJSON(t *testing.T) {
	var out, errb bytes.Buffer
	code := Execute([]string{"version", "--json"}, strings.NewReader(""), &out, &errb)
	if code != 0 {
		t.Fatalf("version --json: exit = %d, want 0 (stderr: %s)", code, errb.String())
	}
	if errb.Len() != 0 {
		t.Errorf("version --json: stderr must be empty, got %q", errb.String())
	}
	got := out.String()
	if !strings.HasSuffix(got, "\n") {
		t.Errorf("version --json: output must end with a newline, got %q", got)
	}
	var info versionInfo
	if err := json.Unmarshal(out.Bytes(), &info); err != nil {
		t.Fatalf("version --json: invalid JSON: %v\n%s", err, got)
	}
	want := versionInfo{
		StandardCorpus:   exchange.SpecificationVersion,
		RSFSerialization: exchange.SerializationVersion,
		ExchangeSpec:     exchange.ExchangeFormatVersion,
		MachineSchema:    machine.Schema,
		EkaYAMLSchema:    fmt.Sprintf("%d", metadata.SchemaVersion),
		CLIVersion:       version,
	}
	if info != want {
		t.Errorf("version --json axes mismatch:\n got %+v\nwant %+v", info, want)
	}
	// Every axis key must be present in the document (no omitted/renamed
	// axis), and the JSON must be deterministic: two runs produce
	// identical bytes.
	for _, key := range []string{
		"standardCorpus", "rsfSerialization", "exchangeSpec",
		"machineSchema", "ekaYamlSchema", "cliVersion",
	} {
		if !strings.Contains(got, `"`+key+`"`) {
			t.Errorf("version --json missing axis key %q:\n%s", key, got)
		}
	}
	var out2 bytes.Buffer
	Execute([]string{"version", "--json"}, strings.NewReader(""), &out2, &errb)
	if !bytes.Equal(out.Bytes(), out2.Bytes()) {
		t.Errorf("version --json is not deterministic:\n%q\nvs\n%q", out.String(), out2.String())
	}
}

// TestVersionPlainAxes: the plain `eka version` output keeps the
// historical two-line head ("eka <ver>", "EKA standard <std>") and
// reports every remaining axis on its own line, all deterministic and
// derived from the same single sources as --json.
func TestVersionPlainAxes(t *testing.T) {
	var out, errb bytes.Buffer
	code := Execute([]string{"version"}, strings.NewReader(""), &out, &errb)
	if code != 0 {
		t.Fatalf("version: exit = %d, want 0", code)
	}
	got := out.String()
	for _, want := range []string{
		"eka " + version + "\n",
		"EKA standard " + exchange.SpecificationVersion + "\n",
		"RSF serialization " + exchange.SerializationVersion + "\n",
		"Exchange spec " + exchange.ExchangeFormatVersion + "\n",
		"Machine schema " + machine.Schema + "\n",
		fmt.Sprintf("eka.yaml schema %d\n", metadata.SchemaVersion),
	} {
		if !strings.Contains(got, want) {
			t.Errorf("version plain output missing %q, got:\n%s", want, got)
		}
	}
	// Deterministic: two runs produce identical bytes.
	var out2 bytes.Buffer
	Execute([]string{"version"}, strings.NewReader(""), &out2, &errb)
	if out.String() != out2.String() {
		t.Errorf("version plain output is not deterministic")
	}
}

// TestVersionJSONFlagHelp: the --json flag is documented in the command
// help.
func TestVersionJSONFlagHelp(t *testing.T) {
	var out, errb bytes.Buffer
	code := Execute([]string{"version", "--help"}, strings.NewReader(""), &out, &errb)
	if code != 0 {
		t.Fatalf("version --help: exit = %d, want 0", code)
	}
	if !strings.Contains(out.String(), "--json") {
		t.Errorf("version --help must document --json, got:\n%s", out.String())
	}
}

// TestVersionFlag: the top-level `eka --version` flag (sto:cli-polish)
// prints the CLI version — the SAME single source as `eka version` (the
// ldflags-injected `version` variable) — as one deterministic line
// byte-identical to the first line `eka version` emits, and exits 0
// with empty stderr.
func TestVersionFlag(t *testing.T) {
	code, out, errText := runIn([]string{"--version"})
	if code != 0 {
		t.Fatalf("--version: exit = %d, want 0\nstdout: %s\nstderr: %s", code, out, errText)
	}
	if errText != "" {
		t.Errorf("--version: stderr must be empty, got %q", errText)
	}
	if out != "  eka "+version+"\n" {
		t.Errorf("--version output = %q, want %q (the first line of 'eka version')", out, "  eka "+version+"\n")
	}

	// Agreement with `eka version`: the first line of `eka version` is
	// byte-identical to `eka --version` — both derive from `version`
	// and render through the same presentation writer.
	code, versionOut, _ := runIn([]string{"version"})
	if code != 0 {
		t.Fatalf("version: exit = %d, want 0", code)
	}
	if !strings.HasPrefix(versionOut, out) {
		t.Errorf("eka version must start with the eka --version line %q, got:\n%s", out, versionOut)
	}

	// Deterministic: two runs produce identical bytes.
	_, out2, _ := runIn([]string{"--version"})
	if out != out2 {
		t.Errorf("--version is not deterministic")
	}
}

// TestVersionFlagDocumented: the --version flag is discoverable in the
// root help (persistent flag) and the landing points at it.
func TestVersionFlagDocumented(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"help"}} {
		var out, errb bytes.Buffer
		if code := Execute(args, strings.NewReader(""), &out, &errb); code != 0 {
			t.Errorf("args %v: exit = %d, want 0", args, code)
		}
		if !strings.Contains(out.String(), "--version") {
			t.Errorf("args %v: root help must document --version:\n%s", args, out.String())
		}
	}
	_, landing, _ := runIn(nil)
	if !strings.Contains(landing, "eka --version") {
		t.Errorf("landing must point at 'eka --version':\n%s", landing)
	}
}
