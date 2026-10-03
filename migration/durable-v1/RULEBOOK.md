# Durable v1 migration rulebook

Port Pi v1.0.0 a13d35a742c6ef8462812a28fbe1d8c8b7431c32 into the optional Pith Durable SDK. This is the implemented runtime at that pinned commit; never substitute later Pico5 architecture documents or code from another snapshot.

Use native Go, mature pinned libraries where useful, and CGO_ENABLED=0 for normal builds. Race instrumentation may use CGO_ENABLED=1; the product must have no CgoFiles. No Node, npm, TypeScript/JavaScript engine, external database server or live credentials are needed by the delivered core runtime. SQLite is modernc.org/sqlite v1.44.3 with libc v1.67.6, already frozen in dependency manifests. Do not edit manifests or licences or run go mod tidy.

All 60 Durable runtime files are in scope; required Chord closure is scoped in chord.md. Full source behavior must be translated, including candidate-owned tests corresponding to source tests. Independent judges are mandatory finite checks, not permission to implement only sampled behavior. Benchmarks are optional execution, but their public helpers are in scope. TS-specific branded/generic conveniences map to safe native identifiers/interfaces and documented Go helpers. Document explicit host/runtime format differences; do not claim a line-for-line or binary-compatible API.

Use the original TS tests as behavior references, never copy frozen Go judges or rewrite historical tests. Do not add test-only shortcuts, permanent placeholders, bypass replay policy or stub SQLite with a memory/JSON file. Never silently fabricate task completion, usage, model replies or guarantees. Application callbacks and foreign I/O are ordinary Go code; no arbitrary external effect can be guaranteed exactly once.

Only the declared candidate outputs (including earlier outputs of this module) are writable. Existing frozen packages are read-only except the three reviewed AI prompt-order bridge files and three SDK documentation files. Preserve public APIs and every cumulative judge. No paid calls during preparation/checking. Implementation calls belong to the operator-started migrate invocation. Let Portsmith handle verifications, checkpoints, transactional integration and commits.

For recovery tests use actual subprocess termination and synchronization barriers rather than sleeps or timing guesses. Context/deadline safeguards in tests are hang detectors, not unattended model-run limits. Do not introduce attempt/turn/runtime caps; cancellation and authentication errors must still preserve state. Normal local file/command tools are host capabilities, not a permission sandbox.

The shared contracts below are visible throughout the run to prevent independent stage API invention. The CURRENT step goal determines which implementation files are being introduced; later stages must retain these interfaces and all source-defined behavior.

# Chord dependencies required by Pi Durable

This step ports the complete Chord runtime dependency closure needed by the
Durable SDK at Pi revision `a13d35a742c6ef8462812a28fbe1d8c8b7431c32`.
It adds strict JSON ownership, decoded delta operations, transactional revision
tracking, and source-attached publication. It preserves the already shipped
`packages/chord/context` API and uses standard `context.Context` at SDK boundaries.

## Source closure and scope

Read these complete files, while porting only the dependency slice identified here:

| Upstream file | Lines | Required responsibility |
| --- | ---: | --- |
| `packages/chord/src/index.ts` | 85 | Public JSON/state/type exports |
| `packages/chord/src/api.ts` | 110 | Source form of `replicatedState` |
| `packages/chord/src/types.ts` | 339 | JSON, context, source attachment, publication contracts |
| `packages/chord/src/json.ts` | 120 | Strict JSON validation and alias-free copying |
| `packages/chord/src/context/index.ts` | 121 | Existing Go API compatibility |
| `packages/chord/src/delta/index.ts` | 694 | Operations, safety, overlap, apply, immutable replay, codec |
| `packages/chord/src/delta/tracker.ts` | 2,205 | Draft, prepared revision, ownership and adoption |
| `packages/chord/src/delta/diff.ts` | 523 | Correct revision diff generation |
| `packages/chord/src/delta/draft.ts` | 10 | Mutable draft type adaptation |
| `packages/chord/src/delta/apply-immutable-trusted.ts` | 128 | Trusted replay ownership rules |
| `packages/chord/src/delta/revision-validator.ts` | 75 | Strict immutable revision validation |
| `packages/chord/src/services/state.ts` | 457 | Attached source and independent serialized delivery |
| `packages/chord/src/services/state-internals.ts` | 19 | Snapshot/publication ordering contract |

The complete reference files contain 4,886 lines. This is not a requirement to
reimplement every declaration in these files. In particular, Durable imports no
Chord facet host, remote-service protocol, JavaScript bundler, or Node loader.
Those independent Chord products and optional facet/remote integrations shown in
`packages/durable/test/chord-guide.test.ts` are deferred explicitly. All source
attachment behavior used by document states, conversation views, and task graphs
is in scope.

Selected behavior references are Chord `json.test.ts`, `context.test.ts`,
`delta.test.ts`, `delta-apply-immutable.test.ts`, `delta-diff.test.ts`,
`delta-clone.test.ts`, `delta-tracker/tracker.test.ts`, `state.test.ts`, and
`state-delivery.test.ts` (3,417 lines in total). Durable document/watch/live-delta
and guide tests are additional cross-module references. Benchmarks, weak-reference
retention metrics, and JavaScript-specific object identity are not correctness
requirements for the Go port.

## Public Go surface

Use `github.com/minifish-org/pith/packages/chord` and its `delta` subpackage.
Public signatures below are fixed for downstream Durable steps and independent
judges. Additional helpers are allowed without replacing these signatures.

```go
// packages/chord
type JSONValue = any
type Op = delta.Op
type Path = delta.Path
func CopyJSON(value any) (any, error)
func IsJSONValue(value any) bool

type StateSnapshot struct { Value any; Cursor uint64 }
type StateFrame struct {
    Value any
    Cursor uint64
    Ops []delta.Op
    Context context.Context
}
type StateSource interface { Attach() (StateAttachment, error) }
type StateAttachment interface {
    Snapshot() StateSnapshot
    Activate(func(StateFrame)) error
    Dispose()
}
type StateDelivery struct { Kind string; Sequence uint64 }
type StateListener func(any, context.Context, StateDelivery) error
type StateOptions struct { OnError func(error) }
func AttachReplicatedState(StateSource, StateOptions) (*AttachedState, error)
func (*AttachedState) Value() any
func (*AttachedState) Subscribe(StateListener) (func(), error)
func (*AttachedState) Dispose()

// packages/chord/delta
type Path []any
type Op []any
func Apply(any, []Op) (any, error)
func ApplyImmutable(any, []Op) (any, error)
func ApplyImmutableBatches(any, [][]Op) (any, error)
func DiffRevisions(any, any) ([]Op, error)
func Overlap(a, b string, scan int) int
func Track(any) (*Tracker, error)
func (*Tracker) Value() any
func (*Tracker) Revision() uint64
func (*Tracker) BeginChange() (*Change, error)
func (*Tracker) PrepareReplace(any) (*Prepared, error)
func (*Tracker) Adopt(*Prepared) error
func (*Change) Value() (any, error)
func (*Change) Prepare() (*Prepared, error)
func (*Change) Abort()
func (*Prepared) Base() any
func (*Prepared) Value() any
func (*Prepared) Ops() []Op
func (*Prepared) BaseRevision() uint64
func (*Prepared) Abort()
type WireOp []any
func NewEncoder() *Encoder
func (*Encoder) Encode([]Op) ([]WireOp, error)
func NewDecoder() *Decoder
func (*Decoder) Decode([]WireOp) ([]Op, error)
func IsBase([]Op) bool
func IsReplace(Op) bool
```

## JSON and tuple safety

Canonical runtime JSON containers are `map[string]any` and `[]any`, with null,
booleans, strings, and finite numeric values. Copying must duplicate each
container occurrence: a shared input child becomes independent output children.
Detect ancestor cycles, reject non-finite numbers, functions, channels, and
unsupported objects. Strict JSON is a runtime boundary, not merely `any`.
Go numeric primitives may normalize to JSON numbers; correctness comparisons
are by JSON value rather than a particular Go numeric representation.

Persist and expose operations as the exact decoded Pi tuple vocabulary:
`r`, `s`, `d`, `a`, `t`, `p`, and `m`. Retain numeric array indices and string
object keys as distinct path segments. No path may contain `__proto__`,
`constructor`, or `prototype`. Reject malformed arity, unknown verbs, fractional
or negative indices, unresolved paths, non-array splice/permutation targets,
non-bijective permutations, and sparse-array writes. Root replacement is `r`;
root `s`, `d`, `a`, or `t` is invalid. Root array splice and permutation are legal.

`a` appends a string. `t` removes the requested number of **UTF-16 code units**
from the start, matching upstream JavaScript and on-disk tuple compatibility.
`Overlap` returns UTF-16 units too, honors the requested scan window, and must
never claim a suffix/prefix match that is false. Do not use raw UTF-8 byte counts
for stored string offsets. Go cannot represent isolated UTF-16 surrogates as
valid UTF-8 strings: reject a truncation that bisects a supplementary character
and document this narrow adaptation rather than corrupting persisted data.

`Apply` may mutate a caller-owned target and adopt tuple payload ownership.
`ApplyImmutable` and batch replay must leave earlier revisions unchanged.
No exact operation minimization strategy or internal structural sharing is
required: replay of emitted operations must equal the prepared value. No-op
diffs and unchanged draft preparation must emit an empty operation batch.

## Transactional tracking

Track accepts only JSON object/array roots. A change holds a private mutable
working copy. Preparation validates and detaches that copy, records its matching
base revision, and leaves the tracker unchanged. An aborted candidate never
changes authority. Adoption is the commit boundary: require the same owner,
current base revision, prepared status, and one-time consumption. Reject foreign,
stale, aborted, and previously adopted candidates. Adoption advances revision
once, including an accepted no-op candidate; competing open/prepared changes
then become stale.

Go cannot revoke an escaped map reference like a JavaScript Proxy. The explicit
Go adaptation is to invalidate the change handle, detach candidate data during
preparation, and prevent mutations of retained draft maps or public candidate
getters from changing committed revisions. Value getters must return independent
copies where needed to enforce this isolation. Callers own drafts while editing
and use `CopyJSON` when assigning externally owned data. Errors are returned
explicitly; no implementation may silently turn an invalid draft into a no-op.

## Source-attached publication

Attachment atomically captures an immutable value and a source cursor. Activation
installs one listener and drains buffered frames in source order. A frame cursor
must be exactly the previous source cursor plus one. A gap, duplicate, or invalid
JSON revision stops/disposes that attachment and reports the error through
`StateOptions.OnError`; it must not overwrite the last valid value. If activation
fails, dispose the attachment before returning the error.

An attached state's publication sequence starts at zero. Each subscriber first
receives hydration at the current publication sequence, then future updates in
increasing sequence order. Every subscription serializes its callbacks
independently. Go callback workers may begin asynchronously; a slow subscriber
must not block the source or another subscriber. Preserve a maximum of 100
pending deliveries: when that backlog overflows, coalesce pending updates to
the newest revision while retaining a not-yet-started hydration. This is an
upstream correctness rule, not a new arbitrary migration limit.

Report callback errors in isolation and keep delivering subsequent revisions.
Unsubscribe discards pending work without cancelling or joining an active
callback. Dispose is idempotent, releases the source attachment once, ignores
later source frames, and preserves the last value for reading. Propagate the
provided invocation context for updates; hydration uses a background context.
Source frames carry authoritative revisions: publication must not re-diff or
re-apply their operations as an alternative authority.

## Output ownership and implementation freedom

Recommended new files are `packages/chord/{json,types,state}.go` and
`packages/chord/delta/{operations,diff,tracker,codec}.go`, plus focused normal
implementation tests. More idiomatic file splitting is allowed within these
owned directories. Existing `packages/chord/context/{context,value}.go` and
all their exported names stay compatible; no known change is required there.
Never modify permanent independent judges or unrelated packages to satisfy this
step. The port may use mature pure-Go dependencies, but this dependency slice
should normally need only the standard library. Builds and ordinary validation
run with `CGO_ENABLED=0`.

Codec interning occurs on a path's second use. Dictionary definitions and IDs
are local to one state stream. Same-path omission is scoped to one batch; a
batch's first operation always has an explicit path or an existing dictionary
reference. A replacement resets dictionary state so later recovery can start
with a fresh decoder. Unknown IDs, omitted first paths, invalid tuple grammar,
and unsafe dictionary paths return errors. Encoding/decoding is lossless over
decoded operations and does not mutate its input.

Candidate-owned normal tests should be added beside JSON/state/delta code for
all exported behavior. They must cover error/rollback paths, invalid ownership,
Unicode boundary behavior, no-op revisions, stalled observers and disposal.
The independent `portsmith_judge_*` tests remain read-only acceptance evidence;
they supplement implementation tests and do not claim exhaustive full-Chord
parity. The existing context implementation and its normal tests stay present.


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


# Durable execution environment

Source: Pi v1.0.0 `a13d35a742c6ef8462812a28fbe1d8c8b7431c32`,
`packages/durable/src/env/{index,node}.ts` and its environment tests.
Implement the complete portable capability surface, with an ordinary Go local
adapter. This package is independent of the Durable harness and storage.

## Frozen native API

Package `github.com/minifish-org/pith/packages/durable/env` uses `context.Context`
as the first argument of every fallible operation and idiomatic `(value, error)`
instead of JavaScript `Result`. All exported error types implement `error` and
`Unwrap`; use `errors.As`, not string matching.

```go
type FileError struct { Code, Path string; Cause error }
type ExecutionError struct { Code, SpillPath string; Cause error }
type FileInfo struct { Name, Path, Kind string; Size int64; MtimeMs int64 }
type TextLine struct { Text string; Terminated bool }
type ReadTextLinesOptions struct { MaxLines int }
type RemoveOptions struct { Recursive, Force bool }
type TempFileOptions struct { Prefix, Suffix string }
type TextLineReader interface {
    ReadLine(context.Context) (*TextLine, error) // nil at EOF
    Close(context.Context) error
}
type FileSystem interface {
    ID() string
    CWD() string
    SetCWD(string) error
    AbsolutePath(context.Context, string) (string, error)
    JoinPath(context.Context, ...string) (string, error)
    ReadTextFile(context.Context, string) (string, error)
    OpenTextLineReader(context.Context, string) (TextLineReader, error)
    ReadTextLines(context.Context, string, ReadTextLinesOptions) ([]string, error)
    ReadBinaryFile(context.Context, string) ([]byte, error)
    WriteFile(context.Context, string, []byte) error
    AppendFile(context.Context, string, []byte) error
    TruncateFile(context.Context, string, int64) error
    FlushFile(context.Context, string) error
    RenameFile(context.Context, string, string) error
    FileInfo(context.Context, string) (FileInfo, error)
    ListDir(context.Context, string) ([]FileInfo, error)
    CanonicalPath(context.Context, string) (string, error)
    Exists(context.Context, string) (bool, error)
    CreateDir(context.Context, string, bool) error
    Remove(context.Context, string, RemoveOptions) error
    CreateTempDir(context.Context, string) (string, error)
    CreateTempFile(context.Context, TempFileOptions) (string, error)
    Cleanup(context.Context) error
}
type SpillOptions struct { AfterBytes, AfterLines int }
type ExecOptions struct {
    CWD string
    Env map[string]string
    InheritEnv *bool // nil means true
    Timeout time.Duration // zero means no default timeout
    OnOutput func(context.Context, []byte) error
    Spill *SpillOptions
}
type ExecResult struct { ExitCode int; SpillPath string }
type Shell interface {
    Exec(context.Context, string, ExecOptions) (ExecResult, error)
}
type ExecutionEnv interface { FileSystem; Shell }
func NewLocal(cwd string) (*Local, error)
// *Local implements ExecutionEnv.
```

Match source error codes (`aborted`, `not_found`, `permission_denied`,
`not_directory`, `is_directory`, `invalid`, `not_supported`, `unknown` for files;
`aborted`, `timeout`, `shell_unavailable`, `spawn_error`, `callback_error`,
`unknown` for commands). Cancellation must be checked before admission and
between operations. A cancelled operation must not silently mutate a file.

Local instances share a namespace ID even when CWD differs. Resolve symlinks for
canonical existing paths; resolve the existing parent for a new path. Preserve
line terminators accurately: CRLF removes CR from Text, Terminated records LF;
unterminated last line is returned once. Avoid scanner's default 64-KiB limit.
Flush uses fsync. Cleanup removes only temp paths owned by this environment.

Use the local shell (`bash` on POSIX; a documented native Windows fallback).
Emit combined stdout/stderr chunks without truncation before the harness bounds
them. Spill the COMPLETE raw stream, including chunks before the threshold, if
either byte or logical-line threshold is exceeded. Exactly-at-threshold is not
an overflow. Return ordinary nonzero process status in ExecResult; it is the
bash tool's job to mark that status as a failed tool. Cancel the process tree,
drain output, reap the process, preserve SpillPath on timeout/cancellation, and
report callback failures rather than swallowing them. Remote/container adapters
can implement this same interface; do not bake host filesystem assumptions into
the interface or claim it is an authorization sandbox.

## Acceptance

Independent judges exercise long CRLF/UTF-8 lines, EOF, namespace/canonical paths,
truncate/append/flush/rename, pre-cancel mutation rejection, missing-file typed
errors, full spill contents, strict thresholds, callback errors and subprocess
cancellation. Candidate-owned tests must additionally cover the other exported
methods and platform-specific shell behavior. In particular, exercise cancellation
and timeout after a spill has actually been created, using a persistence barrier
or controlled file-adapter fault injection rather than sleeps. Preserve that
existing spill's path and bytes in the typed error. Cancellation in the very first
output callback can precede spill admission in the pinned source; it does not
require creating a new spill file after cancellation. No live provider is needed.


# Ordered system-section bridge

Pi revision `a13d35a742c6ef8462812a28fbe1d8c8b7431c32` treats system section object insertion order as semantic. See `packages/ai/src/types.ts`, `packages/ai/src/utils/text.ts`, `packages/ai/src/utils/transcript.ts`, and Durable `src/harness/prompt.ts`. Durable requests can remove and re-add equal-valued sections in a different order. The existing Go port deliberately sorts `SystemSections` maps, which cannot represent this behavior and prevents faithful Durable prompt replay.

The permitted compatibility change keeps `type SystemSections map[string]*string` and all existing public fields/callers. Add `SectionOrder []string` with `json:"-"` to `types.SystemMessage`. Do not expose an extra wire field. Its custom JSON decoder captures section property order, including null removals. Its encoder writes section properties in SectionOrder, ignoring duplicate/stale names and appending unlisted map keys in sorted order for deterministic map-only callers. JSON escaping, nulls, content variants, tool fields, timestamps, and strict error handling retain their existing contracts. Decoding one message twice clears old metadata. `Message` union encode/decode must preserve it automatically. Native message copies/checkpoints must detach the slice.

`utils.GetSystemMessageText`, `RenderSystemMessageUpdate`, and `GetCurrentSystemMessage` honor recorded order rather than sorting known ordered messages. Replay preserves existing key position on replacement, removes position on null removal, and appends a reintroduced name. The folded SystemMessage records the resulting SectionOrder; downstream providers thereby preserve the same order. Nil/absent metadata still uses sorted fallback. Do not mutate input message maps or metadata during rendering/replay.

Existing products can continue initializing maps. New Durable system baseline/delta messages always set SectionOrder from the registry-selected ordered sections. This bridge is a source-semantic correction, not a generic AI refactor. Exactly three previously committed files are writable: `packages/ai/types/types.go`, `packages/ai/utils/text.go`, and `packages/ai/utils/transcript.go`, plus new candidate regression tests. Existing tests/judges cannot be rewritten. Full AI/provider/core integration is required after the bridge.

Independent judges decode intentionally reverse-alphabetic section order and exercise wire roundtrip, direct rendering, update rendering, historical remove/re-add replay, explicit native order, map-only fallback, and input ownership. They can run against the old product as a negative control: the first render currently alphabetizes those sections and must fail.


# Durable harness contract

The source authority is Pi commit `a13d35a742c6ef8462812a28fbe1d8c8b7431c32`, specifically `packages/durable/src/harness/*.ts`, `tasks.ts`, `types.ts`, `entries.ts`, and the corresponding executable `test/harness-*.test.ts` suites. This contract ports that implemented version. Later Pico5 design documents are not a replacement source. Every exported source operation must have a documented native Go counterpart and source mapping; private files may be combined idiomatically.

## Packages and API

`packages/durable` owns records, task definitions, document definitions, storage, Session, and Tx. `packages/durable/harness` owns the scheduling and model/tool runtime. `packages/durable/env` owns the native environment interfaces. Existing Pith AI types and provider implementations are reused. Context arguments are first; operation errors are returned. IDs are `durable.ID` aliases, not JS numbers. All JSON values are detached and valid JSON. No CGo or JavaScript execution is introduced.

The following signatures are frozen for independent judges. Additional ergonomic typed helpers are welcome but cannot substitute for these operations.

```go
type ModelRef struct { Provider string; ModelID string }
type ModelRequest struct {
    ConversationID durable.ConversationID
    Model ModelRef
    Messages []types.Message
    Tools []types.Tool
    ThinkingLevel types.ThinkingLevel
    Options ConversationStreamOptions
    Summary bool
    Instructions string
}
type ModelRunner interface {
    Resolve(context.Context, ModelRef) (*types.Model, error)
    Run(context.Context, ModelRequest, func(types.AssistantMessage) error) (types.AssistantMessage, error)
}
type DeferredModelRunner interface {
    Poll(context.Context, ModelRef, types.DeferredHandle) (types.AssistantMessage, error)
    Cancel(context.Context, ModelRef, types.DeferredHandle) error
}
func NewPithModelRunner(
    func(context.Context, ModelRef) (*types.Model, error),
    *types.SimpleStreamOptions,
) ModelRunner
type Options struct {
    Models ModelRunner
    Registry RegistryReader
    Settings func() Settings
    Env func(context.Context, EnvTarget) (env.ExecutionEnv, error)
    ConversationCreated func(context.Context, durable.Tx, durable.ConversationRecord) error
    Now func() int64 // milliseconds
    OnReport func(error)
}
func Open(context.Context, durable.Storage, Options) (*Harness, error)

func CreateRegistry() *Registry // includes exactly the pinned built-in task kinds
type RegistryReader interface { Snapshot() RegistrySnapshot; Subscribe(func()) func() }
func (*Registry) Install(Extension) error
func (*Registry) Uninstall(string) error
type Extension struct {
    Name string
    Tools []ToolRegistration
    Sections []PromptSection
    Hooks []HookRegistration
    Wraps []Wrap
    Tasks []durable.TaskDefinition
}
type RegistrySnapshot interface {
    Installed() []Extension
    Extension(string) (Extension, bool)
    Tools() []RegisteredTool
    Sections() []RegisteredSection
    Tasks() []durable.TaskDefinition
    Task(string) (durable.TaskDefinition, bool)
}
type RegisteredTool struct { Extension Extension; Tool ToolRegistration }
type RegisteredSection struct { Extension Extension; Section PromptSection }
type HookRegistration struct { Task string; Handlers map[string]HookHandler }
type HookHandler func(context.Context, json.RawMessage, HookAPI) (json.RawMessage, error)
type Wrap struct {
    Tool string
    Section string
    WrapTool func(ToolRegistration) (ToolRegistration, error)
    WrapSection func(PromptSection) (PromptSection, error)
}
type PromptSection struct {
    Key string
    Render func(context.Context, PromptInput) (string, error)
    Untagged bool // false matches source tag=true
}
type PromptInput struct {
    ConversationID durable.ConversationID
    Agent Agent
    Env env.ExecutionEnv
    Shown map[string]string
    Read durable.DocumentReader
}
type EnvTarget struct { ConversationID durable.ConversationID; CWD string; Read durable.DocumentReader }

type Agent struct {
    Model *ModelRef
    ThinkingLevel types.ThinkingLevel
    Extensions []Extension
    Tools []ToolRegistration
    Sections []PromptSection
    Instructions string
    CWD string
}
// Configure and creation Agent fields are raw JSON patches using the source
// AgentState wire names. Omission leaves a value unchanged; null clears it.
// extensions/tools selections persist names, never Go callbacks or objects.
type CreateOptions struct {
    Ownership durable.ConversationOwnership
    Agent json.RawMessage
    Init func(context.Context, durable.Tx, durable.ConversationID) error
}
type RootOptions struct { Agent json.RawMessage; Init func(context.Context, durable.Tx, durable.ConversationID) error }
type AbortOptions struct { Background bool }
type SubmissionDraft struct {
    Type string // input or write
    Content types.UserContent
    Entry *durable.EntryDraft
    RequestID string
    WhenBusy string // steer, followUp, reject; absent defaults followUp
}
type ContextView struct {
    Head *durable.EntryRecord
    Entries []durable.EntryRecord
    Contributions [][]types.Message
    Messages []types.Message
}

func (*Harness) Resume() error
func (*Harness) Close(context.Context) error
func (*Harness) Root(context.Context, RootOptions) (*Conversation, error)
func (*Harness) Conversation(context.Context, durable.ConversationID) (*Conversation, error)
func (*Harness) CreateConversation(context.Context, CreateOptions) (*Conversation, error)
func (*Harness) Commit(context.Context, func(durable.Tx) error) error
func (*Harness) GetTask(context.Context, durable.TaskID) (*durable.TaskRecord, error)
func (*Harness) Inspect(context.Context) (Inspection, error)
func (*Harness) Submission(context.Context, durable.SubmissionID) (*Submission, error)
func (*Harness) AbortSubmission(context.Context, durable.SubmissionID, durable.ConversationID) (string, error)
func (*Harness) AbortTask(context.Context, durable.TaskID) (string, error)
func (*Harness) WaitForTask(context.Context, durable.TaskID) (*durable.TaskRecord, error)
func (*Harness) WaitForIdle(context.Context) error
func (*Harness) Usage(context.Context) (UsageState, error)
func (*Harness) TaskGraph(context.Context) (*TaskGraphState, error)
func (*Harness) WatchTaskGraph(context.Context) (*TaskGraphWatch, error)
// Expose the Session document Snapshot/SnapshotAsOf/WatchDoc/State methods
// and SubscribeCommits/SubscribeClose with the same admission/lifetime gates.

func (*Conversation) ID() durable.ConversationID
func (*Conversation) Agent(context.Context) (Agent, error)
func (*Conversation) Configure(context.Context, json.RawMessage) error
func (*Conversation) Submit(context.Context, SubmissionDraft) (*Submission, error)
func (*Conversation) Reset(context.Context, string) error
func (*Conversation) Compact(context.Context, string) (durable.TaskID, error)
func (*Conversation) Commit(context.Context, func(durable.Tx) error) error
func (*Conversation) Context(context.Context) (ContextView, error)
func (*Conversation) Entries(context.Context, durable.EntryQuery, int, durable.Cursor) (durable.Page[durable.EntryRecord], error)
func (*Conversation) Fork(context.Context, durable.EntryID, CreateOptions) (*Conversation, error)
func (*Conversation) Abort(context.Context, AbortOptions) error
func (*Conversation) WaitForIdle(context.Context) error
func (*Conversation) ViewState(context.Context) (*ConversationState, error)
func (*Conversation) Watch(context.Context) (*ConversationWatch, error)

func (*Submission) ID() durable.SubmissionID
func (*Submission) Status(context.Context) (*durable.SubmissionRecord, error)
func (*Submission) Wait(context.Context) (*durable.SubmissionRecord, error)
func (*Submission) Abort(context.Context) (string, error)
```

`Settings` preserves the source `extensions`, `stream`, `retry`, `compaction`, `toolExecution`, `steeringMode`, and `followUpMode` meanings. `ConversationStreamOptions` preserves transport, timeout, provider retries and maximum delay, headers, metadata, cache retention, and deferred options. Native time.Duration helpers may be provided; persisted deadlines remain integer milliseconds. Resolve settings at each decision, including getters supplied by the host. Defaults are exactly source `agent.ts`: retry enabled, 3 retries, 2000 ms delay, 60000 ms maximum agent delay; compaction enabled, reserve 16384, keepRecent 20000, background 32768; parallel tools; both queue modes one-at-a-time. An absent optional setting must remain distinguishable from false/zero, using pointer fields or a documented `ResolveSettings` helper. The independent fixture calls `Settings{Retry: RetryPolicy{Enabled:false}, Compaction: CompactionPolicy{Enabled:false}}` to explicitly disable both policies. For clarity, the native `Settings` returned by Options.Settings is the effective representation, with bool fields for those policy switches. Provide `DefaultSettings() Settings` and `ResolveSettings(json.RawMessage) (Settings, error)` for a source-wire partial patch: omitted keys inherit defaults while explicit false/zero retain their meaning. A nil Options.Settings selects defaults; a host getter returns effective settings (normally starting from DefaultSettings or ResolveSettings). Do not invent pointer Enabled fields that would make the frozen bool literals fail to compile. Resolve per-conversation raw patches separately and retain null-clearing semantics.

## Tool and hook interfaces

```go
type ToolRegistration struct {
    Declaration types.Tool
    Replay string // safe or unsafe; empty is unsafe
    ExecutionMode string // parallel/sequential; empty follows settings
    PrepareArguments func(json.RawMessage) (json.RawMessage, error)
    OutputLimits *OutputLimits
    Execute func(context.Context, json.RawMessage, ToolAPI) (ToolResult, error)
}
type OutputLimits struct { MaxBytes int; MaxLines int; Retain string }
type ToolDiagnostic struct { Severity string; Message string; Code string }
type ToolControl struct { AddTools []string; Terminate bool; Handoff string }
type ToolResult struct {
    Content []types.ContentBlock
    IsError bool
    Details json.RawMessage
    Diagnostics []ToolDiagnostic
    Usage *types.Usage
    Control *ToolControl
}
type HookAPI interface {
    durable.DocumentReader
    TaskID() durable.TaskID
    ConversationID() durable.ConversationID
    Memo(context.Context, string, json.RawMessage) (json.RawMessage, error)
}
type ConversationHandle interface {
    ID() durable.ConversationID
    Submit(context.Context, SubmissionDraft) (*Submission, error) // input only
    Abort(context.Context, AbortOptions) error
    WaitForIdle(context.Context) error
}
type ToolAPI interface {
    durable.DocumentReader
    durable.DocumentObserver
    TaskID() durable.TaskID
    ConversationID() durable.ConversationID
    CallID() string
    Registry() RegistrySnapshot
    Agent(context.Context) (Agent, error)
    Env() env.ExecutionEnv
    Output([]byte) error
    Diagnostic(ToolDiagnostic) error
    Details(context.Context, json.RawMessage) error
    Commit(context.Context, func(durable.Tx) error) error
    Memo(context.Context, string, json.RawMessage) (json.RawMessage, error)
    CreateTask(context.Context, durable.TaskDefinition, json.RawMessage, durable.TaskOptions) (durable.TaskID, error)
    GetTask(context.Context, durable.TaskID) (*durable.TaskRecord, error)
    WaitForTask(context.Context, durable.TaskID) (*durable.TaskRecord, error)
    Conversation(context.Context, durable.ConversationID) (ConversationHandle, error)
}
```

Tool declaration schemas, arguments, and results use existing Pith AI types. Preserve nil content (use retained output) versus explicit empty content (empty result), absent Details versus explicit JSON null, running output sanitization versus explicit result content, diagnostics order, and usage attribution. All tool operations, acquired watches, conversation handles, and returned submission methods are invocation-bound. Their admitted records survive; operations fail after the invocation ends. Output defaults are exactly pinned `truncate.ts`: 50 KiB, 2000 lines, head retention. Count Unicode bytes correctly, support split UTF-8 byte chunks, remove the exact source control-character ranges while retaining tabs/newlines, and persist head/tail retained output and dropped counters. Do not substitute a different ANSI parser or truncate explicit returned result content, which the pinned implementation preserves.

Hook input/output JSON schemas must follow each actual pinned named hook. A documented typed helper may wrap them: generation beforeRequest/afterResponse/onYield/afterTools, tool beforeTool/afterTool, compaction beforeCompact. Apply selected extensions in order. First block/continuation/compaction decision wins where the source does; other replacement chains compose. Report nonfatal handler errors, preserve cancellation propagation, and support durable first-writer-wins memos. A hook interrupted before its consuming commit may rerun. Arguments are repaired purely and validated before and after beforeTool; the final arguments and replay policy are committed before execution. Recovery after intent does not rerun beforeTool.

## Scheduler and recovery semantics

Use the actual five task states: pending, running, waiting, completing, terminal. A wait stores `On` task IDs and failFast/allSettled policy; completing holds a decided outcome until ordinary owned work has settled. No `After` field or open-time orphan sweep is introduced. On open, surviving running tasks become pending with checkpoint/memos/abort mark preserved; nothing dispatches. Missing, too-old, and migration-failed task definitions remain blocked and inspectable until replacement is available or explicit abort orphans them. Migration occurs at reservation, not during read-only inspection. Registry changes wake blocked work, and phase boundaries may adopt a compatible changed task definition. Never run two handlers for one task at once.

Each phase must commit a changed checkpoint, waiting state, or terminal outcome. No durable progress, an unavailable phase, an uncaught error, or invalid runtime contract faults the task. Durable checkpoints are full replacements, not merges. Task ownership and conversation ownership form the live-work tree; history parent edges do not establish ownership. Required ownership is conversation/task for tasks and ownerless/task for conversations. Child tasks share their owner conversation, cannot be background, and live children hold parent completion. Fail-fast joins abort remaining work on failure; all-settled joins retain outcome order. Ordinary idle and abort skip background ownership boundaries; background=true abort includes the reached background work at admission but not newly created unrelated work.

Abort commits the mark, signals and joins the active run, and dispatches a fresh abort handler after ordinary owned work drains. Run commits after the mark and any runtime operation after invocation end reject. Canceling a caller's context cancels that operation/wait, never durably aborts shared work unless the invoked operation is an abort API. Close seals admission/reservation, signals and joins callbacks/watches, lets already-admitted storage commits finish, and closes storage. It writes no abort mark or outcome. A canceled close caller does not cancel the shared shutdown; a later close joins it. Old and new owners cannot use the same store simultaneously.

Effects use intent/effect/outcome. Reopen in an intent phase means an external effect may have happened. A tool reruns only when BOTH stored and current selected declaration say safe. Stored unsafe cannot be upgraded by current safe; current unsafe or deselection vetoes stored safe. Otherwise produce an interrupted error result with committed partial output/details/diagnostics and continue or fail the generation as in source. A safe operation needs an external idempotency key or equivalent recovery protocol to make its effect exactly once; repeated invocation is expected. Do not claim arbitrary external side effects are exactly once. Persist provider request model/thinking/options/cutoff before sending; recovery sends the same request without repeating preparation. Committed interrupted assistant partials remain raw aborted entries, excluded from future model context. Deferred handles/poll deadlines and retry backoffs survive reopen; do not resend an already-deferred provider request. Abort cancels deferred responses through the current provider adapter.

## Admission, configuration, context, and compaction

RequestID dedup is conversation-scoped and happens before every write; a key already used for the other submission type rejects. Submit returns at durable admission, not settlement. Busy reject writes nothing. Busy inputs and writes queue in positional ID order. Follow-up is selected only at final boundary; steering at tools/final; writes are passive and never start a run. Respect both queue modes, ordered boundaries, stale heads, reset/handoff behavior, and queued-input withdrawal; abort preserves writes. Wait cancellation leaves submission pending. Reacquire handles after reopen and retain terminal receipts.

Root has reserved ID1, is created lazily and seeded atomically, and root options do not overwrite reopen state. Raw Tx conversation creation also invokes the creation hook. Built-in `pi.agent` is rewindable/asOf; task-owned new conversations copy their owner's stored agent, and forks obey document policies at their visible cutoff. Registry install replaces the same extension in place; uninstall/reinstall appends; snapshots remain immutable. Tool/section duplicate names across extensions compose in selection order. Wrappers are pure; a throwing or renaming wrapper drops its target and reports. Store only agent choice names. Current code changes do not rewrite old prompt history.

Context preserves raw head marker and active entries, newest edits per target, contribution alignment, positional system messages, tool-result call order, missing-result synthesis, and exclusion of aborted/error/deferred assistant messages. A reset or compaction changes active model context using immutable head entries, not deletion of history. Prompt rendering retains ordered sections, tag/wrapper behavior, minimal delta patches, remove/re-add for order-only changes, and head-cut baseline rebasing through omissions of retained old system deltas. Fork configuration/doc state is inherited at selected history cutoff; ownership stays independently selected.

Compaction is an ordinary durable task: manual/background summary placement uses write submission; blocking threshold/overflow compaction is generation-owned. Persist selected range/model/options before summary; close/reopen resumes summary/backoff correctly. Avoid splitting assistant/tool-result pairs, preserve recent context, validate first-kept/cutoff against resets, run hooks, account summary usage, clear live status, and do not perform compaction repeatedly forever after a failed/no-op overflow attempt. Tests and docs must preserve all pinned manual, threshold, background, overflow, decline, retry, and stale-summary behaviors.

## Observation, usage, and execution

Expose `ConversationView{Conversation durable.ConversationRecord; Entries []durable.EntryRecord; Docs map[string]durable.JsonObject}` mounting exactly `pi.agent`, `pi.live`, `pi.inbox`, `pi.usage`. `ConversationWatch` mirrors the root DocumentWatch shape: `Value() ConversationView`, `Start(func(context.Context,ConversationView,[]chord.Op) error) error`, `Stop() error`, `Closed() <-chan durable.WatchEnd`. Acquisition is atomic with listener registration; complete commits produce complete frames; observers never see uncommitted state. Bounded pending frames compact to a complete reset retaining the latest committed view. Native read-only replicated state adapters mirror upstream viewState and taskGraph lifetimes. Task graph includes owners, live state, and owned conversations, and updates from the same commit source.

Native `WatchEvents(ctx,harness,conversationID)` exposes the source named agent event protocol with initial snapshot, run/message/tool/submission/inbox/retry/deferred/compaction/configuration/usage events and reset snapshot on the source 100-batch overflow. Events derive from uncoalesced committed publication before structural watch compaction, and are isolated by conversation. No event-journal durability or old pre-attachment replay is promised. Do not silently substitute read-only structural watches for lifecycle events. Read-only inspection, views, graph, usage, context, registry queries do not enable scheduling. Source progress APIs enable it; Resume is idempotent and rejected after close.

The concrete observation signatures used by independent judges are:

```go
type TaskGraph struct { Tasks map[string]TaskGraphNode }
type TaskGraphNode struct {
    ID durable.TaskID; Kind string; ConversationID durable.ConversationID
    Owner *durable.TaskID; Background bool; AbortRequested bool
    State TaskGraphTaskState; Conversations []durable.ConversationID
}
type TaskGraphTaskState struct { Status string; Phase string; On []durable.TaskID; Policy string; Outcome string }
// Both read-only states implement Value() and Dispose() error, and both watches
// implement Value(), Start(ctx,value,ops callback), Stop(), Closed() as above.
type AgentEvent struct {
    Type string
    Payload json.RawMessage
}
// MarshalJSON/UnmarshalJSON merge Type and Payload into the EXACT source wire
// shape, with payload fields at the top level. Typed event helpers are encouraged.
type AgentEventStream interface {
    Snapshot() AgentEvent
    Start(func(context.Context, []AgentEvent) error) error
    Stop() error
    Closed() <-chan durable.WatchEnd
}
func WatchEvents(context.Context, *Harness, durable.ConversationID) (AgentEventStream, error)
type TaskInspectionState struct { Kind string; Reason string; On []durable.TaskID; Migrates bool; Err error }
type TaskInspection struct { Record durable.TaskRecord; State TaskInspectionState }
type Inspection struct { Scheduling string; Tasks []TaskInspection; Submissions []durable.SubmissionRecord }
```

Provide `UsageState{Models map[string]types.Usage; Tools map[string]types.Usage}` with wire keys `models` and `tools`, as pinned source: assistant and compaction usage share provider/modelId buckets; tool execution usage uses tool-name buckets. Preserve every optional counter and monetary field. Native AI adapter must call existing Pith provider stream/deferred APIs, forwarding cancellation/thinking/curated options, validate model availability and context-window metadata, and report unsupported deferred providers as durable errors. Offline scripted ModelRunner is a test seam; it is not the sole production backend. Independent delivery judges exercise a real provider against a local HTTP fixture without keys or external network.

Export native `AgentDoc`, `InboxDoc`, `LiveDoc`, and `UsageDoc` definitions as `durable.DocumentDefinition`, and the built-in `GenerationTask`, `ToolTask`, and `CompactionTask` as `durable.TaskDefinition`, together with their input/checkpoint/result JSON shapes and typed convenience structs. Keep source `DefineTool`, `DefineExtension`, `Hook`, `Section`, `WrapSection`, `WrapTool`, `Configure`, `ResolveSettings`, `DefineTask`, and entry token counterparts available as Go constructors/helpers with no loss of behavior. These helpers must not serialize executable callbacks.

## Acceptance and limitations

Independent harness judges cover task progress/faults, close and resumption, memo idempotency, blocked definitions and migration, caller wait cancellation, deduplicated submissions, queue and abort rules, context history/forks, model retry/deferred recovery, tool replay matrix and intent hooks, compaction, prompt/registry changes, live progress, usage, structural frames, graph and agent events. File-backed killed-process judges use file/stdin/channel barriers at durable effect boundaries; no sleep establishes correctness. At least one subprocess is killed after an acknowledged intent plus external effect and before its outcome, then reopened: safe reruns deduplicate via an external key and unsafe runs are never repeated. Prove normal compile/test/vet and cross-platform compile with CGO_ENABLED=0; run race checks separately where supported. Translate the pinned upstream suite behavior in product tests in addition to these independently authored judges. Passing the finite judges does not prove full parity.


# Durable coding tools

Port pinned `packages/durable/src/tools/**` and `src/truncate.ts`, preserving
behavior as native Go tools using the Durable harness ToolAPI and execution
environment. Reuse existing Pith validation/diff facilities where appropriate;
do not import or adapt the old coding-agent session engine as Durable itself.

Package `packages/durable/tools` exports:

```go
func CreateReadTool() harness.ToolRegistration
func CreateWriteTool() harness.ToolRegistration
func CreateEditTool() harness.ToolRegistration
type BashExecution struct {
    Command, CWD string
    Env map[string]string
    InheritEnv bool
}
type BashToolOptions struct {
    CommandPrefix string
    Prepare func(context.Context, *BashExecution, harness.ToolAPI) error
}
func CreateBashTool(options ...BashToolOptions) harness.ToolRegistration
func CodingTools() harness.Extension
```

CodingTools returns extension `coding-tools` with read/write/edit/bash. It is
explicitly installed by the integrator; never silently install host tools in all
harnesses. The source declarations default to unsafe replay; preserve that
default, even for read, unless an application explicitly opts in with its own
registration. A function called `read` is not evidence it is replay-safe.

Implement ALL source-defined tool behavior:

- read: resolve tilde/relative/absolute paths through Env; 1-based offset and
  optional positive limit; typed missing/directory errors; detect image signature
  and emit unsupported-image diagnostics (this release does not synthesize image
  reading); retain text separate from diagnostics, head limits of 2000 lines and
  50 KiB; UTF-8 boundary-safe oversized-first-line trimming; continuation details.
- write: create parent directories; write exactly requested bytes; serialize
  mutations by environment namespace ID and canonical path, not path spelling;
  cancel before/after relevant operations and release queue ownership on error.
- edit: repair legacy top-level oldText/newText and edits encoded as a JSON
  string or single object without modifying original arguments; nonempty edits;
  match every edit against ORIGINAL content, exact unique nonoverlapping match,
  reject duplicates/overlap/missing matches with no partial write; preserve BOM
  and original CRLF; return diff, unified patch, and firstChangedLine details.
- bash: no default timeout; validate finite positive seconds and the source
  maximum; prefix/preparation callback; Env.Exec with 2000-line/50-KiB spilling;
  forward all raw chunks to ToolAPI.Output, keep tail through harness limits;
  emit full_output diagnostic for spill file even on timeout/abort/nonzero exit;
  nonzero exit is an error. Preserve cancellation rather than converting it to
  successful empty output. Pre-cancelled tools must not execute.

Keep output truncation, overlap merging, streaming progress throttling and
diagnostics faithful to the source. The existing `diff` npm package becomes the
already-frozen `github.com/sergi/go-diff` Go dependency, or native logic where
needed for source-equivalent patches. TypeBox schemas become the existing Pith
JSON-schema validator. No JavaScript runtime or CGO dependency is introduced.

Independent tests use a real Local environment and an injected ToolAPI, then
exercise end-to-end registration through the actual harness in the final stage.
Candidate self-tests must cover mutation-queue cancellation, symlink aliases,
output bounds and every source tool test not represented in the independent
smoke tests. Do not claim that builtin tools alone enforce filesystem permission
or exactly-once external side effects.


# Durable SDK delivery and conformance helpers

Deliver the full optional native Durable SDK at the reviewed Pi v1.0.0 commit.
Existing coding-agent users continue to work without enabling Durable. Do not
replace the existing session engine or silently change its file format.

## Testing exports

Port all six `src/testing` modules. The native facade is runner-independent:

```go
// package packages/durable/testing (import alias durabletesting)
type StorageProvider func(context.Context, func(durable.Storage) error) error
type StorageConformanceCase struct {
    Name string
    Run func(context.Context) error
}
func CreateStorageConformance(StorageProvider) []StorageConformanceCase
func RegisterStorageConformance(*testing.T, string, StorageProvider)
```

Every upstream `createCase(options, name, ...)` becomes a native case with the
same name and actual storage assertions; return descriptive errors. The provider
must call and await the callback once per case, with a fresh adapter. Register
the cases as Go subtests. Implement the source assertions' strict/deep/partial
equality, rejection and greater-than semantics as native helpers, not a fake
Jest runner. Preserve all benchmark scale definitions, primary record counts,
seeding algorithms, read/write benchmark case definitions and callback behavior
as idiomatic exported Go values/functions. Benchmarks are optional for normal
applications and must not run automatically. They are not performance claims.

## Native provider integration

`harness.NewPithModelRunner(resolve func(context.Context, harness.ModelRef)
(*types.Model, error), options *types.SimpleStreamOptions) harness.ModelRunner`
must use the real Pith provider implementation, propagate contexts/options,
stream updates, terminal error/abort status, usage and deferred handles.
An injected ModelRunner is for host integration and deterministic tests; do not
deliver a harness which can only run mock models. The independent delivery gate
uses a local OpenAI-compatible SSE endpoint through this production adapter.

## Documents and example

Add `docs/sdk/durable.md` with a minimal Open/Root/Submit/Wait/Close embedding
example, all adapter choices, task/extension registration, cancellation, watches,
ownership versus conversation history, CGO-disabled build commands, format
compatibility decisions, single-writer assumptions and crash guarantees.
Link it from `docs/sdk/README.md` and update `docs/sdk/compatibility.md` precisely.
Add `examples/durable/main.go` and its own tests, runnable offline via an explicit
flag and usable with a host-supplied Pith model/provider configuration. Offline
mode must be clearly labelled; never fabricate real model usage or costs.
Add `docs/third-party-notices.md` entries for Durable/Chord and pure Go SQLite
dependencies without changing upstream or dependency licences.

The immutable `docs/sdk/durable-source-map.json` asset records reviewed source
ownership. Preserve Go idioms and map semantic owners, including files merged
into a package. Do not imply a line-for-line or binary-compatible TS API.

Durable's pinned experimental guarantees are deliberately limited: a committed
request ID deduplicates submissions; checkpointed tasks resume; already-terminal
work is not re-executed; tools replay only when BOTH stored and current policy
say safe; unsafe interrupted intent becomes an interrupted error. External
effects can happen before a crash, so this is not a generic exactly-once claim.
Close/suspend must not fabricate successful completion. Include an explicit
crash/restart example and recovery test instructions. No live credentials are
needed for independent acceptance.

Backend conformance, source-defined behavior, process-kill recovery, historical
judges and whole-project tests must all pass before the module is committed.
Compilation of preparation facades and successful plan preflight do not satisfy
those implementation gates. The preparation adds no implementation code.
