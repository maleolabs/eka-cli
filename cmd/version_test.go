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
