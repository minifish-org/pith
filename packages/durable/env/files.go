package env

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// maxSafeInteger is the largest integer upstream treats as a valid file size.
const maxSafeInteger = int64(9007199254740991)

// abortFileError is the canonical pre-cancellation filesystem error.
func abortFileError(path string, ctx context.Context) *FileError {
	cause := ctx.Err()
	if cause == nil {
		cause = errors.New("aborted")
	}
	return &FileError{Code: FileErrorAborted, Path: path, Cause: cause}
}

// toFileError maps a host error to an upstream filesystem error code. The
// fallback path is used when the host error carries no path of its own.
func toFileError(err error, fallbackPath string) *FileError {
	if err == nil {
		return &FileError{Code: FileErrorUnknown, Path: fallbackPath}
	}
	var fileErr *FileError
	if errors.As(err, &fileErr) {
		return fileErr
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return &FileError{Code: FileErrorAborted, Path: fallbackPath, Cause: err}
	}
	if errors.Is(err, os.ErrNotExist) {
		return &FileError{Code: FileErrorNotFound, Path: fallbackPath, Cause: err}
	}
	if errors.Is(err, os.ErrPermission) {
		return &FileError{Code: FileErrorPermissionDenied, Path: fallbackPath, Cause: err}
	}
	switch {
	case errors.Is(err, syscall.ENOTDIR):
		return &FileError{Code: FileErrorNotDirectory, Path: fallbackPath, Cause: err}
	case errors.Is(err, syscall.EISDIR):
		return &FileError{Code: FileErrorIsDirectory, Path: fallbackPath, Cause: err}
	case errors.Is(err, syscall.EINVAL):
		return &FileError{Code: FileErrorInvalid, Path: fallbackPath, Cause: err}
	}
	return &FileError{Code: FileErrorUnknown, Path: fallbackPath, Cause: err}
}

func fileKind(info os.FileInfo) (string, bool) {
	mode := info.Mode()
	switch {
	case mode&os.ModeSymlink != 0:
		return "symlink", true
	case mode.IsRegular():
		return "file", true
	case mode.IsDir():
		return "directory", true
	default:
		return "", false
	}
}

func fileInfoFromLstat(path string, info os.FileInfo) (FileInfo, *FileError) {
	kind, ok := fileKind(info)
	if !ok {
		return FileInfo{}, &FileError{Code: FileErrorInvalid, Path: path, Cause: errors.New("Unsupported file type")}
	}
	return FileInfo{
		Name:    filepath.Base(path),
		Path:    path,
		Kind:    kind,
		Size:    info.Size(),
		MtimeMs: info.ModTime().UnixNano() / int64(time.Millisecond),
	}, nil
}

func pathExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// AbsolutePath resolves path against the environment working directory.
func (l *Local) AbsolutePath(_ context.Context, path string) (string, error) {
	return l.resolve(path), nil
}

// JoinPath joins path segments.
func (l *Local) JoinPath(_ context.Context, parts ...string) (string, error) {
	return filepath.Join(parts...), nil
}

// ReadTextFile reads a complete file as UTF-8 text.
func (l *Local) ReadTextFile(ctx context.Context, path string) (string, error) {
	resolved := l.resolve(path)
	if ctx.Err() != nil {
		return "", abortFileError(resolved, ctx)
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		return "", toFileError(err, resolved)
	}
	return string(data), nil
}

// ReadBinaryFile reads a complete file as raw bytes.
func (l *Local) ReadBinaryFile(ctx context.Context, path string) ([]byte, error) {
	resolved := l.resolve(path)
	if ctx.Err() != nil {
		return nil, abortFileError(resolved, ctx)
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		return nil, toFileError(err, resolved)
	}
	return data, nil
}

// OpenTextLineReader opens a pull-based line reader over path.
func (l *Local) OpenTextLineReader(ctx context.Context, path string) (TextLineReader, error) {
	resolved := l.resolve(path)
	if ctx.Err() != nil {
		return nil, abortFileError(resolved, ctx)
	}
	file, err := os.Open(resolved)
	if err != nil {
		return nil, toFileError(err, resolved)
	}
	if ctx.Err() != nil {
		_ = file.Close()
		return nil, abortFileError(resolved, ctx)
	}
	return newTextLineReader(file, resolved), nil
}

// ReadTextLines reads up to options.MaxLines lines. A non-positive limit reads
// every line.
func (l *Local) ReadTextLines(ctx context.Context, path string, options ReadTextLinesOptions) ([]string, error) {
	reader, err := l.OpenTextLineReader(ctx, path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = reader.Close(ctx) }()
	lines := []string{}
	for {
		if options.MaxLines > 0 && len(lines) >= options.MaxLines {
			break
		}
		line, readErr := reader.ReadLine(ctx)
		if readErr != nil {
			return nil, readErr
		}
		if line == nil {
			break
		}
		lines = append(lines, line.Text)
	}
	return lines, nil
}

// WriteFile writes content, creating parent directories as needed.
func (l *Local) WriteFile(ctx context.Context, path string, content []byte) error {
	resolved := l.resolve(path)
	if ctx.Err() != nil {
		return abortFileError(resolved, ctx)
	}
	if err := os.MkdirAll(filepath.Dir(resolved), 0o755); err != nil {
		return toFileError(err, resolved)
	}
	if ctx.Err() != nil {
		return abortFileError(resolved, ctx)
	}
	if err := os.WriteFile(resolved, content, 0o644); err != nil {
		return toFileError(err, resolved)
	}
	return nil
}

// AppendFile appends content, creating parent directories as needed.
func (l *Local) AppendFile(ctx context.Context, path string, content []byte) error {
	resolved := l.resolve(path)
	if ctx.Err() != nil {
		return abortFileError(resolved, ctx)
	}
	if err := os.MkdirAll(filepath.Dir(resolved), 0o755); err != nil {
		return toFileError(err, resolved)
	}
	if ctx.Err() != nil {
		return abortFileError(resolved, ctx)
	}
	file, err := os.OpenFile(resolved, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return toFileError(err, resolved)
	}
	if _, err := file.Write(content); err != nil {
		_ = file.Close()
		return toFileError(err, resolved)
	}
	if err := file.Close(); err != nil {
		return toFileError(err, resolved)
	}
	if ctx.Err() != nil {
		return abortFileError(resolved, ctx)
	}
	return nil
}

// TruncateFile truncates or extends a file to exactly size bytes.
func (l *Local) TruncateFile(ctx context.Context, path string, size int64) error {
	resolved := l.resolve(path)
	if ctx.Err() != nil {
		return abortFileError(resolved, ctx)
	}
	if size < 0 || size > maxSafeInteger {
		return &FileError{Code: FileErrorInvalid, Path: resolved, Cause: errors.New("File size must be a non-negative safe integer")}
	}
	file, err := os.OpenFile(resolved, os.O_RDWR, 0)
	if err != nil {
		return toFileError(err, resolved)
	}
	defer func() { _ = file.Close() }()
	if err := file.Truncate(size); err != nil {
		return toFileError(err, resolved)
	}
	if ctx.Err() != nil {
		return abortFileError(resolved, ctx)
	}
	return nil
}

// FlushFile fsyncs an existing file without changing its content.
func (l *Local) FlushFile(ctx context.Context, path string) error {
	resolved := l.resolve(path)
	if ctx.Err() != nil {
		return abortFileError(resolved, ctx)
	}
	file, err := os.OpenFile(resolved, os.O_RDWR, 0)
	if err != nil {
		return toFileError(err, resolved)
	}
	defer func() { _ = file.Close() }()
	if err := file.Sync(); err != nil {
		return toFileError(err, resolved)
	}
	if ctx.Err() != nil {
		return abortFileError(resolved, ctx)
	}
	return nil
}

// RenameFile atomically renames source onto destination.
func (l *Local) RenameFile(ctx context.Context, sourcePath, destinationPath string) error {
	source := l.resolve(sourcePath)
	destination := l.resolve(destinationPath)
	if ctx.Err() != nil {
		return abortFileError(destination, ctx)
	}
	if err := os.Rename(source, destination); err != nil {
		return toFileError(err, source)
	}
	return nil
}

// FileInfo returns metadata without following symlinks.
func (l *Local) FileInfo(ctx context.Context, path string) (FileInfo, error) {
	resolved := l.resolve(path)
	if ctx.Err() != nil {
		return FileInfo{}, abortFileError(resolved, ctx)
	}
	info, err := os.Lstat(resolved)
	if err != nil {
		return FileInfo{}, toFileError(err, resolved)
	}
	projected, infoErr := fileInfoFromLstat(resolved, info)
	if infoErr != nil {
		return FileInfo{}, infoErr
	}
	return projected, nil
}

// ListDir lists directory entries without following symlinks.
func (l *Local) ListDir(ctx context.Context, path string) ([]FileInfo, error) {
	resolved := l.resolve(path)
	if ctx.Err() != nil {
		return nil, abortFileError(resolved, ctx)
	}
	entries, err := os.ReadDir(resolved)
	if err != nil {
		return nil, toFileError(err, resolved)
	}
	infos := make([]FileInfo, 0, len(entries))
	for _, entry := range entries {
		if ctx.Err() != nil {
			return nil, abortFileError(resolved, ctx)
		}
		entryPath := filepath.Join(resolved, entry.Name())
		info, err := os.Lstat(entryPath)
		if err != nil {
			return nil, toFileError(err, entryPath)
		}
		projected, infoErr := fileInfoFromLstat(entryPath, info)
		if infoErr != nil {
			continue
		}
		infos = append(infos, projected)
	}
	return infos, nil
}

// CanonicalPath resolves symlinks in an existing path.
func (l *Local) CanonicalPath(ctx context.Context, path string) (string, error) {
	resolved := l.resolve(path)
	if ctx.Err() != nil {
		return "", abortFileError(resolved, ctx)
	}
	canonical, err := filepath.EvalSymlinks(resolved)
	if err != nil {
		return "", toFileError(err, resolved)
	}
	return canonical, nil
}

// Exists reports whether a path exists. A missing path is not an error.
func (l *Local) Exists(ctx context.Context, path string) (bool, error) {
	_, err := l.FileInfo(ctx, path)
	if err == nil {
		return true, nil
	}
	var fileErr *FileError
	if errors.As(err, &fileErr) && fileErr.Code == FileErrorNotFound {
		return false, nil
	}
	return false, err
}

// CreateDir creates a directory, recursively by default.
func (l *Local) CreateDir(ctx context.Context, path string, recursive bool) error {
	resolved := l.resolve(path)
	if ctx.Err() != nil {
		return abortFileError(resolved, ctx)
	}
	var err error
	if recursive {
		err = os.MkdirAll(resolved, 0o755)
	} else {
		err = os.Mkdir(resolved, 0o755)
	}
	if err != nil {
		return toFileError(err, resolved)
	}
	return nil
}

// Remove removes a path with the requested recursive/force options.
func (l *Local) Remove(ctx context.Context, path string, options RemoveOptions) error {
	resolved := l.resolve(path)
	if ctx.Err() != nil {
		return abortFileError(resolved, ctx)
	}
	if !options.Force {
		if _, err := os.Lstat(resolved); err != nil {
			return toFileError(err, resolved)
		}
	}
	var err error
	if options.Recursive {
		err = os.RemoveAll(resolved)
	} else {
		err = os.Remove(resolved)
	}
	if err != nil {
		if options.Force && errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return toFileError(err, resolved)
	}
	return nil
}

// CreateTempDir creates a fresh temporary directory under the system temp root
// and records it for Cleanup.
func (l *Local) CreateTempDir(ctx context.Context, prefix string) (string, error) {
	if ctx.Err() != nil {
		return "", abortFileError("", ctx)
	}
	if prefix == "" {
		prefix = "tmp-"
	}
	dir, err := os.MkdirTemp("", prefix)
	if err != nil {
		return "", toFileError(err, "")
	}
	l.trackTemp(dir)
	return dir, nil
}

// CreateTempFile creates a fresh temporary file inside a fresh temporary
// directory and records the directory for Cleanup.
func (l *Local) CreateTempFile(ctx context.Context, options TempFileOptions) (string, error) {
	if l.createTempFileHook != nil {
		return l.createTempFileHook(ctx, options)
	}
	dir, err := l.CreateTempDir(ctx, "tmp-")
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, options.Prefix+randomUUID()+options.Suffix)
	if err := os.WriteFile(path, []byte{}, 0o600); err != nil {
		return "", toFileError(err, path)
	}
	return path, nil
}

// textLineReader is a strict LF reader that reports whether the final line was
// terminated. Reads use an explicit offset so a read cancelled while pending
// can be retried without consuming bytes.
type textLineReader struct {
	file     *os.File
	path     string
	buf      []byte
	buffered []byte
	offset   int64
	ended    bool
	closed   bool
}

func newTextLineReader(file *os.File, path string) *textLineReader {
	return &textLineReader{file: file, path: path, buf: make([]byte, 64*1024)}
}

// trimCR normalizes a CRLF terminator: the LF is reported through Terminated
// and the carriage return is removed from the line text.
func trimCR(text string) string {
	if strings.HasSuffix(text, "\r") {
		return text[:len(text)-1]
	}
	return text
}

// ReadLine returns the next line, or (nil, nil) at end of file.
func (r *textLineReader) ReadLine(ctx context.Context) (*TextLine, error) {
	if ctx.Err() != nil {
		return nil, abortFileError(r.path, ctx)
	}
	if r.closed {
		return nil, &FileError{Code: FileErrorInvalid, Path: r.path, Cause: errors.New("Text line reader is closed")}
	}
	for {
		if index := bytes.IndexByte(r.buffered, '\n'); index >= 0 {
			line := r.buffered[:index]
			r.buffered = r.buffered[index+1:]
			return &TextLine{Text: trimCR(string(line)), Terminated: true}, nil
		}
		if r.ended {
			if len(r.buffered) == 0 {
				return nil, nil
			}
			text := string(r.buffered)
			r.buffered = nil
			return &TextLine{Text: text, Terminated: false}, nil
		}
		read, err := r.file.ReadAt(r.buf, r.offset)
		if ctx.Err() != nil {
			return nil, abortFileError(r.path, ctx)
		}
		if read > 0 {
			r.offset += int64(read)
			r.buffered = append(r.buffered, r.buf[:read]...)
		}
		switch {
		case errors.Is(err, io.EOF):
			r.ended = true
		case err != nil:
			return nil, toFileError(err, r.path)
		case read == 0:
			r.ended = true
		}
	}
}

// Close releases the underlying file. It is idempotent.
func (r *textLineReader) Close(_ context.Context) error {
	if r.closed {
		return nil
	}
	r.closed = true
	r.buffered = nil
	return r.file.Close()
}
