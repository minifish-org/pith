package utils

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/minifish-org/pith/packages/ai/types"
)

// Mirrors the AssistantMessageEventStream behavior exercised by the
// assistant-message-frame fixture: the first terminal event resolves the
// result and the stream stops accepting events.
func TestAssistantMessageEventStreamDone(t *testing.T) {
	message := types.NewAssistantMessage(types.ApiOpenAICompletions, "fixture", "fixture", 1)
	message.Content = []types.ContentBlock{types.TextBlock("你好")}
	message.StopReason = types.StopReasonStop
	message.Usage = types.Usage{Input: 10, Output: 3, CacheRead: 2, CacheWrite: 1, TotalTokens: 16}

	stream := NewAssistantMessageEventStream()
	stream.Push(types.NewStartEvent(message))
	stream.Push(types.NewDoneEvent(types.StopReasonStop, message))

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	result, err := stream.Result(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if result.StopReason != types.StopReasonStop || len(result.Content) != 1 || result.Content[0].Text == nil {
		t.Fatalf("result = %+v", result)
	}
	if result.Content[0].Text.Text != "你好" {
		t.Fatalf("result text = %q", result.Content[0].Text.Text)
	}
	if result.Usage.TotalTokens != 16 {
		t.Fatalf("usage lost: %+v", result.Usage)
	}

	events := []types.AssistantMessageEvent{}
	for {
		item := <-stream.Next()
		if item.Done {
			break
		}
		events = append(events, item.Value)
	}
	if len(events) != 2 || events[0].Type != types.AssistantEventStart || events[1].Type != types.AssistantEventDone {
		t.Fatalf("events = %+v", events)
	}
}

// Mirrors the error terminal path: an error event resolves the result to the
// failed message and carries the error message.
func TestAssistantMessageEventStreamError(t *testing.T) {
	message := types.NewAssistantMessage(types.ApiOpenAICompletions, "fixture", "fixture", 1)
	message.Content = []types.ContentBlock{types.TextBlock("你好")}
	message.StopReason = types.StopReasonStop
	message.Usage = types.Usage{Input: 10, Output: 3, CacheRead: 2, CacheWrite: 1, TotalTokens: 16}

	errorMessage := message
	errorMessage.StopReason = types.StopReasonError
	text := "fixture"
	errorMessage.ErrorMessage = &text

	stream := NewAssistantMessageEventStream()
	stream.Push(types.NewStartEvent(message))
	stream.Push(types.NewErrorEvent(types.StopReasonError, errorMessage))

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	result, err := stream.Result(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if result.StopReason != types.StopReasonError || result.ErrorMessage == nil || *result.ErrorMessage != "fixture" {
		t.Fatalf("result = %+v", result)
	}

	events := []types.AssistantMessageEvent{}
	for {
		item := <-stream.Next()
		if item.Done {
			break
		}
		events = append(events, item.Value)
	}
	if len(events) != 2 || events[1].Type != types.AssistantEventError {
		t.Fatalf("events = %+v", events)
	}
	if events[1].Error == nil || events[1].Error.ErrorMessage == nil {
		t.Fatalf("error event payload = %+v", events[1])
	}
}

// Verifies the stream rejects late pushes after the first terminal event.
func TestAssistantMessageEventStreamFirstTerminalWins(t *testing.T) {
	message := types.NewAssistantMessage(types.ApiOpenAICompletions, "fixture", "fixture", 1)
	message.StopReason = types.StopReasonStop

	stream := NewAssistantMessageEventStream()
	stream.Push(types.NewDoneEvent(types.StopReasonStop, message))

	late := message
	late.StopReason = types.StopReasonError
	stream.Push(types.NewErrorEvent(types.StopReasonError, late))

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result, err := stream.Result(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if result.StopReason != types.StopReasonStop {
		t.Fatalf("late event replaced the result: %+v", result)
	}
}

// Verifies the assistant stream round trips the full content block vocabulary.
func TestAssistantMessageEventStreamContentVariants(t *testing.T) {
	message := types.NewAssistantMessage(types.ApiAnthropicMessages, types.ProviderAnthropic, "fixture", 1)
	message.Content = []types.ContentBlock{
		types.ThinkingBlockSigned("reasoning", "sig"),
		types.TextBlock("answer"),
		types.ToolCallBlock(types.NewToolCall("c1", "echo", json.RawMessage(`{"n":1}`))),
	}
	message.StopReason = types.StopReasonToolUse

	stream := NewAssistantMessageEventStream()
	stream.Push(types.NewStartEvent(message))
	stream.Push(types.NewToolCallEndEvent(2, *message.Content[2].ToolCall, message))
	stream.Push(types.NewDoneEvent(types.StopReasonToolUse, message))

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result, err := stream.Result(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Content) != 3 {
		t.Fatalf("content = %+v", result.Content)
	}
	if result.Content[0].Thinking == nil || result.Content[0].Thinking.ThinkingSignature == nil {
		t.Fatalf("thinking signature lost: %+v", result.Content[0])
	}
	if result.Content[2].ToolCall == nil || string(result.Content[2].ToolCall.Arguments) != `{"n":1}` {
		t.Fatalf("tool call arguments lost: %+v", result.Content[2])
	}
}

// Verifies the exported factory produces an independent stream.
func TestCreateAssistantMessageEventStreamIsIndependent(t *testing.T) {
	first := CreateAssistantMessageEventStream()
	second := CreateAssistantMessageEventStream()
	if first == second {
		t.Fatal("factory returned the same stream")
	}
	message := types.NewAssistantMessage(types.ApiOpenAICompletions, "fixture", "fixture", 1)
	message.StopReason = types.StopReasonStop
	second.Push(types.NewDoneEvent(types.StopReasonStop, message))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := first.Result(ctx); err == nil {
		t.Fatal("first stream resolved from the second stream's event")
	}
}

// Verifies the context-aware result accessor reports cancellation for a stream
// that has not terminated.
func TestAssistantMessageEventStreamResultCancellation(t *testing.T) {
	stream := NewAssistantMessageEventStream()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := stream.Result(ctx); err == nil {
		t.Fatal("expected cancellation error")
	}
}
