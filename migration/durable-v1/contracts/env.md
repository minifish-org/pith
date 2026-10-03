# Durable execution environment

Source: Pi v1.0.0 `a13d35a742c6ef8462812a28fbe1d8c8b7431c32`,
`packages/durable/src/env/{index,node}.ts` and its environment tests.
Implement the complete portable capability surface, with an ordinary Go local
adapter. This package is independent of the Durable harness and storage.

## Frozen native API

Package `github.com/minifish-org/pith/packages/durable/env` uses `context.Context`
as the first argument of every fallible operation and idiomatic `(value, error)`
instead of JavaScript `Result`. All exported error types implement `error` and
`Unwrap`; use `errors.As`, not string matching.

```go
type FileError struct { Code, Path string; Cause error }
type ExecutionError struct { Code, SpillPath string; Cause error }
type FileInfo struct { Name, Path, Kind string; Size int64; MtimeMs int64 }
type TextLine struct { Text string; Terminated bool }
type ReadTextLinesOptions struct { MaxLines int }
type RemoveOptions struct { Recursive, Force bool }
type TempFileOptions struct { Prefix, Suffix string }
type TextLineReader interface {
    ReadLine(context.Context) (*TextLine, error) // nil at EOF
    Close(context.Context) error
}
type FileSystem interface {
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
type SpillOptions struct { AfterBytes, AfterLines int }
type ExecOptions struct {
    CWD string
    Env map[string]string
    InheritEnv *bool // nil means true
    Timeout time.Duration // zero means no default timeout
    OnOutput func(context.Context, []byte) error
    Spill *SpillOptions
}
type ExecResult struct { ExitCode int; SpillPath string }
type Shell interface {
    Exec(context.Context, string, ExecOptions) (ExecResult, error)
}
type ExecutionEnv interface { FileSystem; Shell }
func NewLocal(cwd string) (*Local, error)
// *Local implements ExecutionEnv.
```

Match source error codes (`aborted`, `not_found`, `permission_denied`,
`not_directory`, `is_directory`, `invalid`, `not_supported`, `unknown` for files;
`aborted`, `timeout`, `shell_unavailable`, `spawn_error`, `callback_error`,
`unknown` for commands). Cancellation must be checked before admission and
between operations. A cancelled operation must not silently mutate a file.

Local instances share a namespace ID even when CWD differs. Resolve symlinks for
canonical existing paths; resolve the existing parent for a new path. Preserve
line terminators accurately: CRLF removes CR from Text, Terminated records LF;
unterminated last line is returned once. Avoid scanner's default 64-KiB limit.
Flush uses fsync. Cleanup removes only temp paths owned by this environment.

Use the local shell (`bash` on POSIX; a documented native Windows fallback).
Emit combined stdout/stderr chunks without truncation before the harness bounds
them. Spill the COMPLETE raw stream, including chunks before the threshold, if
either byte or logical-line threshold is exceeded. Exactly-at-threshold is not
an overflow. Return ordinary nonzero process status in ExecResult; it is the
bash tool's job to mark that status as a failed tool. Cancel the process tree,
drain output, reap the process, preserve SpillPath on timeout/cancellation, and
report callback failures rather than swallowing them. Remote/container adapters
can implement this same interface; do not bake host filesystem assumptions into
the interface or claim it is an authorization sandbox.

## Acceptance

Independent judges exercise long CRLF/UTF-8 lines, EOF, namespace/canonical paths,
truncate/append/flush/rename, pre-cancel mutation rejection, missing-file typed
errors, full spill contents, strict thresholds, callback errors and subprocess
cancellation. Candidate-owned tests must additionally cover the other exported
methods and platform-specific shell behavior. In particular, exercise cancellation
and timeout after a spill has actually been created, using a persistence barrier
or controlled file-adapter fault injection rather than sleeps. Preserve that
existing spill's path and bytes in the typed error. Cancellation in the very first
output callback can precede spill admission in the pinned source; it does not
require creating a new spill file after cancellation. No live provider is needed.
