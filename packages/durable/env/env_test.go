package env

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func newLocal(t *testing.T, cwd string) *Local {
	t.Helper()
	local, err := NewLocal(cwd)
	if err != nil {
		t.Fatalf("NewLocal: %v", err)
	}
	t.Cleanup(func() { _ = local.Cleanup(context.Background()) })
	return local
}

func TestNamespaceIdentityIsShared(t *testing.T) {
	first, err := NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if first.ID() == "" || first.ID() != second.ID() {
		t.Fatalf("namespace ids must be shared: %q %q", first.ID(), second.ID())
	}
}

func TestFilesystemLifecycle(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	local := newLocal(t, root)

	child, err := local.AbsolutePath(ctx, "nested/child")
	if err != nil || child != filepath.Join(root, "nested/child") {
		t.Fatalf("AbsolutePath: %q %v", child, err)
	}
	joined, err := local.JoinPath(ctx, root, "nested", "child")
	if err != nil || joined != filepath.Join(root, "nested", "child") {
		t.Fatalf("JoinPath: %q %v", joined, err)
	}
	if err := local.CreateDir(ctx, "nested/child", true); err != nil {
		t.Fatalf("CreateDir: %v", err)
	}
	if err := local.WriteFile(ctx, "nested/child/file.txt", []byte("hel")); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := local.AppendFile(ctx, "nested/child/file.txt", []byte("lo")); err != nil {
		t.Fatalf("AppendFile: %v", err)
	}
	if text, err := local.ReadTextFile(ctx, "nested/child/file.txt"); err != nil || text != "hello" {
		t.Fatalf("ReadTextFile: %q %v", text, err)
	}
	if lines, err := local.ReadTextLines(ctx, "nested/child/file.txt", ReadTextLinesOptions{MaxLines: 1}); err != nil || len(lines) != 1 || lines[0] != "hello" {
		t.Fatalf("ReadTextLines: %#v %v", lines, err)
	}
	if data, err := local.ReadBinaryFile(ctx, "nested/child/file.txt"); err != nil || string(data) != "hello" {
		t.Fatalf("ReadBinaryFile: %q %v", data, err)
	}
	entries, err := local.ListDir(ctx, "nested/child")
	if err != nil || len(entries) != 1 {
		t.Fatalf("ListDir: %#v %v", entries, err)
	}
	if entries[0].Name != "file.txt" || entries[0].Kind != "file" || entries[0].Size != 5 || entries[0].MtimeMs <= 0 {
		t.Fatalf("entry metadata: %#v", entries[0])
	}
	if present, err := local.Exists(ctx, "nested/child/file.txt"); err != nil || !present {
		t.Fatalf("Exists: %v %v", present, err)
	}
	if err := local.Remove(ctx, "nested/child/file.txt", RemoveOptions{}); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if present, err := local.Exists(ctx, "nested/child/file.txt"); err != nil || present {
		t.Fatalf("Exists after remove: %v %v", present, err)
	}
}

func TestAbsolutePathHomeAndFileURL(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	local := newLocal(t, root)
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home directory")
	}
	got, err := local.AbsolutePath(ctx, "~/pi-env-test")
	if err != nil || got != filepath.Join(home, "pi-env-test") {
		t.Fatalf("home expansion: %q %v", got, err)
	}
	filePath := filepath.Join(root, "file with spaces.txt")
	got, err = local.AbsolutePath(ctx, "file://"+strings.ReplaceAll(filePath, " ", "%20"))
	if err != nil || got != filePath {
		t.Fatalf("file url: %q %v", got, err)
	}
}

func TestFileInfoKindsAndCanonicalAliases(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	local := newLocal(t, root)
	if err := local.CreateDir(ctx, "dir", true); err != nil {
		t.Fatal(err)
	}
	if err := local.WriteFile(ctx, "dir/file.txt", []byte("hello")); err != nil {
		t.Fatal(err)
	}
	info, err := local.FileInfo(ctx, "dir")
	if err != nil || info.Kind != "directory" {
		t.Fatalf("dir info: %#v %v", info, err)
	}
	info, err = local.FileInfo(ctx, "dir/file.txt")
	if err != nil || info.Kind != "file" || info.Size != 5 {
		t.Fatalf("file info: %#v %v", info, err)
	}
	if runtime.GOOS == "windows" {
		return
	}
	link := filepath.Join(root, "file-link")
	if err := os.Symlink(filepath.Join(root, "dir/file.txt"), link); err != nil {
		t.Fatal(err)
	}
	info, err = local.FileInfo(ctx, "file-link")
	if err != nil || info.Kind != "symlink" {
		t.Fatalf("symlink info: %#v %v", info, err)
	}
	canonicalLink, err := local.CanonicalPath(ctx, "file-link")
	if err != nil {
		t.Fatal(err)
	}
	canonicalFile, err := local.CanonicalPath(ctx, "dir/file.txt")
	if err != nil {
		t.Fatal(err)
	}
	if canonicalLink != canonicalFile {
		t.Fatalf("canonical aliases differ: %q %q", canonicalLink, canonicalFile)
	}
}

func TestMissingPathIsTypedNotFound(t *testing.T) {
	ctx := context.Background()
	local := newLocal(t, t.TempDir())
	_, err := local.ReadTextFile(ctx, "missing.txt")
	var fileErr *FileError
	if !errors.As(err, &fileErr) || fileErr.Code != FileErrorNotFound {
		t.Fatalf("missing file error: %v", err)
	}
	present, err := local.Exists(ctx, "missing.txt")
	if err != nil || present {
		t.Fatalf("Exists missing: %v %v", present, err)
	}
}

func TestListDirNonDirectoryIsTyped(t *testing.T) {
	ctx := context.Background()
	local := newLocal(t, t.TempDir())
	if err := local.WriteFile(ctx, "file.txt", []byte("hello")); err != nil {
		t.Fatal(err)
	}
	_, err := local.ListDir(ctx, "file.txt")
	var fileErr *FileError
	if !errors.As(err, &fileErr) || fileErr.Code != FileErrorNotDirectory {
		t.Fatalf("list non-directory error: %v", err)
	}
}

func TestRenameReplacesDestination(t *testing.T) {
	ctx := context.Background()
	local := newLocal(t, t.TempDir())
	if err := local.WriteFile(ctx, "source.txt", []byte("new")); err != nil {
		t.Fatal(err)
	}
	if err := local.WriteFile(ctx, "destination.txt", []byte("old")); err != nil {
		t.Fatal(err)
	}
	if err := local.RenameFile(ctx, "source.txt", "destination.txt"); err != nil {
		t.Fatal(err)
	}
	if present, _ := local.Exists(ctx, "source.txt"); present {
		t.Fatal("source must be gone")
	}
	if text, _ := local.ReadTextFile(ctx, "destination.txt"); text != "new" {
		t.Fatalf("destination: %q", text)
	}
}

func TestRenameMissingSourceReportsSource(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	local := newLocal(t, root)
	if err := local.WriteFile(ctx, "destination.txt", []byte("unchanged")); err != nil {
		t.Fatal(err)
	}
	err := local.RenameFile(ctx, "missing-source.txt", "destination.txt")
	var fileErr *FileError
	if !errors.As(err, &fileErr) || fileErr.Code != FileErrorNotFound || fileErr.Path != filepath.Join(root, "missing-source.txt") {
		t.Fatalf("rename error: %v", err)
	}
	if text, _ := local.ReadTextFile(ctx, "destination.txt"); text != "unchanged" {
		t.Fatal("destination changed")
	}
}

func TestTempDirAndFile(t *testing.T) {
	ctx := context.Background()
	local := newLocal(t, t.TempDir())
	dir, err := local.CreateTempDir(ctx, "env-test-")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("temp dir missing: %v", err)
	}
	file, err := local.CreateTempFile(ctx, TempFileOptions{Prefix: "prefix-", Suffix: ".txt"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(file, ".txt") {
		t.Fatalf("temp file suffix: %q", file)
	}
	if text, err := local.ReadTextFile(ctx, file); err != nil || text != "" {
		t.Fatalf("temp file content: %q %v", text, err)
	}
}

func TestCleanupRemovesOwnedTempPaths(t *testing.T) {
	ctx := context.Background()
	local := newLocal(t, t.TempDir())
	dir, err := local.CreateTempDir(ctx, "cleanup-")
	if err != nil {
		t.Fatal(err)
	}
	if err := local.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("owned temp dir must be removed: %v", err)
	}
	// Cleanup is idempotent.
	if err := local.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestCreateDirAndRemoveOptions(t *testing.T) {
	ctx := context.Background()
	local := newLocal(t, t.TempDir())
	err := local.CreateDir(ctx, "missing/child", false)
	var fileErr *FileError
	if !errors.As(err, &fileErr) || fileErr.Code != FileErrorNotFound {
		t.Fatalf("non-recursive create: %v", err)
	}
	if err := local.WriteFile(ctx, "dir/child/file.txt", []byte("hello")); err != nil {
		t.Fatal(err)
	}
	if err := local.Remove(ctx, "dir", RemoveOptions{}); err == nil {
		t.Fatal("non-recursive remove of a directory must fail")
	}
	if err := local.Remove(ctx, "dir", RemoveOptions{Recursive: true}); err != nil {
		t.Fatal(err)
	}
	if present, _ := local.Exists(ctx, "dir"); present {
		t.Fatal("dir must be removed")
	}
	if err := local.Remove(ctx, "missing", RemoveOptions{}); err == nil {
		t.Fatal("removing a missing path without force must fail")
	}
	if err := local.Remove(ctx, "missing", RemoveOptions{Force: true}); err != nil {
		t.Fatal(err)
	}
}

func TestPreAbortedOperationsHaveNoEffects(t *testing.T) {
	ctx := context.Background()
	local := newLocal(t, t.TempDir())
	if err := local.WriteFile(ctx, "file.txt", []byte("hello")); err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()

	checks := []struct {
		name string
		fn   func() error
	}{
		{"readTextFile", func() error { _, err := local.ReadTextFile(cancelled, "file.txt"); return err }},
		{"readTextLines", func() error { _, err := local.ReadTextLines(cancelled, "file.txt", ReadTextLinesOptions{}); return err }},
		{"readBinaryFile", func() error { _, err := local.ReadBinaryFile(cancelled, "file.txt"); return err }},
		{"openTextLineReader", func() error { _, err := local.OpenTextLineReader(cancelled, "file.txt"); return err }},
		{"writeFile", func() error { return local.WriteFile(cancelled, "other.txt", []byte("x")) }},
		{"appendFile", func() error { return local.AppendFile(cancelled, "file.txt", []byte(" world")) }},
		{"truncateFile", func() error { return local.TruncateFile(cancelled, "file.txt", 1) }},
		{"flushFile", func() error { return local.FlushFile(cancelled, "file.txt") }},
		{"renameFile", func() error { return local.RenameFile(cancelled, "file.txt", "renamed.txt") }},
		{"fileInfo", func() error { _, err := local.FileInfo(cancelled, "file.txt"); return err }},
		{"listDir", func() error { _, err := local.ListDir(cancelled, "."); return err }},
		{"canonicalPath", func() error { _, err := local.CanonicalPath(cancelled, "file.txt"); return err }},
		{"createDir", func() error { return local.CreateDir(cancelled, "dir", true) }},
		{"remove", func() error { return local.Remove(cancelled, "file.txt", RemoveOptions{}) }},
		{"createTempDir", func() error { _, err := local.CreateTempDir(cancelled, ""); return err }},
		{"createTempFile", func() error { _, err := local.CreateTempFile(cancelled, TempFileOptions{}); return err }},
	}
	for _, check := range checks {
		var fileErr *FileError
		if err := check.fn(); !errors.As(err, &fileErr) || fileErr.Code != FileErrorAborted {
			t.Fatalf("%s must be aborted: %v", check.name, err)
		}
	}
	if text, err := local.ReadTextFile(ctx, "file.txt"); err != nil || text != "hello" {
		t.Fatalf("cancelled mutation ran: %q %v", text, err)
	}
}

func TestTruncateExtendsAndShrinksExactly(t *testing.T) {
	ctx := context.Background()
	local := newLocal(t, t.TempDir())
	content := []byte{0x61, 0xc3, 0xa9, 0x0a, 0x62, 0x0a}
	if err := local.WriteFile(ctx, "file.bin", content); err != nil {
		t.Fatal(err)
	}
	if err := local.TruncateFile(ctx, "file.bin", 2); err != nil {
		t.Fatal(err)
	}
	if data, _ := local.ReadBinaryFile(ctx, "file.bin"); string(data) != string([]byte{0x61, 0xc3}) {
		t.Fatalf("truncated bytes: %v", data)
	}
	if err := local.TruncateFile(ctx, "file.bin", 4); err != nil {
		t.Fatal(err)
	}
	if data, _ := local.ReadBinaryFile(ctx, "file.bin"); string(data) != string([]byte{0x61, 0xc3, 0, 0}) {
		t.Fatalf("extended bytes: %v", data)
	}
	if err := local.TruncateFile(ctx, "file.bin", 0); err != nil {
		t.Fatal(err)
	}
	if info, _ := local.FileInfo(ctx, "file.bin"); info.Size != 0 {
		t.Fatalf("size: %d", info.Size)
	}
}

func TestTruncateRejectsInvalidAndMissing(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	local := newLocal(t, root)
	if err := local.WriteFile(ctx, "file.txt", []byte("hello")); err != nil {
		t.Fatal(err)
	}
	err := local.TruncateFile(ctx, "file.txt", -1)
	var fileErr *FileError
	if !errors.As(err, &fileErr) || fileErr.Code != FileErrorInvalid || fileErr.Path != filepath.Join(root, "file.txt") {
		t.Fatalf("invalid size: %v", err)
	}
	err = local.TruncateFile(ctx, "missing.txt", 0)
	if !errors.As(err, &fileErr) || fileErr.Code != FileErrorNotFound {
		t.Fatalf("missing truncate: %v", err)
	}
	if present, _ := local.Exists(ctx, "missing.txt"); present {
		t.Fatal("truncate must not create a missing file")
	}
	if text, _ := local.ReadTextFile(ctx, "file.txt"); text != "hello" {
		t.Fatal("invalid truncate mutated content")
	}
}

func TestFlushReportsMissingAndDirectory(t *testing.T) {
	ctx := context.Background()
	local := newLocal(t, t.TempDir())
	if err := local.WriteFile(ctx, "file.txt", []byte("durable")); err != nil {
		t.Fatal(err)
	}
	if err := local.FlushFile(ctx, "file.txt"); err != nil {
		t.Fatal(err)
	}
	if text, _ := local.ReadTextFile(ctx, "file.txt"); text != "durable" {
		t.Fatal("flush changed content")
	}
	var fileErr *FileError
	if err := local.FlushFile(ctx, "missing.txt"); !errors.As(err, &fileErr) || fileErr.Code != FileErrorNotFound {
		t.Fatalf("flush missing: %v", err)
	}
	if err := local.CreateDir(ctx, "dir", true); err != nil {
		t.Fatal(err)
	}
	if err := local.FlushFile(ctx, "dir"); !errors.As(err, &fileErr) || fileErr.Code != FileErrorIsDirectory {
		t.Fatalf("flush directory: %v", err)
	}
}

func TestTextLineReaderTermination(t *testing.T) {
	ctx := context.Background()
	local := newLocal(t, t.TempDir())
	// CRLF is normalized to the line text; Terminated reports the LF.
	if err := local.WriteFile(ctx, "lines.txt", []byte("one\r\n\ntwo\npartial")); err != nil {
		t.Fatal(err)
	}
	reader, err := local.OpenTextLineReader(ctx, "lines.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close(ctx)
	want := []TextLine{
		{Text: "one", Terminated: true},
		{Text: "", Terminated: true},
		{Text: "two", Terminated: true},
		{Text: "partial", Terminated: false},
	}
	for index, expected := range want {
		line, err := reader.ReadLine(ctx)
		if err != nil || line == nil || *line != expected {
			t.Fatalf("line %d: %#v %v (want %#v)", index, line, err, expected)
		}
	}
	if line, err := reader.ReadLine(ctx); err != nil || line != nil {
		t.Fatalf("eof: %#v %v", line, err)
	}
}

func TestTextLineReaderEmptyAndLoneNewline(t *testing.T) {
	ctx := context.Background()
	local := newLocal(t, t.TempDir())
	if err := local.WriteFile(ctx, "empty.txt", []byte("")); err != nil {
		t.Fatal(err)
	}
	reader, _ := local.OpenTextLineReader(ctx, "empty.txt")
	if line, err := reader.ReadLine(ctx); err != nil || line != nil {
		t.Fatalf("empty file: %#v %v", line, err)
	}
	_ = reader.Close(ctx)

	if err := local.WriteFile(ctx, "newline.txt", []byte("\n")); err != nil {
		t.Fatal(err)
	}
	reader, _ = local.OpenTextLineReader(ctx, "newline.txt")
	line, err := reader.ReadLine(ctx)
	if err != nil || line == nil || line.Text != "" || !line.Terminated {
		t.Fatalf("lone newline: %#v %v", line, err)
	}
	if line, err := reader.ReadLine(ctx); err != nil || line != nil {
		t.Fatalf("eof after newline: %#v %v", line, err)
	}
	_ = reader.Close(ctx)
}

func TestTextLineReaderMultibyteAcrossChunks(t *testing.T) {
	ctx := context.Background()
	local := newLocal(t, t.TempDir())
	first := strings.Repeat("a", 64*1024-2) + "😀tail"
	second := strings.Repeat("é", 100_000)
	if err := local.WriteFile(ctx, "large.txt", []byte(first+"\n"+second)); err != nil {
		t.Fatal(err)
	}
	reader, err := local.OpenTextLineReader(ctx, "large.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close(ctx)
	line, err := reader.ReadLine(ctx)
	if err != nil || line == nil || line.Text != first || !line.Terminated {
		t.Fatalf("first line corrupted: %v", err)
	}
	line, err = reader.ReadLine(ctx)
	if err != nil || line == nil || line.Text != second || line.Terminated {
		t.Fatalf("second line corrupted: %v", err)
	}
	if line, err := reader.ReadLine(ctx); err != nil || line != nil {
		t.Fatalf("eof: %#v %v", line, err)
	}
}

func TestTextLineReaderCloseIsIdempotentAndRejectsReads(t *testing.T) {
	ctx := context.Background()
	local := newLocal(t, t.TempDir())
	if err := local.WriteFile(ctx, "lines.txt", []byte("one\n")); err != nil {
		t.Fatal(err)
	}
	reader, _ := local.OpenTextLineReader(ctx, "lines.txt")
	if err := reader.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(ctx); err != nil {
		t.Fatal(err)
	}
	_, err := reader.ReadLine(ctx)
	var fileErr *FileError
	if !errors.As(err, &fileErr) || fileErr.Code != FileErrorInvalid {
		t.Fatalf("read after close: %v", err)
	}
	if _, err := local.OpenTextLineReader(ctx, "missing.txt"); !errors.As(err, &fileErr) || fileErr.Code != FileErrorNotFound {
		t.Fatalf("open missing reader: %v", err)
	}
}

func TestReadTextLinesUnlimited(t *testing.T) {
	ctx := context.Background()
	local := newLocal(t, t.TempDir())
	if err := local.WriteFile(ctx, "file.txt", []byte("one\ntwo\nthree")); err != nil {
		t.Fatal(err)
	}
	lines, err := local.ReadTextLines(ctx, "file.txt", ReadTextLinesOptions{})
	if err != nil || len(lines) != 3 || lines[2] != "three" {
		t.Fatalf("unlimited lines: %#v %v", lines, err)
	}
	lines, err = local.ReadTextLines(ctx, "file.txt", ReadTextLinesOptions{MaxLines: 2})
	if err != nil || len(lines) != 2 || lines[1] != "two" {
		t.Fatalf("limited lines: %#v %v", lines, err)
	}
}
