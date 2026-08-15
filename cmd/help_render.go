package cmd

import (
	"fmt"
	"io"
	"strings"
	"unicode"

	"github.com/maleolabs/eka-cli/cmd/ui"
	"github.com/spf13/cobra"
)

// This file implements the CLI's own help renderer. Cobra's default
// help path renders through templates and an internal buffer swap that
// would hide the terminal's TTY/color state, so the CLI renders help
// itself: the same layout cobra's defaultUsageFunc produces (the two
// must stay in sync), with colors flowing through the ui.Style paint
// path — command names Info, flag names Dim, group headers Accent
// (root only), the trailing help hint Dim, descriptions plain. On a
// non-TTY every paint call is the identity, so the output stays plain
// text, byte-identical in structure to cobra's.

// renderHelp renders the full help text of cmd: the long (or short)
// description followed by the usage block, mirroring cobra's
// defaultHelpFunc. It is wired as the root HelpFunc (children resolve
// up the tree), so it serves root and subcommand help alike; subcommand
// help is colored but flat — no group headers.
func renderHelp(cmd *cobra.Command, s *ui.Style, w io.Writer) {
	text := cmd.Long
	if text == "" {
		text = cmd.Short
	}
	text = strings.TrimRightFunc(text, unicode.IsSpace)
	if text != "" {
		fmt.Fprintln(w, text)
		fmt.Fprintln(w)
	}
	if cmd.Runnable() || cmd.HasSubCommands() {
		renderUsage(cmd, s, w)
	}
}

// renderUsage mirrors cobra's defaultUsageFunc: the same deterministic
// layout (usage lines, command list, flags, hint) with the group-aware
// command section. The root renders its five intent groups through
// renderCommandGroups; commands without groups (subcommands) render the
// flat "Available Commands:" list.
func renderUsage(cmd *cobra.Command, s *ui.Style, w io.Writer) {
	fmt.Fprint(w, "Usage:")
	if cmd.Runnable() {
		fmt.Fprintf(w, "\n  %s", cmd.UseLine())
	}
	if cmd.HasAvailableSubCommands() {
		fmt.Fprintf(w, "\n  %s [command]", cmd.CommandPath())
	}
	if len(cmd.Aliases) > 0 {
		fmt.Fprintf(w, "\n\nAliases:\n  %s", cmd.NameAndAliases())
	}
	if cmd.HasExample() {
		fmt.Fprintf(w, "\n\nExamples:\n%s", cmd.Example)
	}
	if cmd.HasAvailableSubCommands() {
		cmds := cmd.Commands()
		if len(cmd.Groups()) == 0 {
			// Flat list (subcommands): no group headers — the intent
			// groups are a root-level concept.
			fmt.Fprint(w, "\n\nAvailable Commands:")
			for _, sub := range cmds {
				if sub.IsAvailableCommand() || sub.Name() == helpCommandName {
					fmt.Fprintf(w, "\n  %s %s",
						s.Info(rpad(sub.Name(), sub.NamePadding())), sub.Short)
				}
			}
		} else {
			renderCommandGroups(s, w, cmd.Groups(), cmds)
		}
	}
	if cmd.HasAvailableLocalFlags() {
		fmt.Fprint(w, "\n\nFlags:\n")
		renderFlagUsages(s, w, strings.TrimRightFunc(cmd.LocalFlags().FlagUsages(), unicode.IsSpace))
	}
	if cmd.HasAvailableInheritedFlags() {
		fmt.Fprint(w, "\n\nGlobal Flags:\n")
		renderFlagUsages(s, w, strings.TrimRightFunc(cmd.InheritedFlags().FlagUsages(), unicode.IsSpace))
	}
	if cmd.HasHelpSubCommands() {
		fmt.Fprint(w, "\n\nAdditional help topics:")
		for _, sub := range cmd.Commands() {
			if sub.IsAdditionalHelpTopicCommand() {
				fmt.Fprintf(w, "\n  %s %s",
					rpad(sub.CommandPath(), sub.CommandPathPadding()), sub.Short)
			}
		}
	}
	if cmd.HasAvailableSubCommands() {
		fmt.Fprintf(w, "\n\n%s", s.Dim(fmt.Sprintf(
			"Use %q for more information about a command.",
			cmd.CommandPath()+" [command] --help")))
	}
	fmt.Fprintln(w)
}

// renderFlagUsages writes pflag's flag-usages text with the flag-name
// column rendered in Dim and the descriptions plain (amber is reserved
// for warnings and is deliberately not used here). Non-color output is
// byte-identical to the input. The name column ends at the first run of
// two or more spaces after the leading indentation — pflag pads names
// to a computed alignment, and names never contain consecutive spaces;
// a line without such a separator (a wrapped usage continuation) is
// written plain.
func renderFlagUsages(s *ui.Style, w io.Writer, usages string) {
	if !s.Color {
		io.WriteString(w, usages)
		return
	}
	for _, line := range strings.SplitAfter(usages, "\n") {
		if i := flagNameEnd(line); i > 0 {
			io.WriteString(w, s.Dim(line[:i]))
			io.WriteString(w, line[i:])
		} else {
			io.WriteString(w, line)
		}
	}
}

// flagNameEnd returns the index at which a flag line's name column ends
// (the usage text begins), or -1 when the line carries no such
// separator. The separator is the first run of two or more spaces that
// follows a non-space character (the last character of the name) —
// flag names, with or without a shorthand, contain single spaces only,
// and the leading indentation is part of the name column.
func flagNameEnd(line string) int {
	for i := 1; i+1 < len(line); i++ {
		if line[i] == ' ' && line[i+1] == ' ' && line[i-1] != ' ' {
			return i
		}
	}
	return -1
}
