package conformance

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/minifish-org/pith/packages/ai/api"
	"github.com/minifish-org/pith/packages/ai/types"
	"github.com/minifish-org/pith/packages/ai/utils"
)

// RunCase is the only test bridge for the ai-utils batch. It translates
// operations into calls to the real exported Go SDK. It does not reimplement SDK
// behavior, read fixtures or invoke Node.
func RunCase(ctx context.Context, input json.RawMessage) (json.RawMessage, error) {
	var request struct {
		Op   string            `json:"op"`
		File string            `json:"file"`
		Fn   string            `json:"fn"`
		Args []json.RawMessage `json:"args"`
	}
	if err := json.Unmarshal(input, &request); err != nil {
		return nil, fmt.Errorf("invalid adapter input: %w", err)
	}

	switch request.Op {
	case "call":
		return runCall(request.Fn, request.Args)
	default:
		return nil, fmt.Errorf("unsupported ai-utils operation %q", request.Op)
	}
}

func runCall(fn string, args []json.RawMessage) (json.RawMessage, error) {
	switch fn {
	case "parseStreamingJson":
		text, err := argString(args, 0)
		if err != nil {
			return nil, err
		}
		return json.Marshal(utils.ParseStreamingJSON(text))

	case "parseJsonWithRepair":
		text, err := argString(args, 0)
		if err != nil {
			return nil, err
		}
		value, err := utils.ParseJSONWithRepair(text)
		if err != nil {
			return nil, err
		}
		return json.Marshal(value)

	case "repairJson":
		text, err := argString(args, 0)
		if err != nil {
			return nil, err
		}
		return json.Marshal(utils.RepairJSON(text))

	case "contentText":
		content, err := argContent(args, 0)
		if err != nil {
			return nil, err
		}
		separator := "\n"
		if len(args) > 1 {
			if parsed, ok := decodeString(args[1]); ok {
				separator = parsed
			}
		}
		return json.Marshal(utils.ContentText(content, separator))

	case "estimateTextTokens":
		text, err := argString(args, 0)
		if err != nil {
			return nil, err
		}
		return json.Marshal(utils.EstimateTextTokens(text))

	case "estimateTextAndImageContentTokens":
		content, err := argContent(args, 0)
		if err != nil {
			return nil, err
		}
		return json.Marshal(utils.EstimateTextAndImageContentTokens(content))

	case "calculateContextTokens":
		if len(args) < 1 {
			return nil, fmt.Errorf("calculateContextTokens requires a usage argument")
		}
		var usage types.Usage
		if err := json.Unmarshal(args[0], &usage); err != nil {
			return nil, err
		}
		return json.Marshal(utils.CalculateContextTokens(usage))

	case "retryDelayMs":
		if len(args) < 2 {
			return nil, fmt.Errorf("retryDelayMs requires a policy and an attempt")
		}
		var policy struct {
			BaseDelayMs     float64  `json:"baseDelayMs"`
			MaxAgentDelayMs *float64 `json:"maxAgentDelayMs"`
		}
		if err := json.Unmarshal(args[0], &policy); err != nil {
			return nil, err
		}
		attempt, err := argInt(args, 1)
		if err != nil {
			return nil, err
		}
		return json.Marshal(utils.RetryDelayMs(utils.RetryPolicy{
			Enabled:         true,
			BaseDelayMs:     policy.BaseDelayMs,
			MaxAgentDelayMs: policy.MaxAgentDelayMs,
		}, attempt))

	case "shortHash":
		text, err := argString(args, 0)
		if err != nil {
			return nil, err
		}
		return json.Marshal(utils.ShortHash(text))

	case "sanitizeSurrogates":
		text, err := argString(args, 0)
		if err != nil {
			return nil, err
		}
		return json.Marshal(utils.SanitizeSurrogates(text))

	case "getCurrentSystemPrompt":
		messages, err := argMessages(args, 0)
		if err != nil {
			return nil, err
		}
		return json.Marshal(utils.GetCurrentSystemPrompt(messages))

	case "getCurrentTools":
		messages, err := argMessages(args, 0)
		if err != nil {
			return nil, err
		}
		return json.Marshal(utils.GetCurrentTools(messages))

	case "getCurrentSystemMessage":
		messages, err := argMessages(args, 0)
		if err != nil {
			return nil, err
		}
		message := utils.GetCurrentSystemMessage(messages)
		if message == nil {
			return json.Marshal(nil)
		}
		return json.Marshal(message)

	case "hasToolRedefinitions":
		messages, err := argMessages(args, 0)
		if err != nil {
			return nil, err
		}
		return json.Marshal(utils.HasToolRedefinitions(messages))

	case "hasNonAdditiveToolChanges":
		messages, err := argMessages(args, 0)
		if err != nil {
			return nil, err
		}
		return json.Marshal(utils.HasNonAdditiveToolChanges(messages))

	case "validateToolArguments":
		if len(args) < 2 {
			return nil, fmt.Errorf("validateToolArguments requires a tool and a tool call")
		}
		var toolCall types.ToolCall
		if err := json.Unmarshal(args[1], &toolCall); err != nil {
			return nil, err
		}
		tool, err := decodeTool(args[0])
		if err != nil {
			return nil, err
		}
		value, err := utils.ValidateToolArguments(tool, toolCall)
		if err != nil {
			return nil, err
		}
		return json.Marshal(value)

	case "validateToolCall":
		if len(args) < 2 {
			return nil, fmt.Errorf("validateToolCall requires tools and a tool call")
		}
		var tools []types.Tool
		if err := json.Unmarshal(args[0], &tools); err != nil {
			return nil, err
		}
		var toolCall types.ToolCall
		if err := json.Unmarshal(args[1], &toolCall); err != nil {
			return nil, err
		}
		value, err := utils.ValidateToolCall(tools, toolCall)
		if err != nil {
			return nil, err
		}
		return json.Marshal(value)

	case "isContextOverflow":
		if len(args) < 1 {
			return nil, fmt.Errorf("isContextOverflow requires a message")
		}
		var message types.AssistantMessage
		if err := json.Unmarshal(args[0], &message); err != nil {
			return nil, err
		}
		var contextWindow *float64
		if len(args) > 1 && string(args[1]) != "null" {
			var value float64
			if err := json.Unmarshal(args[1], &value); err != nil {
				return nil, err
			}
			contextWindow = &value
		}
		return json.Marshal(utils.IsContextOverflow(message, contextWindow))

	case "isRetryableAssistantError":
		if len(args) < 1 {
			return nil, fmt.Errorf("isRetryableAssistantError requires a message")
		}
		var message types.AssistantMessage
		if err := json.Unmarshal(args[0], &message); err != nil {
			return nil, err
		}
		return json.Marshal(utils.IsRetryableAssistantError(message))

	case "getOverflowPatterns":
		patterns := utils.GetOverflowPatterns()
		out := make([]string, len(patterns))
		for i, pattern := range patterns {
			out[i] = pattern.String()
		}
		return json.Marshal(out)

	case "stringEnum":
		if len(args) < 1 {
			return nil, fmt.Errorf("stringEnum requires values")
		}
		var values []string
		if err := json.Unmarshal(args[0], &values); err != nil {
			return nil, err
		}
		var options *utils.StringEnumOptions
		if len(args) > 1 && string(args[1]) != "null" {
			var decoded struct {
				Description *string `json:"description"`
				Default     *string `json:"default"`
			}
			if err := json.Unmarshal(args[1], &decoded); err != nil {
				return nil, err
			}
			options = &utils.StringEnumOptions{Description: decoded.Description, Default: decoded.Default}
		}
		return utils.StringEnum(values, options), nil

	case "normalizeProviderError":
		if len(args) < 1 {
			return nil, fmt.Errorf("normalizeProviderError requires a value")
		}
		var value map[string]any
		if err := json.Unmarshal(args[0], &value); err != nil {
			return nil, err
		}
		normalized := utils.NormalizeProviderErrorValue(value)
		return json.Marshal(normalized)

	case "formatProviderError":
		if len(args) < 1 {
			return nil, fmt.Errorf("formatProviderError requires a normalized error")
		}
		var normalized utils.NormalizedProviderError
		if err := json.Unmarshal(args[0], &normalized); err != nil {
			return nil, err
		}
		var prefix *string
		if len(args) > 1 && string(args[1]) != "null" {
			var value string
			if err := json.Unmarshal(args[1], &value); err != nil {
				return nil, err
			}
			prefix = &value
		}
		return json.Marshal(utils.FormatProviderError(normalized, prefix))

	case "makeStrictJsonSchema":
		if len(args) < 1 {
			return nil, fmt.Errorf("makeStrictJsonSchema requires a schema")
		}
		schema, err := api.MakeStrictJSONSchema(args[0])
		if err != nil {
			return nil, err
		}
		return json.Marshal(schema)

	case "clampMaxTokensToContext":
		if len(args) < 3 {
			return nil, fmt.Errorf("clampMaxTokensToContext requires a model, a context and maxTokens")
		}
		model, err := decodeModel(args[0])
		if err != nil {
			return nil, err
		}
		maxTokens, err := argFloat(args, 2)
		if err != nil {
			return nil, err
		}
		return json.Marshal(api.ClampMaxTokensToContext(model, decodeContext(args[1]), maxTokens))

	case "transformMessages":
		if len(args) < 2 {
			return nil, fmt.Errorf("transformMessages requires messages and a model")
		}
		messages, err := argMessages(args, 0)
		if err != nil {
			return nil, err
		}
		model, err := decodeModel(args[1])
		if err != nil {
			return nil, err
		}
		return json.Marshal(api.TransformMessages(messages, model, nil))

	case "uuidv7":
		var timestamp *float64
		if len(args) > 0 && string(args[0]) != "null" {
			value, err := argFloat(args, 0)
			if err != nil {
				return nil, err
			}
			timestamp = &value
		}
		value, err := utils.UUIDv7(timestamp)
		if err != nil {
			return nil, err
		}
		return json.Marshal(value)

	default:
		return nil, fmt.Errorf("unsupported ai-utils function %q", fn)
	}
}

func argString(args []json.RawMessage, index int) (string, error) {
	if index >= len(args) {
		return "", fmt.Errorf("missing string argument %d", index)
	}
	var value string
	if err := json.Unmarshal(args[index], &value); err != nil {
		return "", err
	}
	return value, nil
}

func argInt(args []json.RawMessage, index int) (int, error) {
	value, err := argFloat(args, index)
	if err != nil {
		return 0, err
	}
	return int(value), nil
}

func argFloat(args []json.RawMessage, index int) (float64, error) {
	if index >= len(args) {
		return 0, fmt.Errorf("missing numeric argument %d", index)
	}
	var value float64
	if err := json.Unmarshal(args[index], &value); err != nil {
		return 0, err
	}
	return value, nil
}

func decodeString(raw json.RawMessage) (string, bool) {
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", false
	}
	return value, true
}

// argContent decodes the `string | blocks[]` content union into the value shape
// the utils package understands, preserving which form was supplied.
func argContent(args []json.RawMessage, index int) (any, error) {
	if index >= len(args) {
		return nil, fmt.Errorf("missing content argument %d", index)
	}
	raw := args[index]
	if text, ok := decodeString(raw); ok {
		return text, nil
	}
	var blocks []types.ContentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil, err
	}
	return blocks, nil
}

func argMessages(args []json.RawMessage, index int) ([]types.Message, error) {
	if index >= len(args) {
		return nil, fmt.Errorf("missing messages argument %d", index)
	}
	var messages []types.Message
	if err := json.Unmarshal(args[index], &messages); err != nil {
		return nil, err
	}
	return messages, nil
}

func decodeModel(raw json.RawMessage) (*types.Model, error) {
	var model types.Model
	if err := json.Unmarshal(raw, &model); err != nil {
		return nil, err
	}
	return &model, nil
}

// decodeContext accepts either a raw Context or a normalized transcript context.
func decodeContext(raw json.RawMessage) any {
	var withMessages struct {
		Messages []types.Message `json:"messages"`
	}
	if err := json.Unmarshal(raw, &withMessages); err != nil {
		return nil
	}
	return withMessages.Messages
}

// decodeTool decodes a tool whose parameters are given as a raw JSON schema.
func decodeTool(raw json.RawMessage) (types.Tool, error) {
	var wire struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
		Input       json.RawMessage `json:"input"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return types.Tool{}, err
	}
	var tool types.Tool
	if len(wire.Input) > 0 && strings.Contains(string(wire.Input), "type") {
		if err := json.Unmarshal(wire.Input, &tool.Input); err != nil {
			return types.Tool{}, err
		}
	} else {
		schema := wire.Parameters
		if len(schema) == 0 {
			schema = json.RawMessage("{}")
		}
		tool.Input = types.JSONSchemaToolInput(schema)
	}
	if err := json.Unmarshal(raw, &tool); err != nil {
		return types.Tool{}, err
	}
	tool.Name = wire.Name
	return tool, nil
}
