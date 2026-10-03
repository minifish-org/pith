# Preparation validation — 2026-10-03

This records preparation, not a completed Durable implementation. No paid model
calls, candidate generation or migration execution occurred during preparation.

Source: Pi v1.0.0 `a13d35a742c6ef8462812a28fbe1d8c8b7431c32`.
Product baseline: Pith `c4515fb25561098b95ca7cead246a336144b1e9b`.
Executor: Portsmith-Go with the current pinned Pith backend.

Completed:

1. Analyzed the complete Pi snapshot, then selected and reindexed the Durable
   and required Chord scope. All **60 Durable runtime files / 17,662 lines** have
   owners; all **83 Durable test/example files** are preserved as behavior
   references. Chord's reviewed closure is 13 files / 4,886 lines. Scope-filtered
   import cycles are retained. `review/source-check.json` verifies 406 relevant
   cached files/configs against exact Git tree object IDs, with zero mismatches.
2. Prepared **11 automatic sequential steps** with **80 globally unique
   independent test functions**, including three subprocess helper entrypoints.
   Frozen contracts, source hashes, candidate self-test outputs, semantic mapping,
   separate journal/runs, cumulative gates and module integration are complete.
   `review/preflight.log` reports ready, canStart=true, preparedSteps=11,
   verifiedSteps=0, and no blocked batches. All stages are prepared before start.
3. Compiled all final judges together against temporary no-op native API facades,
   actual existing AI types/utils and frozen dependency manifests with CGO
   disabled. `review/final-judge-compile.log` passed. This verifies syntax and
   interface consistency only: no future runtime behavior was accepted.
4. Checked meaningful negative controls: incorrect Chord ownership/JSON/delta
   behavior, broken memory ID allocation, empty environment/tool/conformance
   facades, and the existing alphabetical prompt-section baseline are rejected.
   Existing Chord context passed its two new context judges unchanged. Detailed
   evidence is in `review/*compile.log` and `review/prompt-order-negative.json`.
5. Pinned mature pure-Go SQLite `modernc.org/sqlite v1.44.3`, required Go 1.24.0,
   matching libc v1.67.6 and exact transitive checksums. A **real dependency probe**
   created/read a SQLite database with CGO disabled, then cross-built for Darwin
   arm64/amd64, Linux amd64/arm64 and Windows amd64. Source, manifests and results
   are in `review/sqlite-probe` and `review/sqlite-dependency-check.json`. These
   are SQLite-driver feasibility checks, not future Durable adapter acceptance.
6. Ran the existing complete Pith suite after dependency preparation:
   `CGO_ENABLED=0 GOMAXPROCS=4 go test -p=4 ./...`: passed.
   `review/baseline-tests.log` tests the existing implementation. Historical
   migration plans, source fixtures, judges and accepted receipts are unchanged.

The independent recovery gates are ready to run against generated code. They
actually terminate subprocesses after explicit persistence/effect barriers,
restart JSONL/SQLite stores, and test durable request dedup, safe/unsafe replay,
terminal work, memos, changed registration policy, deferred/generation state and
retry deadlines. Their readiness must not be described as passed recovery.

Still required during operator execution: actual Go implementation, all candidate
self-tests, every cumulative independent judge including the race gate, final
whole-project integration, and the resulting module commit. Windows driver
cross-compilation does not certify Windows shell/runtime behavior. Live-model
validation, performance claims and production hardening are separate activities.

Preparation changes only dependency manifests and `migration/durable-v1`. Six
existing product/document files are declared writable for the future migration:
three AI prompt-order bridge files and three SDK documentation files. Existing
AI/provider/core/tool APIs and all other current product files are frozen.
After acceptance, the reviewed new source mappings must be merged into the
existing sync configuration without dropping its prior owners.
