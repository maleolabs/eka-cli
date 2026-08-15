package cmd

import (
	"encoding/json"
	"fmt"

	"github.com/maleolabs/eka-core/exchange"
	"github.com/maleolabs/eka-core/machine"
	"github.com/maleolabs/eka-core/metadata"
	"github.com/spf13/cobra"
)

// version is the CLI build version. It defaults to "dev" and is
// overridable at build time:
//
//	go build -ldflags "-X github.com/maleolabs/eka-cli/cmd.version=v1.2.3" ./cmd/eka
//
// The version identifies the CLI implementation, never the standard.
var version = "dev"

// standardVersion is the EKA standard version this CLI implements,
// derived from the single canonical source: the version constants the
// eka-core library exports (exchange.SpecificationVersion — the EKA
// standard 1.0 corpus, JSON-native authoring and the camelCase
// convention). The value is a compile-time constant here only because
// the core constant is one; it is never hardcoded or re-derived in the
// CLI. Standards use a two-component scheme (major.minor) — a standard,
// unlike a tool, has no patch line.
const standardVersion = exchange.SpecificationVersion

// versionInfo is the `eka version --json` report: one field per version
// axis, each derived from a single source (the owning package's exported
// constant — never re-declared or hardcoded in the CLI). Field order is
// the fixed JSON serialization order (deterministic output).
//
// Axes (sto:version-clarity):
//
//	standardCorpus   EKA standard version the CLI implements        exchange.SpecificationVersion
//	rsfSerialization RSF (Reference Serialization Format) version   exchange.SerializationVersion
//	exchangeSpec     Exchange Contract format version               exchange.ExchangeFormatVersion
//	machineSchema    machine interface (CKO JSON) schema identifier machine.Schema
//	ekaYamlSchema    eka.yaml identity-file schema version           metadata.SchemaVersion
//	cliVersion       CLI build version (ldflags cmd.version)         cmd/version.go
type versionInfo struct {
	StandardCorpus   string `json:"standardCorpus"`
	RSFSerialization string `json:"rsfSerialization"`
	ExchangeSpec     string `json:"exchangeSpec"`
	MachineSchema    string `json:"machineSchema"`
	EkaYAMLSchema    string `json:"ekaYamlSchema"`
	CLIVersion       string `json:"cliVersion"`
}

// newVersionCommand builds the `eka version` command: prints the CLI
// build version and every EKA version axis — the standard corpus, the
// RSF serialization, the exchange spec, the machine schema, the eka.yaml
// schema and the CLI — each from its single canonical source. Plain
// output is deterministic and readable; --json emits all six axes as a
// single machine-readable document.
func newVersionCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Long: `Print the EKA version axes this CLI reports, each from its single
canonical source:

  standard corpus    the EKA standard version (exchange.SpecificationVersion)
  RSF serialization  the Reference Serialization Format version
                     (exchange.SerializationVersion)
  exchange spec      the Exchange Contract format version
                     (exchange.ExchangeFormatVersion)
  machine schema     the machine interface (CKO JSON) schema identifier
                     (machine.Schema)
  eka.yaml schema    the eka.yaml identity-file schema version
                     (metadata.SchemaVersion)
  CLI version        the CLI build version (ldflags cmd.version, default
                     "dev")

The CLI version is set at build time:
  go build -ldflags "-X .../cmd.version=v1.2.3" ./cmd/eka

The standard version is fixed by the ratified specifications (EKA v1.0)
and is never hardcoded in the CLI — it derives from eka-core's exported
version constants, the single source of truth for every axis.

--json emits all six axes as one machine-readable document (deterministic
key order, two-space indentation, trailing newline).`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, args []string) error {
			s := styleFor(c)
			info := versionInfo{
				StandardCorpus:   standardVersion,
				RSFSerialization: exchange.SerializationVersion,
				ExchangeSpec:     exchange.ExchangeFormatVersion,
				MachineSchema:    machine.Schema,
				EkaYAMLSchema:    fmt.Sprintf("%d", metadata.SchemaVersion),
				CLIVersion:       version,
			}
			if asJSON, err := c.Flags().GetBool("json"); err != nil {
				return fmt.Errorf("version failed: %w", err) // Exit 2: internal.
			} else if asJSON {
				out, err := json.MarshalIndent(info, "", "  ")
				if err != nil {
					return fmt.Errorf("version failed: %w", err) // Exit 2: internal.
				}
				out = append(out, '\n')
				if _, err := s.W.Write(out); err != nil {
					return fmt.Errorf("version failed: %w", err) // Exit 2: internal.
				}
				return nil
			}
			// Plain output: deterministic one-line-per-axis report. The
			// first two lines keep the historical contract ("eka <ver>",
			// "EKA standard <std>"); the remaining axes extend it.
			fmt.Fprintf(s.W, "eka %s\n", version)
			fmt.Fprintf(s.W, "EKA standard %s\n", standardVersion)
			fmt.Fprintf(s.W, "RSF serialization %s\n", exchange.SerializationVersion)
			fmt.Fprintf(s.W, "Exchange spec %s\n", exchange.ExchangeFormatVersion)
			fmt.Fprintf(s.W, "Machine schema %s\n", machine.Schema)
			fmt.Fprintf(s.W, "eka.yaml schema %d\n", metadata.SchemaVersion)
			return nil
		},
	}
	cmd.Flags().Bool("json", false, "emit all version axes as a single JSON document (deterministic key order)")
	return cmd
}
