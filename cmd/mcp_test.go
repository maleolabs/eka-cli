package cmd

import (
	"bytes"
	"strings"
	"testing"
)

// TestMcpHelpDisclosureStatic verifies that `eka mcp -h` (and the two
// sibling forms) disclose the plugin's actual subcommands even when the
// plugin is not installed. The native stub provides the static fallback
// (bug:mcp-help-subcommands-hidden). Rich descriptions are deferred (B3),
// so the test checks names only.
func TestMcpHelpDisclosureStatic(t *testing.T) {
	// Isolate from any installed plugin by using an empty plugin dir and PATH.
	emptyPluginDir := t.TempDir()
	t.Setenv("EKA_PLUGIN_DIR", emptyPluginDir)
	t.Setenv("PATH", t.TempDir())
	// Clear memoised registration state so the empty dir is re-probed.
	clearPluginRegCacheForTest()

	for _, args := range [][]string{
		{"mcp", "-h"},
		{"mcp", "--help"},
		{"help", "mcp"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := Execute(args, nil, &stdout, &stderr)
			if code != 0 {
				t.Fatalf("Execute(%v) exit %d, want 0; stdout=%q stderr=%q", args, code, stdout.String(), stderr.String())
			}
			out := stdout.String() + stderr.String()
			for _, want := range []string{"manifest", "install", "configure", "serve"} {
				if !strings.Contains(out, want) {
					t.Errorf("help %v must disclose %q, got:\n%s", args, want, out)
				}
			}
		})
	}
}

// TestMcpHelpDisclosureWithPlugin verifies that when the plugin is
// installed, `eka mcp -h` still discloses the subcommands via the
// proxy help (disclosure suffix) and that proxy execution remains intact.
func TestMcpHelpDisclosureWithPlugin(t *testing.T) {
	dir := t.TempDir()
	// Install a fake mcp plugin whose manifest declares the disclosure set.
	manifestJSON := registrationManifest("mcp",
		pluginCommandSpec{Name: "mcp", Description: "EKA MCP server and plugin tooling", Args: []string{}},
		pluginCommandSpec{Name: "manifest", Description: "Show plugin manifest", Args: []string{"manifest", "--json"}},
		pluginCommandSpec{Name: "install", Description: "Install artifact family", Args: []string{"install"}},
		pluginCommandSpec{Name: "configure", Description: "Configure MCP client", Args: []string{"configure"}},
		pluginCommandSpec{Name: "serve", Description: "Run the MCP server", Args: []string{"serve"}},
	)
	body := registrationPluginScript(manifestJSON, "", 0)
	installRegistrationPlugin(t, dir, "eka-mcp", body)
	t.Setenv("EKA_PLUGIN_DIR", dir)
	t.Setenv("PATH", t.TempDir())
	clearPluginRegCacheForTest()

	for _, args := range [][]string{
		{"mcp", "-h"},
		{"mcp", "--help"},
		{"help", "mcp"},
	} {
		t.Run("installed:"+strings.Join(args, " "), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := Execute(args, nil, &stdout, &stderr)
			if code != 0 {
				t.Fatalf("Execute(%v) exit %d, want 0; stdout=%q stderr=%q", args, code, stdout.String(), stderr.String())
			}
			out := stdout.String() + stderr.String()
			for _, want := range []string{"manifest", "install", "configure", "serve"} {
				if !strings.Contains(out, want) {
					t.Errorf("installed help %v must disclose %q, got:\n%s", args, want, out)
				}
			}
		})
	}

	// Proxy execution must still work: `eka mcp manifest --json` dispatches.
	t.Run("proxy execution", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := Execute([]string{"mcp", "manifest", "--json"}, nil, &stdout, &stderr)
		// The fake plugin's manifest handler prints the manifest JSON.
		if code != 0 {
			t.Fatalf("proxy Execute failed exit %d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
		}
		if !strings.Contains(stdout.String(), `"name":"mcp"`) {
			t.Errorf("proxy output must contain manifest JSON, got %q", stdout.String())
		}
	})
}

// clearPluginRegCacheForTest resets the memoised plugin registration
// state so a test HOME change is re-probed. It is test-only.
func clearPluginRegCacheForTest() {
	pluginRegMu.Lock()
	pluginRegCache = map[string]*pluginRegEntry{}
	pluginRegWarned = map[string]bool{}
	pluginRegWarnings = nil
	pluginRegMu.Unlock()
	pluginPathScanMu.Lock()
	pluginPathScanCache = map[string][]pathOnlyFinding{}
	pluginPathScanMu.Unlock()
}
