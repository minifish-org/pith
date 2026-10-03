package env

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// localNamespaceID is shared by every Local instance. Files at a path are the
// same file for every local environment whatever its CWD; each local process is
// one namespace.
const localNamespaceID = "local"

// LocalOptions configures a Local environment.
type LocalOptions struct {
	// CWD is the working directory relative paths resolve against.
	CWD string
	// ShellPath overrides bash auto-detection.
	ShellPath string
	// ShellEnv is layered over the inherited process environment before a
	// per-command Env override.
	ShellEnv map[string]string
}

// Local is the ordinary local filesystem and shell implementation of
// ExecutionEnv. It is safe for concurrent use.
type Local struct {
	id string

	mu        sync.Mutex
	cwd       string
	shellPath string
	shellEnv  map[string]string

	activePids map[int]struct{}
	tempPaths  []string

	// createTempFileHook and onSpillWritten are unexported test seams mirroring
	// the upstream subclass and vitest mocks. They are nil in production.
	createTempFileHook func(context.Context, TempFileOptions) (string, error)
	onSpillWritten     func(string)
}

// NewLocal builds a Local environment rooted at cwd.
func NewLocal(cwd string) (*Local, error) {
	return NewLocalWithOptions(LocalOptions{CWD: cwd})
}

// NewLocalWithOptions builds a Local environment with an optional shell
// override and configured shell environment.
func NewLocalWithOptions(options LocalOptions) (*Local, error) {
	cwd := options.CWD
	if cwd == "" {
		if working, err := os.Getwd(); err == nil {
			cwd = working
		}
	}
	return &Local{
		id:         localNamespaceID,
		cwd:        filepath.Clean(cwd),
		shellPath:  options.ShellPath,
		shellEnv:   cloneStringMap(options.ShellEnv),
		activePids: map[int]struct{}{},
	}, nil
}

// ID reports the shared local namespace identity.
func (l *Local) ID() string { return l.id }

// CWD reports the current working directory.
func (l *Local) CWD() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.cwd
}

// SetCWD changes the working directory used to resolve relative paths.
func (l *Local) SetCWD(cwd string) error {
	l.mu.Lock()
	l.cwd = filepath.Clean(cwd)
	l.mu.Unlock()
	return nil
}

// currentCWD reads the working directory without taking the lock twice.
func (l *Local) currentCWD() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.cwd
}

// resolve resolves an input path against the environment working directory.
func (l *Local) resolve(path string) string { return resolvePath(l.currentCWD(), path) }

func (l *Local) trackChild(pid int) {
	if pid <= 0 {
		return
	}
	l.mu.Lock()
	l.activePids[pid] = struct{}{}
	l.mu.Unlock()
}

func (l *Local) untrackChild(pid int) {
	if pid <= 0 {
		return
	}
	l.mu.Lock()
	delete(l.activePids, pid)
	l.mu.Unlock()
}

func (l *Local) trackTemp(path string) {
	if path == "" {
		return
	}
	l.mu.Lock()
	l.tempPaths = append(l.tempPaths, path)
	l.mu.Unlock()
}

// Cleanup terminates tracked shell processes and removes only the temporary
// paths created by this environment. It is best effort and idempotent.
func (l *Local) Cleanup(_ context.Context) error {
	l.mu.Lock()
	pids := make([]int, 0, len(l.activePids))
	for pid := range l.activePids {
		pids = append(pids, pid)
	}
	l.activePids = map[int]struct{}{}
	paths := l.tempPaths
	l.tempPaths = nil
	l.mu.Unlock()
	for _, pid := range pids {
		killProcessTree(pid)
	}
	for _, path := range paths {
		_ = os.RemoveAll(path)
	}
	return nil
}

// resolvePath expands home-relative paths and file URLs, then makes the result
// absolute against cwd. It never touches the filesystem.
func resolvePath(cwd, path string) string {
	normalized := path
	switch {
	case normalized == "~":
		normalized = homeDir()
	case strings.HasPrefix(normalized, "~/"):
		normalized = filepath.Join(homeDir(), normalized[2:])
	case runtime.GOOS == "windows" && strings.HasPrefix(normalized, "~\\"):
		normalized = filepath.Join(homeDir(), normalized[2:])
	case strings.HasPrefix(normalized, "file://"):
		if converted, ok := fileURLToPath(normalized); ok {
			normalized = converted
		}
	}
	if filepath.IsAbs(normalized) {
		return filepath.Clean(normalized)
	}
	return filepath.Join(cwd, normalized)
}

func homeDir() string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return home
	}
	return os.Getenv("HOME")
}

// fileURLToPath converts a file:// URL to a host path. Malformed URLs are left
// alone so filesystem methods keep their non-throwing contract.
func fileURLToPath(raw string) (string, bool) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "file" {
		return "", false
	}
	if parsed.Host != "" && parsed.Host != "localhost" {
		return "//" + parsed.Host + parsed.Path, true
	}
	decoded, err := url.PathUnescape(parsed.Path)
	if err != nil {
		return "", false
	}
	if runtime.GOOS == "windows" && len(decoded) >= 3 && decoded[0] == '/' && decoded[2] == ':' {
		decoded = decoded[1:]
	}
	return decoded, true
}

// randomUUID renders a version-4 UUID from the system entropy source.
func randomUUID() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	buf[6] = (buf[6] & 0x0f) | 0x40
	buf[8] = (buf[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(buf)
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32]
}

func cloneStringMap(source map[string]string) map[string]string {
	if source == nil {
		return nil
	}
	clone := make(map[string]string, len(source))
	for key, value := range source {
		clone[key] = value
	}
	return clone
}

// setProcessGroup places the shell in its own process group on POSIX so a
// cancellation can terminate the shell and every descendant with one signal.
// The field is set reflectively so this single file compiles for Windows,
// whose SysProcAttr has no Setpgid.
func setProcessGroup(cmd *exec.Cmd) {
	attr := &syscall.SysProcAttr{}
	if runtime.GOOS != "windows" {
		if field := reflect.ValueOf(attr).Elem().FieldByName("Setpgid"); field.IsValid() && field.Kind() == reflect.Bool && field.CanSet() {
			field.SetBool(true)
		}
	}
	cmd.SysProcAttr = attr
}

// killProcessTree kills pid and its descendants. On POSIX it signals the
// process group created by setProcessGroup and falls back to the process; on
// Windows it delegates to taskkill /T.
func killProcessTree(pid int) {
	if pid <= 0 {
		return
	}
	if runtime.GOOS == "windows" {
		killWindowsProcessTree(pid)
		return
	}
	if process, err := os.FindProcess(-pid); err == nil {
		if err := process.Signal(os.Kill); err == nil {
			return
		}
	}
	if process, err := os.FindProcess(pid); err == nil {
		_ = process.Signal(os.Kill)
	}
}

func killWindowsProcessTree(pid int) {
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

// exitCodeFromState maps a finished process to an exit code. A signal-killed
// process has no exit code, so it maps to the conventional 128+signal.
func exitCodeFromState(state *os.ProcessState) int {
	if state == nil {
		return 1
	}
	if wait, ok := state.Sys().(syscall.WaitStatus); ok {
		if wait.Signaled() {
			return 128 + int(wait.Signal())
		}
		return wait.ExitStatus()
	}
	return state.ExitCode()
}

var _ ExecutionEnv = (*Local)(nil)
var _ FileSystem = (*Local)(nil)
var _ Shell = (*Local)(nil)
