//go:build unix

// Process control for Unix-like systems. Kept in a build-tagged file so the
// Windows taskkill path never leaks into Unix builds.
package env

import (
	"os"
	"os/exec"
	"syscall"
)

var (
	syscallENOTDIR = syscall.ENOTDIR
	syscallEISDIR  = syscall.EISDIR
	syscallEINVAL  = syscall.EINVAL
)

// setProcessGroup starts the shell in its own process group so a cancellation
// can terminate the shell and every descendant in one signal.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcessTree kills the process group led by pid, falling back to the
// process itself when the group no longer exists.
func killProcessTree(pid int) {
	if pid <= 0 {
		return
	}
	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
}

// exitCodeFromState maps a process state to the upstream exit code convention.
// A signal-killed process has no exit code, so it maps to 128 + signal.
func exitCodeFromState(state *os.ProcessState) int {
	if state == nil {
		return 1
	}
	if status, ok := state.Sys().(syscall.WaitStatus); ok {
		if status.Signaled() {
			return 128 + int(status.Signal())
		}
		return status.ExitStatus()
	}
	return state.ExitCode()
}
