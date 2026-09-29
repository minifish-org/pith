// This file carries jsonl/repo.ts: the file-backed format-4 session repository.
package jsonl

import (
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/minifish-org/pith/packages/agent/harness/session"
	harnesstypes "github.com/minifish-org/pith/packages/agent/harness/types"
	aiutils "github.com/minifish-org/pith/packages/ai/utils"
)

func metadataFromHeader(header JsonlStorageHeader, path string, modifiedAt float64) JsonlSessionMetadata {
	cwd := header.Cwd
	return JsonlSessionMetadata{
		SessionMetadata: harnesstypes.SessionMetadata{
			ID:                      header.ID,
			CreatedAt:               header.CreatedAt,
			StorageVersion:          header.StorageVersion,
			Cwd:                     &cwd,
			ParentSessionID:         header.ParentSessionID,
			LegacyParentSessionPath: header.LegacyParentSessionPath,
		},
		Path:       path,
		ModifiedAt: modifiedAt,
		Cwd:        header.Cwd,
	}
}

func sessionDirectoryName(cwd string) string {
	trimmed := strings.TrimPrefix(strings.TrimPrefix(cwd, "/"), "\\")
	replacer := strings.NewReplacer("/", "-", "\\", "-", ":", "-")
	return "--" + replacer.Replace(trimmed) + "--"
}

func sessionFileName(createdAt float64, id string) string {
	timestamp := time.UnixMilli(int64(createdAt)).UTC().Format("2006-01-02T15:04:05.000Z")
	timestamp = strings.NewReplacer(":", "-", ".", "-").Replace(timestamp)
	return timestamp + "_" + url.QueryEscape(id) + ".jsonl"
}

// JsonlSessionRepo is a file-backed format-4 session repository.
type JsonlSessionRepo struct {
	fileSystem     harnesstypes.FileSystem
	sessionsRoot   string
	now            func() float64
	mu             sync.Mutex
	openSessions   map[string]*JsonlStorage
	pendingCreates map[string]bool
	closed         bool
}

// NewJsonlSessionRepo builds a repository.
func NewJsonlSessionRepo(options JsonlSessionRepoOptions) *JsonlSessionRepo {
	now := options.Now
	if now == nil {
		now = defaultNow
	}
	return &JsonlSessionRepo{
		fileSystem:     options.FileSystem,
		sessionsRoot:   options.SessionsRoot,
		now:            now,
		openSessions:   map[string]*JsonlStorage{},
		pendingCreates: map[string]bool{},
	}
}

func (r *JsonlSessionRepo) assertOpen() error {
	if r.closed {
		return errors.New("JsonlSessionRepo is closed")
	}
	return nil
}

func (r *JsonlSessionRepo) sessionKey(cwd, id string) string { return cwd + "\x00" + id }

// Create creates a fresh session.
func (r *JsonlSessionRepo) Create(options JsonlSessionCreateOptions, ctx harnesstypes.Context) (harnesstypes.Session[JsonlSessionMetadata], error) {
	if err := r.assertOpen(); err != nil {
		return nil, err
	}
	createdAt := r.now()
	cwd, err := FileValue(r.fileSystem.AbsolutePath(options.Cwd, ctx), "Failed to resolve session cwd "+options.Cwd)
	if err != nil {
		return nil, err
	}
	id := ""
	if options.ID != nil {
		id = *options.ID
	} else {
		generated, uuidErr := aiutils.UUIDv7(&createdAt)
		if uuidErr != nil {
			return nil, uuidErr
		}
		id = generated
	}
	key := r.sessionKey(cwd, id)
	r.mu.Lock()
	if _, open := r.openSessions[key]; open || r.pendingCreates[key] {
		r.mu.Unlock()
		return nil, errors.New("Session already exists: " + id)
	}
	r.pendingCreates[key] = true
	r.mu.Unlock()
	var path string
	var storage *JsonlStorage
	defer func() {
		r.mu.Lock()
		delete(r.pendingCreates, key)
		r.mu.Unlock()
	}()
	path, err = r.resolveNewSessionPath(cwd, createdAt, id, ctx)
	if err != nil {
		return nil, err
	}
	header := JsonlStorageHeader{
		V:               JSONL_FORMAT_VERSION,
		Kind:            "header",
		ID:              id,
		StorageVersion:  JSONL_STORAGE_VERSION,
		CreatedAt:       createdAt,
		Cwd:             cwd,
		ParentSessionID: options.ParentSessionID,
	}
	storage, err = Create(JsonlStorageOptions{FileSystem: r.fileSystem, Path: path, Now: r.now}, header, nil, ctx)
	if err != nil {
		_, _ = FileValue(r.fileSystem.Remove(path, &harnesstypes.RemoveOptions{Force: boolPointer(true)}, ctx), "Failed to remove session")
		return nil, err
	}
	info, err := FileValue(r.fileSystem.FileInfo(path, ctx), "Failed to read session "+path)
	if err != nil {
		return nil, err
	}
	return r.publishOpenSession(metadataFromHeader(header, path, info.MtimeMs), storage, key)
}

// Open opens an existing session.
func (r *JsonlSessionRepo) Open(metadata JsonlSessionMetadata, ctx harnesstypes.Context) (harnesstypes.Session[JsonlSessionMetadata], error) {
	if err := r.assertOpen(); err != nil {
		return nil, err
	}
	key := r.sessionKey(metadata.Cwd, metadata.ID)
	r.mu.Lock()
	if _, open := r.openSessions[key]; open {
		r.mu.Unlock()
		return nil, errors.New("Session is already open: " + metadata.ID)
	}
	r.mu.Unlock()
	storage, err := r.loadStorage(metadata, ctx)
	if err != nil {
		return nil, err
	}
	return r.publishOpenSession(metadata, storage, key)
}

// List lists session metadata.
func (r *JsonlSessionRepo) List(options *JsonlSessionListOptions, ctx harnesstypes.Context) ([]JsonlSessionMetadata, error) {
	if err := r.assertOpen(); err != nil {
		return nil, err
	}
	var cwd *string
	if options != nil && options.Cwd != nil {
		resolved, err := FileValue(r.fileSystem.AbsolutePath(*options.Cwd, ctx), "Failed to resolve session cwd "+*options.Cwd)
		if err != nil {
			return nil, err
		}
		cwd = &resolved
	}
	root, err := r.root(ctx)
	if err != nil {
		return nil, err
	}
	exists, err := FileValue(r.fileSystem.Exists(root, ctx), "Failed to check sessions root "+root)
	if err != nil {
		return nil, err
	}
	if !exists {
		return []JsonlSessionMetadata{}, nil
	}
	var directories []string
	if cwd == nil {
		directories, err = r.sessionDirectories(root, ctx)
		if err != nil {
			return nil, err
		}
	} else {
		directory, dirErr := r.sessionDirectory(*cwd, ctx)
		if dirErr != nil {
			return nil, dirErr
		}
		directories = []string{directory}
	}
	metadata := []JsonlSessionMetadata{}
	for _, directory := range directories {
		found, listErr := r.listDirectory(directory, cwd, ctx)
		if listErr != nil {
			return nil, listErr
		}
		metadata = append(metadata, found...)
	}
	sort.SliceStable(metadata, func(i, j int) bool {
		if metadata[i].CreatedAt != metadata[j].CreatedAt {
			return metadata[i].CreatedAt > metadata[j].CreatedAt
		}
		if metadata[i].ID != metadata[j].ID {
			return metadata[i].ID < metadata[j].ID
		}
		return metadata[i].Cwd < metadata[j].Cwd
	})
	return metadata, nil
}

// Delete removes a closed session file.
func (r *JsonlSessionRepo) Delete(metadata JsonlSessionMetadata, ctx harnesstypes.Context) error {
	if err := r.assertOpen(); err != nil {
		return err
	}
	key := r.sessionKey(metadata.Cwd, metadata.ID)
	r.mu.Lock()
	if _, open := r.openSessions[key]; open {
		r.mu.Unlock()
		return errors.New("Session is open: " + metadata.ID)
	}
	r.mu.Unlock()
	exists, err := FileValue(r.fileSystem.Exists(metadata.Path, ctx), "Failed to check session "+metadata.Path)
	if err != nil {
		return err
	}
	if !exists {
		return errors.New("Session file does not exist: " + metadata.Path)
	}
	_, err = FileValue(r.fileSystem.Remove(metadata.Path, nil, ctx), "Failed to delete session "+metadata.Path)
	return err
}

// Fork copies a source session into a fresh destination.
func (r *JsonlSessionRepo) Fork(source JsonlSessionMetadata, options harnesstypes.ForkOptions, ctx harnesstypes.Context) (harnesstypes.Session[JsonlSessionMetadata], error) {
	if err := r.assertOpen(); err != nil {
		return nil, err
	}
	createdAt := r.now()
	cwd := source.Cwd
	id := ""
	if options.ID != nil {
		id = *options.ID
	} else {
		generated, uuidErr := aiutils.UUIDv7(&createdAt)
		if uuidErr != nil {
			return nil, uuidErr
		}
		id = generated
	}
	destinationKey := r.sessionKey(cwd, id)
	r.mu.Lock()
	if _, open := r.openSessions[destinationKey]; open || r.pendingCreates[destinationKey] {
		r.mu.Unlock()
		return nil, errors.New("Session already exists: " + id)
	}
	r.pendingCreates[destinationKey] = true
	sourceStorage := r.openSessions[r.sessionKey(source.Cwd, source.ID)]
	r.mu.Unlock()
	var path string
	var storage *JsonlStorage
	defer func() {
		r.mu.Lock()
		delete(r.pendingCreates, destinationKey)
		r.mu.Unlock()
	}()
	input, err := r.resolveForkInput(source, sourceStorage, ctx)
	if err != nil {
		return nil, err
	}
	path, err = r.resolveNewSessionPath(cwd, createdAt, id, ctx)
	if err != nil {
		return nil, err
	}
	header := JsonlStorageHeader{
		V:               JSONL_FORMAT_VERSION,
		Kind:            "header",
		ID:              id,
		StorageVersion:  JSONL_STORAGE_VERSION,
		CreatedAt:       createdAt,
		Cwd:             cwd,
		ParentSessionID: &source.ID,
	}
	err = RunJsonlFork(JsonlForkRunOptions{
		Input:             input,
		FileSystem:        r.fileSystem,
		DestinationPath:   path,
		DestinationHeader: header,
		Fork:              options,
	}, ctx)
	if err != nil {
		_, _ = FileValue(r.fileSystem.Remove(path, &harnesstypes.RemoveOptions{Force: boolPointer(true)}, ctx), "Failed to remove session")
		return nil, err
	}
	storage, err = Open(JsonlStorageOptions{FileSystem: r.fileSystem, Path: path, Now: r.now}, ctx)
	if err != nil {
		return nil, err
	}
	info, err := FileValue(r.fileSystem.FileInfo(path, ctx), "Failed to read session "+path)
	if err != nil {
		return nil, err
	}
	return r.publishOpenSession(metadataFromHeader(header, path, info.MtimeMs), storage, destinationKey)
}

// Close seals repository admission.
func (r *JsonlSessionRepo) Close(ctx harnesstypes.Context) error {
	r.mu.Lock()
	r.closed = true
	r.mu.Unlock()
	return nil
}

func (r *JsonlSessionRepo) sessionDirectory(cwd string, ctx harnesstypes.Context) (string, error) {
	root, err := r.root(ctx)
	if err != nil {
		return "", err
	}
	return FileValue(r.fileSystem.JoinPath([]string{root, sessionDirectoryName(cwd)}, ctx), "Failed to resolve sessions directory for "+cwd)
}

func (r *JsonlSessionRepo) sessionDirectories(root string, ctx harnesstypes.Context) ([]string, error) {
	entries, err := FileValue(r.fileSystem.ListDir(root, ctx), "Failed to list sessions root "+root)
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, entry := range entries {
		if entry.Kind == harnesstypes.FileKindDirectory {
			out = append(out, entry.Path)
		}
	}
	return out, nil
}

func (r *JsonlSessionRepo) listDirectory(directory string, cwd *string, ctx harnesstypes.Context) ([]JsonlSessionMetadata, error) {
	exists, err := FileValue(r.fileSystem.Exists(directory, ctx), "Failed to check sessions directory "+directory)
	if err != nil {
		return nil, err
	}
	if !exists {
		return []JsonlSessionMetadata{}, nil
	}
	files, err := FileValue(r.fileSystem.ListDir(directory, ctx), "Failed to list sessions directory "+directory)
	if err != nil {
		return nil, err
	}
	metadata := []JsonlSessionMetadata{}
	for _, file := range files {
		if file.Kind == harnesstypes.FileKindDirectory || !strings.HasSuffix(file.Name, ".jsonl") {
			continue
		}
		discovered, readErr := r.readSessionMetadata(file, ctx)
		if readErr != nil {
			return nil, readErr
		}
		if discovered == nil {
			continue
		}
		if cwd == nil || discovered.Cwd == *cwd {
			metadata = append(metadata, *discovered)
		}
	}
	return metadata, nil
}

func (r *JsonlSessionRepo) readSessionMetadata(file harnesstypes.FileInfo, ctx harnesstypes.Context) (*JsonlSessionMetadata, error) {
	lines, err := FileValue(r.fileSystem.ReadTextLines(file.Path, &harnesstypes.ReadTextLinesOptions{MaxLines: intPointer(1)}, ctx), "Failed to read session header "+file.Path)
	if err != nil {
		return nil, err
	}
	if len(lines) == 0 || lines[0] == "" {
		return nil, nil
	}
	parsed, parseErr := ParseJsonlSessionHeader(lines[0])
	if parseErr != nil {
		return nil, nil
	}
	if parsed.Format == "v3-legacy" && parsed.V3 != nil {
		metadata, metaErr := MetadataFromLegacyV3Header(r.fileSystem, *parsed.V3, ctx)
		if metaErr != nil {
			return nil, metaErr
		}
		metadata.Path = file.Path
		metadata.ModifiedAt = file.MtimeMs
		return &metadata, nil
	}
	if parsed.V4 == nil {
		return nil, nil
	}
	metadata := metadataFromHeader(*parsed.V4, file.Path, file.MtimeMs)
	return &metadata, nil
}

func (r *JsonlSessionRepo) resolveNewSessionPath(cwd string, createdAt float64, id string, ctx harnesstypes.Context) (string, error) {
	directory, err := r.sessionDirectory(cwd, ctx)
	if err != nil {
		return "", err
	}
	if err := r.assertSessionIDAvailable(directory, id, ctx); err != nil {
		return "", err
	}
	if _, err := FileValue(r.fileSystem.CreateDir(directory, nil, ctx), "Failed to create sessions directory "+directory); err != nil {
		return "", err
	}
	return FileValue(r.fileSystem.JoinPath([]string{directory, sessionFileName(createdAt, id)}, ctx), "Failed to resolve path for session "+id)
}

func (r *JsonlSessionRepo) assertSessionIDAvailable(directory, id string, ctx harnesstypes.Context) error {
	exists, err := FileValue(r.fileSystem.Exists(directory, ctx), "Failed to check sessions directory "+directory)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	suffix := "_" + url.QueryEscape(id) + ".jsonl"
	entries, err := FileValue(r.fileSystem.ListDir(directory, ctx), "Failed to list sessions directory "+directory)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Kind != harnesstypes.FileKindDirectory && strings.HasSuffix(entry.Name, suffix) {
			return errors.New("Session already exists: " + id)
		}
	}
	return nil
}

func (r *JsonlSessionRepo) resolveForkInput(source JsonlSessionMetadata, storage *JsonlStorage, ctx harnesstypes.Context) (JsonlForkInput, error) {
	if storage != nil {
		if storage.IsLegacyV3() {
			return JsonlForkInput{}, errors.New("Cannot fork an open legacy v3 JSONL session; commit a non-empty transaction to upgrade it to format 4 first")
		}
		nextSeq, err := storage.CaptureForkNextSeq(ctx)
		if err != nil {
			return JsonlForkInput{}, err
		}
		return JsonlForkInput{Kind: "open", Metadata: JsonlForkSourceMetadata{ID: source.ID, Cwd: source.Cwd, Path: source.Path}, NextSeq: nextSeq}, nil
	}
	legacy, err := r.isLegacyV3ForkSource(source, ctx)
	if err != nil {
		return JsonlForkInput{}, err
	}
	if legacy {
		normalized, readErr := ReadLegacyV3Source(r.fileSystem, source.Path, ctx)
		if readErr != nil {
			return JsonlForkInput{}, readErr
		}
		if normalized.Header.ID != source.ID || normalized.Header.Cwd != source.Cwd {
			return JsonlForkInput{}, errors.New("Session identity does not match header: " + source.ID)
		}
		return JsonlForkInput{Kind: "legacy-v3", Normalized: normalized}, nil
	}
	return JsonlForkInput{Kind: "closed", Metadata: JsonlForkSourceMetadata{ID: source.ID, Cwd: source.Cwd, Path: source.Path}}, nil
}

func (r *JsonlSessionRepo) isLegacyV3ForkSource(source JsonlSessionMetadata, ctx harnesstypes.Context) (bool, error) {
	lines, err := FileValue(r.fileSystem.ReadTextLines(source.Path, &harnesstypes.ReadTextLinesOptions{MaxLines: intPointer(1)}, ctx), "Failed to read session header "+source.Path)
	if err != nil {
		return false, err
	}
	if len(lines) == 0 || lines[0] == "" {
		return false, nil
	}
	parsed, parseErr := ParseJsonlSessionHeader(lines[0])
	if parseErr != nil {
		return false, nil
	}
	return parsed.Format == "v3-legacy", nil
}

func (r *JsonlSessionRepo) publishOpenSession(metadata JsonlSessionMetadata, storage *JsonlStorage, key string) (harnesstypes.Session[JsonlSessionMetadata], error) {
	r.mu.Lock()
	if _, open := r.openSessions[key]; open {
		r.mu.Unlock()
		return nil, errors.New("Session is already open: " + metadata.ID)
	}
	r.openSessions[key] = storage
	r.mu.Unlock()
	session := session.NewStorageBackedSession(metadata, storage, session.StorageBackedSessionOptions{
		OnClose: func() {
			r.mu.Lock()
			if r.openSessions[key] == storage {
				delete(r.openSessions, key)
			}
			r.mu.Unlock()
		},
	})
	return session, nil
}

func (r *JsonlSessionRepo) loadStorage(metadata JsonlSessionMetadata, ctx harnesstypes.Context) (*JsonlStorage, error) {
	exists, err := FileValue(r.fileSystem.Exists(metadata.Path, ctx), "Failed to check session "+metadata.Path)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, errors.New("Session file does not exist: " + metadata.Path)
	}
	storage, err := Open(JsonlStorageOptions{FileSystem: r.fileSystem, Path: metadata.Path, Now: r.now}, ctx)
	if err != nil {
		return nil, err
	}
	if storage.Header.ID != metadata.ID || storage.Header.Cwd != metadata.Cwd {
		_ = storage.Close(ctx)
		return nil, errors.New("Session identity does not match header: " + metadata.ID)
	}
	if storage.Header.StorageVersion != JSONL_STORAGE_VERSION {
		_ = storage.Close(ctx)
		return nil, fmt.Errorf("Session %s uses unsupported storage version %d", metadata.ID, storage.Header.StorageVersion)
	}
	return storage, nil
}

func (r *JsonlSessionRepo) root(ctx harnesstypes.Context) (string, error) {
	return FileValue(r.fileSystem.AbsolutePath(r.sessionsRoot, ctx), "Failed to resolve sessions root "+r.sessionsRoot)
}
