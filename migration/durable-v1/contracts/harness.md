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
