// This file carries commit.ts: the pure write-stamping and validation helpers
// shared by every storage backend.
package session

import (
	"errors"
	"fmt"

	harnesstypes "github.com/minifish-org/pith/packages/agent/harness/types"
)

// CommittedWrite is one storage mutation after sequence and timestamp
// assignment.
//
// The upstream type is a discriminated union of six shapes. Go models the
// union as one struct selected by Kind so that the parallel entry/usage and
// value/list pipelines can share the commit queue and the JSONL codec. The
// named variants below are aliases so call sites stay readable.
type CommittedWrite struct {
	Kind string

	// Entry is set when Kind == "entry".
	Entry harnesstypes.Entry

	// Usage is set when Kind == "usage".
	Usage *harnesstypes.UsageRow

	// Op, Namespace, Key and Value describe value/list writes.
	Op        string
	Namespace string
	Key       string
	Value     any

	// Seq and Timestamp are the assigned sequence and commit timestamp.
	Seq       int
	Timestamp float64
}

// CommittedEntryWrite is an entry-shaped committed write.
type CommittedEntryWrite = CommittedWrite

// CommittedUsageWrite is a usage-shaped committed write.
type CommittedUsageWrite = CommittedWrite

// CommittedValueSetWrite is a scalar-set committed write.
type CommittedValueSetWrite = CommittedWrite

// CommittedValueDeleteWrite is a scalar-delete committed write.
type CommittedValueDeleteWrite = CommittedWrite

// CommittedListAppendWrite is a list-append committed write.
type CommittedListAppendWrite = CommittedWrite

// CommittedListDeleteWrite is a list-delete committed write.
type CommittedListDeleteWrite = CommittedWrite

// ID returns the entry or usage id, or the empty string for value writes.
func (w CommittedWrite) ID() string {
	switch w.Kind {
	case "entry":
		return EntryID(w.Entry)
	case "usage":
		if w.Usage == nil {
			return ""
		}
		return w.Usage.ID
	default:
		return ""
	}
}

// ParentID returns the entry parent id for entry writes.
func (w CommittedWrite) ParentID() *string {
	if w.Kind != "entry" {
		return nil
	}
	return EntryParentID(w.Entry)
}

// EntrySeq returns the assigned sequence of the write payload.
func (w CommittedWrite) EntrySeq() int {
	switch w.Kind {
	case "entry":
		return EntrySeqOf(w.Entry)
	case "usage":
		if w.Usage == nil {
			return w.Seq
		}
		return w.Usage.Seq
	default:
		return w.Seq
	}
}

// PreparedCommit is a stamped commit before it is applied.
type PreparedCommit struct {
	Writes []CommittedWrite
	Result PreparedCommitResult
}

// PreparedCommitResult is the commit result without post-apply statistics.
type PreparedCommitResult struct {
	FirstSeq  int
	Seqs      []int
	Timestamp float64
}

// CommitValidationState reports the identifiers already present in a backend.
type CommitValidationState interface {
	HasEntryOrUsageID(id string) bool
	HasEntryID(id string) bool
}

// InsertEntry builds an entry write.
func InsertEntry(entry harnesstypes.NewEntry) harnesstypes.EntryWrite {
	return harnesstypes.EntryWrite{Entry: entry}
}

// InsertUsage builds a usage write.
func InsertUsage(row harnesstypes.UsageRow) harnesstypes.UsageWrite {
	return harnesstypes.UsageWrite{Row: row}
}

// MaterializeCommittedEntry copies an entry with the assigned sequence and
// timestamp.
func MaterializeCommittedEntry(entry harnesstypes.NewEntry, seq int, timestamp float64) harnesstypes.Entry {
	return materializeEntry(entry, seq, timestamp)
}

// CommitWrite stamps one requested write with its sequence and timestamp.
func CommitWrite(write harnesstypes.Write, seq int, timestamp float64) CommittedWrite {
	if write == nil {
		return CommittedWrite{}
	}
	switch typed := write.(type) {
	case harnesstypes.EntryWrite:
		return CommittedWrite{Kind: "entry", Entry: materializeEntry(typed.Entry, seq, timestamp), Seq: seq, Timestamp: timestamp}
	case *harnesstypes.EntryWrite:
		return CommittedWrite{Kind: "entry", Entry: materializeEntry(typed.Entry, seq, timestamp), Seq: seq, Timestamp: timestamp}
	case harnesstypes.UsageWrite:
		row := typed.Row
		row.Seq = seq
		return CommittedWrite{Kind: "usage", Usage: &row, Seq: seq, Timestamp: timestamp}
	case *harnesstypes.UsageWrite:
		row := typed.Row
		row.Seq = seq
		return CommittedWrite{Kind: "usage", Usage: &row, Seq: seq, Timestamp: timestamp}
	case harnesstypes.ValueSetWrite:
		return CommittedWrite{Kind: "value", Op: "set", Namespace: typed.Namespace, Key: typed.Key, Value: typed.Value, Seq: seq, Timestamp: timestamp}
	case *harnesstypes.ValueSetWrite:
		return CommittedWrite{Kind: "value", Op: "set", Namespace: typed.Namespace, Key: typed.Key, Value: typed.Value, Seq: seq, Timestamp: timestamp}
	case harnesstypes.ValueDeleteWrite:
		return CommittedWrite{Kind: "value", Op: "delete", Namespace: typed.Namespace, Key: typed.Key, Seq: seq, Timestamp: timestamp}
	case *harnesstypes.ValueDeleteWrite:
		return CommittedWrite{Kind: "value", Op: "delete", Namespace: typed.Namespace, Key: typed.Key, Seq: seq, Timestamp: timestamp}
	case harnesstypes.ListAppendWrite:
		return CommittedWrite{Kind: "list", Op: "append", Namespace: typed.Namespace, Key: typed.Key, Value: typed.Value, Seq: seq, Timestamp: timestamp}
	case *harnesstypes.ListAppendWrite:
		return CommittedWrite{Kind: "list", Op: "append", Namespace: typed.Namespace, Key: typed.Key, Value: typed.Value, Seq: seq, Timestamp: timestamp}
	case harnesstypes.ListDeleteWrite:
		return CommittedWrite{Kind: "list", Op: "delete", Namespace: typed.Namespace, Key: typed.Key, Seq: seq, Timestamp: timestamp}
	case *harnesstypes.ListDeleteWrite:
		return CommittedWrite{Kind: "list", Op: "delete", Namespace: typed.Namespace, Key: typed.Key, Seq: seq, Timestamp: timestamp}
	default:
		return CommittedWrite{}
	}
}

// PrepareStorageCommit stamps a whole transaction in write order.
func PrepareStorageCommit(writes []harnesstypes.Write, firstSeq int, timestamp float64) PreparedCommit {
	committed := make([]CommittedWrite, 0, len(writes))
	seqs := make([]int, 0, len(writes))
	for index, write := range writes {
		entry := CommitWrite(write, firstSeq+index, timestamp)
		committed = append(committed, entry)
		seqs = append(seqs, entry.Seq)
	}
	return PreparedCommit{
		Writes: committed,
		Result: PreparedCommitResult{FirstSeq: firstSeq, Seqs: seqs, Timestamp: timestamp},
	}
}

// ValidateCommittedWrites rejects non-monotonic sequences, duplicate entry and
// usage ids, and dangling parents.
func ValidateCommittedWrites(writes []CommittedWrite, firstSeq int, state CommitValidationState) error {
	previousSeq := firstSeq - 1
	transactionIDs := map[string]bool{}
	transactionEntryIDs := map[string]bool{}
	for _, write := range writes {
		if write.Seq <= previousSeq {
			return fmt.Errorf("Non-monotonic storage sequence: %d", write.Seq)
		}
		previousSeq = write.Seq
		if write.Kind != "entry" && write.Kind != "usage" {
			continue
		}
		id := write.ID()
		if id == "" {
			return errors.New("Committed entry or usage id must not be empty")
		}
		if state.HasEntryOrUsageID(id) || transactionIDs[id] {
			return fmt.Errorf("Duplicate entry or usage id: %s", id)
		}
		if write.Kind == "entry" {
			parentID := write.ParentID()
			if parentID != nil && !state.HasEntryID(*parentID) && !transactionEntryIDs[*parentID] {
				return fmt.Errorf("Missing parent entry: %s", *parentID)
			}
		}
		transactionIDs[id] = true
		if write.Kind == "entry" {
			transactionEntryIDs[id] = true
		}
	}
	return nil
}

func materializeEntry(entry harnesstypes.Entry, seq int, timestamp float64) harnesstypes.Entry {
	if entry == nil {
		return nil
	}
	switch typed := entry.(type) {
	case harnesstypes.MessageEntry:
		typed.Seq = seq
		typed.Timestamp = timestamp
		return typed
	case *harnesstypes.MessageEntry:
		copied := *typed
		copied.Seq = seq
		copied.Timestamp = timestamp
		return &copied
	case harnesstypes.CompactionEntry:
		typed.Seq = seq
		typed.Timestamp = timestamp
		return typed
	case *harnesstypes.CompactionEntry:
		copied := *typed
		copied.Seq = seq
		copied.Timestamp = timestamp
		return &copied
	case harnesstypes.BranchSummaryEntry:
		typed.Seq = seq
		typed.Timestamp = timestamp
		return typed
	case *harnesstypes.BranchSummaryEntry:
		copied := *typed
		copied.Seq = seq
		copied.Timestamp = timestamp
		return &copied
	case harnesstypes.CustomEntry:
		typed.Seq = seq
		typed.Timestamp = timestamp
		return typed
	case *harnesstypes.CustomEntry:
		copied := *typed
		copied.Seq = seq
		copied.Timestamp = timestamp
		return &copied
	default:
		return entry
	}
}
