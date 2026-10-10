# Provider stream events in the embedded SDK

This document describes how the Pi `provider_stream_event` extension event is
adapted to the headless Pith Go SDK. It covers the AI layer, the agent layer and
the session layer, and records the semantics that callers can rely on:
metadata, error propagation, ordering, backpressure and non-durability.

The adaptation is a native Go callback plus a transient `SessionEvent`. It is
**not** an implementation of the excluded npm/TypeScript extension host and does
not load `pi.on(...)` handlers.

## Where the event comes from

A provider adapter parses a wire event (an SSE chunk, a Codex WebSocket message
or a Bedrock event-stream frame) and normalizes it into Pi message content.
Before normalization, the adapter invokes the observer with:

- `data`: the parsed adapter event, exactly as decoded. It is owned by the
  adapter and must be treated as read-only.
- `model`: the `*aitypes.Model` of the **current request**.

The observer is optional at each layer. The next section shows each entry point.

## 1. AI options (provider layer)

`aitypes.ProviderRequestOptions.OnProviderStreamEvent` is the lowest-level knob.
It is embedded in `StreamOptions`, so every provider options type exposes it.

```go
import (
	"fmt"

	aitypes "github.com/minifish-org/pith/packages/ai/types"
	"github.com/minifish-org/pith/packages/ai/api"
)

func observeChunks(model *aitypes.Model, transcript *aitypes.TranscriptContext) *aitypes.AssistantMessageEventStream {
	options := &aitypes.SimpleStreamOptions{}
	options.OnProviderStreamEvent = func(data any, requestModel *aitypes.Model) error {
		fmt.Printf("provider=%s api=%s model=%s event=%T\n",
			requestModel.Provider, requestModel.Api, requestModel.Id, data)
		return nil // returning an error fails the request with that text
	}
	return api.OpenAICompletionsStreamSimple(model, transcript, options)
}
```

`BuildBaseOptions` copies the callback into the shared `StreamOptions`
unchanged; a nil callback stays nil so adapters can skip work.

## 2. Agent options (agent layer)

`agent.AgentOptions.OnProviderStreamEvent` is forwarded verbatim into every
request's `SimpleStreamOptions`, including initial prompts, tool-loop turns and
continuations.

```go
import (
	agentcore "github.com/minifish-org/pith/packages/agent"
	aitypes "github.com/minifish-org/pith/packages/ai/types"
)

a, err := agentcore.NewAgent(agentcore.AgentOptions{
	InitialState: &agentcore.AgentInitialState{Model: model},
	StreamFn:     streamFn,
	OnProviderStreamEvent: func(data any, requestModel *aitypes.Model) error {
		// The model is the request's model, not the agent's stored snapshot.
		return audit(requestModel.Provider, requestModel.Api, requestModel.Id, data)
	},
})
```

The observer is stored per `Agent` instance. Two agents in one process never
observe each other's requests.

## 3. Session options (SDK layer)

`codingagent.SessionOptions.OnProviderStreamEvent` is the embedded-SDK entry
point. The session installs a native observer on the agent it owns, so the
callback survives agent rebuilds and model changes (for example `SetModel`).

```go
import (
	codingagent "github.com/minifish-org/pith/packages/coding-agent"
	aitypes "github.com/minifish-org/pith/packages/ai/types"
)

session, err := codingagent.CreateAgentSession(codingagent.SessionOptions{
	Cwd:   "/path/to/project",
	Model: codingagent.ModelOptions{Model: model},
	OnProviderStreamEvent: func(data any, requestModel *aitypes.Model) error {
		return audit(requestModel.Provider, requestModel.Api, requestModel.Id, data)
	},
})
```

A returned error fails the logical request with its original text. The session
does not retry, replay or silently recover from an observer failure.

## 4. Native session subscribers

Every session publishes a transient `SessionEvent` of type
`provider_stream_event` for each parsed provider event, **whether or not**
`OnProviderStreamEvent` is configured. Native subscribers are the headless
replacement for `pi.on("provider_stream_event", ...)`.

```go
unsubscribe := session.Subscribe(func(event codingagent.SessionEvent) {
	if event.Type != codingagent.SessionEventProviderStreamEvent {
		return
	}
	// Serialized as "provider", "api", "model" and "data".
	fmt.Println(event.Provider, event.API, event.Model)
	use(event.Data) // read-only, transient
})
defer unsubscribe()
```

Serialized fields:

| Go field | JSON key | Meaning |
| --- | --- | --- |
| `Type` | `type` | Always `provider_stream_event`. |
| `Provider` | `provider` | Provider of the current request. |
| `API` | `api` | API/protocol of the current request. |
| `Model` | `model` | Model id of the current request. |
| `Data` | `data` | The same parsed adapter event as the callback. |

`Provider`, `API` and `Model` come from the model of the current request, never
from a construction-time snapshot. All other `SessionEvent` fields
(`ToolName`, `ToolCallID`, `Text`, `IsError`, `Message`) stay empty for this
event type.

## Ordering and locks

For a single parsed event the session delivers deterministically:

1. Subscribers registered through `Subscribe`, in subscription order.
2. The explicit `SessionOptions.OnProviderStreamEvent` callback.

This matches the upstream runner's "subscriptions, then the wired callback"
shape. The success path therefore delivers to both, which is what the frozen
acceptance tests assert. The failure path only requires that the callback error
propagates; it does not constrain delivery order.

All user code runs outside internal session locks: `Subscribe` callbacks run
outside `AgentSession.mu`, and the explicit callback runs after the subscriber
copy is taken. `AgentSession` serializes `Prompt`, so events are delivered in
stream order.

## Error and backpressure semantics

- **Synchronous consumption.** The observer is invoked on the goroutine that
  consumes the provider stream, before the event is normalized. A slow callback
  therefore applies backpressure to the whole stream: later events are not
  delivered and the request does not complete until the callback returns.
- **Errors are terminal.** A non-nil return value fails the logical request with
  the callback's original error text. It is never classified as a transient
  transport error, so there is no HTTP retry, WebSocket retry or SSE fallback,
  and no `RunResult` replay.
- **Errors are observable.** The session's own retry classification sees the
  failed assistant turn and returns a `TerminalRequestError`; subscribers are
  still notified of the event that triggered the failure.
- **No buffering.** Events are not queued. If a consumer needs asynchronous
  work, it must hand the payload to its own goroutine; the SDK does not add
  buffering or backpressure that the source does not have.

## Parsed data and SDK field limitations

- `Data` is `any`. Its concrete type depends on the adapter (typically
  `map[string]any` for JSON protocols). Callers must type-assert and tolerate
  unknown keys; adapters add fields over time.
- The provided `Provider`, `API`, `Model` fields are the identity of the current
  request. Extra request metadata (routing, cost, account) is only available
  inside `Data` when the adapter emits it.
- Not every adapter emits events. Adapters that cannot parse raw wire events
  (for example the Google/Bedrock code paths that batch differently) may
  observe fewer or different events. The observer is documented per adapter in
  `packages/ai/api`.
- `Data` is shared with the adapter. Do not mutate it in place; copy what you
  need to retain.

## Non-durable events

Provider stream events are transient and are **never** durably stored. They are
not appended to `RunResult.Messages`, the session JSONL, compaction input or
normal assistant content. Only the normalized assistant message is persisted.
Because of this, `SessionEvent.Data` must not be used to reconstruct history;
use the session manager and `RunResult` for the durable transcript.

## Receive limits and stream inactivity

Codex WebSocket reception defaults to 128 MiB per message. Set
`aitypes.StreamOptions.WebsocketMaxMessageBytes` to override that receive limit;
nil or a nonpositive value selects the default. Both new connections and cached
connections apply the current request's limit. This does not change the
provider's own request, image or context limits.

Mistral Conversations and Pi Messages no longer apply a default whole-request
deadline. They wait up to 30 seconds for response headers, then allow five
minutes of body inactivity; incoming bytes renew that window. An explicit
`ProviderRequestOptions.TimeoutMs` still bounds the whole request, and the
caller's abort signal remains effective. `StreamIdleTimeoutMs` overrides the
body inactivity window; a nonpositive value disables that window. These are
adapter-specific defaults, not a global HTTP-client policy.

## Native adaptation versus the excluded TS extension/TUI runtime

| Pi feature | Pith native adaptation |
| --- | --- |
| `pi.on("provider_stream_event", handler)` | `AgentSession.Subscribe` with `SessionEventProviderStreamEvent`. |
| `options.onProviderStreamEvent(data, model)` | `SessionOptions.OnProviderStreamEvent`, `AgentOptions.OnProviderStreamEvent`, `aitypes.ProviderRequestOptions.OnProviderStreamEvent`. |
| `ExtensionRunner.emit(...)` | Direct callback + subscriber dispatch; no runner, no extension host. |
| TUI rendering of provider events | Excluded; callers render the transient `SessionEvent` themselves. |
| npm/Git extension loading, `ExtensionAPI`/`ExtensionContext` | Excluded; no JS extension runtime is shipped. |

This is a Go API adaptation: there is no npm loader, no `ExtensionAPI`, no
`ExtensionRunner` and no TUI. Behavior is preserved only for the observable
provider-event notification path described above.
