package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

// mcpSubcommands is the static disclosure list of the eka-mcp plugin's
// actual subcommands (bug:mcp-help-subcommands-hidden). It is the single
// source of truth for the native stub help so `eka mcp -h` discloses the
// surface even when the plugin is not installed. The list matches
// pack.PluginCommands in eka-mcp (mcp, manifest, install, configure,
// serve) minus the dispatch proxy "mcp" itself, and the actual
// cmd/eka-mcp subcommands. Rich descriptions remain deferred (B3) — this
// is about disclosure, not rich help.
var mcpSubcommands = []string{"configure", "install", "manifest", "serve"}

// mcpStubLong is the native help text for `eka mcp` when the plugin is
// not installed or when the proxied help is rendered. It embeds the
// static subcommand list so discovery does not require the plugin to be
// installed.
func mcpStubLong() string {
	return fmt.Sprintf(`EKA MCP server and plugin tooling

The mcp command is proxied to the installed "mcp" plugin executable
(eka-mcp). Every dispatch re-verifies the binary against its recorded
install checksum, runs it with a bounded environment whitelist, and
propagates its exit code. Arguments after the command name pass through
to the plugin unchanged — the plugin owns its flags.

Subcommands (disclosure):
  %s

Use "eka mcp <subcommand> --help" for subcommand help, or "eka plugin install mcp" to install the plugin.

Help-only forms (-h, --help) render this native help; everything else
is dispatched to the plugin when installed.`, strings.Join(mcpSubcommands, ", "))
}

// newMcpStubCommand builds the native stub for `eka mcp` that exists
// even when the plugin is not installed. It is grouped under Plugins so
// `eka --help` always discloses the MCP surface. When the plugin is
// installed the B1 dispatch command takes precedence (first-wins), but
// the stub's Long is used as fallback for help disclosure.
func newMcpStubCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "mcp",
		Short:   "EKA MCP server and plugin tooling",
		Long:    mcpStubLong(),
		GroupID: groupUtility,
		Hidden:  true, // not shown in `eka --help` when plugin not installed; `eka mcp -h` still works
		// Help is native; dispatch when invoked with args is handled by
		// the plugin dispatch command when installed. The stub itself
		// runs help when executed without a plugin.
		RunE: func(cmd *cobra.Command, args []string) error {
			// Help-only forms render help deterministically.
			if len(args) == 1 && (args[0] == "-h" || args[0] == "--help" || args[0] == "help") {
				return cmd.Help()
			}
			if len(args) == 0 {
				return cmd.Help()
			}
			// Not help and plugin not installed: deterministic install hint.
			return fmt.Errorf("plugin \"mcp\" is not installed — install it with: eka plugin install mcp")
		},
	}
	// Disclosure subcommands as cobra subcommands for Available Commands
	// rendering. They are help-only disclosure when plugin not installed;
	// when installed the parent dispatch proxies the whole binary, so
	// these subcommands are not used for dispatch but ensure
	// `eka mcp -h` lists them via the standard Available Commands block.
	for _, sub := range mcpSubcommands {
		subCmd := &cobra.Command{
			Use:   sub,
			Short: fmt.Sprintf("Run the %s subcommand of the MCP plugin", sub),
			Long:  fmt.Sprintf("Run the %s subcommand of the MCP plugin (proxied to eka-mcp).", sub),
			RunE: func(cmd *cobra.Command, args []string) error {
				return fmt.Errorf("plugin \"mcp\" is not installed — install it with: eka plugin install mcp")
			},
		}
		cmd.AddCommand(subCmd)
	}
	return cmd
}

// findMcpCommand returns the "mcp" command on root if present.
func findMcpCommand(root *cobra.Command) *cobra.Command {
	for _, c := range root.Commands() {
		if c.Name() == "mcp" {
			return c
		}
	}
	return nil
}
