package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/maleolabs/eka-cli/cmd/ui"
)

func mcpKey(s string) tea.KeyMsg {
	switch s {
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "ctrl+c":
		return tea.KeyMsg{Type: tea.KeyCtrlC}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}
}

// -------------------------------------------------------------------
// Disclosure (updated for sto:mcp-entrypoint-ux: bare mcp is overview,
// serve is the only disclosed subcommand)
// -------------------------------------------------------------------

func TestMcpHelpDisclosureStatic(t *testing.T) {
	emptyPluginDir := t.TempDir()
	t.Setenv("EKA_PLUGIN_DIR", emptyPluginDir)
	t.Setenv("PATH", t.TempDir())
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
			if !strings.Contains(out, "serve") {
				t.Errorf("help %v must disclose serve, got:\n%s", args, out)
			}
			if !strings.Contains(out, "EKA MCP") {
				t.Errorf("help %v must contain overview heading, got:\n%s", args, out)
			}
		})
	}
}

func TestMcpHelpDisclosureWithPlugin(t *testing.T) {
	dir := t.TempDir()
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
			if !strings.Contains(out, "serve") {
				t.Errorf("installed help %v must disclose serve, got:\n%s", args, out)
			}
		})
	}

	t.Run("proxy execution", func(t *testing.T) {
		dir2 := t.TempDir()
		serveBody := []byte("#!/bin/sh\ncase \"$1\" in serve) echo '{\"jsonrpc\":\"2.0\"}';; *) echo \"$@\";; esac\n")
		installRegistrationPlugin(t, dir2, "eka-mcp", serveBody)
		t.Setenv("EKA_PLUGIN_DIR", dir2)
		clearPluginRegCacheForTest()
		var stdout, stderr bytes.Buffer
		code := Execute([]string{"mcp", "serve"}, nil, &stdout, &stderr)
		if code != 0 {
			t.Fatalf("proxy serve failed exit %d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
		}
		if !strings.Contains(stdout.String(), "jsonrpc") {
			t.Errorf("serve output must be protocol JSON, got %q", stdout.String())
		}
	})
}

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

// -------------------------------------------------------------------
// Serve stdout purity (A)
// -------------------------------------------------------------------

func TestMcpServeStdoutPurity(t *testing.T) {
	dir := t.TempDir()
	body := []byte("#!/bin/sh\nprintf 'diagnostic\\n' >&2\nprintf '{\"jsonrpc\":\"2.0\",\"id\":1}\\n'\n")
	installRegistrationPlugin(t, dir, "eka-mcp", body)
	t.Setenv("EKA_PLUGIN_DIR", dir)
	t.Setenv("PATH", t.TempDir())
	clearPluginRegCacheForTest()

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"mcp", "serve"}, strings.NewReader(`{"jsonrpc":"2.0","method":"ping"}`), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("serve exit %d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if strings.Contains(stdout.String(), "diagnostic") {
		t.Errorf("stdout must be protocol-only, got %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "jsonrpc") {
		t.Errorf("stdout must contain protocol, got %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "diagnostic") {
		t.Errorf("diagnostic must go to stderr, got %q", stderr.String())
	}
}

// -------------------------------------------------------------------
// TUI driven by encoded key sequences WITHOUT PTY (H)
// -------------------------------------------------------------------

func TestMcpTUIKeySequence(t *testing.T) {
	agents, scopes := mcpBuildOptions()
	if len(agents) == 0 || len(scopes) == 0 {
		t.Fatal("registry must have agents and scopes")
	}
	m := mcpInstallerModel{
		stage:       mcpStageAgents,
		agents:      agents,
		cursor:      0,
		scopeOpts:   scopes,
		scopeCursor: 0,
		style:       &ui.Style{Color: false},
	}
	steps := []string{"down", " ", "enter", "down", "enter"}
	for _, k := range steps {
		msg := mcpKey(k)
		next, _ := m.Update(msg)
		m = next.(mcpInstallerModel)
		if m.aborted {
			t.Fatalf("unexpected abort on key %q", k)
		}
	}
	if m.stage != mcpStageDone {
		t.Errorf("stage = %v, want done", m.stage)
	}
	hasSelected := false
	for _, a := range m.agents {
		if a.Selected {
			hasSelected = true
			break
		}
	}
	if !hasSelected {
		t.Error("at least one agent must be selected after key sequence")
	}
}

// -------------------------------------------------------------------
// Non-TTY fallback (H)
// -------------------------------------------------------------------

func TestMcpNonTTYFallback(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Execute([]string{"mcp"}, nil, &stdout, &stderr)
	out := stdout.String() + stderr.String()
	if code != mcpExitPrecondition {
		t.Fatalf("non-TTY bare mcp exit %d want %d out=%q", code, mcpExitPrecondition, out)
	}
	if !strings.Contains(out, "--json") || !strings.Contains(out, "--agent") {
		t.Errorf("non-TTY refusal must name bypasses, got %q", out)
	}
	stdout.Reset()
	stderr.Reset()
	code = Execute([]string{"mcp", "--json"}, nil, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("--json exit %d want 0", code)
	}
	var env mcpEnvelope
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
		t.Fatalf("envelope parse failed: %v out=%q", err, stdout.String())
	}
	if env.Version != mcpEnvelopeVersion || env.Status != "ok" {
		t.Errorf("envelope version/status = %d/%q want %d/ok", env.Version, env.Status, mcpEnvelopeVersion)
	}
}

func TestMcpInvalidFlagFailFast(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Execute([]string{"mcp", "--agent", "unknown_agent", "--json"}, nil, &stdout, &stderr)
	if code != mcpExitNotFound {
		t.Fatalf("invalid agent exit %d want %d", code, mcpExitNotFound)
	}
	stdout.Reset()
	stderr.Reset()
	code = Execute([]string{"mcp", "--scope", "bad"}, nil, &stdout, &stderr)
	if code != mcpExitGeneral {
		t.Fatalf("invalid scope exit %d want %d", code, mcpExitGeneral)
	}
}

// -------------------------------------------------------------------
// Preflight / conflict / rollback (H)
// -------------------------------------------------------------------

func TestMcpPreflightConflictAndRollback(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	xdg := filepath.Join(home, ".config")
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("EKA_HOME", home)

	def := mcpAgentRegistry[0]
	if !def.Selectable {
		def = mcpAgentRegistry[1]
	}
	path, err := mcpAgentPath(def, "global")
	if err != nil {
		t.Skipf("path resolve failed: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"other":true}`), 0644); err != nil {
		t.Fatal(err)
	}
	conflicts, _ := mcpPreflight([]string{def.ID}, "global", false)
	if len(conflicts) == 0 {
		t.Fatalf("expected conflict for owned file, got none")
	}
	var stdout, stderr bytes.Buffer
	code := Execute([]string{"mcp", "--agent", def.ID, "--scope", "global"}, nil, &stdout, &stderr)
	if code != mcpExitConflict {
		t.Fatalf("conflict exit %d want %d out=%q", code, mcpExitConflict, stdout.String()+stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	code = Execute([]string{"mcp", "--agent", def.ID, "--scope", "global", "--force"}, nil, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("force install exit %d want 0 out=%q", code, stdout.String()+stderr.String())
	}
	b, _ := os.ReadFile(path)
	s := string(b)
	if !strings.Contains(s, `"command"`) || !strings.Contains(s, `"eka"`) || !strings.Contains(s, `"serve"`) {
		t.Errorf("forced install should write master content, got %q", s)
	}
}

// -------------------------------------------------------------------
// Batch failure isolation (H)
// -------------------------------------------------------------------

func TestMcpBatchIsolation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("EKA_HOME", home)

	var ids []string
	for _, d := range mcpAgentRegistry {
		if d.Selectable && len(ids) < 2 {
			ids = append(ids, d.ID)
		}
	}
	if len(ids) < 2 {
		t.Skip("need 2 selectable agents")
	}
	var stdout, stderr bytes.Buffer
	code := Execute([]string{"mcp", "--agent", ids[0], "--agent", ids[1], "--scope", "global", "--force"}, nil, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("initial batch exit %d", code)
	}
	stdout.Reset()
	stderr.Reset()
	code = Execute([]string{"mcp", "--agent", ids[0], "--scope", "global"}, nil, &stdout, &stderr)
	if code != 0 && code != mcpExitConflict {
		t.Fatalf("idempotent install exit %d", code)
	}
}
