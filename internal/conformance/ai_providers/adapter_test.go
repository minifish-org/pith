package conformance

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/minifish-org/pith/packages/ai/providers"
	"github.com/minifish-org/pith/packages/ai/types"
	"golang.org/x/text/collate"
	"golang.org/x/text/language"
)

// RunCase is the only bridge the frozen judge uses. It translates one input
// operation into calls against the real exported Go SDK and returns the
// normalized result. It never reads expected results, golden files or TS
// sources, and it does not implement SDK behavior itself.
//
// The ai-providers batch defines the `provider` operation: construct the named
// built-in provider and describe its id, name, catalog models and stream
// capabilities. Any other operation is reported as an error.
func RunCase(ctx context.Context, input json.RawMessage) (json.RawMessage, error) {
	_ = ctx
	var envelope struct {
		Op string `json:"op"`
		Fn string `json:"fn"`
	}
	if err := json.Unmarshal(input, &envelope); err != nil {
		return nil, fmt.Errorf("conformance: invalid input: %w", err)
	}
	switch envelope.Op {
	case "provider":
		return runProviderCase(envelope.Fn)
	default:
		return nil, fmt.Errorf("conformance: unsupported operation %q", envelope.Op)
	}
}

// undefinedValue is the wire form of a JavaScript `undefined` field.
func undefinedValue() map[string]any { return map[string]any{"$undefined": true} }

func providerFor(fn string) (any, error) {
	switch fn {
	case "amazonBedrockProvider":
		return providers.AmazonBedrockProvider(), nil
	case "antLingProvider":
		return providers.AntLingProvider(), nil
	case "anthropicProvider":
		return providers.AnthropicProvider(), nil
	case "azureOpenAIResponsesProvider":
		return providers.AzureOpenAIResponsesProvider(), nil
	case "basetenProvider":
		return providers.BasetenProvider(), nil
	case "cerebrasProvider":
		return providers.CerebrasProvider(), nil
	case "cloudflareAIGatewayProvider":
		return providers.CloudflareAIGatewayProvider(), nil
	case "cloudflareWorkersAIProvider":
		return providers.CloudflareWorkersAIProvider(), nil
	case "deepseekProvider":
		return providers.DeepseekProvider(), nil
	case "fauxProvider":
		return providers.FauxProvider().Provider, nil
	case "fireworksProvider":
		return providers.FireworksProvider(), nil
	case "githubCopilotProvider":
		return providers.GitHubCopilotProvider(), nil
	case "googleVertexProvider":
		return providers.GoogleVertexProvider(), nil
	case "googleProvider":
		return providers.GoogleProvider(), nil
	case "groqProvider":
		return providers.GroqProvider(), nil
	case "huggingfaceProvider":
		return providers.HuggingfaceProvider(), nil
	case "kimiCodingProvider":
		return providers.KimiCodingProvider(), nil
	case "metaProvider":
		return providers.MetaProvider(), nil
	case "minimaxCnProvider":
		return providers.MinimaxCnProvider(), nil
	case "minimaxProvider":
		return providers.MinimaxProvider(), nil
	case "mistralProvider":
		return providers.MistralProvider(), nil
	case "moonshotaiCnProvider":
		return providers.MoonshotaiCnProvider(), nil
	case "moonshotaiProvider":
		return providers.MoonshotaiProvider(), nil
	case "nvidiaProvider":
		return providers.NvidiaProvider(), nil
	case "openaiCodexProvider":
		return providers.OpenAICodexProvider(), nil
	case "openaiProvider":
		return providers.OpenAIProvider(), nil
	case "opencodeGoProvider":
		return providers.OpencodeGoProvider(), nil
	case "opencodeProvider":
		return providers.OpencodeProvider(), nil
	case "openrouterImagesProvider":
		return providers.OpenrouterImagesProvider(), nil
	case "openrouterProvider":
		return providers.OpenrouterProvider(), nil
	case "qwenTokenPlanCnProvider":
		return providers.QwenTokenPlanCnProvider(), nil
	case "qwenTokenPlanIndividualProvider":
		return providers.QwenTokenPlanIndividualProvider(), nil
	case "qwenTokenPlanProvider":
		return providers.QwenTokenPlanProvider(), nil
	case "radiusProvider":
		return providers.RadiusProvider(providers.RadiusProviderOptions{}), nil
	case "togetherProvider":
		return providers.TogetherProvider(), nil
	case "vercelAIGatewayProvider":
		return providers.VercelAIGatewayProvider(), nil
	case "xaiProvider":
		return providers.XaiProvider(), nil
	case "xiaomiTokenPlanAmsProvider":
		return providers.XiaomiTokenPlanAmsProvider(), nil
	case "xiaomiTokenPlanCnProvider":
		return providers.XiaomiTokenPlanCnProvider(), nil
	case "xiaomiTokenPlanSgpProvider":
		return providers.XiaomiTokenPlanSgpProvider(), nil
	case "xiaomiProvider":
		return providers.XiaomiProvider(), nil
	case "zaiCodingCnProvider":
		return providers.ZaiCodingCnProvider(), nil
	case "zaiProvider":
		return providers.ZaiProvider(), nil
	default:
		return nil, fmt.Errorf("conformance: unknown provider factory %q", fn)
	}
}

func runProviderCase(fn string) (json.RawMessage, error) {
	value, err := providerFor(fn)
	if err != nil {
		return nil, err
	}
	switch provider := value.(type) {
	case interface {
		ID() string
		Name() string
		GetModels() []types.Model
	}:
		models := encodeModels(provider.GetModels())
		return json.Marshal(map[string]any{
			"id":           provider.ID(),
			"name":         provider.Name(),
			"models":       models,
			"stream":       true,
			"streamSimple": true,
		})
	case interface {
		ID() string
		Name() string
		GetModels() []types.ImagesModel
	}:
		models := encodeImageModels(provider.GetModels())
		return json.Marshal(map[string]any{
			"id":           provider.ID(),
			"name":         provider.Name(),
			"models":       models,
			"stream":       false,
			"streamSimple": false,
		})
	default:
		return nil, fmt.Errorf("conformance: provider %q has an unsupported shape", fn)
	}
}

var providerCollator = collate.New(language.English)

func sortModelMaps(models []map[string]any) {
	sort.SliceStable(models, func(i, j int) bool {
		left, _ := models[i]["id"].(string)
		right, _ := models[j]["id"].(string)
		return providerCollator.CompareString(left, right) < 0
	})
}

func modalityStrings(input []types.ModelInputModality) []string {
	values := make([]string, 0, len(input))
	for _, modality := range input {
		values = append(values, string(modality))
	}
	return values
}

func encodeModels(models []types.Model) []map[string]any {
	out := make([]map[string]any, 0, len(models))
	for _, model := range models {
		out = append(out, map[string]any{
			"id":            model.Id,
			"api":           string(model.Api),
			"provider":      string(model.Provider),
			"reasoning":     model.Reasoning,
			"input":         modalityStrings(model.Input),
			"cost":          model.Cost,
			"contextWindow": model.ContextWindow,
			"maxTokens":     model.MaxTokens,
		})
	}
	sortModelMaps(out)
	return out
}

// encodeImageModels describes image models, whose upstream DTO omits the
// text-model reasoning/contextWindow/maxTokens fields. Those fields are
// undefined, not zero.
func encodeImageModels(models []types.ImagesModel) []map[string]any {
	out := make([]map[string]any, 0, len(models))
	for _, model := range models {
		out = append(out, map[string]any{
			"id":            model.Id,
			"api":           string(model.Api),
			"provider":      string(model.Provider),
			"reasoning":     undefinedValue(),
			"input":         modalityStrings(model.Input),
			"cost":          model.Cost,
			"contextWindow": undefinedValue(),
			"maxTokens":     undefinedValue(),
		})
	}
	sortModelMaps(out)
	return out
}
