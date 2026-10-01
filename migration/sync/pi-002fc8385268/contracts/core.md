# Native agent observer forwarding

Read upstream `agent.ts`, `agent-loop.ts`, `types.ts` and the exact old/new agent
regression test. This increment adds an optional observer, not a new agent loop.

Expose `AgentOptions.OnProviderStreamEvent func(any, *aitypes.Model) error`.
Store it per agent instance and forward it through every current request's loop
configuration into `aitypes.SimpleStreamOptions`. Callback errors must reach the
same provider/agent failure path rather than being swallowed by forwarding.
Preserve the existing initial state, steering, continuations, tools and callbacks.
No process-global observer or observer registry. The callback remains optional.
Use the AI contract's synchronous order and parsed-data semantics.

The frozen judge invokes two prompts using an injected fake provider and checks
that both requests receive the observer with unchanged model identity and raw
request metadata. Add candidate self-tests in `provider_stream_events_test.go`
for two independent agent instances, continuation/tool requests and nil callback
compatibility. Do not rewrite old tests or historical conformance mappings.
The agent loop and Go type file are writable dependencies only if implementation
requires a change; unchanged files must not be claimed as changed output.
No Pico3 or Chord state replication is included.
