package cmd

// This file implements B1 deferred command registration
// (sto:mcp-command-registration, ADR-031): plugin commands declared in
// an installed plugin's manifest are registered into the cobra command
// tree at root construction time, and dispatch to the plugin
// executable under the G-series trust boundary.
//
// Registration is DEFERRED: it happens at every root construction
// (per process, memoized), never at install time. A plugin installed
// via the official registry (G1) whose manifest declares commands
// (the B1 versioned dispatch-protocol extension — an additive
// "commands" array on the v1 manifest contract) gets one cobra command
// per declared command, grouped under the dynamic "Plugins" group
// (the cobra GroupID mechanism from sto:help-ux). Executing such a
// command dispatches to the plugin executable with the bounded env and
// args contract (the eka-core/plugin runner's pluginEnv whitelist:
// PATH, HOME, EKA_PLUGIN_DIR, SystemRoot — never the CLI's secrets)
// and propagates the plugin's exit code.
//
// Trust boundary (G1-G4, ADR-031):
//
//	G1  official-registry-only — only plugins resolved through the
//	    built-in registry (plugin.OfficialRegistry) register commands;
//	    third-party plugins never register.
//	G2  anti-TOCTOU — the install/update finalize records the installed
//	    binary's SHA-256 in a sidecar (<dir>/.eka-<name>.sha256);
//	    registration verifies the binary against the sidecar, and EVERY
//	    dispatch recomputes the binary's SHA-256 and compares it against
//	    the recorded checksum — a binary swapped between registration
//	    and dispatch refuses deterministically.
//	G3  plugin dir over PATH — commands register only from the
//	    plugin-directory instance (the install target); a plugin found
//	    only on PATH is refused with a deterministic message, and a PATH
//	    copy shadowing an installed plugin is ignored with a warning.
//	G4  collision refusal + manifest cache — a command colliding with a
//	    built-in or with another plugin's command refuses
//	    deterministically (first-wins in sorted order); the manifest
//	    probe is cached per process with a TTL and invalidated on
//	    reinstall (the binary's file identity changes).
//
// Failure semantics (never degrade the CLI): the manifest probe is
// bounded by pluginProbeTimeout (<= 2s — a hung plugin is killed, not
// waited on); a broken, hung, unverifiable or colliding plugin is
// SKIPPED with a visible deterministic warning on stderr — the CLI
// always continues, and every other command works unchanged.
// Registration and dispatch decisions are memoized per process
// (pluginRegCache + the checksum bound into each dispatch command), so
// repeated root constructions within one process never re-probe.
//
// The probe runs the plugin's "manifest --json" itself (bounded env,
// bounded output, bounded deadline) because the B1 extension lives in
// the manifest JSON, which the eka-core ManifestContext parse does not
// expose raw; the core contract fields are validated the same way
// (contract version + name). This is the CLI-side mirror of the
// eka-core runner, deliberately kept minimal and documented in
// ADR-031.

import (
	"bytes"
	"context"
	"encoding/hex"
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
	"sync"
	"time"

	"github.com/maleolabs/eka-core/plugin"
	"github.com/spf13/cobra"
)

// pluginProbeTimeout bounds the registration manifest probe of an
// installed plugin: a hung plugin is killed after this deadline and
// skipped with a warning — a broken plugin must never wedge the CLI
// (acceptance criterion: probe timeout <= 2s). Var (not const) so
// tests can shrink it.
var pluginProbeTimeout = 2 * time.Second

// probeWaitDelay bounds the probe's pipes after the direct child has
// exited (F1): a pipe-inheriting grandchild (a daemonized sidecar, a
// backgrounded subshell) holds the stdout/stderr write-ends open, so
// exec.Wait() would block forever even though the child itself exited
// cleanly — WaitDelay forcibly closes the pipes and Run() returns
// ErrWaitDelay. Small: it only costs time when a grandchild actually
// holds the pipes (a normal probe finishes before the timer fires).
var probeWaitDelay = 100 * time.Millisecond

// pluginManifestCacheTTL bounds the per-process registration cache: a
// cached probe is reused for this long and only while the binary's
// file identity (size + mtime) is unchanged — a reinstall writes a new
// binary, so it invalidates the entry immediately (acceptance
// criterion: manifest cached with TTL and invalidated on reinstall).
// Var (not const) so tests can shrink it.
var pluginManifestCacheTTL = 30 * time.Second

// pluginMaxManifestSize caps the manifest output of the registration
// probe (a manifest is a few KiB; anything larger is a broken or
// hostile plugin). It mirrors the eka-core runner's
// maxPluginOutputSize contract.
const pluginMaxManifestSize = 1 << 20 // 1 MiB

// pluginMaxCommandCount caps the number of commands a single plugin
// manifest may declare (L2): a manifest declaring more is refused with
// a visible warning and skipped — a single plugin must not inflate the
// command tree arbitrarily.
const pluginMaxCommandCount = 100

// pluginCommandSpec is one command of the B1 dispatch-protocol
// extension: an additive "commands" array on the v1 manifest contract
// (the contract field stays "v1" — the extension is backward
// compatible; a plugin that does not emit it registers nothing).
//
// Dispatch contract: running the registered command executes
//
//	eka-<name> <args...> [user args...]
//
// under the bounded env whitelist (pluginDispatchEnv); the plugin's
// stdout/stderr/stdin inherit the CLI's streams and its exit code
// propagates. The plugin owns its own flags — the CLI never interprets
// them (DisableFlagParsing).
type pluginCommandSpec struct {
	// Name is the CLI command name (e.g. "mcp").
	Name string `json:"name"`
	// Description is the one-line help text shown in `eka --help`.
	Description string `json:"description"`
	// Args are the fixed arguments appended to the plugin executable
	// before the user's arguments (e.g. ["serve"] for an "mcp" command
	// dispatching to "eka-mcp serve").
	Args []string `json:"args"`
}

// pluginCommandManifest extends the eka-core Manifest with the B1
// versioned extension (the commands array). json.Unmarshal ignores
// unknown fields, so the core contract parse stays authoritative and
// the extension is additive.
type pluginCommandManifest struct {
	plugin.Manifest
	Commands []pluginCommandSpec `json:"commands"`
}

// pluginRegEntry is the per-process registration record of one plugin
// candidate path (an installed executable or a PATH-only find): the
// parsed manifest + expected checksum for a registered plugin, or the
// deterministic refusal message. The entry is valid for
// pluginManifestCacheTTL and only while the file's identity (size +
// mtime) and the recorded checksum still match — a reinstall writes a
// new binary and a new sidecar, so it invalidates the entry
// (acceptance criterion: invalidated on reinstall).
type pluginRegEntry struct {
	path     string
	name     string // plugin name (sidecar lookup for the M2 reinstall check)
	dir      string // plugin directory (sidecar lookup for the M2 reinstall check)
	manifest pluginCommandManifest
	checksum string // expected SHA-256 (sidecar), "" when refused
	refusal  string // deterministic refusal message, "" when registered
	size     int64
	modTime  time.Time
	recorded time.Time
}

// pluginRegCache is the per-process registration cache (the memoize of
// registration decisions): keyed by candidate path, guarded by a mutex
// so concurrent Execute calls stay safe.
var (
	pluginRegMu    sync.Mutex
	pluginRegCache = map[string]*pluginRegEntry{}
	// pluginRegWarned memoizes the warnings already reported per
	// candidate path (and per command collision), so repeated root
	// constructions within one process never repeat them.
	pluginRegWarned = map[string]bool{}
	// pluginRegWarnings collects the deterministic registration
	// warnings of the process; Execute flushes them to the caller's
	// stderr AFTER the root's streams are set (registration runs during
	// root construction, before the streams exist). Guarded by
	// pluginRegMu.
	pluginRegWarnings []string
	// pluginPathScanMu guards pluginPathScanCache, the memoized PATH
	// scan (L3): keyed by the PATH env value, so a changed PATH
	// re-scans.
	pluginPathScanMu    sync.Mutex
	pluginPathScanCache = map[string][]pathOnlyFinding{}
)

// pluginRegistryOfficial reports whether a plugin name is official
// (G1). Production is the built-in registry; var (not const) so tests
// can extend the official set for collision scenarios.
var pluginRegistryOfficial = plugin.OfficialRegistry.IsOfficial

// registerPluginCommands registers the commands of every installed
// official plugin into root (B1 deferred registration) and returns the
// registered commands. It runs at every root construction; the
// per-process cache makes repeated constructions cheap. Registration
// NEVER fails the CLI: every problem (broken plugin, probe timeout,
// missing checksum, collision, PATH-only find) is a visible
// deterministic warning on stderr and a skip.
func registerPluginCommands(root *cobra.Command) []*cobra.Command {
	dir := ""
	if home, err := os.UserHomeDir(); err == nil {
		dir = plugin.PluginDir(home)
	}
	// L1: the PATH scan/refusals run UNCONDITIONALLY — a plugin found
	// only on PATH must be visibly refused even when the plugin
	// directory is missing or empty, not only after the dir-existence
	// fast path (on a machine without ~/.eka/plugins, PATH-only eka-*
	// used to get no visible refusal at all). They run before the
	// UserHomeDir check too (L2): a machine where the home directory
	// cannot be resolved still gets the PATH refusals (dir is "" then).
	pathOnlyRefusals(dir)
	if dir == "" {
		return nil
	}
	// Fast path: no plugin directory on disk — nothing can be installed,
	// no probe cost on a plugin-free machine.
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return nil
	}

	// L3: the plugin-directory scan is memoized per process (the dir
	// path is the key; a reinstall writes into the same dir, and the
	// per-plugin cache invalidates on the new binary identity).
	var registered []*cobra.Command
	for _, name := range installedOfficialNamesIn(dir) {
		exe := installedExePath(dir, name)
		entry := pluginRegEntryFor(exe, name, dir)
		if entry == nil {
			continue
		}
		if entry.refusal != "" {
			pluginRegWarn(entry)
			continue
		}
		// Commands register in sorted name order (deterministic
		// first-wins collision semantics across plugins).
		specs := append([]pluginCommandSpec(nil), entry.manifest.Commands...)
		sort.Slice(specs, func(i, j int) bool { return specs[i].Name < specs[j].Name })
		for _, spec := range specs {
			// Disclosure-only commands for the mcp plugin (bug:mcp-help-subcommands-hidden):
			// the manifest declares the actual subcommands (manifest, install, configure, serve)
			// for help disclosure; they are not top-level `eka <subcommand>` but
			// subcommands of `eka mcp`. The native stub `eka mcp` already
			// discloses them statically, and `eka mcp <subcommand>` is
			// handled by the whole-binary proxy (Args []), so skip
			// top-level registration for disclosure entries (only those 4).
			if name == "mcp" {
				isDisclosure := false
				for _, d := range mcpSubcommands {
					if spec.Name == d {
						isDisclosure = true
						break
					}
				}
				if isDisclosure {
					continue
				}
			}
			cmd, refusal := newPluginDispatchCommand(root, exe, name, spec, entry.checksum)
			if refusal != "" {
				if spec.Name == "mcp" && strings.Contains(refusal, "collides with the existing") {
					continue
				}
				pluginRegWarnKey(exe+"\x00"+spec.Name, refusal)
				continue
			}
			registered = append(registered, cmd)
		}
	}
	return registered
}

// pathOnlyRefusals applies the G3 rule to the PATH: every "eka-*"
// executable found on PATH that is NOT the installed plugin-directory
// instance is refused (PATH-only — never installed through the
// verified install path, so its commands never register) or ignored
// with a warning (a PATH copy shadowing an installed plugin). The
// refusal is memoized per process (keyed by the found path + the
// classification), so repeated root constructions print it once. The
// plugin-directory instance itself (when the plugin dir is on PATH) is
// the same file and is skipped silently.
//
// L3: the PATH scan itself is memoized per process (keyed by the PATH
// env value) — scanning every PATH dir on every root construction is
// wasteful; the cache is keyed by the env value, so a changed PATH
// (e.g. between test runs) re-scans.
func pathOnlyRefusals(dir string) {
	for _, f := range pathOnlyScan(os.Getenv("PATH")) {
		if f.path == installedExePath(dir, f.name) {
			continue // the installed instance itself (plugin dir on PATH).
		}
		// M1 (review finding): the installed/path-only classification
		// is recomputed HERE, at refusal time, against the CURRENT
		// plugin directory — the memoized PATH scan is keyed by the
		// PATH env value only, and the classification depends on dir,
		// so it must not be captured at scan time (a stale scan would
		// misclassify after the plugin is installed). The warning memo
		// key carries the classification, so the same path can emit
		// the correct message per dir within one process.
		if pluginInstalledIn(dir, f.name, runtime.GOOS) {
			// The plugin-dir instance wins (G3): the PATH copy is
			// shadowed and never used for dispatch — visible, so a
			// stale PATH copy cannot silently diverge from the
			// installed plugin. The path is sanitized (F2): a PATH dir
			// or file name carrying terminal-control bytes must not
			// inject raw ESC into the warning.
			pluginRegWarnKey(f.path+"\x00installed", fmt.Sprintf("plugin %q is also on PATH (%s) — the installed plugin is used and the PATH copy is ignored", f.name, sanitizeTerminal(f.path)))
			continue
		}
		// PATH-only plugin: refused deterministically — it was never
		// installed through the verified install path, so its
		// commands never register. The path is sanitized (F2).
		pluginRegWarnKey(f.path+"\x00path-only", fmt.Sprintf("plugin %q is on PATH (%s) but not installed in the plugin directory — its commands are not registered; install it with: eka plugin install %s", f.name, sanitizeTerminal(f.path), f.name))
	}
}

// pathOnlyFinding is one "eka-*" executable found on PATH during the
// memoized PATH scan. The installed/path-only classification is NOT
// captured here (M1): it depends on the plugin directory, which can
// change within a process, while the scan cache is keyed by the PATH
// env value only — pathOnlyRefusals recomputes the classification at
// refusal time.
type pathOnlyFinding struct {
	name string
	path string
}

// pathOnlyScan scans PATH for "eka-*" executables; the result is
// memoized per process by the PATH env value (L3). The scan is
// deliberately classification-free (M1): the installed/path-only
// decision depends on the plugin directory, which is not part of the
// cache key — pathOnlyRefusals recomputes it against the current dir.
func pathOnlyScan(pathEnv string) []pathOnlyFinding {
	pluginPathScanMu.Lock()
	if f, ok := pluginPathScanCache[pathEnv]; ok {
		pluginPathScanMu.Unlock()
		return f
	}
	pluginPathScanMu.Unlock()

	var findings []pathOnlyFinding
	for _, pathDir := range filepath.SplitList(pathEnv) {
		if pathDir == "" {
			continue
		}
		entries, err := os.ReadDir(pathDir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasPrefix(e.Name(), "eka-") {
				continue
			}
			name := strings.TrimPrefix(e.Name(), "eka-")
			if runtime.GOOS == "windows" {
				name = strings.TrimSuffix(name, ".exe")
			}
			if name == "" || name == "eka" || strings.HasSuffix(name, ".old") {
				continue
			}
			findings = append(findings, pathOnlyFinding{
				name: name,
				path: filepath.Join(pathDir, e.Name()),
			})
		}
	}

	pluginPathScanMu.Lock()
	pluginPathScanCache[pathEnv] = findings
	pluginPathScanMu.Unlock()
	return findings
}

// installedOfficialNamesIn lists the plugin-directory entries that are
// installed OFFICIAL plugins: "eka-<name>" executables (not the CLI
// itself, not .old debris, not the checksum sidecars) whose name
// resolves through the official registry, sorted. Only these are
// registration candidates (G1 + G3: the plugin-dir instance is the
// authoritative executable).
func installedOfficialNamesIn(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), "eka-") {
			continue
		}
		name := strings.TrimPrefix(e.Name(), "eka-")
		if runtime.GOOS == "windows" {
			name = strings.TrimSuffix(name, ".exe")
		}
		if name == "" || name == "eka" || strings.HasSuffix(name, ".old") {
			continue
		}
		if !pluginRegistryOfficial(name) {
			// L3 (review finding): a third-party plugin installed in
			// the plugin directory is never a registration candidate
			// (G1) — the silent skip becomes a visible once-per-process
			// hint, so the user understands why its commands are not
			// registered.
			pluginRegWarnKey(filepath.Join(dir, e.Name()), fmt.Sprintf("plugin %q is not from the official registry — its commands are not registered", name))
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// installedExePath is the plugin-directory executable path of an
// installed plugin (the .exe form on windows mirrors the asset
// suffix).
func installedExePath(dir, name string) string {
	exe := filepath.Join(dir, "eka-"+name)
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	return exe
}

// pluginRegEntryFor returns the registration record of one installed
// plugin, probing and verifying it on a cache miss. The order is
// deliberate (M1, verify-before-execute): the sidecar checksum is read
// and the binary's SHA-256 is verified BEFORE the manifest probe runs —
// a tampered or unverifiable binary never has its own code executed.
// Only a verified binary is probed (bounded by pluginProbeTimeout).
// The record is cached per process (TTL + binary identity), so
// repeated root constructions reuse it without re-probing
// (acceptance criteria: manifest cached with TTL, invalidated on
// reinstall, per-process memoize).
func pluginRegEntryFor(exe, name, dir string) *pluginRegEntry {
	pluginRegMu.Lock()
	if e, ok := pluginRegCache[exe]; ok && pluginRegEntryValid(e) {
		pluginRegMu.Unlock()
		return e
	}
	pluginRegMu.Unlock()

	// Slow path (cache miss): verify the binary, then probe it (a
	// bounded subprocess — never under the lock).
	entry := &pluginRegEntry{path: exe, name: name, dir: dir, recorded: time.Now()}
	if fi, err := os.Stat(exe); err == nil {
		entry.size, entry.modTime = fi.Size(), fi.ModTime()
	}
	// M1: verify-before-execute — read the sidecar and verify the
	// binary's SHA-256 FIRST. A tampered binary (or one with no
	// recorded checksum) is refused here, before the manifest probe
	// ever runs the plugin's own code (defense in depth: the probe is
	// the plugin's binary; never run it on an unverifiable binary).
	sum, ok := readPluginChecksum(dir, name)
	if !ok {
		entry.refusal = fmt.Sprintf("plugin %q has no recorded checksum — its commands are not registered (reinstall it with: eka plugin install %s)", name, name)
	} else if got, err := sha256File(exe); err != nil {
		// L1 (review finding): an unreadable binary is a distinct
		// failure from a checksum mismatch — the message must not
		// claim the binary "does not match" when it cannot even be
		// read for verification.
		entry.refusal = fmt.Sprintf("plugin %q cannot be read for verification — its commands are not registered (reinstall it with: eka plugin install %s)", name, name)
	} else if !strings.EqualFold(got, sum) {
		// G2 at registration: a binary that does not match the
		// checksum recorded at install never registers (defense in
		// depth — dispatch re-verifies anyway).
		entry.refusal = fmt.Sprintf("plugin %q does not match the checksum recorded at install — its commands are not registered (reinstall it with: eka plugin install %s)", name, name)
	} else {
		entry.checksum = sum
		// Only after the checksum verifies do we probe the manifest —
		// a verified binary may still be broken or hung, which is a
		// skip (visible warning), not a refusal.
		ctx, cancel := context.WithTimeout(context.Background(), pluginProbeTimeout)
		defer cancel()
		m, err := probePluginManifest(ctx, exe, name)
		if err != nil {
			if ctx.Err() == context.DeadlineExceeded {
				entry.refusal = fmt.Sprintf("plugin %q timed out after %s answering \"manifest\" — its commands are not registered", name, pluginProbeTimeout)
			} else {
				entry.refusal = fmt.Sprintf("plugin %q is broken (manifest probe failed: %s) — its commands are not registered", name, err)
			}
		} else {
			entry.manifest = m
			// L2: cap the command count — a single plugin must not
			// inflate the command tree arbitrarily.
			if len(m.Commands) > pluginMaxCommandCount {
				entry.refusal = fmt.Sprintf("plugin %q declares %d commands, exceeding the cap of %d — its commands are not registered", name, len(m.Commands), pluginMaxCommandCount)
				entry.manifest.Commands = nil
			}
		}
	}

	// Double-checked store: another goroutine may have filled the entry
	// while the probe ran.
	pluginRegMu.Lock()
	if e, ok := pluginRegCache[exe]; ok && pluginRegEntryValid(e) {
		pluginRegMu.Unlock()
		return e
	}
	pluginRegCache[exe] = entry
	pluginRegMu.Unlock()
	return entry
}

// pluginRegEntryValid reports whether a cached entry is still usable:
// within the TTL, with an unchanged binary identity (size + mtime) and
// — for a registered entry — a sidecar that still records the same
// checksum. A reinstall writes a new binary and a new sidecar, so it
// invalidates the entry immediately (acceptance criterion: invalidated
// on reinstall).
//
// M2 (review finding): size + mtime alone is flaky on
// coarse-granularity filesystems (a reinstall within the same second
// can keep both unchanged); the sidecar is written by the install
// finalize, so its content is the authoritative reinstall signal.
func pluginRegEntryValid(e *pluginRegEntry) bool {
	if time.Since(e.recorded) > pluginManifestCacheTTL {
		return false
	}
	fi, err := os.Stat(e.path)
	if err != nil {
		return false
	}
	if fi.Size() != e.size || !fi.ModTime().Equal(e.modTime) {
		return false
	}
	if e.checksum != "" {
		sum, ok := readPluginChecksum(e.dir, e.name)
		if !ok || !strings.EqualFold(sum, e.checksum) {
			return false
		}
	}
	return true
}

// probePluginManifest runs the plugin's "manifest --json" bounded by
// ctx (the registration probe: pluginProbeTimeout, the env whitelist,
// a 1 MiB output cap) and parses the B1-extended manifest. The core
// contract fields are validated the same way the eka-core
// ManifestContext does (contract version, name). The probe runs the
// executable itself because the B1 extension lives in the manifest
// JSON, which the core parse does not expose raw.
//
// M2: stderr is bounded (pluginMaxManifestSize, same cap as stdout) —
// a spewing plugin cannot exhaust memory, and the failure message
// surfaces a truncation notice instead of an unbounded dump.
func probePluginManifest(ctx context.Context, exe, wantName string) (pluginCommandManifest, error) {
	cmd := exec.CommandContext(ctx, exe, "manifest", "--json")
	cmd.Env = pluginDispatchEnv()
	// F1 (security review): the deadline must bound the WHOLE probe, not
	// just the direct child. A pipe-inheriting grandchild (a daemonized
	// sidecar, a backgrounded subshell) holds the probe's stdout/stderr
	// write-ends open, so exec.Wait() would block past the deadline and
	// wedge every eka invocation. probeKillGroup makes the probe its own
	// process group and kills the group on deadline (the never-exit
	// case); WaitDelay bounds the clean-exit case (the child exited but
	// a grandchild still holds the pipes — exec's Cancel never fires
	// then, so the pipes are forcibly closed and Run() returns
	// ErrWaitDelay). On windows probeKillGroup is a no-op (Setpgid is
	// not in the stdlib syscall package; job objects are out of stdlib
	// scope — WaitDelay still bounds the probe).
	probeKillGroup(cmd)
	cmd.WaitDelay = probeWaitDelay
	var out pluginLimitedBuffer
	var errb pluginLimitedBuffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err := cmd.Run()
	// F1: reap any process-group member that survived the run (the
	// ErrWaitDelay case — a clean-exit plugin's background child must
	// not leak past the probe). A no-op when the group is empty.
	probeKillGroupRemnants(cmd)
	if err != nil {
		if ctx.Err() != nil {
			return pluginCommandManifest{}, ctx.Err()
		}
		if errors.Is(err, exec.ErrWaitDelay) {
			return pluginCommandManifest{}, errors.New("manifest probe left a background child holding the probe pipes")
		}
		if out.overflow {
			return pluginCommandManifest{}, fmt.Errorf("manifest output exceeds %d bytes", pluginMaxManifestSize)
		}
		// M3 (review finding): the plugin's stderr is attacker-controlled
		// text — it is sanitized (terminal-control bytes neutralized)
		// before it can reach the CLI's warning output, exactly like the
		// manifest's description (sanitizeTerminal invariant).
		msg := sanitizeTerminal(strings.TrimSpace(errb.buf.String()))
		if errb.overflow {
			// The buffer bounds memory at pluginMaxManifestSize, but the
			// embedded content is additionally truncated to a small
			// display cap: a hostile plugin must not control the size of
			// the CLI's warning output. Truncation is rune-safe (a
			// multi-byte rune must not be split).
			if r := []rune(msg); len(r) > maxProbeStderrMessage {
				msg = string(r[:maxProbeStderrMessage]) + "..."
			}
			if msg != "" {
				msg += " " + truncatedStderrSuffix
			} else {
				msg = truncatedStderrSuffix
			}
		}
		if msg == "" {
			return pluginCommandManifest{}, errors.New("manifest probe failed with no output")
		}
		return pluginCommandManifest{}, errors.New(msg)
	}
	if out.overflow {
		return pluginCommandManifest{}, fmt.Errorf("manifest output exceeds %d bytes", pluginMaxManifestSize)
	}
	var m pluginCommandManifest
	if err := json.Unmarshal(out.buf.Bytes(), &m); err != nil {
		return pluginCommandManifest{}, fmt.Errorf("manifest is not valid JSON: %w", err)
	}
	if m.Contract != "" && m.Contract != plugin.ContractVersion {
		return pluginCommandManifest{}, fmt.Errorf("contract %q is not supported (want %q)", m.Contract, plugin.ContractVersion)
	}
	if m.Name != wantName {
		return pluginCommandManifest{}, fmt.Errorf("manifest name %q, want %q", m.Name, wantName)
	}
	return m, nil
}

// truncatedStderrSuffix is appended to a probe failure message when the
// plugin's stderr exceeded the bounded buffer (M2).
const truncatedStderrSuffix = "[stderr truncated]"

// maxProbeStderrMessage caps the stderr content embedded in a probe
// failure message (M2): the buffer bounds memory at
// pluginMaxManifestSize, but the warning itself must stay small — a
// hostile plugin must not control the CLI's warning output size.
const maxProbeStderrMessage = 200

// pluginDispatchEnv is the bounded environment whitelist granted to a
// plugin subprocess — the CLI-side mirror of the eka-core runner's
// pluginEnv contract (PATH, HOME, EKA_PLUGIN_DIR, SystemRoot): an
// explicit allow-list, never a denylist. Everything else — notably
// credentials such as GH_TOKEN, SSH_AUTH_SOCK and cloud-provider
// variables — is deliberately NOT inherited: a plugin binary must
// never see the CLI user's secrets.
func pluginDispatchEnv() []string {
	env := []string{"PATH=" + os.Getenv("PATH")}
	for _, key := range []string{"HOME", "EKA_PLUGIN_DIR", "SystemRoot"} {
		if v := os.Getenv(key); v != "" {
			env = append(env, key+"="+v)
		}
	}
	return env
}

// pluginLimitedBuffer writes into an internal buffer up to
// pluginMaxManifestSize bytes; further writes are counted as overflow
// and refused (the plugin is killed via SIGPIPE when the pipe drains),
// so a spewing plugin cannot exhaust memory. Mirrors the eka-core
// runner's limitedBuffer.
type pluginLimitedBuffer struct {
	buf      bytes.Buffer
	overflow bool
}

func (b *pluginLimitedBuffer) Write(p []byte) (int, error) {
	if b.overflow {
		return len(p), nil
	}
	remaining := pluginMaxManifestSize - b.buf.Len()
	if remaining <= 0 {
		b.overflow = true
		return len(p), nil
	}
	if len(p) > remaining {
		b.overflow = true
		b.buf.Write(p[:remaining])
		return len(p), nil
	}
	b.buf.Write(p)
	return len(p), nil
}

// newPluginDispatchCommand builds the cobra command for one registered
// plugin command: it dispatches to the plugin executable under the
// bounded env and args contract and propagates the plugin's exit code.
// A non-empty refusal return means the command was NOT registered
// (invalid name or a collision with a built-in or another plugin's
// command — deterministic, first-wins in sorted order).
//
// Help is a CLI-surface concern, not plugin behavior (ADR-031: the
// command surface is not the behavior layer): a help-only invocation
// (-h, --help, help as the only argument) renders the native styled
// help instead of dispatching, so `eka <cmd> -h` matches every other
// command's help design. Any other argument — including deeper help
// like `<cmd> serve -h` — passes through to the plugin unchanged.
func newPluginDispatchCommand(root *cobra.Command, exe, pluginName string, spec pluginCommandSpec, expected string) (*cobra.Command, string) {
	if !validPluginCommandName(spec.Name) {
		return nil, fmt.Sprintf("plugin %q declares command %q, which is not a valid command name (want lowercase letters, digits and dashes) — not registered", pluginName, spec.Name)
	}
	if existing, taken := pluginCommandNameTaken(root, spec.Name); taken {
		return nil, fmt.Sprintf("plugin %q command %q collides with the existing %q command — not registered", pluginName, spec.Name, existing)
	}
	description := sanitizeTerminal(spec.Description)
	longBase := fmt.Sprintf(`%s

This command is proxied to the installed %q plugin executable. Every
dispatch re-verifies the binary against its recorded install checksum,
runs it with a bounded environment whitelist, and propagates its exit
code. Arguments after the command name pass through to the plugin
unchanged — the plugin owns its flags.

Help-only forms (-h, --help) render this native help; everything else
is dispatched to the plugin.`, description, "eka-"+pluginName)
	// Disclosure for `eka mcp` (bug:mcp-help-subcommands-hidden): the
	// proxy help must list the plugin's actual subcommands so
	// `eka mcp -h` discloses the surface even though the subcommands are
	// not cobra subcommands but whole-binary proxy arguments.
	if spec.Name == "mcp" && pluginName == "mcp" {
		longBase += fmt.Sprintf("\n\nSubcommands (disclosure):\n  %s", strings.Join(mcpSubcommands, ", "))
	}
	cmd := &cobra.Command{
		Use:                spec.Name,
		Short:              description,
		GroupID:            groupPlugins,
		Long:               longBase,
		DisableFlagParsing: true, // the plugin owns its flags; everything after the command name passes through
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 && (args[0] == "-h" || args[0] == "--help" || args[0] == "help") {
				return cmd.Help()
			}
			return dispatchPluginCommand(cmd, exe, pluginName, spec, expected, args)
		},
	}
	root.AddCommand(cmd)
	return cmd, ""
}

// validPluginCommandName reports whether a plugin-declared command name
// is a safe cobra command name: lowercase letters, digits and dashes,
// starting with a letter. Anything else (uppercase, underscores,
// spaces, path separators, control characters) is refused — a
// self-reported command name must never be able to escape the command
// tree or collide ambiguously.
func validPluginCommandName(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
			if i == 0 {
				return false // must start with a letter.
			}
		case r == '-':
			if i == 0 {
				return false // must start with a letter.
			}
		default:
			return false
		}
	}
	return true
}

// pluginCommandNameTaken reports whether a command name is already
// taken on root: by a registered command (a built-in or a command
// registered earlier in this pass — plugins are processed in sorted
// order, so the first-wins outcome is deterministic) or by a cobra
// built-in that ExecuteC creates later (help, completion).
func pluginCommandNameTaken(root *cobra.Command, name string) (string, bool) {
	for _, c := range root.Commands() {
		if c.Name() == name {
			return c.Name(), true
		}
	}
	if name == helpCommandName || name == "completion" {
		return name, true
	}
	return "", false
}

// dispatchPluginCommand executes one registered plugin command: the
// G2 anti-TOCTOU hash verification (the binary's SHA-256 is recomputed
// at EVERY dispatch and compared against the checksum recorded at
// install — a binary swapped after registration refuses
// deterministically), then the dispatch itself: the plugin executable
// with the declared args + the user's args, under the bounded env
// whitelist, with the CLI's streams inherited and the plugin's exit
// code propagated.
func dispatchPluginCommand(cmd *cobra.Command, exe, pluginName string, spec pluginCommandSpec, expected string, args []string) error {
	got, err := sha256File(exe)
	if err != nil {
		return refuse(cmd, "plugin command refused: cannot hash %s: %s", exe, err)
	}
	if !strings.EqualFold(got, expected) {
		return refuse(cmd, "plugin command refused: %s no longer matches the checksum recorded at install (the binary changed); reinstall it with: eka plugin install %s", pluginName, pluginName)
	}
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	execArgs := append(append([]string{}, spec.Args...), args...)
	pc := exec.CommandContext(ctx, exe, execArgs...)
	pc.Env = pluginDispatchEnv()
	pc.Stdin = cmd.InOrStdin()
	pc.Stdout = cmd.OutOrStdout()
	pc.Stderr = cmd.ErrOrStderr()
	if err := pc.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			// The plugin's exit code propagates (a signal death maps to
			// the generic failure code).
			code := ee.ExitCode()
			if code < 0 {
				code = exitFail
			}
			return &exitError{code: code}
		}
		return fmt.Errorf("plugin command %q failed: %w", spec.Name, err)
	}
	return nil
}

// pluginRegWarn prints a deterministic registration warning to the
// root's stderr, memoized per process (keyed by the entry path) so
// repeated root constructions never repeat it.
func pluginRegWarn(entry *pluginRegEntry) {
	if entry == nil || entry.refusal == "" {
		return
	}
	pluginRegWarnKey(entry.path, entry.refusal)
}

// pluginRegWarnKey reports a deterministic registration warning,
// memoized per process (keyed by key) so repeated root constructions
// never repeat it. The warning is collected and flushed by Execute
// after the root's streams are set — registration runs during root
// construction, before the caller's stderr exists.
func pluginRegWarnKey(key, msg string) {
	pluginRegMu.Lock()
	if pluginRegWarned[key] {
		pluginRegMu.Unlock()
		return
	}
	pluginRegWarned[key] = true
	pluginRegWarnings = append(pluginRegWarnings, msg)
	pluginRegMu.Unlock()
}

// flushPluginRegWarnings writes the collected registration warnings to
// w (the caller's stderr) in collection order. Called by Execute after
// the root's streams are set; the memo prevents duplicates across
// repeated constructions.
func flushPluginRegWarnings(w io.Writer) {
	pluginRegMu.Lock()
	pending := pluginRegWarnings
	pluginRegWarnings = nil
	pluginRegMu.Unlock()
	for _, msg := range pending {
		fmt.Fprintf(w, "eka: warning: %s\n", msg)
	}
}

// --- checksum sidecar (G2) -------------------------------------------

// pluginSidecarPath is the checksum sidecar of an installed plugin:
// <dir>/.eka-<name>.sha256. The leading dot keeps it invisible to the
// eka-* executable scans (Discover, plugin list, registration).
func pluginSidecarPath(dir, name string) string {
	return filepath.Join(dir, ".eka-"+name+".sha256")
}

// writePluginChecksum records the SHA-256 of the installed binary in
// the sidecar (atomic: temp + rename). Called at install/update
// finalize; the sidecar is the dispatch-time verification record (G2
// anti-TOCTOU) and the reinstall invalidation trigger for the
// registration cache.
//
// fsync before rename: the sidecar is the G2 verification record — a
// rename that lands before the data is durable can leave a stale
// sidecar behind a crash, so the temp file is fsync'd (data + metadata)
// before the atomic rename.
func writePluginChecksum(dir, name, sum string) error {
	path := pluginSidecarPath(dir, name)
	tmp, err := os.CreateTemp(dir, ".eka-"+name+".sha256-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	fail := func(err error) error {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if _, err := tmp.WriteString(sum + "\n"); err != nil {
		return fail(err)
	}
	if err := tmp.Sync(); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return err
	}
	// F4 (security review): fsync the directory best-effort so the
	// rename itself is durable — the file fsync above covers the data,
	// but the directory entry needs its own sync on crash-consistent
	// filesystems. Best-effort: a directory fsync failure must never
	// fail the install.
	if d, err := os.Open(dir); err == nil {
		d.Sync()
		d.Close()
	}
	return nil
}

// readPluginChecksum reads the sidecar of an installed plugin. A
// missing or malformed sidecar is (false, nil) — the caller treats the
// plugin as unverifiable (fail-closed: no commands register, dispatch
// never runs an unverified binary).
func readPluginChecksum(dir, name string) (string, bool) {
	b, err := os.ReadFile(pluginSidecarPath(dir, name))
	if err != nil {
		return "", false
	}
	sum := strings.ToLower(strings.TrimSpace(string(b)))
	if len(sum) != 64 {
		return "", false
	}
	if _, err := hex.DecodeString(sum); err != nil {
		return "", false
	}
	return sum, true
}

// removePluginChecksum removes the sidecar of an installed plugin
// (best-effort: debris cleanup, never a refusal).
func removePluginChecksum(dir, name string) {
	os.Remove(pluginSidecarPath(dir, name))
}
