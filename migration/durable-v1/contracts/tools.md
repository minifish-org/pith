# Durable coding tools

Port pinned `packages/durable/src/tools/**` and `src/truncate.ts`, preserving
behavior as native Go tools using the Durable harness ToolAPI and execution
environment. Reuse existing Pith validation/diff facilities where appropriate;
do not import or adapt the old coding-agent session engine as Durable itself.

Package `packages/durable/tools` exports:

```go
func CreateReadTool() harness.ToolRegistration
func CreateWriteTool() harness.ToolRegistration
func CreateEditTool() harness.ToolRegistration
type BashExecution struct {
    Command, CWD string
    Env map[string]string
    InheritEnv bool
}
type BashToolOptions struct {
    CommandPrefix string
    Prepare func(context.Context, *BashExecution, harness.ToolAPI) error
}
func CreateBashTool(options ...BashToolOptions) harness.ToolRegistration
func CodingTools() harness.Extension
```

CodingTools returns extension `coding-tools` with read/write/edit/bash. It is
explicitly installed by the integrator; never silently install host tools in all
harnesses. The source declarations default to unsafe replay; preserve that
default, even for read, unless an application explicitly opts in with its own
registration. A function called `read` is not evidence it is replay-safe.

Implement ALL source-defined tool behavior:

- read: resolve tilde/relative/absolute paths through Env; 1-based offset and
  optional positive limit; typed missing/directory errors; detect image signature
  and emit unsupported-image diagnostics (this release does not synthesize image
  reading); retain text separate from diagnostics, head limits of 2000 lines and
  50 KiB; UTF-8 boundary-safe oversized-first-line trimming; continuation details.
- write: create parent directories; write exactly requested bytes; serialize
  mutations by environment namespace ID and canonical path, not path spelling;
  cancel before/after relevant operations and release queue ownership on error.
- edit: repair legacy top-level oldText/newText and edits encoded as a JSON
  string or single object without modifying original arguments; nonempty edits;
  match every edit against ORIGINAL content, exact unique nonoverlapping match,
  reject duplicates/overlap/missing matches with no partial write; preserve BOM
  and original CRLF; return diff, unified patch, and firstChangedLine details.
- bash: no default timeout; validate finite positive seconds and the source
  maximum; prefix/preparation callback; Env.Exec with 2000-line/50-KiB spilling;
  forward all raw chunks to ToolAPI.Output, keep tail through harness limits;
  emit full_output diagnostic for spill file even on timeout/abort/nonzero exit;
  nonzero exit is an error. Preserve cancellation rather than converting it to
  successful empty output. Pre-cancelled tools must not execute.

Keep output truncation, overlap merging, streaming progress throttling and
diagnostics faithful to the source. The existing `diff` npm package becomes the
already-frozen `github.com/sergi/go-diff` Go dependency, or native logic where
needed for source-equivalent patches. TypeBox schemas become the existing Pith
JSON-schema validator. No JavaScript runtime or CGO dependency is introduced.

Independent tests use a real Local environment and an injected ToolAPI, then
exercise end-to-end registration through the actual harness in the final stage.
Candidate self-tests must cover mutation-queue cancellation, symlink aliases,
output bounds and every source tool test not represented in the independent
smoke tests. Do not claim that builtin tools alone enforce filesystem permission
or exactly-once external side effects.
