// This file carries jsonl/storage.ts: JSONL-backed durable storage.
package jsonl

import (
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/minifish-org/pith/packages/agent/harness/session"
	harnesstypes "github.com/minifish-org/pith/packages/agent/harness/types"
	aiutils "github.com/minifish-org/pith/packages/ai/utils"
)

func splitCompleteLines(content string) (lines []string, torn bool) {
	if strings.HasSuffix(content, "\n") {
		return strings.Split(content[:len(content)-1], "\n"), false
	}
	lastNewline := strings.LastIndex(content, "\n")
	if lastNewline == -1 {
		return []string{}, true
	}
	return strings.Split(content[:lastNewline], "\n"), true
}

// JsonlStorage is JSONL storage backed by an injected filesystem capability.
type JsonlStorage struct {
	fileSystem harnesstypes.FileSystem
	path       string
	now        func() float64

	Header  JsonlStorageHeader
	backing string // "v4" or "v3"
	source  *LegacyV3Source
	state   *session.InMemoryStorageState

	mu     sync.Mutex
	closed bool
}

// IsLegacyV3 reports whether the storage is still backed by a legacy v3 file.
func (s *JsonlStorage) IsLegacyV3() bool { return s.backing == "v3" }

// Create builds a fresh format-4 storage and publishes its header.
func Create(options JsonlStorageOptions, header JsonlStorageHeader, initialWrites []harnesstypes.Write, ctx harnesstypes.Context) (*JsonlStorage, error) {
	now := options.Now
	if now == nil {
		now = defaultNow
	}
	storage := &JsonlStorage{
		fileSystem: options.FileSystem,
		path:       options.Path,
		now:        now,
		Header:     header,
		backing:    "v4",
		state:      session.NewInMemoryStorageState(),
	}
	prepared, err := storage.state.PrepareCommit(initialWrites, storage.now())
	if err != nil {
		return nil, err
	}
	err = PublishJsonl(options.FileSystem, options.Path, header, ctx, func(append func(writes []session.CommittedWrite) error) error {
		if len(prepared.Writes) == 0 {
			return nil
		}
		return append(prepared.Writes)
	})
	if err != nil {
		return nil, err
	}
	storage.state.ApplyValidated(prepared.Writes)
	return storage, nil
}

// Open reads an existing JSONL storage.
func Open(options JsonlStorageOptions, ctx harnesstypes.Context) (*JsonlStorage, error) {
	reader, err := FileValue(options.FileSystem.OpenTextLineReader(options.Path, ctx), "Failed to read JSONL storage "+options.Path)
	if err != nil {
		return nil, err
	}
	parsed, parseErr := ReadJsonlHeader(reader, options.Path, ctx)
	reader.Close(ctx)
	if parseErr != nil {
		return nil, parseErr
	}
	if parsed.Format == "v3-legacy" {
		return openLegacyV3(options, ctx)
	}
	if parsed.V4 == nil {
		return nil, errors.New("Invalid JSONL session header")
	}
	return openV4(options, *parsed.V4, ctx)
}

func openV4(options JsonlStorageOptions, header JsonlStorageHeader, ctx harnesstypes.Context) (*JsonlStorage, error) {
	content, err := FileValue(options.FileSystem.ReadTextFile(options.Path, ctx), "Failed to read JSONL storage "+options.Path)
	if err != nil {
		return nil, err
	}
	lines, torn := splitCompleteLines(content)
	if header.StorageVersion != JSONL_STORAGE_VERSION {
		return nil, errors.New("Session " + header.ID + " uses unsupported storage version")
	}
	now := options.Now
	if now == nil {
		now = defaultNow
	}
	storage := &JsonlStorage{
		fileSystem: options.FileSystem,
		path:       options.Path,
		now:        now,
		Header:     header,
		backing:    "v4",
		state:      session.NewInMemoryStorageState(),
	}
	for index := 1; index < len(lines); index++ {
		writes, err := ParseJsonlTransaction(lines[index])
		if err != nil {
			return nil, errors.New("Invalid JSONL storage " + options.Path + ": line " + strconv.Itoa(index+1) + ": " + err.Error())
		}
		if err := storage.replayCommitted(writes); err != nil {
			return nil, err
		}
	}
	if header.NextSeq != nil {
		if err := storage.state.AdvanceNextSeq(*header.NextSeq); err != nil {
			return nil, err
		}
	}
	if torn {
		joined := strings.Join(lines, "\n") + "\n"
		err := PublishFileAtomically(options.FileSystem, options.Path, ctx, func(appendContent func(content string) error) error {
			return appendContent(joined)
		})
		if err != nil {
			return nil, err
		}
	}
	return storage, nil
}

func openLegacyV3(options JsonlStorageOptions, ctx harnesstypes.Context) (*JsonlStorage, error) {
	source, err := ReadLegacyV3Source(options.FileSystem, options.Path, ctx)
	if err != nil {
		return nil, err
	}
	now := options.Now
	if now == nil {
		now = defaultNow
	}
	header := source.Header
	storage := &JsonlStorage{
		fileSystem: options.FileSystem,
		path:       options.Path,
		now:        now,
		Header:     header,
		backing:    "v3",
		source:     source,
		state:      session.NewInMemoryStorageState(),
	}
	for _, write := range source.AllWrites() {
		if err := storage.replayCommitted([]session.CommittedWrite{write}); err != nil {
			return nil, err
		}
	}
	return storage, nil
}

func (s *JsonlStorage) replayCommitted(writes []session.CommittedWrite) error {
	if err := s.state.ValidateCommitted(writes); err != nil {
		return err
	}
	s.state.ApplyValidated(writes)
	return nil
}

// Commit appends one transaction.
func (s *JsonlStorage) Commit(writes []harnesstypes.Write, ctx harnesstypes.Context) (harnesstypes.CommitResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return harnesstypes.CommitResult{}, errors.New("JsonlStorage is closed")
	}
	if s.backing == "v3" && len(writes) != 0 {
		return s.upgradeLegacyV3ToV4(writes, ctx)
	}
	prepared, err := s.state.PrepareCommit(writes, s.now())
	if err != nil {
		return harnesstypes.CommitResult{}, err
	}
	if len(prepared.Writes) != 0 {
		line, err := SerializeJsonlTransaction(prepared.Writes)
		if err != nil {
			return harnesstypes.CommitResult{}, err
		}
		if _, err := FileValue(s.fileSystem.AppendFile(s.path, []byte(line+"\n"), ctx), "Failed to append JSONL storage "+s.path); err != nil {
			return harnesstypes.CommitResult{}, err
		}
	}
	stats := s.state.ApplyValidated(prepared.Writes)
	return harnesstypes.CommitResult{
		FirstSeq:  prepared.Result.FirstSeq,
		Seqs:      prepared.Result.Seqs,
		Timestamp: prepared.Result.Timestamp,
		Stats:     s.withImportedUsage(stats),
	}, nil
}

func (s *JsonlStorage) upgradeLegacyV3ToV4(callerWrites []harnesstypes.Write, ctx harnesstypes.Context) (harnesstypes.CommitResult, error) {
	timestamp := s.now()
	usageID, err := aiutils.UUIDv7(&timestamp)
	if err != nil {
		return harnesstypes.CommitResult{}, err
	}
	importWrite := session.InsertUsage(harnesstypes.UsageRow{
		ID:         usageID,
		Usage:      s.source.ImportedUsage,
		Adjustment: true,
		Details:    map[string]any{"source": "v3-import"},
	})
	all := append([]harnesstypes.Write{importWrite}, callerWrites...)
	prepared, err := s.state.PrepareCommit(all, timestamp)
	if err != nil {
		return harnesstypes.CommitResult{}, err
	}
	nextSeq := prepared.Result.FirstSeq + len(prepared.Writes)
	upgradedHeader := s.Header
	upgradedHeader.NextSeq = &nextSeq
	err = PublishJsonl(s.fileSystem, s.path, upgradedHeader, ctx, func(append func(writes []session.CommittedWrite) error) error {
		for _, write := range s.source.AllWrites() {
			if err := append([]session.CommittedWrite{write}); err != nil {
				return err
			}
		}
		if len(prepared.Writes) > 0 {
			if err := append(prepared.Writes); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return harnesstypes.CommitResult{}, err
	}
	stats := s.state.ApplyValidated(prepared.Writes)
	s.backing = "v4"
	s.source = nil
	firstSeq := prepared.Result.FirstSeq + 1
	seqs := prepared.Result.Seqs
	if len(seqs) > 0 {
		seqs = seqs[1:]
	}
	return harnesstypes.CommitResult{FirstSeq: firstSeq, Seqs: seqs, Timestamp: prepared.Result.Timestamp, Stats: stats}, nil
}

// GetEntries resolves entries by id.
func (s *JsonlStorage) GetEntries(ids []string, ctx harnesstypes.Context) (map[string]harnesstypes.Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("JsonlStorage is closed")
	}
	return s.state.GetEntries(ids), nil
}

// GetValue resolves one scalar value.
func (s *JsonlStorage) GetValue(address harnesstypes.Value, ctx harnesstypes.Context) (harnesstypes.StoredValue, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return harnesstypes.StoredValue{}, false, errors.New("JsonlStorage is closed")
	}
	value, ok := s.state.GetValue(address)
	return value, ok, nil
}

// ScanValues scans scalar values by namespace prefix.
func (s *JsonlStorage) ScanValues(prefix harnesstypes.Value, ctx harnesstypes.Context) ([]harnesstypes.StoredValue, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("JsonlStorage is closed")
	}
	return s.state.ScanValues(prefix), nil
}

// ReadList reads one page of a list.
func (s *JsonlStorage) ReadList(address harnesstypes.ValueList, options *harnesstypes.ListReadOptions, ctx harnesstypes.Context) ([]harnesstypes.ListElement, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("JsonlStorage is closed")
	}
	return s.state.ReadList(address, options)
}

// ScanBranch scans one branch.
func (s *JsonlStorage) ScanBranch(query harnesstypes.StorageBranchScan, ctx harnesstypes.Context) ([]harnesstypes.Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("JsonlStorage is closed")
	}
	return s.state.ScanBranch(query)
}

// ScanBranchStructure scans one branch structure.
func (s *JsonlStorage) ScanBranchStructure(query harnesstypes.StorageBranchScan, ctx harnesstypes.Context) ([]harnesstypes.EntryStructure, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("JsonlStorage is closed")
	}
	return s.state.ScanBranchStructure(query)
}

// ScanEntries scans the global entry index.
func (s *JsonlStorage) ScanEntries(query harnesstypes.EntryScan, ctx harnesstypes.Context) ([]harnesstypes.Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("JsonlStorage is closed")
	}
	return s.state.ScanEntries(query), nil
}

// ScanUsage scans the usage ledger.
func (s *JsonlStorage) ScanUsage(query harnesstypes.UsageScan, ctx harnesstypes.Context) ([]harnesstypes.UsageRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("JsonlStorage is closed")
	}
	return s.state.ScanUsage(query), nil
}

// GetStats returns the session totals.
func (s *JsonlStorage) GetStats(ctx harnesstypes.Context) (harnesstypes.SessionStats, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return harnesstypes.SessionStats{}, errors.New("JsonlStorage is closed")
	}
	return s.withImportedUsage(s.state.GetStats()), nil
}

func (s *JsonlStorage) withImportedUsage(stats harnesstypes.SessionStats) harnesstypes.SessionStats {
	if s.backing == "v4" || s.source == nil {
		return stats
	}
	stats.Usage = s.source.ImportedUsage
	return stats
}

// CaptureForkNextSeq returns the first sequence a later source commit would
// use.
func (s *JsonlStorage) CaptureForkNextSeq(ctx harnesstypes.Context) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return 0, errors.New("JsonlStorage is closed")
	}
	return s.state.GetNextSeq(), nil
}

// Close seals admission and marks the storage closed.
func (s *JsonlStorage) Close(ctx harnesstypes.Context) error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	return nil
}

func defaultNow() float64 {
	return float64(time.Now().UnixMilli())
}
