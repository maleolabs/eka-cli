//go:build !windows

package cmd

import (
	"os/exec"
	"syscall"
)

// probeKillGroup configures the registration probe subprocess to die as
// a process group (F1, sto:mcp-command-registration): the probe's
// deadline (pluginProbeTimeout) must bound the whole probe, not just
// the direct child. A pipe-inheriting grandchild — a daemonized
// sidecar, a backgrounded subshell — holds the probe's stdout/stderr
// write-ends open, so exec.Wait() would block past the deadline and
// wedge every eka invocation (registration runs at every root
// construction).
//
// Two cases are bounded:
//
//   - the direct child never exits: the context fires, the Cancel hook
//     kills the whole group (-pid, Setpgid makes the probe its own
//     group leader), the pipe write-ends close and Run() returns
//     within the deadline (acceptance criterion: probe timeout <= 2s —
//     a hung plugin is killed, not waited on);
//   - the direct child exits cleanly but a grandchild holds the pipes:
//     exec's watchCtx goroutine has already returned (the process
//     exited before the deadline), so Cancel never fires — the
//     WaitDelay set in probePluginManifest bounds this case instead:
//     the pipes are forcibly closed and Run() returns ErrWaitDelay.
func probeKillGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
}

// probeKillGroupRemnants reaps any process-group member that survived
// the probe run (F1): a clean-exit plugin may have left a background
// child in the group (the ErrWaitDelay case — Cancel never fired), and
// that child must not leak past the probe. Best-effort: a no-op when
// the group is already empty (ESRCH) or the command never started.
func probeKillGroupRemnants(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
