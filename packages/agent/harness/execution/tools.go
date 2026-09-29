// This file carries the tool execution primitives of
// packages/agent/src/harness/execution/tools.ts.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// Argument preparation/coercion and final schema validation are separate
// phases. A hook may replace the prepared arguments, which are revalidated
// before the external effect is admitted through the gate.
package harnessexecution

import (
	"encoding/json"
	"errors"

	harnesscontext "github.com/minifish-org/pith/packages/agent/harness/context"
	agenttypes "github.com/minifish-org/pith/packages/agent/types"
	aitypes "github.com/minifish-org/pith/packages/ai/types"
	aiutils "github.com/minifish-org/pith/packages/ai/utils"
)

// ToolInvocation is the durable identity of one logical tool call.
type ToolInvocation interface {
	InvocationID() string
	OperationID() string
	TurnID() string
	GetMemo(name string) (any, bool, error)
	SetMemo(name string, value any) error
}

// HarnessToolDefinition is the executable slice of an AgentHarness tool.
type HarnessToolDefinition struct {
	Tool             aitypes.Tool
	PrepareArguments func(args any) (any, error)
	Execute          func(
		toolCallID string,
		args map[string]any,
		onUpdate func(partial agenttypes.AgentToolResult[any]),
		toolContext any,
		invocation ToolInvocation,
		ctx harnesscontext.Context,
	) (agenttypes.AgentToolResult[any], error)
}

// PreparedToolCall is a tool call whose tool exists and whose prepared
// arguments passed validation.
type PreparedToolCall struct {
	ToolCall aitypes.ToolCall
	Tool     HarnessToolDefinition
	Args     map[string]any
}

// ImmediateToolOutcome is a synthetic result produced without crossing the
// external tool-effect boundary.
type ImmediateToolOutcome struct {
	ToolCall  aitypes.ToolCall
	Result    agenttypes.AgentToolResult[any]
	IsError   bool
	Terminate bool
}

// BeforeToolBlock is a hook block decision.
type BeforeToolBlock struct {
	Reason    string
	Terminate bool
}

// BeforeToolDecision is the aggregated before-tool hook decision.
type BeforeToolDecision struct {
	Args  map[string]any
	Block *BeforeToolBlock
}

// ClearedToolCall is a prepared call cleared for durable intent publication.
type ClearedToolCall struct {
	ToolCall aitypes.ToolCall
	Tool     HarnessToolDefinition
	Args     map[string]any
}

// ExecutedToolCall is raw phase-two tool output before after-tool patching.
type ExecutedToolCall struct {
	Result  agenttypes.AgentToolResult[any]
	IsError bool
}

// AfterToolPatch is the aggregated after-tool hook patch.
type AfterToolPatch struct {
	Content   []aitypes.ContentBlock
	Details   any
	HasDetail bool
	IsError   *bool
	Usage     *aitypes.Usage
	Terminate *bool
}

// FinalizedToolCall is the final tool output ready to become a durable
// tool-result message.
type FinalizedToolCall struct {
	ToolCall  aitypes.ToolCall
	Result    agenttypes.AgentToolResult[any]
	IsError   bool
	Terminate bool
}

func createErrorToolResult(message string) agenttypes.AgentToolResult[any] {
	return agenttypes.AgentToolResult[any]{
		Content: []aitypes.ContentBlock{aitypes.TextBlock(message)},
	}
}

func immediateError(call aitypes.ToolCall, message string, terminate bool) *ImmediateToolOutcome {
	return &ImmediateToolOutcome{
		ToolCall:  call,
		Result:    createErrorToolResult(message),
		IsError:   true,
		Terminate: terminate,
	}
}

func rawArgumentsMap(raw json.RawMessage) map[string]any {
	if len(raw) == 0 {
		return map[string]any{}
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil || decoded == nil {
		return map[string]any{}
	}
	return decoded
}

// PrepareToolCall resolves a tool, applies deterministic argument preparation
// and validates the result. A missing tool or validation failure yields an
// immediate error outcome instead of throwing.
func PrepareToolCall(call aitypes.ToolCall, tools []HarnessToolDefinition) (PreparedToolCall, *ImmediateToolOutcome) {
	var selected *HarnessToolDefinition
	for index := range tools {
		if tools[index].Tool.Name == call.Name {
			selected = &tools[index]
			break
		}
	}
	if selected == nil {
		encoded, _ := json.Marshal(call.Name)
		return PreparedToolCall{}, immediateError(call, "Tool "+string(encoded)+" is unavailable", false)
	}
	preparedCall := call
	if selected.PrepareArguments != nil {
		prepared, err := selected.PrepareArguments(rawArgumentsMap(call.Arguments))
		if err != nil {
			return PreparedToolCall{}, immediateError(call, err.Error(), false)
		}
		if encoded, err := json.Marshal(prepared); err == nil {
			preparedCall.Arguments = encoded
		}
	}
	validated, err := aiutils.ValidateToolArguments(selected.Tool, preparedCall)
	if err != nil {
		return PreparedToolCall{}, immediateError(call, err.Error(), false)
	}
	args, ok := validated.(map[string]any)
	if !ok {
		args = rawArgumentsMap(preparedCall.Arguments)
	}
	return PreparedToolCall{ToolCall: call, Tool: *selected, Args: args}, nil
}

// ApplyBeforeToolDecision applies an explicit hook decision and revalidates
// replacement arguments.
func ApplyBeforeToolDecision(prepared PreparedToolCall, decision *BeforeToolDecision) (ClearedToolCall, *ImmediateToolOutcome) {
	if decision != nil && decision.Block != nil {
		return ClearedToolCall{}, immediateError(prepared.ToolCall, decision.Block.Reason, decision.Block.Terminate)
	}
	if decision == nil || decision.Args == nil {
		return ClearedToolCall{ToolCall: prepared.ToolCall, Tool: prepared.Tool, Args: prepared.Args}, nil
	}
	encoded, err := json.Marshal(decision.Args)
	if err != nil {
		return ClearedToolCall{}, immediateError(prepared.ToolCall, err.Error(), false)
	}
	candidate := prepared.ToolCall
	candidate.Arguments = encoded
	validated, err := aiutils.ValidateToolArguments(prepared.Tool.Tool, candidate)
	if err != nil {
		return ClearedToolCall{}, immediateError(prepared.ToolCall, err.Error(), false)
	}
	args, ok := validated.(map[string]any)
	if !ok {
		args = decision.Args
	}
	return ClearedToolCall{ToolCall: prepared.ToolCall, Tool: prepared.Tool, Args: args}, nil
}

// ExecuteToolCall executes one cleared external tool effect, converting
// expected tool errors into error output. The admitted context observes the
// gate signal.
func ExecuteToolCall(
	call ClearedToolCall,
	gate Gate,
	onUpdate func(partial agenttypes.AgentToolResult[any]),
	toolContext any,
	invocation ToolInvocation,
	ctx harnesscontext.Context,
) (ExecutedToolCall, error) {
	if call.Tool.Execute == nil {
		return ExecutedToolCall{Result: createErrorToolResult("Tool " + call.ToolCall.Name + " has no executor"), IsError: true}, nil
	}
	acceptingUpdates := true
	var executed ExecutedToolCall
	admitErr := gate.Admit(func() error {
		admitted := harnesscontext.WithAbortSignal(gateSignalContext(gate), ctx)
		select {
		case <-admitted.Done():
			return admitted.Err()
		default:
		}
		result, err := call.Tool.Execute(
			call.ToolCall.Id,
			call.Args,
			func(partial agenttypes.AgentToolResult[any]) {
				if acceptingUpdates && onUpdate != nil {
					onUpdate(partial)
				}
			},
			toolContext,
			invocation,
			admitted,
		)
		if err != nil {
			executed = ExecutedToolCall{Result: createErrorToolResult(err.Error()), IsError: true}
			return nil
		}
		executed = ExecutedToolCall{Result: result, IsError: false}
		return nil
	})
	acceptingUpdates = false
	if admitErr != nil {
		return ExecutedToolCall{}, admitErr
	}
	return executed, nil
}

// gateSignalContext adapts the gate's done channel into a context usable by
// WithAbortSignal. The gate exposes only a channel, so the signal is wrapped as
// a context that is cancelled when the channel closes.
func gateSignalContext(gate Gate) harnesscontext.Context {
	ctx, cancel := harnesscontext.WithCancel(harnesscontext.BackgroundContext)
	go func() {
		<-gate.Signal()
		cancel()
	}()
	return ctx
}

// FinalizeToolCall applies an after-tool patch field by field.
func FinalizeToolCall(call ClearedToolCall, executed ExecutedToolCall, patch *AfterToolPatch) FinalizedToolCall {
	result := executed.Result
	if patch != nil {
		if patch.Content != nil {
			result.Content = patch.Content
		}
		if patch.HasDetail {
			result.Details = patch.Details
		}
		if patch.Usage != nil {
			result.Usage = patch.Usage
		}
		if patch.Terminate != nil {
			result.Terminate = patch.Terminate
		}
	}
	isError := executed.IsError
	if patch != nil && patch.IsError != nil {
		isError = *patch.IsError
	}
	terminate := result.Terminate != nil && *result.Terminate
	return FinalizedToolCall{ToolCall: call.ToolCall, Result: result, IsError: isError, Terminate: terminate}
}

// ToolResultFromMessage reconstructs the canonical tool result represented by
// a staged transcript message.
func ToolResultFromMessage(message aitypes.ToolResultMessage, terminate bool) agenttypes.AgentToolResult[any] {
	result := agenttypes.AgentToolResult[any]{
		Content: message.Content,
	}
	if len(message.Details) > 0 {
		var details any
		if json.Unmarshal(message.Details, &details) == nil {
			result.Details = details
		}
	}
	if message.Usage != nil {
		result.Usage = message.Usage
	}
	if terminate {
		yes := true
		result.Terminate = &yes
	}
	return result
}

// CreateToolResultMessage converts finalized tool output to the
// provider-facing transcript message.
func CreateToolResultMessage(call FinalizedToolCall) aitypes.ToolResultMessage {
	content := call.Result.Content
	if content == nil {
		content = []aitypes.ContentBlock{}
	}
	message := aitypes.ToolResultMessage{
		Role:       "toolResult",
		ToolCallId: call.ToolCall.Id,
		ToolName:   call.ToolCall.Name,
		Content:    content,
		IsError:    call.IsError,
	}
	if call.Result.Details != nil {
		if encoded, err := json.Marshal(call.Result.Details); err == nil {
			message.Details = encoded
		}
	}
	if call.Result.Usage != nil {
		message.Usage = call.Result.Usage
	}
	return message
}

var _ = errors.New
