package conformance

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"

	"github.com/minifish-org/pith/packages/agent"

	agenttypes "github.com/minifish-org/pith/packages/agent/types"
	aitypes "github.com/minifish-org/pith/packages/ai/types"
)

// RunCase is the only bridge the frozen judge uses. It translates one input
// operation into calls against the real exported Go SDK and returns the
// normalized result. It never reads expected results, golden files or TS
// sources, and it does not implement SDK behavior itself.
//
// The core-loop batch exercises the low-level agent loop through Agent
// (sequential and parallel tool execution) and the Agent state lifecycle
// (queueing plus reset).
func RunCase(ctx context.Context, input json.RawMessage) (json.RawMessage, error) {
	var envelope struct {
		Op       string `json:"op"`
		File     string `json:"file"`
		Parallel bool   `json:"parallel"`
	}
	if err := json.Unmarshal(input, &envelope); err != nil {
		return nil, fmt.Errorf("conformance: invalid input: %w", err)
	}

	switch envelope.Op {
	case "agent-loop":
		return runAgentLoopCase(envelope.Parallel)
	case "agent-state":
		return runAgentStateCase()
	default:
		return nil, fmt.Errorf("conformance: unsupported operation %q", envelope.Op)
	}
}

type toolResultView struct {
	ToolCallId string                 `json:"toolCallId"`
	Content    []aitypes.ContentBlock `json:"content"`
	IsError    bool                   `json:"isError"`
}

type agentLoopView struct {
	Requests  int              `json:"requests"`
	ToolCalls []string         `json:"toolCalls"`
	Roles     []string         `json:"roles"`
	Results   []toolResultView `json:"results"`
	Ended     string           `json:"ended"`
	Streaming bool             `json:"streaming"`
}

func runAgentLoopCase(parallel bool) (json.RawMessage, error) {
	var counts []string
	var countsMu sync.Mutex
	requests := 0

	makeAssistant := func(content []aitypes.ContentBlock, stopReason aitypes.StopReason) aitypes.AssistantMessage {
		return aitypes.AssistantMessage{
			Role:       aitypes.AssistantMessageRole,
			Content:    content,
			Api:        aitypes.Api("openai-completions"),
			Provider:   aitypes.ProviderId("fixture"),
			Model:      "fixture",
			Usage:      aitypes.Usage{},
			StopReason: stopReason,
			Timestamp:  1,
		}
	}

	streamFn := func(model *aitypes.Model, transcript *aitypes.TranscriptContext, options *aitypes.SimpleStreamOptions) *aitypes.AssistantMessageEventStream {
		stream := aitypes.NewAssistantMessageEventStream()
		var message aitypes.AssistantMessage
		if requests == 0 {
			message = makeAssistant([]aitypes.ContentBlock{
				aitypes.ToolCallBlock(aitypes.NewToolCall("c1", "echo", json.RawMessage(`{"n":1}`))),
				aitypes.ToolCallBlock(aitypes.NewToolCall("c2", "echo", json.RawMessage(`{"n":2}`))),
			}, aitypes.StopReasonToolUse)
		} else {
			message = makeAssistant([]aitypes.ContentBlock{aitypes.TextBlock("done")}, aitypes.StopReasonStop)
		}
		requests++
		stream.Push(aitypes.NewDoneEvent(message.StopReason, message))
		return stream
	}

	schema := json.RawMessage(`{"type":"object","properties":{"n":{"type":"number"}},"required":["n"]}`)
	echo := agenttypes.AgentTool[any, any]{
		Tool:  aitypes.NewTool("echo", "echo", schema),
		Label: "echo",
		Execute: func(toolCallId string, params any, signal <-chan struct{}, onUpdate agenttypes.AgentToolUpdateCallback[any]) (agenttypes.AgentToolResult[any], error) {
			value := ""
			if object, ok := params.(map[string]any); ok {
				value = fmt.Sprint(object["n"])
			}
			countsMu.Lock()
			counts = append(counts, value)
			countsMu.Unlock()
			return agenttypes.AgentToolResult[any]{
				Content: []aitypes.ContentBlock{aitypes.TextBlock(value)},
				Details: map[string]any{},
			}, nil
		},
	}

	toolExecution := agenttypes.ToolExecutionSequential
	if parallel {
		toolExecution = agenttypes.ToolExecutionParallel
	}

	runtime, err := agent.NewAgent(agent.AgentOptions{
		StreamFn:      streamFn,
		ToolExecution: toolExecution,
		InitialState:  &agent.AgentInitialState{Tools: []agenttypes.AgentTool[any, any]{echo}},
	})
	if err != nil {
		return nil, err
	}

	var events []string
	var eventsMu sync.Mutex
	runtime.Subscribe(func(event agenttypes.AgentEvent, signal <-chan struct{}) error {
		eventsMu.Lock()
		events = append(events, event.Type)
		eventsMu.Unlock()
		return nil
	})

	user := aitypes.NewUserMessage("go", 1)
	if err := runtime.Prompt([]agenttypes.AgentMessage{wrapMessage(aitypes.NewUserMessageVariant(user))}); err != nil {
		return nil, err
	}

	state := runtime.State()
	roles := make([]string, 0, len(state.Messages))
	results := []toolResultView{}
	for _, message := range state.Messages {
		roles = append(roles, messageRole(message))
		if message.Message != nil && message.Message.Role == aitypes.ToolResultMessageRole && message.Message.ToolResult != nil {
			result := message.Message.ToolResult
			results = append(results, toolResultView{
				ToolCallId: result.ToolCallId,
				Content:    result.Content,
				IsError:    result.IsError,
			})
		}
	}
	sort.SliceStable(results, func(i, j int) bool { return results[i].ToolCallId < results[j].ToolCallId })

	countsMu.Lock()
	toolCalls := append([]string(nil), counts...)
	countsMu.Unlock()
	sort.Strings(toolCalls)

	ended := ""
	if len(events) > 0 {
		ended = events[len(events)-1]
	}

	view := agentLoopView{
		Requests:  requests,
		ToolCalls: toolCalls,
		Roles:     roles,
		Results:   results,
		Ended:     ended,
		Streaming: state.IsStreaming,
	}
	return json.Marshal(view)
}

func runAgentStateCase() (json.RawMessage, error) {
	streamFn := func(model *aitypes.Model, transcript *aitypes.TranscriptContext, options *aitypes.SimpleStreamOptions) *aitypes.AssistantMessageEventStream {
		stream := aitypes.NewAssistantMessageEventStream()
		message := aitypes.NewAssistantMessage(model.Api, model.Provider, model.Id, 0)
		message.StopReason = aitypes.StopReasonError
		text := "unexpected model"
		message.ErrorMessage = &text
		stream.Push(aitypes.NewErrorEvent(aitypes.StopReasonError, message))
		return stream
	}

	systemPrompt := "system"
	runtime, err := agent.NewAgent(agent.AgentOptions{
		StreamFn: streamFn,
		InitialState: &agent.AgentInitialState{
			SystemPrompt:  &systemPrompt,
			ThinkingLevel: agenttypes.ThinkingLow,
			Messages:      []agenttypes.AgentMessage{wrapMessage(aitypes.NewUserMessageVariant(aitypes.NewUserMessage("hi", 1)))},
		},
	})
	if err != nil {
		return nil, err
	}

	beforeState := runtime.State()
	before := map[string]any{
		"messages":      beforeState.Messages,
		"thinkingLevel": beforeState.ThinkingLevel,
	}

	runtime.Steer(wrapMessage(aitypes.NewUserMessageVariant(aitypes.NewUserMessage("queued", 2))))
	queued := runtime.HasQueuedMessages()

	if err := runtime.Reset(); err != nil {
		return nil, err
	}
	afterState := runtime.State()
	after := map[string]any{
		"messages":         afterState.Messages,
		"isStreaming":      afterState.IsStreaming,
		"pendingToolCalls": afterState.PendingToolCalls,
		"queued":           runtime.HasQueuedMessages(),
	}

	return json.Marshal(map[string]any{
		"before": before,
		"queued": queued,
		"after":  after,
	})
}

func wrapMessage(message aitypes.Message) agenttypes.AgentMessage {
	return agenttypes.NewAgentMessageFromMessage(message)
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
