// This file carries the assistant execution primitives of
// packages/agent/src/harness/execution/assistant.ts.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// The execution package is a leaf: it never imports the shared harness DTO
// package, so the streaming observer and config use local, executable shapes.
package harnessexecution

import (
	"errors"
	"fmt"

	harnesscontext "github.com/minifish-org/pith/packages/agent/harness/context"
	agenttypes "github.com/minifish-org/pith/packages/agent/types"
	aitypes "github.com/minifish-org/pith/packages/ai/types"
)

// AssistantResponseMetadata is HTTP response metadata captured before the
// provider response body is consumed.
type AssistantResponseMetadata struct {
	Status  *int              `json:"status,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

// AssistantStreamObserver is the process-local lifecycle observer for one
// assistant stream.
type AssistantStreamObserver interface {
	Start(message aitypes.AssistantMessage, event aitypes.AssistantMessageEvent, ctx harnesscontext.Context) error
	Update(message aitypes.AssistantMessage, event aitypes.AssistantMessageEvent, ctx harnesscontext.Context) error
	End(message aitypes.AssistantMessage, ctx harnesscontext.Context) error
}

// HarnessAssistantRequestContext is the provider-facing request context.
type HarnessAssistantRequestContext struct {
	SystemPrompt string
	Messages     []aitypes.Message
	Tools        []aitypes.Tool
}

// HarnessAssistantStreamConfig are the executable inputs for one already
// approved assistant provider request.
type HarnessAssistantStreamConfig struct {
	Model              *aitypes.Model
	SystemPrompt       string
	Tools              []aitypes.Tool
	ThinkingLevel      agenttypes.ThinkingLevel
	StreamOptions      aitypes.SimpleStreamOptions
	TransformContext   func(messages []agenttypes.AgentMessage, systemPrompt string, ctx harnesscontext.Context) ([]agenttypes.AgentMessage, string, error)
	ToProviderMessages func(messages []agenttypes.AgentMessage, ctx harnesscontext.Context) ([]aitypes.Message, error)
	BeforePayload      func(payload any, model *aitypes.Model, ctx harnesscontext.Context) (any, error)
	AfterResponse      func(message aitypes.AssistantMessage, metadata AssistantResponseMetadata, ctx harnesscontext.Context) (aitypes.AssistantMessage, error)
	Request            func(requestContext HarnessAssistantRequestContext, options *aitypes.SimpleStreamOptions, ctx harnesscontext.Context) (*aitypes.AssistantMessageEventStream, error)
	Observer           AssistantStreamObserver
}

// isUpdateEvent reports whether the event advances a live partial message.
func isUpdateEvent(event aitypes.AssistantMessageEvent) bool {
	switch event.Type {
	case aitypes.AssistantEventStart, aitypes.AssistantEventDone, aitypes.AssistantEventError:
		return false
	default:
		return true
	}
}

// ConsumeAssistantStream observes one provider stream and returns its settled
// message. A stream must emit exactly one start before any update event, and
// done/error only after start.
func ConsumeAssistantStream(
	stream *aitypes.AssistantMessageEventStream,
	observer AssistantStreamObserver,
	afterResponse func(message aitypes.AssistantMessage, ctx harnesscontext.Context) (aitypes.AssistantMessage, error),
	ctx harnesscontext.Context,
) (aitypes.AssistantMessage, error) {
	if stream == nil {
		return aitypes.AssistantMessage{}, errors.New("assistant stream is nil")
	}
	started := false
	for {
		item := <-stream.Next()
		if item.Done {
			break
		}
		event := item.Value
		switch {
		case event.Type == aitypes.AssistantEventStart:
			if started {
				return aitypes.AssistantMessage{}, errors.New("assistant message stream emitted more than one start event")
			}
			started = true
			partial := aitypes.AssistantMessage{}
			if event.Partial != nil {
				partial = *event.Partial
			}
			if observer != nil {
				if err := observer.Start(partial, event, ctx); err != nil {
					return aitypes.AssistantMessage{}, err
				}
			}
		case isUpdateEvent(event):
			if !started {
				return aitypes.AssistantMessage{}, fmt.Errorf("assistant message stream emitted %s before start", event.Type)
			}
			partial := aitypes.AssistantMessage{}
			if event.Partial != nil {
				partial = *event.Partial
			}
			if observer != nil {
				if err := observer.Update(partial, event, ctx); err != nil {
					return aitypes.AssistantMessage{}, err
				}
			}
		case event.Type == aitypes.AssistantEventDone && !started:
			return aitypes.AssistantMessage{}, errors.New("assistant message stream emitted done before start")
		}
	}

	settled, err := stream.Result(ctx)
	if err != nil {
		return aitypes.AssistantMessage{}, err
	}
	finalMessage := settled
	if afterResponse != nil {
		finalMessage, err = afterResponse(settled, ctx)
		if err != nil {
			return aitypes.AssistantMessage{}, err
		}
	}
	if observer != nil {
		if err := observer.End(finalMessage, ctx); err != nil {
			return aitypes.AssistantMessage{}, err
		}
	}
	return finalMessage, nil
}

// StreamHarnessAssistant streams one assistant response without mutating the
// caller's message list.
func StreamHarnessAssistant(
	messages []agenttypes.AgentMessage,
	config HarnessAssistantStreamConfig,
	ctx harnesscontext.Context,
) (aitypes.AssistantMessage, error) {
	requestMessages := append([]agenttypes.AgentMessage(nil), messages...)
	systemPrompt := config.SystemPrompt
	if config.TransformContext != nil {
		transformed, transformedPrompt, err := config.TransformContext(requestMessages, systemPrompt, ctx)
		if err != nil {
			return aitypes.AssistantMessage{}, err
		}
		requestMessages = transformed
		systemPrompt = transformedPrompt
	}
	providerMessages, err := config.ToProviderMessages(requestMessages, ctx)
	if err != nil {
		return aitypes.AssistantMessage{}, err
	}
	requestContext := HarnessAssistantRequestContext{
		SystemPrompt: systemPrompt,
		Messages:     providerMessages,
		Tools:        config.Tools,
	}
	options := config.StreamOptions
	if config.ThinkingLevel != "" && config.ThinkingLevel != "off" {
		level := aitypes.ThinkingLevel(config.ThinkingLevel)
		options.Reasoning = &level
	}
	stream, err := config.Request(requestContext, &options, ctx)
	if err != nil {
		return aitypes.AssistantMessage{}, err
	}
	metadata := AssistantResponseMetadata{}
	_ = metadata
	afterResponse := config.AfterResponse
	var wrapped func(aitypes.AssistantMessage, harnesscontext.Context) (aitypes.AssistantMessage, error)
	if afterResponse != nil {
		meta := metadata
		wrapped = func(message aitypes.AssistantMessage, afterCtx harnesscontext.Context) (aitypes.AssistantMessage, error) {
			return afterResponse(message, meta, afterCtx)
		}
	}
	return ConsumeAssistantStream(stream, config.Observer, wrapped, ctx)
}
