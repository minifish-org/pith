//go:build windows

// Process control for Windows. taskkill terminates the shell and its
// descendants; the process group helpers used on Unix are no-ops here.
package env

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
)

var (
	syscallENOTDIR = errors.New("not a directory")
	syscallEISDIR  = errors.New("is a directory")
	syscallEINVAL  = errors.New("invalid argument")
)

// setProcessGroup is a no-op on Windows; taskkill /T walks the child tree.
func setProcessGroup(_ *exec.Cmd) {}

// killProcessTree terminates pid and all of its descendants.
func killProcessTree(pid int) {
	if pid <= 0 {
		return
	}
	systemRoot := os.Getenv("SystemRoot")
	if systemRoot == "" {
		systemRoot = `C:\Windows`
	}
	taskkill := filepath.Join(systemRoot, "System32", "taskkill.exe")
	cmd := exec.Command(taskkill, "/F", "/T", "/PID", strconv.Itoa(pid))
	if err := cmd.Start(); err == nil && cmd.Process != nil {
		_ = cmd.Process.Release()
	}
}

// exitCodeFromState returns the process exit code on Windows.
func exitCodeFromState(state *os.ProcessState) int {
	if state == nil {
		return 1
	}
	return state.ExitCode()
}
