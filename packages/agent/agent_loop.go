// This file is a Go port of packages/agent/src/agent-loop.ts from Pi at
// revision f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// The low-level loop works with AgentMessage throughout and transforms to the
// provider Message[] only at the LLM call boundary. Cancellation is carried by
// a done channel (the Go port of AbortSignal) so the loop can be used from both
// Agent and direct embedders.
package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	agenttypes "github.com/minifish-org/pith/packages/agent/types"
	aitypes "github.com/minifish-org/pith/packages/ai/types"
	aiutils "github.com/minifish-org/pith/packages/ai/utils"
	"github.com/minifish-org/pith/packages/ai/utils/eventstream"
)

// AgentEventSink receives loop events. A returned error aborts the loop; the
// sink must not retain the event pointer while the loop continues.
type AgentEventSink func(event agenttypes.AgentEvent) error

// AgentStream is the ordered event stream returned by AgentLoop and
// AgentLoopContinue. It completes on agent_end and resolves to the messages the
// run produced.
type AgentStream = eventstream.EventStream[agenttypes.AgentEvent, []agenttypes.AgentMessage]

func newAgentStream() *AgentStream {
	return eventstream.NewEventStream(
		func(event agenttypes.AgentEvent) bool { return event.Type == agenttypes.AgentEventAgentEnd },
		func(event agenttypes.AgentEvent) []agenttypes.AgentMessage { return event.Messages },
	)
}

// AgentLoop starts an agent loop with new prompt messages. The prompts are added
// to the context and lifecycle events are emitted for them. The returned stream
// completes with the messages produced by the run.
func AgentLoop(
	prompts []agenttypes.AgentMessage,
	context agenttypes.AgentContext,
	config agenttypes.AgentLoopConfig,
	signal <-chan struct{},
	streamFn agenttypes.StreamFn,
) *AgentStream {
	stream := newAgentStream()
	go func() {
		messages, err := RunAgentLoop(prompts, context, config, func(event agenttypes.AgentEvent) error {
			stream.Push(event)
			return nil
		}, signal, streamFn)
		if err != nil {
			// End without a result: Result waiters observe a cancelled context
			// rather than a bogus empty message list.
			stream.End(nil)
			return
		}
		result := messages
		stream.End(&result)
	}()
	return stream
}

// AgentLoopContinue continues an agent loop from the current context without
// adding a new message. The last message must convert to a user or toolResult
// message; an empty context or an assistant tail is rejected.
func AgentLoopContinue(
	context agenttypes.AgentContext,
	config agenttypes.AgentLoopConfig,
	signal <-chan struct{},
	streamFn agenttypes.StreamFn,
) (*AgentStream, error) {
	if err := validateContinuation(context); err != nil {
		return nil, err
	}
	stream := newAgentStream()
	go func() {
		messages, err := RunAgentLoopContinue(context, config, func(event agenttypes.AgentEvent) error {
			stream.Push(event)
			return nil
		}, signal, streamFn)
		if err != nil {
			stream.End(nil)
			return
		}
		result := messages
		stream.End(&result)
	}()
	return stream, nil
}

func validateContinuation(context agenttypes.AgentContext) error {
	if len(context.Messages) == 0 {
		return fmt.Errorf("agent: cannot continue: no messages in context")
	}
	if messageRole(context.Messages[len(context.Messages)-1]) == aitypes.AssistantMessageRole {
		return fmt.Errorf("agent: cannot continue from message role: assistant")
	}
	return nil
}

// RunAgentLoop runs the loop directly with a caller-owned event sink and
// returns the messages produced by the run. It is the primitive underlying
// AgentLoop and the Agent lifecycle.
func RunAgentLoop(
	prompts []agenttypes.AgentMessage,
	context agenttypes.AgentContext,
	config agenttypes.AgentLoopConfig,
	emit AgentEventSink,
	signal <-chan struct{},
	streamFn agenttypes.StreamFn,
) ([]agenttypes.AgentMessage, error) {
	initialMessages := declareToolChanges(context, prompts)
	newMessages := make([]agenttypes.AgentMessage, 0, len(initialMessages))
	newMessages = append(newMessages, initialMessages...)
	currentContext := context
	currentContext.Messages = make([]agenttypes.AgentMessage, 0, len(context.Messages)+len(initialMessages))
	currentContext.Messages = append(currentContext.Messages, context.Messages...)
	currentContext.Messages = append(currentContext.Messages, initialMessages...)

	if err := emit(agenttypes.AgentEvent{Type: agenttypes.AgentEventAgentStart}); err != nil {
		return nil, err
	}
	if err := emit(agenttypes.AgentEvent{Type: agenttypes.AgentEventTurnStart}); err != nil {
		return nil, err
	}
	for _, message := range initialMessages {
		msg := message
		if err := emit(agenttypes.AgentEvent{Type: agenttypes.AgentEventMessageStart, Message: &msg}); err != nil {
			return nil, err
		}
		if err := emit(agenttypes.AgentEvent{Type: agenttypes.AgentEventMessageEnd, Message: &msg}); err != nil {
			return nil, err
		}
	}

	resolved := streamFn
	if resolved == nil {
		defaultFn, err := GetDefaultStreamFn()
		if err != nil {
			return nil, err
		}
		resolved = defaultFn
	}
	if err := runLoop(&currentContext, &newMessages, &config, signal, emit, resolved); err != nil {
		return nil, err
	}
	return newMessages, nil
}

// RunAgentLoopContinue continues the loop directly with a caller-owned event
// sink. It shares runLoop with RunAgentLoop and returns only the messages
// produced by this continuation.
func RunAgentLoopContinue(
	context agenttypes.AgentContext,
	config agenttypes.AgentLoopConfig,
	emit AgentEventSink,
	signal <-chan struct{},
	streamFn agenttypes.StreamFn,
) ([]agenttypes.AgentMessage, error) {
	if err := validateContinuation(context); err != nil {
		return nil, err
	}
	newMessages := []agenttypes.AgentMessage{}
	currentContext := context
	if err := emit(agenttypes.AgentEvent{Type: agenttypes.AgentEventAgentStart}); err != nil {
		return nil, err
	}
	if err := emit(agenttypes.AgentEvent{Type: agenttypes.AgentEventTurnStart}); err != nil {
		return nil, err
	}
	resolved := streamFn
	if resolved == nil {
		defaultFn, err := GetDefaultStreamFn()
		if err != nil {
			return nil, err
		}
		resolved = defaultFn
	}
	if err := runLoop(&currentContext, &newMessages, &config, signal, emit, resolved); err != nil {
		return nil, err
	}
	return newMessages, nil
}

// runLoop is the shared loop body. It mutates the context and newMessages
// slices through the pointers because prepareRequest/prepareNextTurn may
// replace the context wholesale.
func runLoop(
	initialContext *agenttypes.AgentContext,
	newMessages *[]agenttypes.AgentMessage,
	initialConfig *agenttypes.AgentLoopConfig,
	signal <-chan struct{},
	emit AgentEventSink,
	streamFunction agenttypes.StreamFn,
) error {
	currentContext := *initialContext
	config := *initialConfig
	var lastCompletedTurn *agenttypes.PrepareNextTurnContext
	explicitContinuation := false
	pendingMessages, err := steeringMessages(config)
	if err != nil {
		return err
	}

	for {
		hasMoreToolCalls := true

		for hasMoreToolCalls || len(pendingMessages) > 0 {
			var preparedMessages []agenttypes.AgentMessage
			if lastCompletedTurn != nil {
				if config.PrepareNextTurn != nil {
					snapshot, err := config.PrepareNextTurn(*lastCompletedTurn)
					if err != nil {
						return err
					}
					if snapshot != nil {
						if snapshot.Context != nil {
							currentContext = *snapshot.Context
						}
						if snapshot.Messages != nil {
							preparedMessages = snapshot.Messages
						}
						if snapshot.Model != nil {
							config.Model = snapshot.Model
						}
						if snapshot.ThinkingLevel != nil {
							applyThinkingLevel(&config, *snapshot.ThinkingLevel)
						}
					}
				}
				// Preparation can be long-running (for example, compaction). Pick
				// up steering queued while it ran. Only poll again if the earlier
				// poll returned nothing; otherwise one-at-a-time mode would
				// deliver two messages in this turn.
				if len(pendingMessages) == 0 {
					pendingMessages, err = steeringMessages(config)
					if err != nil {
						return err
					}
				}
				if err := emit(agenttypes.AgentEvent{Type: agenttypes.AgentEventTurnStart}); err != nil {
					return err
				}
			}

			combined := make([]agenttypes.AgentMessage, 0, len(preparedMessages)+len(pendingMessages))
			combined = append(combined, preparedMessages...)
			combined = append(combined, pendingMessages...)
			for _, message := range declareToolChanges(currentContext, combined) {
				msg := message
				if err := emit(agenttypes.AgentEvent{Type: agenttypes.AgentEventMessageStart, Message: &msg}); err != nil {
					return err
				}
				if err := emit(agenttypes.AgentEvent{Type: agenttypes.AgentEventMessageEnd, Message: &msg}); err != nil {
					return err
				}
				currentContext.Messages = append(currentContext.Messages, message)
				*newMessages = append(*newMessages, message)
			}
			pendingMessages = nil

			if config.PrepareRequest != nil {
				update, err := config.PrepareRequest(agenttypes.PrepareRequestContext{
					Context:       currentContext,
					Model:         config.Model,
					ThinkingLevel: thinkingLevelOrOff(config.Reasoning),
				}, signal)
				if err != nil {
					return err
				}
				if update.Context != nil {
					currentContext = *update.Context
				}
				if update.Model != nil {
					config.Model = update.Model
				}
				if update.ThinkingLevel != nil {
					applyThinkingLevel(&config, *update.ThinkingLevel)
				}
			}

			message, err := streamAssistantResponse(&currentContext, config, signal, emit, streamFunction)
			if err != nil {
				return err
			}
			*newMessages = append(*newMessages, agentMessageFrom(aitypes.NewAssistantMessageVariant(message)))

			if message.StopReason == aitypes.StopReasonError || message.StopReason == aitypes.StopReasonAborted {
				turn := agenttypes.PrepareNextTurnContext{
					Message:     message,
					ToolResults: []aitypes.ToolResultMessage{},
					Context:     currentContext,
					NewMessages: *newMessages,
				}
				lastCompletedTurn = &turn
				if config.FinishTurn != nil {
					if _, err := config.FinishTurn(turn, signal); err != nil {
						return err
					}
				}
				if err := emit(agenttypes.AgentEvent{Type: agenttypes.AgentEventTurnEnd, Message: agentMessagePtr(message), ToolResults: []aitypes.ToolResultMessage{}}); err != nil {
					return err
				}
				msgs := *newMessages
				return emit(agenttypes.AgentEvent{Type: agenttypes.AgentEventAgentEnd, Messages: msgs})
			}

			toolCalls := toolCallsOf(message)

			var toolResults []aitypes.ToolResultMessage
			hasMoreToolCalls = false
			if len(toolCalls) > 0 {
				// A "length" stop means the output was cut off by the token
				// limit, so every tool call in the message may carry truncated
				// arguments. Fail them all instead of executing borked calls.
				var batch executedToolCallBatch
				if message.StopReason == aitypes.StopReasonLength {
					batch, err = failToolCallsFromTruncatedMessage(toolCalls, emit)
				} else {
					batch, err = executeToolCalls(currentContext, message, config, signal, emit)
				}
				if err != nil {
					return err
				}
				toolResults = batch.Messages
				hasMoreToolCalls = !batch.Terminate

				for _, result := range toolResults {
					agentResult := agentMessageFrom(aitypes.NewToolResultMessageVariant(result))
					currentContext.Messages = append(currentContext.Messages, agentResult)
					*newMessages = append(*newMessages, agentResult)
				}
			}

			turn := agenttypes.PrepareNextTurnContext{
				Message:     message,
				ToolResults: toolResults,
				Context:     currentContext,
				NewMessages: *newMessages,
			}
			lastCompletedTurn = &turn
			decision := agenttypes.AgentTurnDecision{}
			if config.FinishTurn != nil {
				decision, err = config.FinishTurn(turn, signal)
				if err != nil {
					return err
				}
			}
			if err := emit(agenttypes.AgentEvent{Type: agenttypes.AgentEventTurnEnd, Message: agentMessagePtr(message), ToolResults: toolResults}); err != nil {
				return err
			}

			if decision.Action == "end" {
				msgs := *newMessages
				return emit(agenttypes.AgentEvent{Type: agenttypes.AgentEventAgentEnd, Messages: msgs})
			}

			explicitContinuation = decision.Action == "continue"
			pendingMessages, err = steeringMessages(config)
			if err != nil {
				return err
			}
			if hasMoreToolCalls || len(pendingMessages) > 0 {
				explicitContinuation = false
			}
		}

		// Agent would stop here. Check for follow-up messages.
		followUpMessages := []agenttypes.AgentMessage{}
		if config.GetFollowUpMessages != nil {
			followUpMessages, err = config.GetFollowUpMessages()
			if err != nil {
				return err
			}
		}
		if len(followUpMessages) > 0 {
			explicitContinuation = false
			pendingMessages = followUpMessages
			continue
		}

		// No natural request was selected, so fulfill the continuation decision
		// with one context-only turn.
		if explicitContinuation {
			explicitContinuation = false
			continue
		}

		break
	}

	msgs := *newMessages
	return emit(agenttypes.AgentEvent{Type: agenttypes.AgentEventAgentEnd, Messages: msgs})
}

func steeringMessages(config agenttypes.AgentLoopConfig) ([]agenttypes.AgentMessage, error) {
	if config.GetSteeringMessages == nil {
		return nil, nil
	}
	return config.GetSteeringMessages()
}

func thinkingLevelOrOff(level *agenttypes.ThinkingLevel) agenttypes.ThinkingLevel {
	if level == nil {
		return agenttypes.ThinkingOff
	}
	return *level
}

func applyThinkingLevel(config *agenttypes.AgentLoopConfig, level agenttypes.ThinkingLevel) {
	if level == agenttypes.ThinkingOff {
		config.Reasoning = nil
		return
	}
	value := level
	config.Reasoning = &value
}

// declareToolChanges announces tool loadout changes to the model.
//
// context.Tools is what the runtime can execute; the transcript's system
// messages declare what the model may call. Before each request the difference
// becomes toolsAdded/toolsRemoved on a system message. When a pending system
// message exists, its tool fields are treated as intent and replaced with the
// delta between the committed transcript and the executable set, so replay
// always yields exactly context.Tools. Otherwise a new system message is
// inserted before the first non-system pending message.
func declareToolChanges(context agenttypes.AgentContext, pendingMessages []agenttypes.AgentMessage) []agenttypes.AgentMessage {
	systemIndex := -1
	for i := len(pendingMessages) - 1; i >= 0; i-- {
		if messageRole(pendingMessages[i]) == aitypes.SystemMessageRole {
			systemIndex = i
			break
		}
	}
	var pending *aitypes.SystemMessage
	if systemIndex >= 0 {
		pending, _ = systemMessageOf(pendingMessages[systemIndex])
	}
	baseline := pendingMessages
	if pending != nil {
		baseline = make([]agenttypes.AgentMessage, len(pendingMessages))
		copy(baseline, pendingMessages)
		baseline[systemIndex] = agentMessageFrom(aitypes.NewSystemMessageVariant(withToolChanges(*pending, noToolChanges)))
	}
	combined := make([]aitypes.Message, 0, len(context.Messages)+len(baseline))
	combined = append(combined, transcriptMessages(context.Messages)...)
	combined = append(combined, transcriptMessages(baseline)...)
	currentTools := make([]aitypes.Tool, 0, len(context.Tools))
	for _, tool := range context.Tools {
		currentTools = append(currentTools, aiutils.ToToolDeclaration(tool.Tool))
	}
	changes := aiutils.GetToolStateChanges(aiutils.GetCurrentTools(combined), currentTools)
	unchanged := len(changes.ToolsAdded) == 0 && len(changes.ToolsRemoved) == 0

	if pending != nil {
		// Keep the caller's message object when it already declares no tool changes.
		if unchanged && len(pending.ToolsAdded) == 0 && len(pending.ToolsRemoved) == 0 {
			return pendingMessages
		}
		out := make([]agenttypes.AgentMessage, len(baseline))
		copy(out, baseline)
		out[systemIndex] = agentMessageFrom(aitypes.NewSystemMessageVariant(withToolChanges(*pending, changes)))
		return out
	}
	if unchanged {
		return pendingMessages
	}
	update := withToolChanges(aitypes.SystemMessage{
		Role:      aitypes.SystemMessageRole,
		Content:   aitypes.SystemContentText(""),
		Timestamp: float64(time.Now().UnixMilli()),
	}, changes)
	insertIndex := len(pendingMessages)
	for i, message := range pendingMessages {
		if messageRole(message) != aitypes.SystemMessageRole {
			insertIndex = i
			break
		}
	}
	out := make([]agenttypes.AgentMessage, 0, len(pendingMessages)+1)
	out = append(out, pendingMessages[:insertIndex]...)
	out = append(out, agentMessageFrom(aitypes.NewSystemMessageVariant(update)))
	out = append(out, pendingMessages[insertIndex:]...)
	return out
}

var noToolChanges = aiutils.ToolStateChanges{ToolsAdded: []aitypes.Tool{}, ToolsRemoved: []aitypes.ToolReference{}}

// withToolChanges copies a system message with its tool fields replaced. Empty
// lists omit the field, matching the upstream spread that only adds non-empty
// arrays.
func withToolChanges(message aitypes.SystemMessage, changes aiutils.ToolStateChanges) aitypes.SystemMessage {
	message.ToolsAdded = nil
	message.ToolsRemoved = nil
	if len(changes.ToolsAdded) > 0 {
		message.ToolsAdded = changes.ToolsAdded
	}
	if len(changes.ToolsRemoved) > 0 {
		message.ToolsRemoved = changes.ToolsRemoved
	}
	return message
}

// streamAssistantResponse streams one assistant response and appends the final
// message to the context. This is where AgentMessage[] becomes provider
// Message[].
func streamAssistantResponse(
	context *agenttypes.AgentContext,
	config agenttypes.AgentLoopConfig,
	signal <-chan struct{},
	emit AgentEventSink,
	streamFunction agenttypes.StreamFn,
) (aitypes.AssistantMessage, error) {
	messages := context.Messages
	if config.TransformContext != nil {
		transformed, err := config.TransformContext(messages, signal)
		if err != nil {
			return aitypes.AssistantMessage{}, err
		}
		messages = transformed
	}

	if config.ConvertToLlm == nil {
		return aitypes.AssistantMessage{}, fmt.Errorf("agent: convertToLlm is required")
	}
	llmMessages, err := config.ConvertToLlm(messages)
	if err != nil {
		return aitypes.AssistantMessage{}, err
	}
	llmContext := aiutils.NormalizeContext(aitypes.Context{Messages: llmMessages})

	resolvedApiKey := ""
	if config.GetApiKey != nil && config.Model != nil {
		key, ok, err := config.GetApiKey(string(config.Model.Provider))
		if err != nil {
			return aitypes.AssistantMessage{}, err
		}
		if ok {
			resolvedApiKey = key
		}
	}
	if resolvedApiKey == "" && config.APIKey != nil {
		resolvedApiKey = *config.APIKey
	}

	options := config.SimpleStreamOptions
	if resolvedApiKey != "" {
		key := resolvedApiKey
		options.APIKey = &key
	}
	options.Signal = signal

	response := streamFunction(config.Model, llmContext, &options)

	var partialMessage *aitypes.AssistantMessage
	addedPartial := false

	// Record the requested level, whichever stream function answered. The
	// requested level (including "off") is independent of any adapter-specific
	// mapped effort.
	requestedThinkingLevel := string(thinkingLevelOrOff(config.Reasoning))
	finalResult := func() aitypes.AssistantMessage {
		finalMessage, err := response.Result(backgroundFromSignal(signal))
		if err != nil {
			finalMessage = recoverFinalMessage(partialMessage, signal, err)
		}
		finalMessage.ThinkingLevel = requestedThinkingLevel
		return finalMessage
	}

	for {
		event, ok := nextStreamEvent(response, signal)
		if !ok {
			break
		}
		switch event.Type {
		case aitypes.AssistantEventStart:
			if event.Partial != nil {
				partial := *event.Partial
				partialMessage = &partial
				context.Messages = append(context.Messages, agentMessageFrom(aitypes.NewAssistantMessageVariant(partial)))
				addedPartial = true
				msg := agentMessageFrom(aitypes.NewAssistantMessageVariant(partial))
				if err := emit(agenttypes.AgentEvent{Type: agenttypes.AgentEventMessageStart, Message: &msg}); err != nil {
					return aitypes.AssistantMessage{}, err
				}
			}

		case aitypes.AssistantEventTextStart, aitypes.AssistantEventTextDelta, aitypes.AssistantEventTextEnd,
			aitypes.AssistantEventThinkingStart, aitypes.AssistantEventThinkingDelta, aitypes.AssistantEventThinkingEnd,
			aitypes.AssistantEventToolCallStart, aitypes.AssistantEventToolCallDelta, aitypes.AssistantEventToolCallEnd:
			if partialMessage != nil && event.Partial != nil {
				partial := *event.Partial
				partialMessage = &partial
				context.Messages[len(context.Messages)-1] = agentMessageFrom(aitypes.NewAssistantMessageVariant(partial))
				ev := event
				msg := agentMessageFrom(aitypes.NewAssistantMessageVariant(partial))
				if err := emit(agenttypes.AgentEvent{
					Type:                  agenttypes.AgentEventMessageUpdate,
					AssistantMessageEvent: &ev,
					Message:               &msg,
				}); err != nil {
					return aitypes.AssistantMessage{}, err
				}
			}

		case aitypes.AssistantEventDone, aitypes.AssistantEventError:
			finalMessage := finalResult()
			if addedPartial {
				context.Messages[len(context.Messages)-1] = agentMessageFrom(aitypes.NewAssistantMessageVariant(finalMessage))
			} else {
				context.Messages = append(context.Messages, agentMessageFrom(aitypes.NewAssistantMessageVariant(finalMessage)))
				msg := agentMessageFrom(aitypes.NewAssistantMessageVariant(finalMessage))
				if err := emit(agenttypes.AgentEvent{Type: agenttypes.AgentEventMessageStart, Message: &msg}); err != nil {
					return aitypes.AssistantMessage{}, err
				}
			}
			msg := agentMessageFrom(aitypes.NewAssistantMessageVariant(finalMessage))
			if err := emit(agenttypes.AgentEvent{Type: agenttypes.AgentEventMessageEnd, Message: &msg}); err != nil {
				return aitypes.AssistantMessage{}, err
			}
			return finalMessage, nil
		}
	}

	// The stream ended without a terminal event. Recover the best available
	// final message and finish the message lifecycle.
	finalMessage := finalResult()
	if addedPartial {
		context.Messages[len(context.Messages)-1] = agentMessageFrom(aitypes.NewAssistantMessageVariant(finalMessage))
	} else {
		context.Messages = append(context.Messages, agentMessageFrom(aitypes.NewAssistantMessageVariant(finalMessage)))
		msg := agentMessageFrom(aitypes.NewAssistantMessageVariant(finalMessage))
		if err := emit(agenttypes.AgentEvent{Type: agenttypes.AgentEventMessageStart, Message: &msg}); err != nil {
			return aitypes.AssistantMessage{}, err
		}
	}
	msg := agentMessageFrom(aitypes.NewAssistantMessageVariant(finalMessage))
	if err := emit(agenttypes.AgentEvent{Type: agenttypes.AgentEventMessageEnd, Message: &msg}); err != nil {
		return aitypes.AssistantMessage{}, err
	}
	return finalMessage, nil
}

// nextStreamEvent waits for the next stream event or the abort signal. When the
// signal fires first, the registered consumer is drained in the background so
// the stream can keep delivering to other consumers.
func nextStreamEvent(response *aitypes.AssistantMessageEventStream, signal <-chan struct{}) (aitypes.AssistantMessageEvent, bool) {
	ch := response.Next()
	if signal == nil {
		item := <-ch
		return item.Value, !item.Done
	}
	select {
	case item := <-ch:
		return item.Value, !item.Done
	case <-signal:
		go func() { <-ch }()
		return aitypes.AssistantMessageEvent{}, false
	}
}

// recoverFinalMessage builds a terminal assistant message when the stream ended
// without one, preserving whichever partial state was observed.
func recoverFinalMessage(partial *aitypes.AssistantMessage, signal <-chan struct{}, cause error) aitypes.AssistantMessage {
	if partial != nil {
		message := *partial
		if signalAborted(signal) {
			message.StopReason = aitypes.StopReasonAborted
			text := "Request aborted by user"
			message.ErrorMessage = &text
		} else {
			message.StopReason = aitypes.StopReasonError
			text := cause.Error()
			message.ErrorMessage = &text
		}
		return message
	}
	reason := aitypes.StopReasonError
	text := cause.Error()
	if signalAborted(signal) {
		reason = aitypes.StopReasonAborted
		text = "Request aborted by user"
	}
	message := aitypes.NewAssistantMessage("", "", "", float64(time.Now().UnixMilli()))
	message.StopReason = reason
	message.ErrorMessage = &text
	return message
}

// failToolCallsFromTruncatedMessage fails every tool call from an assistant
// message truncated by the output token limit. Streamed arguments are
// finalized with a best-effort salvage parser, so a truncated message can yield
// tool calls whose arguments parse but are silently incomplete. None are safe to
// execute; each is reported as an error so the model can re-issue it.
func failToolCallsFromTruncatedMessage(toolCalls []aitypes.ToolCall, emit AgentEventSink) (executedToolCallBatch, error) {
	messages := []aitypes.ToolResultMessage{}
	for _, toolCall := range toolCalls {
		if err := emitToolExecutionStart(toolCall, emit); err != nil {
			return executedToolCallBatch{}, err
		}
		finalized := finalizedToolCallOutcome{
			toolCall: toolCall,
			result: createErrorToolResult(fmt.Sprintf(
				"Tool call %q was not executed: the response hit the output token limit, so its arguments may be truncated. Re-issue the tool call with complete arguments.",
				toolCall.Name,
			)),
			isError: true,
		}
		if err := emitToolExecutionEnd(finalized, emit); err != nil {
			return executedToolCallBatch{}, err
		}
		message := createToolResultMessage(finalized)
		if err := emitToolResultMessage(message, emit); err != nil {
			return executedToolCallBatch{}, err
		}
		messages = append(messages, message)
	}
	return executedToolCallBatch{Messages: messages, Terminate: false}, nil
}

type executedToolCallBatch struct {
	Messages  []aitypes.ToolResultMessage
	Terminate bool
}

type preparedToolCall struct {
	toolCall aitypes.ToolCall
	tool     *agenttypes.AgentTool[any, any]
	args     any
}

type immediateToolCallOutcome struct {
	result  agenttypes.AgentToolResult[any]
	isError bool
}

type executedToolCallOutcome struct {
	result  agenttypes.AgentToolResult[any]
	isError bool
}

type finalizedToolCallOutcome struct {
	toolCall aitypes.ToolCall
	result   agenttypes.AgentToolResult[any]
	isError  bool
}

func shouldTerminateToolBatch(finalizedCalls []finalizedToolCallOutcome) bool {
	if len(finalizedCalls) == 0 {
		return false
	}
	for _, finalized := range finalizedCalls {
		if finalized.result.Terminate == nil || !*finalized.result.Terminate {
			return false
		}
	}
	return true
}

func prepareToolCallArguments(tool agenttypes.AgentTool[any, any], toolCall aitypes.ToolCall) (aitypes.ToolCall, error) {
	if tool.PrepareArguments == nil {
		return toolCall, nil
	}
	preparedArguments, err := tool.PrepareArguments(rawToAny(toolCall.Arguments))
	if err != nil {
		return toolCall, err
	}
	encoded, err := json.Marshal(preparedArguments)
	if err != nil {
		return toolCall, err
	}
	if bytes.Equal(encoded, toolCall.Arguments) {
		return toolCall, nil
	}
	updated := toolCall
	updated.Arguments = encoded
	return updated, nil
}

// toolCallHooks is the tool-call hook slice of AgentLoopConfig. The main agent
// loop and the nested RunToolCall primitive share this shape so nested calls
// apply the same preparation, validation and finalization steps.
type toolCallHooks struct {
	before func(context agenttypes.BeforeToolCallContext, signal <-chan struct{}) (*agenttypes.BeforeToolCallResult, error)
	after  func(context agenttypes.AfterToolCallContext, signal <-chan struct{}) (*agenttypes.AfterToolCallResult, error)
}

func hooksOf(config agenttypes.AgentLoopConfig) toolCallHooks {
	return toolCallHooks{before: config.BeforeToolCall, after: config.AfterToolCall}
}

// prepareToolCall validates arguments and runs beforeToolCall. On success it
// returns a prepared call; otherwise it returns an immediate error outcome. The
// prepared struct keeps the original tool call (and thus its raw arguments),
// matching the upstream events. When tools is nil the current context tools are
// used.
func prepareToolCall(
	currentContext agenttypes.AgentContext,
	assistantMessage aitypes.AssistantMessage,
	toolCall aitypes.ToolCall,
	hooks toolCallHooks,
	signal <-chan struct{},
	tools []agenttypes.AgentTool[any, any],
) (*preparedToolCall, *immediateToolCallOutcome) {
	if tools == nil {
		tools = currentContext.Tools
	}
	tool := findTool(tools, toolCall.Name)
	if tool == nil {
		return nil, &immediateToolCallOutcome{
			result:  createErrorToolResult(fmt.Sprintf("Tool %s not found", toolCall.Name)),
			isError: true,
		}
	}

	preparedArguments, err := prepareToolCallArguments(*tool, toolCall)
	if err != nil {
		return nil, &immediateToolCallOutcome{result: createErrorToolResult(err.Error()), isError: true}
	}
	validatedArgs, err := aiutils.ValidateToolArguments(tool.Tool, preparedArguments)
	if err != nil {
		return nil, &immediateToolCallOutcome{result: createErrorToolResult(err.Error()), isError: true}
	}

	if hooks.before != nil {
		beforeResult, err := hooks.before(agenttypes.BeforeToolCallContext{
			AssistantMessage: assistantMessage,
			ToolCall:         toolCall,
			Args:             validatedArgs,
			Context:          currentContext,
		}, signal)
		if err != nil {
			return nil, &immediateToolCallOutcome{result: createErrorToolResult(err.Error()), isError: true}
		}
		if signalAborted(signal) {
			return nil, &immediateToolCallOutcome{result: createErrorToolResult("Operation aborted"), isError: true}
		}
		if beforeResult != nil && beforeResult.Block != nil && *beforeResult.Block {
			reason := "Tool execution was blocked"
			if beforeResult.Reason != nil {
				reason = *beforeResult.Reason
			}
			result := createErrorToolResult(reason)
			if beforeResult.Terminate != nil && *beforeResult.Terminate {
				terminate := true
				result.Terminate = &terminate
			}
			return nil, &immediateToolCallOutcome{result: result, isError: true}
		}
	}
	if signalAborted(signal) {
		return nil, &immediateToolCallOutcome{result: createErrorToolResult("Operation aborted"), isError: true}
	}
	return &preparedToolCall{toolCall: toolCall, tool: tool, args: validatedArgs}, nil
}

func executePreparedToolCall(prepared preparedToolCall, signal <-chan struct{}, onUpdate func(agenttypes.AgentToolResult[any]) error) (executedToolCallOutcome, error) {
	var mu sync.Mutex
	acceptingUpdates := true
	var updateEvents []agenttypes.AgentToolResult[any]
	sink := func(partialResult agenttypes.AgentToolResult[any]) {
		mu.Lock()
		defer mu.Unlock()
		if !acceptingUpdates {
			return
		}
		updateEvents = append(updateEvents, partialResult)
	}

	result, err := prepared.tool.Execute(prepared.toolCall.Id, prepared.args, signal, sink)

	mu.Lock()
	acceptingUpdates = false
	pending := updateEvents
	updateEvents = nil
	mu.Unlock()

	for _, partialResult := range pending {
		if updateErr := onUpdate(partialResult); updateErr != nil {
			return executedToolCallOutcome{}, updateErr
		}
	}

	if err != nil {
		return executedToolCallOutcome{result: createErrorToolResult(err.Error()), isError: true}, nil
	}
	return executedToolCallOutcome{result: result, isError: result.IsError}, nil
}

// emitToolExecutionUpdate adapts the agent event sink into the per-call update
// callback used by executePreparedToolCall.
func emitToolExecutionUpdate(toolCall aitypes.ToolCall, emit AgentEventSink) func(agenttypes.AgentToolResult[any]) error {
	id := toolCall.Id
	name := toolCall.Name
	args := rawToAny(toolCall.Arguments)
	return func(partialResult agenttypes.AgentToolResult[any]) error {
		return emit(agenttypes.AgentEvent{
			Type:          agenttypes.AgentEventToolExecutionUpdate,
			ToolCallId:    &id,
			ToolName:      &name,
			Args:          args,
			PartialResult: partialResult,
		})
	}
}

func finalizeExecutedToolCall(
	currentContext agenttypes.AgentContext,
	assistantMessage aitypes.AssistantMessage,
	prepared preparedToolCall,
	executed executedToolCallOutcome,
	hooks toolCallHooks,
	signal <-chan struct{},
) (finalizedToolCallOutcome, error) {
	result := executed.result
	isError := executed.isError

	if hooks.after != nil {
		afterResult, err := hooks.after(agenttypes.AfterToolCallContext{
			AssistantMessage: assistantMessage,
			ToolCall:         prepared.toolCall,
			Args:             prepared.args,
			Result:           result,
			IsError:          isError,
			Context:          currentContext,
		}, signal)
		if err != nil {
			result = createErrorToolResult(err.Error())
			isError = true
		} else if afterResult != nil {
			// Structured content not replaced along with the content may no
			// longer match it, so replacing content without an explicit
			// structured payload discards the stale structured data.
			var structuredContent json.RawMessage
			switch {
			case afterResult.StructuredContent != nil:
				structuredContent = afterResult.StructuredContent
			case afterResult.Content != nil:
				structuredContent = nil
			default:
				structuredContent = result.StructuredContent
			}
			if afterResult.Content != nil {
				result.Content = afterResult.Content
			}
			if afterResult.Details != nil {
				result.Details = afterResult.Details
			}
			if afterResult.Usage != nil {
				result.Usage = afterResult.Usage
			}
			if afterResult.Terminate != nil {
				result.Terminate = afterResult.Terminate
			}
			result.StructuredContent = structuredContent
			if afterResult.IsError != nil {
				isError = *afterResult.IsError
			}
		}
	}

	return finalizedToolCallOutcome{toolCall: prepared.toolCall, result: result, isError: isError}, nil
}

func createErrorToolResult(message string) agenttypes.AgentToolResult[any] {
	return agenttypes.AgentToolResult[any]{
		Content: []aitypes.ContentBlock{aitypes.TextBlock(message)},
		Details: map[string]any{},
	}
}

func emitToolExecutionStart(toolCall aitypes.ToolCall, emit AgentEventSink) error {
	id := toolCall.Id
	name := toolCall.Name
	return emit(agenttypes.AgentEvent{
		Type:       agenttypes.AgentEventToolExecutionStart,
		ToolCallId: &id,
		ToolName:   &name,
		Args:       rawToAny(toolCall.Arguments),
	})
}

func emitToolExecutionEnd(finalized finalizedToolCallOutcome, emit AgentEventSink) error {
	id := finalized.toolCall.Id
	name := finalized.toolCall.Name
	isError := finalized.isError
	return emit(agenttypes.AgentEvent{
		Type:       agenttypes.AgentEventToolExecutionEnd,
		ToolCallId: &id,
		ToolName:   &name,
		Result:     finalized.result,
		IsError:    &isError,
	})
}

func createToolResultMessage(finalized finalizedToolCallOutcome) aitypes.ToolResultMessage {
	content := finalized.result.Content
	if content == nil {
		content = []aitypes.ContentBlock{}
	}
	return aitypes.ToolResultMessage{
		Role:       aitypes.ToolResultMessageRole,
		ToolCallId: finalized.toolCall.Id,
		ToolName:   finalized.toolCall.Name,
		Content:    content,
		Details:    marshalDetails(finalized.result.Details),
		Usage:      finalized.result.Usage,
		IsError:    finalized.isError,
		Timestamp:  float64(time.Now().UnixMilli()),
	}
}

func marshalDetails(details any) json.RawMessage {
	if details == nil {
		return nil
	}
	encoded, err := json.Marshal(details)
	if err != nil {
		return nil
	}
	return encoded
}

func emitToolResultMessage(toolResultMessage aitypes.ToolResultMessage, emit AgentEventSink) error {
	message := agentMessageFrom(aitypes.NewToolResultMessageVariant(toolResultMessage))
	if err := emit(agenttypes.AgentEvent{Type: agenttypes.AgentEventMessageStart, Message: &message}); err != nil {
		return err
	}
	return emit(agenttypes.AgentEvent{Type: agenttypes.AgentEventMessageEnd, Message: &message})
}

func executeToolCalls(
	currentContext agenttypes.AgentContext,
	assistantMessage aitypes.AssistantMessage,
	config agenttypes.AgentLoopConfig,
	signal <-chan struct{},
	emit AgentEventSink,
) (executedToolCallBatch, error) {
	toolCalls := toolCallsOf(assistantMessage)
	hasSequentialToolCall := false
	for _, toolCall := range toolCalls {
		if tool := findTool(currentContext.Tools, toolCall.Name); tool != nil && tool.ExecutionMode == agenttypes.ToolExecutionSequential {
			hasSequentialToolCall = true
			break
		}
	}
	if config.ToolExecution == agenttypes.ToolExecutionSequential || hasSequentialToolCall {
		return executeToolCallsSequential(currentContext, assistantMessage, toolCalls, config, signal, emit)
	}
	return executeToolCallsParallel(currentContext, assistantMessage, toolCalls, config, signal, emit)
}

func executeToolCallsSequential(
	currentContext agenttypes.AgentContext,
	assistantMessage aitypes.AssistantMessage,
	toolCalls []aitypes.ToolCall,
	config agenttypes.AgentLoopConfig,
	signal <-chan struct{},
	emit AgentEventSink,
) (executedToolCallBatch, error) {
	finalizedCalls := []finalizedToolCallOutcome{}
	messages := []aitypes.ToolResultMessage{}

	for _, toolCall := range toolCalls {
		if err := emitToolExecutionStart(toolCall, emit); err != nil {
			return executedToolCallBatch{}, err
		}

		preparation, immediate := prepareToolCall(currentContext, assistantMessage, toolCall, hooksOf(config), signal, nil)
		var finalized finalizedToolCallOutcome
		if immediate != nil {
			finalized = finalizedToolCallOutcome{toolCall: toolCall, result: immediate.result, isError: immediate.isError}
		} else {
			executed, err := executePreparedToolCall(*preparation, signal, emitToolExecutionUpdate(toolCall, emit))
			if err != nil {
				return executedToolCallBatch{}, err
			}
			finalized, err = finalizeExecutedToolCall(currentContext, assistantMessage, *preparation, executed, hooksOf(config), signal)
			if err != nil {
				return executedToolCallBatch{}, err
			}
		}

		if err := emitToolExecutionEnd(finalized, emit); err != nil {
			return executedToolCallBatch{}, err
		}
		toolResultMessage := createToolResultMessage(finalized)
		if err := emitToolResultMessage(toolResultMessage, emit); err != nil {
			return executedToolCallBatch{}, err
		}
		finalizedCalls = append(finalizedCalls, finalized)
		messages = append(messages, toolResultMessage)

		if signalAborted(signal) {
			break
		}
	}

	return executedToolCallBatch{Messages: messages, Terminate: shouldTerminateToolBatch(finalizedCalls)}, nil
}

func executeToolCallsParallel(
	currentContext agenttypes.AgentContext,
	assistantMessage aitypes.AssistantMessage,
	toolCalls []aitypes.ToolCall,
	config agenttypes.AgentLoopConfig,
	signal <-chan struct{},
	emit AgentEventSink,
) (executedToolCallBatch, error) {
	// Emits from concurrent tool jobs are serialized so the event sink and agent
	// state are never mutated concurrently.
	var emitMu sync.Mutex
	safeEmit := func(event agenttypes.AgentEvent) error {
		emitMu.Lock()
		defer emitMu.Unlock()
		return emit(event)
	}

	type finalizeEntry struct {
		immediate *finalizedToolCallOutcome
		job       func() (finalizedToolCallOutcome, error)
	}
	entries := []finalizeEntry{}

	for _, toolCall := range toolCalls {
		if err := safeEmit(agenttypes.AgentEvent{
			Type:       agenttypes.AgentEventToolExecutionStart,
			ToolCallId: stringPtr(toolCall.Id),
			ToolName:   stringPtr(toolCall.Name),
			Args:       rawToAny(toolCall.Arguments),
		}); err != nil {
			return executedToolCallBatch{}, err
		}

		preparation, immediate := prepareToolCall(currentContext, assistantMessage, toolCall, hooksOf(config), signal, nil)
		if immediate != nil {
			finalized := finalizedToolCallOutcome{toolCall: toolCall, result: immediate.result, isError: immediate.isError}
			if err := emitToolExecutionEnd(finalized, safeEmit); err != nil {
				return executedToolCallBatch{}, err
			}
			value := finalized
			entries = append(entries, finalizeEntry{immediate: &value})
			if signalAborted(signal) {
				break
			}
			continue
		}

		prepared := *preparation
		capturedCall := toolCall
		entries = append(entries, finalizeEntry{job: func() (finalizedToolCallOutcome, error) {
			if signalAborted(signal) {
				finalized := finalizedToolCallOutcome{
					toolCall: capturedCall,
					result:   createErrorToolResult("Operation aborted"),
					isError:  true,
				}
				if err := emitToolExecutionEnd(finalized, safeEmit); err != nil {
					return finalizedToolCallOutcome{}, err
				}
				return finalized, nil
			}
			executed, err := executePreparedToolCall(prepared, signal, emitToolExecutionUpdate(capturedCall, safeEmit))
			if err != nil {
				return finalizedToolCallOutcome{}, err
			}
			finalized, err := finalizeExecutedToolCall(currentContext, assistantMessage, prepared, executed, hooksOf(config), signal)
			if err != nil {
				return finalizedToolCallOutcome{}, err
			}
			if err := emitToolExecutionEnd(finalized, safeEmit); err != nil {
				return finalizedToolCallOutcome{}, err
			}
			return finalized, nil
		}})
		if signalAborted(signal) {
			break
		}
	}

	orderedFinalized := make([]finalizedToolCallOutcome, len(entries))
	errs := make([]error, len(entries))
	var wg sync.WaitGroup
	for i, entry := range entries {
		if entry.immediate != nil {
			orderedFinalized[i] = *entry.immediate
			continue
		}
		wg.Add(1)
		go func(index int, job func() (finalizedToolCallOutcome, error)) {
			defer wg.Done()
			outcome, err := job()
			orderedFinalized[index] = outcome
			errs[index] = err
		}(i, entry.job)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return executedToolCallBatch{}, err
		}
	}

	messages := []aitypes.ToolResultMessage{}
	for _, finalized := range orderedFinalized {
		toolResultMessage := createToolResultMessage(finalized)
		if err := emitToolResultMessage(toolResultMessage, safeEmit); err != nil {
			return executedToolCallBatch{}, err
		}
		messages = append(messages, toolResultMessage)
	}

	return executedToolCallBatch{Messages: messages, Terminate: shouldTerminateToolBatch(orderedFinalized)}, nil
}

// --- small helpers -------------------------------------------------------

func agentMessageFrom(message aitypes.Message) agenttypes.AgentMessage {
	return agenttypes.NewAgentMessageFromMessage(message)
}

func agentMessagePtr(message aitypes.AssistantMessage) *agenttypes.AgentMessage {
	value := agentMessageFrom(aitypes.NewAssistantMessageVariant(message))
	return &value
}

func messageRole(message agenttypes.AgentMessage) string {
	if message.Message != nil {
		return message.Message.Role
	}
	if message.Custom != nil {
		return message.Custom.Role
	}
	return ""
}

func systemMessageOf(message agenttypes.AgentMessage) (*aitypes.SystemMessage, bool) {
	if message.Message == nil || message.Message.Role != aitypes.SystemMessageRole || message.Message.System == nil {
		return nil, false
	}
	return message.Message.System, true
}

func transcriptMessages(messages []agenttypes.AgentMessage) []aitypes.Message {
	out := make([]aitypes.Message, 0, len(messages))
	for _, message := range messages {
		if message.Message != nil {
			out = append(out, *message.Message)
		}
	}
	return out
}

func toolCallsOf(message aitypes.AssistantMessage) []aitypes.ToolCall {
	out := []aitypes.ToolCall{}
	for _, block := range message.Content {
		if block.Type == aitypes.ContentTypeToolCall && block.ToolCall != nil {
			out = append(out, *block.ToolCall)
		}
	}
	return out
}

func findTool(tools []agenttypes.AgentTool[any, any], name string) *agenttypes.AgentTool[any, any] {
	for i := range tools {
		if tools[i].Name == name {
			return &tools[i]
		}
	}
	return nil
}

func rawToAny(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil
	}
	return value
}

func stringPtr(value string) *string { return &value }

func signalAborted(signal <-chan struct{}) bool {
	if signal == nil {
		return false
	}
	select {
	case <-signal:
		return true
	default:
		return false
	}
}

// backgroundFromSignal returns a context cancelled when signal fires. It is used
// only to unblock a stream Result after the consumer already got what it needs.
func backgroundFromSignal(signal <-chan struct{}) context.Context {
	if signal == nil {
		return context.Background()
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		select {
		case <-signal:
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx
}
