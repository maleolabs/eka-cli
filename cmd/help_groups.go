package cmd

import (
	"fmt"
	"io"

	"github.com/maleolabs/eka-cli/cmd/ui"
	"github.com/spf13/cobra"
)

// This file implements the intent-grouping data model behind the root
// help ("Available Commands") and the landing "Commands" list: five
// groups — Authoring, Repository & Exchange, Knowledge Access, Runtime
// & Workspace, Utility — modelled with cobra's AddGroup/GroupID (the
// CLI groups the commands it registers, and the built-in help/completion
// commands join the Utility group through the cobra group-ID setters).
// The grouping is a pure presentation concern: no command behavior
// changes, and the shared renderer (renderCommandGroups) is the single
// place that knows how to draw a grouped command list, used by both
// the root help and the landing so the two never drift apart.

// Command group IDs (cobra.Group.ID). The IDs are stable identifiers
// referenced by every command's GroupID field; the Titles below are
// the display strings the renderer prints as the group headers.
const (
	groupAuthoring          = "authoring"
	groupRepositoryExchange = "repository-exchange"
	groupKnowledgeAccess    = "knowledge-access"
	groupRuntimeWorkspace   = "runtime-workspace"
	groupUtility            = "utility"
	// groupPlugins is the DYNAMIC group of plugin-registered commands
	// (B1 deferred registration, ADR-031): it is added to the root only
	// when plugin commands were registered, so a plugin-free CLI renders
	// exactly the five static intent groups.
	groupPlugins = "plugins"
)

// commandGroups is the ordered set of intent groups rendered by
// renderCommandGroups. Order is presentation order (root help and
// landing use the same slice). The titles double as the group headers.
var commandGroups = []*cobra.Group{
	{ID: groupAuthoring, Title: "Authoring"},
	{ID: groupRepositoryExchange, Title: "Repository & Exchange"},
	{ID: groupKnowledgeAccess, Title: "Knowledge Access"},
	{ID: groupRuntimeWorkspace, Title: "Runtime & Workspace"},
	{ID: groupUtility, Title: "Utility"},
}

// commandGroupsByName maps every registered command name (including the
// cobra built-ins help and completion) to its intent group. It is the
// single source of truth for the group data model: assignCommandGroups
// consumes it, and the membership tests assert against it.
var commandGroupsByName = map[string]string{
	// Authoring: creating, editing and publishing knowledge.
	"new":        groupAuthoring,
	"edit":       groupAuthoring,
	"draft":      groupAuthoring,
	"publish":    groupAuthoring,
	"discard":    groupAuthoring,
	"relate":     groupAuthoring,
	"transition": groupAuthoring,
	"note":       groupAuthoring,
	"assign":     groupAuthoring,
	"reassign":   groupAuthoring,
	"unassign":   groupAuthoring,
	// Repository & Exchange: repository lifecycle and RSF exchange.
	"init":     groupRepositoryExchange,
	"validate": groupRepositoryExchange, // user intent: repo conformance, though it runs through the Authoring API internally
	"export":   groupRepositoryExchange,
	"import":   groupRepositoryExchange,
	// Knowledge Access: reading Engineering Knowledge.
	"get":           groupKnowledgeAccess,
	"context":       groupKnowledgeAccess,
	"view":          groupKnowledgeAccess,
	"watch":         groupKnowledgeAccess,
	"code-context":  groupKnowledgeAccess,
	"code-discover": groupKnowledgeAccess,
	"code-get":      groupKnowledgeAccess,
	// Runtime & Workspace: the Knowledge Runtime and its local workspace.
	"sync":      groupRuntimeWorkspace,
	"project":   groupRuntimeWorkspace,
	"status":    groupRuntimeWorkspace,
	"integrity": groupRuntimeWorkspace,
	"snapshot":  groupRuntimeWorkspace, // inspection/repair of workspace snapshots is a runtime concern, not authoring
	// Utility: CLI maintenance and help.
	"update":     groupUtility,
	"version":    groupUtility,
	"feedback":   groupUtility,
	"plugin":     groupUtility,
	"mcp":        groupUtility,
	"completion": groupUtility,
	"help":       groupUtility,
}

// assignCommandGroups sets the GroupID of every registered subcommand
// from the commandGroupsByName model. It runs on the fresh tree in
// newRootCommand; the built-in help/completion commands (which cobra
// only creates at Execute time) receive their Utility group through
// SetHelpCommandGroupID/SetCompletionCommandGroupID instead.
// Plugin-registered commands already carry groupPlugins; preserve that.
func assignCommandGroups(root *cobra.Command) {
	for _, c := range root.Commands() {
		if c.GroupID == groupPlugins {
			continue
		}
		if g, ok := commandGroupsByName[c.Name()]; ok {
			c.GroupID = g
		} else if c.GroupID == "" {
			// Unknown command without group (future plugin) stays
			// without group so renderCommandGroups puts it in
			// Additional Commands; plugin commands already have
			// groupPlugins set via newPluginDispatchCommand.
		}
	}
}

// commandGroupsFor returns the display group list of root: the static
// intent groups plus the dynamic Plugins group when root carries
// plugin-registered commands (B1 deferred registration, ADR-031). The
// root help (cmd.Groups()) and the landing use the same list, so the
// two never drift apart.
func commandGroupsFor(root *cobra.Command) []*cobra.Group {
	groups := make([]*cobra.Group, 0, len(commandGroups)+1)
	groups = append(groups, commandGroups...)
	if hasPluginCommands(root) {
		groups = append(groups, &cobra.Group{ID: groupPlugins, Title: "Plugins"})
	}
	return groups
}

// hasPluginCommands reports whether root carries any plugin-registered
// command (GroupID groupPlugins).
func hasPluginCommands(root *cobra.Command) bool {
	for _, c := range root.Commands() {
		if c.GroupID == groupPlugins {
			return true
		}
	}
	return false
}

// renderCommandGroups renders a grouped command list: for every group in
// order a header line (Accent) followed by its members, each as a padded
// command name (Info) and a plain description — byte-compatible with
// cobra's grouped usage layout (rpad/NamePadding, "\n\n" separators).
// Commands without a group fall into a trailing "Additional Commands:"
// section (cobra's ungrouped contract); with the full name→group model
// above that section never renders in practice. cmds must be in display
// order (cobra sorts with EnableCommandSorting).
func renderCommandGroups(s *ui.Style, w io.Writer, groups []*cobra.Group, cmds []*cobra.Command) {
	for _, group := range groups {
		fmt.Fprintf(w, "\n\n%s", s.Accent(group.Title))
		for _, sub := range cmds {
			if sub.GroupID != group.ID {
				continue
			}
			if sub.IsAvailableCommand() || sub.Name() == helpCommandName {
				fmt.Fprintf(w, "\n  %s %s",
					s.Info(rpad(sub.Name(), sub.NamePadding())), sub.Short)
			}
		}
	}
	if !allChildCommandsHaveGroup(cmds) {
		fmt.Fprint(w, "\n\nAdditional Commands:")
		for _, sub := range cmds {
			if sub.GroupID != "" {
				continue
			}
			if sub.IsAvailableCommand() || sub.Name() == helpCommandName {
				fmt.Fprintf(w, "\n  %s %s",
					s.Info(rpad(sub.Name(), sub.NamePadding())), sub.Short)
			}
		}
	}
}

// allChildCommandsHaveGroup reports whether every displayed command
// carries a GroupID — cobra's AllChildCommandsHaveGroup contract
// (help-compatible: the built-in help command counts as displayed).
func allChildCommandsHaveGroup(cmds []*cobra.Command) bool {
	for _, sub := range cmds {
		if (sub.IsAvailableCommand() || sub.Name() == helpCommandName) && sub.GroupID == "" {
			return false
		}
	}
	return true
}

// helpCommandName is the cobra built-in help command name, mirrored here
// so the grouped renderer can include it without depending on cobra's
// unexported constant.
const helpCommandName = "help"

// rpad right-pads s to width, matching cobra's rpad helper so the
// grouped layout is byte-compatible with cobra's default usage output.
func rpad(s string, width int) string {
	return fmt.Sprintf("%-*s", width, s)
}
