package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/maleolabs/eka-cli/cmd/ui"
	"github.com/maleolabs/eka-cli/plugin"
	"github.com/spf13/cobra"
)

// The EKA AI Skill Pack installation commands: `eka install skills` and
// `eka install commands` install the pack's artifact families through a
// plugin executable (the plugin contract, package plugin): the CLI
// discovers plugins, asks each for its manifest, and delegates the
// copy to the plugin that declares the requested artifact kind. The
// pack itself lives in the eka-mcp plugin; the CLI never embeds it and
// never depends on it — manual copy remains a valid installation path
// (ADR-022).
//
// Targets mirror the configuration locations of the mainstream agent
// ecosystems (documented in skills/docs/installation.md):
//
//	opencode  ~/.config/opencode/{skills,commands}
//	claude    ~/.claude/{skills,commands}
//	agents    ~/.agents/skills
//
// The commands are distribution convenience, not runtime surface: they
// never touch the EKA workspace or the canonical store.

// installTarget describes one agent ecosystem's configuration layout.
type installTarget struct {
	name     string // the --target value
	config   string // the config directory that must exist for auto-detection
	skills   string // directory for installed skills ("" = unsupported)
	commands string // directory for installed commands ("" = unsupported)
}

// installTargets lists the supported agent ecosystems, resolved against
// the user's home directory at runtime.
func installTargets(home string) []installTarget {
	return []installTarget{
		{name: "opencode", config: filepath.Join(home, ".config", "opencode"),
			skills:   filepath.Join(home, ".config", "opencode", "skills"),
			commands: filepath.Join(home, ".config", "opencode", "commands")},
		{name: "claude", config: filepath.Join(home, ".claude"),
			skills:   filepath.Join(home, ".claude", "skills"),
			commands: filepath.Join(home, ".claude", "commands")},
		{name: "agents", config: filepath.Join(home, ".agents"),
			skills: filepath.Join(home, ".agents", "skills")},
	}
}

// installState is the marker written into an install target. It is the
// update-detection record: a re-install with a different pack version
// reports "updated", the same version reports "unchanged (refresh)".
// Deterministic: no timestamps, no host-dependent values.
type installState struct {
	Pack    string `json:"pack"`
	Kind    string `json:"kind"`
	Version string `json:"version"`
}

const installStateFile = ".ekapack.json"

// errNoTarget is the refusal sentinel: no agent configuration
// directory was detected for auto-detection. It maps to the exit-1
// refusal class (environment state), unlike usage errors (exit 2).
var errNoTarget = errors.New("no agent configuration directory detected")

// errNoPluginProvider is the refusal sentinel: no discovered plugin
// declares the requested artifact kind. It maps to the exit-1 refusal
// class: the environment has no provider, and installing the eka-mcp
// plugin is the fix.
var errNoPluginProvider = errors.New("no plugin provides")

// newInstallCommand builds the `eka install` family: `eka install
// skills` and `eka install commands` — the official installation path
// of the EKA AI Skill Pack into an agent's configuration directory,
// sourced through a plugin executable (eka-mcp).
func newInstallCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install the EKA AI Skill Pack into an agent configuration",
		Long: `Install the EKA AI Skill Pack into an agent configuration
directory, so agents pick the skills and commands up without manual
copying. The pack is provided by the eka-mcp plugin: the CLI discovers
the plugin, reads its manifest, and delegates the copy to it.

  eka install skills    install the eka-* skills into the agent's
                        skills directory
  eka install commands  install the eka-discuss / eka-execute commands
                        into the agent's commands directory

Targets are the configuration locations of the mainstream agent
ecosystems: opencode (~/.config/opencode), claude (~/.claude) and the
agents standard (~/.agents). Without --target the command detects the
targets whose configuration directory exists. --dir installs into an
explicit directory (project-scoped installs, tests). --dry-run prints
the plan without writing anything.

The plugin version is the pack version: installation never depends on
the pack being present on disk. Re-running install refreshes the
installed files; the report distinguishes "updated" (pack version
changed) from "unchanged (refresh)".

Exit codes:
  0  installed (or dry-run)
  1  refusal — no agent configuration directory detected, or no
     plugin provides the requested artifact kind
  2  usage or internal error`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	cmd.AddCommand(newInstallSkillsCommand(), newInstallCommandsCommand())
	return cmd
}

// installFlags carries the shared flag set of the install subcommands.
type installFlags struct {
	target string
	dir    string
	dryRun bool
}

func (f *installFlags) bind(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.target, "target", "", "agent target: opencode | claude | agents | all")
	cmd.Flags().StringVar(&f.dir, "dir", "", "explicit destination directory (overrides --target and detection)")
	cmd.Flags().BoolVar(&f.dryRun, "dry-run", false, "print the install plan without writing anything")
}

// installKind is the artifact family being installed.
type installKind string

const (
	kindSkills   installKind = "skills"
	kindCommands installKind = "commands"
)

func newInstallSkillsCommand() *cobra.Command {
	f := &installFlags{}
	cmd := &cobra.Command{
		Use:   "skills",
		Short: "Install the EKA skills into an agent configuration",
		Long: `Install the eka-* skills (provided by the eka-mcp plugin) into
the agent's skills directory, each skill as its own folder with
SKILL.md and resources.

Targets: opencode (~/.config/opencode/skills), claude
(~/.claude/skills), agents (~/.agents/skills). Without --target the
command detects the targets whose configuration directory exists.`,
		Example: `  eka install skills
  eka install skills --target opencode
  eka install skills --dir .opencode/skills --dry-run`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runInstall(cmd, f, kindSkills)
		},
	}
	f.bind(cmd)
	return cmd
}

func newInstallCommandsCommand() *cobra.Command {
	f := &installFlags{}
	cmd := &cobra.Command{
		Use:   "commands",
		Short: "Install the EKA agent commands into an agent configuration",
		Long: `Install the eka-discuss and eka-execute agent commands (provided
by the eka-mcp plugin) into the agent's commands directory.

Targets: opencode (~/.config/opencode/commands), claude
(~/.claude/commands). The agents standard has no commands directory
and is refused. Without --target the command detects the targets
whose configuration directory exists.`,
		Example: `  eka install commands
  eka install commands --target opencode
  eka install commands --dir .opencode/commands --dry-run`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runInstall(cmd, f, kindCommands)
		},
	}
	f.bind(cmd)
	return cmd
}

// resolveInstallDirs maps the flag/target input to concrete destination
// directories for one install kind. A refusal (no target detected) is
// returned as a deterministic error with the exit-1 hint.
func resolveInstallDirs(home string, kind installKind, f *installFlags) ([]string, error) {
	if f.dir != "" {
		if f.target != "" {
			return nil, fmt.Errorf("--target and --dir are mutually exclusive")
		}
		return []string{f.dir}, nil
	}
	targets := installTargets(home)
	if f.target != "" {
		if f.target == "all" {
			var dirs []string
			for _, t := range targets {
				if d := targetDir(t, kind); d != "" {
					dirs = append(dirs, d)
				}
			}
			if len(dirs) == 0 {
				return nil, fmt.Errorf("no target supports %s", kind)
			}
			return dirs, nil
		}
		for _, t := range targets {
			if t.name != f.target {
				continue
			}
			d := targetDir(t, kind)
			if d == "" {
				return nil, fmt.Errorf("target %q does not support %s", f.target, kind)
			}
			return []string{d}, nil
		}
		return nil, fmt.Errorf("unknown target %q (supported: opencode, claude, agents, all)", f.target)
	}
	// Auto-detection: targets whose configuration directory exists.
	var dirs []string
	for _, t := range targets {
		if _, err := os.Stat(t.config); err != nil {
			continue
		}
		if d := targetDir(t, kind); d != "" {
			dirs = append(dirs, d)
		}
	}
	if len(dirs) == 0 {
		return nil, fmt.Errorf("%w (%s); pass --target or --dir explicitly", errNoTarget, kind)
	}
	return dirs, nil
}

// targetDir returns the destination directory for one target and kind.
func targetDir(t installTarget, kind installKind) string {
	switch kind {
	case kindSkills:
		return t.skills
	case kindCommands:
		return t.commands
	}
	return ""
}

// resolvePlugin discovers the plugin executables and returns the first
// one whose manifest declares the requested artifact kind, together
// with its manifest. A plugin whose manifest cannot be read is an
// error (a broken plugin is visible, not silent); when every plugin
// parses but none declares the kind, the errNoPluginProvider refusal
// is returned.
func resolvePlugin(home, kind string) (plugin.Plugin, plugin.Manifest, error) {
	plugins, err := plugin.Discover(home)
	if err != nil {
		return plugin.Plugin{}, plugin.Manifest{}, err
	}
	for _, p := range plugins {
		m, err := p.Manifest()
		if err != nil {
			return plugin.Plugin{}, plugin.Manifest{}, err
		}
		for _, a := range m.Artifacts {
			if a.Kind == kind {
				return p, m, nil
			}
		}
	}
	return plugin.Plugin{}, plugin.Manifest{}, fmt.Errorf("%w %s; install the eka-mcp plugin", errNoPluginProvider, kind)
}

// manifestEntries returns the artifact names the manifest declares for
// one kind ("" when the kind is absent — unreachable after
// resolvePlugin succeeded).
func manifestEntries(m plugin.Manifest, kind string) []string {
	for _, a := range m.Artifacts {
		if a.Kind == kind {
			return a.Entries
		}
	}
	return nil
}

// writeInstallState records the install state marker (update-detection
// record) after a successful install delegation.
func writeInstallState(dir string, kind installKind, version string) error {
	state := installState{Pack: "eka-ai-skill-pack", Kind: string(kind), Version: version}
	b, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, installStateFile), append(b, '\n'), 0o644)
}

// installOutcome describes one destination's result.
type installOutcome struct {
	dir       string
	artifacts int
	status    string // "installed" | "updated" | "unchanged (refresh)"
}

// runInstall executes one install subcommand: resolve the destination,
// resolve the providing plugin, plan (or execute) through the plugin,
// render the deterministic report.
func runInstall(cmd *cobra.Command, f *installFlags, kind installKind) error {
	s := styleFor(cmd)
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("cannot resolve the home directory: %w", err)
	}
	dirs, err := resolveInstallDirs(home, kind, f)
	if err != nil {
		if errors.Is(err, errNoTarget) {
			return refuse(cmd, "install %s refused: %s", kind, err)
		}
		return err
	}
	p, m, err := resolvePlugin(home, string(kind))
	if err != nil {
		if errors.Is(err, errNoPluginProvider) {
			return refuse(cmd, "install %s refused: %s", kind, err)
		}
		return err
	}
	version := m.Version
	entries := manifestEntries(m, string(kind))

	// Context header (the single-operation interaction model).
	fmt.Fprintln(s.W)
	fmt.Fprintln(s.W, s.Accent("Install"))
	fmt.Fprintf(s.W, "  %s %s\n", s.Info("Pack"), "eka-ai-skill-pack "+version)
	if len(dirs) == 1 {
		fmt.Fprintf(s.W, "  %s %s\n", s.Info("Target"), dirs[0])
	}
	fmt.Fprintf(s.W, "  %s %s\n", s.Accent("↓ Install"), string(kind))

	sm := ui.NewSummary(s)
	if f.dryRun {
		// The plugin's install --dry-run performs no writes; the plan
		// is its authoritative answer, and the manifest's declared
		// entries are the deterministic planned count.
		for _, dir := range dirs {
			res, err := p.Install(plugin.InstallOptions{Kind: string(kind), Dir: dir, DryRun: true})
			if err != nil {
				return fmt.Errorf("install %s failed: %w", kind, err)
			}
			fmt.Fprintf(s.W, "  %s %s\n", s.Info("install to"), dir)
			for _, n := range res.Installed {
				fmt.Fprintf(s.W, "    %s %s\n", s.Dim("•"), n)
			}
			fmt.Fprintf(s.W, "    %s %s\n", s.Dim("•"), installStateFile)
		}
		fmt.Fprintln(s.W)
		fmt.Fprintln(s.W, s.Warning("Dry-run: no changes were written."))
		sm.Add("Pack", "eka-ai-skill-pack "+version)
		sm.Add(string(kind), fmt.Sprintf("%d artifact(s) planned for %d target(s)", len(entries), len(dirs)))
		sm.Render()
		return nil
	}

	var outcomes []installOutcome
	for _, dir := range dirs {
		status := "installed"
		if b, err := os.ReadFile(filepath.Join(dir, installStateFile)); err == nil {
			var prev installState
			if json.Unmarshal(b, &prev) == nil {
				if prev.Version == version {
					status = "unchanged (refresh)"
				} else {
					status = "updated"
				}
			}
		}
		res, err := p.Install(plugin.InstallOptions{Kind: string(kind), Dir: dir})
		if err != nil {
			return fmt.Errorf("install %s failed: %w", kind, err)
		}
		if err := writeInstallState(dir, kind, version); err != nil {
			return fmt.Errorf("install %s failed: %w", kind, err)
		}
		outcomes = append(outcomes, installOutcome{dir: dir, artifacts: len(res.Installed), status: status})
	}
	for _, o := range outcomes {
		sm.Add(string(kind), fmt.Sprintf("%d → %s (%s)", o.artifacts, o.dir, o.status))
	}
	sm.Add("Pack", "eka-ai-skill-pack "+version)
	sm.Render()
	return nil
}
