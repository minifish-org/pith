// This file is a Go port of packages/ai/src/utils/estimate.ts from Pi at
// revision f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
package utils

import (
	"encoding/json"
	"math"

	"github.com/minifish-org/pith/packages/ai/types"
)

// ContextUsageEstimate is the estimated context-window usage of a transcript.
type ContextUsageEstimate struct {
	// Tokens is the estimated total context tokens.
	Tokens float64
	// UsageTokens is the tokens reported by the most recent applicable
	// assistant usage block.
	UsageTokens float64
	// TrailingTokens is the estimated tokens after the most recent applicable
	// assistant usage block.
	TrailingTokens float64
	// LastUsageIndex is the index of the applicable message that provided
	// usage, or nil when none exists.
	LastUsageIndex *int
}

const (
	charsPerToken       = 4
	estimatedImageChars = 4800
)

// CalculateContextTokens returns the total context tokens recorded by a usage
// block, falling back to the sum of the fields when totalTokens is zero.
func CalculateContextTokens(usage types.Usage) float64 {
	if usage.TotalTokens != 0 {
		return usage.TotalTokens
	}
	return usage.Input + usage.Output + usage.CacheRead + usage.CacheWrite
}

func safeJSONStringify(value any) string {
	if value == nil {
		return "null"
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "[unserializable]"
	}
	return string(encoded)
}

// EstimateTextTokens estimates the tokens of a plain string. Length is measured
// in UTF-16 code units, matching the upstream JavaScript `text.length`, so an
// astral character counts as two.
func EstimateTextTokens(text string) float64 {
	return math.Ceil(float64(utf16Length(text)) / charsPerToken)
}

// utf16Length returns the number of UTF-16 code units of a string, the Go
// equivalent of the JavaScript string `.length`.
func utf16Length(text string) int {
	units := 0
	for _, r := range text {
		if r > 0xffff {
			units += 2
		} else {
			units++
		}
	}
	return units
}

// EstimateTextAndImageContentTokens estimates the tokens of user or tool-result
// content, charging each image the fixed estimated character budget.
func EstimateTextAndImageContentTokens(content any) float64 {
	return math.Ceil(float64(estimateTextAndImageContentChars(content)) / charsPerToken)
}

func estimateTextAndImageContentChars(content any) int {
	switch value := content.(type) {
	case string:
		return utf16Length(value)
	case types.UserContent:
		if value.Structured {
			return estimateBlockChars(value.Blocks)
		}
		return utf16Length(value.Text)
	case types.SystemContent:
		if value.Structured {
			chars := 0
			for _, block := range value.Blocks {
				chars += utf16Length(block.Text)
			}
			return chars
		}
		return utf16Length(value.Text)
	case []types.ContentBlock:
		return estimateBlockChars(value)
	case []types.TextContent:
		chars := 0
		for _, block := range value {
			chars += utf16Length(block.Text)
		}
		return chars
	default:
		return 0
	}
}

func estimateBlockChars(blocks []types.ContentBlock) int {
	chars := 0
	for _, block := range blocks {
		if block.Type == types.ContentTypeText && block.Text != nil {
			chars += utf16Length(block.Text.Text)
		} else {
			chars += estimatedImageChars
		}
	}
	return chars
}

// EstimateMessageTokens estimates the tokens a single message contributes to the
// context window.
func EstimateMessageTokens(message types.Message) float64 {
	switch message.Role {
	case types.SystemMessageRole:
		if message.System == nil {
			return 0
		}
		return EstimateTextTokens(GetSystemMessageText(*message.System)) +
			estimateToolsTokens(message.System.ToolsAdded) +
			estimateToolsTokens(message.System.ToolsRemoved)
	case types.UserMessageRole:
		if message.User == nil {
			return 0
		}
		return EstimateTextAndImageContentTokens(message.User.Content)
	case types.ToolResultMessageRole:
		if message.ToolResult == nil {
			return 0
		}
		return EstimateTextAndImageContentTokens(types.UserContent{Blocks: message.ToolResult.Content, Structured: true})
	case types.AssistantMessageRole:
		if message.Assistant == nil {
			return 0
		}
		chars := 0
		for _, block := range message.Assistant.Content {
			switch block.Type {
			case types.ContentTypeText:
				if block.Text != nil {
					chars += utf16Length(block.Text.Text)
				}
			case types.ContentTypeThinking:
				if block.Thinking != nil {
					chars += utf16Length(block.Thinking.Thinking)
				}
			case types.ContentTypeToolCall:
				if block.ToolCall != nil {
					chars += utf16Length(block.ToolCall.Name) + utf16Length(safeJSONStringify(json.RawMessage(block.ToolCall.Arguments)))
				}
			}
		}
		return math.Ceil(float64(chars) / charsPerToken)
	default:
		return 0
	}
}

func estimateToolsTokens(tools any) float64 {
	switch value := tools.(type) {
	case []types.Tool:
		if len(value) == 0 {
			return 0
		}
		return EstimateTextTokens(safeJSONStringify(value))
	case []types.ToolReference:
		if len(value) == 0 {
			return 0
		}
		return EstimateTextTokens(safeJSONStringify(value))
	default:
		return 0
	}
}

// messageTimestamp returns the message timestamp through a role-agnostic view.
func messageTimestamp(message types.Message) float64 {
	switch message.Role {
	case types.SystemMessageRole:
		if message.System != nil {
			return message.System.Timestamp
		}
	case types.UserMessageRole:
		if message.User != nil {
			return message.User.Timestamp
		}
	case types.AssistantMessageRole:
		if message.Assistant != nil {
			return message.Assistant.Timestamp
		}
	case types.ToolResultMessageRole:
		if message.ToolResult != nil {
			return message.ToolResult.Timestamp
		}
	}
	return 0
}

// getLastAssistantUsageInfo returns the most recent applicable assistant usage
// block and its index.
func getLastAssistantUsageInfo(messages []types.Message) (types.Usage, int, bool) {
	latestPrefixTimestamp := math.Inf(-1)
	found := false
	var usage types.Usage
	index := 0

	for i, message := range messages {
		if message.Role == types.AssistantMessageRole && message.Assistant != nil {
			assistant := message.Assistant
			// A newer prefix message was inserted after this response (for
			// example, a compaction summary), so its usage cannot describe the
			// current prefix.
			usageAppliesToPrefix := assistant.Timestamp >= latestPrefixTimestamp
			if usageAppliesToPrefix &&
				assistant.StopReason != types.StopReasonAborted &&
				assistant.StopReason != types.StopReasonError &&
				CalculateContextTokens(assistant.Usage) > 0 {
				usage = assistant.Usage
				index = i
				found = true
			}
		}
		if messageTimestamp(message) > latestPrefixTimestamp {
			latestPrefixTimestamp = messageTimestamp(message)
		}
	}

	return usage, index, found
}

// EstimateContextTokens estimates the context-window usage of a transcript. It
// accepts either a normalized transcript context or a raw message list.
func EstimateContextTokens(context any) ContextUsageEstimate {
	messages := contextMessages(context)
	usage, index, found := getLastAssistantUsageInfo(messages)
	if found {
		usageTokens := CalculateContextTokens(usage)
		trailingTokens := 0.0
		for i := index + 1; i < len(messages); i++ {
			trailingTokens += EstimateMessageTokens(messages[i])
		}
		lastIndex := index
		return ContextUsageEstimate{
			Tokens:         usageTokens + trailingTokens,
			UsageTokens:    usageTokens,
			TrailingTokens: trailingTokens,
			LastUsageIndex: &lastIndex,
		}
	}

	tokens := 0.0
	for _, message := range messages {
		tokens += EstimateMessageTokens(message)
	}
	return ContextUsageEstimate{Tokens: tokens, UsageTokens: 0, TrailingTokens: tokens, LastUsageIndex: nil}
}

func contextMessages(context any) []types.Message {
	switch value := context.(type) {
	case *types.TranscriptContext:
		if value == nil {
			return nil
		}
		return value.Messages
	case types.TranscriptContext:
		return value.Messages
	case []types.Message:
		return value
	case types.Context:
		return value.Messages
	default:
		return nil
	}
}
