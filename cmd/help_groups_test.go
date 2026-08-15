package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/maleolabs/eka-cli/cmd/ui"
	"github.com/spf13/cobra"
)

// The intent-group data model behind the root help and the landing:
// membership assertions (the five groups and every registered command)
// and the shared grouped renderer (colors on a color Style, plain
// bytes on a non-color one). See help_groups.go for the model itself.

// groupMember wants the exact membership of the five intent groups as
// registered on the fresh tree (the built-in help/completion commands,
// which cobra only creates at Execute time, are covered by the root
// help assertions below).
var groupMember = map[string][]string{
	groupAuthoring:          {"discard", "draft", "edit", "new", "note", "publish", "relate", "transition"},
	groupRepositoryExchange: {"export", "import", "init", "validate"},
	groupKnowledgeAccess:    {"context", "get", "view", "watch"},
	groupRuntimeWorkspace:   {"integrity", "project", "snapshot", "status", "sync"},
	groupUtility:            {"feedback", "plugin", "update", "version"},
}

// TestCommandGroupMembership: every registered command carries the
// GroupID of its intent group, the five groups contain exactly their
// expected members, and every command with a GroupID has a registered
// group. Cobra panics at Execute time only when a GroupID is SET but
// not registered; a command left without a GroupID instead falls into
// the renderer's "Additional Commands:" section — asserted here so a
// future command without a group fails the build, not a panic or a
// silent fallback.
func TestCommandGroupMembership(t *testing.T) {
	root := newRootCommand()

	// Group titles and order.
	if len(root.Groups()) != 5 {
		t.Fatalf("root has %d groups, want 5", len(root.Groups()))
	}
	var titles []string
	for _, g := range root.Groups() {
		titles = append(titles, g.Title)
	}
	if got, want := strings.Join(titles, "|"),
		"Authoring|Repository & Exchange|Knowledge Access|Runtime & Workspace|Utility"; got != want {
		t.Errorf("group titles = %q, want %q", got, want)
	}

	// Membership: every group member carries the group's ID.
	for groupID, members := range groupMember {
		for _, name := range members {
			c := findCommand(root, name)
			if c == nil {
				t.Errorf("group %s: command %q not registered", groupID, name)
				continue
			}
			if c.GroupID != groupID {
				t.Errorf("command %q: GroupID = %q, want %q", name, c.GroupID, groupID)
			}
		}
	}

	// No stray members: every registered command is in exactly one group
	// (each GroupID is registered — the panic guard), and no command is
	// left without a group.
	for _, c := range root.Commands() {
		if c.GroupID == "" {
			t.Errorf("command %q has no group", c.Name())
			continue
		}
		found := false
		for _, g := range root.Groups() {
			if g.ID == c.GroupID {
				found = true
			}
		}
		if !found {
			t.Errorf("command %q: GroupID %q has no registered group", c.Name(), c.GroupID)
		}
		if !contains(groupMember[c.GroupID], c.Name()) {
			t.Errorf("command %q is not an expected member of group %q", c.Name(), c.GroupID)
		}
	}
}

// TestRootHelpShowsGroups: the root help renders the five intent groups
// with the correct membership (acceptance criterion 1), including the
// built-in completion/help commands in Utility, and carries no ANSI on
// the non-TTY writer.
func TestRootHelpShowsGroups(t *testing.T) {
	code, out, errText := runIn([]string{"--help"})
	if code != 0 {
		t.Fatalf("--help: exit = %d, want 0", code)
	}
	if errText != "" {
		t.Errorf("--help: stderr must be empty, got %q", errText)
	}
	if strings.Contains(out, "\x1b") {
		t.Errorf("non-TTY root help must not contain ANSI:\n%q", out)
	}
	if strings.Contains(out, "Available Commands:") {
		t.Errorf("grouped root help must not keep the flat Available Commands label:\n%s", out)
	}
	for _, want := range []string{
		"Authoring",
		"Repository & Exchange",
		"Knowledge Access",
		"Runtime & Workspace",
		"Utility",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("root help missing group %q:\n%s", want, out)
		}
	}
	// Representative membership lines (the pad width is deterministic:
	// cobra rpad to the longest command name + 1).
	for _, want := range []string{
		"init        Bootstrap a new EKA repository",
		"transition  Transition a work item, plan, container or artifact state",
		"note        Create a note draft (comment) on an artifact",
		"get         Retrieve knowledge as machine-readable CKO JSON",
		"context     Construct the engineering context around a knowledge subject",
		"sync        Sync a repository with the EKA workspace",
		"integrity   Verify the EKA workspace integrity",
		"completion  Generate the autocompletion script for the specified shell",
		"help        Help about any command",
		"update      Update the EKA CLI to the latest release",
		"version     Print version information",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("root help missing %q:\n%s", want, out)
		}
	}
	// The flat label must be gone: the group headers replace it.
	if i := strings.Index(out, "Authoring"); i < 0 {
		t.Fatalf("root help has no Authoring group:\n%s", out)
	}
}

// TestLandingUsesSameGroups: the landing Commands list is rendered with
// the identical grouping (acceptance criterion 2).
func TestLandingUsesSameGroups(t *testing.T) {
	code, out, _ := runIn(nil)
	if code != 0 {
		t.Fatalf("landing: exit = %d, want 0", code)
	}
	for _, want := range []string{
		"Commands",
		"Authoring",
		"Repository & Exchange",
		"Knowledge Access",
		"Runtime & Workspace",
		"Utility",
		"discard     Discard a draft without publishing",
		"init        Bootstrap a new EKA repository",
		"get         Retrieve knowledge as machine-readable CKO JSON",
		"sync        Sync a repository with the EKA workspace",
		"version     Print version information",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("landing missing %q:\n%s", want, out)
		}
	}
	// The landing derives from the fresh tree: no built-in commands.
	if strings.Contains(out, "completion  Generate") || strings.Contains(out, "Help about any command") {
		t.Errorf("landing must not list the built-in help/completion commands:\n%s", out)
	}
}

// TestRenderCommandGroupsNonTTY: the shared renderer emits plain text
// on a non-color Style — group titles and members, no ANSI.
func TestRenderCommandGroupsNonTTY(t *testing.T) {
	var buf bytes.Buffer
	s := &ui.Style{Color: false, W: &buf}
	renderCommandGroups(s, &buf, commandGroups, newRootCommand().Commands())
	out := buf.String()
	if strings.Contains(out, "\x1b") {
		t.Errorf("non-color render must not contain ANSI:\n%q", out)
	}
	for _, want := range []string{
		"\n\nAuthoring\n",
		"\n\nRepository & Exchange\n",
		"\n\nKnowledge Access\n",
		"\n\nRuntime & Workspace\n",
		"\n\nUtility\n",
		"  init        Bootstrap a new EKA repository\n",
		"  transition  Transition a work item, plan, container or artifact state\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("render missing %q:\n%q", want, out)
		}
	}
}

// TestRenderCommandGroupsColor: with Style{Color:true} the group titles
// render in Accent, the command names in Info and the descriptions
// plain — through the ui.Style paint path, never hand-rolled escapes.
func TestRenderCommandGroupsColor(t *testing.T) {
	var buf bytes.Buffer
	s := &ui.Style{Color: true, W: &buf}
	renderCommandGroups(s, &buf, commandGroups, newRootCommand().Commands())
	out := buf.String()
	if !strings.Contains(out, "\x1b[38;5;75mAuthoring\x1b[0m") {
		t.Errorf("group title must render in Accent (Info hue):\n%q", out)
	}
	if !strings.Contains(out, "\x1b[38;5;75minit       \x1b[0m Bootstrap a new EKA repository") {
		t.Errorf("command name must render in Info, description plain:\n%q", out)
	}
	// Descriptions stay plain: the name's reset is the last escape
	// before the description (never a color code inside or around it).
	for _, desc := range []string{
		"Bootstrap a new EKA repository",
		"Sync a repository with the EKA workspace",
		"Retrieve knowledge as machine-readable CKO JSON",
	} {
		if !strings.Contains(out, "\x1b[0m "+desc) {
			t.Errorf("description %q must follow the name reset plainly:\n%q", desc, out)
		}
		if strings.Contains(out, "\x1b[38;5;75m"+desc) {
			t.Errorf("description %q must not be Info-colored:\n%q", desc, out)
		}
	}
}

// TestSubcommandHelpFlatColored: subcommand help is colored but flat —
// command names Info, flag names Dim, the hint Dim — with no group
// headers (grouping is a root-level concept; lists of a few entries
// carry no headers).
func TestSubcommandHelpFlatColored(t *testing.T) {
	get := findCommand(newRootCommand(), "get")
	if get == nil {
		t.Fatal("get command not registered")
	}
	var buf bytes.Buffer
	s := &ui.Style{Color: true, W: &buf}
	renderHelp(get, s, &buf)
	out := buf.String()
	for _, group := range []string{"Authoring", "Repository & Exchange", "Knowledge Access", "Runtime & Workspace", "Utility"} {
		if strings.Contains(out, group) {
			t.Errorf("subcommand help must not show group headers, found %q:\n%q", group, out)
		}
	}
	if !strings.Contains(out, "Available Commands:") {
		t.Errorf("subcommand help must keep the flat Available Commands list:\n%q", out)
	}
	if !strings.Contains(out, "\x1b[38;5;75mticket     \x1b[0m Retrieve one ticket as machine-readable JSON") {
		t.Errorf("subcommand names must render in Info:\n%q", out)
	}
	// The --help flag is registered by cobra only during execution, so
	// the direct render sees the command's own flags: assert the Dim
	// paint on one of those instead.
	if !strings.Contains(out, "\x1b[38;5;245m      --compact\x1b[0m") {
		t.Errorf("flag names must render in Dim:\n%q", out)
	}
	if !strings.Contains(out, "\x1b[38;5;245mUse \"eka get [command] --help\" for more information about a command.\x1b[0m") {
		t.Errorf("the help hint must render in Dim:\n%q", out)
	}
}

// TestRenderFlagUsages: the flag-name column renders Dim, the
// descriptions plain; a non-color Style passes the text through
// byte-identically.
func TestRenderFlagUsages(t *testing.T) {
	usages := strings.TrimRight(""+
		"  -h, --help      help for eka\n"+
		"  -v, --verbose   verbose output: additional detail lines (per-unit lists, plan actions)\n"+
		"      --version   print the CLI version (the same first line 'eka version' reports) and exit\n", "\n")

	var plain bytes.Buffer
	renderFlagUsages(&ui.Style{Color: false, W: &plain}, &plain, usages)
	if plain.String() != usages {
		t.Errorf("non-color flag usages must pass through byte-identically:\ngot  %q\nwant %q", plain.String(), usages)
	}

	var colored bytes.Buffer
	renderFlagUsages(&ui.Style{Color: true, W: &colored}, &colored, usages)
	out := colored.String()
	for _, want := range []string{
		"\x1b[38;5;245m  -h, --help\x1b[0m      help for eka\n",
		"\x1b[38;5;245m  -v, --verbose\x1b[0m   verbose output: additional detail lines (per-unit lists, plan actions)\n",
		"\x1b[38;5;245m      --version\x1b[0m   print the CLI version (the same first line 'eka version' reports) and exit",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("flag line missing %q:\n%q", want, out)
		}
	}
}

// TestHelpNoANSIOnNonTTY: every help entry point emits no ANSI on a
// non-terminal writer (the determinism contract, extended to help
// paths). The Execute-level scenario list in execute_test.go covers
// more paths; this one pins the explicit help commands.
func TestHelpNoANSIOnNonTTY(t *testing.T) {
	for _, args := range [][]string{
		{"-h"},
		{"--help"},
		{"help"},
		{"get", "--help"},
		{"validate", "--help"},
		{"version", "--help"},
		{"plugin", "--help"},
	} {
		var out, errb bytes.Buffer
		if code := Execute(args, strings.NewReader(""), &out, &errb); code != 0 {
			t.Errorf("args %v: exit = %d, want 0", args, code)
		}
		if strings.Contains(out.String(), "\x1b") || strings.Contains(errb.String(), "\x1b") {
			t.Errorf("args %v: non-TTY help must not contain ANSI:\nstdout: %q\nstderr: %q",
				args, out.String(), errb.String())
		}
	}
}

// findCommand returns the direct child of root with the given name.
func findCommand(root *cobra.Command, name string) *cobra.Command {
	for _, c := range root.Commands() {
		if c.Name() == name {
			return c
		}
	}
	return nil
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
