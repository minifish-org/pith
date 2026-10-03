# Pi Durable v1 → Pith

This is a fully prepared **additive migration**, not a completed port. No
implementation model has run during preparation.

Source: implemented Pi v1.0.0 Durable SDK at
`a13d35a742c6ef8462812a28fbe1d8c8b7431c32`.
Baseline Pith: `c4515fb25561098b95ca7cead246a336144b1e9b`.
Executor: Portsmith-Go with its pinned Pith backend; implementation model:
DeepSeek Flash. Later Pico5 redesign snapshots are a separate migration.

## Run once

From the existing prepared workspace:

```bash
cd ~/work/pith
env PORTSMITH_MODEL=deepseek-flash \
    PORTSMITH_BASE_URL=https://api.deepseek.com/v1 \
    CGO_ENABLED=0 GOMAXPROCS=4 \
    ../portsmith-go/bin/portsmith migrate \
      --plan migration/durable-v1 \
      --commit --allow-download --env-file ../omni-pi/.env
```

The env file supplies the key using the existing Portsmith/compatible-provider
configuration. Do not paste keys in prompts or commit them. `--allow-download`
allows Go to fetch **already pinned and checksum-frozen** dependencies; it does
not allow the implementation model to add libraries.

The executor uses normal native Pith coding tools and persistent sessions. It
advances through every stage automatically, saving accepted checkpoints and
running cumulative checks. There are no artificial repair/turn/runtime caps.
Press Ctrl-C to stop; rerun the **same command** to continue. Auth/config errors,
changed frozen inputs and failed correctness gates still stop safely. Do not
edit a live candidate or frozen materials in another process.

One Durable module is committed after complete integration, rather than creating
11 partially usable product commits. A passed intermediate stage is saved in
`.portsmith/durable-v1/runs`; the formal SDK is published by the final module
transaction. The executor does not push the resulting Git commit.

Check preparation without calling a model:

```bash
../portsmith-go/bin/portsmith migrate --plan migration/durable-v1 --check
```

`ready`, 11 prepared steps, and 0 verified steps are the expected initial state.
A ready plan means all execution materials exist; it is not a parity claim.

## Ordered stages

| Stage | Scope | Independent test functions |
| --- | --- | ---: |
| `chord` | Required Chord JSON/delta/tracker/attached-state closure | 14 |\n| `foundation` | Records, definitions and atomic memory adapter | 6 |\n| `session` | Transactions, document history, forks and watches | 7 |\n| `env` | Local filesystem/shell capabilities and output spill | 2 |\n| `jsonl` | Persistent JSONL publication, corruption and crash recovery | 11 |\n| `sqlite` | Pure-Go SQLite storage, migrations and actual tables | 9 |\n| `prompt-order` | Narrow AI system-section ordering compatibility bridge | 3 |\n| `harness` | Complete cohesive scheduler/conversation/model/tool runtime | 21 |\n| `tools` | Explicit read/write/edit/bash extension | 2 |\n| `recovery` | Killed-process safe/unsafe replay and restart guarantees | 3 |\n| `delivery` | Storage conformance helpers, real Pith provider wiring, docs/examples | 2 |\n
There are 80 test functions, including three subprocess helper entrypoints.
Child helpers are exercised by parent kill/restart tests. All 60 Durable runtime
TS files (17,662 lines), the required Chord closure and 83 upstream test/example
files are inventoried; source tests must also become candidate-owned Go tests.
Independent samples do not replace the complete behavior requirement.

The harness is one cohesive stage because its builtins, scheduler, registry,
conversation and generation modules form a cycle. Splitting them into artificial
compile-only stages would require placeholder runtime implementations.

## Read/review

- `plan.json` / `workflow.json`: source, output ownership, order and frozen baseline.
- `scope.json`: every selected source file, hash, role and explicit deferred scope.
- `contracts/*.md`: native APIs and source-defined behavior.
- `judges/**`: immutable independent Go acceptance tests.
- `assets/durable-source-map.json` / `new-mappings.json`: reviewed semantic owners.
- `VALIDATION.md` / `review/`: preparation evidence and its limits.

Existing Chord context and Pith AI/provider code are reused. Six existing files
are writable: three AI ordering files and three SDK documentation files. All
other product files and historical judges/receipts stay frozen. Native Go APIs
are preferred; JavaScript branded/generic convenience APIs and callback host
integration are documented in native terms. Chord facets/remote-services used by
optional guide examples are outside the Durable runtime closure.

## Build/recovery contract

Memory, JSONL and SQLite must implement the same detached, atomic storage
semantics. SQLite uses `modernc.org/sqlite v1.44.3` / `modernc.org/libc v1.67.6`.
Normal test/build gates have `CGO_ENABLED=0`. The separate Go race gate enables
CGO only for instrumentation; the delivered packages must contain no CgoFiles.
The local migration requires a supported race toolchain. POSIX shell judges run
on the prepared macOS/Linux hosts; Windows dependency cross-build is compile
coverage, not a claim of tested Windows shell behavior.

Crash tests actually kill a process after durable intent/external-effect
barriers and restart it against the same storage. Safe replays require both
stored and current policies to be safe. Unsafe work is marked interrupted;
external-effect idempotency is an application responsibility. Request-ID dedup
and terminal task recovery do not guarantee arbitrary exactly-once I/O.

## After completion

Confirm `status: complete`, one accepted Durable module commit, all cumulative
judges and whole-project integration. Read the generated `docs/sdk/durable.md`,
then run its offline embedding/recovery examples. Live-provider validation is
separate and may incur charges.

The pinned upstream revision already matches the current accepted Pi 1.0
baseline, so this additive migration does not advance `migration/sync.json`.
The prepared `new-mappings.json` is a reviewed ownership proposal. After actual
acceptance, merge it into the existing sync config, **preserving existing owners**
(e.g. Chord types/context) and add `packages/durable/src` to watched roots.
Do not register unaccepted future files as delivered. This final metadata review
requires no extra model port and will enable later incremental updates.
