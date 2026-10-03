package env

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	maxTimeoutMs      = 2_147_483_647
	maxTimeoutSeconds = maxTimeoutMs / 1000
	// exitStdioGrace bounds how long Exec waits for the output pipe to drain
	// after the shell exits. A descendant holding inherited stdio open must not
	// delay settlement indefinitely.
	exitStdioGrace = 100 * time.Millisecond
)

// resolveTimeout validates the requested duration. A zero duration requests no
// timeout; a negative or oversized duration is invalid.
func resolveTimeout(timeout time.Duration) (*time.Duration, *ExecutionError) {
	if timeout == 0 {
		return nil, nil
	}
	if timeout < 0 {
		return nil, executionError(ExecutionErrorTimeout, errors.New("Invalid timeout: must be a finite number of seconds"))
	}
	if timeout > time.Duration(maxTimeoutMs)*time.Millisecond {
		return nil, executionError(ExecutionErrorTimeout, fmt.Errorf("Invalid timeout: maximum is %d seconds", maxTimeoutSeconds))
	}
	resolved := timeout
	return &resolved, nil
}

type shellConfig struct {
	shell            string
	args             []string
	commandFromStdin bool
}

var legacyWSLBashPattern = regexp.MustCompile(`^[a-z]:\\windows\\(?:system32|sysnative)\\bash\.exe$`)

func isLegacyWSLBashPath(path string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(path, "/", "\\"))
	return legacyWSLBashPattern.MatchString(normalized)
}

func getBashShellConfig(shell string) shellConfig {
	if isLegacyWSLBashPath(shell) {
		return shellConfig{shell: shell, args: []string{"-s"}, commandFromStdin: true}
	}
	return shellConfig{shell: shell, args: []string{"-c"}}
}

// runProbeOutput runs a short helper command and returns its combined output.
func runProbeOutput(command string, args []string, timeout time.Duration) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	output, err := exec.CommandContext(ctx, command, args...).Output()
	if err != nil {
		return "", false
	}
	return string(output), true
}

func findBashOnPath() (string, bool) {
	var stdout string
	var ok bool
	if runtime.GOOS == "windows" {
		stdout, ok = runProbeOutput("where", []string{"bash.exe"}, 5*time.Second)
	} else {
		stdout, ok = runProbeOutput("which", []string{"bash"}, 5*time.Second)
	}
	if !ok {
		return "", false
	}
	lines := strings.Split(strings.ReplaceAll(strings.TrimSpace(stdout), "\r\n", "\n"), "\n")
	if len(lines) == 0 || lines[0] == "" {
		return "", false
	}
	if !pathExists(lines[0]) {
		return "", false
	}
	return lines[0], true
}

// shellConfigFor resolves the shell to run, honoring an explicit override.
func (l *Local) shellConfigFor() (shellConfig, *ExecutionError) {
	if l.shellPath != "" {
		if pathExists(l.shellPath) {
			return getBashShellConfig(l.shellPath), nil
		}
		return shellConfig{}, executionError(ExecutionErrorShellUnavailable, fmt.Errorf("Custom shell path not found: %s", l.shellPath))
	}
	if runtime.GOOS == "windows" {
		candidates := []string{}
		if programFiles := os.Getenv("ProgramFiles"); programFiles != "" {
			candidates = append(candidates, filepath.Join(programFiles, "Git", "bin", "bash.exe"))
		}
		if programFilesX86 := os.Getenv("ProgramFiles(x86)"); programFilesX86 != "" {
			candidates = append(candidates, filepath.Join(programFilesX86, "Git", "bin", "bash.exe"))
		}
		for _, candidate := range candidates {
			if pathExists(candidate) {
				return getBashShellConfig(candidate), nil
			}
		}
		if bash, ok := findBashOnPath(); ok {
			return getBashShellConfig(bash), nil
		}
		return shellConfig{}, executionError(ExecutionErrorShellUnavailable, errors.New("No bash shell found. Configure an explicit shell path"))
	}
	if pathExists("/bin/bash") {
		return getBashShellConfig("/bin/bash"), nil
	}
	if bash, ok := findBashOnPath(); ok {
		return getBashShellConfig(bash), nil
	}
	return shellConfig{shell: "sh", args: []string{"-c"}}, nil
}

// mergeShellEnv layers baseEnv over the inherited process environment and
// extraEnv over both. With inherit=false only extraEnv is used, matching the
// upstream getShellEnv contract.
func mergeShellEnv(baseEnv map[string]string, extraEnv map[string]string, inherit bool) []string {
	merged := map[string]string{}
	if inherit {
		for _, entry := range os.Environ() {
			if key, value, found := strings.Cut(entry, "="); found {
				merged[key] = value
			}
		}
		for key, value := range baseEnv {
			merged[key] = value
		}
	}
	for key, value := range extraEnv {
		merged[key] = value
	}
	entries := make([]string, 0, len(merged))
	for key, value := range merged {
		entries = append(entries, key+"="+value)
	}
	return entries
}

// execRun carries the mutable outcome of one Exec across its goroutines.
type execRun struct {
	mu          sync.Mutex
	childPid    int
	timedOut    bool
	callbackErr error
	spillErr    error

	// spill is owned entirely by the output reader goroutine.
	spill *spillRun
}

type spillRun struct {
	path         string
	file         *os.File
	started      bool
	prefixes     [][]byte
	seenBytes    int
	seenNewlines int
}

func (r *execRun) setChild(pid int) {
	r.mu.Lock()
	r.childPid = pid
	r.mu.Unlock()
}

func (r *execRun) child() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.childPid
}

func (r *execRun) kill() { killProcessTree(r.child()) }

func (r *execRun) setTimedOut() {
	r.mu.Lock()
	r.timedOut = true
	r.mu.Unlock()
}

func (r *execRun) didTimeOut() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.timedOut
}

func (r *execRun) failCallback(err error) {
	r.mu.Lock()
	if r.callbackErr == nil {
		r.callbackErr = err
	}
	r.mu.Unlock()
}

func (r *execRun) callbackError() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.callbackErr
}

func (r *execRun) failSpill(err error) {
	r.mu.Lock()
	if r.spillErr == nil {
		r.spillErr = err
	}
	r.mu.Unlock()
}

func (r *execRun) spillError() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.spillErr
}

func (r *execRun) setSpill(spill *spillRun) {
	r.mu.Lock()
	r.spill = spill
	r.mu.Unlock()
}

func (r *execRun) spillState() *spillRun {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.spill
}

// invokeOutputCallback calls the caller's sink, converting a panic into an
// ordinary error so a misbehaving observer cannot crash the runtime.
func invokeOutputCallback(ctx context.Context, callback func(context.Context, []byte) error, chunk []byte) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			if recoveredErr, ok := recovered.(error); ok {
				err = recoveredErr
			} else {
				err = fmt.Errorf("%v", recovered)
			}
		}
	}()
	return callback(ctx, chunk)
}

// pushChunk emits a raw chunk to the caller and maintains the optional spill.
// It runs only on the output reader goroutine.
func (l *Local) pushChunk(run *execRun, options ExecOptions, ctx context.Context, chunk []byte) {
	if options.OnOutput != nil && run.callbackError() == nil {
		if err := invokeOutputCallback(ctx, options.OnOutput, chunk); err != nil {
			run.failCallback(err)
			run.kill()
		}
	}
	if options.Spill == nil || len(chunk) == 0 || run.spillError() != nil {
		return
	}
	spill := run.spillState()
	if spill == nil {
		spill = &spillRun{}
		run.setSpill(spill)
	}
	if spill.started {
		l.writeSpillChunk(run, spill, chunk)
		return
	}
	spill.seenBytes += len(chunk)
	for _, b := range chunk {
		if b == '\n' {
			spill.seenNewlines++
		}
	}
	lines := spill.seenNewlines
	if chunk[len(chunk)-1] != '\n' {
		lines++
	}
	if spill.seenBytes <= options.Spill.AfterBytes && lines <= options.Spill.AfterLines {
		spill.prefixes = append(spill.prefixes, chunk)
		return
	}
	if !l.startSpill(ctx, run, spill) {
		return
	}
	for _, prefix := range spill.prefixes {
		l.writeSpillChunk(run, spill, prefix)
	}
	spill.prefixes = nil
	l.writeSpillChunk(run, spill, chunk)
	if l.onSpillWritten != nil {
		l.onSpillWritten(spill.path)
	}
}

// startSpill lazily creates the complete-output file. It reports whether the
// spill is usable.
func (l *Local) startSpill(ctx context.Context, run *execRun, spill *spillRun) bool {
	path, err := l.CreateTempFile(ctx, TempFileOptions{Prefix: "pi-output-", Suffix: ".log"})
	if err != nil {
		run.failSpill(err)
		run.kill()
		return false
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		run.failSpill(err)
		run.kill()
		return false
	}
	spill.path = path
	spill.file = file
	spill.started = true
	return true
}

func (l *Local) writeSpillChunk(run *execRun, spill *spillRun, chunk []byte) {
	if spill.file == nil || len(chunk) == 0 {
		return
	}
	if _, err := spill.file.Write(chunk); err != nil {
		run.failSpill(err)
		run.kill()
	}
}

// Exec runs a shell command, streaming combined stdout and stderr to
// options.OnOutput and optionally spilling the complete raw stream.
func (l *Local) Exec(ctx context.Context, command string, options ExecOptions) (ExecResult, error) {
	if ctx.Err() != nil {
		return ExecResult{}, abortExecutionError(ctx)
	}
	timeout, timeoutErr := resolveTimeout(options.Timeout)
	if timeoutErr != nil {
		return ExecResult{}, timeoutErr
	}
	cwd := l.currentCWD()
	if options.CWD != "" {
		cwd = resolvePath(cwd, options.CWD)
	}
	if info, err := os.Stat(cwd); err != nil || !info.IsDir() {
		return ExecResult{}, executionError(ExecutionErrorSpawn, fmt.Errorf("Working directory does not exist: %s\nCannot execute bash commands", cwd))
	}
	config, shellErr := l.shellConfigFor()
	if shellErr != nil {
		return ExecResult{}, shellErr
	}

	var execCommand *exec.Cmd
	if config.commandFromStdin {
		execCommand = exec.Command(config.shell, config.args...)
		execCommand.Stdin = strings.NewReader(command)
	} else {
		execCommand = exec.Command(config.shell, append(append([]string{}, config.args...), command)...)
	}
	execCommand.Dir = cwd
	inherit := true
	if options.InheritEnv != nil {
		inherit = *options.InheritEnv
	}
	execCommand.Env = mergeShellEnv(l.shellEnv, options.Env, inherit)

	readEnd, writeEnd, pipeErr := os.Pipe()
	if pipeErr != nil {
		return ExecResult{}, executionError(ExecutionErrorUnknown, pipeErr)
	}
	execCommand.Stdout = writeEnd
	execCommand.Stderr = writeEnd
	setProcessGroup(execCommand)
	if startErr := execCommand.Start(); startErr != nil {
		_ = readEnd.Close()
		_ = writeEnd.Close()
		return ExecResult{}, executionError(ExecutionErrorSpawn, startErr)
	}
	_ = writeEnd.Close()
	pid := execCommand.Process.Pid
	run := &execRun{}
	run.setChild(pid)
	l.trackChild(pid)
	defer l.untrackChild(pid)

	var timer *time.Timer
	if timeout != nil {
		timer = time.AfterFunc(*timeout, func() {
			run.setTimedOut()
			run.kill()
		})
	}
	watchStop := make(chan struct{})
	if done := ctx.Done(); done != nil {
		go func() {
			select {
			case <-done:
				run.kill()
			case <-watchStop:
			}
		}()
	}

	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		buffer := make([]byte, 32*1024)
		for {
			read, readErr := readEnd.Read(buffer)
			if read > 0 {
				chunk := make([]byte, read)
				copy(chunk, buffer[:read])
				l.pushChunk(run, options, ctx, chunk)
			}
			if readErr != nil {
				return
			}
		}
	}()

	waitErr := execCommand.Wait()
	if timer != nil {
		timer.Stop()
	}
	close(watchStop)

	select {
	case <-readerDone:
	case <-time.After(exitStdioGrace):
		_ = readEnd.Close()
		<-readerDone
	}
	_ = readEnd.Close()

	spillPath := ""
	if spill := run.spillState(); spill != nil && spill.file != nil {
		_ = spill.file.Close()
		spill.file = nil
		spillPath = spill.path
	}

	if callbackErr := run.callbackError(); callbackErr != nil {
		return ExecResult{}, executionError(ExecutionErrorCallback, callbackErr)
	}
	if run.didTimeOut() {
		failure := executionError(ExecutionErrorTimeout, fmt.Errorf("timeout:%v", options.Timeout.Seconds()))
		failure.SpillPath = spillPath
		return ExecResult{}, failure
	}
	if ctx.Err() != nil {
		failure := abortExecutionError(ctx)
		failure.SpillPath = spillPath
		return ExecResult{}, failure
	}
	if spillErr := run.spillError(); spillErr != nil {
		return ExecResult{}, executionError(ExecutionErrorUnknown, fmt.Errorf("Failed to preserve complete shell output: %w", spillErr))
	}
	if waitErr != nil {
		var exitErr *exec.ExitError
		if !errors.As(waitErr, &exitErr) {
			return ExecResult{}, executionError(ExecutionErrorSpawn, waitErr)
		}
	}
	result := ExecResult{ExitCode: exitCodeFromState(execCommand.ProcessState), SpillPath: spillPath}
	return result, nil
}
