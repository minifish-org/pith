# Embedded SDK provider event adaptation

Upstream creates a `ProviderStreamEvent` extension event and wires the agent
observer into the extension runner. Pith is an embedded Go SDK without the
excluded npm/TypeScript extension runtime. Preserve the observable behavior with
native callback and session subscription interfaces, not an npm extension loader.

Add `SessionOptions.OnProviderStreamEvent func(any, *aitypes.Model) error`.
Wire it through the per-session native agent and preserve it across agent rebuilds
and model changes. Publish a transient SessionEvent with `Type` equal to
`provider_stream_event`, fields `Provider`, `API`, `Model` (strings serialized as
`provider`, `api`, `model`) and `Data any` (serialized as `data`). Data is the same
parsed adapter event as the AI callback and is read-only. Model identity must be
from the current request, not a stale construction-time model.

Native session subscribers receive these events even if no explicit callback is
configured. Existing Subscribe/unsubscribe behavior applies. This is a Go API
adaptation, not an implementation of JS extension host behavior. If the explicit
callback fails, propagate its original error text; fail the logical request and
avoid replay or silent recovery. Use a documented deterministic ordering between
subscriptions and the explicit callback. The frozen judge requires successful
requests to deliver both, but intentionally leaves failure-path delivery order
open. Invoke user code outside internal session locks. Never append raw events to
RunResult.Messages, session JSONL, compaction input or normal assistant content.

Frozen tests use an injected fake provider: callback metadata and model identity,
headless subscription metadata, explicit callback failure, no accidental request
replay, no transcript leakage, subscriber delivery without a configured callback,
and unsubscription. Write additional candidate self-tests in
`provider_stream_events_test.go` for session isolation and rebuild/model change.
Keep all existing SDK tests and historical receipts unchanged.

Create `docs/sdk/provider-stream-events.md` with compilable-style Go examples for
AI options, AgentOptions, SessionOptions and Subscribe; error and backpressure
semantics; parsed data/SDK field limitations; non-durable events; native adaptation
versus excluded TS extension/TUI runtime. No new dependencies and no CGO.
