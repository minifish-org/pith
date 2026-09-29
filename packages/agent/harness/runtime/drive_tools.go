// This file carries tool batch execution of
// packages/agent/src/harness/runtime/drive/tools.ts.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// Each call is prepared, admitted through the effect gate, executed and
// finalized before its result is staged. Staged results are placed by
// tool-placement in batch order.
package agentruntime

import (
	harnesscontext "github.com/minifish-org/pith/packages/agent/harness/context"
	harnessexecution "github.com/minifish-org/pith/packages/agent/harness/execution"
	harnesssession "github.com/minifish-org/pith/packages/agent/harness/session"
	harnesstypes "github.com/minifish-org/pith/packages/agent/harness/types"
	agenttypes "github.com/minifish-org/pith/packages/agent/types"
	aitypes "github.com/minifish-org/pith/packages/ai/types"
)

// toolInvocation is the durable identity of one runtime tool call.
type toolInvocation struct {
	lane         *Lane
	operationID  string
	turnID       string
	invocationID string
	ctx          harnesstypes.Context
}

func (i *toolInvocation) InvocationID() string { return i.invocationID }
func (i *toolInvocation) OperationID() string  { return i.operationID }
func (i *toolInvocation) TurnID() string       { return i.turnID }

// GetMemo reads one invocation-scoped memo.
func (i *toolInvocation) GetMemo(name string) (any, bool, error) {
	stored, ok, err := i.lane.Session.GetValue(harnesssession.OperationToolMemo(i.operationID, i.invocationID, name), i.ctx)
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, nil
	}
	return stored.Value, true, nil
}

// SetMemo writes one invocation-scoped memo.
func (i *toolInvocation) SetMemo(name string, value any) error {
	_, err := i.lane.Commit([]harnesstypes.Write{
		harnesssession.SetValue(harnesssession.OperationToolMemo(i.operationID, i.invocationID, name), value),
	}, i.ctx)
	return err
}

func toolDefinitions(tools []harnesstypes.AgentHarnessTool[any, any, any]) []harnessexecution.HarnessToolDefinition {
	definitions := make([]harnessexecution.HarnessToolDefinition, 0, len(tools))
	for index := range tools {
		tool := tools[index]
		definitions = append(definitions, adaptTool(tool))
	}
	return definitions
}

func adaptTool(tool harnesstypes.AgentHarnessTool[any, any, any]) harnessexecution.HarnessToolDefinition {
	definition := harnessexecution.HarnessToolDefinition{Tool: tool.Tool}
	if tool.PrepareArguments != nil {
		prepare := tool.PrepareArguments
		definition.PrepareArguments = func(args any) (any, error) { return prepare(args) }
	}
	if tool.Execute != nil {
		execute := tool.Execute
		definition.Execute = func(
			toolCallID string,
			args map[string]any,
			onUpdate func(partial agenttypes.AgentToolResult[any]),
			toolContext any,
			invocation harnessexecution.ToolInvocation,
			ctx harnesscontext.Context,
		) (agenttypes.AgentToolResult[any], error) {
			return execute(
				toolCallID,
				args,
				func(partial agenttypes.AgentToolResult[any], _ *harnesstypes.AgentHarnessToolUpdateOptions) {
					if onUpdate != nil {
						onUpdate(partial)
					}
				},
				toolContext,
				&invocationAdapter{inner: invocation},
				ctx,
			)
		}
	}
	return definition
}

// invocationAdapter bridges the execution-local invocation contract to the
// shared harness contract.
type invocationAdapter struct {
	inner harnessexecution.ToolInvocation
}

func (a *invocationAdapter) InvocationID() string { return a.inner.InvocationID() }
func (a *invocationAdapter) OperationID() string  { return a.inner.OperationID() }
func (a *invocationAdapter) TurnID() string       { return a.inner.TurnID() }
func (a *invocationAdapter) GetMemo(name string) (harnesstypes.JsonValue, bool, error) {
	return a.inner.GetMemo(name)
}
func (a *invocationAdapter) SetMemo(name string, value harnesstypes.JsonValue) error {
	return a.inner.SetMemo(name, value)
}

// RunTools executes the planned tool calls of one batch and places ready
// results.
func RunTools(lane *Lane, drive *harnesstypes.Drive, tools *harnesstypes.ToolsOperation) (harnesstypes.ProcedureResult, error) {
	sources, err := ReadToolBatchSource(lane, drive, tools.Batch)
	if err != nil {
		return harnesstypes.ProcedureResult{}, err
	}
	if scope := harnesstypes.OperationScopeOf(tools); scope.Control.Status == "cancel_requested" {
		// Cancellation still materializes every already-staged result.
		if err := MaterializeReady(lane, drive, tools, sources, true); err != nil {
			return harnesstypes.ProcedureResult{}, err
		}
		return procedureContinue(), nil
	}
	config := lane.ReadConfig()
	definitions := toolDefinitions(config.Tools)
	toolContext := any(nil)
	if config.ToolContext != nil {
		if config.ToolContext.Static != nil {
			toolContext = *config.ToolContext.Static
		} else if config.ToolContext.Resolve != nil {
			resolved, err := config.ToolContext.Resolve(drive.Context)
			if err != nil {
				return harnesstypes.ProcedureResult{}, err
			}
			toolContext = resolved
		}
	}

	calls := append([]harnesstypes.ToolCall{}, tools.Batch.Calls...)
	for index := range calls {
		call := calls[index]
		if call.Status != "planned" {
			continue
		}
		source, err := ToolCallFor(sources, call)
		if err != nil {
			return harnesstypes.ProcedureResult{}, err
		}
		invocation := &toolInvocation{
			lane:         lane,
			operationID:  drive.OperationID,
			turnID:       tools.Batch.TurnID,
			invocationID: call.ResultEntryID,
			ctx:          drive.Context,
		}
		prepared, immediate := harnessexecution.PrepareToolCall(source, definitions)
		var cleared harnessexecution.ClearedToolCall
		if immediate != nil {
			cleared = harnessexecution.ClearedToolCall{ToolCall: source}
		} else {
			var decision *harnessexecution.BeforeToolDecision
			if lane.Hooks != nil {
				hookResult, err := lane.Hooks.RunToolWithGate("before_tool", map[string]any{
					"toolCallId": source.Id,
					"toolName":   source.Name,
				}, drive.Gate, drive.Context)
				if err != nil {
					return harnesstypes.ProcedureResult{}, err
				}
				decision = beforeToolDecision(hookResult)
			}
			var blocked *harnessexecution.ImmediateToolOutcome
			cleared, blocked = harnessexecution.ApplyBeforeToolDecision(prepared, decision)
			immediate = blocked
		}
		var finalized harnessexecution.FinalizedToolCall
		if immediate != nil {
			finalized = harnessexecution.FinalizeToolCall(cleared, harnessexecution.ExecutedToolCall{Result: immediate.Result, IsError: immediate.IsError}, nil)
		} else {
			executed, err := harnessexecution.ExecuteToolCall(cleared, drive.Gate, nil, toolContext, invocation, drive.Context)
			if err != nil {
				return harnesstypes.ProcedureResult{}, err
			}
			finalized = harnessexecution.FinalizeToolCall(cleared, executed, nil)
		}
		message := harnessexecution.CreateToolResultMessage(finalized)
		agentMessage := agenttypes.NewAgentMessageFromMessage(aitypes.NewToolResultMessageVariant(message))
		if _, err := lane.Commit([]harnesstypes.Write{
			harnesssession.SetValue(harnesssession.PendingEntry(call.ResultEntryID), harnesstypes.PendingEntry{Type: "message", Payload: &agentMessage}),
		}, drive.Context); err != nil {
			return harnesstypes.ProcedureResult{}, err
		}
		calls[index].Status = "outcome_ready"
		calls[index].Terminate = &finalized.Terminate
	}

	if err := lane.updateBatch(drive, tools, calls); err != nil {
		return harnesstypes.ProcedureResult{}, err
	}
	updated, err := currentTools(lane, drive)
	if err != nil {
		return harnesstypes.ProcedureResult{}, err
	}
	if err := MaterializeReady(lane, drive, updated, sources, false); err != nil {
		return harnesstypes.ProcedureResult{}, err
	}
	return procedureContinue(), nil
}

func beforeToolDecision(result any) *harnessexecution.BeforeToolDecision {
	if result == nil {
		return nil
	}
	if decision, ok := result.(*harnessexecution.BeforeToolDecision); ok {
		return decision
	}
	return nil
}

func (l *Lane) updateBatch(drive *harnesstypes.Drive, tools *harnesstypes.ToolsOperation, calls []harnesstypes.ToolCall) error {
	_, err := l.SettleOperation(tools, func(
		_ *harnesstypes.RuntimeLaneState,
		_ harnesstypes.OperationState,
		_ harnesstypes.OperationMeta,
		_ harnesstypes.SessionReader,
	) (harnesstypes.OperationCommand[any], error) {
		next := WithToolBatch(tools, harnesstypes.ToolBatch{
			AssistantEntryID: tools.Batch.AssistantEntryID,
			Configuration:    tools.Batch.Configuration,
			TurnID:           tools.Batch.TurnID,
			Calls:            calls,
		})
		result := procedureContinue()
		return harnesstypes.OperationCommand[any]{
			Kind:           harnesstypes.OperationCommandCommit,
			OperationState: next,
			Result:         boxProcedure(result),
		}, nil
	}, drive.Context)
	return err
}

func currentTools(lane *Lane, drive *harnesstypes.Drive) (*harnesstypes.ToolsOperation, error) {
	operation, err := currentOperation(lane, drive)
	if err != nil {
		return nil, err
	}
	tools, ok := operation.State.(*harnesstypes.ToolsOperation)
	if !ok {
		return nil, &laneInvariantError{message: "Drive no longer owns a tools leaf"}
	}
	return tools, nil
}
