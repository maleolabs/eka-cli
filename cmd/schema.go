package cmd

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// schemaReport is the `eka schema --json` document (schema eka-cli-schema-v1):
// a machine-readable description of the command surface.
//
// Why it exists: an agent driving this CLI otherwise has to scrape human help
// text to learn a command's flags, which are repeatable, and what the output
// looks like. That is paid on every session, and a wrong guess costs a retry
// loop. The information already exists in the command definitions; this command
// exposes it instead of asking every caller to rediscover it.
//
// The report is generated from the live cobra tree, never hand-maintained, so it
// cannot drift from behaviour.
type schemaReport struct {
	Schema   string          `json:"schema"`
	Version  string          `json:"version"`
	Commands []schemaCommand `json:"commands"`
}

// schemaCommand describes one command.
type schemaCommand struct {
	Name string `json:"name"`
	// Path is the space-joined command path (e.g. "draft validate"), so
	// subcommands are addressable without walking the tree.
	Path string `json:"path"`
	// Group is the intent group the command is presented under, matching the
	// root help grouping.
	Group string `json:"group,omitempty"`
	// Short is the one-line summary. Long is omitted: it is prose for a
	// human reader and would dominate the document for no machine benefit.
	Short string `json:"short"`
	// Mutates reports whether invoking the command can change knowledge,
	// drafts, or the workspace. Derived from the authoring/lifecycle group
	// assignment, which is the existing single source for intent.
	Mutates     bool         `json:"mutates"`
	Usage       string       `json:"usage,omitempty"`
	Flags       []schemaFlag `json:"flags"`
	Subcommands []string     `json:"subcommands,omitempty"`
}

// schemaFlag describes one flag on a command.
type schemaFlag struct {
	Name string `json:"name"`
	// Shorthand is the single-letter form, empty when none.
	Shorthand string `json:"shorthand,omitempty"`
	Type      string `json:"type"`
	// Default is the flag's default rendered as text, empty when the zero
	// value is the default.
	Default string `json:"default,omitempty"`
	// Repeatable reports whether a list-valued flag may be given more than
	// once. Cobra does not model this, so it is derived from the flag type.
	Repeatable  bool   `json:"repeatable"`
	Required    bool   `json:"required"`
	Description string `json:"description"`
}

// newSchemaCommand builds `eka schema`: emit the machine-readable description
// of the whole command surface.
func newSchemaCommand() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "schema",
		Short: "Describe the command surface for machine consumers",
		Long: `Describe every command: its path, intent group, whether it mutates
state, its flags with type and repeatability, and its subcommands.

The description is generated from the live command tree, so it cannot drift
from behaviour. It is the one call an agent needs to learn the surface instead
of scraping help output.

--json emits the report as a single machine-readable document
(schema eka-cli-schema-v1). Plain output is a readable summary.`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, args []string) error {
			root := c.Root()
			report := buildSchemaReport(root)
			if !asJSON {
				s := styleFor(c)
				var b strings.Builder
				fmt.Fprintf(&b, "%s %d commands\n", s.Accent("eka schema"), len(report.Commands))
				for _, sc := range report.Commands {
					marker := " "
					if sc.Mutates {
						marker = "!"
					}
					fmt.Fprintf(&b, "  %s %-24s %s\n", marker, sc.Path, sc.Short)
				}
				fmt.Fprintf(&b, "\n  %s mutates state · use --json for the full contract\n", s.Dim("!"))
				_, err := c.OutOrStdout().Write([]byte(b.String()))
				return err
			}
			enc := json.NewEncoder(c.OutOrStdout())
			enc.SetEscapeHTML(false)
			enc.SetIndent("", "  ")
			return enc.Encode(report)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit the report as JSON (schema eka-cli-schema-v1)")
	return cmd
}

// buildSchemaReport walks the command tree and describes every command.
//
// The help and completion built-ins are excluded: they are cobra's, not the
// CLI's contract, and listing them would describe a surface this project does
// not own.
func buildSchemaReport(root *cobra.Command) schemaReport {
	report := schemaReport{Schema: "eka-cli-schema-v1", Version: version, Commands: []schemaCommand{}}
	var walk func(c *cobra.Command, path []string)
	walk = func(c *cobra.Command, path []string) {
		children := c.Commands()
		sort.Slice(children, func(i, j int) bool { return children[i].Name() < children[j].Name() })
		for _, child := range children {
			name := child.Name()
			if name == "help" || name == "completion" {
				continue
			}
			childPath := append(append([]string{}, path...), name)
			if !child.Hidden {
				report.Commands = append(report.Commands, describeCommand(child, childPath, path))
			}
			walk(child, childPath)
		}
	}
	walk(root, nil)
	sort.Slice(report.Commands, func(i, j int) bool {
		return report.Commands[i].Path < report.Commands[j].Path
	})
	return report
}

// describeCommand renders one command's contract. topLevel is the command's
// root-level ancestor path: group assignment and mutation risk are recorded
// per top-level command, so a subcommand inherits them rather than appearing
// unclassified.
func describeCommand(c *cobra.Command, path, topLevel []string) schemaCommand {
	sc := schemaCommand{
		Name:    c.Name(),
		Path:    strings.Join(path, " "),
		Short:   c.Short,
		Usage:   c.Use,
		Mutates: commandMutates(topLevelName(topLevel, c.Name())),
		Flags:   []schemaFlag{},
	}
	sc.Group = commandGroup(c, topLevel)
	// A command outside the static intent model is plugin-provided; it
	// inherits the plugin lifecycle's mutation semantics rather than
	// reporting "read-only" for something that configures or installs.
	if _, known := commandGroupsByName[topLevelName(topLevel, c.Name())]; !known {
		sc.Mutates = !pluginReadOnlyCommands[c.Name()]
	}
	for _, child := range c.Commands() {
		if child.Hidden || child.Name() == "help" || child.Name() == "completion" {
			continue
		}
		sc.Subcommands = append(sc.Subcommands, child.Name())
	}
	sort.Strings(sc.Subcommands)

	flags := c.Flags()
	flags.SortFlags = true
	flags.VisitAll(func(f *pflag.Flag) {
		sc.Flags = append(sc.Flags, schemaFlag{
			Name:        f.Name,
			Shorthand:   f.Shorthand,
			Type:        f.Value.Type(),
			Default:     defaultIfSet(f),
			Repeatable:  f.Value.Type() == "stringSlice" || f.Value.Type() == "stringArray" || f.Value.Type() == "intSlice",
			Required:    isFlagRequired(f),
			Description: f.Usage,
		})
	})
	return sc
}

// commandGroup returns the presentation group of a command: the static intent
// model for the command's root-level ancestor, the group the command already
// carries for a plugin-provided one, empty when neither applies.
func commandGroup(c *cobra.Command, topLevel []string) string {
	name := topLevelName(topLevel, c.Name())
	if g, ok := commandGroupsByName[name]; ok {
		return g
	}
	if c.GroupID == groupPlugins {
		return groupPlugins
	}
	return ""
}

// commandMutates reports whether a top-level command can change knowledge, a
// draft, or the workspace.
func commandMutates(name string) bool {
	return !readOnlyCommands[name]
}

// topLevelName returns the name whose group assignment governs a command:
// the outermost path element for a subcommand, the command's own name at the
// root.
func topLevelName(topLevel []string, ownName string) string {
	if len(topLevel) > 0 {
		return topLevel[0]
	}
	return ownName
}

// defaultIfSet renders a flag's default only when it differs from the zero
// value, so the report does not carry noise for the common case.
func defaultIfSet(f *pflag.Flag) string {
	def := f.DefValue
	switch def {
	case "", "false", "0", "[]", "<nil>":
		return ""
	}
	return def
}

// isFlagRequired reports whether the command marked the flag required.
// Cobra records that in the flag's annotations rather than as a field.
func isFlagRequired(f *pflag.Flag) bool {
	if f.Annotations == nil {
		return false
	}
	// cobra.BashCompOneRequiredFlag is the annotation key it uses for
	// MarkFlagRequired; spelled out so this file needs no cobra import
	// beyond the command type it already uses.
	if values, ok := f.Annotations["cobra_annotation_bash_completion_one_required_flag"]; ok {
		return len(values) > 0 && values[0] == "true"
	}
	return false
}

// readOnlyCommands are the top-level commands that never change knowledge, a
// draft, or the workspace. Mutation is decided by this allow-list rather than
// by the intent group, because the group is a presentation concern: `feedback`
// sits in Utility alongside `version` yet writes drafts and opens GitHub
// issues. Under-reporting a mutation is the dangerous direction, so a command
// absent from this list is reported as mutating.
//
// A new read-only command must be added here deliberately; a new writing
// command needs nothing.
var readOnlyCommands = map[string]bool{
	"get":           true,
	"context":       true,
	"view":          true,
	"watch":         true,
	"code-context":  true,
	"code-discover": true,
	"code-get":      true,
	"status":        true,
	"integrity":     true,
	"version":       true,
	"schema":        true,
	"help":          true,
	"completion":    true,
}

// pluginReadOnlyCommands are plugin-provided commands that only inspect. A
// plugin command carries no risk metadata of its own, so the plugin lifecycle
// verbs (configure, install, remove, update) are treated as writing.
var pluginReadOnlyCommands = map[string]bool{
	"manifest": true,
	"list":     true,
	"show":     true,
}
