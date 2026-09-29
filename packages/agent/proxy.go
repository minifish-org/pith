// This file is a Go port of packages/agent/src/proxy.ts from Pi at revision
// f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// The proxy stream function routes LLM calls through a server that manages auth
// and forwards requests to providers. The server strips the partial field from
// delta events to reduce bandwidth; the client reconstructs the partial message
// from the event stream.
package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	aitypes "github.com/minifish-org/pith/packages/ai/types"
	aiutils "github.com/minifish-org/pith/packages/ai/utils"
)

// ProxyAssistantMessageEvent is one event sent by the proxy server with the
// partial field stripped.
type ProxyAssistantMessageEvent struct {
	Type                  string              `json:"type"`
	ContentIndex          *int                `json:"contentIndex,omitempty"`
	Delta                 *string             `json:"delta,omitempty"`
	ContentSignature      *string             `json:"contentSignature,omitempty"`
	Id                    *string             `json:"id,omitempty"`
	ToolName              *string             `json:"toolName,omitempty"`
	ToolCall              *aitypes.ToolCall   `json:"toolCall,omitempty"`
	Reason                *aitypes.StopReason `json:"reason,omitempty"`
	Usage                 *aitypes.Usage      `json:"usage,omitempty"`
	ErrorMessage          *string             `json:"errorMessage,omitempty"`
	ProviderThinkingLevel *string             `json:"providerThinkingLevel,omitempty"`
}

// ProxyStreamOptions are the options for streamProxy. Only the serializable
// subset is forwarded to the proxy server.
type ProxyStreamOptions struct {
	Temperature     *float64
	SamplingParams  map[string]any
	MaxTokens       *int
	Reasoning       *aitypes.ThinkingLevel
	CacheRetention  *aitypes.CacheRetention
	SessionId       *string
	Headers         aitypes.ProviderHeaders
	Metadata        map[string]any
	Transport       *aitypes.Transport
	ThinkingBudgets *aitypes.ThinkingBudgets
	MaxRetryDelayMs *int

	// Signal is the local abort signal for the proxy request.
	Signal <-chan struct{}
	// AuthToken is the auth token for the proxy server.
	AuthToken string
	// ProxyUrl is the proxy server URL (for example, "https://genai.example.com").
	ProxyUrl string
}

var errProxyAborted = errors.New("Request aborted by user")

// StreamProxy proxies a streaming request through a server instead of calling
// LLM providers directly. The returned stream follows the assistant message
// event protocol and always terminates with a done or error event.
func StreamProxy(model *aitypes.Model, context *aitypes.TranscriptContext, options ProxyStreamOptions) *aitypes.AssistantMessageEventStream {
	stream := aitypes.NewAssistantMessageEventStream()
	go func() {
		runProxyStream(stream, model, context, options)
	}()
	return stream
}

func runProxyStream(
	stream *aitypes.AssistantMessageEventStream,
	model *aitypes.Model,
	context *aitypes.TranscriptContext,
	options ProxyStreamOptions,
) {
	partial := aitypes.NewAssistantMessage(model.Api, model.Provider, model.Id, float64(time.Now().UnixMilli()))
	state := &proxyStreamState{partial: partial, partialJSON: map[int]string{}}

	ctx, cancel := proxyContext(options.Signal)
	defer cancel()

	response, err := proxyPost(ctx, model, context, options)
	if err != nil {
		reason := aitypes.StopReasonError
		if signalAborted(options.Signal) {
			reason = aitypes.StopReasonAborted
		}
		state.fail(stream, reason, err.Error())
		return
	}
	defer response.Body.Close()

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		message := fmt.Sprintf("Proxy error: %d %s", response.StatusCode, response.Status)
		var errorData struct {
			Error string `json:"error"`
		}
		if decodeErr := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&errorData); decodeErr == nil && errorData.Error != "" {
			message = "Proxy error: " + errorData.Error
		}
		state.fail(stream, aitypes.StopReasonError, message)
		return
	}

	sawTerminalEvent := false
	processLine := func(line string) {
		if !strings.HasPrefix(line, "data: ") {
			return
		}
		data := strings.TrimSpace(line[len("data: "):])
		if data == "" {
			return
		}
		var proxyEvent ProxyAssistantMessageEvent
		if err := json.Unmarshal([]byte(data), &proxyEvent); err != nil {
			return
		}
		event, ok := state.process(proxyEvent)
		if !ok {
			return
		}
		if event.Type == aitypes.AssistantEventDone || event.Type == aitypes.AssistantEventError {
			sawTerminalEvent = true
		}
		stream.Push(event)
	}

	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		if signalAborted(options.Signal) {
			state.fail(stream, aitypes.StopReasonAborted, errProxyAborted.Error())
			return
		}
		processLine(scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		reason := aitypes.StopReasonError
		if signalAborted(options.Signal) {
			reason = aitypes.StopReasonAborted
		}
		state.fail(stream, reason, err.Error())
		return
	}
	if signalAborted(options.Signal) {
		state.fail(stream, aitypes.StopReasonAborted, errProxyAborted.Error())
		return
	}

	if !sawTerminalEvent {
		// A clean EOF without a done/error event means the server dropped the
		// response mid-stream. Surface it as an error instead of leaving
		// consumers waiting on a result that never arrives.
		state.partial.StopReason = aitypes.StopReasonError
		message := "Connection closed by proxy server before the response completed"
		state.partial.ErrorMessage = &message
		stream.Push(aitypes.NewErrorEvent(aitypes.StopReasonError, state.clonePartial()))
	}
	stream.End(nil)
}

type proxyStreamState struct {
	partial     aitypes.AssistantMessage
	partialJSON map[int]string
}

func (s *proxyStreamState) fail(stream *aitypes.AssistantMessageEventStream, reason aitypes.StopReason, message string) {
	s.partial.StopReason = reason
	s.partial.ErrorMessage = &message
	stream.Push(aitypes.NewErrorEvent(reason, s.clonePartial()))
	stream.End(nil)
}

func (s *proxyStreamState) clonePartial() aitypes.AssistantMessage {
	return cloneAssistantMessage(s.partial)
}

func (s *proxyStreamState) process(proxyEvent ProxyAssistantMessageEvent) (aitypes.AssistantMessageEvent, bool) {
	switch proxyEvent.Type {
	case "start":
		return aitypes.NewStartEvent(s.clonePartial()), true

	case "text_start":
		index := contentIndexOf(proxyEvent)
		s.setContent(index, aitypes.TextBlock(""))
		return aitypes.NewTextStartEvent(index, s.clonePartial()), true

	case "text_delta":
		index := contentIndexOf(proxyEvent)
		block := s.contentAt(index)
		if block == nil || block.Type != aitypes.ContentTypeText || block.Text == nil {
			return aitypes.AssistantMessageEvent{}, false
		}
		block.Text.Text += stringValue(proxyEvent.Delta)
		return aitypes.NewTextDeltaEvent(index, stringValue(proxyEvent.Delta), s.clonePartial()), true

	case "text_end":
		index := contentIndexOf(proxyEvent)
		block := s.contentAt(index)
		if block == nil || block.Type != aitypes.ContentTypeText || block.Text == nil {
			return aitypes.AssistantMessageEvent{}, false
		}
		if proxyEvent.ContentSignature != nil {
			signature := *proxyEvent.ContentSignature
			block.Text.TextSignature = &signature
		}
		return aitypes.NewTextEndEvent(index, block.Text.Text, s.clonePartial()), true

	case "thinking_start":
		index := contentIndexOf(proxyEvent)
		s.setContent(index, aitypes.ThinkingBlock(""))
		return aitypes.NewThinkingStartEvent(index, s.clonePartial()), true

	case "thinking_delta":
		index := contentIndexOf(proxyEvent)
		block := s.contentAt(index)
		if block == nil || block.Type != aitypes.ContentTypeThinking || block.Thinking == nil {
			return aitypes.AssistantMessageEvent{}, false
		}
		block.Thinking.Thinking += stringValue(proxyEvent.Delta)
		return aitypes.NewThinkingDeltaEvent(index, stringValue(proxyEvent.Delta), s.clonePartial()), true

	case "thinking_end":
		index := contentIndexOf(proxyEvent)
		block := s.contentAt(index)
		if block == nil || block.Type != aitypes.ContentTypeThinking || block.Thinking == nil {
			return aitypes.AssistantMessageEvent{}, false
		}
		if proxyEvent.ContentSignature != nil {
			signature := *proxyEvent.ContentSignature
			block.Thinking.ThinkingSignature = &signature
		}
		return aitypes.NewThinkingEndEvent(index, block.Thinking.Thinking, s.clonePartial()), true

	case "toolcall_start":
		index := contentIndexOf(proxyEvent)
		call := aitypes.ToolCall{
			Type:      aitypes.ContentTypeToolCall,
			Id:        stringValue(proxyEvent.Id),
			Name:      stringValue(proxyEvent.ToolName),
			Arguments: json.RawMessage("{}"),
		}
		s.setContent(index, aitypes.ToolCallBlock(call))
		s.partialJSON[index] = ""
		return aitypes.NewToolCallStartEvent(index, s.clonePartial()), true

	case "toolcall_delta":
		index := contentIndexOf(proxyEvent)
		block := s.contentAt(index)
		if block == nil || block.Type != aitypes.ContentTypeToolCall || block.ToolCall == nil {
			return aitypes.AssistantMessageEvent{}, false
		}
		s.partialJSON[index] += stringValue(proxyEvent.Delta)
		parsed := aiutils.ParseStreamingJSON(s.partialJSON[index])
		if encoded, err := json.Marshal(parsed); err == nil {
			block.ToolCall.Arguments = encoded
		}
		return aitypes.NewToolCallDeltaEvent(index, stringValue(proxyEvent.Delta), s.clonePartial()), true

	case "toolcall_end":
		index := contentIndexOf(proxyEvent)
		block := s.contentAt(index)
		if block == nil || block.Type != aitypes.ContentTypeToolCall || block.ToolCall == nil {
			return aitypes.AssistantMessageEvent{}, false
		}
		if proxyEvent.ToolCall != nil {
			*block.ToolCall = *proxyEvent.ToolCall
			block.ToolCall.Type = aitypes.ContentTypeToolCall
		}
		delete(s.partialJSON, index)
		return aitypes.NewToolCallEndEvent(index, *block.ToolCall, s.clonePartial()), true

	case "done":
		reason := aitypes.StopReasonStop
		if proxyEvent.Reason != nil {
			reason = *proxyEvent.Reason
		}
		s.partial.StopReason = reason
		if proxyEvent.Usage != nil {
			s.partial.Usage = *proxyEvent.Usage
		}
		if proxyEvent.ProviderThinkingLevel != nil {
			s.partial.ProviderThinkingLevel = proxyEvent.ProviderThinkingLevel
		}
		return aitypes.NewDoneEvent(reason, s.clonePartial()), true

	case "error":
		reason := aitypes.StopReasonError
		if proxyEvent.Reason != nil {
			reason = *proxyEvent.Reason
		}
		s.partial.StopReason = reason
		if proxyEvent.ErrorMessage != nil {
			s.partial.ErrorMessage = proxyEvent.ErrorMessage
		}
		if proxyEvent.Usage != nil {
			s.partial.Usage = *proxyEvent.Usage
		}
		if proxyEvent.ProviderThinkingLevel != nil {
			s.partial.ProviderThinkingLevel = proxyEvent.ProviderThinkingLevel
		}
		return aitypes.NewErrorEvent(reason, s.clonePartial()), true

	default:
		return aitypes.AssistantMessageEvent{}, false
	}
}

func (s *proxyStreamState) contentAt(index int) *aitypes.ContentBlock {
	if index < 0 || index >= len(s.partial.Content) {
		return nil
	}
	return &s.partial.Content[index]
}

func (s *proxyStreamState) setContent(index int, block aitypes.ContentBlock) {
	for len(s.partial.Content) <= index {
		s.partial.Content = append(s.partial.Content, aitypes.ContentBlock{})
	}
	s.partial.Content[index] = block
}

func contentIndexOf(event ProxyAssistantMessageEvent) int {
	if event.ContentIndex == nil {
		return 0
	}
	return *event.ContentIndex
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func proxyContext(signal <-chan struct{}) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	if signal == nil {
		return ctx, cancel
	}
	go func() {
		select {
		case <-signal:
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, cancel
}

func proxyPost(ctx context.Context, model *aitypes.Model, context *aitypes.TranscriptContext, options ProxyStreamOptions) (*http.Response, error) {
	payload := map[string]any{
		"model":   model,
		"context": map[string]any{"messages": context.Messages},
		"options": buildProxyRequestOptions(options),
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(options.ProxyUrl, "/")+"/api/stream", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+options.AuthToken)
	request.Header.Set("Content-Type", "application/json")
	return http.DefaultClient.Do(request)
}

func buildProxyRequestOptions(options ProxyStreamOptions) map[string]any {
	out := map[string]any{}
	if options.Temperature != nil {
		out["temperature"] = *options.Temperature
	}
	if options.SamplingParams != nil {
		out["samplingParams"] = options.SamplingParams
	}
	if options.MaxTokens != nil {
		out["maxTokens"] = *options.MaxTokens
	}
	if options.Reasoning != nil {
		out["reasoning"] = *options.Reasoning
	}
	if options.CacheRetention != nil {
		out["cacheRetention"] = *options.CacheRetention
	}
	if options.SessionId != nil {
		out["sessionId"] = *options.SessionId
	}
	if options.Headers != nil {
		out["headers"] = options.Headers
	}
	if options.Metadata != nil {
		out["metadata"] = options.Metadata
	}
	if options.Transport != nil {
		out["transport"] = *options.Transport
	}
	if options.ThinkingBudgets != nil {
		out["thinkingBudgets"] = options.ThinkingBudgets
	}
	if options.MaxRetryDelayMs != nil {
		out["maxRetryDelayMs"] = *options.MaxRetryDelayMs
	}
	return out
}

// cloneAssistantMessage deep-copies a partial message so every emitted event
// carries an immutable snapshot. The proxy producer keeps mutating its own copy
// while consumers read earlier events.
func cloneAssistantMessage(message aitypes.AssistantMessage) aitypes.AssistantMessage {
	cloned := message
	cloned.Content = make([]aitypes.ContentBlock, len(message.Content))
	for i, block := range message.Content {
		cloned.Content[i] = cloneContentBlock(block)
	}
	return cloned
}

func cloneContentBlock(block aitypes.ContentBlock) aitypes.ContentBlock {
	cloned := aitypes.ContentBlock{Type: block.Type}
	if block.Text != nil {
		text := *block.Text
		if block.Text.TextSignature != nil {
			signature := *block.Text.TextSignature
			text.TextSignature = &signature
		}
		cloned.Text = &text
	}
	if block.Thinking != nil {
		thinking := *block.Thinking
		if block.Thinking.ThinkingSignature != nil {
			signature := *block.Thinking.ThinkingSignature
			thinking.ThinkingSignature = &signature
		}
		if block.Thinking.Redacted != nil {
			redacted := *block.Thinking.Redacted
			thinking.Redacted = &redacted
		}
		cloned.Thinking = &thinking
	}
	if block.Image != nil {
		image := *block.Image
		cloned.Image = &image
	}
	if block.ToolCall != nil {
		call := *block.ToolCall
		call.Arguments = append(json.RawMessage(nil), block.ToolCall.Arguments...)
		if block.ToolCall.ThoughtSignature != nil {
			signature := *block.ToolCall.ThoughtSignature
			call.ThoughtSignature = &signature
		}
		if block.ToolCall.Namespace != nil {
			namespace := *block.ToolCall.Namespace
			call.Namespace = &namespace
		}
		cloned.ToolCall = &call
	}
	return cloned
}
