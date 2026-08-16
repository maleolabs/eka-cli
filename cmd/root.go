// Package cmd implements the EKA CLI as a thin Cobra command layer.
//
// The command tree (root, validate, init, export, import, get, view,
// watch, sync, project, status, feedback, update, note, assign,
// unassign, reassign, plugin) is the only part of the codebase that
// knows about argument parsing, flags, help text, output rendering and
// exit codes. It contains no domain
// logic: validate delegates to the Authoring API (runtime.Authoring),
// init delegates to the bootstrap engine, export/import delegate to the
// exchange engine, get delegates to the machine interface (machine/),
// feedback delegates to the standalone feedback module (feedback/),
// plugin delegates to the plugin contract + registry (plugin/), and
// every runtime command (sync, view, watch, project, status, integrity)
// delegates to the Runtime Kernel services (the runtime package) — the
// CLI is a CLIENT of the Runtime.
//
// Client-only boundary (milestone 5, documented): production code in
// this package must NOT import the store, workspace, sync or compile
// packages — SQLite and the workspace are private implementation
// details of the Runtime Kernel, and all knowledge access goes
// through the runtime services. The allowed production imports are
// runtime (the kernel API), exchange (the CKO model + PackageError),
// view (the projection engine), machine (the machine interface),
// conformance (model types, e.g. Report for render helpers, plus the
// representation-independent reference-parsing helper ParseReference —
// authoring validation itself runs through runtime.Authoring),
// bootstrap (init), feedback (the standalone ADR-026 feedback module),
// plugin (the plugin contract, registry and install flow) and ui.
// Tests MAY import store/workspace/sync for seeding and corruption
// fixtures (test-only, documented).
//
// Two documented exceptions exist, each justified in its file: the
// workspace registry writes of `eka project register` (cmd/project.go
// — the Runtime's WorkspaceService does not expose the metadata
// registration path), and the assignment edge writes of
// cmd/assigned.go (the Authoring API's relate is edge-add only, so
// `eka unassign`/`eka reassign` mirror the relate published-path
// re-point and the draft rewrite at the store/workspace layer; the
// mirror stays a faithful copy of the runtime mechanism so the two
// cannot drift).
//
// Layout rationale: the reusable engines stay where they are
// (bootstrap/, conformance/, exchange/, ...). There is deliberately no
// internal/ or pkg/ directory — those add indirection without serving
// any immediate consumer, and the task rules out speculative
// abstraction. cmd/ is a leaf: nothing imports it except
// cmd/eka/main.go.
//
// Exit codes (deterministic contract, preserved from the pre-Cobra CLI):
//
//	0  fully compliant (warnings allowed); init completed and validates;
//	   export produced a package
//	1  blocking violations present (validate; init produced a
//	   non-conformant repository; export refused the repository)
//	2  usage or internal error (unknown command, invalid path,
//	   unreadable scan root, bad export target, export failure)
package cmd

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/maleolabs/eka-cli/cmd/ui"
	"github.com/spf13/cobra"
)

// Exit codes of the deterministic contract documented above.
const (
	exitOK    = 0
	exitFail  = 1
	exitUsage = 2
)

// exitError carries an explicit exit code out of a command's RunE. Commands
// that must exit non-zero after printing their own diagnostics return it;
// Execute maps the code to the process exit code without printing anything.
type exitError struct{ code int }

func (e *exitError) Error() string { return fmt.Sprintf("exit code %d", e.code) }

// Execute runs the EKA CLI with the given arguments and streams and
// returns the process exit code. It is the single testable entry point:
// main() delegates to it, tests call it directly.
func Execute(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if args == nil {
		args = []string{}
	}
	root := newRootCommand()
	root.SetArgs(args)
	root.SetIn(stdin)
	root.SetOut(stdout)
	root.SetErr(stderr)

	err := root.Execute()
	if err == nil {
		return exitOK
	}
	var ee *exitError
	if errors.As(err, &ee) {
		return ee.code
	}
	fmt.Fprintf(stderr, "eka: %s\n", renderError(err))
	return exitUsage
}

// renderError formats an execution error deterministically. Cobra's
// "unknown command" errors are augmented with the list of available
// commands, mirroring the pre-Cobra CLI contract. Any other error is
// passed through verbatim ("eka: <error>").
func renderError(err error) string {
	msg := err.Error()
	if strings.HasPrefix(msg, "unknown command") {
		msg += " — available commands: " + availableCommands()
	}
	return msg
}

// availableCommands lists the registered subcommands in registration
// order, excluding the built-in help/completion commands (which a fresh
// tree does not contain yet).
func availableCommands() string {
	names := make([]string, 0, 2)
	for _, c := range newRootCommand().Commands() {
		names = append(names, c.Name())
	}
	return strings.Join(names, ", ")
}

// newRootCommand builds the root command with all subcommands registered.
// A fresh tree is built per Execute call so that SetArgs/SetIn/SetOut/
// SetErr never leak between invocations (and concurrent Executes stay
// safe).
func newRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:   "eka",
		Short: "Official EKA CLI: conformance validation and repository bootstrapping",
		Long: `The official EKA CLI.

eka validate checks a repository against the EKA conformance rules,
eka init bootstraps a new EKA repository from the embedded skeleton
(validating the result afterwards), eka export projects a repository
to a deterministic package in the EKA Reference Serialization Format
(RSF) v1.0, eka import consumes such a package, eka view projects
the Engineering Knowledge Model (sprint/wave/ticket views), eka
get retrieves Engineering Knowledge as machine-readable CKO JSON
(the machine interface — scripts, MCP, Atrium, AI agents), eka
watch re-renders a projection live as the repository changes, eka
update replaces this binary with the latest checksum-verified
release from GitHub, eka plugin installs, lists, removes and
updates official checksum-verified plugins (e.g. eka-mcp),
and the Knowledge Runtime commands (eka sync,
eka project, eka status, eka integrity) keep a local canonical
workspace (~/.eka or $EKA_HOME) synchronized with registered
repositories via
deterministic snapshots — immutable, content-addressed knowledge
objects whose integrity eka integrity check verifies.

Command output is deterministic: the same input always produces the
same bytes. On a terminal the output is colored and progress is shown
in place; when piped or redirected it is plain text with no control
sequences. Use --verbose/-v for additional detail lines (per-unit
lists, plan actions).

Exit codes:
  0  fully compliant (warnings allowed)
  1  blocking violations present
  2  usage or internal error (unknown command, invalid path,
     unreadable root)`,
		// `eka` without a subcommand shows the product landing: a calm
		// orientation (what the CLI is, its commands, help and version
		// pointers) instead of the raw usage dump. Landing is
		// informational output — it exits 0. Unknown subcommands remain
		// usage errors (exit 2).
		//
		// `eka --version` prints the CLI version instead of the landing:
		// one deterministic line ("eka <version>") through the same
		// presentation writer `eka version` uses — byte-identical to
		// the first line of `eka version`, the same single source (the
		// ldflags-injected `version` variable).
		RunE: func(cmd *cobra.Command, args []string) error {
			if showVersion, _ := cmd.Flags().GetBool(flagVersion); showVersion {
				fmt.Fprintf(styleFor(cmd).W, "eka %s\n", version)
				return nil
			}
			printLanding(styleFor(cmd))
			return nil
		},
		// The CLI owns all error output: SilenceErrors + SilenceUsage on
		// the root suppress cobra's "Error: …" prefix and its automatic
		// usage dumps (children inherit both flags). Execute renders
		// every error as a single deterministic "eka: …" line and maps
		// it to the exit code contract. Help output is unaffected: the
		// flag.ErrHelp path prints help to stdout and exits 0.
		SilenceErrors: true,
		SilenceUsage:  true,
	}
	root.PersistentFlags().BoolP(flagVerbose, "v", false,
		"verbose output: additional detail lines (per-unit lists, plan actions)")
	root.PersistentFlags().Bool(flagVersion, false,
		"print the CLI version (the same first line 'eka version' reports) and exit")
	root.AddCommand(newValidateCommand(), newInitCommand(), newExportCommand(), newImportCommand(),
		newGetCommand(), newContextCommand(), newViewCommand(), newWatchCommand(), newSyncCommand(), newProjectCommand(),
		newStatusCommand(), newIntegrityCommand(), newUpdateCommand(), newVersionCommand(),
		newTransitionCommand(), newNoteCommand(), newFeedbackCommand(), newSnapshotCommand(),
		newPluginCommand(), newAssignCommand(), newUnassignCommand(), newReassignCommand())
	root.AddCommand(newAuthoringCommands()...)
	// The root help and the landing Commands list are grouped by intent
	// (Authoring / Repository & Exchange / Knowledge Access / Runtime &
	// Workspace / Utility): cobra AddGroup/GroupID is the data model,
	// assignCommandGroups distributes the registered commands, and the
	// two cobra setters give the built-in help/completion commands their
	// Utility group when ExecuteC creates them (without creating them
	// here, so the fresh-tree command list stays built-in-free).
	root.AddGroup(commandGroups...)
	assignCommandGroups(root)
	root.SetHelpCommandGroupID(groupUtility)
	root.SetCompletionCommandGroupID(groupUtility)
	// The output container wraps the help text of EVERY command
	// (HelpFunc resolves up the tree): the help renderer produces the
	// styled text (colors follow the real writer through the Style), the
	// container adds the uniform left margin and the leading/trailing
	// blank lines — help never sticks to the terminal corner.
	root.SetHelpFunc(func(cmd *cobra.Command, args []string) {
		s := styleFor(cmd)
		out := cmd.OutOrStdout()
		buf := &bytes.Buffer{}
		cmd.SetOut(buf)
		renderHelp(cmd, s, buf)
		cmd.SetOut(out)
		ui.Container(s, buf.String())
	})
	return root
}

// printLanding renders the root landing page: a calm product orientation
// without banners or decoration — heading, one-line description, the
// intent-grouped command overview (the same renderCommandGroups the root
// help uses, so the two never drift apart), and pointers to help and
// version — wrapped in the output container (uniform left margin +
// leading/trailing blank lines), so it never sticks to the terminal
// corner. Deterministic on non-TTY output; on a color TTY the heading
// and section/group headers are accent-colored, command names Info and
// the help hints Dim.
func printLanding(s *ui.Style) {
	var b strings.Builder
	fmt.Fprintln(&b, s.Accent("Engineering Knowledge Architecture (EKA)"))
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "The official command-line interface for the EKA engineering")
	fmt.Fprintln(&b, "knowledge standard: bootstrap, validate, exchange, and run")
	fmt.Fprintln(&b, "the knowledge runtime (sync, projects, status, integrity).")
	fmt.Fprintf(&b, "%s\n", s.Dim("New here? Run 'eka init' to bootstrap a repository."))
	fmt.Fprintln(&b)
	fmt.Fprint(&b, s.Accent("Commands"))
	renderCommandGroups(s, &b, commandGroups, newRootCommand().Commands())
	// renderCommandGroups leaves the last member line unterminated:
	// close it and add the blank line before the next section.
	fmt.Fprintln(&b)
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, s.Accent("Help"))
	fmt.Fprintf(&b, "  %s\n", s.Dim("Run 'eka help <command>' for command details,"))
	fmt.Fprintf(&b, "  %s\n", s.Dim("or 'eka <command> --help' for usage."))
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, s.Accent("Version"))
	fmt.Fprintf(&b, "  %s (EKA standard %s)\n", version, standardVersion)
	fmt.Fprintf(&b, "  %s\n", s.Dim("Run 'eka --version' for just the CLI version."))
	ui.Container(s, b.String())
}
