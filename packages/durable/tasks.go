package durable

import (
	"context"
	"encoding/json"
)

// PhaseHandler runs one checkpoint phase of a task. It must commit a changed
// checkpoint or a terminal outcome through the runtime; returning without
// durable progress faults the task.
type PhaseHandler func(context.Context, TaskRecord, TaskRuntime) error

// TaskDefinition is one executable durable state machine definition. Definitions
// are scheduling-neutral: they contain no registry, model, or environment
// references. A harness registry runs tasks of a registered Name.
type TaskDefinition struct {
	// Name is the registered task kind persisted in TaskRecord.Kind.
	Name string
	// Version is the definition version persisted with live input and
	// checkpoints. Migrate converts a record stored by an older supported
	// version and runs at reservation.
	Version int
	// Initial returns the first durable checkpoint for a newly created task.
	Initial func(json.RawMessage) (json.RawMessage, error)
	// Phases is the exhaustive phase map; each handler receives the task record.
	Phases map[string]PhaseHandler
	// Abort runs in a fresh invocation after an abort mark and must commit a
	// terminal outcome.
	Abort PhaseHandler
	// Migrate converts input and checkpoint from an older definition version.
	// It returns the migrated input and checkpoint.
	Migrate func(json.RawMessage, json.RawMessage, int) (json.RawMessage, json.RawMessage, error)
}

// DefineTask is the Go counterpart of upstream `defineTask`. It returns the
// supplied definition unchanged; validation and registration belong to a
// harness registry.
func DefineTask(def TaskDefinition) TaskDefinition { return def }

// TaskRuntime is the scheduling-neutral surface one task invocation uses to
// commit durable state, read memos, wait on other tasks, and report non-fatal
// failures. Every operation rejects after the invocation ends; a harness owns
// the concrete implementation.
type TaskRuntime interface {
	// Commit runs one commit on the Session line after rereading the task. A
	// returned task state replaces the task's state in the same commit.
	Commit(context.Context, func(Tx, *TaskRecord) error) error
	// Memo reads a durable memo of this task.
	Memo(context.Context, string, json.RawMessage) (json.RawMessage, error)
	// GetTask returns the latest committed task record.
	GetTask(context.Context, TaskID) (*TaskRecord, error)
	// WaitForTask resolves with the task's terminal receipt.
	WaitForTask(context.Context, TaskID) (TaskOutcome, error)
	// Outcomes returns the outcomes of terminal tasks in order.
	Outcomes(context.Context, []TaskID) ([]TaskOutcome, error)
	// Sleep resolves once the harness clock reaches the given millisecond
	// instant.
	Sleep(context.Context, int64) error
	// Now returns the harness clock in milliseconds.
	Now() int64
	// Report forwards a non-fatal failure to the harness reporter.
	Report(error)
}

// Tx is the transaction surface of one Session commit callback. Table reads and
// creation results are trusted immutable values and may be shared with internal
// commit state.
type Tx interface {
	Conversation(context.Context, ConversationID) (*ConversationRecord, error)
	Entry(context.Context, EntryID) (*EntryRecord, error)
	Task(context.Context, TaskID) (*TaskRecord, error)
	ScanConversations(context.Context, ConversationQuery, int, Cursor) (Page[ConversationRecord], error)
	ScanEntries(context.Context, EntryQuery, int, Cursor) (Page[EntryRecord], error)
	// LatestHeadMarker returns the newest visible entry of the conversation that
	// carries a head.
	LatestHeadMarker(context.Context, ConversationID) (*EntryRecord, error)
	ScanTasks(context.Context, TaskQuery, int, Cursor) (Page[TaskRecord], error)
	// SubmissionByRequest returns the committed submission with a
	// conversation-scoped request ID.
	SubmissionByRequest(context.Context, ConversationID, string) (*SubmissionRecord, error)

	// CreateConversation creates a conversation with explicitly selected
	// ownership.
	CreateConversation(context.Context, ConversationOwnership) (*ConversationRecord, error)
	// ForkConversation creates a history fork at one concrete visible entry with
	// explicitly selected ownership.
	ForkConversation(context.Context, ConversationID, EntryID, ConversationOwnership) (*ConversationRecord, error)
	// AppendEntry appends one immutable entry and returns the stored record.
	AppendEntry(context.Context, ConversationID, EntryDraft) (*EntryRecord, error)
	// CreateTask creates a task of the supplied definition and input.
	CreateTask(context.Context, TaskDefinition, json.RawMessage, TaskOptions) (TaskID, error)
	// CreateSubmission creates a raw submission record with a fresh ID. No
	// admission rules apply.
	CreateSubmission(context.Context, SubmissionCreate) (*SubmissionRecord, error)
	// SettleSubmission settles a queued or placed submission.
	SettleSubmission(SubmissionID, SubmissionSettlement) error
	// PlaceSubmission places a queued submission at an entry.
	PlaceSubmission(SubmissionID, EntryID) error
	// Doc returns one private mutable document draft, creating the incarnation
	// lazily when the logical address is absent. Seed supplies a family seed.
	Doc(context.Context, DocumentDefinition, DocumentAddress, json.RawMessage) (*DocumentDraft, error)
	// RetireDoc retires the incarnation at one logical address.
	RetireDoc(context.Context, DocumentDefinition, DocumentAddress) error
}
