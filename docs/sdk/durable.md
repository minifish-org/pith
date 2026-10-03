# Pith Durable SDK

`github.com/minifish-org/pith/packages/durable` and its subpackages are the
optional native Durable SDK. It is an additive Go port of Pi Durable at revision
`a13d35a742c6ef8462812a28fbe1d8c8b7431c32`. Existing coding-agent users are
unaffected: enabling Durable does not replace the coding-agent session engine or
change its on-disk session format.

The SDK is headless and CGO-free. There is no Node, npm, TypeScript/JavaScript
engine, external database server or live credential requirement in the delivered
core runtime.

## Packages

| Package | Responsibility |
| --- | --- |
| `packages/durable` | Shared records, validation, `Storage`/`Tx`/`TaskRuntime`, `Session`, documents and watches. |
| `packages/durable/harness` | Scheduling, model/tool runtime, registry, prompts, compaction, tasks, observation. |
| `packages/durable/env` | Portable file/shell capability (`ExecutionEnv`) with an ordinary local adapter. |
| `packages/durable/tools` | Native read/write/edit/bash coding tools and the `coding-tools` extension. |
| `packages/durable/storage/memory` | Complete detached in-memory adapter. |
| `packages/durable/storage/jsonl` | Local JSONL adapter with recovery, poisoning and reclamation. |
| `packages/durable/storage/sqlite` | Real SQLite adapter (`modernc.org/sqlite v1.44.3`). |
| `packages/durable/testing` | Runner-independent storage conformance suite and benchmark builders. |
| `packages/chord` | Strict JSON, decoded delta operations, transactional tracking and source-attached state. |

The root `packages/durable` package never imports a storage adapter.

## Minimal embedding example

The same lifecycle is in `examples/durable/main.go` (offline by default).

```go
package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/minifish-org/pith/packages/ai/types"
	"github.com/minifish-org/pith/packages/durable"
	"github.com/minifish-org/pith/packages/durable/env"
	h "github.com/minifish-org/pith/packages/durable/harness"
	"github.com/minifish-org/pith/packages/durable/storage/sqlite"
	"github.com/minifish-org/pith/packages/durable/tools"
)

func main() {
	ctx := context.Background()

	// 1. Choose an adapter. The root package never imports one.
	storage, err := sqlite.Open(ctx, "state.sqlite", sqlite.Options{})
	if err != nil {
		panic(err)
	}
	defer storage.Close(context.Background())

	// 2. Supply a model runner. NewPithModelRunner wraps the real Pith
	//    providers; an injected runner is for tests and host integration.
	key := "sk-..." // supply from your host configuration
	runner := h.NewPithModelRunner(func(ctx context.Context, ref h.ModelRef) (*types.Model, error) {
		return &types.Model{
			Id: "gpt-4o-mini", Name: "gpt-4o-mini",
			Api: types.ApiOpenAICompletions, Provider: "openai",
			BaseUrl: "https://api.openai.com/v1",
			ContextWindow: 128000, MaxTokens: 16384,
			Input: []types.ModelInputModality{types.ModelInputText},
		}, nil
	}, &types.SimpleStreamOptions{StreamOptions: types.StreamOptions{
		ProviderRequestOptions: types.ProviderRequestOptions{APIKey: &key},
	}})

	// 3. Build a registry and explicitly install extensions.
	registry := h.CreateRegistry()
	if err := registry.Install(tools.CodingTools()); err != nil {
		panic(err)
	}

	// 4. Open the harness.
	harness, err := h.Open(ctx, storage, h.Options{
		Models:   runner,
		Registry: registry,
		Env: func(ctx context.Context, target h.EnvTarget) (env.ExecutionEnv, error) {
			return env.NewLocal(target.CWD)
		},
	})
	if err != nil {
		panic(err)
	}
	defer harness.Close(context.Background())

	// 5. Root creates the reserved conversation lazily and seeds it atomically.
	conversation, err := harness.Root(ctx, h.RootOptions{
		Agent: json.RawMessage(`{"model":{"provider":"openai","modelId":"gpt-4o-mini"}}`),
	})
	if err != nil {
		panic(err)
	}

	// 6. Submit returns at durable admission, not settlement.
	submission, err := conversation.Submit(ctx, h.SubmissionDraft{
		Type:      "input",
		Content:   types.UserContentText("hello"),
		RequestID: "one-shot-request",
	})
	if err != nil {
		panic(err)
	}

	// 7. Wait blocks until the submission settles.
	record, err := submission.Wait(ctx)
	if err != nil {
		panic(err)
	}
	fmt.Println("status:", record.Status)
}
```

## Storage adapter choices

| Adapter | Constructor | Use it when |
| --- | --- | --- |
| Memory | `memory.New()` | Tests, ephemeral agents, no restart durability. |
| JSONL | `jsonl.Open(ctx, directory, jsonl.Options{})` | Human-inspectable local transcripts; single-writer local use. |
| SQLite | `sqlite.Open(ctx, path, sqlite.Options{})` | The default durable local store with real transactions. |

- `jsonl.Options{Fsync: true}` syncs sidecars before the main marker and the
  marker before destructive reclamation. The default promises ordinary
  process-crash consistency, not power/host/kernel failure durability.
- `sqlite.Options{WALAutoCheckpointPages, BusyTimeoutMS}` override the upstream
  defaults (1000 pages, 5000 ms). The adapter uses WAL and `synchronous=NORMAL`.

Adapters import the shared records; the root package imports no adapter. A
storage adapter owns atomicity, one global ID namespace, detached ownership and
document consistency. It must not acquire a second Session-facing commit lock or
call user definitions.

## Task and extension registration

A registry holds exactly the pinned built-in tasks (`GenerationTask`,
`ToolTask`, `CompactionTask`) plus explicitly installed extensions.

```go
registry := h.CreateRegistry()
err := registry.Install(h.Extension{
	Name: "my-tools",
	Tools: []h.ToolRegistration{
		{
			Declaration: types.Tool{Name: "greet", /* ... */},
			Replay:      "unsafe", // default; opt in to "safe" only when idempotent
			Execute: func(ctx context.Context, args json.RawMessage, api h.ToolAPI) (h.ToolResult, error) {
				return h.ToolResult{Content: []types.ContentBlock{types.TextBlock("hi")}}, nil
			},
		},
	},
	Sections: []h.PromptSection{
		{Key: "greeting", Render: func(ctx context.Context, in h.PromptInput) (string, error) {
			return "Be concise.", nil
		}},
	},
	Tasks: []durable.TaskDefinition{myTask},
})
```

`CodingTools()` returns extension `coding-tools`. It is never installed
implicitly: an integrator installs it. The built-in tool declarations default to
unsafe replay, including `read`; a function called `read` is not evidence it is
replay-safe. Install replaces the same extension in place; uninstall/reinstall
appends. Snapshots are immutable, so a running decision never observes a partial
registry update.

Task definitions contain only scheduling-neutral callbacks. The harness exposes
its registry, models, agent, environment, hooks and invocation-owned conversation
handles.

## Cancellation

Every fallible operation takes `context.Context` first. Standard contexts define
cancellation lifetime.

- Cancellation before callback admission rejects the operation with no effects.
- Once durable settlement begins the commit is completed with
  `context.WithoutCancel`: an acknowledged commit stays committed even if the
  caller cancels.
- Canceling a caller's context cancels that operation or wait. It never durably
  aborts shared work unless the invoked operation is an abort API.
- A rejected (`StorageRejected`) commit allows later commits. An uncertain
  storage or adoption failure poisons the Session until reopen.

## Watches and observation

- `Conversation.Watch(ctx)` returns a `*ConversationWatch` with
  `Value() ConversationView`, `Start(callback)`, `Stop()` and `Closed()`.
- `Session.WatchDoc` / `DocumentWatch` and `Harness.WatchTaskGraph` /
  `TaskGraphWatch` follow the same shape.
- Acquisition is atomic with listener registration: a watch captures committed
  state and registers for later commits in one step and never observes
  uncommitted state.
- Callbacks serialize on a delivery line and never run inline in `Start` or a
  Session callback. A bounded pending frame buffer compacts to a complete reset
  rather than seeing partial state.
- Retirement delivers null/reset and binds the handle to the original
  incarnation; recreation does not revive it. `Stop` is idempotent; a listener
  error closes and reports only that observer.
- `Harness.WatchEvents(ctx, harness, conversationID)` exposes the source named
  agent event protocol. Events derive from uncoalesced committed publication,
  before structural watch compaction, and are isolated by conversation. No
  event-journal durability or replay of pre-attachment history is promised.

Read-only inspection, views, graph, usage, context and registry queries do not
enable scheduling. Source progress APIs do; `Resume` is idempotent and rejected
after close.

## Ownership versus conversation history

Two independent trees coexist:

- **Ownership** is the live-work tree. A task's `Owner` is a task ID for a child
  task and absent for a conversation-owned task. A conversation's `Owner` is the
  task that created it. Ownership decides attribution, subtree abort and
  subtree idle waits.
- **History** is the immutable transcript ancestry: `ConversationRecord.Parent`
  names a fork source and an inclusive cut entry. A child never sees parent
  entries appended after its cut, even through a grandchild.

The live graph is: `pending`, `running`, `waiting`, `completing`, `terminal`.
A waiting task records `On []TaskID` plus `failFast`/`allSettled`. Completing
holds a decided outcome until ordinary owned work settles. There is no
dependency-only `After` field and no open-time orphan sweep. Conversation
creation for a task copies its owner's stored agent, and forks obey document
policies at their visible cutoff.

## Single-writer and crash guarantees

- One process owns a store at a time. Old and new owners must not use the same
  store simultaneously.
- A storage commit is all-or-nothing across tables and document writes.
  Sequences increase strictly, with allowed gaps.
- Documents have half-open lifetimes `[CreatedAt, RetiredAt)`; creation and
  retirement in one batch has an empty lifetime. A base bounds replay but never
  authorizes history reclamation for rewindable documents.
- Task documents are retired atomically at terminal settlement.

Recovery behavior after a process crash:

- On reopen, surviving `running` tasks become `pending` with checkpoint, memos
  and abort mark preserved; nothing dispatches until a progress API runs.
- A task interrupted in an intent phase means an external effect may already
  have happened. A tool reruns only when BOTH the stored and the current
  selected declaration say `safe`. Stored `unsafe` cannot be upgraded by current
  `safe`; current `unsafe` or deselection vetoes stored `safe`. Otherwise the
  interrupted intent becomes an interrupted error result with committed partial
  output, and the run continues or fails per source.
- A safe operation needs an external idempotency key or equivalent recovery
  protocol to make its effect exactly once. Repeated invocation is expected.
  **Arbitrary external effects are not exactly once.** This SDK does not claim a
  generic exactly-once guarantee.
- A committed request ID deduplicates submissions conversation-scoped; already
  terminal work is not re-executed.
- `Close`/suspend never fabricates successful completion. `Close` writes no abort
  mark or outcome.

JSONL recovery truncates incomplete final lines at byte boundaries (including
torn UTF-8), removes unconfirmed sidecar tails, replays only confirmed markers
and checks strict sequence/ordinal/reference consistency. Missing confirmed data,
malformed complete lines, unsupported format, conflicting/duplicate committed
records and invalid complete UTF-8 are corruption and are never silently
repaired by resetting state. An uncertain append/sync failure poisons the open
backend; reopen decides the confirmed durable state.

## Documents

Definitions are supplied explicitly at each `Tx.Doc` call, never registered
globally. Only `Tx.Doc` creates a document; Snapshot/state/watch/as-of reads
never create. Current-only session/task/latest conversation documents reject
historical content reads; rewindable documents retain all required
bases/deltas, including after retirement. A definition-free document copy
preserves the stored version; the source may not change in the copy batch.

Definitions are not serialized with executable callbacks. `DocumentDraft.Value`
returns a private working copy; preparation detaches it, and adoption is the
commit boundary.

## Testing and conformance

`packages/durable/testing` is runner-independent:

```go
provider := func(ctx context.Context, use func(durable.Storage) error) error {
	storage, err := sqlite.Open(ctx, filepath.Join(dir, "state.sqlite"), sqlite.Options{})
	if err != nil {
		return err
	}
	defer storage.Close(context.Background())
	return use(storage)
}
// Registers every source conformance case as Go subtests.
durabletesting.RegisterStorageConformance(t, "sqlite", provider)

// Or drive the cases yourself:
for _, c := range durabletesting.CreateStorageConformance(provider) {
	if err := c.Run(ctx); err != nil {
		t.Fatal(c.Name, err)
	}
}
```

The provider opens a fresh adapter exactly once per case. The 23 case names match
`packages/durable/src/testing/storage-conformance.ts`. `storage_benchmark.go`
exports the scale definitions, primary record counts, seeding functions and
read/write benchmark definitions; benchmarks are optional and never run
automatically.

`harness.NewPithModelRunner` uses the real Pith provider implementation and
propagates contexts, options, streaming updates, terminal error/abort status,
usage and deferred handles. An injected `ModelRunner` is for host integration
and deterministic tests; it is not the only backend.

## Crash/restart example and recovery test instructions

- Offline embedding example: `go run ./examples/durable`. It is labelled
  `OFFLINE`; the scripted reply is not real model usage and has no cost. Pass
  `-offline=false` with `-provider`, `-model`, `-base-url` and `-api-key-env` to
  use the production adapter.
- Crash/restart recovery: `go run ./examples/durable-recovery`. It re-execs
  itself as a child, lets a tool start and acknowledge an external effect, kills
  the child before an outcome is written, then reopens the store and finishes.
  `-replay=safe` deduplicates the effect through an external idempotency key;
  `-replay=unsafe` returns the interrupted receipt unchanged.
- Run the recovery test suite with:

  ```
  CGO_ENABLED=0 go test -mod=readonly -timeout=0 ./packages/durable/... ./examples/durable/...
  ```

Use `-race` with `CGO_ENABLED=1` only for the race detector; the product must
have no `CgoFiles`.

## Build commands

```sh
# Normal build and tests (CGO disabled).
CGO_ENABLED=0 go build -mod=readonly ./...
CGO_ENABLED=0 go test -mod=readonly -timeout=0 ./...

# Cross-platform release compile.
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -mod=readonly ./...
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -mod=readonly ./...
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -mod=readonly ./...
```

Do not edit dependency manifests or run `go mod tidy`. SQLite is
`modernc.org/sqlite v1.44.3` with `modernc.org/libc v1.67.6`, already pinned.

## Format compatibility

- This is a native Go API, not a line-for-line or binary-compatible TypeScript
  API. See `compatibility.md` and the immutable `durable-source-map.json`.
- Records serialize with the upstream lower-camel field names. JSON payloads
  retain unknown fields. Allocated IDs and commit sequences are positive safe
  integers at most `9007199254740991`; root ID `1` is reserved.
- Document operations persist the exact decoded Pi tuple vocabulary (`r`, `s`,
  `d`, `a`, `t`, `p`, `m`). `t` and `Overlap` count UTF-16 code units.
- Go cannot represent isolated UTF-16 surrogates as valid UTF-8 strings, so a
  truncation that would bisect a supplementary character is rejected rather
  than corrupting persisted data.
- Go cannot revoke an escaped map reference as a JavaScript `Proxy` can. The
  documented adaptation is: change handles are invalidated, candidate data is
  detached during preparation, and value getters return independent copies, so
  retained draft maps cannot change committed revisions.

## Experimental guarantees

Durable's pinned guarantees are deliberately limited:

1. A committed request ID deduplicates submissions (conversation-scoped).
2. Checkpointed tasks resume.
3. Already-terminal work is not re-executed.
4. Tools replay only when BOTH stored and current policy say `safe`.
5. An unsafe interrupted intent becomes an interrupted error.

External effects can happen before a crash, so none of this is a generic
exactly-once claim.
