package mcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"reflect"
	"sync"
	"syscall"
	"time"
)

const (
	defaultMaxStderrBytes = 64 * 1024
	defaultCloseTimeout   = 2 * time.Second
	stdinCloseGrace       = 500 * time.Millisecond
)

// StdioOptions configures a StdioTransport.
type StdioOptions struct {
	// Cwd is the working directory for the child process.
	Cwd string
	// Env holds KEY=value pairs layered over the inherited environment. When
	// InheritEnv is false, only Env is passed.
	Env []string
	// InheritEnv controls whether the parent environment is inherited. The
	// default is true.
	InheritEnv *bool
	// Stderr is "pipe" (default) or "inherit".
	Stderr string
	// OnStderr receives stderr chunks as they arrive.
	OnStderr func(string)
	// MaxMessageBytes bounds one stdout message. Defaults to
	// DefaultMaxMessageBytes.
	MaxMessageBytes int
	// MaxStderrBytes bounds the retained stderr tail. Defaults to 64 KiB.
	MaxStderrBytes int
	// CloseTimeout is how long the child gets after SIGTERM before SIGKILL.
	// Defaults to 2s.
	CloseTimeout time.Duration
}

// StdioTransport runs an MCP server as a child process and speaks
// newline-delimited JSON-RPC over its stdin and stdout.
type StdioTransport struct {
	TransportEvents

	command string
	args    []string
	options StdioOptions

	mu       sync.Mutex
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	started  bool
	closed   bool
	stderr   string
	exited   chan struct{}
	exitOnce sync.Once
	writeMu  sync.Mutex
}

// NewStdioTransport creates a stdio transport. The command is started by
// Client.Connect.
func NewStdioTransport(command string, args []string, options *StdioOptions) Transport {
	var resolved StdioOptions
	if options != nil {
		resolved = *options
	}
	return &StdioTransport{
		command: command,
		args:    append([]string{}, args...),
		options: resolved,
	}
}

// PID returns the child process id, or 0 before start and after exit.
func (t *StdioTransport) PID() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.cmd == nil || t.cmd.Process == nil {
		return 0
	}
	return t.cmd.Process.Pid
}

// Stderr returns the retained tail of the child's stderr.
func (t *StdioTransport) Stderr() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.stderr
}

// Start spawns the child process.
func (t *StdioTransport) Start() error {
	t.mu.Lock()
	if t.started {
		t.mu.Unlock()
		return errors.New("MCP stdio transport already started")
	}
	if t.closed {
		t.mu.Unlock()
		return NewConnectionClosedError("")
	}
	t.started = true
	t.exited = make(chan struct{})
	t.mu.Unlock()

	cmd := exec.Command(t.command, t.args...)
	cmd.Dir = t.options.Cwd
	cmd.Env = t.environment()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var stderr io.ReadCloser
	if t.options.Stderr == "inherit" {
		cmd.Stderr = os.Stderr
	} else {
		stderr, err = cmd.StderrPipe()
		if err != nil {
			return err
		}
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	configureProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	t.mu.Lock()
	t.cmd = cmd
	t.stdin = stdin
	t.mu.Unlock()

	go t.readStdout(stdout)
	if stderr != nil {
		go t.readStderr(stderr)
	}
	go func() {
		_ = cmd.Wait()
		t.mu.Lock()
		t.cmd = nil
		t.stdin = nil
		t.mu.Unlock()
		t.exitOnce.Do(func() { close(t.exited) })
		t.EmitClose()
	}()
	return nil
}

// Send writes one newline-delimited message to the child's stdin.
func (t *StdioTransport) Send(message *Message) error {
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	t.mu.Lock()
	stdin := t.stdin
	started := t.started
	closed := t.closed
	t.mu.Unlock()
	if !started || closed || stdin == nil {
		return NewConnectionClosedError("")
	}
	data, err := json.Marshal(message)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	_, err = stdin.Write(data)
	return err
}

// Close shuts stdin, then escalates to SIGTERM and SIGKILL within bounded time.
func (t *StdioTransport) Close() error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}
	t.closed = true
	cmd := t.cmd
	stdin := t.stdin
	exited := t.exited
	t.mu.Unlock()

	if stdin != nil {
		_ = stdin.Close()
	}
	if cmd == nil || exited == nil {
		t.EmitClose()
		return nil
	}

	closeTimeout := t.options.CloseTimeout
	if closeTimeout <= 0 {
		closeTimeout = defaultCloseTimeout
	}
	grace := stdinCloseGrace
	if closeTimeout < grace {
		grace = closeTimeout
	}
	termTimer := time.AfterFunc(grace, func() { terminateProcessTree(cmd) })
	killTimer := time.AfterFunc(grace+closeTimeout, func() { killProcessTree(cmd) })
	defer termTimer.Stop()
	defer killTimer.Stop()

	select {
	case <-exited:
		// The process is gone; reap any children it left behind.
		terminateProcessTree(cmd)
	case <-time.After(grace + closeTimeout + 2*time.Second):
		killProcessTree(cmd)
	}
	return nil
}

func (t *StdioTransport) environment() []string {
	if t.options.InheritEnv != nil && !*t.options.InheritEnv {
		return append([]string{}, t.options.Env...)
	}
	env := os.Environ()
	return append(env, t.options.Env...)
}

func (t *StdioTransport) readStdout(reader io.Reader) {
	maxBytes := t.options.MaxMessageBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxMessageBytes
	}
	buffer := make([]byte, 0, 8192)
	chunk := make([]byte, 32*1024)
	for {
		n, err := reader.Read(chunk)
		if n > 0 {
			buffer = append(buffer, chunk[:n]...)
			for {
				index := bytes.IndexByte(buffer, '\n')
				if index < 0 {
					break
				}
				line := append([]byte(nil), buffer[:index]...)
				buffer = append([]byte(nil), buffer[index+1:]...)
				t.handleStdoutLine(line, maxBytes)
			}
			if len(buffer) > maxBytes {
				buffer = buffer[:0]
				t.EmitError(fmt.Errorf("MCP stdio message exceeds %d bytes", maxBytes))
			}
		}
		if err != nil {
			break
		}
	}
	if len(bytes.TrimSpace(buffer)) > 0 {
		t.EmitError(errors.New("MCP stdio server closed with an incomplete JSON-RPC message"))
	}
}

func (t *StdioTransport) handleStdoutLine(line []byte, maxBytes int) {
	line = bytes.TrimSuffix(line, []byte("\r"))
	if len(bytes.TrimSpace(line)) == 0 {
		return
	}
	if len(line) > maxBytes {
		t.EmitError(fmt.Errorf("MCP stdio message exceeds %d bytes", maxBytes))
		return
	}
	message, err := ParseMessage(line)
	if err != nil {
		t.EmitError(err)
		return
	}
	t.EmitMessage(message)
}

func (t *StdioTransport) readStderr(reader io.Reader) {
	maxBytes := t.options.MaxStderrBytes
	if maxBytes <= 0 {
		maxBytes = defaultMaxStderrBytes
	}
	chunk := make([]byte, 32*1024)
	for {
		n, err := reader.Read(chunk)
		if n > 0 {
			text := string(chunk[:n])
			t.mu.Lock()
			t.stderr += text
			if len(t.stderr) > maxBytes {
				t.stderr = t.stderr[len(t.stderr)-maxBytes:]
			}
			t.mu.Unlock()
			if t.options.OnStderr != nil {
				t.options.OnStderr(text)
			}
		}
		if err != nil {
			return
		}
	}
}

// configureProcessGroup places the child in its own process group on POSIX so
// the whole tree can be signalled, matching the upstream `detached` spawn. The
// platform-specific SysProcAttr field is set reflectively so the same file
// compiles on platforms without process groups.
func configureProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	value := reflect.ValueOf(cmd.SysProcAttr).Elem()
	field := value.FieldByName("Setpgid")
	if field.IsValid() && field.CanSet() && field.Kind() == reflect.Bool {
		field.SetBool(true)
	}
}

// terminateProcessTree sends SIGTERM to the child's process group when the
// platform supports it, falling back to the child itself.
func terminateProcessTree(cmd *exec.Cmd) {
	signalProcessTree(cmd, syscall.SIGTERM, false)
}

// killProcessTree sends SIGKILL to the child's process group when the platform
// supports it, falling back to a hard kill of the child.
func killProcessTree(cmd *exec.Cmd) {
	signalProcessTree(cmd, syscall.SIGKILL, true)
}

// signalProcessTree signals the child's process group by reinterpreting the
// (positive) child pid as the negated process-group id. os.FindProcess wraps the
// id without validating it on POSIX, so Process.Signal becomes kill(-pgid, sig);
// on platforms where that fails the direct child is signalled instead.
func signalProcessTree(cmd *exec.Cmd, signal syscall.Signal, hard bool) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	pid := cmd.Process.Pid
	if group, err := os.FindProcess(-pid); err == nil {
		if group.Signal(signal) == nil {
			return
		}
	}
	if hard {
		_ = cmd.Process.Kill()
		return
	}
	_ = cmd.Process.Signal(signal)
}
