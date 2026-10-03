# Codemode

`packages/codemode` is the native, CGO-free Codemode sandbox. JavaScript runs as
an async function body inside an embedded QuickJS VM that is driven by the
pure-Go [wazero](https://wazero.io) runtime, and tool calls are bridged over a
JSON boundary. There is no Node.js executable, no shell JavaScript engine, no
cgo and no external QuickJS shared library.

The embedded runtime is the immutable `quickjs-wasi` 3.6.2 release committed as
base64 under `packages/codemode/runtime/quickjs.wasm.base64.txt`. Its MIT license
text is retained at `packages/codemode/LICENSES/quickjs-wasi.txt`; the upstream
QuickJS-ng notice is at `packages/codemode/LICENSES/quickjs-ng.txt`. See
[../third-party-notices.md](../third-party-notices.md).

## Sandbox

```go
sandbox, err := codemode.NewSandbox(codemode.SandboxOptions{
    Timeout:          5 * time.Second,
    MemoryLimitBytes: 32 << 20,
    Tools:            []codemode.Tool{{Name: "read-note", Execute: readNote}},
})
if err != nil { ... }
defer sandbox.Close(context.Background())

result := sandbox.Execute(ctx, `text("hi"); return 42;`, codemode.ExecuteOptions{})
```

`Sandbox` is safe for concurrent use; every execution gets a fresh QuickJS VM, so
global state never leaks between runs. `Close` aborts in-flight executions and
releases the wazero runtime. `SandboxOptions.Timeout` is the default per-run
deadline (zero uses 300s, negative disables it). `MemoryLimitBytes` and
`MaxStackBytes` bound the QuickJS heap and native stack.
`ExecuteOptions.Store` is a JSON map copied into the VM; the caller's map is never
mutated.

`RegisterTool` / `UnregisterTool` / `Tools` / `Globals` mutate the tool tables.
A duplicate tool name, a duplicate global, an invalid global identifier or a
global that shadows a reserved name is rejected by `NewSandbox`/`RegisterTool`
instead of failing later in a script.

## Scripts

A script is the body of an async function:

- `tools.<identifier>(args)` calls a registered tool. Non-identifier characters
  become `_` (`mcp.search-docs` → `tools.mcp_search_docs`); the original name is
  also available. Reading an unknown member throws a helpful `TypeError`.
- `ALL_TOOLS` lists the callable tool identifiers.
- `text(value)` appends a text output item. `image(value)` appends a base64 image
  output item and rejects remote URLs and non-image data.
- `console.log/info/warn/error/debug` append text output items.
- `store(key, value)` / `load(key)` read and write a size-bounded JSON store.
  `store(key, undefined)` deletes a key.
- `exit()` reports success early.

A script may start with a `// @options:` first line. `timeout_ms` and
`max_output_tokens` are the recognized fields; an unknown or malformed options
line is a `CodemodeSourceError` (kind `script`).

## Callback concurrency and cancellation

Tool and global callbacks are Go functions, not JavaScript host handlers. The
whole blocking callback runs in its own goroutine, so independent callbacks run
concurrently — overlapping globals or tool calls, and calls issued by concurrent
`sandbox.Execute` invocations — and may start and finish in either order. This
is a deliberate adaptation, not a claim of identical JavaScript scheduling: the
JavaScript host runs a callback's synchronous prefix before its first `await`,
while Go has no analogous first-await boundary and the complete callback runs
concurrently.

Because callbacks may overlap, hosts must synchronize any shared state they
touch (for example with a `sync.Mutex`). Script sequencing, not callback
ordering, expresses dependency: `await` one call before starting the next to
depend on it, or start several and await `Promise.all` to allow them to overlap.

A sandbox return cancels the context passed to every outstanding callback,
including callbacks the script never awaited, but it cannot force a callback
that ignores its context to stop. Do not rely on the completion or side effects
of an unawaited callback before `Execute` returns.

## Results

`Result` has `OK`, the JSON `Value` (undefined leaves it nil), ordered `Output`
items, recorded `Calls` (globals are not recorded), optional `StoreWrites`, and
an `Error`. On failure `StoreWrites` is nil: a failed script commits nothing.
`ExecutionError.Kind` is one of `script`, `timeout`, `aborted` or `sandbox`.

`Execute` never panics for a script failure; it reports it in `Result.Error`.
Caller cancellation and the sandbox deadline both terminate a CPU loop or a
pending promise. A promise that can never settle — no pending tool call and no
timers — is diagnosed as a script error instead of hanging.

## Declarations

`RenderDeclarations`, `RenderDeclarationsWithGlobals` and `RenderToolSignature`
turn the tool table into TypeScript declarations for an agent prompt.
`SchemaToType` converts JSON Schema to a TypeScript type with a character budget
(`DefaultInputSchemaMaxChars`). `MCPTypescriptPreamble` ships the MCP content
types the declarations reference.

## SDK integration

The embedded SDK builds a `codemode` tool from a tool registry:

```go
tool, err := codingagent.NewCodemodeTool(registry, &codemode.SandboxOptions{Timeout: time.Second})
```

- The tool is registered as `codingagent.CodemodeToolName` (`"codemode"`) with
  one parameter, `code`.
- Every active, non-hidden registry tool becomes callable as
  `tools.<identifier>`. The spec table is captured when `NewCodemodeTool` runs.
- Nested calls re-enter the registry, so schema validation and the `Before`
  and `After` hooks run for every nested call. A denied tool never executes.
- The deny list and deferred/codemode exposure rules still apply to nested calls.
- A tool's structured content becomes the script call's return value; otherwise
  its text content is returned.
- Successful `store()` writes are persisted on the session branch
  (`packages/coding-agent/codemode_store.go`); failed scripts commit nothing.

See `examples/codemode` for a runnable offline example and
`internal/conformance/pi_v1_sdk/integration_test.go` for a composition test.

## Limits and caveats

- The sandbox is an isolation boundary for JavaScript capabilities, not a
  security boundary for the host process. Tools are the only escape hatch and
  they are explicit.
- `setTimeout`, `fetch`, `process`, `require` and `WebAssembly` are not exposed.
- Timers do not exist; keep long-running work inside tool calls.
- Passing the Codemode tests proves the covered contract: async/JSON bridging,
  isolation, store semantics, cancellation, declarations and the compiled asset.
  It does not claim full Node.js compatibility.
