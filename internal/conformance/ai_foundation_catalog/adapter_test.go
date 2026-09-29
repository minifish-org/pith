package conformance

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/minifish-org/pith/packages/ai/catalog"
)

// RunCase is the only bridge the frozen judge uses for the ai-foundation-catalog
// batch. It translates one input operation into calls against the real exported
// Go SDK and returns the normalized result. It never reads expected results,
// golden files or TS sources, and it does not reimplement catalog behavior.
//
// The supported operation is `catalog`: the input names an upstream source file
// and export symbol, and the result is the flattened catalog value exported by
// the Go catalog package. Any other operation is reported as an error rather
// than silently succeeding.
func RunCase(ctx context.Context, input json.RawMessage) (json.RawMessage, error) {
	_ = ctx
	var envelope struct {
		Op string `json:"op"`
	}
	if err := json.Unmarshal(input, &envelope); err != nil {
		return nil, fmt.Errorf("conformance: invalid input: %w", err)
	}

	switch envelope.Op {
	case "catalog":
		return runCatalogCase(input)
	default:
		return nil, fmt.Errorf("conformance: unsupported operation %q", envelope.Op)
	}
}

// runCatalogCase resolves the requested upstream export to its real Go catalog
// declaration and marshals it. The lookup is by upstream symbol name only; it
// does not encode any expected value.
func runCatalogCase(input json.RawMessage) (json.RawMessage, error) {
	var request struct {
		File   string `json:"file"`
		Symbol string `json:"symbol"`
	}
	if err := json.Unmarshal(input, &request); err != nil {
		return nil, fmt.Errorf("conformance: invalid catalog input: %w", err)
	}

	value, ok := catalogSymbols[request.Symbol]
	if !ok {
		return nil, fmt.Errorf("conformance: unknown catalog symbol %q", request.Symbol)
	}
	return json.Marshal(value)
}

// catalogSymbols maps every upstream catalog export name to the Go declaration
// in packages/ai/catalog that ports it.
var catalogSymbols = map[string]any{
	"IMAGE_MODELS":                      catalog.IMAGE_MODELS,
	"MODELS":                            catalog.MODELS,
	"AMAZON_BEDROCK_MODELS":             catalog.AMAZON_BEDROCK_MODELS,
	"ANT_LING_MODELS":                   catalog.ANT_LING_MODELS,
	"ANTHROPIC_MODELS":                  catalog.ANTHROPIC_MODELS,
	"AZURE_OPENAI_RESPONSES_MODELS":     catalog.AZURE_OPENAI_RESPONSES_MODELS,
	"BASETEN_MODELS":                    catalog.BASETEN_MODELS,
	"CEREBRAS_MODELS":                   catalog.CEREBRAS_MODELS,
	"CLOUDFLARE_AI_GATEWAY_MODELS":      catalog.CLOUDFLARE_AI_GATEWAY_MODELS,
	"CLOUDFLARE_WORKERS_AI_MODELS":      catalog.CLOUDFLARE_WORKERS_AI_MODELS,
	"DEEPSEEK_MODELS":                   catalog.DEEPSEEK_MODELS,
	"FIREWORKS_MODELS":                  catalog.FIREWORKS_MODELS,
	"GITHUB_COPILOT_MODELS":             catalog.GITHUB_COPILOT_MODELS,
	"GOOGLE_VERTEX_MODELS":              catalog.GOOGLE_VERTEX_MODELS,
	"GOOGLE_MODELS":                     catalog.GOOGLE_MODELS,
	"GROQ_MODELS":                       catalog.GROQ_MODELS,
	"HUGGINGFACE_MODELS":                catalog.HUGGINGFACE_MODELS,
	"KIMI_CODING_MODELS":                catalog.KIMI_CODING_MODELS,
	"META_MODELS":                       catalog.META_MODELS,
	"MINIMAX_CN_MODELS":                 catalog.MINIMAX_CN_MODELS,
	"MINIMAX_MODELS":                    catalog.MINIMAX_MODELS,
	"MISTRAL_MODELS":                    catalog.MISTRAL_MODELS,
	"MOONSHOTAI_CN_MODELS":              catalog.MOONSHOTAI_CN_MODELS,
	"MOONSHOTAI_MODELS":                 catalog.MOONSHOTAI_MODELS,
	"NVIDIA_MODELS":                     catalog.NVIDIA_MODELS,
	"OPENAI_CODEX_MODELS":               catalog.OPENAI_CODEX_MODELS,
	"OPENAI_MODELS":                     catalog.OPENAI_MODELS,
	"OPENCODE_GO_MODELS":                catalog.OPENCODE_GO_MODELS,
	"OPENCODE_MODELS":                   catalog.OPENCODE_MODELS,
	"OPENROUTER_MODELS":                 catalog.OPENROUTER_MODELS,
	"QWEN_TOKEN_PLAN_CN_MODELS":         catalog.QWEN_TOKEN_PLAN_CN_MODELS,
	"QWEN_TOKEN_PLAN_INDIVIDUAL_MODELS": catalog.QWEN_TOKEN_PLAN_INDIVIDUAL_MODELS,
	"QWEN_TOKEN_PLAN_MODELS":            catalog.QWEN_TOKEN_PLAN_MODELS,
	"RADIUS_MODELS":                     catalog.RADIUS_MODELS,
	"TOGETHER_MODELS":                   catalog.TOGETHER_MODELS,
	"VERCEL_AI_GATEWAY_MODELS":          catalog.VERCEL_AI_GATEWAY_MODELS,
	"XAI_MODELS":                        catalog.XAI_MODELS,
	"XIAOMI_TOKEN_PLAN_AMS_MODELS":      catalog.XIAOMI_TOKEN_PLAN_AMS_MODELS,
	"XIAOMI_TOKEN_PLAN_CN_MODELS":       catalog.XIAOMI_TOKEN_PLAN_CN_MODELS,
	"XIAOMI_TOKEN_PLAN_SGP_MODELS":      catalog.XIAOMI_TOKEN_PLAN_SGP_MODELS,
	"XIAOMI_MODELS":                     catalog.XIAOMI_MODELS,
	"ZAI_CODING_CN_MODELS":              catalog.ZAI_CODING_CN_MODELS,
	"ZAI_MODELS":                        catalog.ZAI_MODELS,
}
