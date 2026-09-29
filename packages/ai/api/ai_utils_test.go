package api

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/minifish-org/pith/packages/ai/types"
)

// This file ports the deterministic scenarios of the upstream API-level tests
// for packages/ai/src/api/{constrained-sampling,lazy,simple-options,
// transform-messages}.ts at revision
// f07218c4d4bbc12bef056a7058c3dd49dfe41abe. Live-credential scenarios from the
// upstream suite become deterministic local scenarios here.

func schemaObject(t *testing.T, raw string) json.RawMessage {
	t.Helper()
	if !json.Valid([]byte(raw)) {
		t.Fatalf("invalid test schema: %s", raw)
	}
	return json.RawMessage(raw)
}

func TestMakeStrictJSONSchemaRejectsUnsupportedKeywords(t *testing.T) {
	_, err := MakeStrictJSONSchema(schemaObject(t, `{"type":"object","properties":{"a":{"type":"string","$ref":"#/x"}}}`))
	if err == nil {
		t.Fatal("expected a $ref schema to be rejected")
	}
	_, err = MakeStrictJSONSchema(schemaObject(t, `{"type":"object","properties":{"a":{"type":"string","allOf":[]}}}`))
	if err == nil {
		t.Fatal("expected an allOf schema to be rejected")
	}
	_, err = MakeStrictJSONSchema(schemaObject(t, `true`))
	if err == nil {
		t.Fatal("expected a boolean schema to be rejected")
	}
	_, err = MakeStrictJSONSchema(schemaObject(t, `{"type":"string"}`))
	if err == nil {
		t.Fatal("expected a non-object root schema to be rejected")
	}
}

func TestMakeStrictJSONSchemaRequiresAllProperties(t *testing.T) {
	strict, err := MakeStrictJSONSchema(schemaObject(t, `{"type":"object","properties":{"a":{"type":"string"},"b":{"type":"number"}},"required":["a"]}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(strict, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed["additionalProperties"] != false {
		t.Fatalf("expected additionalProperties false, got %v", parsed["additionalProperties"])
	}
	required, ok := parsed["required"].([]any)
	if !ok || len(required) != 2 {
		t.Fatalf("expected both properties required, got %v", parsed["required"])
	}
	properties := parsed["properties"].(map[string]any)
	// The optional property must become non-nullable anyOf.
	if _, ok := properties["b"].(map[string]any)["anyOf"]; !ok {
		t.Fatalf("expected optional property to become anyOf, got %v", properties["b"])
	}
}

func TestGetJSONSchemaToolParametersOnlyStrictWhenTrue(t *testing.T) {
	tool := types.NewTool("echo", "", schemaObject(t, `{"type":"object","properties":{"a":{"type":"string"}}}`))
	strict := true
	converted, err := GetJSONSchemaToolParameters(tool, &strict)
	if err != nil {
		t.Fatal(err)
	}
	if string(converted) == string(tool.Input.Schema) {
		t.Fatal("expected a converted schema")
	}
	converted, err = GetJSONSchemaToolParameters(tool, nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(converted) != string(tool.Input.Schema) {
		t.Fatal("expected the original schema when strict is not true")
	}
}

func TestResolveGrammarConstrainedSampling(t *testing.T) {
	tool := types.NewTool("setValue", "", schemaObject(t, `{"type":"object","properties":{"value":{"type":"string"}},"required":["value"]}`))
	values := map[string]string{
		string(types.GrammarFormatOpenAILark): "[0-9]+",
	}
	config := types.NewGrammarSampling(types.GrammarVariants{types.GrammarFormatOpenAILark: values[string(types.GrammarFormatOpenAILark)]})
	tool.ConstrainedSampling = &config

	resolved, err := ResolveGrammarConstrainedSampling(tool, true)
	if err != nil {
		t.Fatal(err)
	}
	if resolved == nil || resolved.Format != "lark" || resolved.InputProperty != "value" {
		t.Fatalf("unexpected resolution: %+v", resolved)
	}

	unsupported, err := ResolveGrammarConstrainedSampling(tool, false)
	if err != nil {
		t.Fatal(err)
	}
	if unsupported != nil {
		t.Fatal("expected no grammar when OpenAI grammar tools are unsupported")
	}

	properties, err := CreateGrammarToolInputProperties([]types.Tool{tool}, true)
	if err != nil {
		t.Fatal(err)
	}
	if properties["setValue"] != "value" {
		t.Fatalf("unexpected input properties: %v", properties)
	}
}

func TestResolveJSONSchemaStrictSamplingRequiresStrict(t *testing.T) {
	tool := types.NewTool("echo", "", schemaObject(t, `{"type":"object","properties":{"a":{"type":"string","$ref":"#/x"}}}`))
	config := types.NewJSONSchemaSampling(types.ConstrainedStrictRequire)
	tool.ConstrainedSampling = &config

	if _, err := ResolveJSONSchemaStrictSampling(tool, true); err == nil {
		t.Fatal("expected an unsupported strict schema to fail when strict is required")
	}

	prefer := types.NewJSONSchemaSampling(types.ConstrainedStrictPrefer)
	tool.ConstrainedSampling = &prefer
	resolved, err := ResolveJSONSchemaStrictSampling(tool, true)
	if err != nil {
		t.Fatal(err)
	}
	if resolved != nil {
		t.Fatalf("expected no strict mode for a preferred unsupported schema, got %v", *resolved)
	}
}

func TestThinkingBudgetHelpers(t *testing.T) {
	if DefaultThinkingBudgets.Minimal == nil || *DefaultThinkingBudgets.Minimal != 1024 {
		t.Fatalf("unexpected minimal budget: %v", DefaultThinkingBudgets.Minimal)
	}
	if got := ThinkingBudgetForLevel(types.ThinkingMedium, nil); got != 8192 {
		t.Fatalf("unexpected medium budget: %d", got)
	}
	// xhigh and max clamp back to high.
	if got := ThinkingBudgetForLevel(types.ThinkingXHigh, nil); got != 16384 {
		t.Fatalf("unexpected xhigh budget: %d", got)
	}
	custom := types.ThinkingBudgets{High: intPointer(5)}
	if got := ThinkingBudgetForLevel(types.ThinkingHigh, &custom); got != 5 {
		t.Fatalf("expected the custom override, got %d", got)
	}
	if got := ClampThinkingBudgetToAnswerRoom(4096, 2048); got != 1024 {
		t.Fatalf("expected 1024 answer room, got %v", got)
	}
	result := AdjustMaxTokensForThinking(nil, 8000, types.ThinkingHigh, nil)
	if result.MaxTokens != 8000 || result.ThinkingBudget != 6976 {
		t.Fatalf("unexpected adjustment: %+v", result)
	}
}

func intPointer(value int) *int { return &value }

func TestClampMaxTokensToContextReservesSafetyMargin(t *testing.T) {
	model := &types.Model{ContextWindow: 10000, MaxTokens: 8000}
	context := []types.Message{types.NewUserMessageVariant(types.NewUserMessage("hello", 1))}
	clamped := ClampMaxTokensToContext(model, context, 8000)
	// The 4096-token safety margin is always reserved, so even a tiny context
	// clamps the request budget below the model maximum.
	if clamped != 5902 {
		t.Fatalf("expected the safety-margin budget 5902, got %v", clamped)
	}
	// A context that fills most of the window shrinks the budget.
	long := make([]byte, 0)
	for i := 0; i < 6000; i++ {
		long = append(long, 'x')
	}
	full := []types.Message{types.NewUserMessageVariant(types.NewUserMessage(string(long), 1))}
	clamped = ClampMaxTokensToContext(model, full, 8000)
	if clamped >= 8000 {
		t.Fatalf("expected a clamped budget, got %v", clamped)
	}
}

func TestTransformMessagesDropsCrossModelSignatures(t *testing.T) {
	model := &types.Model{Id: "target", Api: types.ApiAnthropicMessages, Provider: types.ProviderAnthropic, Input: []types.ModelInputModality{types.ModelInputText}}
	signature := "sig"
	thinking := types.ThinkingBlockSigned("reasoning", signature)
	call := types.NewToolCall("call-1", "echo", json.RawMessage(`{"n":1}`))
	thought := "thought-sig"
	call.ThoughtSignature = &thought
	assistant := types.NewAssistantMessage(types.ApiOpenAIResponses, types.ProviderOpenAI, "source", 1)
	assistant.Content = []types.ContentBlock{thinking, types.ToolCallBlock(call)}
	assistant.StopReason = types.StopReasonToolUse

	result := TransformMessages([]types.Message{types.NewAssistantMessageVariant(assistant)}, model, nil)
	// The orphaned tool call gets a synthetic error result appended, so the
	// transformed transcript carries the assistant and that placeholder.
	if len(result) != 2 {
		t.Fatalf("expected the assistant plus a synthetic tool result, got %d", len(result))
	}
	transformed := result[0].Assistant
	// Cross-model thinking becomes plain text.
	if transformed.Content[0].Type != types.ContentTypeText {
		t.Fatalf("expected thinking to become text, got %s", transformed.Content[0].Type)
	}
	if transformed.Content[1].ToolCall.ThoughtSignature != nil {
		t.Fatal("expected the thought signature to be dropped cross-model")
	}
}

func TestTransformMessagesDowngradesImagesForNonVisionModel(t *testing.T) {
	model := &types.Model{Id: "text-only", Api: types.ApiOpenAICompletions, Provider: types.ProviderOpenAI, Input: []types.ModelInputModality{types.ModelInputText}}
	user := types.NewUserMessageBlocks([]types.ContentBlock{
		types.TextBlock("look"),
		types.ImageBlock("AAAA", "image/png"),
		types.ImageBlock("BBBB", "image/png"),
	}, 1)
	result := TransformMessages([]types.Message{types.NewUserMessageVariant(user)}, model, nil)
	blocks := result[0].User.Content.Blocks
	if len(blocks) != 2 {
		t.Fatalf("expected two blocks after collapsing images, got %d", len(blocks))
	}
	if blocks[1].Text == nil || blocks[1].Text.Text != NonVisionUserImagePlaceholder {
		t.Fatalf("unexpected placeholder: %+v", blocks[1])
	}
}

func TestTransformMessagesSynthesizesOrphanResults(t *testing.T) {
	model := &types.Model{Id: "m", Api: types.ApiAnthropicMessages, Provider: types.ProviderAnthropic, Input: []types.ModelInputModality{types.ModelInputText}}
	assistant := types.NewAssistantMessage(types.ApiAnthropicMessages, types.ProviderAnthropic, "m", 1)
	assistant.Content = []types.ContentBlock{types.ToolCallBlock(types.NewToolCall("call-1", "echo", json.RawMessage(`{}`)))}
	assistant.StopReason = types.StopReasonToolUse

	result := TransformMessages([]types.Message{types.NewAssistantMessageVariant(assistant)}, model, nil)
	if len(result) != 2 {
		t.Fatalf("expected a synthetic tool result, got %d messages", len(result))
	}
	if result[1].Role != types.ToolResultMessageRole {
		t.Fatalf("expected a toolResult message, got %s", result[1].Role)
	}
	if !result[1].ToolResult.IsError {
		t.Fatal("expected the synthetic result to be an error")
	}
}

func TestTransformMessagesSkipsErroredAssistants(t *testing.T) {
	model := &types.Model{Id: "m", Api: types.ApiAnthropicMessages, Provider: types.ProviderAnthropic, Input: []types.ModelInputModality{types.ModelInputText}}
	assistant := types.NewAssistantMessage(types.ApiAnthropicMessages, types.ProviderAnthropic, "m", 1)
	assistant.StopReason = types.StopReasonError
	result := TransformMessages([]types.Message{types.NewAssistantMessageVariant(assistant)}, model, nil)
	if len(result) != 0 {
		t.Fatalf("expected errored assistant to be dropped, got %d messages", len(result))
	}
}

func TestLazyStreamForwardsSetupEvents(t *testing.T) {
	model := &types.Model{Id: "m", Api: types.ApiOpenAICompletions, Provider: types.ProviderOpenAI}
	stream := LazyStream(context.Background(), model, func(ctx context.Context) (*types.AssistantMessageEventStream, error) {
		inner := types.NewAssistantMessageEventStream()
		message := types.NewAssistantMessage(types.ApiOpenAICompletions, types.ProviderOpenAI, "m", 1)
		message.StopReason = types.StopReasonStop
		inner.Push(types.NewStartEvent(message))
		inner.Push(types.NewDoneEvent(types.StopReasonStop, message))
		return inner, nil
	})

	events := []types.AssistantMessageEventType{}
	for {
		item := <-stream.Next()
		if item.Done {
			break
		}
		events = append(events, item.Value.Type)
	}
	if len(events) != 2 || events[0] != types.AssistantEventStart || events[1] != types.AssistantEventDone {
		t.Fatalf("unexpected events: %v", events)
	}
	result, err := stream.Result(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.StopReason != types.StopReasonStop {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestLazyStreamSetupFailureTerminatesWithError(t *testing.T) {
	model := &types.Model{Id: "m", Api: types.ApiOpenAICompletions, Provider: types.ProviderOpenAI}
	stream := LazyStream(context.Background(), model, func(ctx context.Context) (*types.AssistantMessageEventStream, error) {
		return nil, context.DeadlineExceeded
	})
	item := <-stream.Next()
	if item.Done || item.Value.Type != types.AssistantEventError {
		t.Fatalf("expected an error event, got %+v", item)
	}
	result, err := stream.Result(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.StopReason != types.StopReasonError {
		t.Fatalf("expected an error result, got %+v", result)
	}
}
