// Code generated from packages/ai/src/image-models.generated.ts by the Go port.
package catalog

import "github.com/minifish-org/pith/packages/ai/types"

// openRouterImagesBaseURL is the shared OpenRouter image endpoint.
const openRouterImagesBaseURL = "https://openrouter.ai/api/v1"

// imageModel is a small constructor that keeps the generated table declarative.
func imageModel(id, name string, input []types.ModelInputModality, output []types.ImagesModelOutputModality, cost types.ModelCostRates) types.ImagesModel {
	return types.ImagesModel{
		Model: types.Model{
			Id:        id,
			Name:      name,
			Api:       types.ApiOpenRouterImages,
			Provider:  types.ProviderOpenRouterImages,
			BaseUrl:   openRouterImagesBaseURL,
			Reasoning: false,
			Input:     input,
			Cost:      types.ModelCost{ModelCostRates: cost},
		},
		Output: output,
	}
}

// textImageInput is the `["text", "image"]` modality order.
var textImageInput = []types.ModelInputModality{types.ModelInputText, types.ModelInputImage}

// imageTextInput is the `["image", "text"]` modality order.
var imageTextInput = []types.ModelInputModality{types.ModelInputImage, types.ModelInputText}

// textOnlyInput is the `["text"]` modality list.
var textOnlyInput = []types.ModelInputModality{types.ModelInputText}

// imageOnlyOutput is the `["image"]` output list.
var imageOnlyOutput = []types.ImagesModelOutputModality{types.ImagesModelOutputImage}

// imageTextOutput is the `["image", "text"]` output list.
var imageTextOutput = []types.ImagesModelOutputModality{types.ImagesModelOutputImage, types.ImagesModelOutputText}

// IMAGE_MODELS is the two-level image-model catalog, keyed by image provider and
// then by model id.
//
// Ports `IMAGE_MODELS` from packages/ai/src/image-models.generated.ts. The
// upstream constant is a `Record<string, Record<string, ImagesModel>>`; the Go
// form keeps the same nesting with typed keys.
var IMAGE_MODELS = map[types.ImagesProviderId]map[string]types.ImagesModel{
	types.ProviderOpenRouterImages: {
		"black-forest-labs/flux.2-flex": imageModel(
			"black-forest-labs/flux.2-flex", "Black Forest Labs: FLUX.2 Flex",
			textImageInput, imageOnlyOutput, types.ModelCostRates{}),
		"black-forest-labs/flux.2-klein-4b": imageModel(
			"black-forest-labs/flux.2-klein-4b", "Black Forest Labs: FLUX.2 Klein 4B",
			textImageInput, imageOnlyOutput, types.ModelCostRates{}),
		"black-forest-labs/flux.2-max": imageModel(
			"black-forest-labs/flux.2-max", "Black Forest Labs: FLUX.2 Max",
			textImageInput, imageOnlyOutput, types.ModelCostRates{}),
		"black-forest-labs/flux.2-pro": imageModel(
			"black-forest-labs/flux.2-pro", "Black Forest Labs: FLUX.2 Pro",
			textImageInput, imageOnlyOutput, types.ModelCostRates{}),
		"bytedance-seed/seedream-4.5": imageModel(
			"bytedance-seed/seedream-4.5", "ByteDance Seed: Seedream 4.5",
			imageTextInput, imageOnlyOutput, types.ModelCostRates{}),
		"bytedance-seed/seedream-5-0-lite": imageModel(
			"bytedance-seed/seedream-5-0-lite", "ByteDance Seed: Seedream 5.0 Lite",
			textImageInput, imageOnlyOutput, types.ModelCostRates{}),
		"bytedance-seed/seedream-5-0-pro": imageModel(
			"bytedance-seed/seedream-5-0-pro", "ByteDance Seed: Seedream 5.0 Pro",
			textImageInput, imageOnlyOutput, types.ModelCostRates{}),
		"google/gemini-2.5-flash-image": imageModel(
			"google/gemini-2.5-flash-image", "Google: Nano Banana (Gemini 2.5 Flash Image)",
			imageTextInput, imageTextOutput, types.ModelCostRates{Input: 0.3, Output: 2.5, CacheRead: 0.03, CacheWrite: 0.0833333333333333}),
		"google/gemini-3-pro-image": imageModel(
			"google/gemini-3-pro-image", "Google: Nano Banana Pro (Gemini 3 Pro Image)",
			imageTextInput, imageTextOutput, types.ModelCostRates{Input: 2, Output: 12, CacheRead: 0.19999999999999998, CacheWrite: 0.375}),
		"google/gemini-3-pro-image-preview": imageModel(
			"google/gemini-3-pro-image-preview", "Google: Nano Banana Pro (Gemini 3 Pro Image Preview)",
			imageTextInput, imageTextOutput, types.ModelCostRates{Input: 2, Output: 12, CacheRead: 0.19999999999999998, CacheWrite: 0.375}),
		"google/gemini-3.1-flash-image": imageModel(
			"google/gemini-3.1-flash-image", "Google: Nano Banana 2 (Gemini 3.1 Flash Image)",
			imageTextInput, imageTextOutput, types.ModelCostRates{Input: 0.5, Output: 3}),
		"google/gemini-3.1-flash-image-preview": imageModel(
			"google/gemini-3.1-flash-image-preview", "Google: Nano Banana 2 (Gemini 3.1 Flash Image Preview)",
			imageTextInput, imageTextOutput, types.ModelCostRates{Input: 0.5, Output: 3}),
		"google/gemini-3.1-flash-lite-image": imageModel(
			"google/gemini-3.1-flash-lite-image", "Google: Nano Banana 2 Lite (Gemini 3.1 Flash Lite Image)",
			imageTextInput, imageTextOutput, types.ModelCostRates{Input: 0.25, Output: 1.5}),
		"inclusionai/ming-image-0.1-design": imageModel(
			"inclusionai/ming-image-0.1-design", "inclusionAI: Ming Image 0.1 Design",
			textOnlyInput, imageOnlyOutput, types.ModelCostRates{}),
		"krea/krea-2-large": imageModel(
			"krea/krea-2-large", "Krea: Krea 2 Large",
			textImageInput, imageOnlyOutput, types.ModelCostRates{}),
		"krea/krea-2-medium": imageModel(
			"krea/krea-2-medium", "Krea: Krea 2 Medium",
			textImageInput, imageOnlyOutput, types.ModelCostRates{}),
		"krea/krea-2-medium-turbo": imageModel(
			"krea/krea-2-medium-turbo", "Krea: Krea 2 Medium Turbo",
			textImageInput, imageOnlyOutput, types.ModelCostRates{}),
		"meta/muse-image": imageModel(
			"meta/muse-image", "Meta: Muse Image",
			textImageInput, imageOnlyOutput, types.ModelCostRates{}),
		"microsoft/mai-image-2.5": imageModel(
			"microsoft/mai-image-2.5", "Microsoft AI: MAI-Image-2.5",
			textImageInput, imageOnlyOutput, types.ModelCostRates{Input: 5}),
		"microsoft/mai-image-2.5-pro": imageModel(
			"microsoft/mai-image-2.5-pro", "Microsoft AI: MAI-Image-2.5 Pro",
			textImageInput, imageOnlyOutput, types.ModelCostRates{Input: 5}),
		"microsoft/mai-image-2.6": imageModel(
			"microsoft/mai-image-2.6", "Microsoft AI: MAI-Image-2.6",
			textImageInput, imageOnlyOutput, types.ModelCostRates{Input: 5}),
		"microsoft/mai-image-2.6-flash": imageModel(
			"microsoft/mai-image-2.6-flash", "Microsoft AI: MAI-Image-2.6 Flash",
			textImageInput, imageOnlyOutput, types.ModelCostRates{Input: 1.75}),
		"openai/gpt-5-image": imageModel(
			"openai/gpt-5-image", "OpenAI: GPT-5 Image",
			imageTextInput, imageTextOutput, types.ModelCostRates{Input: 10, Output: 10, CacheRead: 1.25}),
		"openai/gpt-5-image-mini": imageModel(
			"openai/gpt-5-image-mini", "OpenAI: GPT-5 Image Mini",
			imageTextInput, imageTextOutput, types.ModelCostRates{Input: 2.5, Output: 2, CacheRead: 0.25}),
		"openai/gpt-5.4-image-2": imageModel(
			"openai/gpt-5.4-image-2", "OpenAI: GPT-5.4 Image 2",
			imageTextInput, imageTextOutput, types.ModelCostRates{Input: 8, Output: 15, CacheRead: 2}),
		"openai/gpt-image-1": imageModel(
			"openai/gpt-image-1", "OpenAI: GPT Image 1",
			textImageInput, imageOnlyOutput, types.ModelCostRates{Input: 10, Output: 10, CacheRead: 1.25}),
		"openai/gpt-image-1-mini": imageModel(
			"openai/gpt-image-1-mini", "OpenAI: GPT Image 1 Mini",
			textImageInput, imageOnlyOutput, types.ModelCostRates{Input: 2.5, Output: 2.5, CacheRead: 0.25}),
		"openai/gpt-image-2": imageModel(
			"openai/gpt-image-2", "OpenAI: GPT Image 2",
			textImageInput, imageOnlyOutput, types.ModelCostRates{Input: 8, Output: 8, CacheRead: 2}),
		"openai/gpt-image-2.5-flare": imageModel(
			"openai/gpt-image-2.5-flare", "OpenAI: GPT Image 2.5 Flare",
			textImageInput, imageOnlyOutput, types.ModelCostRates{Input: 8, Output: 8, CacheRead: 2}),
		"openai/gpt-image-2.5-sunburst": imageModel(
			"openai/gpt-image-2.5-sunburst", "OpenAI: GPT Image 2.5 Sunburst",
			textImageInput, imageOnlyOutput, types.ModelCostRates{Input: 8, Output: 8, CacheRead: 2}),
		"openrouter/auto": imageModel(
			"openrouter/auto", "Auto Router",
			textImageInput, imageTextOutput, types.ModelCostRates{Input: -1000000, Output: -1000000}),
		"openrouter/auto-beta": imageModel(
			"openrouter/auto-beta", "Auto Router (Beta)",
			textImageInput, imageTextOutput, types.ModelCostRates{Input: -1000000, Output: -1000000}),
		"qwen/qwen-image-3": imageModel(
			"qwen/qwen-image-3", "Qwen: Qwen Image 3",
			textImageInput, imageOnlyOutput, types.ModelCostRates{}),
		"qwen/qwen-image-3-pro": imageModel(
			"qwen/qwen-image-3-pro", "Qwen: Qwen Image 3 Pro",
			textImageInput, imageOnlyOutput, types.ModelCostRates{}),
		"recraft/recraft-v3": imageModel(
			"recraft/recraft-v3", "Recraft: Recraft V3",
			textImageInput, imageOnlyOutput, types.ModelCostRates{}),
		"recraft/recraft-v4": imageModel(
			"recraft/recraft-v4", "Recraft: Recraft V4",
			textImageInput, imageOnlyOutput, types.ModelCostRates{}),
		"recraft/recraft-v4-pro": imageModel(
			"recraft/recraft-v4-pro", "Recraft: Recraft V4 Pro",
			textImageInput, imageOnlyOutput, types.ModelCostRates{}),
		"recraft/recraft-v4-pro-vector": imageModel(
			"recraft/recraft-v4-pro-vector", "Recraft: Recraft V4 Pro Vector",
			textImageInput, imageOnlyOutput, types.ModelCostRates{}),
		"recraft/recraft-v4-styles": imageModel(
			"recraft/recraft-v4-styles", "Recraft: Recraft V4 Styles",
			textImageInput, imageOnlyOutput, types.ModelCostRates{}),
		"recraft/recraft-v4-styles-pro": imageModel(
			"recraft/recraft-v4-styles-pro", "Recraft: Recraft V4 Styles Pro",
			textImageInput, imageOnlyOutput, types.ModelCostRates{}),
		"recraft/recraft-v4-styles-pro-vector": imageModel(
			"recraft/recraft-v4-styles-pro-vector", "Recraft: Recraft V4 Styles Pro Vector",
			textImageInput, imageOnlyOutput, types.ModelCostRates{}),
		"recraft/recraft-v4-styles-vector": imageModel(
			"recraft/recraft-v4-styles-vector", "Recraft: Recraft V4 Styles Vector",
			textImageInput, imageOnlyOutput, types.ModelCostRates{}),
		"recraft/recraft-v4-vector": imageModel(
			"recraft/recraft-v4-vector", "Recraft: Recraft V4 Vector",
			textImageInput, imageOnlyOutput, types.ModelCostRates{}),
		"recraft/recraft-v4.1": imageModel(
			"recraft/recraft-v4.1", "Recraft: Recraft V4.1",
			textImageInput, imageOnlyOutput, types.ModelCostRates{}),
		"recraft/recraft-v4.1-pro": imageModel(
			"recraft/recraft-v4.1-pro", "Recraft: Recraft V4.1 Pro",
			textImageInput, imageOnlyOutput, types.ModelCostRates{}),
		"recraft/recraft-v4.1-pro-vector": imageModel(
			"recraft/recraft-v4.1-pro-vector", "Recraft: Recraft V4 Pro Vector",
			textImageInput, imageOnlyOutput, types.ModelCostRates{}),
		"recraft/recraft-v4.1-utility": imageModel(
			"recraft/recraft-v4.1-utility", "Recraft: Recraft V4.1 Utility",
			textImageInput, imageOnlyOutput, types.ModelCostRates{}),
		"recraft/recraft-v4.1-utility-pro": imageModel(
			"recraft/recraft-v4.1-utility-pro", "Recraft: Recraft V4.1 Utility Pro",
			textImageInput, imageOnlyOutput, types.ModelCostRates{}),
		"recraft/recraft-v4.1-vector": imageModel(
			"recraft/recraft-v4.1-vector", "Recraft: Recraft V4.1 Vector",
			textImageInput, imageOnlyOutput, types.ModelCostRates{}),
		"sourceful/riverflow-v2-fast": imageModel(
			"sourceful/riverflow-v2-fast", "Sourceful: Riverflow V2 Fast",
			textImageInput, imageOnlyOutput, types.ModelCostRates{}),
		"sourceful/riverflow-v2-pro": imageModel(
			"sourceful/riverflow-v2-pro", "Sourceful: Riverflow V2 Pro",
			textImageInput, imageOnlyOutput, types.ModelCostRates{}),
		"sourceful/riverflow-v2.5-fast": imageModel(
			"sourceful/riverflow-v2.5-fast", "Sourceful: Riverflow V2.5 Fast",
			textImageInput, imageOnlyOutput, types.ModelCostRates{}),
		"sourceful/riverflow-v2.5-pro": imageModel(
			"sourceful/riverflow-v2.5-pro", "Sourceful: Riverflow V2.5 Pro",
			textImageInput, imageOnlyOutput, types.ModelCostRates{}),
		"x-ai/grok-imagine-image-2.0": imageModel(
			"x-ai/grok-imagine-image-2.0", "xAI: Grok Imagine Image 2.0",
			textImageInput, imageOnlyOutput, types.ModelCostRates{}),
		"x-ai/grok-imagine-image-quality": imageModel(
			"x-ai/grok-imagine-image-quality", "SpaceXAI: Grok Imagine Image Quality",
			textImageInput, imageOnlyOutput, types.ModelCostRates{}),
	},
}
