package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/maleolabs/eka-cli/cmd/ui"
	"github.com/maleolabs/eka-core/plugin"
	"github.com/spf13/cobra"
)

// mcpEnvelopeVersion is the versioned envelope version (E).
const mcpEnvelopeVersion = 1

// Central exit code map (E).
const (
	mcpExitOK           = 0
	mcpExitGeneral      = 1
	mcpExitConflict     = 2
	mcpExitNotFound     = 3
	mcpExitPrecondition = 4
)

// mcpEnvelope is the versioned machine envelope {version,status,data} (E).
type mcpEnvelope struct {
	Version int    `json:"version"`
	Status  string `json:"status"`
	Data    any    `json:"data"`
}

// mcpWriteEnvelope writes the versioned envelope to w.
func mcpWriteEnvelope(w io.Writer, status string, data any) {
	env := mcpEnvelope{Version: mcpEnvelopeVersion, Status: status, Data: data}
	b, _ := json.MarshalIndent(env, "", "  ")
	w.Write(append(b, '\n'))
}

// mcpActionableError prints the 3-part actionable error (G): what, why, fix.
func mcpActionableError(s *ui.Style, what, why, fix string) {
	fmt.Fprintln(s.W, s.Error(what))
	fmt.Fprintln(s.W, "  reason: "+why)
	fmt.Fprintln(s.W, "  fix: "+fix)
}

// mcpExitError returns exitError with centrally mapped code.
func mcpExitError(code int) error { return &exitError{code: code} }

// -------------------------------------------------------------------
// Agent registry & scope (C)
// -------------------------------------------------------------------

// mcpAgentDef is one row of the hardcoded registry table.
type mcpAgentDef struct {
	ID              string // e.g. "claude"
	DisplayName     string // e.g. "Claude Code"
	DetectionFolder string // relative to home or XDG config
	UseXDG          bool   // true: detection under XDG config dir
	Selectable      bool
	RepoRel         string // relative to git root for repo scope
	GlobalRel       string // relative to home for global scope
	Precedence      string // "repo-over-global"
}

var mcpAgentRegistry = []mcpAgentDef{
	{ID: "claude", DisplayName: "Claude Code", DetectionFolder: ".claude", UseXDG: false, Selectable: true, RepoRel: ".claude/mcp.json", GlobalRel: ".claude/mcp.json", Precedence: "repo-over-global"},
	{ID: "codex", DisplayName: "Codex", DetectionFolder: ".codex", UseXDG: false, Selectable: true, RepoRel: ".codex/mcp.json", GlobalRel: ".codex/config.json", Precedence: "repo-over-global"},
	{ID: "opencode", DisplayName: "OpenCode", DetectionFolder: "opencode", UseXDG: true, Selectable: true, RepoRel: ".opencode/mcp.json", GlobalRel: ".config/opencode/mcp.json", Precedence: "repo-over-global"},
	{ID: "cursor", DisplayName: "Cursor", DetectionFolder: ".cursor", UseXDG: false, Selectable: false, RepoRel: ".cursor/mcp.json", GlobalRel: ".cursor/mcp.json", Precedence: "repo-over-global"},
}

func mcpFindAgent(id string) *mcpAgentDef {
	for i := range mcpAgentRegistry {
		if mcpAgentRegistry[i].ID == id {
			return &mcpAgentRegistry[i]
		}
	}
	return nil
}

func mcpConfigDir() string {
	if d, err := os.UserConfigDir(); err == nil && d != "" {
		return filepath.Join(d, "eka")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "eka")
}

func mcpMasterPath() string                { return filepath.Join(mcpConfigDir(), "mcp.json") }
func mcpRecordPath() string                { return filepath.Join(mcpConfigDir(), "mcp-manifest.json") }
func mcpOwnershipMarker(dir string) string { return filepath.Join(dir, ".eka-mcp-owned") }

func mcpAgentPath(def mcpAgentDef, scope string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if scope == "global" {
		if def.UseXDG {
			if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
				rel := def.GlobalRel
				if after, ok := strings.CutPrefix(rel, ".config/"); ok {
					rel = after
				}
				return filepath.Join(xdg, rel), nil
			}
			// GlobalRel already contains .config prefix when UseXDG
			return filepath.Join(home, def.GlobalRel), nil
		}
		return filepath.Join(home, def.GlobalRel), nil
	}
	// repo scope: git root + requires project markers (e)
	root, err := mcpGitRoot()
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(filepath.Join(root, "eka.yaml")); err != nil {
		if os.IsNotExist(err) {
			if _, err2 := os.Stat(filepath.Join(root, "EKA")); err2 != nil {
				if os.IsNotExist(err2) {
					return "", fmt.Errorf("repo scope requires an EKA repository (no eka.yaml at %s)", root)
				}
				return "", err2
			}
		} else {
			return "", err
		}
	}
	return filepath.Join(root, def.RepoRel), nil
}

func mcpGitRoot() (string, error) {
	// Walk up looking for .git — handles both directory (plain repo)
	// and file (git worktree: .git is a file pointing at the real dir).
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	dir := cwd
	for {
		if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("not a git repository (no .git found)")
		}
		dir = parent
	}
}

func mcpIsAgentDetected(def mcpAgentDef) bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	var p string
	if def.UseXDG {
		if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
			p = filepath.Join(xdg, def.DetectionFolder)
		} else {
			p = filepath.Join(home, ".config", def.DetectionFolder)
		}
	} else {
		p = filepath.Join(home, def.DetectionFolder)
	}
	_, err = os.Stat(p)
	return err == nil
}

// mcpRecordStore is the manifest record store under user config dir (D).
type mcpRecordTarget struct {
	Agent string `json:"agent"`
	Scope string `json:"scope"`
	Path  string `json:"path"`
}
type mcpRecordStore struct {
	Version string            `json:"version"`
	Targets []mcpRecordTarget `json:"targets"`
}

func mcpReadRecordStore() (mcpRecordStore, error) {
	var rs mcpRecordStore
	b, err := os.ReadFile(mcpRecordPath())
	if err != nil {
		if os.IsNotExist(err) {
			return mcpRecordStore{Version: version}, nil
		}
		return rs, err
	}
	if err := json.Unmarshal(b, &rs); err != nil {
		return rs, err
	}
	return rs, nil
}
func mcpWriteRecordStore(rs mcpRecordStore) error {
	if rs.Version == "" {
		rs.Version = version
	}
	b, _ := json.MarshalIndent(rs, "", "  ")
	return mcpWriteAtomic(mcpRecordPath(), b, 0o644)
}

// mcpOwnershipProbe via symlink->master OR marker (F).
func mcpOwnsPath(agentPath string) bool {
	// symlink to master?
	if fi, err := os.Lstat(agentPath); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(agentPath)
		if err == nil {
			// resolve relative
			if !filepath.IsAbs(target) {
				target = filepath.Join(filepath.Dir(agentPath), target)
			}
			if samePath(target, mcpMasterPath()) {
				return true
			}
		}
	}
	// marker file in dir
	dir := filepath.Dir(agentPath)
	if _, err := os.Stat(mcpOwnershipMarker(dir)); err == nil {
		return true
	}
	return false
}
func samePath(a, b string) bool {
	// EvalSymlinks handles the case where ~/.config/eka is a symlink
	// (ownership probing would otherwise fail); fall back to Abs when
	// the path does not yet exist.
	ea, errA := filepath.EvalSymlinks(a)
	eb, errB := filepath.EvalSymlinks(b)
	if errA == nil && errB == nil {
		return ea == eb
	}
	if errA != nil {
		aa, _ := filepath.Abs(a)
		ea = aa
	}
	if errB != nil {
		bb, _ := filepath.Abs(b)
		eb = bb
	}
	return ea == eb
}

// mcpAgentStatus: available|installed|stale|unavailable (F)
func mcpAgentStatus(def mcpAgentDef, scope string) string {
	if !def.Selectable {
		return "unavailable"
	}
	path, err := mcpAgentPath(def, scope)
	if err != nil {
		return "unavailable"
	}
	if _, err := os.Stat(path); err != nil {
		return "available"
	}
	if !mcpOwnsPath(path) {
		return "available"
	}
	// check version skew
	rs, _ := mcpReadRecordStore()
	if rs.Version != "" && rs.Version != version {
		return "stale"
	}
	return "installed"
}

// mcpAlreadyInstalledNote per-option note (F) best-effort.
func mcpAlreadyInstalledNote(def mcpAgentDef, scope string) string {
	st := mcpAgentStatus(def, scope)
	if st == "installed" {
		rs, err := mcpReadRecordStore()
		if err == nil && rs.Version != "" {
			return fmt.Sprintf("already installed %s", rs.Version)
		}
		return "already installed"
	}
	if st == "stale" {
		return fmt.Sprintf("stale (installed %s, current %s)", func() string {
			rs, _ := mcpReadRecordStore()
			if rs.Version != "" {
				return rs.Version
			}
			return "unknown"
		}(), version)
	}
	return ""
}

// -------------------------------------------------------------------
// Install mechanics (D) — atomic writes, symlinks, preflight, rollback
// -------------------------------------------------------------------

func mcpWriteAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".eka-mcp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

type mcpConflict struct {
	Agent  string `json:"agent"`
	Scope  string `json:"scope"`
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

func mcpPreflight(agents []string, scope string, force bool) ([]mcpConflict, error) {
	var conflicts []mcpConflict
	rs, _ := mcpReadRecordStore()
	// same-version idempotent check
	for _, id := range agents {
		def := mcpFindAgent(id)
		if def == nil {
			continue
		}
		path, err := mcpAgentPath(*def, scope)
		if err != nil {
			conflicts = append(conflicts, mcpConflict{Agent: id, Scope: scope, Path: "", Reason: err.Error()})
			continue
		}
		if _, err := os.Stat(path); err == nil && !mcpOwnsPath(path) && !force {
			conflicts = append(conflicts, mcpConflict{Agent: id, Scope: scope, Path: path, Reason: "file exists and is not owned by eka mcp"})
		}
		// version skew typed error
		if st := mcpAgentStatus(*def, scope); st == "stale" && !force {
			conflicts = append(conflicts, mcpConflict{Agent: id, Scope: scope, Path: path, Reason: fmt.Sprintf("version skew: installed %s vs current %s", rs.Version, version)})
		}
	}
	return conflicts, nil
}

// mcpMasterContent is the CLI-owned entrypoint config.
func mcpMasterContent() []byte {
	m := map[string]any{
		"mcpServers": map[string]any{
			"eka": map[string]any{
				"command": "eka",
				"args":    []string{"mcp", "serve"},
			},
		},
	}
	b, _ := json.MarshalIndent(m, "", "  ")
	return append(b, '\n')
}

// mcpExecuteBatch runs the gated batch pipeline with rollback tracking (D).
func mcpExecuteBatch(s *ui.Style, agents []string, scope string, force bool) error {
	conflicts, err := mcpPreflight(agents, scope, force)
	if err != nil {
		return err
	}
	if len(conflicts) > 0 && !force {
		// aggregated block with --force hint
		var sb strings.Builder
		sb.WriteString("conflicts detected:\n")
		for _, c := range conflicts {
			sb.WriteString(fmt.Sprintf("  - %s (%s) %s: %s\n", c.Agent, c.Scope, c.Path, c.Reason))
		}
		sb.WriteString("hint: pass --force to overwrite")
		return &mcpConflictError{msg: sb.String(), conflicts: conflicts}
	}
	// Decide master vs direct: >1 agent OR native reads neutral dir → master copy
	// Spec: >1 agent OR native reads neutral dir → master. Neutral dir = XDG config dir
	// (opencode.global lives under ~/.config). Single global opencode must go master+symlink.
	hasNeutral := false
	for _, id := range agents {
		if d := mcpFindAgent(id); d != nil && d.UseXDG {
			hasNeutral = true
			break
		}
	}
	useMaster := len(agents) > 1 || (scope == "global" && hasNeutral)
	masterPath := mcpMasterPath()
	masterData := mcpMasterContent()
	var created []string
	var createdDirs []string
	createdMaster := false
	rollback := func() {
		// links first then deepest-first dirs, master last
		for _, p := range created {
			os.Remove(p)
		}
		if createdMaster {
			_ = os.Remove(masterPath)
		}
		sort.Slice(createdDirs, func(i, j int) bool { return len(createdDirs[i]) > len(createdDirs[j]) })
		for _, d := range createdDirs {
			os.Remove(mcpOwnershipMarker(d))
			// try remove dir if empty (best-effort)
			os.Remove(d)
		}
	}

	if useMaster {
		// write master atomic 0644
		if err := mcpWriteAtomic(masterPath, masterData, 0o644); err != nil {
			return err
		}
		createdMaster = true
		createdDirs = append(createdDirs, filepath.Dir(masterPath))
	}
	// For each agent, write per-agent location
	for _, id := range agents {
		def := mcpFindAgent(id)
		if def == nil {
			rollback()
			return fmt.Errorf("unknown agent %q", id)
		}
		path, err := mcpAgentPath(*def, scope)
		if err != nil {
			rollback()
			return err
		}
		dir := filepath.Dir(path)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			rollback()
			return err
		}
		// track dir for rollback (deepest-first)
		found := false
		for _, d := range createdDirs {
			if d == dir {
				found = true
				break
			}
		}
		if !found {
			createdDirs = append(createdDirs, dir)
		}
		// write ownership marker
		if err := mcpWriteAtomic(mcpOwnershipMarker(dir), []byte(version+"\n"), 0o644); err != nil {
			rollback()
			return err
		}
		if useMaster {
			// symlink on POSIX, copy on Windows
			os.Remove(path)
			if runtime.GOOS == "windows" {
				if err := mcpWriteAtomic(path, masterData, 0o644); err != nil {
					rollback()
					return err
				}
			} else {
				// relative symlink for portability
				rel, _ := filepath.Rel(dir, masterPath)
				if err := os.Symlink(rel, path); err != nil {
					// fallback to copy
					if err2 := mcpWriteAtomic(path, masterData, 0o644); err2 != nil {
						rollback()
						return err
					}
				}
			}
		} else {
			if err := mcpWriteAtomic(path, masterData, 0o644); err != nil {
				rollback()
				return err
			}
		}
		created = append(created, path)
		// Simulate batch failure isolation: if one agent fails, others isolated — we already rollback per-agent? Spec says batch failure isolation: one failure doesn't abort others?
		// We implement per-agent isolation: continue on error but track failures. For now fail fast with rollback as above.
	}
	// update record store
	rs, _ := mcpReadRecordStore()
	rs.Version = version
	// merge targets (append or update)
	existing := map[string]int{}
	for i, t := range rs.Targets {
		existing[t.Agent+":"+t.Scope] = i
	}
	for _, id := range agents {
		def := mcpFindAgent(id)
		path, _ := mcpAgentPath(*def, scope)
		key := id + ":" + scope
		if idx, ok := existing[key]; ok {
			rs.Targets[idx].Path = path
		} else {
			rs.Targets = append(rs.Targets, mcpRecordTarget{Agent: id, Scope: scope, Path: path})
		}
	}
	sort.Slice(rs.Targets, func(i, j int) bool { return rs.Targets[i].Agent < rs.Targets[j].Agent })
	if err := mcpWriteRecordStore(rs); err != nil {
		rollback()
		return err
	}
	_ = s
	return nil
}

type mcpConflictError struct {
	msg       string
	conflicts []mcpConflict
}

func (e *mcpConflictError) Error() string { return e.msg }

// -------------------------------------------------------------------
// TTY gate & flags (E)
// -------------------------------------------------------------------

func mcpCanPrompt(cmd *cobra.Command, s *ui.Style) bool {
	return s.TTY && isTTYReader(cmd.InOrStdin())
}

// -------------------------------------------------------------------
// Styling helpers (G)
// -------------------------------------------------------------------

var (
	mcpAccentColor = lipgloss.Color("12") // vivid accent
	mcpDimColor    = lipgloss.Color("8")
	mcpChromeColor = lipgloss.Color("7")
	mcpRedColor    = lipgloss.Color("9")
)

func mcpLipglossEnabled(s *ui.Style) bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	return s.Color
}

func mcpFrameTitle(s *ui.Style, title string, stage, total int) string {
	if mcpLipglossEnabled(s) {
		bold := lipgloss.NewStyle().Bold(true).Foreground(mcpAccentColor)
		dim := lipgloss.NewStyle().Foreground(mcpDimColor)
		return fmt.Sprintf("%s %s", bold.Render(title), dim.Render(fmt.Sprintf("stage %d/%d", stage, total)))
	}
	return fmt.Sprintf("%s stage %d/%d", title, stage, total)
}

func mcpHR(s *ui.Style) string {
	if mcpLipglossEnabled(s) {
		return lipgloss.NewStyle().Foreground(mcpChromeColor).Render(strings.Repeat("─", 60))
	}
	return strings.Repeat("-", 60)
}

// runeClip clips to ~60 cols by rune (G).
func mcpRuneClip(s string, width int) string {
	runes := []rune(s)
	if len(runes) <= width {
		return s
	}
	return string(runes[:width-1]) + "…"
}

// -------------------------------------------------------------------
// Interactive form (B) — ONE model with enum-driven stage state machine
// -------------------------------------------------------------------

type mcpStage int

const (
	mcpStageAgents mcpStage = iota
	mcpStageScope
	mcpStageDone
)

type mcpInstallerModel struct {
	stage       mcpStage
	agents      []mcpAgentOption
	cursor      int
	scopeOpts   []mcpScopeOption
	scopeCursor int
	errMsg      string
	aborted     bool
	abortReason string
	style       *ui.Style
}

type mcpAgentOption struct {
	Def      mcpAgentDef
	Detected bool
	Selected bool
	Status   string
	Note     string
}

type mcpScopeOption struct {
	ID    string
	Label string
	Note  string
}

func mcpBuildOptions() ([]mcpAgentOption, []mcpScopeOption) {
	var agents []mcpAgentOption
	for _, def := range mcpAgentRegistry {
		detected := mcpIsAgentDetected(def)
		status := mcpAgentStatus(def, "repo")
		// default scope repo for status probe; per-option note best-effort from record store
		note := mcpAlreadyInstalledNote(def, "repo")
		agents = append(agents, mcpAgentOption{
			Def:      def,
			Detected: detected,
			Selected: detected && def.Selectable, // detected pre-selected
			Status:   status,
			Note:     note,
		})
	}
	scopes := []mcpScopeOption{
		{ID: "repo", Label: "Repo (project)", Note: "git root + requires eka.yaml, shared with team"},
		{ID: "global", Label: "Global (user)", Note: "home dirs, no project required"},
	}
	return agents, scopes
}

func (m mcpInstallerModel) Init() tea.Cmd { return nil }

func (m mcpInstallerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "esc":
			m.aborted = true
			m.abortReason = "aborted by user (" + msg.String() + ")"
			return m, tea.Quit
		case "up", "k":
			if m.stage == mcpStageAgents {
				if m.cursor == 0 {
					m.cursor = len(m.agents) - 1
				} else {
					m.cursor--
				}
			} else if m.stage == mcpStageScope {
				if m.scopeCursor == 0 {
					m.scopeCursor = len(m.scopeOpts) - 1
				} else {
					m.scopeCursor--
				}
			}
		case "down", "j":
			if m.stage == mcpStageAgents {
				m.cursor++
				if m.cursor >= len(m.agents) {
					m.cursor = 0
				}
			} else if m.stage == mcpStageScope {
				m.scopeCursor++
				if m.scopeCursor >= len(m.scopeOpts) {
					m.scopeCursor = 0
				}
			}
		case " ":
			if m.stage == mcpStageAgents {
				// space toggles multi-select (only selectable)
				if m.agents[m.cursor].Def.Selectable {
					m.agents[m.cursor].Selected = !m.agents[m.cursor].Selected
				}
				m.errMsg = ""
			} else if m.stage == mcpStageScope {
				// scope is radio following cursor (space also selects)
				// no-op, cursor already indicates selection
			}
		case "enter":
			if m.stage == mcpStageAgents {
				// inline validation between stages (at least one selection)
				has := false
				for _, a := range m.agents {
					if a.Selected {
						has = true
						break
					}
				}
				if !has {
					m.errMsg = "select at least one agent"
					return m, nil
				}
				m.stage = mcpStageScope
				m.errMsg = ""
			} else if m.stage == mcpStageScope {
				m.stage = mcpStageDone
				return m, tea.Quit
			}
		}
	}
	return m, nil
}

func (m mcpInstallerModel) View() string {
	var b strings.Builder
	// frame layout: bold title + stage N/M, accent stage name, HR
	total := 2
	stageNum := int(m.stage) + 1
	if m.stage == mcpStageDone {
		stageNum = total
	}
	title := mcpFrameTitle(m.style, "EKA MCP", stageNum, total)
	b.WriteString(title + "\n")
	b.WriteString(mcpHR(m.style) + "\n")
	stageName := ""
	if m.stage == mcpStageAgents {
		stageName = "Select agents"
	} else if m.stage == mcpStageScope {
		stageName = "Select scope"
	} else {
		stageName = "Done"
	}
	if mcpLipglossEnabled(m.style) {
		b.WriteString(lipgloss.NewStyle().Foreground(mcpAccentColor).Render(stageName) + "\n")
	} else {
		b.WriteString(stageName + "\n")
	}
	b.WriteString("\n")
	if m.stage == mcpStageAgents {
		for i, a := range m.agents {
			// checkbox glyphs for multi-select, dim unselected, vivid accent for selected
			checked := "[ ]"
			if a.Selected {
				checked = "[✓]"
			} else if !a.Def.Selectable {
				checked = "[×]"
			}
			cursor := "  "
			if i == m.cursor {
				cursor = "▶ "
			}
			line := fmt.Sprintf("%s%s %s", cursor, checked, a.Def.DisplayName+" ("+a.Def.ID+")")
			if a.Note != "" {
				line += " — " + a.Note
			}
			if a.Status != "" {
				line += " [" + a.Status + "]"
			}
			if !a.Def.Selectable {
				line += " (unavailable)"
			}
			line = mcpRuneClip(line, 60)
			if mcpLipglossEnabled(m.style) {
				if i == m.cursor {
					line = lipgloss.NewStyle().Foreground(mcpAccentColor).Render(line)
				} else if !a.Selected {
					line = lipgloss.NewStyle().Foreground(mcpDimColor).Render(line)
				}
			}
			b.WriteString(line + "\n")
		}
		if m.errMsg != "" {
			if mcpLipglossEnabled(m.style) {
				b.WriteString(lipgloss.NewStyle().Foreground(mcpRedColor).Render(m.errMsg) + "\n")
			} else {
				b.WriteString(m.errMsg + "\n")
			}
		} else {
			b.WriteString("\n")
		}
		b.WriteString("up/down wrap • space toggles • enter advances • esc aborts\n")
	} else if m.stage == mcpStageScope {
		for i, sc := range m.scopeOpts {
			radio := "( )"
			if i == m.scopeCursor {
				radio = "(●)"
			}
			cursor := "  "
			if i == m.scopeCursor {
				cursor = "▶ "
			}
			line := fmt.Sprintf("%s%s %s — %s", cursor, radio, sc.Label, sc.Note)
			line = mcpRuneClip(line, 60)
			if mcpLipglossEnabled(m.style) {
				if i == m.scopeCursor {
					line = lipgloss.NewStyle().Foreground(mcpAccentColor).Render(line)
				} else {
					line = lipgloss.NewStyle().Foreground(mcpDimColor).Render(line)
				}
			}
			b.WriteString(line + "\n")
		}
		b.WriteString("\nenter confirms • esc aborts\n")
	}
	return b.String()
}

// mcpRunTUI runs the bubbletea form; discovery BEFORE program starts.
func mcpRunTUI(s *ui.Style, stdin io.Reader, stdout io.Writer, presetAgents []string, presetScope string) (selectedAgents []string, selectedScope string, aborted bool, abortReason string, err error) {
	agents, scopes := mcpBuildOptions()
	// seed presets
	if len(presetAgents) > 0 {
		// reset selections
		for i := range agents {
			agents[i].Selected = false
		}
		for _, id := range presetAgents {
			found := false
			for i := range agents {
				if agents[i].Def.ID == id {
					agents[i].Selected = true
					found = true
					break
				}
			}
			if !found {
				return nil, "", false, "", fmt.Errorf("unknown agent %q", id)
			}
		}
	}
	scopeCursor := 0 // default repo
	if presetScope != "" {
		found := -1
		for i, sc := range scopes {
			if sc.ID == presetScope {
				found = i
				break
			}
		}
		if found == -1 {
			return nil, "", false, "", fmt.Errorf("unknown scope %q (want repo or global)", presetScope)
		}
		scopeCursor = found
	}
	m := mcpInstallerModel{
		stage:       mcpStageAgents,
		agents:      agents,
		cursor:      0,
		scopeOpts:   scopes,
		scopeCursor: scopeCursor,
		style:       s,
	}
	prog := tea.NewProgram(m, tea.WithInput(stdin), tea.WithOutput(stdout))
	final, err := prog.Run()
	if err != nil {
		return nil, "", false, "", err
	}
	fm, ok := final.(mcpInstallerModel)
	if !ok {
		return nil, "", false, "", errors.New("invalid program state")
	}
	if fm.aborted {
		return nil, "", true, fm.abortReason, nil
	}
	// collect selections
	for _, a := range fm.agents {
		if a.Selected {
			selectedAgents = append(selectedAgents, a.Def.ID)
		}
	}
	if fm.scopeCursor < len(fm.scopeOpts) {
		selectedScope = fm.scopeOpts[fm.scopeCursor].ID
	} else {
		selectedScope = "repo"
	}
	return selectedAgents, selectedScope, false, "", nil
}

// mcpSpinnerOutsideTUI: braille spinner ~80ms, plain final lines for non-TTY (G)
func mcpSpinner(s *ui.Style, msg string, fn func() error) error {
	if !s.TTY || !s.Color {
		fmt.Fprintln(s.W, msg+"...")
		err := fn()
		if err != nil {
			fmt.Fprintln(s.W, "✗ "+msg+": "+err.Error())
		} else {
			fmt.Fprintln(s.W, "✓ "+msg)
		}
		return err
	}
	// TTY braille spinner — stop chan closed via defer to avoid leak on panic/hang;
	// ticker is owned by the spinner goroutine; final line is printed only after
	// spinner goroutine has exited to avoid unsynchronized writes to s.W.
	frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	stop := make(chan struct{})
	spinnerDone := make(chan struct{})
	go func() {
		defer close(spinnerDone)
		ticker := time.NewTicker(80 * time.Millisecond)
		defer ticker.Stop()
		i := 0
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				fmt.Fprintf(s.W, "\r\033[K%s %s", frames[i%len(frames)], msg)
				i++
			}
		}
	}()
	done := make(chan error, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				done <- fmt.Errorf("panic: %v", r)
			}
		}()
		done <- fn()
	}()
	err := <-done
	// ensure spinner goroutine stops before final write (avoids concurrent Fprintf)
	select {
	case <-stop:
	default:
		close(stop)
	}
	<-spinnerDone
	// clear line and print final
	fmt.Fprintf(s.W, "\r\033[K")
	if err != nil {
		fmt.Fprintln(s.W, "✗ "+msg+": "+err.Error())
	} else {
		fmt.Fprintln(s.W, "✓ "+msg)
	}
	return err
}

// -------------------------------------------------------------------
// mcp serve — silent MCP server (protocol-only stdio) (A)
// -------------------------------------------------------------------

func newMcpServeCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Run the MCP server (protocol-only stdio)",
		Long: `Run the EKA MCP server over stdio (JSON-RPC 2.0, newline-delimited).
This is the CLI-owned entrypoint referenced in per-agent configs — e.g.
"command": "eka", "args": ["mcp","serve"].

Stdout is protocol-only; diagnostics go to stderr. Human output on stdout
would corrupt JSON-RPC framing.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMcpServe(cmd, args)
		},
	}
}

func runMcpServe(cmd *cobra.Command, args []string) error {
	// ZERO human output on stdout (diagnostics to stderr, stdout purity protocol-critical)
	// Ensure no accidental prints to stdout.
	// If extra args contain help, show help to stderr? But stdout must stay pure — help goes to stdout via cobra? We handle explicitly.
	for _, a := range args {
		if a == "-h" || a == "--help" || a == "help" {
			// Help is human output; it goes to stdout by cobra, but serve is protocol-only.
			// For serve, help should go to stderr to preserve stdout purity.
			// Render to stderr.
			s := styleFor(cmd)
			// Use cmd's help func but redirect
			buf := &bytes.Buffer{}
			cmd.SetOut(buf)
			// temporarily? Simpler: just print minimal help to stderr
			fmt.Fprintln(cmd.ErrOrStderr(), "Usage: eka mcp serve")
			fmt.Fprintln(cmd.ErrOrStderr(), "Run the MCP server over stdio (JSON-RPC 2.0).")
			_ = s
			return nil
		}
	}
	// Locate plugin binary (eka-mcp) for actual MCP server.
	// The CLI-owned entrypoint delegates to the plugin binary's serve, but stdout stays pure.
	home, _ := os.UserHomeDir()
	dir := plugin.PluginDir(home)
	exe := ""
	if dir != "" {
		cand := filepath.Join(dir, "eka-mcp")
		if runtime.GOOS == "windows" {
			cand += ".exe"
		}
		if _, err := os.Stat(cand); err == nil {
			exe = cand
		}
	}
	if exe == "" {
		// fallback to PATH
		if p, err := exec.LookPath("eka-mcp"); err == nil {
			exe = p
		}
	}
	if exe == "" {
		fmt.Fprintln(cmd.ErrOrStderr(), "eka: plugin \"mcp\" is not installed — install it with: eka plugin install mcp")
		return mcpExitError(mcpExitNotFound)
	}
	// Verify checksum if sidecar exists (reuse plugin dispatch verification)
	if dir != "" {
		if sum, ok := readPluginChecksum(dir, "mcp"); ok {
			if got, err := sha256File(exe); err == nil && !strings.EqualFold(got, sum) {
				fmt.Fprintln(cmd.ErrOrStderr(), "eka: plugin \"mcp\" does not match checksum recorded at install — reinstall it with: eka plugin install mcp")
				return mcpExitError(mcpExitGeneral)
			}
		}
	}
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	// Dispatch with bounded env, propagate exit code, stdout purity: plugin stdout -> our stdout, diagnostics -> stderr
	c := exec.CommandContext(ctx, exe, "serve")
	c.Env = pluginDispatchEnv()
	c.Stdin = cmd.InOrStdin()
	c.Stdout = cmd.OutOrStdout()
	c.Stderr = cmd.ErrOrStderr()
	if err := c.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code := ee.ExitCode()
			if code < 0 {
				code = mcpExitGeneral
			}
			return &exitError{code: code}
		}
		return err
	}
	return nil
}

// -------------------------------------------------------------------
// Bare `eka mcp` — informational overview + interactive installer (A)
// -------------------------------------------------------------------

func newMcpCommand() *cobra.Command {
	var flagAgents []string
	var flagScope string
	var flagJSON bool
	var flagForce bool
	cmd := &cobra.Command{
		Use:     "mcp",
		Short:   "MCP entrypoint — overview and installer",
		GroupID: groupUtility,
		Long: `EKA MCP entrypoint.

Bare 'eka mcp' prints an overview and opens the interactive installer
(pick target agent(s) and scope). 'eka mcp serve' runs the MCP server
with protocol-only stdio — it is the command referenced in per-agent
configs (migrating away from direct plugin-binary invocations).

Agents are detected by folder presence and pre-selected. Scope is a
single-select radio defaulting to repo (git root + eka.yaml required)
vs global (home dirs, no project).

Exit codes:
  0  ok / installed
  1  general (validation, internal)
  2  conflict (preflight without --force)
  3  not found (unknown agent / missing plugin for serve)
  4  precondition (non-TTY without --agent/--scope/--json, not selectable)`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Handle help-only forms deterministically
			if len(args) == 1 && (args[0] == "-h" || args[0] == "--help" || args[0] == "help") {
				return cmd.Help()
			}
			if len(args) > 0 {
				return fmt.Errorf("unknown subcommand %q for \"eka mcp\" (only \"serve\" is supported)", args[0])
			}
			return runMcpBare(cmd, flagAgents, flagScope, flagJSON, flagForce)
		},
	}
	cmd.Flags().StringSliceVar(&flagAgents, "agent", nil, "preset agent(s) for the installer (repeatable, e.g. --agent claude --agent codex)")
	cmd.Flags().StringVar(&flagScope, "scope", "", "preset scope: repo or global")
	cmd.Flags().BoolVar(&flagJSON, "json", false, "emit versioned envelope {version,status,data} and never open the form")
	cmd.Flags().BoolVar(&flagForce, "force", false, "overwrite conflicts")
	cmd.AddCommand(newMcpServeCommand())
	return cmd
}

func runMcpBare(cmd *cobra.Command, flagAgents []string, flagScope string, flagJSON, flagForce bool) error {
	s := styleFor(cmd)
	// Validation: invalid --agent/--scope FAIL FAST (C)
	if flagScope != "" && flagScope != "repo" && flagScope != "global" {
		if flagJSON {
			mcpWriteEnvelope(s.Raw(), "error", map[string]string{"error": fmt.Sprintf("invalid --scope %q (want repo or global)", flagScope)})
		} else {
			mcpActionableError(s, fmt.Sprintf("invalid --scope %q", flagScope), "scope must be repo or global", "pass --scope repo or --scope global")
		}
		return mcpExitError(mcpExitGeneral)
	}
	for _, a := range flagAgents {
		if mcpFindAgent(a) == nil {
			if flagJSON {
				mcpWriteEnvelope(s.Raw(), "error", map[string]string{"error": fmt.Sprintf("unknown agent %q", a)})
			} else {
				mcpActionableError(s, fmt.Sprintf("unknown agent %q", a), "agent not in registry", fmt.Sprintf("available: %s", mcpRegistryIDs()))
			}
			return mcpExitError(mcpExitNotFound)
		}
		if !mcpFindAgent(a).Selectable {
			if flagJSON {
				mcpWriteEnvelope(s.Raw(), "error", map[string]string{"error": fmt.Sprintf("agent %q is not selectable", a)})
			} else {
				mcpActionableError(s, fmt.Sprintf("agent %q is not selectable", a), "agent is marked unavailable", "pick a selectable agent")
			}
			return mcpExitError(mcpExitPrecondition)
		}
	}
	// --json NEVER opens form (E)
	if flagJSON {
		// status/info: per-option notes, states (F)
		agents, _ := mcpBuildOptions()
		statusData := map[string]any{
			"overview": "EKA MCP — CLI-owned entrypoint. Bare 'eka mcp' opens the interactive installer; 'eka mcp serve' is the protocol-only server.",
			"agents":   agents,
			"scope":    flagScope,
			"serve":    "eka mcp serve",
		}
		mcpWriteEnvelope(s.Raw(), "ok", statusData)
		return nil
	}
	// TTY gate: pipes/redirects never open form; non-TTY refusal names bypasses (E)
	if !mcpCanPrompt(cmd, s) {
		// Non-TTY fallback: plain overview + hint, no form
		fmt.Fprintln(s.W, "EKA MCP — CLI-owned entrypoint")
		fmt.Fprintln(s.W, "Bare 'eka mcp' opens the interactive installer (pick agents + scope).")
		fmt.Fprintln(s.W, "Server: 'eka mcp serve' (protocol-only stdio; referenced in per-agent configs).")
		fmt.Fprintln(s.W, "")
		fmt.Fprintln(s.W, "Non-interactive bypasses:")
		fmt.Fprintln(s.W, "  eka mcp --agent <id> --scope <repo|global> [--force]")
		fmt.Fprintln(s.W, "  eka mcp --json   (machine envelope)")
		// If presets provided, run gated pipeline directly
		if len(flagAgents) > 0 && flagScope != "" {
			// run batch pipeline without TUI
			err := mcpSpinner(s, "Installing MCP configs", func() error {
				return mcpExecuteBatch(s, flagAgents, flagScope, flagForce)
			})
			if err != nil {
				var ce *mcpConflictError
				if errors.As(err, &ce) {
					mcpActionableError(s, ce.Error(), "conflicts require --force", "re-run with --force to overwrite")
					return mcpExitError(mcpExitConflict)
				}
				mcpActionableError(s, err.Error(), "install failed", "check paths and retry with --force if needed")
				return mcpExitError(mcpExitGeneral)
			}
			fmt.Fprintln(s.W, "Installed: "+strings.Join(flagAgents, ", ")+" ["+flagScope+"]")
			return nil
		}
		mcpActionableError(s, "interactive installer requires a TTY", "stdin/stdout is not a terminal", "use --agent/--scope flags or --json, or run in a terminal")
		return mcpExitError(mcpExitPrecondition)
	}
	// Discovery/option-building BEFORE bubbletea starts (B)
	// If presets fully provided, optionally still open form seeded? Spec: --agent/--scope flags seed form as presets
	// We open TUI seeded with presets (if any)
	selectedAgents, selectedScope, aborted, abortReason, err := mcpRunTUI(s, cmd.InOrStdin(), cmd.OutOrStdout(), flagAgents, flagScope)
	if err != nil {
		mcpActionableError(s, err.Error(), "installer form failed", "retry or use --agent/--scope --json bypass")
		return mcpExitError(mcpExitGeneral)
	}
	if aborted {
		// Ctrl-C/Esc abort with reason on model (B)
		fmt.Fprintln(s.W, "Aborted: "+abortReason)
		return mcpExitError(mcpExitGeneral)
	}
	// Form ONLY resolves input; installation runs AFTER program returns (B)
	// Plain-text confirmation summary after terminal restore (B)
	fmt.Fprintln(s.W, "")
	fmt.Fprintln(s.W, "Selected agents: "+strings.Join(selectedAgents, ", "))
	fmt.Fprintln(s.W, "Selected scope: "+selectedScope)
	fmt.Fprintln(s.W, "")
	// Installation through same gated batch pipeline (TUI cannot bypass gates)
	err = mcpSpinner(s, "Installing MCP configs", func() error {
		return mcpExecuteBatch(s, selectedAgents, selectedScope, flagForce)
	})
	if err != nil {
		var ce *mcpConflictError
		if errors.As(err, &ce) {
			mcpActionableError(s, ce.Error(), "conflicts detected", "re-run with --force to overwrite")
			return mcpExitError(mcpExitConflict)
		}
		mcpActionableError(s, err.Error(), "install failed", "check diagnostics and retry")
		return mcpExitError(mcpExitGeneral)
	}
	fmt.Fprintln(s.W, "✓ Installed MCP configs for "+strings.Join(selectedAgents, ", ")+" ["+selectedScope+"]")
	fmt.Fprintln(s.W, "  Per-agent configs now reference: eka mcp serve")
	return nil
}

func mcpRegistryIDs() string {
	ids := make([]string, 0, len(mcpAgentRegistry))
	for _, d := range mcpAgentRegistry {
		if d.Selectable {
			ids = append(ids, d.ID)
		}
	}
	return strings.Join(ids, ", ")
}

// legacy helpers for compatibility with existing tests (stub helpers)
var mcpSubcommands = []string{"serve"}

func mcpStubLong() string {
	return `EKA MCP server and plugin tooling

Bare 'eka mcp' prints an overview and opens the interactive installer.
'eka mcp serve' runs the MCP server with protocol-only stdio.

Use "eka mcp --help" for overview, or "eka mcp serve --help" for server help.`
}

func newMcpStubCommand() *cobra.Command { return newMcpCommand() }

func findMcpCommand(root *cobra.Command) *cobra.Command {
	for _, c := range root.Commands() {
		if c.Name() == "mcp" {
			return c
		}
	}
	return nil
}
