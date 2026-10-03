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
