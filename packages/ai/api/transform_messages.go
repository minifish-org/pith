// This file is a Go port of packages/ai/src/api/transform-messages.ts from Pi at
// revision f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// TransformMessages normalizes a conversation before it reaches a provider:
// unsupported images are downgraded to placeholders, cross-model thinking and
// tool-call signatures are dropped or converted, tool-call ids can be rewritten
// for cross-provider compatibility, and orphaned tool calls receive synthetic
// empty results. System messages that land between a tool call and its results
// are held back until the results (synthetic ones included) have been emitted.
package api

import (
	"encoding/json"
	"time"

	"github.com/minifish-org/pith/packages/ai/types"
)

// Non-vision placeholders used when a model does not accept image input.
const (
	NonVisionUserImagePlaceholder = "(image omitted: model does not support images)"
	NonVisionToolImagePlaceholder = "(tool image omitted: model does not support images)"
)

// nowMillis returns the current wall-clock time in Unix milliseconds. The SDK
// timestamps use millisecond floats, matching the upstream `Date.now()`.
func nowMillis() float64 {
	return float64(time.Now().UnixMilli())
}

// replaceImagesWithPlaceholder replaces image blocks with a single text
// placeholder per run of images, collapsing adjacent images so a model that
// cannot see images does not receive the placeholder several times in a row.
func replaceImagesWithPlaceholder(content []types.ContentBlock, placeholder string) []types.ContentBlock {
	result := make([]types.ContentBlock, 0, len(content))
	previousWasPlaceholder := false

	for _, block := range content {
		if block.Type == types.ContentTypeImage {
			if !previousWasPlaceholder {
				result = append(result, types.TextBlock(placeholder))
			}
			previousWasPlaceholder = true
			continue
		}

		result = append(result, block)
		previousWasPlaceholder = block.Type == types.ContentTypeText && block.Text != nil && block.Text.Text == placeholder
	}

	return result
}

func modelSupportsImages(model *types.Model) bool {
	if model == nil {
		return false
	}
	for _, modality := range model.Input {
		if modality == types.ModelInputImage {
			return true
		}
	}
	return false
}

// downgradeUnsupportedImages replaces images with text placeholders for models
// that do not accept image input.
func downgradeUnsupportedImages(messages []types.Message, model *types.Model) []types.Message {
	if modelSupportsImages(model) {
		return messages
	}

	out := make([]types.Message, 0, len(messages))
	for _, message := range messages {
		switch message.Role {
		case types.UserMessageRole:
			if message.User != nil && message.User.Content.Structured {
				user := *message.User
				user.Content = types.UserContentBlocks(replaceImagesWithPlaceholder(user.Content.Blocks, NonVisionUserImagePlaceholder))
				out = append(out, types.NewUserMessageVariant(user))
				continue
			}
		case types.ToolResultMessageRole:
			if message.ToolResult != nil {
				toolResult := *message.ToolResult
				toolResult.Content = replaceImagesWithPlaceholder(toolResult.Content, NonVisionToolImagePlaceholder)
				out = append(out, types.NewToolResultMessageVariant(toolResult))
				continue
			}
		}
		out = append(out, message)
	}
	return out
}

// NormalizeToolCallId rewrites a tool-call id for cross-provider compatibility.
// It returns the id to use for the call.
type NormalizeToolCallId func(id string, model *types.Model, source types.AssistantMessage) string

// TransformMessages normalizes a conversation for a target model.
//
// The optional normalizeToolCallId is applied to tool calls of assistant
// messages that were produced by a different model; the rewritten id is also
// applied to matching tool results.
func TransformMessages(messages []types.Message, model *types.Model, normalizeToolCallId NormalizeToolCallId) []types.Message {
	// Build a map of original tool call IDs to normalized IDs.
	toolCallIdMap := map[string]string{}

	// Normalize missing content from untyped callers so downstream code can rely
	// on the type contract. In the Go model a plain-string user message is
	// represented with a nil Blocks slice and Structured=false, exactly like an
	// absent content array would be, so user content must not be rewritten here:
	// doing so would erase every string-form user turn. Assistant and tool-result
	// messages carry typed slices and remain distinguishable.
	normalizedMessages := make([]types.Message, 0, len(messages))
	for _, message := range messages {
		switch message.Role {
		case types.AssistantMessageRole:
			if message.Assistant != nil && message.Assistant.Content == nil {
				assistant := *message.Assistant
				assistant.Content = []types.ContentBlock{}
				normalizedMessages = append(normalizedMessages, types.NewAssistantMessageVariant(assistant))
				continue
			}
		case types.ToolResultMessageRole:
			if message.ToolResult != nil && message.ToolResult.Content == nil {
				toolResult := *message.ToolResult
				toolResult.Content = []types.ContentBlock{}
				normalizedMessages = append(normalizedMessages, types.NewToolResultMessageVariant(toolResult))
				continue
			}
		}
		normalizedMessages = append(normalizedMessages, message)
	}
	imageAwareMessages := downgradeUnsupportedImages(normalizedMessages, model)

	// First pass: transform messages (thinking blocks, tool call ID
	// normalization).
	transformed := make([]types.Message, 0, len(imageAwareMessages))
	for _, message := range imageAwareMessages {
		if message.Role == types.SystemMessageRole || message.Role == types.UserMessageRole {
			transformed = append(transformed, message)
			continue
		}

		if message.Role == types.ToolResultMessageRole {
			if message.ToolResult != nil {
				if normalizedId, ok := toolCallIdMap[message.ToolResult.ToolCallId]; ok && normalizedId != message.ToolResult.ToolCallId {
					toolResult := *message.ToolResult
					toolResult.ToolCallId = normalizedId
					transformed = append(transformed, types.NewToolResultMessageVariant(toolResult))
					continue
				}
			}
			transformed = append(transformed, message)
			continue
		}

		if message.Role == types.AssistantMessageRole && message.Assistant != nil {
			assistantMsg := *message.Assistant
			isSameModel := assistantMsg.Provider == model.Provider &&
				assistantMsg.Api == model.Api &&
				assistantMsg.Model == model.Id

			transformedContent := make([]types.ContentBlock, 0, len(assistantMsg.Content))
			for _, block := range assistantMsg.Content {
				switch block.Type {
				case types.ContentTypeThinking:
					if block.Thinking == nil {
						continue
					}
					thinking := block.Thinking
					// Redacted thinking is opaque encrypted content, only valid
					// for the same model. Drop it for cross-model to avoid API
					// errors.
					if thinking.Redacted != nil && *thinking.Redacted {
						if isSameModel {
							transformedContent = append(transformedContent, block)
						}
						continue
					}
					// For the same model keep thinking blocks with signatures
					// (needed for replay) even if the thinking text is empty.
					if isSameModel && thinking.ThinkingSignature != nil {
						transformedContent = append(transformedContent, block)
						continue
					}
					// Skip empty thinking blocks, convert others to plain text.
					if len(thinking.Thinking) == 0 || isBlank(thinking.Thinking) {
						continue
					}
					if isSameModel {
						transformedContent = append(transformedContent, block)
						continue
					}
					transformedContent = append(transformedContent, types.TextBlock(thinking.Thinking))

				case types.ContentTypeText:
					transformedContent = append(transformedContent, block)

				case types.ContentTypeToolCall:
					if block.ToolCall == nil {
						continue
					}
					toolCall := *block.ToolCall
					if !isSameModel && toolCall.ThoughtSignature != nil {
						toolCall.ThoughtSignature = nil
					}
					if !isSameModel && normalizeToolCallId != nil {
						normalizedId := normalizeToolCallId(toolCall.Id, model, assistantMsg)
						if normalizedId != toolCall.Id {
							toolCallIdMap[toolCall.Id] = normalizedId
							toolCall.Id = normalizedId
						}
					}
					transformedContent = append(transformedContent, types.ToolCallBlock(toolCall))

				default:
					transformedContent = append(transformedContent, block)
				}
			}

			assistantMsg.Content = transformedContent
			transformed = append(transformed, types.NewAssistantMessageVariant(assistantMsg))
			continue
		}

		transformed = append(transformed, message)
	}

	// Second pass: insert synthetic empty tool results for orphaned tool calls.
	result := []types.Message{}
	var pendingToolCalls []types.ToolCall
	existingToolResultIds := map[string]bool{}
	// System messages are transparent to tool-call accounting: one that lands
	// between a tool call and its results is held back and emitted after the
	// results (synthetic ones included), so it never causes a duplicate result
	// for a call that is answered later.
	var heldSystemMessages []types.Message

	closePendingToolCalls := func() {
		if len(pendingToolCalls) > 0 {
			for _, toolCall := range pendingToolCalls {
				if !existingToolResultIds[toolCall.Id] {
					result = append(result, types.NewToolResultMessageVariant(types.ToolResultMessage{
						Role:       types.ToolResultMessageRole,
						ToolCallId: toolCall.Id,
						ToolName:   toolCall.Name,
						Content:    []types.ContentBlock{types.TextBlock("No result provided")},
						IsError:    true,
						Timestamp:  nowMillis(),
					}))
				}
			}
			pendingToolCalls = nil
			existingToolResultIds = map[string]bool{}
		}
		result = append(result, heldSystemMessages...)
		heldSystemMessages = nil
	}

	for _, message := range transformed {
		switch message.Role {
		case types.AssistantMessageRole:
			// If there are pending orphaned tool calls from a previous
			// assistant, insert synthetic results now.
			closePendingToolCalls()

			if message.Assistant == nil {
				result = append(result, message)
				continue
			}
			assistantMsg := *message.Assistant
			// Skip errored/aborted assistant messages entirely: incomplete turns
			// must not be replayed.
			if assistantMsg.StopReason == types.StopReasonError || assistantMsg.StopReason == types.StopReasonAborted {
				continue
			}

			toolCalls := make([]types.ToolCall, 0)
			for _, block := range assistantMsg.Content {
				if block.Type == types.ContentTypeToolCall && block.ToolCall != nil {
					toolCalls = append(toolCalls, *block.ToolCall)
				}
			}
			if len(toolCalls) > 0 {
				pendingToolCalls = toolCalls
				existingToolResultIds = map[string]bool{}
			}

			result = append(result, message)

		case types.ToolResultMessageRole:
			if message.ToolResult != nil {
				existingToolResultIds[message.ToolResult.ToolCallId] = true
			}
			result = append(result, message)

		case types.SystemMessageRole:
			if len(pendingToolCalls) > 0 {
				heldSystemMessages = append(heldSystemMessages, message)
			} else {
				result = append(result, message)
			}

		case types.UserMessageRole:
			// A new user turn interrupts tool flow; insert synthetic results for
			// orphaned calls.
			closePendingToolCalls()
			result = append(result, message)

		default:
			result = append(result, message)
		}
	}

	// If the conversation ends with unresolved tool calls, synthesize results
	// now.
	closePendingToolCalls()

	return result
}

// isBlank reports whether a string is empty or contains only whitespace, the Go
// equivalent of the upstream `text.trim() === ""` check.
func isBlank(text string) bool {
	for _, r := range text {
		switch r {
		case ' ', '\t', '\n', '\v', '\f', '\r', 0x85, 0xa0:
			continue
		default:
			return false
		}
	}
	return true
}

// marshalArguments is a small helper keeping tool-call arguments in their raw
// JSON form, so number and signature semantics survive unchanged.
func marshalArguments(arguments map[string]any) json.RawMessage {
	encoded, err := json.Marshal(arguments)
	if err != nil {
		return json.RawMessage("{}")
	}
	return encoded
}
