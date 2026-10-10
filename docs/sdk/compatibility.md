# Embedded SDK compatibility

This table records the compatibility scope of the Go embedded SDK
(`packages/coding-agent`). The original increment used Pi
`f07218c4d4bbc12bef056a7058c3dd49dfe41abe`; the accepted Pi 1.0 sync baseline is
`a13d35a742c6ef8462812a28fbe1d8c8b7431c32`, with additive Durable delivery.
It is deliberately conservative: a checked-in test proves the covered contract
only. Passing the cumulative and independent tests is **not** proof of full Pi
behavior or production readiness.

Status legend:

- **Ported** — Go implementation with matching public behavior.
- **Adapted** — Go shape differs for an idiomatic or headless reason; the
  difference is recorded in the reason column of the source map.
- **Excluded** — intentionally outside this increment, with a concrete reason.

## Core session and provider surface

| Pi area | Status | Go surface | Notes |
| --- | --- | --- | --- |
| `createAgentSession` | Ported | `CreateAgentSession`, `SessionOptions` | Returns `*AgentSession` plus error; services are built internally. |
| Session events | Adapted | `SessionEvent` + `SessionEventType` | One discriminated union replaces the TS event hierarchy; callers switch on `Type`. |
| Streaming / partial updates | Adapted | `Subscribe` callbacks, `RunResult` | Headless callers observe events and the final result; no TUI rendering. |
| Images and prompt options | Adapted | `Prompt`, `Steer`, `FollowUp` | Images, loaded skill/template expansion and streaming input are wired through the session. All inputs share the same option-aware methods; automatic resizing is not implemented. See [prompt-inputs.md](./prompt-inputs.md). |
| Prepared services | Adapted | `CreateAgentSessionFromServices` | Preserves resolved resources and settings; accepts virtual routes and stream observers. |
| Message/transcript conversion | Ported | `ConvertToLlm` | Reuses the accepted harness message conversion. |
| Model resolution | Ported | `ResolveModel`, `ResolveSessionModel` | Explicit overrides only; capacity is never reduced. |
| Model/provider runtime | Ported | `ModelRuntime`, `ModelRegistry` | Reuses the Pith AI catalog, auth and provider composition. |
| Provider protocols | Ported (reused) | `packages/ai/api`, `packages/ai/providers` | The SDK does not reimplement provider wire protocols. |
| Credential callbacks | Ported | `ModelOptions.APIKey` | Per-request resolution; no global credential cache. |
| Headless provider selection | Ported | native `Api` dispatch | Model API selects the provider when `StreamFn` is nil. |
| Usage reporting | Ported | `RunResult.Usage`, `SessionStats`, `UsageTotals` | Aggregated per run and session. |

## Sessions, storage and compaction

| Pi area | Status | Go surface | Notes |
| --- | --- | --- | --- |
| Durable session file | Ported | `OpenSession`, `SessionManager` | Append-only JSONL tree; version 3. |
| Branching / tree navigation | Ported | `Branch`, `BranchWithSummary`, `GetTree`, `GetBranch` | Leaf changes are durable. |
| Session projection/context | Ported | `BuildSessionProjection`, `BuildSessionContext` | Context edits and compaction entries are applied. |
| Auto-compaction | Ported | `RunPolicy.CompactReserveTokens`, `Compact` | Caller supplies `Summarize`. |
| Branch summarization | Ported | `PrepareBranchEntries`, `GenerateBranchSummary` | Reuses the accepted harness compaction package. |
| Retry of transient provider errors | Ported | `RunPolicy.RetryAttempts`, `IsRetryableAssistantError` | Non-retryable auth failures fail fast. |
| Queued messages (steer/follow-up) | Adapted | `Steer`, `FollowUp`, `PromptOptions` | Delivered at turn boundaries; in-memory pending queues survive rebuilds. Undelivered queues are not saved across process restarts. |
| Cancellation | Ported | context cancellation, `Abort` | Reaches provider I/O, tools and child processes. |

## Tools and resources

| Pi area | Status | Go surface | Notes |
| --- | --- | --- | --- |
| Built-in read/write/edit/bash | Ported | `CreateReadTool`, `CreateWriteTool`, `CreateEditTool`, `CreateBashTool` | Reuses the accepted harness tools. |
| grep/find/ls | Ported | `CreateGrepTool`, `CreateFindTool`, `CreateLsTool` | Go implementations with truncation. |
| Custom tools | Adapted | `ToolDefinition`, `ToolRegistry` | Constructed directly in Go instead of `defineTool` in TS. |
| Tool hooks / validation | Ported | `ToolHooks`, `ToolRegistry.Execute` | `Before` runs before execution and can deny. |
| File mutation queue | Ported | `WithFileMutationQueue` | Serializes same-path mutations. |
| Output truncation | Ported | `TruncateHead`, `TruncateTail`, `TruncateLine` | Explicit limits; never silently drops capacity. |
| Skills (plain text) | Ported | `LoadSkills`, `Skill`, `FormatSkillsForPrompt` | No JS skill execution. |
| Prompt templates | Ported | `LoadPromptTemplates`, `ExpandTemplate` | Plain markdown plus frontmatter. |
| Project context files | Ported | `LoadResources`, `ContextFile` | Explicit paths only; no implicit home discovery. |
| System prompt assembly | Ported | `BuildSystemPrompt` | Headless prompt text. |

## Additional native surfaces

These rows cover the AI catalog and the MCP, Codemode and virtual-model
surfaces. Their Go packages are delivered alongside the SDK and compose through
`CreateAgentSession`.

| Pi area | Status | Go surface | Notes |
| --- | --- | --- | --- |
| Legacy static catalog | Ported | `packages/ai/catalog` (`MODELS`, `IMAGE_MODELS`) | Frozen pre-1.0 assets kept separate. |
| V1 release catalog | Ported | `catalog.V1Models`, `V1Manifest`, `V1ProviderNames` | Defensive copies; pinned digests; chat/image/classifier kinds. |
| Mixed provider registry | Ported | `providers.BuiltinModels`, `Models.GetAllModels` | `GetModels` stays chat-only; classifiers never leak into it. |
| Structured classification | Ported | `packages/ai/api` (`TypesafeSystemOneClassify`, ...), `ai.Models.Classify` | Failures are encoded in the result, never returned as an error. |
| Native MCP client | Ported | `packages/mcp` (stdio, Streamable HTTP, in-memory) | Client half only; no Node helper. |
| MCP session runtime | Ported | `codingagent.MCPRuntime`, `MCPServerConfig` | Partial connection failures become diagnostics. |
| Codemode sandbox | Ported | `packages/codemode` | Embedded quickjs-wasi 3.6.2 driven by wazero; CGO-free. |
| Codemode SDK tool | Ported | `codingagent.NewCodemodeTool`, `CodemodeToolName` | Nested calls re-enter registry validation and hooks. |
| Codemode store | Ported | `codingagent.NewCodemodeStore` | Persisted on the session branch. |
| Virtual model routing | Ported | `codingagent.CreateVirtualModel`, `ModelRouteRequest`, `ModelRoute` | Per-session router; state persisted as `pi.virtual-model-state`. |
| Tool search / deferred tools | Ported | `codingagent.CreateToolSearchTool`, exposure modes | BM25 ranker over deferred tool declarations. |

See [catalog-versions.md](./catalog-versions.md), [mcp.md](./mcp.md),
[codemode.md](./codemode.md) and [virtual-models.md](./virtual-models.md) for the
per-surface contracts.

## Configuration and settings

| Pi area | Status | Go surface | Notes |
| --- | --- | --- | --- |
| Layered settings | Ported | `LoadSettings`, `SaveSettings`, `SettingsManager` | Unknown JSON keys survive round trips. |
| Credential storage | Ported | `AuthStorage`, `FileAuthStorageBackend` | Headless file-backed credentials. |
| Provider/auth helpers | Ported | `ResolveConfigValue`, `AuthStatus` | Env and command config values. |
| HTTP dispatcher/proxy | Adapted | `ConfigureHTTPDispatcher`, `ApplyHTTPProxySettings` | Idle-timeout validation only; the host applies transport idle monitoring. Proxy settings fill unset process proxy variables. |

## Excluded from this increment

| Pi area | Status | Reason |
| --- | --- | --- |
| TS/JS extension execution | Excluded | Custom Go tools and hooks replace extension plugins. |
| `ExtensionAPI` / `ExtensionContext` / `ExtensionRunner` | Excluded | No JS extension host is shipped. |
| Extension lifecycle hook events (`BeforeAgentStartEvent`, `SessionBefore*Event`, provider hooks) | Excluded | Go hooks cover tool execution; extension hook events have no headless equivalent. |
| TUI widgets, themes, editors, renderers | Excluded | This SDK is headless; terminal rendering is out of scope. |
| `RpcClient` / RPC host mode | Excluded | Remote service management is out of scope. |
| npm/Git extension package manager | Excluded | Package installation is explicitly out of scope. |
| Browser login / OAuth UI | Excluded | Headless credential callbacks are provided instead. |
| Clipboard and syntax highlighting | Excluded | Host UI utilities. |
| Automatic image normalization/resize | Not implemented | Pi also uses this in the session SDK. Pith passes attachment bytes through; this is a remaining SDK gap, not merely a TUI utility. |
| CLI orchestration (`main`, `parseArgs`, print/interactive modes) | Excluded | Stays in `cmd/pith`; the SDK is a library. |
| Pi Durable runtime | Ported (additive) | `github.com/minifish-org/pith/packages/durable` plus `.../harness`, `.../env`, `.../tools`, `.../storage/{memory,jsonl,sqlite}` and `.../testing` | Optional headless port; enabling Durable does not replace the coding-agent session engine or change its file format. See [durable.md](./durable.md). |
| Pi interactive CLI parity | Excluded | `cmd/pith` is a non-interactive turn loop; the interactive CLI modes are not ported. |

## Known adaptation caveats

The targeted post-port audit and remaining priorities are recorded in
[port-gaps.md](./port-gaps.md). Export/source-map coverage alone is not proof
that optional fields are consumed by the runtime.

- The Durable SDK is a native Go API, not a line-for-line or binary-compatible
  TypeScript API. `packages/durable` (records, Session, storage adapters),
  `packages/durable/harness` (scheduling and models), `packages/durable/env`
  (capabilities), `packages/durable/tools` (coding tools) and
  `packages/durable/testing` (conformance) are idiomatic ports of their source
  modules; `docs/sdk/durable-source-map.json` records ownership and merges.
- External effects cannot be guaranteed exactly once. Durable's pinned
  guarantees are: a committed request ID deduplicates submissions; checkpointed
  tasks resume; already-terminal work is not re-executed; tools replay only when
  BOTH stored and current policy say `safe`; an unsafe interrupted intent
  becomes an interrupted error. `Close`/suspend never fabricates completion.
- Go cannot revoke an escaped map reference as a JavaScript Proxy can, so the
  Go adaptation invalidates change handles, detaches candidate data during
  preparation and returns independent copies from value getters.
- `t` truncation and `Overlap` count UTF-16 code units; a truncation that would
  bisect a supplementary character is rejected because isolated surrogates are
  not representable as valid UTF-8 strings.

- The TS event union is collapsed into `SessionEvent`; event kinds that are not
  in the headless subset (session tree/compact/shutdown notifications) are not
  republished. Use `SessionManager` and `Compact` for those states.
- Source-map `reason` fields record per-export adaptations; when an export mixes
  excluded host behavior with SDK behavior, the SDK part is implemented and the
  host part is excluded rather than claimed as parity.
- The SDK is not a filesystem sandbox or an authorization system. Tool hooks are
  application policy, not a security boundary.
- Provider coverage depends on the accepted Pith AI packages; this table does
  not assert a level of behavioral parity beyond the tested contract.
