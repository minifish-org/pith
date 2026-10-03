package durable

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/minifish-org/pith/packages/ai/types"
	"github.com/minifish-org/pith/packages/chord"
)

// JsonObject is the JSON object used as the root of every durable document. It
// is an alias, so callers interoperate directly with map[string]any.
type JsonObject = map[string]any

// Storage write discriminators. They are the exact persisted operation names.
const (
	WriteConversation    = "conversation"
	WriteEntry           = "entry"
	WriteTask            = "task"
	WriteSubmission      = "submission"
	WriteDocumentCreate  = "document.create"
	WriteDocumentCopy    = "document.copy"
	WriteDocumentChange  = "document.change"
	WriteDocumentRetire  = "document.retire"
	WriteDocumentContent = "document.content"
)

// Submission types and lifecycle statuses.
const (
	SubmissionTypeInput = "input"
	SubmissionTypeWrite = "write"

	SubmissionStatusQueued     = "queued"
	SubmissionStatusPlaced     = "placed"
	SubmissionStatusDone       = "done"
	SubmissionStatusUnanswered = "unanswered"
)

// Task state statuses.
const (
	TaskStatusPending    = "pending"
	TaskStatusRunning    = "running"
	TaskStatusWaiting    = "waiting"
	TaskStatusCompleting = "completing"
	TaskStatusTerminal   = "terminal"
)

// Task outcome statuses.
const (
	OutcomeCompleted = "completed"
	OutcomeFailed    = "failed"
	OutcomeAborted   = "aborted"
	OutcomeOrphaned  = "orphaned"
	OutcomeFaulted   = "faulted"
)

// Document scopes.
const (
	ScopeSession      = "session"
	ScopeConversation = "conversation"
	ScopeTask         = "task"
)

// Document history policies.
const (
	HistoryLatest     = "latest"
	HistoryRewindable = "rewindable"
)

// Document fork policies.
const (
	ForkAsOf    = "asOf"
	ForkCurrent = "current"
	ForkInitial = "initial"
)

// Document content discriminators.
const (
	ContentBase  = "base"
	ContentDelta = "delta"
)

// Join policies for a waiting task.
const (
	PolicyFailFast   = "failFast"
	PolicyAllSettled = "allSettled"
)

// ConversationParent records the fork source and inclusive parent entry through
// which history is inherited.
type ConversationParent struct {
	ConversationID ConversationID `json:"conversationId"`
	At             EntryID        `json:"at"`
}

// ConversationOwner records the creator edge used for attribution, subtree
// abort, and subtree idle waits.
type ConversationOwner struct {
	ConversationID ConversationID `json:"conversationId"`
	TaskID         TaskID         `json:"taskId"`
}

// ConversationRecord is the immutable identity, history ancestry, and task
// ownership of one transcript scope.
type ConversationRecord struct {
	ID     ConversationID      `json:"id"`
	Parent *ConversationParent `json:"parent,omitempty"`
	Owner  *ConversationOwner  `json:"owner,omitempty"`
}

// ContextEdit is an immutable override of one visible entry's contribution to
// model context. Action is "omit" or "replace"; replace carries Messages.
type ContextEdit struct {
	Target   EntryID         `json:"target"`
	Action   string          `json:"action"`
	Messages []types.Message `json:"messages,omitempty"`
}

// EntryRecord is one immutable transcript event with separate model-facing and
// application-facing payloads.
type EntryRecord struct {
	ID             EntryID         `json:"id"`
	ConversationID ConversationID  `json:"conversationId"`
	Kind           string          `json:"kind"`
	Model          []types.Message `json:"model,omitempty"`
	Data           json.RawMessage `json:"data,omitempty"`
	Head           *EntryID        `json:"head,omitempty"`
	Edits          []ContextEdit   `json:"edits,omitempty"`
	ByTaskID       *TaskID         `json:"byTaskId,omitempty"`
}

// EntryDraft is entry content supplied before the Session assigns identity and
// task attribution. HeadSelf starts active context at the newly assigned entry
// ID, mirroring the upstream "self" head.
type EntryDraft struct {
	Kind     string          `json:"kind"`
	Model    []types.Message `json:"model,omitempty"`
	Data     json.RawMessage `json:"data,omitempty"`
	Head     *EntryID        `json:"head,omitempty"`
	HeadSelf bool            `json:"-"`
	Edits    []ContextEdit   `json:"edits,omitempty"`
}

// StoredEntry is one global entry together with the sequence of the commit that
// persisted it.
type StoredEntry struct {
	Entry     EntryRecord `json:"entry"`
	CommitSeq Seq         `json:"commitSeq"`
}

// TaskOutcomeError is a JSON-safe error snapshot persisted instead of a runtime
// error object.
type TaskOutcomeError struct {
	Message string          `json:"message"`
	Detail  json.RawMessage `json:"detail,omitempty"`
}

// TaskOutcome is the durable reason and optional result recorded when a task
// becomes terminal. Status is one of OutcomeCompleted, OutcomeFailed,
// OutcomeAborted, OutcomeOrphaned, OutcomeFaulted.
type TaskOutcome struct {
	Status string            `json:"status"`
	Result json.RawMessage   `json:"result,omitempty"`
	Error  *TaskOutcomeError `json:"error,omitempty"`
	Reason string            `json:"reason,omitempty"`
}

// TaskState is the complete durable execution state of a task. Status is one of
// the TaskStatus constants.
type TaskState struct {
	Status     string          `json:"status"`
	Checkpoint json.RawMessage `json:"checkpoint,omitempty"`
	On         []TaskID        `json:"on,omitempty"`
	Policy     string          `json:"policy,omitempty"`
	Outcome    *TaskOutcome    `json:"outcome,omitempty"`
}

// TaskRecord is the complete replacement record for one durable task state
// machine. Owner is a task ID for child tasks and absent for conversation-owned
// tasks; Background applies only to conversation-owned tasks. Completion discards
// live memos and checkpoints.
type TaskRecord struct {
	ID             TaskID                     `json:"id"`
	ConversationID ConversationID             `json:"conversationId"`
	Kind           string                     `json:"kind"`
	Version        int                        `json:"version"`
	Input          json.RawMessage            `json:"input,omitempty"`
	Owner          *TaskID                    `json:"owner,omitempty"`
	Background     bool                       `json:"background"`
	AbortRequested bool                       `json:"abortRequested"`
	State          TaskState                  `json:"state"`
	Memos          map[string]json.RawMessage `json:"memos,omitempty"`
}

// SubmissionRecord is the durable lifecycle of one admitted user input or
// passive entry write.
type SubmissionRecord struct {
	ID             SubmissionID    `json:"id"`
	ConversationID ConversationID  `json:"conversationId"`
	RequestID      *string         `json:"requestId,omitempty"`
	Type           string          `json:"type"`
	Status         string          `json:"status"`
	Entry          *EntryID        `json:"entry,omitempty"`
	Answer         *EntryID        `json:"answer,omitempty"`
	Reason         string          `json:"reason,omitempty"`
	Detail         json.RawMessage `json:"detail,omitempty"`
}

// SubmissionCreate carries submission fields supplied before the Session
// assigns an ID.
type SubmissionCreate struct {
	ConversationID ConversationID  `json:"conversationId"`
	RequestID      *string         `json:"requestId,omitempty"`
	Type           string          `json:"type"`
	Status         string          `json:"status"`
	Entry          *EntryID        `json:"entry,omitempty"`
	Answer         *EntryID        `json:"answer,omitempty"`
	Reason         string          `json:"reason,omitempty"`
	Detail         json.RawMessage `json:"detail,omitempty"`
}

// SubmissionSettlement is a terminal status staged for a submission; identity,
// type, and entry come from its current record.
type SubmissionSettlement struct {
	Status string          `json:"status"`
	Answer *EntryID        `json:"answer,omitempty"`
	Reason string          `json:"reason,omitempty"`
	Detail json.RawMessage `json:"detail,omitempty"`
}

// DocumentScope identifies the owner of a document incarnation.
type DocumentScope struct {
	Kind           string         `json:"kind"`
	ConversationID ConversationID `json:"conversationId,omitempty"`
	TaskID         TaskID         `json:"taskId,omitempty"`
}

// DocumentRecord is the persisted lifecycle record of one create-to-retire
// document incarnation. Lifetimes are half-open [CreatedAt, RetiredAt).
type DocumentRecord struct {
	ID        DocumentID    `json:"id"`
	Kind      string        `json:"kind"`
	Key       *string       `json:"key,omitempty"`
	Scope     DocumentScope `json:"scope"`
	History   string        `json:"history,omitempty"`
	Fork      string        `json:"fork,omitempty"`
	CreatedAt Seq           `json:"createdAt"`
	RetiredAt *Seq          `json:"retiredAt,omitempty"`
}

// DocumentCreate carries the fields supplied when storage creates and stamps a
// new DocumentRecord.
type DocumentCreate struct {
	ID      DocumentID    `json:"id"`
	Kind    string        `json:"kind"`
	Key     *string       `json:"key,omitempty"`
	Scope   DocumentScope `json:"scope"`
	History string        `json:"history,omitempty"`
	Fork    string        `json:"fork,omitempty"`
}

// DocumentAddress is the exact logical identity of a singleton or one keyed
// family member. A nil Key selects the singleton; a non-nil Key (including the
// empty string) selects one family member.
type DocumentAddress struct {
	Kind  string        `json:"kind"`
	Scope DocumentScope `json:"scope"`
	Key   *string       `json:"key,omitempty"`
}

// DocumentPoint is the current state or one historical commit sequence used for
// document membership and content reads.
type DocumentPoint struct {
	Current bool
	Seq     Seq
}

// CurrentDocument selects the current state of a document.
func CurrentDocument() DocumentPoint { return DocumentPoint{Current: true} }

// AtSeq selects the state of a document at one commit sequence.
func AtSeq(seq Seq) DocumentPoint { return DocumentPoint{Seq: seq} }

// MarshalJSON encodes the point as the upstream union: the string "current" or a
// sequence number.
func (p DocumentPoint) MarshalJSON() ([]byte, error) {
	if p.Current {
		return []byte(`"current"`), nil
	}
	return []byte(strconv.FormatInt(int64(p.Seq), 10)), nil
}

// UnmarshalJSON decodes the upstream union form.
func (p *DocumentPoint) UnmarshalJSON(data []byte) error {
	if string(data) == `"current"` {
		p.Current = true
		p.Seq = 0
		return nil
	}
	var seq int64
	if err := json.Unmarshal(data, &seq); err != nil {
		return err
	}
	p.Current = false
	p.Seq = Seq(seq)
	return nil
}

// DocumentContent is a complete checkpoint or Chord operation batch selected by
// the owning Session. Kind is ContentBase (with Value) or ContentDelta (with
// Ops).
type DocumentContent struct {
	Kind    string     `json:"kind"`
	Version int        `json:"version"`
	Value   JsonObject `json:"value,omitempty"`
	Ops     []chord.Op `json:"ops,omitempty"`
}

// DocumentCopySource is the exact persisted source selected for a definition-free
// document copy.
type DocumentCopySource struct {
	ID DocumentID    `json:"id"`
	At DocumentPoint `json:"at"`
}

// StoredDocument is the detached materialized value and stored definition
// version at a selected point.
type StoredDocument struct {
	Record          DocumentRecord `json:"record"`
	Version         int            `json:"version"`
	Value           JsonObject     `json:"value"`
	DeltasSinceBase int            `json:"deltasSinceBase"`
}

// StorageWrite is one record or document mutation in an atomic storage commit.
type StorageWrite struct {
	Type         string              `json:"type"`
	Conversation *ConversationRecord `json:"conversation,omitempty"`
	Entry        *EntryRecord        `json:"entry,omitempty"`
	Task         *TaskRecord         `json:"task,omitempty"`
	Submission   *SubmissionRecord   `json:"submission,omitempty"`
	Record       *DocumentCreate     `json:"record,omitempty"`
	Content      *DocumentContent    `json:"content,omitempty"`
	Source       *DocumentCopySource `json:"source,omitempty"`
	ID           DocumentID          `json:"id,omitempty"`
}

// Cursor is backend-owned JSON continuation state that callers only round-trip
// to the same scan. It is not an offset for callers to inspect.
type Cursor = json.RawMessage

// Page is one ordered scan result and its optional continuation state.
type Page[T any] struct {
	Items []T    `json:"items"`
	Next  Cursor `json:"next,omitempty"`
}

// ConversationQuery holds optional filters for an ordered conversation scan.
type ConversationQuery struct {
	OwnerConversationID *ConversationID `json:"ownerConversationId,omitempty"`
	OwnerTaskID         *TaskID         `json:"ownerTaskId,omitempty"`
}

// EntryQuery holds inclusive ID bounds for a newest-first scan of one
// conversation's fork-aware history.
type EntryQuery struct {
	ConversationID ConversationID `json:"conversationId"`
	MinEntryID     *EntryID       `json:"minEntryId,omitempty"`
	MaxEntryID     *EntryID       `json:"maxEntryId,omitempty"`
}

// TaskQuery holds optional filters for an ordered scan of durable task records.
type TaskQuery struct {
	ConversationID *ConversationID `json:"conversationId,omitempty"`
	Kind           string          `json:"kind,omitempty"`
	Status         string          `json:"status,omitempty"`
	AbortRequested *bool           `json:"abortRequested,omitempty"`
	Background     *bool           `json:"background,omitempty"`
}

// SubmissionQuery holds optional filters for an ordered scan of submission
// records.
type SubmissionQuery struct {
	ConversationID *ConversationID `json:"conversationId,omitempty"`
	Status         string          `json:"status,omitempty"`
}

// DocumentQuery is an ordered scan of document incarnations alive in one exact
// scope at one point.
type DocumentQuery struct {
	Scope DocumentScope `json:"scope"`
	At    DocumentPoint `json:"at"`
	Kind  string        `json:"kind,omitempty"`
}

// Storage is the atomic persistence boundary for Session records.
//
// Storage trusts the owning Session to supply semantically valid records,
// references, ancestry, and transitions. Implementations enforce atomicity,
// global ID ownership, immutable conversation/entry creation, document record
// consistency, and detached values; the Session serializes commits. The root
// package never imports an adapter.
type Storage interface {
	// Commit atomically persists one batch and returns its sequence. Once it
	// returns, later reads through this storage observe it.
	Commit(context.Context, []StorageWrite) (Seq, error)
	// MintID returns a fresh candidate from the Session-global numeric ID
	// namespace.
	MintID(context.Context) (ID, error)
	// Conversation looks up one conversation by exact ID.
	Conversation(context.Context, ConversationID) (*ConversationRecord, error)
	// ScanConversations scans conversations in ascending ID order.
	ScanConversations(context.Context, ConversationQuery, int, Cursor) (Page[ConversationRecord], error)
	// Entry looks up one global entry and the sequence of the commit that
	// persisted it.
	Entry(context.Context, EntryID) (*StoredEntry, error)
	// VisibleEntry looks up one entry only when it is visible through the
	// requested conversation's ancestry.
	VisibleEntry(context.Context, ConversationID, EntryID) (*StoredEntry, error)
	// FindLatestHeadMarker returns the newest visible entry with a head at or
	// below the optional inclusive cutoff. The returned entry is the marker; its
	// Head is the range's actual lower bound.
	FindLatestHeadMarker(context.Context, ConversationID, *EntryID) (*EntryRecord, error)
	// ScanEntries scans the inclusive visible range newest-first, returning at
	// most limit entries.
	ScanEntries(context.Context, EntryQuery, int, Cursor) (Page[EntryRecord], error)
	// Task looks up the latest complete record for one task.
	Task(context.Context, TaskID) (*TaskRecord, error)
	// ScanTasks scans task records matching every supplied filter.
	ScanTasks(context.Context, TaskQuery, int, Cursor) (Page[TaskRecord], error)
	// Submission looks up the latest complete record for one admitted
	// submission.
	Submission(context.Context, SubmissionID) (*SubmissionRecord, error)
	// ScanSubmissions scans submissions matching every supplied filter in
	// ascending ID order.
	ScanSubmissions(context.Context, SubmissionQuery, int, Cursor) (Page[SubmissionRecord], error)
	// SubmissionByRequest finds a submission by its conversation-scoped host
	// deduplication key.
	SubmissionByRequest(context.Context, ConversationID, string) (*SubmissionRecord, error)
	// FindDocument resolves the incarnation occupying one exact logical address
	// at the selected point.
	FindDocument(context.Context, DocumentAddress, DocumentPoint) (*DocumentRecord, error)
	// Document materializes one specific incarnation by ID at the selected point
	// without following a replacement at its address.
	Document(context.Context, DocumentID, DocumentPoint) (*StoredDocument, error)
	// ScanDocuments scans incarnations alive in one exact scope at the selected
	// point.
	ScanDocuments(context.Context, DocumentQuery, int, Cursor) (Page[DocumentRecord], error)
	// Close releases backend resources; all later operations reject.
	Close(context.Context) error
}

// CheckpointInfo is stored replay state supplied to a document's checkpoint
// predicate. DeltasSinceBase counts deltas already stored after the newest base,
// excluding the change being evaluated.
type CheckpointInfo struct {
	DeltasSinceBase int
}

// DocumentDefinition is the explicitly supplied definition of a singleton or
// keyed document family. Definitions are not registered globally. Scope,
// History, and Fork mirror the source semantics; Family marks a keyed family.
// Initial receives the family seed (nil for singletons).
type DocumentDefinition struct {
	Kind           string
	Version        int
	Scope          string
	History        string
	Fork           string
	Family         bool
	Initial        func(json.RawMessage) (JsonObject, error)
	Migrate        func(JsonObject, int) (JsonObject, error)
	CheckpointWhen func(JsonObject, []chord.Op, CheckpointInfo) (bool, error)
}

// TaskOwnership names who owns a task: its conversation (a top-level task) or
// another task of the same conversation (a child task).
type TaskOwnership struct {
	Kind   string `json:"kind"`
	TaskID TaskID `json:"taskId,omitempty"`
}

// TaskOptions configures durable task creation.
type TaskOptions struct {
	Ownership      TaskOwnership  `json:"ownership"`
	ConversationID ConversationID `json:"conversationId,omitempty"`
	Background     bool           `json:"background,omitempty"`
}

// ConversationOwnership is ownership selected explicitly whenever a conversation
// is created.
type ConversationOwnership struct {
	Kind   string `json:"kind"`
	TaskID TaskID `json:"taskId,omitempty"`
}
