# Durable records, Session, and storage contract

Source: Pi `a13d35a742c6ef8462812a28fbe1d8c8b7431c32`, principally
`packages/durable/src/types.ts`, `ids.ts`, `documents.ts`, `entries.ts`,
`tasks.ts`, `session/**`, `storage/**`, `testing/storage-conformance.ts`, and
the Session/storage tests. These are implementation references, not the later
Pico5 rewrite specification. Do not substitute the incompatible `after` task
graph found in later snapshots for this revision's ownership and join graph.

## Package ownership and stages

1. `packages/durable` owns shared records, constructors and validation, typed
   definitions, Storage/Tx/TaskRuntime interfaces, and the Session kernel.
2. `packages/durable/storage/memory` implements the complete detached in-memory
   storage contract. It imports the shared records but the root package never
   imports an adapter.
3. `packages/durable/storage/jsonl` implements local JSONL publication,
   recovery, poisoning, and reclamation.
4. `packages/durable/storage/sqlite` implements a real SQLite adapter and the
   database facade and migration history, using `database/sql` with
   `modernc.org/sqlite v1.44.3` and its matching `modernc.org/libc v1.67.6`.

Memory/records must be accepted before Session; Session must be accepted before
the harness runtime. JSONL and SQLite may be implemented independently after
records/memory, but delivery requires all three adapters. Keep source-to-target
mapping explicit, including source files merged into idiomatic Go files.

## Go API

The following names/signatures are the independently reviewed embedding facade.
Additional APIs may be added to represent every upstream export, but these
must not be removed or replaced by test-specific adapters. Types below live in
`github.com/minifish-org/pith/packages/durable` (`package durable`).

```go
type ID int64
type ConversationID = ID
type EntryID = ID
type TaskID = ID
type SubmissionID = ID
type DocumentID = ID
type Seq int64
const RootConversationID ConversationID = 1
type JsonObject = map[string]any

type ConversationParent struct { ConversationID ConversationID; At EntryID }
type ConversationOwner struct { ConversationID ConversationID; TaskID TaskID }
type ConversationRecord struct {
    ID ConversationID
    Parent *ConversationParent
    Owner *ConversationOwner
}
type ContextEdit struct {
    Target EntryID
    Action string
    Messages []types.Message
}
type EntryRecord struct {
    ID EntryID
    ConversationID ConversationID
    Kind string
    Model []types.Message
    Data json.RawMessage
    Head *EntryID
    Edits []ContextEdit
    ByTaskID *TaskID
}
type EntryDraft struct {
    Kind string
    Model []types.Message
    Data json.RawMessage
    Head *EntryID
    HeadSelf bool
    Edits []ContextEdit
}
type StoredEntry struct { Entry EntryRecord; CommitSeq Seq }
type TaskOutcomeError struct { Message string; Detail json.RawMessage }
type TaskOutcome struct {
    Status string
    Result json.RawMessage
    Error *TaskOutcomeError
    Reason string
}
type TaskState struct {
    Status string
    Checkpoint json.RawMessage
    On []TaskID
    Policy string
    Outcome *TaskOutcome
}
type TaskRecord struct {
    ID TaskID
    ConversationID ConversationID
    Kind string
    Version int
    Input json.RawMessage
    Owner *TaskID
    Background bool
    AbortRequested bool
    State TaskState
    Memos map[string]json.RawMessage
}
type SubmissionRecord struct {
    ID SubmissionID
    ConversationID ConversationID
    RequestID *string
    Type string
    Status string
    Entry *EntryID
    Answer *EntryID
    Reason string
    Detail json.RawMessage
}
type SubmissionCreate struct {
    ConversationID ConversationID
    RequestID *string
    Type string
    Status string
    Entry *EntryID
    Answer *EntryID
    Reason string
    Detail json.RawMessage
}
type SubmissionSettlement struct {
    Status string
    Answer *EntryID
    Reason string
    Detail json.RawMessage
}
type DocumentScope struct {
    Kind string
    ConversationID ConversationID
    TaskID TaskID
}
type DocumentRecord struct {
    ID DocumentID
    Kind string
    Key *string
    Scope DocumentScope
    History string
    Fork string
    CreatedAt Seq
    RetiredAt *Seq
}
type DocumentCreate struct {
    ID DocumentID
    Kind string
    Key *string
    Scope DocumentScope
    History string
    Fork string
}
type DocumentAddress struct { Kind string; Scope DocumentScope; Key *string }
type DocumentPoint struct { Current bool; Seq Seq }
func CurrentDocument() DocumentPoint
func AtSeq(seq Seq) DocumentPoint
type DocumentContent struct {
    Kind string
    Version int
    Value JsonObject
    Ops []chord.Op
}
type DocumentCopySource struct { ID DocumentID; At DocumentPoint }
type StoredDocument struct {
    Record DocumentRecord
    Version int
    Value JsonObject
    DeltasSinceBase int
}
type StorageWrite struct {
    Type string
    Conversation *ConversationRecord
    Entry *EntryRecord
    Task *TaskRecord
    Submission *SubmissionRecord
    Record *DocumentCreate
    Content *DocumentContent
    Source *DocumentCopySource
    ID DocumentID
}
type Cursor json.RawMessage
type Page[T any] struct { Items []T; Next Cursor }
type ConversationQuery struct { OwnerConversationID *ConversationID; OwnerTaskID *TaskID }
type EntryQuery struct { ConversationID ConversationID; MinEntryID *EntryID; MaxEntryID *EntryID }
type TaskQuery struct {
    ConversationID *ConversationID
    Kind string
    Status string
    AbortRequested *bool
    Background *bool
}
type SubmissionQuery struct { ConversationID *ConversationID; Status string }
type DocumentQuery struct { Scope DocumentScope; At DocumentPoint; Kind string }

type Storage interface {
    Commit(context.Context, []StorageWrite) (Seq, error)
    MintID(context.Context) (ID, error)
    Conversation(context.Context, ConversationID) (*ConversationRecord, error)
    ScanConversations(context.Context, ConversationQuery, int, Cursor) (Page[ConversationRecord], error)
    Entry(context.Context, EntryID) (*StoredEntry, error)
    VisibleEntry(context.Context, ConversationID, EntryID) (*StoredEntry, error)
    FindLatestHeadMarker(context.Context, ConversationID, *EntryID) (*EntryRecord, error)
    ScanEntries(context.Context, EntryQuery, int, Cursor) (Page[EntryRecord], error)
    Task(context.Context, TaskID) (*TaskRecord, error)
    ScanTasks(context.Context, TaskQuery, int, Cursor) (Page[TaskRecord], error)
    Submission(context.Context, SubmissionID) (*SubmissionRecord, error)
    ScanSubmissions(context.Context, SubmissionQuery, int, Cursor) (Page[SubmissionRecord], error)
    SubmissionByRequest(context.Context, ConversationID, string) (*SubmissionRecord, error)
    FindDocument(context.Context, DocumentAddress, DocumentPoint) (*DocumentRecord, error)
    Document(context.Context, DocumentID, DocumentPoint) (*StoredDocument, error)
    ScanDocuments(context.Context, DocumentQuery, int, Cursor) (Page[DocumentRecord], error)
    Close(context.Context) error
}

type CheckpointInfo struct { DeltasSinceBase int }
type DocumentDefinition struct {
    Kind string
    Version int
    Scope string
    History string
    Fork string
    Family bool
    Initial func(json.RawMessage) (JsonObject, error)
    Migrate func(JsonObject, int) (JsonObject, error)
    CheckpointWhen func(JsonObject, []chord.Op, CheckpointInfo) (bool, error)
}
type DocumentDraft struct { /* private, backed by one Chord change */ }
func (*DocumentDraft) Value() (JsonObject, error)
type TaskOwnership struct { Kind string; TaskID TaskID }
type TaskOptions struct { Ownership TaskOwnership; ConversationID ConversationID; Background bool }
type ConversationOwnership struct { Kind string; TaskID TaskID }
type PhaseHandler func(context.Context, TaskRecord, TaskRuntime) error
type TaskDefinition struct {
    Name string
    Version int
    Initial func(json.RawMessage) (json.RawMessage, error)
    Phases map[string]PhaseHandler
    Abort PhaseHandler
    Migrate func(json.RawMessage, json.RawMessage, int) (json.RawMessage, json.RawMessage, error)
}
type TaskRuntime interface {
    Commit(context.Context, func(Tx, *TaskRecord) error) error
    Memo(context.Context, string, json.RawMessage) (json.RawMessage, error)
    GetTask(context.Context, TaskID) (*TaskRecord, error)
    WaitForTask(context.Context, TaskID) (TaskOutcome, error)
    Outcomes(context.Context, []TaskID) ([]TaskOutcome, error)
    Sleep(context.Context, int64) error
    Now() int64
    Report(error)
}
type Tx interface {
    Conversation(context.Context, ConversationID) (*ConversationRecord, error)
    Entry(context.Context, EntryID) (*EntryRecord, error)
    Task(context.Context, TaskID) (*TaskRecord, error)
    ScanConversations(context.Context, ConversationQuery, int, Cursor) (Page[ConversationRecord], error)
    ScanEntries(context.Context, EntryQuery, int, Cursor) (Page[EntryRecord], error)
    LatestHeadMarker(context.Context, ConversationID) (*EntryRecord, error)
    ScanTasks(context.Context, TaskQuery, int, Cursor) (Page[TaskRecord], error)
    SubmissionByRequest(context.Context, ConversationID, string) (*SubmissionRecord, error)
    CreateConversation(context.Context, ConversationOwnership) (*ConversationRecord, error)
    ForkConversation(context.Context, ConversationID, EntryID, ConversationOwnership) (*ConversationRecord, error)
    AppendEntry(context.Context, ConversationID, EntryDraft) (*EntryRecord, error)
    CreateTask(context.Context, TaskDefinition, json.RawMessage, TaskOptions) (TaskID, error)
    CreateSubmission(context.Context, SubmissionCreate) (*SubmissionRecord, error)
    SettleSubmission(SubmissionID, SubmissionSettlement) error
    PlaceSubmission(SubmissionID, EntryID) error
    Doc(context.Context, DocumentDefinition, DocumentAddress, json.RawMessage) (*DocumentDraft, error)
    RetireDoc(context.Context, DocumentDefinition, DocumentAddress) error
}
type CommitPublication struct { Seq Seq; Changes []CommitChange }
type CommitChange struct {
    Type string
    Write *StorageWrite
    Record *DocumentRecord
    ConversationID *ConversationID
    Version *int
    Value JsonObject
    Ops []chord.Op
    Source *DocumentCopySource
}
type WatchEnd struct { Reason string; Err error }
type DocumentWatch struct { /* private */ }
func (*DocumentWatch) Value() JsonObject
func (*DocumentWatch) Start(func(context.Context, JsonObject, []chord.Op) error) error
func (*DocumentWatch) Stop() error
func (*DocumentWatch) Closed() <-chan WatchEnd
type DocumentReader interface {
    Snapshot(context.Context, DocumentDefinition, DocumentAddress) (JsonObject, error)
    SnapshotAsOf(context.Context, DocumentDefinition, DocumentAddress, EntryID) (JsonObject, error)
}
type DocumentObserver interface {
    WatchDoc(context.Context, DocumentDefinition, DocumentAddress) (*DocumentWatch, error)
}
type Session struct { /* private */ }
type StorageRejected struct { Message string; Cause error }
func (*StorageRejected) Error() string
func (*StorageRejected) Unwrap() error
type ReadAfterWrite struct { Method string }
func (*ReadAfterWrite) Error() string
func NewSession(Storage) *Session
func (*Session) Commit(context.Context, func(Tx) error) error
func (*Session) Snapshot(context.Context, DocumentDefinition, DocumentAddress) (JsonObject, error)
func (*Session) SnapshotAsOf(context.Context, DocumentDefinition, DocumentAddress, EntryID) (JsonObject, error)
func (*Session) WatchDoc(context.Context, DocumentDefinition, DocumentAddress) (*DocumentWatch, error)
func (*Session) DocumentState(context.Context, DocumentDefinition, DocumentAddress) (*chord.AttachedState, error)
func (*Session) SubscribeCommits(func(CommitPublication)) func()
func (*Session) SubscribeClose(func()) func()
func (*Session) Close(context.Context) error
```

`types.Message` above is Pith's existing AI message type, never a new Durable
message dialect. Nil `*Record`/nil `JsonObject` means absent, not an error.
Go record fields serialize using the upstream lower-camel field names. JSON
payloads retain unknown fields. Allocated IDs and commit sequences are positive
safe integers at most `9007199254740991`; root ID 1 is reserved, `MintID`
initially returns 2. Upstream idFromNumber/seqFromNumber are erased trusted
casts; do not turn them into a new validation policy for already-trusted records.
Optional pointers distinguish absent values, including an absent document key
from the valid empty family key. Cursor bytes are backend-owned JSON continuation
state, not offsets for callers to inspect. No scan returns more than its limit.

The wide root API above prevents adapter/harness dependency cycles. Additional
typed helpers can use package-level Go generics; Go does not support generic
methods. Root task definitions contain only scheduling-neutral callbacks;
the harness may expose an extension interface for its registry, models, agent,
environment, hooks and invocation-owned conversation handles.

Adapter construction:

```go
// package memory
func New() *Storage
// package jsonl
type Options struct { Fsync bool; FileSystem env.FileSystem }
func Open(context.Context, string, Options) (*Storage, error)
type CorruptionError struct { /* message/cause, errors.As supported */ }
type PoisonedError struct { /* cause, errors.As supported */ }
// package sqlite
type Options struct { WALAutoCheckpointPages *int; BusyTimeoutMS *int }
func Open(context.Context, string, Options) (*Storage, error)
```

Provide `StorageRejected`, `ReadAfterWrite`, and closed/poisoned errors with
`errors.As`/`errors.Is` support. `StorageRejected` means the batch definitely had
no effect. It is not a general wrapper for uncertain write errors.

## Record semantics

Storage owns atomicity, one global namespace, detached ownership, immutable
conversation/entry creation, document consistency and address uniqueness.
Session owns reference validation, task ownership and transitions, fork
selection, submission admission/settlement, and serialized transactions. A
storage adapter must not acquire a second Session-facing commit lock or call
user definitions. Adapter methods must remain race-safe when readers observe a
Session's in-flight commit; asynchronous Go APIs are represented by blocking
calls with `context.Context` and coordinated goroutines.

One commit is all-or-nothing across tables and document writes. Sequences
increase strictly, with allowed gaps. Empty storage commits still allocate a
sequence; an unchanged Session callback emits no storage commit/publication.
Fresh candidate IDs may be skipped after rollback, but committed IDs never
reappear in another table. A failed mixed batch leaves no earlier item visible.
Tasks and submissions replace whole mutable records. Entries/conversations are
immutable, including within a single batch. All retained inputs and read results
are detached, including RawMessage bytes, nested maps, slices, and pointer fields.

Input submissions use `queued`, `placed`, `done`, or `unanswered`; passive writes
use `queued`, `done`, or `unanswered`. A queued input has no entry/answer; placed
has entry; done input has entry/answer. Only placed input may be answered;
terminal settlement stays unchanged. Dedup keys are conversation-scoped,
including `""`. This lookup is not global deduplication.

Task states are `pending`, `running`, `waiting`, `completing`, `terminal`.
Waiting includes `On` plus `failFast`/`allSettled`. `Owner` is a task ID for child
tasks, absent for conversation-owned tasks. Background applies only to
conversation-owned tasks. Terminal/completing outcomes are `completed`, `failed`,
`aborted`, `orphaned`, `faulted`; those states discard live memos/checkpoints.
Do not replace this graph with a dependency-only `After` field.

Conversation/task/submission/document scans are ascending IDs. Entry scans are
newest-first and respect inclusive bounds and every ancestor fork cap. A child
must not see parent entries appended after its cut, even through a grandchild.
VisibleEntry is the overload split of upstream entry(conversation,id), not
global lookup followed by a conversation-ID equality check. Head lookup returns
the newest visible marker, retaining that marker's actual head value.

## Documents and Session

Lifetimes are half-open `[CreatedAt,RetiredAt)`. Creation+retirement in one batch
has empty lifetime. Logical address includes scope, kind, and optional family
key. Retire+recreate creates a new incarnation; an exact Document(id,point) never
follows the address to a replacement. Current-only session/task/latest
conversation documents reject historical content reads. Rewindable documents
retain all required bases/deltas including after retirement.

Creation/version transition requires a base; a delta cannot cross versions.
Materialization starts from the newest eligible base, applies canonical Chord
ops in order, and returns the stored version plus replay count. A base of a
rewindable document bounds replay but never authorizes history reclamation.
Document copy is definition-free and preserves the stored version; the source
may not change in the copy batch.

Definitions are supplied explicitly, not registered globally. Only `Tx.Doc`
creates a document. Snapshot/state/watch/as-of reads never create. Family initial
seed is used exactly once per absent logical address in a transaction. Task
documents are retired atomically at terminal settlement and cannot be accessed
after a terminal candidate in that same transaction. Later lazy access migrates
older stored versions; read-only migration changes no storage. First successful
mutable access writes the required new-version base even without content edits.
Newer stored versions and unavailable migrations reject. Checkpoint predicates
run exactly once after Chord preparation; failure rolls back before storage.

Session holds one mutation line through callback, preparation, durable
settlement, adoption and publication. Read every needed table before the first
table write; later table reads raise ReadAfterWrite. Document read-your-writes
remain legal. Escaped Go maps cannot be language-level revoked: returned drafts
are private working copies, preparation detaches them, and DocumentDraft.Value
rejects after sealing. This explicit Go adaptation must be documented.

Cancellation before callback admission rejects without effects. Once storage
settlement begins, use `context.WithoutCancel`; acknowledged commits remain
committed even if the caller cancels. Callback/preparation/checkpoint failure
allows later commits. A known StorageRejected allows later commits; uncertain
storage/adoption failure poisons the Session until reopen. Close seals admission
immediately, terminates watches, joins admitted work, then closes storage.

Fork transcript cut uses the concrete visible entry ID, not today's last entry.
Document `asOf` uses final document state of the cut entry's commit; `current`
uses source state when fork commits; `initial` creates no document copy and
initializes lazily with the supplied definition. Session/task documents and task
records never copy. Family members and definition-free copies obey the same
rules. Child changes never modify parent records.

Watch/state acquisition atomically captures committed state and registers for
later commits, never drafts. Watch callbacks serialize on a delivery line and
never run inline in Start or a Session callback. Preserve bounded pending frames
and canonical reset on slow observers. Retirement delivers null/reset and binds
the handle to the original incarnation; recreation does not revive it.
Stop is idempotent; listener errors close/report only that observer. Standard
contexts define cancellation lifetime. Keep a13 source watch completion and
callback ownership semantics explicit; do not silently substitute Pico5's newer
stronger shutdown guarantees.

## JSONL publication and recovery

Persist the upstream format-1 layout with lower-camel JSON field names:
`main.jsonl`, `doc-<id>.jsonl`, `task-<id>.jsonl`. One commit appends prepared
records to each affected sidecar, then one main marker, then publishes memory.
There is no sidecar-only commit shortcut. Durable Fsync mode syncs sidecars
before the marker, and syncs the marker before destructive reclamation.
Default mode promises ordinary process-crash consistency, not power/host/kernel
failure durability. Do not claim an OS process-kill test proves power-loss safety.

Recovery truncates incomplete final lines at byte boundaries (including torn
UTF-8), removes unconfirmed sidecar tails, replays only confirmed markers, and
checks strict sequence/ordinal/reference consistency. Missing confirmed data,
malformed complete lines, unsupported format, conflicting/duplicate committed
records and invalid complete UTF-8 are corruption; never silently reset state.
Later committed current-only bases/retirement may prove earlier physical data
unneeded; rewindable history remains required. Appending unconfirmed live task
records cannot resurrect a terminal receipt.

Uncertain append/sync failures poison the open backend; no further read or write
may imply a known consistent state. Reopen decides the confirmed durable state.
Reclamation begins after the authorizing marker, writes a replacement `.reclaim`,
syncs when required, renames, and invalidates cached descriptors. Reclaimed
files must be the targets of future appends, never unlinked old inodes.

## SQLite adapter and dependency evidence

Use real SQLite transactions, rows/indexes, and private revision storage. One
SQL transaction is one storage batch; do not apply Chord deltas as SQL JSON
patches. Initial schema matches the pinned migration history, with future
versions append-only, contiguous and atomic. Reopen is idempotent; a database
with a newer schema is rejected without rewriting it. WAL, NORMAL synchronous,
1,000-page auto checkpoint and 5,000ms busy timeout match upstream defaults;
options permit deliberate override. Scope/point scans use bounded indexed reads.
Keep query-plan, revision reclamation and deleted-page-reuse tests, not just a
map serialized into one SQLite blob. Preserve the replaceable database facade
for adapters and rollback/fault testing.

The driver author documents a CGO-free SQLite port through database/sql and
requires matching modernc.org/libc versions: [official driver documentation](https://pkg.go.dev/modernc.org/sqlite).
The exact selected [v1.44.3 module manifest](https://gitlab.com/cznic/sqlite/-/blob/v1.44.3/go.mod)
requires Go 1.24.0 and libc 1.67.6. Its license is BSD-3-Clause; bundled SQLite
is public domain. Pin driver and transitive checksums in preparation go.mod/sum;
update product go.mod only during verified integration. Do not opportunistically
upgrade the product toolchain or add a CGO sqlite driver.

## Independent acceptance

`judges/storage-records` runs immutable/global-namespace/mixed rollback,
detached values, replacement/filter/dedup, paging and deep fork cap cases against
memory. `judges/storage-jsonl` repeats the same full suite on JSONL and adds
tail/corruption/reopen/process-kill tests. `judges/storage-sqlite` repeats it on
SQLite, including real SQL file/schema and process-kill recovery. The shared
suite is frozen separately from candidate-owned upstream conformance ports.
`judges/storage-session` checks callback rollback, mutation-line concurrency,
cancellation admission, poison-vs-rejection, document migration/checkpoint/fork,
non-creating reads and observer publication through the embedding facade.

Actual subprocess tests use the Go test binary as a child: child commits and
flushes a ready line to an OS pipe, starts an uncommitted Session callback, then
blocks on another pipe. Parent kills it at that announced boundary, joins it,
reopens the adapter and verifies complete acknowledged state and no ghost
callback mutations. Synchronization uses pipes/channels; timers only guard a
broken test from hanging. These tests do not use paid models or depend on
external LLM endpoints.

Port upstream storage-conformance, SQLite facade/migration/query-plan,
reclamation/size, JSONL short-write/every marker boundary, Session states,
forks, documents, tables, watches and migrations tests as candidate-owned
evidence. The independent suite complements them rather than claiming full
parity from a few API smoke cases. All ordinary acceptance runs/builds use
CGO_ENABLED=0; CGO_ENABLED=1 is permitted only for Go's race detector, not as a
product dependency. Delivery must cross-build all supported release platforms.
