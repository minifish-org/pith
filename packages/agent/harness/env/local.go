// Package env is the Go port of packages/agent/src/harness/env/nodejs.ts.
//
// This is a Go port of Pi at revision
// f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// LocalExecutionEnv implements the harness FileSystem and Shell protocols with
// the Go standard library. Failures are encoded as Result values so backend
// errors never panic across the capability boundary. Process cancellation,
// timeouts and descendant reaping use the platform helpers in process_unix.go
// and process_windows.go.
package env

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	harnesstypes "github.com/minifish-org/pith/packages/agent/harness/types"
	outputcapture "github.com/minifish-org/pith/packages/agent/harness/utils/output_capture"
)

const (
	maxTimeoutMs      = 2_147_483_647
	maxTimeoutSeconds = maxTimeoutMs / 1000
	exitStdioGraceMs  = 100
)

// LocalExecutionEnvOptions configures a LocalExecutionEnv.
type LocalExecutionEnvOptions struct {
	// Cwd is the working directory against which relative paths resolve.
	Cwd string
	// ShellPath overrides bash auto-detection.
	ShellPath *string
	// ShellEnv is merged over the inherited process environment.
	ShellEnv map[string]string
}

// LocalExecutionEnv is the local filesystem and shell execution environment.
type LocalExecutionEnv struct {
	cwd       string
	shellPath *string
	shellEnv  map[string]string

	mu              sync.Mutex
	activeChildPids map[int]struct{}

	// createTempFileHook is a test seam mirroring the upstream subclass used to
	// simulate a failing spill file. It is nil in production.
	createTempFileHook func(options *harnesstypes.CreateTempFileOptions, ctx harnesstypes.Context) harnesstypes.Result[string, harnesstypes.FileError]
}

// NewLocalExecutionEnv builds an environment rooted at the supplied options.
func NewLocalExecutionEnv(options LocalExecutionEnvOptions) *LocalExecutionEnv {
	return &LocalExecutionEnv{
		cwd:             options.Cwd,
		shellPath:       options.ShellPath,
		shellEnv:        options.ShellEnv,
		activeChildPids: map[int]struct{}{},
	}
}

// Cwd returns the configured working directory.
func (e *LocalExecutionEnv) Cwd() string { return e.cwd }

func fileOk[T any](value T) harnesstypes.Result[T, harnesstypes.FileError] {
	return harnesstypes.Ok[T, harnesstypes.FileError](value)
}

func fileErr[T any](err *harnesstypes.FileError) harnesstypes.Result[T, harnesstypes.FileError] {
	return harnesstypes.Err[T, harnesstypes.FileError](*err)
}

func voidResult(err *harnesstypes.FileError) harnesstypes.Result[struct{}, harnesstypes.FileError] {
	if err != nil {
		return fileErr[struct{}](err)
	}
	return fileOk(struct{}{})
}

func execErr(err *harnesstypes.ExecutionError) harnesstypes.Result[harnesstypes.ShellExecResult, harnesstypes.ExecutionError] {
	return harnesstypes.Err[harnesstypes.ShellExecResult, harnesstypes.ExecutionError](*err)
}

func execOk(value harnesstypes.ShellExecResult) harnesstypes.Result[harnesstypes.ShellExecResult, harnesstypes.ExecutionError] {
	return harnesstypes.Ok[harnesstypes.ShellExecResult, harnesstypes.ExecutionError](value)
}

func stringPointer(value string) *string { return &value }

func abortResult[T any](ctx harnesstypes.Context, path *string) (harnesstypes.Result[T, harnesstypes.FileError], bool) {
	if ctx != nil && ctx.Err() != nil {
		return fileErr[T](harnesstypes.NewFileError(harnesstypes.FileErrorAborted, "aborted", path, nil)), true
	}
	return harnesstypes.Result[T, harnesstypes.FileError]{}, false
}

// resolveTimeoutMs validates the upstream seconds timeout and returns
// milliseconds. A nil result means no timeout was requested.
func resolveTimeoutMs(options *harnesstypes.ShellExecOptions) (*int, *harnesstypes.ExecutionError) {
	if options == nil || options.Timeout == nil {
		return nil, nil
	}
	timeout := *options.Timeout
	if timeout != timeout || timeout <= 0 || timeout > maxTimeoutSeconds {
		if timeout > maxTimeoutSeconds {
			return nil, harnesstypes.NewExecutionError(harnesstypes.ExecutionErrorTimeout, fmt.Sprintf("Invalid timeout: maximum is %d seconds", maxTimeoutSeconds), nil)
		}
		return nil, harnesstypes.NewExecutionError(harnesstypes.ExecutionErrorTimeout, "Invalid timeout: must be a finite number of seconds", nil)
	}
	ms := int(timeout * 1000)
	return &ms, nil
}

func homeDir() string {
	if home, err := os.UserHomeDir(); err == nil {
		return home
	}
	return os.Getenv("HOME")
}

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

// resolvePath mirrors the upstream home expansion and file URL handling.
func resolvePath(cwd string, path string) string {
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

func (e *LocalExecutionEnv) resolvePath(path string) string { return resolvePath(e.cwd, path) }

func fileKindFromInfo(info os.FileInfo) (harnesstypes.FileKind, bool) {
	mode := info.Mode()
	switch {
	case mode.IsRegular():
		return harnesstypes.FileKindFile, true
	case mode.IsDir():
		return harnesstypes.FileKindDirectory, true
	case mode&os.ModeSymlink != 0:
		return harnesstypes.FileKindSymlink, true
	default:
		return "", false
	}
}

func fileInfoFromInfo(path string, info os.FileInfo) (*harnesstypes.FileInfo, *harnesstypes.FileError) {
	kind, ok := fileKindFromInfo(info)
	if !ok {
		return nil, harnesstypes.NewFileError(harnesstypes.FileErrorInvalid, "Unsupported file type", stringPointer(path), nil)
	}
	return &harnesstypes.FileInfo{
		Name:    filepath.Base(path),
		Path:    path,
		Kind:    kind,
		Size:    int(info.Size()),
		MtimeMs: float64(info.ModTime().UnixNano()) / 1e6,
	}, nil
}

func toFileError(err error, fallbackPath *string) *harnesstypes.FileError {
	if err == nil {
		return harnesstypes.NewFileError(harnesstypes.FileErrorUnknown, "unknown error", fallbackPath, nil)
	}
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return harnesstypes.NewFileError(harnesstypes.FileErrorAborted, err.Error(), fallbackPath, err)
	case os.IsNotExist(err):
		return harnesstypes.NewFileError(harnesstypes.FileErrorNotFound, err.Error(), fallbackPath, err)
	case os.IsPermission(err):
		return harnesstypes.NewFileError(harnesstypes.FileErrorPermissionDenied, err.Error(), fallbackPath, err)
	}
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		switch {
		case errors.Is(pathErr.Err, syscallENOTDIR):
			return harnesstypes.NewFileError(harnesstypes.FileErrorNotDirectory, err.Error(), fallbackPath, err)
		case errors.Is(pathErr.Err, syscallEISDIR):
			return harnesstypes.NewFileError(harnesstypes.FileErrorIsDirectory, err.Error(), fallbackPath, err)
		case errors.Is(pathErr.Err, syscallEINVAL):
			return harnesstypes.NewFileError(harnesstypes.FileErrorInvalid, err.Error(), fallbackPath, err)
		}
	}
	return harnesstypes.NewFileError(harnesstypes.FileErrorUnknown, err.Error(), fallbackPath, err)
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func (e *LocalExecutionEnv) trackChild(pid int) {
	if pid <= 0 {
		return
	}
	e.mu.Lock()
	e.activeChildPids[pid] = struct{}{}
	e.mu.Unlock()
}

func (e *LocalExecutionEnv) untrackChild(pid int) {
	if pid <= 0 {
		return
	}
	e.mu.Lock()
	delete(e.activeChildPids, pid)
	e.mu.Unlock()
}

// AbsolutePath resolves a path relative to the environment working directory.
func (e *LocalExecutionEnv) AbsolutePath(path string, _ harnesstypes.Context) harnesstypes.Result[string, harnesstypes.FileError] {
	return fileOk(e.resolvePath(path))
}

// JoinPath joins path segments.
func (e *LocalExecutionEnv) JoinPath(parts []string, _ harnesstypes.Context) harnesstypes.Result[string, harnesstypes.FileError] {
	return fileOk(filepath.Join(parts...))
}

// ReadTextFile reads a complete UTF-8 text file.
func (e *LocalExecutionEnv) ReadTextFile(path string, ctx harnesstypes.Context) harnesstypes.Result[string, harnesstypes.FileError] {
	resolved := e.resolvePath(path)
	if aborted, ok := abortResult[string](ctx, &resolved); ok {
		return aborted
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		return fileErr[string](toFileError(err, &resolved))
	}
	return fileOk(string(data))
}

// ReadBinaryFile reads a complete file as bytes.
func (e *LocalExecutionEnv) ReadBinaryFile(path string, ctx harnesstypes.Context) harnesstypes.Result[[]byte, harnesstypes.FileError] {
	resolved := e.resolvePath(path)
	if aborted, ok := abortResult[[]byte](ctx, &resolved); ok {
		return aborted
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		return fileErr[[]byte](toFileError(err, &resolved))
	}
	return fileOk(data)
}

// WriteFile writes content, creating parent directories as needed.
func (e *LocalExecutionEnv) WriteFile(path string, content []byte, ctx harnesstypes.Context) harnesstypes.Result[struct{}, harnesstypes.FileError] {
	resolved := e.resolvePath(path)
	if aborted, ok := abortResult[struct{}](ctx, &resolved); ok {
		return aborted
	}
	if err := os.MkdirAll(filepath.Dir(resolved), 0o755); err != nil {
		return fileErr[struct{}](toFileError(err, &resolved))
	}
	if aborted, ok := abortResult[struct{}](ctx, &resolved); ok {
		return aborted
	}
	if err := os.WriteFile(resolved, content, 0o644); err != nil {
		return fileErr[struct{}](toFileError(err, &resolved))
	}
	return voidResult(nil)
}

// AppendFile appends content, creating parent directories as needed.
func (e *LocalExecutionEnv) AppendFile(path string, content []byte, ctx harnesstypes.Context) harnesstypes.Result[struct{}, harnesstypes.FileError] {
	resolved := e.resolvePath(path)
	if aborted, ok := abortResult[struct{}](ctx, &resolved); ok {
		return aborted
	}
	if err := os.MkdirAll(filepath.Dir(resolved), 0o755); err != nil {
		return fileErr[struct{}](toFileError(err, &resolved))
	}
	if aborted, ok := abortResult[struct{}](ctx, &resolved); ok {
		return aborted
	}
	file, err := os.OpenFile(resolved, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fileErr[struct{}](toFileError(err, &resolved))
	}
	if _, err := file.Write(content); err != nil {
		_ = file.Close()
		return fileErr[struct{}](toFileError(err, &resolved))
	}
	if err := file.Close(); err != nil {
		return fileErr[struct{}](toFileError(err, &resolved))
	}
	if aborted, ok := abortResult[struct{}](ctx, &resolved); ok {
		return aborted
	}
	return voidResult(nil)
}

// RenameFile atomically renames source onto destination.
func (e *LocalExecutionEnv) RenameFile(sourcePath string, destinationPath string, ctx harnesstypes.Context) harnesstypes.Result[struct{}, harnesstypes.FileError] {
	source := e.resolvePath(sourcePath)
	destination := e.resolvePath(destinationPath)
	if aborted, ok := abortResult[struct{}](ctx, &destination); ok {
		return aborted
	}
	if err := os.Rename(source, destination); err != nil {
		return fileErr[struct{}](toFileError(err, &source))
	}
	return voidResult(nil)
}

// FileInfo returns metadata without following symlinks.
func (e *LocalExecutionEnv) FileInfo(path string, ctx harnesstypes.Context) harnesstypes.Result[harnesstypes.FileInfo, harnesstypes.FileError] {
	resolved := e.resolvePath(path)
	if aborted, ok := abortResult[harnesstypes.FileInfo](ctx, &resolved); ok {
		return aborted
	}
	info, err := os.Lstat(resolved)
	if err != nil {
		return fileErr[harnesstypes.FileInfo](toFileError(err, &resolved))
	}
	result, infoErr := fileInfoFromInfo(resolved, info)
	if infoErr != nil {
		return fileErr[harnesstypes.FileInfo](infoErr)
	}
	return fileOk(*result)
}

// ListDir returns metadata for each directory entry.
func (e *LocalExecutionEnv) ListDir(path string, ctx harnesstypes.Context) harnesstypes.Result[[]harnesstypes.FileInfo, harnesstypes.FileError] {
	resolved := e.resolvePath(path)
	if aborted, ok := abortResult[[]harnesstypes.FileInfo](ctx, &resolved); ok {
		return aborted
	}
	entries, err := os.ReadDir(resolved)
	if err != nil {
		return fileErr[[]harnesstypes.FileInfo](toFileError(err, &resolved))
	}
	infos := make([]harnesstypes.FileInfo, 0, len(entries))
	for _, entry := range entries {
		if aborted, ok := abortResult[[]harnesstypes.FileInfo](ctx, &resolved); ok {
			return aborted
		}
		entryPath := filepath.Join(resolved, entry.Name())
		info, err := os.Lstat(entryPath)
		if err != nil {
			return fileErr[[]harnesstypes.FileInfo](toFileError(err, &entryPath))
		}
		projected, infoErr := fileInfoFromInfo(entryPath, info)
		if infoErr != nil {
			continue
		}
		infos = append(infos, *projected)
	}
	return fileOk(infos)
}

// CanonicalPath resolves symlinks to a canonical absolute path.
func (e *LocalExecutionEnv) CanonicalPath(path string, ctx harnesstypes.Context) harnesstypes.Result[string, harnesstypes.FileError] {
	resolved := e.resolvePath(path)
	if aborted, ok := abortResult[string](ctx, &resolved); ok {
		return aborted
	}
	canonical, err := filepath.EvalSymlinks(resolved)
	if err != nil {
		return fileErr[string](toFileError(err, &resolved))
	}
	return fileOk(canonical)
}

// Exists reports whether the path exists; a missing path is not an error.
func (e *LocalExecutionEnv) Exists(path string, ctx harnesstypes.Context) harnesstypes.Result[bool, harnesstypes.FileError] {
	result := e.FileInfo(path, ctx)
	if result.OK {
		return fileOk(true)
	}
	if result.Error.Code == harnesstypes.FileErrorNotFound {
		return fileOk(false)
	}
	return fileErr[bool](&result.Error)
}

// CreateDir creates a directory. The default is recursive.
func (e *LocalExecutionEnv) CreateDir(path string, options *harnesstypes.CreateDirOptions, ctx harnesstypes.Context) harnesstypes.Result[struct{}, harnesstypes.FileError] {
	resolved := e.resolvePath(path)
	if aborted, ok := abortResult[struct{}](ctx, &resolved); ok {
		return aborted
	}
	recursive := true
	if options != nil && options.Recursive != nil {
		recursive = *options.Recursive
	}
	var err error
	if recursive {
		err = os.MkdirAll(resolved, 0o755)
	} else {
		err = os.Mkdir(resolved, 0o755)
	}
	if err != nil {
		return fileErr[struct{}](toFileError(err, &resolved))
	}
	return voidResult(nil)
}

// Remove removes a path using the requested recursive and force options.
func (e *LocalExecutionEnv) Remove(path string, options *harnesstypes.RemoveOptions, ctx harnesstypes.Context) harnesstypes.Result[struct{}, harnesstypes.FileError] {
	resolved := e.resolvePath(path)
	if aborted, ok := abortResult[struct{}](ctx, &resolved); ok {
		return aborted
	}
	recursive := false
	force := false
	if options != nil {
		if options.Recursive != nil {
			recursive = *options.Recursive
		}
		if options.Force != nil {
			force = *options.Force
		}
	}
	if !force {
		if _, err := os.Lstat(resolved); err != nil {
			return fileErr[struct{}](toFileError(err, &resolved))
		}
	}
	var err error
	if recursive {
		err = os.RemoveAll(resolved)
	} else {
		err = os.Remove(resolved)
	}
	if err != nil {
		return fileErr[struct{}](toFileError(err, &resolved))
	}
	return voidResult(nil)
}

// CreateTempDir creates a fresh temporary directory under the system temp root.
func (e *LocalExecutionEnv) CreateTempDir(prefix *string, ctx harnesstypes.Context) harnesstypes.Result[string, harnesstypes.FileError] {
	if aborted, ok := abortResult[string](ctx, nil); ok {
		return aborted
	}
	dirPrefix := "tmp-"
	if prefix != nil {
		dirPrefix = *prefix
	}
	dir, err := os.MkdirTemp("", dirPrefix)
	if err != nil {
		return fileErr[string](toFileError(err, nil))
	}
	return fileOk(dir)
}

// CreateTempFile creates a fresh temporary file under a fresh temporary
// directory.
func (e *LocalExecutionEnv) CreateTempFile(options *harnesstypes.CreateTempFileOptions, ctx harnesstypes.Context) harnesstypes.Result[string, harnesstypes.FileError] {
	if e.createTempFileHook != nil {
		return e.createTempFileHook(options, ctx)
	}
	dir := e.CreateTempDir(stringPointer("tmp-"), ctx)
	if !dir.OK {
		return fileErr[string](&dir.Error)
	}
	prefix := ""
	suffix := ""
	if options != nil {
		if options.Prefix != nil {
			prefix = *options.Prefix
		}
		if options.Suffix != nil {
			suffix = *options.Suffix
		}
	}
	filePath := filepath.Join(dir.Value, prefix+randomUUID()+suffix)
	if err := os.WriteFile(filePath, []byte{}, 0o644); err != nil {
		return fileErr[string](toFileError(err, &filePath))
	}
	return fileOk(filePath)
}

// Cleanup terminates every tracked active shell process.
func (e *LocalExecutionEnv) Cleanup(_ harnesstypes.Context) {
	e.mu.Lock()
	pids := make([]int, 0, len(e.activeChildPids))
	for pid := range e.activeChildPids {
		pids = append(pids, pid)
	}
	e.activeChildPids = map[int]struct{}{}
	e.mu.Unlock()
	for _, pid := range pids {
		killProcessTree(pid)
	}
}

// OpenTextLineReader opens a pull-based UTF-8 line reader.
func (e *LocalExecutionEnv) OpenTextLineReader(path string, ctx harnesstypes.Context) harnesstypes.Result[harnesstypes.TextLineReader, harnesstypes.FileError] {
	resolved := e.resolvePath(path)
	if aborted, ok := abortResult[harnesstypes.TextLineReader](ctx, &resolved); ok {
		return aborted
	}
	file, err := os.Open(resolved)
	if err != nil {
		return fileErr[harnesstypes.TextLineReader](toFileError(err, &resolved))
	}
	if aborted, ok := abortResult[harnesstypes.TextLineReader](ctx, &resolved); ok {
		_ = file.Close()
		return aborted
	}
	return fileOk[harnesstypes.TextLineReader](&localTextLineReader{file: file, path: resolved})
}

// ReadTextLines reads up to options.MaxLines text lines.
func (e *LocalExecutionEnv) ReadTextLines(path string, options *harnesstypes.ReadTextLinesOptions, ctx harnesstypes.Context) harnesstypes.Result[[]string, harnesstypes.FileError] {
	if options != nil && options.MaxLines != nil && *options.MaxLines <= 0 {
		return fileOk([]string{})
	}
	opened := e.OpenTextLineReader(path, ctx)
	if !opened.OK {
		return fileErr[[]string](&opened.Error)
	}
	lines := []string{}
	defer opened.Value.Close(ctx)
	for {
		if options != nil && options.MaxLines != nil && len(lines) >= *options.MaxLines {
			break
		}
		line := opened.Value.ReadLine(ctx)
		if !line.OK {
			return fileErr[[]string](&line.Error)
		}
		if line.Value == nil {
			break
		}
		lines = append(lines, line.Value.Text)
	}
	return fileOk(lines)
}

// localTextLineReader is a strict LF reader. Node's readline does not report
// whether its final line was newline-terminated, so this port tracks it.
type localTextLineReader struct {
	file     *os.File
	path     string
	buffered string
	ended    bool
	closed   bool
}

func (r *localTextLineReader) ReadLine(ctx harnesstypes.Context) harnesstypes.Result[*harnesstypes.TextLine, harnesstypes.FileError] {
	if aborted, ok := abortResult[*harnesstypes.TextLine](ctx, &r.path); ok {
		return aborted
	}
	if r.closed {
		return fileErr[*harnesstypes.TextLine](harnesstypes.NewFileError(harnesstypes.FileErrorInvalid, "Text line reader is closed", &r.path, nil))
	}
	buffer := make([]byte, 64*1024)
	for {
		if newline := strings.Index(r.buffered, "\n"); newline != -1 {
			text := r.buffered[:newline]
			r.buffered = r.buffered[newline+1:]
			return fileOk(&harnesstypes.TextLine{Text: text, Terminated: true})
		}
		if r.ended {
			if r.buffered == "" {
				return fileOk[*harnesstypes.TextLine](nil)
			}
			text := r.buffered
			r.buffered = ""
			return fileOk(&harnesstypes.TextLine{Text: text, Terminated: false})
		}
		bytesRead, err := r.file.Read(buffer)
		if aborted, ok := abortResult[*harnesstypes.TextLine](ctx, &r.path); ok {
			return aborted
		}
		if bytesRead > 0 {
			r.buffered += string(buffer[:bytesRead])
		}
		if err == io.EOF || bytesRead == 0 {
			r.ended = true
			continue
		}
		if err != nil {
			return fileErr[*harnesstypes.TextLine](toFileError(err, &r.path))
		}
	}
}

func (r *localTextLineReader) Close(_ harnesstypes.Context) {
	if r.closed {
		return
	}
	r.closed = true
	r.buffered = ""
	_ = r.file.Close()
}

func randomUUID() string {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	bytes[6] = (bytes[6] & 0x0f) | 0x40
	bytes[8] = (bytes[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(bytes)
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32]
}

// Shell configuration helpers.

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

func runCommand(command string, args []string, timeoutMs int) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutMs)*time.Millisecond)
	defer cancel()
	cmd := exec.CommandContext(ctx, command, args...)
	output, err := cmd.Output()
	if err != nil {
		return "", false
	}
	return string(output), true
}

func findBashOnPath() (string, bool) {
	var stdout string
	var ok bool
	if runtime.GOOS == "windows" {
		stdout, ok = runCommand("where", []string{"bash.exe"}, 5000)
	} else {
		stdout, ok = runCommand("which", []string{"bash"}, 5000)
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

func (e *LocalExecutionEnv) getShellConfig() (shellConfig, *harnesstypes.ExecutionError) {
	if e.shellPath != nil {
		if pathExists(*e.shellPath) {
			return getBashShellConfig(*e.shellPath), nil
		}
		return shellConfig{}, harnesstypes.NewExecutionError(harnesstypes.ExecutionErrorShellUnavailable, "Custom shell path not found: "+*e.shellPath, nil)
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
		return shellConfig{}, harnesstypes.NewExecutionError(harnesstypes.ExecutionErrorShellUnavailable, "No bash shell found. Configure an explicit shellPath", nil)
	}
	if pathExists("/bin/bash") {
		return getBashShellConfig("/bin/bash"), nil
	}
	if bash, ok := findBashOnPath(); ok {
		return getBashShellConfig(bash), nil
	}
	return shellConfig{shell: "sh", args: []string{"-c"}}, nil
}

func getShellEnv(baseEnv map[string]string, extraEnv map[string]string, inherit bool) []string {
	if !inherit {
		return envSlice(extraEnv)
	}
	merged := map[string]string{}
	for _, entry := range os.Environ() {
		key, value, found := strings.Cut(entry, "=")
		if found {
			merged[key] = value
		}
	}
	for key, value := range baseEnv {
		merged[key] = value
	}
	for key, value := range extraEnv {
		merged[key] = value
	}
	return envSlice(merged)
}

func envSlice(env map[string]string) []string {
	entries := make([]string, 0, len(env))
	for key, value := range env {
		entries = append(entries, key+"="+value)
	}
	return entries
}

// Exec runs a shell command, capturing bounded output and optionally spilling
// the complete byte stream to a temp file.
func (e *LocalExecutionEnv) Exec(command string, options *harnesstypes.ShellExecOptions, ctx harnesstypes.Context) harnesstypes.Result[harnesstypes.ShellExecResult, harnesstypes.ExecutionError] {
	if ctx != nil && ctx.Err() != nil {
		return execErr(harnesstypes.NewExecutionError(harnesstypes.ExecutionErrorAborted, "aborted", nil))
	}
	timeoutMs, timeoutErr := resolveTimeoutMs(options)
	if timeoutErr != nil {
		return execErr(timeoutErr)
	}
	cwd := e.cwd
	if options != nil && options.Cwd != nil {
		cwd = e.resolvePath(*options.Cwd)
	}
	shellConfig, shellErr := e.getShellConfig()
	if shellErr != nil {
		return execErr(shellErr)
	}
	if !pathExists(cwd) {
		return execErr(harnesstypes.NewExecutionError(harnesstypes.ExecutionErrorSpawn, fmt.Sprintf("Working directory does not exist: %s\nCannot execute bash commands.", cwd), nil))
	}

	state := &execState{}
	var onUpdate func(harnesstypes.ShellOutputUpdate, harnesstypes.Context)
	if options != nil {
		onUpdate = options.OnUpdate
	}
	onError := func(err error) {
		state.failCallback(err)
		e.killStateChild(state)
	}
	var captureOptions *harnesstypes.ShellOutputCaptureOptions
	if options != nil {
		captureOptions = options.Capture
	}
	capture := outputcapture.NewOutputCapture(captureOptions, ctx, onUpdate, onError)
	defer capture.Dispose()
	spillRequested := options != nil && options.Capture != nil && options.Capture.Spill != nil && *options.Capture.Spill

	readPipe, writePipe, pipeErr := os.Pipe()
	if pipeErr != nil {
		return execErr(harnesstypes.NewExecutionError(harnesstypes.ExecutionErrorUnknown, pipeErr.Error(), pipeErr))
	}

	execCommand := exec.Command(shellConfig.shell, append(append([]string{}, shellConfig.args...), command)...)
	if shellConfig.commandFromStdin {
		execCommand = exec.Command(shellConfig.shell, shellConfig.args...)
		execCommand.Stdin = strings.NewReader(command)
	}
	execCommand.Dir = cwd
	inheritEnv := true
	var extraEnv map[string]string
	if options != nil {
		if options.InheritEnv != nil {
			inheritEnv = *options.InheritEnv
		}
		extraEnv = options.Env
	}
	execCommand.Env = getShellEnv(e.shellEnv, extraEnv, inheritEnv)
	execCommand.Stdout = writePipe
	execCommand.Stderr = writePipe
	setProcessGroup(execCommand)
	if startErr := execCommand.Start(); startErr != nil {
		_ = readPipe.Close()
		_ = writePipe.Close()
		return execErr(harnesstypes.NewExecutionError(harnesstypes.ExecutionErrorSpawn, startErr.Error(), startErr))
	}
	_ = writePipe.Close()
	pid := execCommand.Process.Pid
	e.trackChild(pid)
	state.setChild(pid)
	defer e.untrackChild(pid)

	var timer *time.Timer
	if timeoutMs != nil {
		timer = time.AfterFunc(time.Duration(*timeoutMs)*time.Millisecond, func() {
			state.setTimedOut()
			e.killStateChild(state)
		})
	}
	stopWatch := make(chan struct{})
	if ctx != nil {
		go func() {
			select {
			case <-ctx.Done():
				e.killStateChild(state)
			case <-stopWatch:
			}
		}()
	}

	spill := &spillState{}
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		buffer := make([]byte, 32*1024)
		for {
			read, readErr := readPipe.Read(buffer)
			if read > 0 {
				chunk := append([]byte(nil), buffer[:read]...)
				func() {
					defer func() {
						if recovered := recover(); recovered != nil {
							state.failCallback(toError(recovered))
							e.killStateChild(state)
						}
					}()
					wasTruncated := capture.Truncated()
					capture.Push(chunk)
					if !spillRequested || len(chunk) == 0 {
						return
					}
					switch {
					case spill.started || wasTruncated:
						if err := spill.write(e, capture, ctx, chunk); err != nil {
							state.failSpill(err)
							e.killStateChild(state)
						}
					case capture.Truncated():
						for _, prefix := range state.takeSpillPrefix() {
							if err := spill.write(e, capture, ctx, prefix); err != nil {
								state.failSpill(err)
								e.killStateChild(state)
								return
							}
						}
						if err := spill.write(e, capture, ctx, chunk); err != nil {
							state.failSpill(err)
							e.killStateChild(state)
						}
					default:
						state.appendSpillPrefix(chunk)
					}
				}()
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
	close(stopWatch)

	select {
	case <-readerDone:
	case <-time.After(exitStdioGraceMs * time.Millisecond):
		_ = readPipe.Close()
		<-readerDone
	}
	_ = readPipe.Close()
	spill.close()

	func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				state.failCallback(toError(recovered))
			}
		}()
		capture.Finish()
		capture.Flush()
	}()

	if callbackErr := state.callbackError(); callbackErr != nil {
		return execErr(harnesstypes.NewExecutionError(harnesstypes.ExecutionErrorCallback, callbackErr.Error(), callbackErr))
	}
	if state.timedOut() {
		return execErr(harnesstypes.NewExecutionError(harnesstypes.ExecutionErrorTimeout, fmt.Sprintf("timeout:%v", options.Timeout), nil))
	}
	if ctx != nil && ctx.Err() != nil {
		return execErr(harnesstypes.NewExecutionError(harnesstypes.ExecutionErrorAborted, "aborted", nil))
	}
	if spillErr := state.spillError(); spillErr != nil {
		return execErr(harnesstypes.NewExecutionError(harnesstypes.ExecutionErrorUnknown, "Failed to preserve complete shell output: "+spillErr.Error(), spillErr))
	}
	if waitErr != nil {
		var exitErr *exec.ExitError
		if !errors.As(waitErr, &exitErr) {
			return execErr(harnesstypes.NewExecutionError(harnesstypes.ExecutionErrorSpawn, waitErr.Error(), waitErr))
		}
	}
	output := capture.Snapshot()
	exitCode := exitCodeFromState(execCommand.ProcessState)
	return execOk(harnesstypes.ShellExecResult{
		ShellOutputMetadata: output.ShellOutputMetadata,
		ExitCode:            exitCode,
	})
}

func (e *LocalExecutionEnv) killStateChild(state *execState) {
	if pid := state.child(); pid > 0 {
		killProcessTree(pid)
	}
}

// execState carries the mutable outcome of one exec across its goroutines.
type execState struct {
	mu          sync.Mutex
	childPid    int
	didTimeout  bool
	callbackErr error
	spillErr    error
	spillPrefix [][]byte
}

func (s *execState) setChild(pid int) {
	s.mu.Lock()
	s.childPid = pid
	s.mu.Unlock()
}

func (s *execState) child() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.childPid
}

func (s *execState) setTimedOut() {
	s.mu.Lock()
	s.didTimeout = true
	s.mu.Unlock()
}

func (s *execState) timedOut() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.didTimeout
}

func (s *execState) failCallback(err error) {
	s.mu.Lock()
	if s.callbackErr == nil {
		s.callbackErr = err
	}
	s.mu.Unlock()
}

func (s *execState) callbackError() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.callbackErr
}

func (s *execState) failSpill(err error) {
	s.mu.Lock()
	if s.spillErr == nil {
		s.spillErr = err
	}
	s.mu.Unlock()
}

func (s *execState) spillError() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.spillErr
}

func (s *execState) appendSpillPrefix(chunk []byte) {
	s.mu.Lock()
	s.spillPrefix = append(s.spillPrefix, chunk)
	s.mu.Unlock()
}

func (s *execState) takeSpillPrefix() [][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	prefix := s.spillPrefix
	s.spillPrefix = nil
	return prefix
}

// spillState owns the lazily created full-output file.
type spillState struct {
	file    *os.File
	started bool
}

func (s *spillState) write(env *LocalExecutionEnv, capture *outputcapture.OutputCapture, ctx harnesstypes.Context, chunk []byte) error {
	if s.file == nil {
		created := env.CreateTempFile(&harnesstypes.CreateTempFileOptions{Prefix: stringPointer("pi-output-"), Suffix: stringPointer(".log")}, ctx)
		if !created.OK {
			return errors.New(created.Error.Message)
		}
		capture.SetSpillPath(created.Value)
		file, err := os.OpenFile(created.Value, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return err
		}
		s.file = file
	}
	s.started = true
	if len(chunk) == 0 {
		return nil
	}
	if _, err := s.file.Write(chunk); err != nil {
		return err
	}
	return nil
}

func (s *spillState) close() {
	if s.file != nil {
		_ = s.file.Close()
		s.file = nil
	}
}

func toError(value any) error {
	if err, ok := value.(error); ok {
		return err
	}
	return fmt.Errorf("%v", value)
}
