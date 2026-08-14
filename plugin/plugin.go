// Package plugin defines the EKA CLI plugin contract (v1): a stable,
// versionable, executable-based integration point for extensions such as
// eka-mcp.
//
// The contract keeps the CLI decoupled from any plugin's implementation.
// The CLI depends only on this package — never on eka-mcp or any other
// plugin. A plugin is an executable named "eka-<name>" (e.g. "eka-mcp")
// discoverable on PATH or under the EKA plugin directory; the CLI talks to
// it through two machine-readable subcommands:
//
//	eka-<name> manifest --json
//	eka-<name> install <kind> --dir <dir> [--dry-run] --json
//
// "manifest" reports what the plugin provides (name, version, installable
// artifact families), "install" delegates an artifact-family installation
// into an agent configuration directory. The JSON output is the contract:
// it is deterministic, schema-stable, and versioned by ContractVersion.
//
// Plugins import these types to implement their executable side
// (see eka-mcp); they never import the CLI's internal packages.
package plugin

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// ContractVersion is the machine-readable contract version. Consumers and
// providers negotiate against it; a mismatch is a refusal, never a silent
// misinterpretation.
const ContractVersion = "v1"

// Manifest is the machine-readable self-description a plugin executable
// emits for "manifest --json". It is the single source of truth the CLI
// uses to know what a plugin provides.
type Manifest struct {
	// Contract is the contract version the manifest is written against
	// (must equal ContractVersion).
	Contract string `json:"contract"`
	// Name is the stable plugin identity (e.g. "mcp").
	Name string `json:"name"`
	// Version is the plugin semantic version.
	Version string `json:"version"`
	// Description is a human-readable one-line summary.
	Description string `json:"description"`
	// Artifacts lists the installable artifact families.
	Artifacts []Artifact `json:"artifacts"`
}

// Artifact is one installable family the plugin can install into an agent
// configuration directory.
type Artifact struct {
	// Kind is the family name: "skills", "commands", "tools", …
	Kind string `json:"kind"`
	// Entries are the artifact names within the family (skill directory
	// names, command file names, …).
	Entries []string `json:"entries"`
}

// InstallOptions is the request for a plugin "install <kind>" delegation.
type InstallOptions struct {
	Kind   string
	Dir    string
	DryRun bool
}

// InstallResult is the machine-readable result of an install delegation.
type InstallResult struct {
	// Installed are the artifact names installed (or that would be).
	Installed []string `json:"installed"`
	// Version is the plugin version that served the install.
	Version string `json:"version"`
}

// Plugin is a discovered plugin executable. Its methods invoke the
// executable subcommands and parse their JSON output.
type Plugin struct {
	// Exe is the absolute path of the plugin executable.
	Exe string
}

// pluginDirEnv is the environment variable overriding the plugin
// directory; pluginDirDefault is the fallback under the EKA home.
const (
	pluginDirEnv     = "EKA_PLUGIN_DIR"
	pluginDirDefault = ".eka/plugins"
)

// DefaultPluginPaths returns the ordered list of directories searched for
// plugin executables: $EKA_PLUGIN_DIR, then ~/.eka/plugins. PATH search is
// applied by Discover separately via exec.LookPath.
func DefaultPluginPaths(home string) []string {
	var paths []string
	if d := os.Getenv(pluginDirEnv); d != "" {
		paths = append(paths, d)
	}
	if home != "" {
		paths = append(paths, filepath.Join(home, pluginDirDefault))
	}
	return paths
}

// Discover finds plugin executables: any "eka-*" executable on PATH
// (excluding the CLI itself) plus any "eka-*" executable in the plugin
// directories. Duplicate names collapse to the first discovered path.
// A plugin whose manifest cannot be read is skipped only when a runnable
// candidate of the same name also failed to produce a manifest; otherwise
// the error is returned so a broken plugin is visible, not silent.
func Discover(home string) ([]Plugin, error) {
	seen := map[string]bool{}
	var plugins []Plugin
	add := func(exe string) {
		name := pluginName(exe)
		if name == "" || name == "eka" || seen[name] {
			return
		}
		seen[name] = true
		plugins = append(plugins, Plugin{Exe: exe})
	}
	// PATH search.
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if dir == "" {
			continue
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			if strings.HasPrefix(e.Name(), "eka-") {
				add(filepath.Join(dir, e.Name()))
			}
		}
	}
	// Plugin directories.
	for _, dir := range DefaultPluginPaths(home) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			if strings.HasPrefix(e.Name(), "eka-") {
				add(filepath.Join(dir, e.Name()))
			}
		}
	}
	sort.Slice(plugins, func(i, j int) bool { return pluginName(plugins[i].Exe) < pluginName(plugins[j].Exe) })
	return plugins, nil
}

// pluginName extracts the stable plugin name from an executable path:
// the basename with the leading "eka-" prefix dropped.
func pluginName(exe string) string {
	base := filepath.Base(exe)
	if !strings.HasPrefix(base, "eka-") {
		return ""
	}
	return strings.TrimPrefix(base, "eka-")
}

// Manifest runs "manifest --json" and parses the result.
func (p Plugin) Manifest() (Manifest, error) {
	out, err := p.run("manifest")
	if err != nil {
		return Manifest{}, fmt.Errorf("plugin %q manifest failed: %w", pluginName(p.Exe), err)
	}
	var m Manifest
	if err := json.Unmarshal(out, &m); err != nil {
		return Manifest{}, fmt.Errorf("plugin %q manifest is not valid JSON: %w", pluginName(p.Exe), err)
	}
	if m.Contract != "" && m.Contract != ContractVersion {
		return Manifest{}, fmt.Errorf("plugin %q contract %q is not supported (want %q)", pluginName(p.Exe), m.Contract, ContractVersion)
	}
	return m, nil
}

// Install runs "install <kind> --dir <dir> [--dry-run] --json" and parses
// the result.
func (p Plugin) Install(opts InstallOptions) (InstallResult, error) {
	args := []string{"install", opts.Kind, "--dir", opts.Dir, "--json"}
	if opts.DryRun {
		args = append(args, "--dry-run")
	}
	out, err := p.run(args...)
	if err != nil {
		return InstallResult{}, fmt.Errorf("plugin %q install %s failed: %w", pluginName(p.Exe), opts.Kind, err)
	}
	var r InstallResult
	if err := json.Unmarshal(out, &r); err != nil {
		return InstallResult{}, fmt.Errorf("plugin %q install %s result is not valid JSON: %w", pluginName(p.Exe), opts.Kind, err)
	}
	return r, nil
}

// run executes the plugin executable with the given arguments, returning
// the stdout bytes on success (stderr is surfaced on failure).
func (p Plugin) run(args ...string) ([]byte, error) {
	cmd := exec.Command(p.Exe, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, errors.New(strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}
