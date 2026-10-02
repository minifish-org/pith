// This file carries the nested tool-call execution primitive of
// packages/agent/src/agent-loop.ts (runToolCall).
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// The main agent loop and native nested tools share this primitive so that
// argument preparation, schema validation, the before/after hooks and result
// finalization behave identically for model-issued and nested calls. It emits no
// agent events and inserts no transcript messages.
package agent

import (
	agenttypes "github.com/minifish-org/pith/packages/agent/types"
	aitypes "github.com/minifish-org/pith/packages/ai/types"
)

// RunToolCall runs one tool call through the same steps as a model-issued call:
// argument preparation, schema validation, BeforeToolCall, execution and
// AfterToolCall. It emits no agent events and inserts no transcript messages.
// Tools that call other tools use this so the hooks (for example permission
// checks) apply to those calls too.
//
// It never reports tool failures as a Go error: unknown tools, validation
// errors, blocked calls and execution errors come back as an outcome with
// IsError true.
func RunToolCall(call aitypes.ToolCall, options agenttypes.RunToolCallOptions) agenttypes.ToolCallOutcome {
	hooks := toolCallHooks{before: options.BeforeToolCall, after: options.AfterToolCall}

	preparation, immediate := prepareToolCall(
		options.Context,
		options.AssistantMessage,
		call,
		hooks,
		options.Signal,
		options.Tools,
	)
	if immediate != nil {
		return agenttypes.ToolCallOutcome{ToolCall: call, Result: immediate.result, IsError: immediate.isError}
	}

	onUpdate := options.OnUpdate
	sink := func(partial agenttypes.AgentToolResult[any]) error {
		if onUpdate != nil {
			onUpdate(partial)
		}
		return nil
	}
	executed, err := executePreparedToolCall(*preparation, options.Signal, sink)
	if err != nil {
		return agenttypes.ToolCallOutcome{ToolCall: call, Result: createErrorToolResult(err.Error()), IsError: true}
	}

	finalized, err := finalizeExecutedToolCall(options.Context, options.AssistantMessage, *preparation, executed, hooks, options.Signal)
	if err != nil {
		return agenttypes.ToolCallOutcome{ToolCall: call, Result: createErrorToolResult(err.Error()), IsError: true}
	}
	return agenttypes.ToolCallOutcome{ToolCall: finalized.toolCall, Result: finalized.result, IsError: finalized.isError}
}
