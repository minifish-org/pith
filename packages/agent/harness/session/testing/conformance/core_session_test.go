package conformance

import (
	"bufio"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/minifish-org/pith/packages/agent/harness/session"
	"github.com/minifish-org/pith/packages/agent/harness/session/jsonl"
	sessiontesting "github.com/minifish-org/pith/packages/agent/harness/session/testing"
	harnesstypes "github.com/minifish-org/pith/packages/agent/harness/types"
)

// testFileSystem is a minimal OS-backed FileSystem used only by these tests
// until the harness execution environment batch lands.
type testFileSystem struct {
	cwd string
}

func newTestFileSystem(root string) *testFileSystem { return &testFileSystem{cwd: root} }

func (fs *testFileSystem) Cwd() string { return fs.cwd }

func fileErr(path string, err error) harnesstypes.FileError {
	code := harnesstypes.FileErrorUnknown
	if os.IsNotExist(err) {
		code = harnesstypes.FileErrorNotFound
	}
	return harnesstypes.FileError{Code: code, Message: err.Error(), Path: &path}
}

func okStruct() harnesstypes.Result[struct{}, harnesstypes.FileError] {
	return harnesstypes.Ok[struct{}, harnesstypes.FileError](struct{}{})
}

func (fs *testFileSystem) resolve(path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Join(fs.cwd, path)
}

func (fs *testFileSystem) AbsolutePath(path string, ctx harnesstypes.Context) harnesstypes.Result[string, harnesstypes.FileError] {
	return harnesstypes.Ok[string, harnesstypes.FileError](fs.resolve(path))
}

func (fs *testFileSystem) JoinPath(parts []string, ctx harnesstypes.Context) harnesstypes.Result[string, harnesstypes.FileError] {
	return harnesstypes.Ok[string, harnesstypes.FileError](filepath.Join(parts...))
}

func (fs *testFileSystem) ReadTextFile(path string, ctx harnesstypes.Context) harnesstypes.Result[string, harnesstypes.FileError] {
	resolved := fs.resolve(path)
	content, err := os.ReadFile(resolved)
	if err != nil {
		return harnesstypes.Err[string, harnesstypes.FileError](fileErr(path, err))
	}
	return harnesstypes.Ok[string, harnesstypes.FileError](string(content))
}

func (fs *testFileSystem) ReadBinaryFile(path string, ctx harnesstypes.Context) harnesstypes.Result[[]byte, harnesstypes.FileError] {
	resolved := fs.resolve(path)
	content, err := os.ReadFile(resolved)
	if err != nil {
		return harnesstypes.Err[[]byte, harnesstypes.FileError](fileErr(path, err))
	}
	return harnesstypes.Ok[[]byte, harnesstypes.FileError](content)
}

func (fs *testFileSystem) WriteFile(path string, content []byte, ctx harnesstypes.Context) harnesstypes.Result[struct{}, harnesstypes.FileError] {
	resolved := fs.resolve(path)
	if err := os.MkdirAll(filepath.Dir(resolved), 0o755); err != nil {
		return harnesstypes.Err[struct{}, harnesstypes.FileError](fileErr(path, err))
	}
	if err := os.WriteFile(resolved, content, 0o644); err != nil {
		return harnesstypes.Err[struct{}, harnesstypes.FileError](fileErr(path, err))
	}
	return okStruct()
}

func (fs *testFileSystem) AppendFile(path string, content []byte, ctx harnesstypes.Context) harnesstypes.Result[struct{}, harnesstypes.FileError] {
	resolved := fs.resolve(path)
	if err := os.MkdirAll(filepath.Dir(resolved), 0o755); err != nil {
		return harnesstypes.Err[struct{}, harnesstypes.FileError](fileErr(path, err))
	}
	file, err := os.OpenFile(resolved, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return harnesstypes.Err[struct{}, harnesstypes.FileError](fileErr(path, err))
	}
	defer file.Close()
	if _, err := file.Write(content); err != nil {
		return harnesstypes.Err[struct{}, harnesstypes.FileError](fileErr(path, err))
	}
	return okStruct()
}

func (fs *testFileSystem) RenameFile(sourcePath string, destinationPath string, ctx harnesstypes.Context) harnesstypes.Result[struct{}, harnesstypes.FileError] {
	if err := os.MkdirAll(filepath.Dir(fs.resolve(destinationPath)), 0o755); err != nil {
		return harnesstypes.Err[struct{}, harnesstypes.FileError](fileErr(destinationPath, err))
	}
	if err := os.Rename(fs.resolve(sourcePath), fs.resolve(destinationPath)); err != nil {
		return harnesstypes.Err[struct{}, harnesstypes.FileError](fileErr(sourcePath, err))
	}
	return okStruct()
}

func (fs *testFileSystem) FileInfo(path string, ctx harnesstypes.Context) harnesstypes.Result[harnesstypes.FileInfo, harnesstypes.FileError] {
	resolved := fs.resolve(path)
	info, err := os.Lstat(resolved)
	if err != nil {
		return harnesstypes.Err[harnesstypes.FileInfo, harnesstypes.FileError](fileErr(path, err))
	}
	kind := harnesstypes.FileKindFile
	switch {
	case info.IsDir():
		kind = harnesstypes.FileKindDirectory
	case info.Mode()&os.ModeSymlink != 0:
		kind = harnesstypes.FileKindSymlink
	}
	return harnesstypes.Ok[harnesstypes.FileInfo, harnesstypes.FileError](harnesstypes.FileInfo{
		Name:    info.Name(),
		Path:    resolved,
		Kind:    kind,
		Size:    int(info.Size()),
		MtimeMs: float64(info.ModTime().UnixMilli()),
	})
}

func (fs *testFileSystem) ListDir(path string, ctx harnesstypes.Context) harnesstypes.Result[[]harnesstypes.FileInfo, harnesstypes.FileError] {
	resolved := fs.resolve(path)
	entries, err := os.ReadDir(resolved)
	if err != nil {
		return harnesstypes.Err[[]harnesstypes.FileInfo, harnesstypes.FileError](fileErr(path, err))
	}
	out := make([]harnesstypes.FileInfo, 0, len(entries))
	for _, entry := range entries {
		info, infoErr := entry.Info()
		if infoErr != nil {
			return harnesstypes.Err[[]harnesstypes.FileInfo, harnesstypes.FileError](fileErr(path, infoErr))
		}
		kind := harnesstypes.FileKindFile
		if entry.IsDir() {
			kind = harnesstypes.FileKindDirectory
		} else if entry.Type()&os.ModeSymlink != 0 {
			kind = harnesstypes.FileKindSymlink
		}
		out = append(out, harnesstypes.FileInfo{
			Name:    entry.Name(),
			Path:    filepath.Join(resolved, entry.Name()),
			Kind:    kind,
			Size:    int(info.Size()),
			MtimeMs: float64(info.ModTime().UnixMilli()),
		})
	}
	return harnesstypes.Ok[[]harnesstypes.FileInfo, harnesstypes.FileError](out)
}

func (fs *testFileSystem) CanonicalPath(path string, ctx harnesstypes.Context) harnesstypes.Result[string, harnesstypes.FileError] {
	resolved, err := filepath.EvalSymlinks(fs.resolve(path))
	if err != nil {
		return harnesstypes.Err[string, harnesstypes.FileError](fileErr(path, err))
	}
	return harnesstypes.Ok[string, harnesstypes.FileError](resolved)
}

func (fs *testFileSystem) Exists(path string, ctx harnesstypes.Context) harnesstypes.Result[bool, harnesstypes.FileError] {
	_, err := os.Lstat(fs.resolve(path))
	if err == nil {
		return harnesstypes.Ok[bool, harnesstypes.FileError](true)
	}
	if os.IsNotExist(err) {
		return harnesstypes.Ok[bool, harnesstypes.FileError](false)
	}
	return harnesstypes.Err[bool, harnesstypes.FileError](fileErr(path, err))
}

func (fs *testFileSystem) CreateDir(path string, options *harnesstypes.CreateDirOptions, ctx harnesstypes.Context) harnesstypes.Result[struct{}, harnesstypes.FileError] {
	recursive := options == nil || options.Recursive == nil || *options.Recursive
	if recursive {
		if err := os.MkdirAll(fs.resolve(path), 0o755); err != nil {
			return harnesstypes.Err[struct{}, harnesstypes.FileError](fileErr(path, err))
		}
		return okStruct()
	}
	if err := os.Mkdir(fs.resolve(path), 0o755); err != nil {
		return harnesstypes.Err[struct{}, harnesstypes.FileError](fileErr(path, err))
	}
	return okStruct()
}

func (fs *testFileSystem) Remove(path string, options *harnesstypes.RemoveOptions, ctx harnesstypes.Context) harnesstypes.Result[struct{}, harnesstypes.FileError] {
	recursive := options != nil && options.Recursive != nil && *options.Recursive
	force := options != nil && options.Force != nil && *options.Force
	var err error
	if recursive {
		err = os.RemoveAll(fs.resolve(path))
	} else {
		err = os.Remove(fs.resolve(path))
	}
	if err != nil {
		if force && os.IsNotExist(err) {
			return okStruct()
		}
		return harnesstypes.Err[struct{}, harnesstypes.FileError](fileErr(path, err))
	}
	return okStruct()
}

func (fs *testFileSystem) CreateTempDir(prefix *string, ctx harnesstypes.Context) harnesstypes.Result[string, harnesstypes.FileError] {
	pattern := "tmp-"
	if prefix != nil {
		pattern = *prefix
	}
	dir, err := os.MkdirTemp(fs.cwd, pattern)
	if err != nil {
		return harnesstypes.Err[string, harnesstypes.FileError](harnesstypes.FileError{Code: harnesstypes.FileErrorUnknown, Message: err.Error()})
	}
	return harnesstypes.Ok[string, harnesstypes.FileError](dir)
}

func (fs *testFileSystem) CreateTempFile(options *harnesstypes.CreateTempFileOptions, ctx harnesstypes.Context) harnesstypes.Result[string, harnesstypes.FileError] {
	pattern := "tmp-"
	if options != nil && options.Prefix != nil {
		pattern = *options.Prefix
	}
	file, err := os.CreateTemp(fs.cwd, pattern)
	if err != nil {
		return harnesstypes.Err[string, harnesstypes.FileError](harnesstypes.FileError{Code: harnesstypes.FileErrorUnknown, Message: err.Error()})
	}
	name := file.Name()
	_ = file.Close()
	return harnesstypes.Ok[string, harnesstypes.FileError](name)
}

func (fs *testFileSystem) ReadTextLines(path string, options *harnesstypes.ReadTextLinesOptions, ctx harnesstypes.Context) harnesstypes.Result[[]string, harnesstypes.FileError] {
	content, err := os.ReadFile(fs.resolve(path))
	if err != nil {
		return harnesstypes.Err[[]string, harnesstypes.FileError](fileErr(path, err))
	}
	text := string(content)
	if text == "" {
		return harnesstypes.Ok[[]string, harnesstypes.FileError]([]string{})
	}
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	if options != nil && options.MaxLines != nil && len(lines) > *options.MaxLines {
		lines = lines[:*options.MaxLines]
	}
	return harnesstypes.Ok[[]string, harnesstypes.FileError](lines)
}

func (fs *testFileSystem) OpenTextLineReader(path string, ctx harnesstypes.Context) harnesstypes.Result[harnesstypes.TextLineReader, harnesstypes.FileError] {
	file, err := os.Open(fs.resolve(path))
	if err != nil {
		return harnesstypes.Err[harnesstypes.TextLineReader, harnesstypes.FileError](fileErr(path, err))
	}
	return harnesstypes.Ok[harnesstypes.TextLineReader, harnesstypes.FileError](&testLineReader{file: file, reader: bufio.NewReader(file)})
}

func (fs *testFileSystem) Cleanup(ctx harnesstypes.Context) {}

type testLineReader struct {
	file   *os.File
	reader *bufio.Reader
}

func (r *testLineReader) ReadLine(ctx harnesstypes.Context) harnesstypes.Result[*harnesstypes.TextLine, harnesstypes.FileError] {
	line, err := r.reader.ReadString('\n')
	if err == io.EOF {
		if line == "" {
			return harnesstypes.Ok[*harnesstypes.TextLine, harnesstypes.FileError](nil)
		}
		return harnesstypes.Ok[*harnesstypes.TextLine, harnesstypes.FileError](&harnesstypes.TextLine{Text: line, Terminated: false})
	}
	if err != nil {
		return harnesstypes.Err[*harnesstypes.TextLine, harnesstypes.FileError](harnesstypes.FileError{Code: harnesstypes.FileErrorUnknown, Message: err.Error()})
	}
	return harnesstypes.Ok[*harnesstypes.TextLine, harnesstypes.FileError](&harnesstypes.TextLine{Text: strings.TrimSuffix(line, "\n"), Terminated: true})
}

func (r *testLineReader) Close(ctx harnesstypes.Context) { _ = r.file.Close() }

func runCases(t *testing.T, cases []sessiontesting.ConformanceCase) {
	t.Helper()
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.Group+"/"+testCase.Name, func(t *testing.T) {
			if err := testCase.Run(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func memoryStorageFactory(root string) sessiontesting.StorageFixtureFunc {
	return func() (sessiontesting.StorageFixture, error) {
		storage := session.NewMemoryStorage(&session.MemoryStorageOptions{Now: func() float64 { return 1700000000000 }})
		return sessiontesting.StorageFixture{Storage: storage, Close: func() error { return storage.Close(ctx) }}, nil
	}
}

func jsonlStorageFactory(root string) sessiontesting.StorageFixtureFunc {
	return func() (sessiontesting.StorageFixture, error) {
		directory, err := os.MkdirTemp(root, "storage-")
		if err != nil {
			return sessiontesting.StorageFixture{}, err
		}
		fileSystem := newTestFileSystem(directory)
		header := jsonl.JsonlStorageHeader{V: jsonl.JSONL_FORMAT_VERSION, Kind: "header", ID: "session", StorageVersion: 1, CreatedAt: 1700000000000, Cwd: "/workspace"}
		storage, err := jsonl.Create(jsonl.JsonlStorageOptions{FileSystem: fileSystem, Path: "session.jsonl", Now: func() float64 { return 1700000000000 }}, header, nil, ctx)
		if err != nil {
			return sessiontesting.StorageFixture{}, err
		}
		return sessiontesting.StorageFixture{Storage: storage, Close: func() error { return storage.Close(ctx) }}, nil
	}
}

type memoryBackend struct {
	repo *session.MemorySessionRepo
}

func (b *memoryBackend) Create(id string, parentID *string) (harnesstypes.Session[harnesstypes.SessionMetadata], error) {
	return b.repo.Create(harnesstypes.SessionCreateOptions{ID: &id, ParentSessionID: parentID}, ctx)
}

func (b *memoryBackend) Open(metadata harnesstypes.SessionMetadata) (harnesstypes.Session[harnesstypes.SessionMetadata], error) {
	return b.repo.Open(metadata, ctx)
}

func (b *memoryBackend) List() ([]harnesstypes.SessionMetadata, error) { return b.repo.List(nil, ctx) }

func (b *memoryBackend) Delete(metadata harnesstypes.SessionMetadata) error {
	return b.repo.Delete(metadata, ctx)
}

func (b *memoryBackend) Fork(source harnesstypes.SessionMetadata, options harnesstypes.ForkOptions) (harnesstypes.Session[harnesstypes.SessionMetadata], error) {
	return b.repo.Fork(source, options, ctx)
}

type jsonlBackend struct {
	repo *jsonl.JsonlSessionRepo
	cwd  string
}

func (b *jsonlBackend) Create(id string, parentID *string) (harnesstypes.Session[jsonl.JsonlSessionMetadata], error) {
	return b.repo.Create(jsonl.JsonlSessionCreateOptions{SessionCreateOptions: harnesstypes.SessionCreateOptions{ID: &id, ParentSessionID: parentID}, Cwd: b.cwd}, ctx)
}

func (b *jsonlBackend) Open(metadata jsonl.JsonlSessionMetadata) (harnesstypes.Session[jsonl.JsonlSessionMetadata], error) {
	return b.repo.Open(metadata, ctx)
}

func (b *jsonlBackend) List() ([]jsonl.JsonlSessionMetadata, error) { return b.repo.List(nil, ctx) }

func (b *jsonlBackend) Delete(metadata jsonl.JsonlSessionMetadata) error {
	return b.repo.Delete(metadata, ctx)
}

func (b *jsonlBackend) Fork(source jsonl.JsonlSessionMetadata, options harnesstypes.ForkOptions) (harnesstypes.Session[jsonl.JsonlSessionMetadata], error) {
	return b.repo.Fork(source, options, ctx)
}

func TestStorageConformanceMemory(t *testing.T) {
	root := t.TempDir()
	runCases(t, CreateStorageConformance(memoryStorageFactory(root)))
}

func TestStorageConformanceJSONL(t *testing.T) {
	root := t.TempDir()
	runCases(t, CreateStorageConformance(jsonlStorageFactory(root)))
}

func TestSessionRepoConformanceMemory(t *testing.T) {
	factory := func() (SessionRepoBackend[harnesstypes.SessionMetadata], func() error) {
		repo := session.NewMemorySessionRepo(&session.MemorySessionRepoOptions{Now: func() float64 { return 1700000000000 }})
		return &memoryBackend{repo: repo}, func() error { return repo.Close(ctx) }
	}
	runCases(t, CreateSessionRepoConformance(factory))
}

func TestSessionRepoConformanceJSONL(t *testing.T) {
	base := t.TempDir()
	factory := func() (SessionRepoBackend[jsonl.JsonlSessionMetadata], func() error) {
		directory, err := os.MkdirTemp(base, "repo-")
		if err != nil {
			return nil, func() error { return err }
		}
		repo := jsonl.NewJsonlSessionRepo(jsonl.JsonlSessionRepoOptions{FileSystem: newTestFileSystem(directory), SessionsRoot: "sessions", Now: func() float64 { return 1700000000000 }})
		return &jsonlBackend{repo: repo, cwd: "/workspace"}, func() error { return repo.Close(ctx) }
	}
	runCases(t, CreateSessionRepoConformance(factory))
}

func TestJSONLStorageRestartRoundTrip(t *testing.T) {
	fs := newTestFileSystem(t.TempDir())
	opts := jsonl.JsonlStorageOptions{FileSystem: fs, Path: "session.jsonl", Now: func() float64 { return 1700000000000 }}
	header := jsonl.JsonlStorageHeader{V: jsonl.JSONL_FORMAT_VERSION, Kind: "header", ID: "round", StorageVersion: 1, CreatedAt: 1700000000000, Cwd: "/workspace"}
	storage, err := jsonl.Create(opts, header, nil, ctx)
	if err != nil {
		t.Fatal(err)
	}
	committed, err := storage.Commit([]harnesstypes.Write{
		session.InsertEntry(userEntry("root", nil, "hello")),
		session.SetValue(session.BranchTip("main"), "root"),
	}, ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.Close(ctx); err != nil {
		t.Fatal(err)
	}
	reopened, err := jsonl.Open(opts, ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close(ctx)
	entries, err := reopened.GetEntries([]string{"root"}, ctx)
	if err != nil {
		t.Fatal(err)
	}
	entry, ok := entries["root"].(harnesstypes.MessageEntry)
	if !ok || entry.Seq != committed.Seqs[0] {
		t.Fatalf("entry = %#v", entries["root"])
	}
	tip, ok, err := reopened.GetValue(session.BranchTip("main"), ctx)
	if err != nil || !ok || tip.Value != "root" || tip.Seq != committed.Seqs[1] {
		t.Fatalf("tip = %v %v %v", tip, ok, err)
	}
	next, err := reopened.Commit([]harnesstypes.Write{session.SetValue(session.SessionName, "name")}, ctx)
	if err != nil {
		t.Fatal(err)
	}
	if next.FirstSeq != committed.Seqs[1]+1 {
		t.Fatalf("next firstSeq = %d", next.FirstSeq)
	}
}

func TestJSONLStorageTornTail(t *testing.T) {
	fs := newTestFileSystem(t.TempDir())
	opts := jsonl.JsonlStorageOptions{FileSystem: fs, Path: "torn.jsonl", Now: func() float64 { return 1 }}
	header := jsonl.JsonlStorageHeader{V: jsonl.JSONL_FORMAT_VERSION, Kind: "header", ID: "torn", StorageVersion: 1, CreatedAt: 1, Cwd: "/workspace"}
	storage, err := jsonl.Create(opts, header, nil, ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := storage.Commit([]harnesstypes.Write{session.InsertEntry(userEntry("kept", nil, "kept"))}, ctx); err != nil {
		t.Fatal(err)
	}
	if err := storage.Close(ctx); err != nil {
		t.Fatal(err)
	}
	prefix := fs.ReadTextFile("torn.jsonl", ctx)
	if !prefix.OK {
		t.Fatal(prefix.Error)
	}
	torn := `{"kind":"entry","id":"torn","parentId":null,"type":"message","seq":2,"timestamp":1,"message":{"role":"user","content":"torn","timestamp":1}}`
	fs.AppendFile("torn.jsonl", []byte(torn), ctx)
	reopened, err := jsonl.Open(opts, ctx)
	if err != nil {
		t.Fatal(err)
	}
	if entries, err := reopened.GetEntries([]string{"torn"}, ctx); err != nil {
		t.Fatal(err)
	} else if _, ok := entries["torn"]; ok {
		t.Fatalf("torn entry must be discarded")
	}
	if entries, err := reopened.GetEntries([]string{"kept"}, ctx); err != nil {
		t.Fatal(err)
	} else if _, ok := entries["kept"]; !ok {
		t.Fatalf("kept entry must survive")
	}
	after := fs.ReadTextFile("torn.jsonl", ctx)
	if !after.OK || after.Value != prefix.Value {
		t.Fatalf("torn file was not truncated: %q", after.Value)
	}
	if exists := fs.Exists("torn.jsonl.tmp", ctx); exists.OK && exists.Value {
		t.Fatal("staging file must not survive")
	}
	if err := reopened.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestJSONLStorageRejectsMalformedInteriorLine(t *testing.T) {
	fs := newTestFileSystem(t.TempDir())
	opts := jsonl.JsonlStorageOptions{FileSystem: fs, Path: "bad.jsonl", Now: func() float64 { return 1 }}
	header := jsonl.JsonlStorageHeader{V: jsonl.JSONL_FORMAT_VERSION, Kind: "header", ID: "bad", StorageVersion: 1, CreatedAt: 1, Cwd: "/workspace"}
	storage, err := jsonl.Create(opts, header, nil, ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := storage.Commit([]harnesstypes.Write{session.InsertEntry(userEntry("kept", nil, "kept"))}, ctx); err != nil {
		t.Fatal(err)
	}
	if err := storage.Close(ctx); err != nil {
		t.Fatal(err)
	}
	original := fs.ReadTextFile("bad.jsonl", ctx)
	fs.WriteFile("bad.jsonl", []byte(original.Value+"not-json\n"), ctx)
	if _, err := jsonl.Open(opts, ctx); err == nil {
		t.Fatal("expected malformed interior line to reject open")
	}
	unchanged := fs.ReadTextFile("bad.jsonl", ctx)
	if unchanged.Value != original.Value+"not-json\n" {
		t.Fatal("malformed file must not be rewritten")
	}
}

func TestLegacyV3Normalization(t *testing.T) {
	fs := newTestFileSystem(t.TempDir())
	header := `{"type":"session","version":3,"id":"legacy","timestamp":"2023-11-14T22:13:20.000Z","cwd":"/workspace"}`
	records := []string{
		`{"type":"message","id":"before","parentId":null,"timestamp":"2023-11-14T22:13:20.000Z","message":{"role":"user","content":"before","timestamp":1700000000000}}`,
		`{"type":"session_info","id":"boundary","parentId":"before","timestamp":"2023-11-14T22:13:20.000Z"}`,
		`{"type":"message","id":"kept","parentId":"boundary","timestamp":"2023-11-14T22:13:20.000Z","message":{"role":"user","content":"kept","timestamp":1700000000000}}`,
		`{"type":"compaction","id":"selected","parentId":"kept","timestamp":"2023-11-14T22:13:20.000Z","summary":"selected","firstKeptEntryId":"boundary","tokensBefore":20,"fromHook":true,"details":{"a":2}}`,
	}
	content := header + "\n" + strings.Join(records, "\n") + "\n"
	if result := fs.WriteFile("legacy.jsonl", []byte(content), ctx); !result.OK {
		t.Fatal(result.Error)
	}
	source, err := jsonl.ReadLegacyV3Source(fs, "legacy.jsonl", ctx)
	if err != nil {
		t.Fatal(err)
	}
	if source.Header.ID != "legacy" || source.Header.Cwd != "/workspace" {
		t.Fatalf("header = %+v", source.Header)
	}
	structures := source.EntryStructures()
	if len(structures) != 3 {
		t.Fatalf("retained structures = %d", len(structures))
	}
	selectedID := structures[len(structures)-1].ID
	writes, err := source.Writes(ctx, func(id string) bool { return id == selectedID })
	if err != nil {
		t.Fatal(err)
	}
	var compaction *harnesstypes.CompactionEntry
	for index := range writes {
		if writes[index].Kind != "entry" {
			continue
		}
		if entry, ok := writes[index].Entry.(harnesstypes.CompactionEntry); ok {
			copy := entry
			compaction = &copy
		}
	}
	if compaction == nil {
		t.Fatalf("missing compaction write: %#v", writes)
	}
	if compaction.Summary != "selected" || !compaction.FromHook || compaction.TokensBefore != 20 {
		t.Fatalf("compaction = %+v", compaction)
	}
	if len(compaction.RetainedTail) != 1 || compaction.RetainedTail[0].Message == nil || compaction.RetainedTail[0].Message.User == nil {
		t.Fatalf("retained tail = %+v", compaction.RetainedTail)
	}
}

func TestLegacyV3UpgradeOnFirstCommit(t *testing.T) {
	fs := newTestFileSystem(t.TempDir())
	headerLine := `{"type":"session","version":3,"id":"legacy-upgrade","timestamp":"2023-11-14T22:13:20.000Z","cwd":"/workspace"}`
	messageLine := `{"type":"message","id":"old","parentId":null,"timestamp":"2023-11-14T22:13:20.000Z","message":{"role":"user","content":"old","timestamp":1700000000000}}`
	infoLine := `{"type":"session_info","id":"info","parentId":"old","timestamp":"2023-11-14T22:13:20.000Z","name":"legacy name"}`
	if result := fs.WriteFile("legacy.jsonl", []byte(headerLine+"\n"+messageLine+"\n"+infoLine+"\n"), ctx); !result.OK {
		t.Fatal(result.Error)
	}
	opts := jsonl.JsonlStorageOptions{FileSystem: fs, Path: "legacy.jsonl", Now: func() float64 { return 1700000000000 }}
	storage, err := jsonl.Open(opts, ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !storage.IsLegacyV3() {
		t.Fatal("expected legacy backing before upgrade")
	}
	name, ok, err := storage.GetValue(session.SessionName, ctx)
	if err != nil || !ok || name.Value != "legacy name" {
		t.Fatalf("legacy name = %v %v %v", name, ok, err)
	}
	if _, err := storage.Commit([]harnesstypes.Write{session.SetValue(session.NewValue("test.upgrade.value"), "after")}, ctx); err != nil {
		t.Fatal(err)
	}
	if err := storage.Close(ctx); err != nil {
		t.Fatal(err)
	}
	reopened, err := jsonl.Open(opts, ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close(ctx)
	if reopened.IsLegacyV3() {
		t.Fatal("expected upgraded format 4 backing")
	}
	entries, err := reopened.ScanEntries(harnesstypes.EntryScan{Order: orderPointer("asc")}, ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || session.EntryTypeOf(entries[0]) != harnesstypes.EntryTypeMessage {
		t.Fatalf("imported entries = %#v", entries)
	}
}
