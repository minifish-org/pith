# Pi 1.0 SDK migration target

Accepted starting upstream revision: `002fc8385268300ca91a5fc95f935c2afbbdac02`.
Pinned release: `v1.0.0`, commit `a13d35a742c6ef8462812a28fbe1d8c8b7431c32`.
This is an increment, not a new whole-repository translation. Pi Durable is a
separate subsequent project scope and is not part of this acceptance.

## Required common scope

- Audit the complete cumulative Git diff, including paths outside existing watch roots.
- Update existing native AI/provider/model/auth APIs, agent tool execution and
  embedded coding-agent SDK behavior for the pinned release.
- Preserve idiomatic Go APIs and existing consumer compatibility wherever possible.
- Keep historical source snapshots, contracts, conformance judges and receipts intact.
- No CGO. Mature third-party dependencies are permitted after explicit dependency
  review; the generator cannot silently change go.mod or go.sum.
- The operator runs paid DeepSeek migration after all batches are prepared and
  native preflight passes. Planning must not call the implementation model.

## New capabilities requiring separate ownership

The pinned source introduces `packages/mcp` and `packages/codemode`; these are
outside the original watch roots. SDK support for virtual models, deferred tools,
model operations and cache warming must also be reviewed as capabilities, not
inferred from file overlap. Do not claim full 1.0 SDK parity solely because the
original mapped modules compiled.

Codemode executes untrusted model-generated JavaScript. Engine choice must
preserve cancellation, resource bounds, asynchronous tool calls and capability
isolation without requiring Node on the deployed customer's machine. Do not
substitute an unrestricted host-language evaluator or silently weaken isolation.

## Deletions and compatibility

Upstream removes the experimental harness from pi-agent-core. Existing Pith
public harness/SDK APIs may still be consumed by native applications. Review their
actual dependencies and retain compatibility facades where needed; do not remove
Go files merely because the TS source disappeared. Exclude the new Durable-backed
experimental client/server/TUI implementation from this increment.

## Acceptance priorities

Independent judges must cover provider wire behavior, model type dispatch,
catalog/cache validators, raw event preservation, nested tool permission hooks,
structured result/error propagation, native SDK lifecycle, and selected
MCP transport/auth plus codemode isolation and virtual model/deferred tool behavior.
Storage recovery/conversation/task guarantees of pi-durable belong to the later plan.

Selected scope: full native embedded SDK, including MCP, Codemode, virtual models
and deferred tools. Nine executable stages are defined in workflow.json.
