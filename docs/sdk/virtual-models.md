# Virtual models

A virtual model is a catalog entry that routes each logical request to a physical
model. The session selection (`SessionOptions.Model`, a model change) may name the
virtual entry, but every request below the routing step only ever sees the physical
model a provider can stream. A virtual model never reaches a provider: its API id
is `pi-virtual` (`codingagent.VirtualModelAPI`).

The behavior is a Go port of Pi's `packages/coding-agent/src/core/virtual-models.ts`.

## Defining a router

```go
definition := codingagent.VirtualModelDefinition{
    Provider:       "router",
    ID:             "smart-cheap",
    Name:           "Smart Cheap",
    ThinkingLevels: []aitypes.ModelThinkingLevel{aitypes.ThinkingOff, aitypes.ThinkingHigh},
    ContextWindow:  128000,
    MaxTokens:      8192,
    Route: func(ctx context.Context, request codingagent.ModelRouteRequest) (codingagent.ModelRoute, error) {
        return codingagent.ModelRoute{Model: physical, ThinkingLevel: aitypes.ThinkingHigh}, nil
    },
}
virtual := codingagent.CreateVirtualModel(definition)

session, err := codingagent.CreateAgentSession(codingagent.SessionOptions{
    Cwd:           cwd,
    Model:         codingagent.ModelOptions{Model: &virtual},
    VirtualModels: []codingagent.VirtualModelDefinition{definition},
})
```

`CreateVirtualModel` builds the catalog entry. Non-empty `ThinkingLevels`
populate `ThinkingLevelMap` and mark the entry as reasoning; unsupported levels
map to `nil`. A zero `ThinkingLevels` defaults to `["off"]`. The entry must be
registered in `SessionOptions.VirtualModels` or a request fails with "not
registered" instead of reaching a provider.

## Route request

`ModelRouteRequest` describes why the router is being called:

| Field | Meaning |
| --- | --- |
| `Model` | The selected virtual model. |
| `ThinkingLevel` | The selected thinking level. |
| `Reason` | `user`, `continuation`, `retry` or `direct`. |
| `Previous` | The physical model and level of the latest successful response. |
| `Failed` | The failed request for a retry; nil otherwise (`Reason == retry`). |
| `State` | The router state stored on this branch; empty before any state. |
| `Messages` | The conversation for this request, including system messages. |

Reasons: the first request after a user message is `user`; every other request in
the agent loop (including after a tool result) is `continuation`; an automatic
retry is `retry`; a request outside the agent loop is `direct`.

`ModelRoute` returns the physical `Model`, the `ThinkingLevel` and optional
`State`. If `ThinkingLevel` is empty it defaults to `off`.

## Validation

Before a routed request is streamed, the session rejects:

- a route to another virtual model (virtual-to-virtual routing is not allowed);
- a route whose physical model has an empty provider or id ("unavailable physical
  model");
- a callback error, which fails the request with its original text.

## Routing state

State is persisted as a `pi.virtual-model-state` custom entry on the active
session branch. The payload records the provider, model id and the raw state JSON.
On the next request `ModelRouteRequest.State` is the stored value, so a resumed
session keeps its routing memory. State is written only when it changes and is
ignored for direct requests, which neither read nor write it.

`GetVirtualModelState(branch, provider, modelID)` reads the latest state for a
virtual model from a branch. `GetBranchSelection(branch, getModel)` reports the
provider/model a branch currently selects; a virtual model change holds until the
next change because responses name the physical models it routed to.

## Thinking levels

`ThinkingLevels` is the set of levels the router advertises. The session forwards
the selected level in `ModelRouteRequest.ThinkingLevel`; the router decides what
it means. `CreateVirtualModel` records the mapping in `ThinkingLevelMap`.

## Limits and caveats

- A router is Go code the caller owns; the SDK never invents a route.
- The router sees the conversation, so keep routing decisions pure and fast.
- Passing the routing tests proves the covered contract: validation, reasons,
  state round-trip and that a virtual model never reaches a provider. It does not
  claim any particular routing policy.

See `examples/virtual-model` for a runnable offline example.
