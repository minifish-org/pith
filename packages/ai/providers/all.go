// This file is a Go port of packages/ai/src/providers/all.ts from Pi at revision
// f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// The built-in provider barrel: typed reads of the generated catalog plus the
// freshly constructed built-in provider and image-provider collections. The
// upstream `radiusProvider` re-export is the real declaration in radius.go.
package providers

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/minifish-org/pith/packages/ai"
	"github.com/minifish-org/pith/packages/ai/auth"
	authtypes "github.com/minifish-org/pith/packages/ai/auth/types"
	"github.com/minifish-org/pith/packages/ai/catalog"
	"github.com/minifish-org/pith/packages/ai/types"
)

// BuiltinProvider is the set of providers present in the generated catalog.
// Upstream `keyof typeof MODELS`; the Go form is the closed KnownProvider
// vocabulary shared by the model DTOs.
type BuiltinProvider = types.KnownProvider

// signalErrorf builds an error with the given format. It keeps provider login
// validation messages close to the upstream wording.
func signalErrorf(format string, args ...any) error { return fmt.Errorf(format, args...) }

// envAuth builds the standard env-api-key provider auth, including the
// interactive login that prompts for the key.
func envAuth(name string, envVars ...string) authtypes.ProviderAuth {
	apiKey := auth.EnvApiKeyAuth(name, envVars)
	return authtypes.ProviderAuth{APIKey: &apiKey}
}

// catalogModelList returns the typed models of a flattened catalog in a stable
// id order. The upstream catalog object preserves JSON insertion order; a Go
// map does not, so the port sorts by id (already a deliberate, documented
// difference of the catalog leaf).
func catalogModelList(entries catalog.ModelCatalog) []types.Model {
	ids := make([]string, 0, len(entries))
	for id := range entries {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	models := make([]types.Model, 0, len(ids))
	for _, id := range ids {
		models = append(models, entries[id].Model)
	}
	return models
}

// getBuiltinCatalog returns the catalog of a builtin provider, or nil.
func getBuiltinCatalog(provider BuiltinProvider) catalog.ModelCatalog {
	return catalog.MODELS[types.ProviderId(provider)]
}

// GetBuiltinModel is the typed read of the generated built-in catalog. It
// returns nil for an unknown provider or model id.
func GetBuiltinModel(provider BuiltinProvider, modelID string) *types.Model {
	models := getBuiltinCatalog(provider)
	if models == nil {
		return nil
	}
	entry, ok := models[modelID]
	if !ok {
		return nil
	}
	model := entry.Model
	return &model
}

// GetBuiltinProviders returns the provider ids present in the generated catalog.
// The upstream `Object.keys` order is not representable on a Go map; the port
// returns a sorted, deterministic list.
func GetBuiltinProviders() []BuiltinProvider {
	providers := make([]BuiltinProvider, 0, len(catalog.MODELS))
	for provider := range catalog.MODELS {
		providers = append(providers, BuiltinProvider(provider))
	}
	sort.Slice(providers, func(i, j int) bool { return providers[i] < providers[j] })
	return providers
}

// GetBuiltinModelDataGeneratedAt returns the generation timestamp shared by all
// built-in catalogs, or nil when the manifest is missing or unparsable.
func GetBuiltinModelDataGeneratedAt() *float64 {
	raw := catalog.MustDocument("manifest.json")
	var manifest struct {
		GeneratedAt string `json:"generatedAt"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, manifest.GeneratedAt)
	if err != nil {
		return nil
	}
	value := float64(parsed.UnixMilli())
	return &value
}

// GetBuiltinModels returns every catalog model of a provider, or an empty slice
// for an unknown provider.
func GetBuiltinModels(provider BuiltinProvider) []types.Model {
	models := getBuiltinCatalog(provider)
	if models == nil {
		return []types.Model{}
	}
	return catalogModelList(models)
}

// BuiltinProviders returns all built-in providers, freshly constructed.
func BuiltinProviders() []ai.Provider {
	return []ai.Provider{
		AmazonBedrockProvider(),
		AntLingProvider(),
		AnthropicProvider(),
		AzureOpenAIResponsesProvider(),
		BasetenProvider(),
		CerebrasProvider(),
		CloudflareAIGatewayProvider(),
		CloudflareWorkersAIProvider(),
		DeepseekProvider(),
		FireworksProvider(),
		GitHubCopilotProvider(),
		GoogleProvider(),
		GoogleVertexProvider(),
		GroqProvider(),
		HuggingfaceProvider(),
		KimiCodingProvider(),
		MetaProvider(),
		MinimaxProvider(),
		MinimaxCnProvider(),
		MistralProvider(),
		MoonshotaiProvider(),
		MoonshotaiCnProvider(),
		NvidiaProvider(),
		OpenAIProvider(),
		OpenAICodexProvider(),
		OpencodeProvider(),
		OpencodeGoProvider(),
		OpenrouterProvider(),
		QwenTokenPlanProvider(),
		QwenTokenPlanCnProvider(),
		QwenTokenPlanIndividualProvider(),
		RadiusProvider(RadiusProviderOptions{}),
		TogetherProvider(),
		VercelAIGatewayProvider(),
		XaiProvider(),
		XiaomiProvider(),
		XiaomiTokenPlanAmsProvider(),
		XiaomiTokenPlanCnProvider(),
		XiaomiTokenPlanSgpProvider(),
		ZaiProvider(),
		ZaiCodingCnProvider(),
	}
}

// BuiltinModels returns a Models collection with every built-in provider
// registered.
func BuiltinModels(options *ai.CreateModelsOptions) ai.MutableModels {
	models := ai.CreateModels(options)
	for _, provider := range BuiltinProviders() {
		models.SetProvider(provider)
	}
	return models
}

// BuiltinImagesProviders returns all built-in image-generation providers,
// freshly constructed.
func BuiltinImagesProviders() []ai.ImagesProvider {
	return []ai.ImagesProvider{OpenrouterImagesProvider()}
}

// BuiltinImagesModels returns an ImagesModels collection with every built-in
// image-generation provider registered.
func BuiltinImagesModels(options *ai.CreateModelsOptions) ai.MutableImagesModels {
	models := ai.CreateImagesModels(options)
	for _, provider := range BuiltinImagesProviders() {
		models.SetProvider(provider)
	}
	return models
}
