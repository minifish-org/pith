# Targeted embedded-SDK gap audit

Reviewed on 2026-10-04 against the accepted Pi revision
`a13d35a742c6ef8462812a28fbe1d8c8b7431c32`. This is a targeted audit of the
embedded session's input and composition paths, not a certification of all Pi
providers or the complete TypeScript project. Export counts and source mappings
can miss options that exist only as unused data types.

## Repaired

| Gap | Repair | Regression evidence |
| --- | --- | --- |
| `PromptOptions.Images` existed but `AgentSession.Prompt` could not accept it | Wire images into the unified `Prompt` entry point | Actual local HTTP image payload, transient retry, detached snapshots and JSONL reopen |
| Streaming behavior and expansion flags were unused | Add image-aware queue options and wire loaded skill/template expansion | Both steering/follow-up paths, template toggle, skill/unknown commands, cancellation and busy checks |
| Rebuilding an agent discarded undelivered messages | Copy both complete pending queues and their delivery modes into the replacement | Multiple images in both queues survive retry/model change in original order |
| `CreateAgentSessionFromServices` discarded the prepared resource set and default settings | Use a detached prepared resource snapshot, with explicit settings overriding services settings | Deleted source template still usable; caller mutation cannot change the session |
| Services construction could not pass virtual routers or stream observers | Expose and forward the same optional configuration as direct creation | Real route selection and observer callback through services |

See [prompt-inputs.md](./prompt-inputs.md) for usage and adaptation details.

## Remaining useful SDK work

| Priority | Area | Verified boundary and value |
| --- | --- | --- |
| High for image-heavy applications | Automatic image normalization/resize | Pi normalizes prompt images before history; Pith currently passes bytes unchanged. Read tools accept a caller `ImageProcessor`, but there is no default session normalizer. This matters for provider limits and large images. |
| Medium for host UIs | Structured session lifecycle and queue state | Pi publishes `queue_update`, `agent_settled` and compaction start/end data. Pith declares some event names but does not publish those session events. The present Go host can use run completion and its own status, but reusable SDK events would reduce that duplication. |
| Medium for configurable agents | Native input and pre-run hooks | The TS extension runner can transform/handle input and change per-run instructions/tools. Pith exposes native tool hooks and provider stream observers, not a complete equivalent of the input/pre-run lifecycle. A small native Go hook API could provide useful behavior without running JS. |
| Only when a provider use case needs it | Deferred provider response operations | `providerStreamsAdapter` and `streamFnAdapter` return explicit unsupported errors for deferred fetch/cancel. This does not affect the normal synchronous session loop. |

These are candidates for separate behavior contracts and regressions, not
promises that every missing TypeScript host feature will be implemented.

## Intentional product boundaries

The TUI, TS/JS extension execution, npm extension installation and Pi interactive
CLI parity remain excluded. A web-search tool, desktop attachment picker,
computer-use implementation, sandbox and desktop distribution are host/product
features; their absence is not evidence of a missed Pi agent-core port. Pith Desk
has its own image UI/policy restrictions and must separately adopt the new SDK.

Undelivered input queues are in memory. Persisted transcript images survive
reopen, but pending queue durability and exactly-once external actions are not
claimed. Optional Durable is a separate runtime with its own recovery contract.
