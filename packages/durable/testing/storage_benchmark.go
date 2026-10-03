package durabletesting

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/minifish-org/pith/packages/chord"
	"github.com/minifish-org/pith/packages/durable"
)

// StorageBenchmarkScale names a representative dataset size for the storage
// benchmarks. It mirrors the source StorageBenchmarkScale.
type StorageBenchmarkScale struct {
	Name          string
	EntryCount    int
	TaskCount     int
	DocumentCount int
}

// StorageMemoryScales are the small in-memory scales from the source.
var StorageMemoryScales = []StorageBenchmarkScale{
	{Name: "1k", EntryCount: 1_000, TaskCount: 200, DocumentCount: 200},
	{Name: "10k", EntryCount: 10_000, TaskCount: 2_000, DocumentCount: 2_000},
}

// TimingScale is the default dataset used by the timing benchmarks.
var TimingScale = StorageBenchmarkScale{Name: "timing", EntryCount: 1_000, TaskCount: 300, DocumentCount: 300}

// Benchmark constants from the source seed algorithm.
const (
	benchmarkHistorySegmentLength = 128
	benchmarkForkDepth            = 8
	benchmarkEntriesPerFork       = 32
	benchmarkBatchSize            = 100
)

// BenchmarkReplayTails are the rewindable-document tails seeded by
// SeedStorageBenchmark.
var BenchmarkReplayTails = []int{0, 16, 128, 1_024}

// StorageBenchmarkPrimaryRecordCount is the number of primary records a scale
// seeds: root + entries + tasks + documents + replay tails + historical base +
// fork conversations and their entries.
func StorageBenchmarkPrimaryRecordCount(scale StorageBenchmarkScale) int {
	return 1 +
		scale.EntryCount +
		scale.TaskCount +
		scale.DocumentCount +
		len(BenchmarkReplayTails) +
		1 +
		benchmarkForkDepth*(1+benchmarkEntriesPerFork)
}

// StorageBenchmarkDataset is the deterministic handle set produced by
// SeedStorageBenchmark for the read benchmarks.
type StorageBenchmarkDataset struct {
	FirstEntryID          durable.EntryID
	FilteredTaskCount     int
	ExactDocumentID       durable.DocumentID
	ExactDocumentKey      string
	ReplayDocumentIDs     map[int]durable.DocumentID
	HistoricalDocumentID  durable.DocumentID
	AncientAt             durable.Seq
	RecentAt              durable.Seq
	DeepestConversationID durable.ConversationID
	AncestorHeadEntryID   durable.EntryID
}

func benchmarkTask(id durable.TaskID, index int) durable.TaskRecord {
	statuses := []string{durable.TaskStatusPending, durable.TaskStatusRunning, durable.TaskStatusTerminal}
	status := statuses[index%len(statuses)]
	task := durable.TaskRecord{
		ID:             id,
		ConversationID: durable.RootConversationID,
		Version:        1,
		Input:          json.RawMessage(fmt.Sprintf(`{"index":%d}`, index)),
		Background:     index%5 == 0,
		AbortRequested: index%7 == 0,
	}
	if index%4 == 0 {
		task.Kind = "benchmark.filtered"
	} else {
		task.Kind = "benchmark.other"
	}
	if status == durable.TaskStatusTerminal {
		task.State = durable.TaskState{Status: status, Outcome: &durable.TaskOutcome{Status: durable.OutcomeCompleted, Result: json.RawMessage(fmt.Sprintf(`{"index":%d}`, index))}}
	} else {
		task.State = durable.TaskState{Status: status, Checkpoint: json.RawMessage(fmt.Sprintf(`{"index":%d,"payload":%q}`, index, strings.Repeat("x", 64)))}
	}
	return task
}

// SeedStorageBenchmark seeds deterministic representative data through only the
// public durable.Storage contract and returns the handles the read benchmarks
// need. It is the native port of seedStorageBenchmark.
func SeedStorageBenchmark(ctx context.Context, storage durable.Storage, scale StorageBenchmarkScale) (StorageBenchmarkDataset, error) {
	var dataset StorageBenchmarkDataset
	if _, err := storage.Commit(ctx, []durable.StorageWrite{{Type: durable.WriteConversation, Conversation: &durable.ConversationRecord{ID: durable.RootConversationID}}}); err != nil {
		return dataset, err
	}

	var firstEntryID durable.EntryID
	for start := 0; start < scale.EntryCount; start += benchmarkBatchSize {
		writes := make([]durable.StorageWrite, 0, benchmarkBatchSize)
		for index := start; index < minInt(start+benchmarkBatchSize, scale.EntryCount); index++ {
			id, err := storage.MintID(ctx)
			if err != nil {
				return dataset, err
			}
			if index == 0 {
				firstEntryID = durable.EntryID(id)
			}
			entry := durable.EntryRecord{
				ID:             durable.EntryID(id),
				ConversationID: durable.RootConversationID,
				Kind:           "benchmark.entry",
				Data:           json.RawMessage(fmt.Sprintf(`{"index":%d,"text":%q}`, index, fmt.Sprintf("entry-%d-%s", index, strings.Repeat("x", 96)))),
			}
			if index == 0 {
				entry.Head = ptr(durable.EntryID(id))
			}
			writes = append(writes, durable.StorageWrite{Type: durable.WriteEntry, Entry: &entry})
		}
		if _, err := storage.Commit(ctx, writes); err != nil {
			return dataset, err
		}
	}

	for start := 0; start < scale.TaskCount; start += benchmarkBatchSize {
		writes := make([]durable.StorageWrite, 0, benchmarkBatchSize)
		for index := start; index < minInt(start+benchmarkBatchSize, scale.TaskCount); index++ {
			id, err := storage.MintID(ctx)
			if err != nil {
				return dataset, err
			}
			task := benchmarkTask(durable.TaskID(id), index)
			writes = append(writes, durable.StorageWrite{Type: durable.WriteTask, Task: &task})
		}
		if _, err := storage.Commit(ctx, writes); err != nil {
			return dataset, err
		}
	}

	var exactDocumentID durable.DocumentID
	for start := 0; start < scale.DocumentCount; start += benchmarkBatchSize {
		writes := make([]durable.StorageWrite, 0, benchmarkBatchSize)
		for index := start; index < minInt(start+benchmarkBatchSize, scale.DocumentCount); index++ {
			id, err := storage.MintID(ctx)
			if err != nil {
				return dataset, err
			}
			exactDocumentID = durable.DocumentID(id)
			record := durable.DocumentCreate{ID: durable.DocumentID(id), Kind: "benchmark.family", Key: ptr(fmt.Sprintf("key-%d", index)), Scope: durable.DocumentScope{Kind: durable.ScopeSession}}
			writes = append(writes, durable.StorageWrite{Type: durable.WriteDocumentCreate, Record: &record, Content: &durable.DocumentContent{Kind: durable.ContentBase, Version: 1, Value: durable.JsonObject{"index": index, "text": strings.Repeat("x", 128)}}})
		}
		if _, err := storage.Commit(ctx, writes); err != nil {
			return dataset, err
		}
	}

	replayIDs := make(map[int]durable.DocumentID, len(BenchmarkReplayTails))
	replayWrites := make([]durable.StorageWrite, 0, len(BenchmarkReplayTails))
	for _, tail := range BenchmarkReplayTails {
		id, err := storage.MintID(ctx)
		if err != nil {
			return dataset, err
		}
		replayIDs[tail] = durable.DocumentID(id)
		record := durable.DocumentCreate{
			ID:      durable.DocumentID(id),
			Kind:    "benchmark.replay",
			Key:     ptr(fmt.Sprintf("%d", tail)),
			Scope:   durable.DocumentScope{Kind: durable.ScopeConversation, ConversationID: durable.RootConversationID},
			History: durable.HistoryRewindable,
			Fork:    durable.ForkAsOf,
		}
		replayWrites = append(replayWrites, durable.StorageWrite{Type: durable.WriteDocumentCreate, Record: &record, Content: &durable.DocumentContent{Kind: durable.ContentBase, Version: 1, Value: durable.JsonObject{"count": 0, "text": strings.Repeat("x", 64)}}})
	}
	if _, err := storage.Commit(ctx, replayWrites); err != nil {
		return dataset, err
	}
	maxTail := BenchmarkReplayTails[len(BenchmarkReplayTails)-1]
	for count := 1; count <= maxTail; count++ {
		changes := make([]durable.StorageWrite, 0, len(BenchmarkReplayTails))
		for _, tail := range BenchmarkReplayTails {
			if count <= tail {
				changes = append(changes, durable.StorageWrite{Type: durable.WriteDocumentChange, ID: replayIDs[tail], Content: &durable.DocumentContent{Kind: durable.ContentDelta, Version: 1, Ops: []chord.Op{{"s", []any{"count"}, count}}}})
			}
		}
		if _, err := storage.Commit(ctx, changes); err != nil {
			return dataset, err
		}
	}

	historicalID, err := storage.MintID(ctx)
	if err != nil {
		return dataset, err
	}
	dataset.HistoricalDocumentID = durable.DocumentID(historicalID)
	historicalRecord := durable.DocumentCreate{
		ID:      durable.DocumentID(historicalID),
		Kind:    "benchmark.history",
		Scope:   durable.DocumentScope{Kind: durable.ScopeConversation, ConversationID: durable.RootConversationID},
		History: durable.HistoryRewindable,
		Fork:    durable.ForkAsOf,
	}
	if _, err := storage.Commit(ctx, []durable.StorageWrite{{Type: durable.WriteDocumentCreate, Record: &historicalRecord, Content: &durable.DocumentContent{Kind: durable.ContentBase, Version: 1, Value: durable.JsonObject{"count": 0}}}}); err != nil {
		return dataset, err
	}
	var ancientAt durable.Seq
	for count := 1; count <= benchmarkHistorySegmentLength; count++ {
		seq, err := storage.Commit(ctx, []durable.StorageWrite{{Type: durable.WriteDocumentChange, ID: durable.DocumentID(historicalID), Content: &durable.DocumentContent{Kind: durable.ContentDelta, Version: 1, Ops: []chord.Op{{"s", []any{"count"}, count}}}}})
		if err != nil {
			return dataset, err
		}
		ancientAt = seq
	}
	if _, err := storage.Commit(ctx, []durable.StorageWrite{{Type: durable.WriteDocumentChange, ID: durable.DocumentID(historicalID), Content: &durable.DocumentContent{Kind: durable.ContentBase, Version: 1, Value: durable.JsonObject{"count": benchmarkHistorySegmentLength}}}}); err != nil {
		return dataset, err
	}
	recentAt := ancientAt
	for count := benchmarkHistorySegmentLength + 1; count <= benchmarkHistorySegmentLength*2; count++ {
		seq, err := storage.Commit(ctx, []durable.StorageWrite{{Type: durable.WriteDocumentChange, ID: durable.DocumentID(historicalID), Content: &durable.DocumentContent{Kind: durable.ContentDelta, Version: 1, Ops: []chord.Op{{"s", []any{"count"}, count}}}}})
		if err != nil {
			return dataset, err
		}
		recentAt = seq
	}

	if scale.EntryCount == 0 {
		return dataset, fmt.Errorf("benchmark scale must create entries")
	}
	parentConversationID := durable.RootConversationID
	parentAt := firstEntryID
	deepestConversationID := durable.RootConversationID
	for depth := 0; depth < benchmarkForkDepth; depth++ {
		conversationID, err := storage.MintID(ctx)
		if err != nil {
			return dataset, err
		}
		if _, err := storage.Commit(ctx, []durable.StorageWrite{{Type: durable.WriteConversation, Conversation: &durable.ConversationRecord{ID: durable.ConversationID(conversationID), Parent: &durable.ConversationParent{ConversationID: parentConversationID, At: parentAt}}}}); err != nil {
			return dataset, err
		}
		ids := make([]durable.ID, 0, benchmarkEntriesPerFork)
		for index := 0; index < benchmarkEntriesPerFork; index++ {
			id, err := storage.MintID(ctx)
			if err != nil {
				return dataset, err
			}
			ids = append(ids, id)
		}
		writes := make([]durable.StorageWrite, 0, len(ids))
		for index, id := range ids {
			entry := durable.EntryRecord{ID: durable.EntryID(id), ConversationID: durable.ConversationID(conversationID), Kind: "benchmark.fork", Data: json.RawMessage(fmt.Sprintf(`{"depth":%d,"index":%d}`, depth, index))}
			writes = append(writes, durable.StorageWrite{Type: durable.WriteEntry, Entry: &entry})
		}
		if _, err := storage.Commit(ctx, writes); err != nil {
			return dataset, err
		}
		parentConversationID = durable.ConversationID(conversationID)
		parentAt = durable.EntryID(ids[len(ids)-1])
		deepestConversationID = durable.ConversationID(conversationID)
	}

	if scale.DocumentCount == 0 {
		return dataset, fmt.Errorf("benchmark scale must create documents")
	}
	dataset.FirstEntryID = firstEntryID
	dataset.FilteredTaskCount = minInt(50, (scale.TaskCount+59)/60)
	dataset.ExactDocumentID = exactDocumentID
	dataset.ExactDocumentKey = fmt.Sprintf("key-%d", scale.DocumentCount-1)
	dataset.ReplayDocumentIDs = replayIDs
	dataset.AncientAt = ancientAt
	dataset.RecentAt = recentAt
	dataset.DeepestConversationID = deepestConversationID
	dataset.AncestorHeadEntryID = firstEntryID
	return dataset, nil
}

// StorageReadBenchmark is one named read micro-benchmark. Run returns the
// observed value; Expected returns the value the adapter must produce.
type StorageReadBenchmark struct {
	Name     string
	Run      func(context.Context, durable.Storage, StorageBenchmarkDataset) (int, error)
	Expected func(StorageBenchmarkDataset) int
}

// StorageReadBenchmarks are the source read benchmark definitions in order.
var StorageReadBenchmarks = buildStorageReadBenchmarks()

func buildStorageReadBenchmarks() []StorageReadBenchmark {
	benchmarks := []StorageReadBenchmark{
		{
			Name: "exact entry lookup",
			Run: func(ctx context.Context, storage durable.Storage, dataset StorageBenchmarkDataset) (int, error) {
				entry, err := storage.Entry(ctx, dataset.FirstEntryID)
				if err != nil {
					return 0, err
				}
				if entry == nil {
					return -1, nil
				}
				return int(entry.Entry.ID), nil
			},
			Expected: func(dataset StorageBenchmarkDataset) int { return int(dataset.FirstEntryID) },
		},
		{
			Name: "entry page scan (100)",
			Run: func(ctx context.Context, storage durable.Storage, _ StorageBenchmarkDataset) (int, error) {
				page, err := storage.ScanEntries(ctx, durable.EntryQuery{ConversationID: durable.RootConversationID}, 100, nil)
				if err != nil {
					return 0, err
				}
				return len(page.Items), nil
			},
			Expected: func(StorageBenchmarkDataset) int { return 100 },
		},
		{
			Name: "filtered task scan (50)",
			Run: func(ctx context.Context, storage durable.Storage, _ StorageBenchmarkDataset) (int, error) {
				page, err := storage.ScanTasks(ctx, durable.TaskQuery{Kind: "benchmark.filtered", Status: durable.TaskStatusPending, Background: ptr(true)}, 50, nil)
				if err != nil {
					return 0, err
				}
				return len(page.Items), nil
			},
			Expected: func(dataset StorageBenchmarkDataset) int { return dataset.FilteredTaskCount },
		},
		{
			Name: "exact document address among many",
			Run: func(ctx context.Context, storage durable.Storage, dataset StorageBenchmarkDataset) (int, error) {
				record, err := storage.FindDocument(ctx, durable.DocumentAddress{Kind: "benchmark.family", Key: ptr(dataset.ExactDocumentKey), Scope: durable.DocumentScope{Kind: durable.ScopeSession}}, durable.CurrentDocument())
				if err != nil {
					return 0, err
				}
				if record == nil {
					return -1, nil
				}
				return int(record.ID), nil
			},
			Expected: func(dataset StorageBenchmarkDataset) int { return int(dataset.ExactDocumentID) },
		},
	}
	for _, tail := range BenchmarkReplayTails {
		tail := tail
		benchmarks = append(benchmarks, StorageReadBenchmark{
			Name: fmt.Sprintf("document replay tail (%d)", tail),
			Run: func(ctx context.Context, storage durable.Storage, dataset StorageBenchmarkDataset) (int, error) {
				document, err := storage.Document(ctx, dataset.ReplayDocumentIDs[tail], durable.CurrentDocument())
				if err != nil {
					return 0, err
				}
				if document == nil {
					return 0, nil
				}
				return jsonInt(document.Value["count"]), nil
			},
			Expected: func(StorageBenchmarkDataset) int { return tail },
		})
	}
	benchmarks = append(benchmarks,
		StorageReadBenchmark{
			Name: "ancient historical read before newer base",
			Run: func(ctx context.Context, storage durable.Storage, dataset StorageBenchmarkDataset) (int, error) {
				document, err := storage.Document(ctx, dataset.HistoricalDocumentID, durable.AtSeq(dataset.AncientAt))
				if err != nil {
					return 0, err
				}
				if document == nil {
					return 0, nil
				}
				return jsonInt(document.Value["count"]), nil
			},
			Expected: func(StorageBenchmarkDataset) int { return benchmarkHistorySegmentLength },
		},
		StorageReadBenchmark{
			Name: "recent historical read after newer base",
			Run: func(ctx context.Context, storage durable.Storage, dataset StorageBenchmarkDataset) (int, error) {
				document, err := storage.Document(ctx, dataset.HistoricalDocumentID, durable.AtSeq(dataset.RecentAt))
				if err != nil {
					return 0, err
				}
				if document == nil {
					return 0, nil
				}
				return jsonInt(document.Value["count"]), nil
			},
			Expected: func(StorageBenchmarkDataset) int { return benchmarkHistorySegmentLength * 2 },
		},
		StorageReadBenchmark{
			Name: "fork-depth history scan (100)",
			Run: func(ctx context.Context, storage durable.Storage, dataset StorageBenchmarkDataset) (int, error) {
				page, err := storage.ScanEntries(ctx, durable.EntryQuery{ConversationID: dataset.DeepestConversationID}, 100, nil)
				if err != nil {
					return 0, err
				}
				return len(page.Items), nil
			},
			Expected: func(StorageBenchmarkDataset) int { return 100 },
		},
		StorageReadBenchmark{
			Name: "fork-depth head lookup",
			Run: func(ctx context.Context, storage durable.Storage, dataset StorageBenchmarkDataset) (int, error) {
				marker, err := storage.FindLatestHeadMarker(ctx, dataset.DeepestConversationID, nil)
				if err != nil {
					return 0, err
				}
				if marker == nil {
					return -1, nil
				}
				return int(marker.ID), nil
			},
			Expected: func(dataset StorageBenchmarkDataset) int { return int(dataset.AncestorHeadEntryID) },
		},
	)
	return benchmarks
}

// StorageWriteBenchmark is one named write micro-benchmark.
type StorageWriteBenchmark struct {
	Name     string
	Expected int
	Run      func(context.Context, durable.Storage) (int, error)
}

// StorageWriteBenchmarks are the source write benchmark definitions in order.
var StorageWriteBenchmarks = []StorageWriteBenchmark{
	{
		Name:     "commit one entry",
		Expected: 1,
		Run: func(ctx context.Context, storage durable.Storage) (int, error) {
			id, err := storage.MintID(ctx)
			if err != nil {
				return 0, err
			}
			entry := durable.EntryRecord{ID: durable.EntryID(id), ConversationID: durable.RootConversationID, Kind: "benchmark.write", Data: json.RawMessage(fmt.Sprintf(`{"text":%q}`, strings.Repeat("x", 128)))}
			if _, err := storage.Commit(ctx, []durable.StorageWrite{{Type: durable.WriteEntry, Entry: &entry}}); err != nil {
				return 0, err
			}
			return 1, nil
		},
	},
	{
		Name:     "commit 100 entries",
		Expected: 100,
		Run: func(ctx context.Context, storage durable.Storage) (int, error) {
			writes := make([]durable.StorageWrite, 0, 100)
			for index := 0; index < 100; index++ {
				id, err := storage.MintID(ctx)
				if err != nil {
					return 0, err
				}
				entry := durable.EntryRecord{ID: durable.EntryID(id), ConversationID: durable.RootConversationID, Kind: "benchmark.write", Data: json.RawMessage(fmt.Sprintf(`{"index":%d,"text":%q}`, index, strings.Repeat("x", 128)))}
				writes = append(writes, durable.StorageWrite{Type: durable.WriteEntry, Entry: &entry})
			}
			if _, err := storage.Commit(ctx, writes); err != nil {
				return 0, err
			}
			return len(writes), nil
		},
	},
	{
		Name:     "commit mixed entry/task/submission/document",
		Expected: 4,
		Run: func(ctx context.Context, storage durable.Storage) (int, error) {
			entryID, err := storage.MintID(ctx)
			if err != nil {
				return 0, err
			}
			taskID, err := storage.MintID(ctx)
			if err != nil {
				return 0, err
			}
			submissionID, err := storage.MintID(ctx)
			if err != nil {
				return 0, err
			}
			documentID, err := storage.MintID(ctx)
			if err != nil {
				return 0, err
			}
			task := benchmarkTask(durable.TaskID(taskID), int(taskID))
			entry := durable.EntryRecord{ID: durable.EntryID(entryID), ConversationID: durable.RootConversationID, Kind: "benchmark.mixed"}
			submission := durable.SubmissionRecord{
				ID:             durable.SubmissionID(submissionID),
				ConversationID: durable.RootConversationID,
				RequestID:      ptr(fmt.Sprintf("benchmark-%d", submissionID)),
				Type:           durable.SubmissionTypeWrite,
				Status:         durable.SubmissionStatusDone,
				Entry:          ptr(durable.EntryID(entryID)),
			}
			record := durable.DocumentCreate{ID: durable.DocumentID(documentID), Kind: "benchmark.mixed", Key: ptr(fmt.Sprintf("%d", documentID)), Scope: durable.DocumentScope{Kind: durable.ScopeSession}}
			writes := []durable.StorageWrite{
				{Type: durable.WriteEntry, Entry: &entry},
				{Type: durable.WriteTask, Task: &task},
				{Type: durable.WriteSubmission, Submission: &submission},
				{Type: durable.WriteDocumentCreate, Record: &record, Content: &durable.DocumentContent{Kind: durable.ContentBase, Version: 1, Value: durable.JsonObject{"entryId": int(entryID), "taskId": int(taskID)}}},
			}
			if _, err := storage.Commit(ctx, writes); err != nil {
				return 0, err
			}
			return len(writes), nil
		},
	},
}

// SeedStorageWriteBenchmark seeds the common state every write benchmark sample
// expects: the root conversation plus 100 baseline entries.
func SeedStorageWriteBenchmark(ctx context.Context, storage durable.Storage) error {
	if _, err := storage.Commit(ctx, []durable.StorageWrite{{Type: durable.WriteConversation, Conversation: &durable.ConversationRecord{ID: durable.RootConversationID}}}); err != nil {
		return err
	}
	writes := make([]durable.StorageWrite, 0, 100)
	for index := 0; index < 100; index++ {
		id, err := storage.MintID(ctx)
		if err != nil {
			return err
		}
		entry := durable.EntryRecord{ID: durable.EntryID(id), ConversationID: durable.RootConversationID, Kind: "benchmark.baseline", Data: json.RawMessage(fmt.Sprintf(`{"index":%d}`, index))}
		writes = append(writes, durable.StorageWrite{Type: durable.WriteEntry, Entry: &entry})
	}
	if _, err := storage.Commit(ctx, writes); err != nil {
		return err
	}
	return nil
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// jsonInt normalizes a decoded JSON number to int. Values written by the
// benchmark are plain integers.
func jsonInt(value any) int {
	switch typed := value.(type) {
	case float64:
		return int(typed)
	case int:
		return typed
	case int64:
		return int(typed)
	case json.Number:
		parsed, err := typed.Int64()
		if err != nil {
			return 0
		}
		return int(parsed)
	default:
		return 0
	}
}
