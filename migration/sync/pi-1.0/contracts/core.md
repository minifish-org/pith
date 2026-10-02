# Pi 1.0 core and nested tool execution

Port agent.ts/agent-loop.ts/types.ts and all changed tests. Preserve agent instance
isolation, queue ordering, continuation, steering, cancellation and provider events.
Add thinkingLevel to final assistant metadata. The requested level (including off)
is recorded, independent of adapter-specific mapped effort.

Expose `agent.RunToolCall(call aitypes.ToolCall, options agenttypes.RunToolCallOptions)
agenttypes.ToolCallOutcome`. Options contain Tools []AgentTool[any,any],
AssistantMessage aitypes.AssistantMessage, Context AgentContext, Signal <-chan struct{},
OnUpdate AgentToolUpdateCallback[any], BeforeToolCall and AfterToolCall matching
AgentLoopConfig hook signatures. Outcome contains ToolCall, Result AgentToolResult[any]
and IsError bool. It emits no agent events and inserts no transcript messages.
The main agent loop and native nested tools share this execution primitive.

Preparation/schema validation precede BeforeToolCall, then Execute then AfterToolCall.
Blocked/unknown/invalid calls do not execute. Errors become tool error outcomes.
Explicit execute-result IsError and StructuredContent are preserved. OutputSchema
is declared but output is not silently rewritten. After-hook content replacement
without StructuredContent discards stale structured data; explicit replacement
keeps it. Preserve all original call IDs/arguments/details/usage/terminate and
update semantics, including Go errors and cancellation.

Upstream removed the experimental agent harness. Retain existing Pith harness,
Chord compatibility and built-in tool APIs; this increment does not delete them
or claim parity with Durable. Update compatibility adapters only where needed for
new AI/core fields. Do not migrate removed pico3 experimental code.

Independent tests cover successful execution, hook denial, structured-output
replacement and real tool errors. Candidate self-tests port the source loop
regressions, including concurrent updates, prepare/validate failures, no-event
nested calls, before/after cancellation and thinking-level recording.
