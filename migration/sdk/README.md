# Incremental migration: Pith as an embedded agent SDK

This plan adds the headless coding-agent application layer to the accepted Pith AI/Core/Tools port. It uses the **existing TypeScript Portsmith + Pi coding-agent backend**, with **DeepSeek Flash** generating the Go candidate. It does not use Pith as the migration executor yet, and does not migrate Portsmith-go.

The upstream remains Pi **v0.87.1**, commit `f07218c4d4bbc12bef056a7058c3dd49dfe41abe`. This is a scope expansion at the same revision, not an update to newer Pi code. The accepted Pith baseline is `f11b605c89ce3b9eefcd347786cd1a4e0abbb3dd`.

## Run it

Build the updated sibling executor first; this plan requires its additive-baseline and separate-journal support:

```sh
cd ~/work/portsmith
rtk npm run build
cd ~/work/pith
rtk proxy node ../portsmith/dist/cli.js migrate --plan migration/sdk --check
```

The preflight should report `ready`, `canStart: true`, seven prepared steps and no blocked batches. It does not call a model. It verifies source hashes, baseline hashes, contracts, judge files and progress identity; it does not claim the SDK already exists.

Start the complete increment:

```sh
cd ~/work/pith
rtk proxy env PORTSMITH_MODEL=deepseek-flash PORTSMITH_BASE_URL=https://api.deepseek.com/v1 \
  node ../portsmith/dist/cli.js migrate --plan migration/sdk --commit --env-file ../omni-pi/.env
```

The key is read from the existing `PORTSMITH_API_KEY` or `OMNI_API_KEY` configuration. Do not put the key in a command or commit an env file. Explicit environment values override the env file. Provider usage is billed when this command calls DeepSeek. No paid run was made while preparing this plan.

One command advances through all seven steps, verifies and repairs each candidate, and commits the **one complete SDK module** after cumulative and actual-project tests pass. The executor may first commit the new `migration/sdk` preparation files. It does not push. There are no default model-turn, repair-attempt or run-duration caps. Ctrl-C preserves the candidate; rerun the same command to resume. Do not delete the previous `.portsmith/modules.json` or old migration receipts.

## Scope and order

| Step | Capability |
| --- | --- |
| configuration | Settings, custom model/provider configuration, capacities and credential callbacks |
| storage | Durable session entries, active branches, reopen and immutable snapshots |
| resources | System/project instructions, skills, templates and explicit resource roots |
| tools | Existing execution tools, search tools, Go custom tools and before/after hooks |
| session | Embedded session creation, tool loop, structured events, queues and lifecycle |
| resilience | Cancellation, retry, truncation continuation, auto/manual compaction and recovery |
| delivery | Public SDK documentation, examples, real local HTTP provider integration and source maps |

The inventory owns **54 upstream source files / 21,109 physical lines**, including comments and blank lines. These are adapted source lines, not an estimate of new Go code: existing Pith packages are reused. There are 161 source/test/example references and 998 source-export mapping records, including barrel re-exports. The full coding-agent source inventory and each selected source's import decisions are in `inventory.json`.

The public entry point will be `github.com/minifish-org/pith/packages/coding-agent` (`package codingagent`). `API.go.txt` specifies the minimum Go signatures; its no-op bodies are a deliberately wrong test control, never implementation. It supports caller-owned tools, model configuration, resource paths, storage and contexts. Models use Pith's existing provider layer; a `StreamFn` can be injected for offline tests or a host-specific provider adapter.

Explicit exclusions: TUI and interactive CLI; JS/TS plugin execution and npm/Git package installation; experimental service backends; Pi-hosted account management; HTML/session-sharing UI; cache prewarming. Go tool/hook registration replaces the JS extension runtime. No promise of transparently loading arbitrary Pi plugins or every historical Pi session-file version is made. Skills and templates remain text resources. The existing `sdk.Run` and CLI remain compatible and unchanged; the new package is the full embedded entry point.

## Isolation, evidence and maintenance

- `workflow.json` freezes 391 existing package/CLI files by hash and accepted Git commit. They are copied into candidates read-only. Existing conformance tests also run at final integration in the real Pith project. Changes to the baseline stop the run instead of silently rebasing it.
- New progress: `.portsmith/sdk/modules.json`. Candidate/session logs: `.portsmith/sdk/runs/sdk/<step>/port/`. The original progress is untouched. The repository-wide execution lock prevents simultaneous plans from committing into the same checkout.
- The SDK files are installed only after cumulative acceptance. The new receipt is `migration/results/sdk.json`; original `ai`, `core` and `tools` receipts stay intact.
- Each step writes its own `source_map_<step>.json`, including **actual Go file and exported declaration**, source symbol and adaptation reason. The delivery judge resolves those declarations using Go's parser. This makes the next upstream diff traceable without forcing a one-to-one directory layout.
- Candidate compilation/tests use `CGO_ENABLED=0`; Go's race detector uses its required CGO-enabled test runtime. Delivery acceptance cross-builds the SDK and examples for macOS arm64, Linux amd64 and Windows amd64 without CGO.
- No TUI, Node, Pi executable or npm packages are required in an application that embeds the resulting SDK. Portsmith itself still uses Node while performing this migration.

If a frozen underlying Pith API proves defective or insufficient, stop and review a small baseline fix/new plan. Do not tell the model to modify frozen files or disable a judge. This plan deliberately adds an SDK against a stable baseline; it is not a generic arbitrary-file patch/merge engine.

## Verify the preparation itself

```sh
cd ~/work/pith
rtk proxy node migration/sdk/audit.mts
```

This prepares every step in disposable directories, compiles the independent tests against a typed empty SDK, and requires **every judge to reject that empty implementation**. See `readiness/audit.json`. All preparation checks are offline and make no model calls.

The judges cover settings/model capacities, durable branching, resources, tool schema/allowlist/hooks, a two-turn custom-tool session, real HTTP provider integration, retry/exhaustion, cancelled streams, queued messages, compaction/rollback, source-map resolution and cross-builds. They are frozen Go contract tests informed by upstream source and tests, **not exhaustive differential execution of Pi**. The contracts require additional candidate tests for unenumerated edge cases. Passing preparation means the migration is ready to run, not that SDK correctness or full parity has already been established. After generation, review compatibility notes and run a separate real-DeepSeek smoke test before relying on the SDK in a customer product.
