# Pi 1.0 SDK delivery

This document is the map between the delivered native Go SDK and the upstream Pi
source, plus a precise feature/adaptation table. It is deliberately conservative:
the checked-in tests prove the covered contract only. Passing them is **not**
proof of full Pi behavior, production readiness or parity with the Pi CLI, the
Durable runtime, the TypeScript extension host or the TUI.

Upstream revision: `f07218c4d4bbc12bef056a7058c3dd49dfe41abe` (Copyright (c) 2025
Mario Zechner, MIT License).

## Delivered packages

| Go package | Import path | Upstream source |
| --- | --- | --- |
| `ai` | `packages/ai` | `packages/ai/src/{models,models-store,classifier,...}.ts` |
| `api` | `packages/ai/api` | `packages/ai/src/api/*` |
| `providers` | `packages/ai/providers` | `packages/ai/src/providers/*` |
| `catalog` | `packages/ai/catalog` | generated catalogs + `providers/data-json.d.ts` |
| `auth` | `packages/ai/auth` (+ `auth/oauth`, `auth/types`) | `packages/ai/src/auth/*` |
| `compat` | `packages/ai/compat` | `packages/ai/src/compat.ts` and legacy aliases |
| `agent` | `packages/agent` | `packages/agent/src/{agent,agent-loop,proxy,types}.ts` |
| harness | `packages/agent/harness/...` | `packages/agent/src/harness/*` |
| `codingagent` | `packages/coding-agent` | `packages/coding-agent/src/core/{sdk,agent-session,virtual-models,...}.ts` |
| `mcp` | `packages/mcp` | `packages/mcp/src/*` + MCP TS SDK v1.29.0 |
| `codemode` | `packages/codemode` (+ `codemode/runtime`) | `packages/codemode/src/*` + quickjs-wasi |
| `telemetry` | `packages/telemetry` | `packages/telemetry/src/*` |
| CLI | `cmd/pith`, `cmd/pith-ai` | assembled Pi entry points |

Per-export mapping (source file, upstream symbol, Go package/symbol, reason and
excluded or not) is recorded in the machine-readable source maps:

- `packages/coding-agent/source_map_delivery.json`
- `packages/coding-agent/source_map_configuration.json`
- `packages/coding-agent/source_map_resilience.json`
- `packages/coding-agent/source_map_resources.json`
- `packages/coding-agent/source_map_session.json`
- `packages/coding-agent/source_map_storage.json`
- `packages/coding-agent/source_map_tools.json`
- `internal/conformance/<area>/source_map.json` for the AI, core, harness and
  tools areas.

## Feature and adaptation table

Status legend: **Ported** = matching public behavior; **Adapted** = idiomatic or
headless Go shape with the reason recorded in the source map; **Excluded** =
intentionally outside the delivered scope.

### AI and catalog

| Pi feature | Status | Go surface | Adaptation |
| --- | --- | --- | --- |
| Chat model DTO | Ported | `types.Model` | missing `type` means chat |
| Image / classifier model DTOs | Ported | `types.ImagesModel`, `types.ClassifierModel` | tagged union via `types.AnyModel` |
| Legacy static catalog | Ported | `catalog.MODELS`, `catalog.IMAGE_MODELS` | historical assets kept separate |
| V1 release catalog | Ported | `catalog.V1Models`, `V1Manifest`, `V1ProviderNames` | defensive copies; `schemaVersion: 6` |
| Mixed provider registry | Ported | `providers.BuiltinModels`, `Models.GetAllModels` | `GetModels` stays chat-only |
| Structured classification | Ported | `api.TypesafeSystemOneClassify`, `ai.Models.Classify` | union questions/answers as tagged structs; never returns an error |
| Deprecated global API | Adapted | `packages/ai/compat`, `legacy_api_aliases.go` | delegates to modern entry points; deprecated but not removed |
| Provider protocols | Ported (reused) | `packages/ai/api`, `packages/ai/providers` | wire protocols are not reimplemented |
| Auth and credential stores | Ported | `packages/ai/auth`, `auth/oauth` | headless callbacks replace browser UI |

### Core, session and compaction

| Pi feature | Status | Go surface | Adaptation |
| --- | --- | --- | --- |
| Agent loop | Ported | `packages/agent` | events as Go structs |
| Harness (context, session, compaction, tools, resources) | Adapted | `packages/agent/harness/...` | moved to a sibling package; removed from the `agent` barrel re-exports |
| Session tree / JSONL storage | Ported | `harness/session`, `harness/session/jsonl` | append-only version 3; legacy v3 read kept |
| Compaction and branch summarization | Ported | `harness/compaction`, `codingagent.Compact` | caller supplies `Summarize` |
| Built-in tools | Ported | `harness/tools`, `codingagent.Create*Tool` | Go implementations with explicit limits |
| Telemetry | Adapted | `packages/telemetry` | in-memory and noop sinks; no exporter |
| `AgentSession` | Ported | `codingagent.AgentSession`, `CreateAgentSession` | returns `*AgentSession` plus error |

### MCP, Codemode and virtual models (new in this step)

| Pi feature | Status | Go surface | Adaptation |
| --- | --- | --- | --- |
| MCP client (stdio / HTTP / in-memory) | Ported | `packages/mcp` | native Go client; no Node helper |
| MCP OAuth | Ported | `mcp.AuthorizeMcp`, `McpOAuthProvider`, `OAuthCallbackServer` | headless callback pages |
| MCP session tool runtime | Ported | `codingagent.MCPRuntime`, `MCPServerConfig` | partial failures reported as diagnostics |
| Codemode sandbox | Ported | `packages/codemode` | embedded quickjs-wasi + wazero; CGO-free |
| Codemode SDK tool | Ported | `codingagent.NewCodemodeTool` | nested calls re-enter registry validation and hooks |
| Codemode store | Ported | `codingagent.CodemodeStore` | persisted on the session branch |
| Virtual model routing | Ported | `codingagent.CreateVirtualModel`, `ModelRouteRequest` | per-session router; state persisted on the branch |
| Tool search / deferred tools | Ported | `codingagent.CreateToolSearchTool`, `ToolSearchToolName`, exposure modes | BM25 ranker over registry declarations |

### CLI, TUI, extensions and Durable

| Pi area | Status | Boundary |
| --- | --- | --- |
| `cmd/pith` non-interactive CLI | Ported | one Chat Completions turn loop over the SDK, no Node dependency |
| `cmd/pith-ai` | Ported | AI surface test command |
| Interactive TUI (`modes/interactive/*`, themes, widgets, editors, renderers) | Excluded | headless SDK; terminal rendering is out of scope |
| TS/JS extension host (`ExtensionAPI`, `ExtensionContext`, `ExtensionRunner`) | Excluded | replaced by native Go tools and `ToolHooks` |
| npm/Git extension package manager | Excluded | package installation is out of scope |
| RPC host mode / `RpcClient` | Excluded | remote service management is out of scope |
| Remote service management and project-trust prompts | Excluded | not part of the headless SDK |
| Pi Durable runtime | Excluded | not claimed; `docs/` and `sync` are not advanced manually |
| TS CLI orchestration (`parseArgs`, print/interactive modes) | Adapted | `cmd/pith` flags; the SDK is a library |

## Runnable examples

All examples compile offline and default to injected/fake providers; none checks
in a credential and none calls a paid model in `go test`.

| Example | Path | Shows |
| --- | --- | --- |
| Codemode | `examples/codemode` | sandbox tools, output, store writes, declarations |
| MCP client | `examples/mcp-client` | in-memory transport, tool listing/call, content conversion |
| Pi V1 | `examples/pi-v1` | mixed catalog, kinds, offline System One classification |
| Virtual model | `examples/virtual-model` | router, reasons, persisted routing state |
| Embedded SDK | `examples/embedded-sdk/*` | minimal, custom tool, events, resume, resources, web |

The delivery integration test is
`internal/conformance/pi_v1_sdk/integration_test.go`: it composes the catalog, an
in-process MCP tool, the embedded Codemode tool and virtual routing through one
`CreateAgentSession` run, and it exercises the embedded sandbox under the
CGO-disabled acceptance build.

## Build and verification

```sh
# Native, offline, CGO-free.
CGO_ENABLED=0 GOMAXPROCS=4 go build -mod=readonly ./...
CGO_ENABLED=0 go vet -mod=readonly ./...
CGO_ENABLED=0 GOMAXPROCS=4 go test -mod=readonly -p=4 -timeout=0 ./...

# Cross-build the delivery scope (compile only).
CGO_ENABLED=0 GOOS=linux   GOARCH=amd64 go build ./...
CGO_ENABLED=0 GOOS=linux   GOARCH=arm64 go build ./...
CGO_ENABLED=0 GOOS=darwin  GOARCH=arm64 go build ./...
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build ./...
```

The Go race detector requires the cgo race runtime, so `-race` is a separate
local developer check and is not part of the CGO-disabled acceptance gate.

## Honest capability evidence

- The cumulative independent judges and the candidate self-tests exercise the
  reviewed scope with offline fixtures. They are evidence for that scope.
- No measured performance, live-provider, Durable or TUI parity is claimed, and
  none was tested. Provider edge cases and production hardening need more work.
- The V1 catalog and the embedded release assets are provenance-checked against
  their pinned digests (see [catalog-versions.md](./catalog-versions.md)).
- The Codemode sandbox is an isolation boundary for JavaScript capabilities, not
  a security boundary for the host process.

## Related documents

- [README.md](./README.md) — embedded SDK guide
- [compatibility.md](./compatibility.md) — compatibility scope and caveats
- [catalog-versions.md](./catalog-versions.md) — legacy vs V1 catalog
- [codemode.md](./codemode.md) — Codemode sandbox and SDK tool
- [mcp.md](./mcp.md) — native MCP client
- [virtual-models.md](./virtual-models.md) — virtual model routing
- [../third-party-notices.md](../third-party-notices.md) — licenses and provenance
