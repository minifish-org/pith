// Package env is the native Go adaptation of the Pi Durable execution
// environment at revision a13d35a742c6ef8462812a28fbe1d8c8b7431c32
// (packages/durable/src/env/{index,node}.ts).
//
// It exposes the portable filesystem and shell capability surface used by the
// Durable harness and its coding tools. Every fallible operation takes a
// standard context.Context as its first argument and returns an idiomatic
// (value, error) pair instead of the upstream Result/JS exceptions. Expected
// failures are typed: *FileError for filesystem operations and *ExecutionError
// for shell operations. Both implement error and Unwrap so callers use
// errors.As/errors.Is rather than string matching.
//
// This package is independent of the Durable harness, storage and Chord; it
// imports only the Go standard library. Remote or container hosts can implement
// the same FileSystem/Shell interfaces; the interface deliberately makes no
// authorization-sandbox claim.
//
// # Documented host/runtime differences
//
//   - ReadLine removes a trailing carriage return from a CRLF terminator and
//     reports Terminated for the LF. Upstream Node keeps the CR inside the
//     decoded text; the Go contract normalizes CRLF to the line text.
//   - ReadTextLinesOptions.MaxLines <= 0 means "read every line" (Go's zero
//     value cannot distinguish an omitted limit), while the upstream options
//     object treated an explicit non-positive limit as an empty result.
//   - Errors carry the source error code plus an optional Cause instead of a
//     separate human message field; Error() renders the cause.
//   - Process-tree termination uses a process group on POSIX and taskkill /T
//     on Windows rather than Node's detached-spawn cookie.
package env

import (
	"context"
	"fmt"
	"time"
)

// Filesystem error codes, matching the upstream FileErrorCode union.
const (
	FileErrorAborted          = "aborted"
	FileErrorNotFound         = "not_found"
	FileErrorPermissionDenied = "permission_denied"
	FileErrorNotDirectory     = "not_directory"
	FileErrorIsDirectory      = "is_directory"
	FileErrorInvalid          = "invalid"
	FileErrorNotSupported     = "not_supported"
	FileErrorUnknown          = "unknown"
)

// Command error codes, matching the upstream ExecutionErrorCode union.
const (
	ExecutionErrorAborted          = "aborted"
	ExecutionErrorTimeout          = "timeout"
	ExecutionErrorShellUnavailable = "shell_unavailable"
	ExecutionErrorSpawn            = "spawn_error"
	ExecutionErrorCallback         = "callback_error"
	ExecutionErrorUnknown          = "unknown"
)

// FileError reports an expected filesystem failure. Code is one of the
// FileError* constants and Cause holds the underlying host error, if any.
type FileError struct {
	Code  string
	Path  string
	Cause error
}

// Error renders the cause when present, else the code and path.
func (e *FileError) Error() string {
	if e == nil {
		return "file error"
	}
	if e.Cause != nil {
		return e.Cause.Error()
	}
	if e.Path != "" {
		return e.Code + ": " + e.Path
	}
	return e.Code
}

// Unwrap exposes the underlying host error for errors.Is/errors.As.
func (e *FileError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// ExecutionError reports an expected shell failure. Code is one of the
// ExecutionError* constants. SpillPath is set only when a spill file holding
// the complete raw output already existed when the command timed out or was
// aborted.
type ExecutionError struct {
	Code      string
	SpillPath string
	Cause     error
}

// Error renders the cause when present, else the code.
func (e *ExecutionError) Error() string {
	if e == nil {
		return "execution error"
	}
	if e.Cause != nil {
		return e.Cause.Error()
	}
	return e.Code
}

// Unwrap exposes the underlying host error for errors.Is/errors.As.
func (e *ExecutionError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// FileInfo describes one filesystem entry. Kind is "file", "directory" or
// "symlink"; MtimeMs is the modification time in Unix milliseconds.
type FileInfo struct {
	Name    string
	Path    string
	Kind    string
	Size    int64
	MtimeMs int64
}

// TextLine is one line returned by a TextLineReader. Terminated reports whether
// the line ended with LF.
type TextLine struct {
	Text       string
	Terminated bool
}

// ReadTextLinesOptions bounds the number of lines ReadTextLines returns. A
// non-positive MaxLines reads every line.
type ReadTextLinesOptions struct {
	MaxLines int
}

// RemoveOptions controls Remove. Recursive removes directory trees; Force
// ignores a missing target.
type RemoveOptions struct {
	Recursive bool
	Force     bool
}

// TempFileOptions names the prefix and suffix of a created temporary file.
type TempFileOptions struct {
	Prefix string
	Suffix string
}

// TextLineReader is a pull-based UTF-8 line reader. ReadLine returns (nil, nil)
// at end of file.
type TextLineReader interface {
	ReadLine(context.Context) (*TextLine, error)
	Close(context.Context) error
}

// FileSystem is the portable filesystem capability.
type FileSystem interface {
	// ID names the file namespace: equal IDs see the same files at the same
	// paths whatever their CWD.
	ID() string
	CWD() string
	SetCWD(string) error
	AbsolutePath(context.Context, string) (string, error)
	JoinPath(context.Context, ...string) (string, error)
	ReadTextFile(context.Context, string) (string, error)
	OpenTextLineReader(context.Context, string) (TextLineReader, error)
	ReadTextLines(context.Context, string, ReadTextLinesOptions) ([]string, error)
	ReadBinaryFile(context.Context, string) ([]byte, error)
	WriteFile(context.Context, string, []byte) error
	AppendFile(context.Context, string, []byte) error
	TruncateFile(context.Context, string, int64) error
	FlushFile(context.Context, string) error
	RenameFile(context.Context, string, string) error
	FileInfo(context.Context, string) (FileInfo, error)
	ListDir(context.Context, string) ([]FileInfo, error)
	CanonicalPath(context.Context, string) (string, error)
	Exists(context.Context, string) (bool, error)
	CreateDir(context.Context, string, bool) error
	Remove(context.Context, string, RemoveOptions) error
	CreateTempDir(context.Context, string) (string, error)
	CreateTempFile(context.Context, TempFileOptions) (string, error)
	Cleanup(context.Context) error
}

// SpillOptions spills the complete output to a temporary file once it exceeds
// either threshold. Exactly at the threshold is not an overflow.
type SpillOptions struct {
	AfterBytes int
	AfterLines int
}

// ExecOptions configures one shell execution.
type ExecOptions struct {
	// CWD overrides the environment working directory for this command.
	CWD string
	// Env is layered over the environment's configured shell environment and,
	// unless InheritEnv is false, over the inherited process environment.
	Env map[string]string
	// InheritEnv is nil to inherit (the default) or a pointer to false to start
	// from only Env.
	InheritEnv *bool
	// Timeout bounds the command. Zero means no default timeout.
	Timeout time.Duration
	// OnOutput receives every decoded chunk of combined stdout and stderr as it
	// arrives. A returned error aborts the command and is reported as a
	// callback_error.
	OnOutput func(context.Context, []byte) error
	// Spill spills the complete raw stream when it crosses the thresholds.
	Spill *SpillOptions
}

// ExecResult is the outcome of a command. A non-zero exit code is ordinary and
// not an error; the caller decides whether that status is a failure.
type ExecResult struct {
	ExitCode  int
	SpillPath string
}

// Shell is the portable command capability.
type Shell interface {
	Exec(context.Context, string, ExecOptions) (ExecResult, error)
}

// ExecutionEnv is a filesystem plus a shell.
type ExecutionEnv interface {
	FileSystem
	Shell
}

// errorf builds a typed execution error whose message is rendered from cause.
func executionError(code string, cause error) *ExecutionError {
	return &ExecutionError{Code: code, Cause: cause}
}

// abortExecutionError is the canonical pre-cancellation execution error.
func abortExecutionError(ctx context.Context) *ExecutionError {
	cause := ctx.Err()
	if cause == nil {
		cause = fmt.Errorf("aborted")
	}
	return &ExecutionError{Code: ExecutionErrorAborted, Cause: cause}
}
