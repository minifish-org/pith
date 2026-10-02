# Pi 1.0 native SDK migration

This executable plan updates the accepted Pith SDK from upstream
`002fc8385268300ca91a5fc95f935c2afbbdac02` to the fixed Pi v1.0.0 release,
`a13d35a742c6ef8462812a28fbe1d8c8b7431c32`.

## Scope

The target is an embeddable Go SDK: AI/model/provider operations, agent core,
native tools, standalone MCP, sandboxed Codemode and headless session integration.
Native Go APIs and legacy caller compatibility take priority over TS filename
parity. No CGO, Node runtime, TUI, interactive CLI, TS extension loader or Durable
runtime is added. The older experimental Pith harness is retained for compatibility;
its upstream deletion does not authorize deleting existing Pith application APIs.

Nine steps execute in order within one release module:

| Step | Behavior |
| --- | --- |
| ai-foundation | Typed models/DTOs, headers/errors/retry helpers, exact V1 catalog |
| ai-runtime | Mixed catalogs, image/classifier operations, stores and providers |
| ai-protocol | Streaming fixes, Mistral effort/deltas, tool replay/cache metadata |
| ai-auth | Provider credentials, OAuth callbacks/PKCE and refresh flows |
| core | Nested RunToolCall, structured results, hooks and thinking metadata |
| mcp | Stdio/Streamable HTTP client, OAuth, content and lifecycle |
| codemode | Embedded QuickJS WASM, async Go bridge, limits and store semantics |
| sdk | Virtual routing, MCP/Codemode, deferred tools, sessions and cache warmer |
| delivery | Composable SDK examples, docs, compatibility and no-CGO cross builds |

Each step has a contract, frozen independent judges and mandatory candidate
self-tests. Verification is cumulative. Accepted steps are checkpointed, and the
whole release module is committed only after complete acceptance. A second
recoverable metadata commit advances the upstream revision and installs reviewed
source mappings. The executor does not push commits.

## Run from Pith

Preflight makes no model calls:

```sh
cd ~/work/pith
../portsmith-go/bin/portsmith sync \
  --upstream a13d35a742c6ef8462812a28fbe1d8c8b7431c32 \
  --out migration/sync/pi-1.0 --check
```

Start the actual migration with the existing DeepSeek Flash configuration:

```sh
cd ~/work/pith
rtk proxy env CGO_ENABLED=0 GOMAXPROCS=4 \
  ../portsmith-go/bin/portsmith sync \
  --upstream a13d35a742c6ef8462812a28fbe1d8c8b7431c32 \
  --out migration/sync/pi-1.0 \
  --commit --env-file ../omni-pi/.env
```

RTK is only the local command-output wrapper; the underlying binary works without
RTK. `--out` selects this reviewed plan instead of creating an unprepared default
draft. `--commit` enables the model run, cumulative acceptance and Git commits.
`--env-file` loads local credentials; never put the API key in this plan or Git.
The CGO/CPU settings apply to the process and its Go build/test children.

The current Portsmith Go defaults leave model turns, runtime and repair attempts
unlimited. Ctrl-C preserves progress. Repeat exactly the same command to resume
an interrupted/failed step. Do not regenerate the plan, modify its frozen judges,
delete `.portsmith` or move the source pin while a run is active. If a contract or
judge requires correction, stop and review the affected unfinished task first.

After completion, inspect the acceptance receipt and commits, then push Pith.
Rebuild/re-pin a consuming application such as Portsmith Go separately to link the
new Pith revision: upgrading the library source does not replace a running binary.
Only then evaluate a separate Durable migration.

## Evidence and ownership

* `scope-review.json`: every cumulative changed path and adaptation/defer reason.
* `new-mappings.json`: semantic source-to-Go owners applied after acceptance.
* `contracts/`, `judges/`: native API contracts and 26 independent offline tests.
* `assets/`, `review/release-artifacts.json`: registry-pinned catalogs and QuickJS,
  integrity values, third-party notices and pure-Go runtime dependency.
* `VALIDATION.md`, `review/`: completed preparation checks and their limits.
* `plan.json`, `workflow.json`: 234 existing writable files, 815 read-only baseline
  files and explicit new outputs/assets. Existing tests/oracles/receipts are frozen.

The expanded sync watch roots include the new MCP/Codemode packages and their
headless builtin integrations. The config revision is deliberately still the old
accepted revision until the release module passes. Generated V1 catalogs come from
the exact npm release, not mutable latest catalog endpoints or invented data.

Legacy static catalogs and no-argument provider factories retain their historical
data for backward compatibility. The modern mixed builtin registry and embedded
SDK must use the V1 catalog. This distinction is explicit in the runtime contract
and must be documented in delivered SDK docs; test-specific behavior is forbidden.

Source snapshots are checked against exact Git blobs. On a fresh checkout, `sync`
fetches/materializes the pinned source cache. Dependencies must be available in
the Go module cache (run `go mod download` once on a fresh machine). No npm install
or external JavaScript interpreter is needed by the migrated product.
