# Native Codemode sandbox without CGO

Use the immutable quickjs-wasi 3.6.2 WASM artifact embedded from base64 text and
wazero v1.9.0 (already pinned in project go.mod). Port the QuickJS host ABI from
the frozen release JS wrapper, including value lifetimes, promise jobs, callbacks
and interrupt handling. Do not invoke Node, shell JavaScript or a native library.
The reviewed pure-Go feasibility probe verifies module instantiation/eval only;
it is not an implementation of async sandbox behavior.

Root facade: NewSandbox(SandboxOptions) (*Sandbox,error),
Sandbox.Execute(ctx context.Context, source string, options ExecuteOptions) Result,
Sandbox.Close(ctx context.Context) error.
SandboxOptions{Tools []Tool; Globals []Tool; Timeout time.Duration;
MemoryLimitBytes uint64; MaxStackBytes uint64}.
Tool{Name,Description string; InputSchema,OutputSchema json.RawMessage;
Spread bool; Signature string; Execute func(context.Context,json.RawMessage)(json.RawMessage,error)}.
ExecuteOptions{Store map[string]json.RawMessage; Timeout time.Duration}.
Result{OK bool; Value json.RawMessage; Output []OutputItem; Calls []Call;
StoreWrites *StoreWrites; Error *ExecutionError}.
OutputItem{Type,Text,Data,MimeType string}; Call{Name,Status string; DurationMs float64};
StoreWrites{Set map[string]json.RawMessage; Delete []string};
ExecutionError{Kind,Name,Message,Stack string}.
Keep upstream JSON wire names via tags. Undefined result is nil, not fabricated null.

Scripts run as async function bodies with top-level await/return. Tool/global
arguments/results cross a JSON boundary, promises settle through a serialized VM
event loop; global helpers are not recorded as tools. Normalize identifier names
and reject collisions. Port ALL_TOOLS, text, console, image, exit, store/load,
declaration rendering/local refs and source // @options parsing. Fresh VM each
execute. No process/require/fetch/timers/host FS/network/module loader/preopened
dirs. WASI clock/random are host infrastructure only; JavaScript cannot access
WASI or the privileged bridge. eval and Function may evaluate inside guest only.

Honor source timeout/output options and caller abort during CPU loops, host tools
and pending promises. Preserve text/image output before failure. Report stalled
promises rather than hanging. Unawaited calls cancel at return; tool work has the
same execution-scoped cancellation. Apply QuickJS memory/stack bounds, including
OOM handling and a WASM page cap; a broken/closed VM cannot contaminate next run.
Store is JSON-copied, per-value 256 Ki UTF-16 code units, total 1 Mi code units.
Return only changed set/delete keys on success; failed runs have no StoreWrites.
Images accept base64 data, reject remote URLs. Source docs define exact semantics.

Expose ToIdentifier(name string) string and RenderDeclarations([]Tool) (string,error).
Frozen judges cover real async calls, outputs, unavailable globals, VM isolation,
store commit/rollback, cancellation/deadline/stall, declaration schemas and
normalized tools. Candidate tests port every upstream sandbox/source/declarations
test and cover heap pressure, recursive stack, bridge escape probes, concurrent
Execute/Close, unawaited tools and invalid options. No live provider or Node.
