//go:build windows

package cmd

import "os/exec"

// probeKillGroup is a no-op on windows: Setpgid is not available in the
// stdlib syscall package there, and job objects are out of stdlib
// scope. The residual risk — a pipe-inheriting grandchild holding the
// probe's stdout/stderr write-ends open past the deadline — is bounded
// by the WaitDelay set in probePluginManifest (the pipes are forcibly
// closed and Run() returns ErrWaitDelay); the direct child is still
// killed by the default exec.CommandContext Cancel.
func probeKillGroup(cmd *exec.Cmd) {}

// probeKillGroupRemnants is a no-op on windows (no process-group kill
// available in the stdlib): a grandchild that survived the probe run
// cannot be reaped here — documented residual.
func probeKillGroupRemnants(cmd *exec.Cmd) {}
