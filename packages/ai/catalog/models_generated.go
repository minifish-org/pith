// Code generated from packages/ai/src/models.generated.ts by the Go port. The
// per-provider catalogs are defined in the sibling provider files.
package catalog

import "github.com/minifish-org/pith/packages/ai/types"

// MODELS maps every bundled provider identifier to its flattened model catalog.
//
// Ports `MODELS` from packages/ai/src/models.generated.ts. The upstream object is
// keyed by provider id; the Go form uses the typed types.ProviderId so callers
// get the same closed vocabulary.
var MODELS = map[types.ProviderId]ModelCatalog{
	types.ProviderAmazonBedrock:           AMAZON_BEDROCK_MODELS,
	types.ProviderAntLing:                 ANT_LING_MODELS,
	types.ProviderAnthropic:               ANTHROPIC_MODELS,
	types.ProviderAzureOpenAIResponses:    AZURE_OPENAI_RESPONSES_MODELS,
	types.ProviderBaseten:                 BASETEN_MODELS,
	types.ProviderCerebras:                CEREBRAS_MODELS,
	types.ProviderCloudflareAIGateway:     CLOUDFLARE_AI_GATEWAY_MODELS,
	types.ProviderCloudflareWorkersAI:     CLOUDFLARE_WORKERS_AI_MODELS,
	types.ProviderDeepSeek:                DEEPSEEK_MODELS,
	types.ProviderFireworks:               FIREWORKS_MODELS,
	types.ProviderGitHubCopilot:           GITHUB_COPILOT_MODELS,
	types.ProviderGoogle:                  GOOGLE_MODELS,
	types.ProviderGoogleVertex:            GOOGLE_VERTEX_MODELS,
	types.ProviderGroq:                    GROQ_MODELS,
	types.ProviderHuggingFace:             HUGGINGFACE_MODELS,
	types.ProviderKimiCoding:              KIMI_CODING_MODELS,
	types.ProviderMeta:                    META_MODELS,
	types.ProviderMinimax:                 MINIMAX_MODELS,
	types.ProviderMinimaxCN:               MINIMAX_CN_MODELS,
	types.ProviderMistral:                 MISTRAL_MODELS,
	types.ProviderMoonshotAI:              MOONSHOTAI_MODELS,
	types.ProviderMoonshotAICN:            MOONSHOTAI_CN_MODELS,
	types.ProviderNvidia:                  NVIDIA_MODELS,
	types.ProviderOpenAI:                  OPENAI_MODELS,
	types.ProviderOpenAICodex:             OPENAI_CODEX_MODELS,
	types.ProviderOpencode:                OPENCODE_MODELS,
	types.ProviderOpencodeGo:              OPENCODE_GO_MODELS,
	types.ProviderOpenRouter:              OPENROUTER_MODELS,
	types.ProviderQwenTokenPlan:           QWEN_TOKEN_PLAN_MODELS,
	types.ProviderQwenTokenPlanCN:         QWEN_TOKEN_PLAN_CN_MODELS,
	types.ProviderQwenTokenPlanIndividual: QWEN_TOKEN_PLAN_INDIVIDUAL_MODELS,
	types.ProviderRadius:                  RADIUS_MODELS,
	types.ProviderTogether:                TOGETHER_MODELS,
	types.ProviderVercelAIGateway:         VERCEL_AI_GATEWAY_MODELS,
	types.ProviderXAI:                     XAI_MODELS,
	types.ProviderXiaomi:                  XIAOMI_MODELS,
	types.ProviderXiaomiTokenPlanAMS:      XIAOMI_TOKEN_PLAN_AMS_MODELS,
	types.ProviderXiaomiTokenPlanCN:       XIAOMI_TOKEN_PLAN_CN_MODELS,
	types.ProviderXiaomiTokenPlanSGP:      XIAOMI_TOKEN_PLAN_SGP_MODELS,
	types.ProviderZai:                     ZAI_MODELS,
	types.ProviderZaiCodingCN:             ZAI_CODING_CN_MODELS,
}
