// This file is a Go port of packages/ai/src/api/simple-options.ts from Pi at
// revision f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
package api

import (
	"math"

	"github.com/minifish-org/pith/packages/ai/types"
	"github.com/minifish-org/pith/packages/ai/utils"
)

const contextSafetyTokens = 4096
const minMaxTokens = 1

// ClampMaxTokensToContext clamps maxTokens so the request plus a safety margin
// fits inside the model context window.
func ClampMaxTokensToContext(model *types.Model, context any, maxTokens float64) float64 {
	if model == nil || model.ContextWindow <= 0 {
		return math.Max(minMaxTokens, maxTokens)
	}
	available := model.ContextWindow - utils.EstimateContextTokens(context).Tokens - contextSafetyTokens
	return math.Min(maxTokens, math.Max(minMaxTokens, available))
}

// BuildBaseOptions builds the shared StreamOptions for a simple request,
// clamping the output budget to the remaining context.
func BuildBaseOptions(model *types.Model, context any, options *types.SimpleStreamOptions, apiKey *string) types.StreamOptions {
	base := types.StreamOptions{}
	if options != nil {
		base = options.StreamOptions
	}

	var samplingParams map[string]any
	if model != nil && model.SamplingParams != nil {
		samplingParams = map[string]any{}
		for key, value := range model.SamplingParams {
			samplingParams[key] = value
		}
	}
	if options != nil && options.SamplingParams != nil {
		if samplingParams == nil {
			samplingParams = map[string]any{}
		}
		for key, value := range options.SamplingParams {
			samplingParams[key] = value
		}
	}

	maxTokens := float64(0)
	if options != nil && options.MaxTokens != nil {
		maxTokens = float64(*options.MaxTokens)
	} else if model != nil {
		maxTokens = model.MaxTokens
	}
	clamped := int(ClampMaxTokensToContext(model, context, maxTokens))

	resolvedKey := apiKey
	if resolvedKey == nil && options != nil {
		resolvedKey = options.APIKey
	}

	result := base
	result.SamplingParams = samplingParams
	result.MaxTokens = &clamped
	result.APIKey = resolvedKey
	return result
}

// MinAnswerTokens are the tokens always left for the answer when a thinking
// budget shares the response ceiling.
const MinAnswerTokens = 1024

// DefaultThinkingBudgets are the default per-level thinking token budgets.
var DefaultThinkingBudgets = types.ThinkingBudgets{
	Minimal: intPtr(1024),
	Low:     intPtr(2048),
	Medium:  intPtr(8192),
	High:    intPtr(16384),
}

// ClampReasoning maps the extended thinking levels back to their clamped base
// level. Unknown levels pass through unchanged.
func ClampReasoning(effort *types.ThinkingLevel) *types.ThinkingLevel {
	if effort == nil {
		return nil
	}
	value := *effort
	if value == types.ThinkingXHigh || value == types.ThinkingMax {
		clamped := types.ThinkingHigh
		return &clamped
	}
	return effort
}

// ThinkingBudgetForLevel returns the thinking budget for a level, applying any
// custom overrides over the defaults.
func ThinkingBudgetForLevel(reasoningLevel types.ThinkingLevel, customBudgets *types.ThinkingBudgets) int {
	budgets := map[types.ThinkingLevel]int{
		types.ThinkingMinimal: valueOr(DefaultThinkingBudgets.Minimal, 1024),
		types.ThinkingLow:     valueOr(DefaultThinkingBudgets.Low, 2048),
		types.ThinkingMedium:  valueOr(DefaultThinkingBudgets.Medium, 8192),
		types.ThinkingHigh:    valueOr(DefaultThinkingBudgets.High, 16384),
	}
	if customBudgets != nil {
		if customBudgets.Minimal != nil {
			budgets[types.ThinkingMinimal] = *customBudgets.Minimal
		}
		if customBudgets.Low != nil {
			budgets[types.ThinkingLow] = *customBudgets.Low
		}
		if customBudgets.Medium != nil {
			budgets[types.ThinkingMedium] = *customBudgets.Medium
		}
		if customBudgets.High != nil {
			budgets[types.ThinkingHigh] = *customBudgets.High
		}
	}
	level := ClampReasoning(&reasoningLevel)
	if level == nil {
		level = &reasoningLevel
	}
	return budgets[*level]
}

// ClampThinkingBudgetToAnswerRoom caps a thinking budget so at least
// MinAnswerTokens remain under a shared response ceiling.
func ClampThinkingBudgetToAnswerRoom(thinkingBudget, ceiling float64) float64 {
	return math.Min(thinkingBudget, math.Max(0, ceiling-MinAnswerTokens))
}

// AdjustMaxTokensForThinking fits a thinking budget inside the response ceiling,
// shrinking the thinking budget when they would otherwise collide.
type MaxTokensForThinking struct {
	MaxTokens      int
	ThinkingBudget int
}

// AdjustMaxTokensForThinking computes the effective max-token and thinking
// budgets for a reasoning level. A nil baseMaxTokens means no explicit caller
// cap, so the model cap is used and thinking is fit inside it.
func AdjustMaxTokensForThinking(baseMaxTokens *float64, modelMaxTokens float64, reasoningLevel types.ThinkingLevel, customBudgets *types.ThinkingBudgets) MaxTokensForThinking {
	thinkingBudget := float64(ThinkingBudgetForLevel(reasoningLevel, customBudgets))
	maxTokens := modelMaxTokens
	if baseMaxTokens != nil {
		maxTokens = math.Min(*baseMaxTokens+thinkingBudget, modelMaxTokens)
	}
	if maxTokens <= thinkingBudget {
		thinkingBudget = ClampThinkingBudgetToAnswerRoom(thinkingBudget, maxTokens)
	}
	return MaxTokensForThinking{MaxTokens: int(maxTokens), ThinkingBudget: int(thinkingBudget)}
}

func intPtr(value int) *int { return &value }

func valueOr(value *int, fallback int) int {
	if value == nil {
		return fallback
	}
	return *value
}
