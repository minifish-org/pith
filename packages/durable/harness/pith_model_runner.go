package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/minifish-org/pith/packages/ai/api"
	"github.com/minifish-org/pith/packages/ai/types"
)

// pithModelRunner is the production Pith model adapter.
type pithModelRunner struct {
	resolve func(context.Context, ModelRef) (*types.Model, error)
	options *types.SimpleStreamOptions
}

// NewPithModelRunner builds a ModelRunner backed by the real Pith provider
// implementation for a model's API. resolve returns the model descriptor for a
// reference; base carries caller-curated default stream options.
func NewPithModelRunner(resolve func(context.Context, ModelRef) (*types.Model, error), base *types.SimpleStreamOptions) ModelRunner {
	return &pithModelRunner{resolve: resolve, options: base}
}

// Resolve returns the model descriptor for a reference.
func (r *pithModelRunner) Resolve(ctx context.Context, ref ModelRef) (*types.Model, error) {
	if r.resolve == nil {
		return nil, errors.New("harness: no model resolver configured")
	}
	return r.resolve(ctx, ref)
}

func (r *pithModelRunner) baseOptions() types.SimpleStreamOptions {
	var options types.SimpleStreamOptions
	if r.options != nil {
		options = *r.options
	}
	return options
}

// Run streams one request through the model's provider.
func (r *pithModelRunner) Run(ctx context.Context, request ModelRequest, emit func(types.AssistantMessage) error) (types.AssistantMessage, error) {
	model, err := r.Resolve(ctx, request.Model)
	if err != nil {
		return types.AssistantMessage{}, err
	}
	streams := streamsForApi(model.Api)
	if streams == nil {
		return types.AssistantMessage{}, fmt.Errorf("harness: no provider stream for api %q", model.Api)
	}
	options := r.baseOptions()
	options.Signal = ctx.Done()
	options.TelemetryContext = ctx
	if request.ThinkingLevel != "" && request.ThinkingLevel != types.ThinkingOff {
		level := request.ThinkingLevel
		options.Reasoning = &level
	}
	if request.Summary && options.MaxTokens == nil {
		// Summarization requests keep the caller's options; maxTokens is set by
		// the compaction task through Options.MaxTokens when configured.
	}
	transcript := types.NewTranscriptContext(append([]types.Message{}, request.Messages...))
	stream := streams.StreamSimple(model, transcript, &options)
	// The provider emits normalized events whose Partial aliases a message the
	// provider goroutine keeps mutating. Reading that live object from here races
	// with the provider, so build and stream a consumer-owned snapshot from the
	// events' own value fields instead of dereferencing the shared partial.
	snapshot := newStreamSnapshot(model)
	for {
		item := <-stream.Next()
		if item.Done {
			break
		}
		event := item.Value
		if ctx.Err() != nil {
			return types.AssistantMessage{}, ctx.Err()
		}
		snapshot.apply(event)
		if emit != nil && len(snapshot.message.Content) > 0 {
			if err := emit(snapshot.cloneMessage()); err != nil {
				return types.AssistantMessage{}, err
			}
		}
	}
	if ctx.Err() != nil {
		return types.AssistantMessage{}, ctx.Err()
	}
	return stream.Result(ctx)
}

// streamSnapshot is a consumer-owned AssistantMessage assembled from provider
// events. It never reads the provider's shared partial, so streaming updates
// are race-free while still reflecting real incremental progress.
//
// The normalized provider events carry fresh per-event scalars (Delta,
// ContentIndex, ContentBlock, ToolCall) and are handled generically for every
// provider API: text and thinking deltas accumulate, end events apply their
// authoritative content, and completed tool calls are copied with their
// arguments detached.
type streamSnapshot struct {
	message types.AssistantMessage
	args    map[int]*strings.Builder
}

func newStreamSnapshot(model *types.Model) *streamSnapshot {
	return &streamSnapshot{
		message: types.AssistantMessage{
			Role:       types.AssistantMessageRole,
			Api:        model.Api,
			Provider:   model.Provider,
			Model:      model.Id,
			StopReason: types.StopReasonPending,
		},
		args: map[int]*strings.Builder{},
	}
}

func (s *streamSnapshot) ensure(index int, kind types.ContentBlockType) {
	for len(s.message.Content) <= index {
		s.message.Content = append(s.message.Content, types.ContentBlock{})
	}
	block := &s.message.Content[index]
	if block.Type != kind {
		*block = types.ContentBlock{Type: kind}
	}
	switch kind {
	case types.ContentTypeText:
		if block.Text == nil {
			block.Text = &types.TextContent{Type: types.ContentTypeText}
		}
	case types.ContentTypeThinking:
		if block.Thinking == nil {
			block.Thinking = &types.ThinkingContent{Type: types.ContentTypeThinking}
		}
	case types.ContentTypeToolCall:
		if block.ToolCall == nil {
			block.ToolCall = &types.ToolCall{Type: types.ContentTypeToolCall, Arguments: json.RawMessage("{}")}
		}
	}
}

func (s *streamSnapshot) apply(event types.AssistantMessageEvent) {
	index := 0
	if event.ContentIndex != nil {
		index = *event.ContentIndex
	}
	switch event.Type {
	case types.AssistantEventTextStart:
		s.ensure(index, types.ContentTypeText)
	case types.AssistantEventTextDelta:
		s.ensure(index, types.ContentTypeText)
		if event.Delta != nil {
			s.message.Content[index].Text.Text += *event.Delta
		}
	case types.AssistantEventTextEnd:
		s.ensure(index, types.ContentTypeText)
		if event.ContentBlock != nil {
			s.message.Content[index].Text.Text = *event.ContentBlock
		}
	case types.AssistantEventThinkingStart:
		s.ensure(index, types.ContentTypeThinking)
	case types.AssistantEventThinkingDelta:
		s.ensure(index, types.ContentTypeThinking)
		if event.Delta != nil {
			s.message.Content[index].Thinking.Thinking += *event.Delta
		}
	case types.AssistantEventThinkingEnd:
		s.ensure(index, types.ContentTypeThinking)
		if event.ContentBlock != nil {
			s.message.Content[index].Thinking.Thinking = *event.ContentBlock
		}
	case types.AssistantEventToolCallStart:
		s.ensure(index, types.ContentTypeToolCall)
	case types.AssistantEventToolCallDelta:
		s.ensure(index, types.ContentTypeToolCall)
		if event.Delta != nil {
			builder := s.args[index]
			if builder == nil {
				builder = &strings.Builder{}
				s.args[index] = builder
			}
			builder.WriteString(*event.Delta)
			// Only publish arguments once they form a complete JSON value; a
			// partial fragment would fail to marshal and drop the whole snapshot.
			if json.Valid([]byte(builder.String())) {
				s.message.Content[index].ToolCall.Arguments = json.RawMessage(builder.String())
			}
		}
	case types.AssistantEventToolCallEnd:
		s.ensure(index, types.ContentTypeToolCall)
		if event.ToolCall != nil {
			call := *event.ToolCall
			call.Arguments = append(json.RawMessage(nil), event.ToolCall.Arguments...)
			s.message.Content[index].ToolCall = &call
		}
	}
}

// cloneMessage detaches the accumulated message from this builder so a caller
// that retains it cannot observe later mutations on the next event.
func (s *streamSnapshot) cloneMessage() types.AssistantMessage {
	out := s.message
	out.Content = make([]types.ContentBlock, len(s.message.Content))
	for index, block := range s.message.Content {
		cloned := block
		switch {
		case block.Text != nil:
			value := *block.Text
			cloned.Text = &value
		case block.Thinking != nil:
			value := *block.Thinking
			cloned.Thinking = &value
		case block.ToolCall != nil:
			value := *block.ToolCall
			value.Arguments = append(json.RawMessage(nil), block.ToolCall.Arguments...)
			cloned.ToolCall = &value
		}
		out.Content[index] = cloned
	}
	return out
}

// Poll fetches a deferred response.
func (r *pithModelRunner) Poll(ctx context.Context, ref ModelRef, handle types.DeferredHandle) (types.AssistantMessage, error) {
	model, err := r.Resolve(ctx, ref)
	if err != nil {
		return types.AssistantMessage{}, err
	}
	streams := streamsForApi(model.Api)
	if streams == nil {
		return types.AssistantMessage{}, fmt.Errorf("harness: no provider stream for api %q", model.Api)
	}
	options := types.DeferredFetchOptions{ProviderRequestOptions: types.ProviderRequestOptions{Signal: ctx.Done(), TelemetryContext: ctx}}
	stream, err := streams.FetchDeferred(model, handle, &options)
	if err != nil {
		return types.AssistantMessage{}, err
	}
	for {
		item := <-stream.Next()
		if item.Done {
			break
		}
		if ctx.Err() != nil {
			return types.AssistantMessage{}, ctx.Err()
		}
	}
	return stream.Result(ctx)
}

// Cancel cancels a deferred response.
func (r *pithModelRunner) Cancel(ctx context.Context, ref ModelRef, handle types.DeferredHandle) error {
	model, err := r.Resolve(ctx, ref)
	if err != nil {
		return err
	}
	streams := streamsForApi(model.Api)
	if streams == nil {
		return fmt.Errorf("harness: no provider stream for api %q", model.Api)
	}
	options := types.DeferredCancelOptions{ProviderRequestOptions: types.ProviderRequestOptions{Signal: ctx.Done(), TelemetryContext: ctx}}
	return streams.CancelDeferred(model, handle, &options)
}

func streamsForApi(value types.Api) types.ProviderStreams {
	switch value {
	case types.ApiOpenAICompletions:
		return api.OpenAICompletionsApi()
	case types.ApiOpenAIResponses:
		return api.OpenAIResponsesApi()
	case types.ApiOpenAICodexResponses:
		return api.OpenAICodexResponsesApi()
	case types.ApiAzureOpenAIResponses:
		return api.AzureOpenAIResponsesApi()
	case types.ApiAnthropicMessages:
		return api.AnthropicMessagesApi()
	case types.ApiGoogleGenerativeAI:
		return api.GoogleGenerativeAIApi()
	case types.ApiGoogleVertex:
		return api.GoogleVertexApi()
	case types.ApiMistralConversations:
		return api.MistralConversationsApi()
	case types.ApiBedrockConverseStream:
		return api.BedrockConverseStreamApi()
	case types.ApiPiMessages:
		return api.PiMessagesApi()
	default:
		return nil
	}
}

var _ ModelRunner = (*pithModelRunner)(nil)
var _ DeferredModelRunner = (*pithModelRunner)(nil)
