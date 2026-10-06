// Package agenttypes is the Go port of packages/agent/src/types.ts.
//
// This is a Go port of Pi at revision
// f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// AgentMessage models the ordered transcript element as a discriminated record:
// a standard provider message or an application-defined custom message. Unknown
// custom payloads are kept verbatim in Raw so no content is silently dropped.
// Optional provider options use pointers so absent, null and zero stay distinct.
package agenttypes

import (
	"encoding/json"
	"fmt"

	aitypes "github.com/minifish-org/pith/packages/ai/types"
)

// StreamFn is the stream function used by the agent loop.
type StreamFn func(model *aitypes.Model, context *aitypes.TranscriptContext, options *aitypes.SimpleStreamOptions) *aitypes.AssistantMessageEventStream

// ToolExecutionMode configures how tool calls from one assistant message run.
type ToolExecutionMode string

// Tool execution modes.
const (
	ToolExecutionSequential ToolExecutionMode = "sequential"
	ToolExecutionParallel   ToolExecutionMode = "parallel"
)

// QueueMode controls how many queued user messages are injected per drain.
type QueueMode string

// Queue modes.
const (
	QueueModeAll        QueueMode = "all"
	QueueModeOneAtATime QueueMode = "one-at-a-time"
)

// AgentToolCall is a tool-call content block emitted by an assistant message.
type AgentToolCall = aitypes.ToolCall

// BeforeToolCallResult is returned from the beforeToolCall hook.
type BeforeToolCallResult struct {
	Block     *bool   `json:"block,omitempty"`
	Reason    *string `json:"reason,omitempty"`
	Terminate *bool   `json:"terminate,omitempty"`
}

// AfterToolCallResult partially overrides an executed tool result.
//
// Merge semantics are field by field. Content replaces the full content array,
// Details the full details payload, IsError the error flag, Usage the usage and
// Terminate the early-termination hint. StructuredContent replaces the machine
// readable payload; when Content is provided without StructuredContent the
// structured payload is dropped because it may no longer match the content.
// Other omitted fields keep the original executed tool result values.
type AfterToolCallResult struct {
	Content           []aitypes.ContentBlock `json:"content,omitempty"`
	Details           any                    `json:"details,omitempty"`
	StructuredContent json.RawMessage        `json:"structuredContent,omitempty"`
	IsError           *bool                  `json:"isError,omitempty"`
	Usage             *aitypes.Usage         `json:"usage,omitempty"`
	Terminate         *bool                  `json:"terminate,omitempty"`
}

// BeforeToolCallContext is passed to the beforeToolCall hook.
type BeforeToolCallContext struct {
	AssistantMessage aitypes.AssistantMessage `json:"assistantMessage"`
	ToolCall         AgentToolCall            `json:"toolCall"`
	Args             any                      `json:"args"`
	Context          AgentContext             `json:"context"`
}

// AfterToolCallContext is passed to the afterToolCall hook.
type AfterToolCallContext struct {
	AssistantMessage aitypes.AssistantMessage `json:"assistantMessage"`
	ToolCall         AgentToolCall            `json:"toolCall"`
	Args             any                      `json:"args"`
	Result           AgentToolResult[any]     `json:"result"`
	IsError          bool                     `json:"isError"`
	Context          AgentContext             `json:"context"`
}

// AgentTurnContext is passed to completed-turn callbacks.
type AgentTurnContext struct {
	Message     aitypes.AssistantMessage    `json:"message"`
	ToolResults []aitypes.ToolResultMessage `json:"toolResults"`
	Context     AgentContext                `json:"context"`
	NewMessages []AgentMessage              `json:"newMessages"`
}

// AgentTurnDecision is returned by FinishTurn.
type AgentTurnDecision struct {
	Action string `json:"action"`
}

// FinishTurn is called after a completed assistant turn and its tool results.
type FinishTurn func(turn AgentTurnContext, signal <-chan struct{}) (AgentTurnDecision, error)

// AgentLoopTurnUpdate replaces loop runtime state before the next request.
type AgentLoopTurnUpdate struct {
	Context       *AgentContext
	Messages      []AgentMessage
	Model         *aitypes.Model
	ThinkingLevel *ThinkingLevel
}

// PrepareRequestContext is the state before a conversational request.
type PrepareRequestContext struct {
	Context       AgentContext
	Model         *aitypes.Model
	ThinkingLevel ThinkingLevel
}

// AgentRequestUpdate replaces state for the request being prepared.
type AgentRequestUpdate struct {
	Context       *AgentContext
	Model         *aitypes.Model
	ThinkingLevel *ThinkingLevel
}

// PrepareRequest is called before every conversational request.
type PrepareRequest func(request PrepareRequestContext, signal <-chan struct{}) (AgentRequestUpdate, error)

// PrepareNextTurnContext mirrors AgentTurnContext.
type PrepareNextTurnContext = AgentTurnContext

// AgentLoopConfig is the low-level agent loop configuration.
type AgentLoopConfig struct {
	aitypes.SimpleStreamOptions
	Model               *aitypes.Model
	ConvertToLlm        func(messages []AgentMessage) ([]aitypes.Message, error)
	TransformContext    func(messages []AgentMessage, signal <-chan struct{}) ([]AgentMessage, error)
	GetApiKey           func(provider string) (string, bool, error)
	FinishTurn          FinishTurn
	PrepareRequest      PrepareRequest
	PrepareNextTurn     func(context PrepareNextTurnContext) (*AgentLoopTurnUpdate, error)
	GetSteeringMessages func() ([]AgentMessage, error)
	GetFollowUpMessages func() ([]AgentMessage, error)
	ToolExecution       ToolExecutionMode
	BeforeToolCall      func(context BeforeToolCallContext, signal <-chan struct{}) (*BeforeToolCallResult, error)
	AfterToolCall       func(context AfterToolCallContext, signal <-chan struct{}) (*AfterToolCallResult, error)
}

// ThinkingLevel is the requested reasoning level.
type ThinkingLevel = aitypes.ThinkingLevel

// Thinking levels.
const (
	ThinkingOff     = aitypes.ThinkingOff
	ThinkingMinimal = aitypes.ThinkingMinimal
	ThinkingLow     = aitypes.ThinkingLow
	ThinkingMedium  = aitypes.ThinkingMedium
	ThinkingHigh    = aitypes.ThinkingHigh
	ThinkingXHigh   = aitypes.ThinkingXHigh
	ThinkingMax     = aitypes.ThinkingMax
)

// CustomAgentMessages is the open extension point for application messages.
type CustomAgentMessages interface{}

// CustomMessage is one application-defined transcript message. Raw preserves
// the complete unknown payload.
type CustomMessage struct {
	Role string          `json:"role"`
	Raw  json.RawMessage `json:"-"`
}

// AgentMessage is one element of the agent transcript.
type AgentMessage struct {
	Message *aitypes.Message
	Custom  *CustomMessage
	// QueueID identifies host-managed pending input in native events. It is
	// ephemeral: neither provider payloads nor persisted transcripts contain it.
	QueueID string `json:"-"`
}

// NewAgentMessageFromMessage wraps a standard provider message.
func NewAgentMessageFromMessage(message aitypes.Message) AgentMessage {
	return AgentMessage{Message: &message}
}

// NewCustomMessage wraps an application-defined message payload.
func NewCustomMessage(role string, raw json.RawMessage) AgentMessage {
	return AgentMessage{Custom: &CustomMessage{Role: role, Raw: append(json.RawMessage(nil), raw...)}}
}

var standardRoles = map[string]bool{
	aitypes.SystemMessageRole:     true,
	aitypes.UserMessageRole:       true,
	aitypes.AssistantMessageRole:  true,
	aitypes.ToolResultMessageRole: true,
}

// MarshalJSON writes the standard message or the verbatim custom payload.
func (m AgentMessage) MarshalJSON() ([]byte, error) {
	if m.Message != nil {
		return json.Marshal(m.Message)
	}
	if m.Custom != nil {
		if len(m.Custom.Raw) > 0 {
			return m.Custom.Raw, nil
		}
		return json.Marshal(map[string]any{"role": m.Custom.Role})
	}
	return []byte("null"), nil
}

// UnmarshalJSON reads a standard provider message or preserves a custom one.
func (m *AgentMessage) UnmarshalJSON(data []byte) error {
	m.QueueID = ""
	var probe struct {
		Role string `json:"role"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return err
	}
	if standardRoles[probe.Role] {
		var message aitypes.Message
		if err := json.Unmarshal(data, &message); err != nil {
			return err
		}
		m.Message = &message
		m.Custom = nil
		return nil
	}
	if probe.Role == "" {
		return fmt.Errorf("agent message is missing a role")
	}
	m.Message = nil
	m.Custom = &CustomMessage{Role: probe.Role, Raw: append(json.RawMessage(nil), data...)}
	return nil
}

// AgentState is the public agent state.
type AgentState struct {
	SystemPrompt     string                `json:"systemPrompt"`
	Model            *aitypes.Model        `json:"model"`
	ThinkingLevel    ThinkingLevel         `json:"thinkingLevel"`
	Tools            []AgentTool[any, any] `json:"tools"`
	Messages         []AgentMessage        `json:"messages"`
	IsStreaming      bool                  `json:"isStreaming"`
	StreamingMessage *AgentMessage         `json:"streamingMessage,omitempty"`
	PendingToolCalls []string              `json:"pendingToolCalls"`
	ErrorMessage     *string               `json:"errorMessage,omitempty"`
}

// AgentToolResult is the final or partial result produced by a tool.
type AgentToolResult[T any] struct {
	Content []aitypes.ContentBlock `json:"content"`
	Details T                      `json:"details"`
	// StructuredContent is the machine-readable result matching the tool's
	// OutputSchema. It is not sent to the model; Content remains the
	// model-facing result.
	StructuredContent json.RawMessage `json:"structuredContent,omitempty"`
	Usage             *aitypes.Usage  `json:"usage,omitempty"`
	// IsError reports a failure without throwing. The model sees Content as an
	// error result, while Details and StructuredContent are kept.
	IsError   bool  `json:"isError,omitempty"`
	Terminate *bool `json:"terminate,omitempty"`
}

// AgentToolUpdateCallback streams partial execution updates.
type AgentToolUpdateCallback[T any] func(partial AgentToolResult[T])

// AgentTool is the tool definition used by the agent runtime.
type AgentTool[TParameters any, TDetails any] struct {
	aitypes.Tool
	Label            string
	PrepareArguments func(args any) (TParameters, error)
	// OutputSchema is the JSON Schema of StructuredContent in successful
	// results. It is declared for programmatic callers; output is never
	// silently rewritten to satisfy it.
	OutputSchema  json.RawMessage
	Execute       func(toolCallId string, params TParameters, signal <-chan struct{}, onUpdate AgentToolUpdateCallback[TDetails]) (AgentToolResult[TDetails], error)
	Replay        string
	ExecutionMode ToolExecutionMode
}

// RunToolCallOptions configures RunToolCall.
type RunToolCallOptions struct {
	// Tools the call resolves against.
	Tools []AgentTool[any, any]
	// AssistantMessage is passed to the hooks as the message that issued the call.
	AssistantMessage aitypes.AssistantMessage
	// Context is passed to the hooks as the current agent context.
	Context AgentContext
	// Signal cancels preparation, execution and hooks.
	Signal <-chan struct{}
	// OnUpdate streams partial execution updates, if non-nil.
	OnUpdate AgentToolUpdateCallback[any]
	// BeforeToolCall runs after preparation and schema validation.
	BeforeToolCall func(context BeforeToolCallContext, signal <-chan struct{}) (*BeforeToolCallResult, error)
	// AfterToolCall runs after execution, before the outcome is finalized.
	AfterToolCall func(context AfterToolCallContext, signal <-chan struct{}) (*AfterToolCallResult, error)
}

// ToolCallOutcome is the final outcome of one tool call after hooks ran.
type ToolCallOutcome struct {
	ToolCall AgentToolCall
	Result   AgentToolResult[any]
	IsError  bool
}

// AgentContext is the context snapshot passed into the low-level agent loop.
type AgentContext struct {
	Messages []AgentMessage        `json:"messages"`
	Tools    []AgentTool[any, any] `json:"tools,omitempty"`
}

// AgentEvent is an event emitted by the Agent for UI updates.
type AgentEvent struct {
	Type                  string                         `json:"type"`
	Messages              []AgentMessage                 `json:"messages,omitempty"`
	Message               *AgentMessage                  `json:"message,omitempty"`
	ToolResults           []aitypes.ToolResultMessage    `json:"toolResults,omitempty"`
	AssistantMessageEvent *aitypes.AssistantMessageEvent `json:"assistantMessageEvent,omitempty"`
	ToolCallId            *string                        `json:"toolCallId,omitempty"`
	ToolName              *string                        `json:"toolName,omitempty"`
	Args                  any                            `json:"args,omitempty"`
	PartialResult         any                            `json:"partialResult,omitempty"`
	Result                any                            `json:"result,omitempty"`
	IsError               *bool                          `json:"isError,omitempty"`
}

// Agent event types.
const (
	AgentEventAgentStart          = "agent_start"
	AgentEventAgentEnd            = "agent_end"
	AgentEventTurnStart           = "turn_start"
	AgentEventTurnEnd             = "turn_end"
	AgentEventMessageStart        = "message_start"
	AgentEventMessageUpdate       = "message_update"
	AgentEventMessageEnd          = "message_end"
	AgentEventToolExecutionStart  = "tool_execution_start"
	AgentEventToolExecutionUpdate = "tool_execution_update"
	AgentEventToolExecutionEnd    = "tool_execution_end"
)
