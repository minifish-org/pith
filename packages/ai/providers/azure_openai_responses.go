// This file is a Go port of packages/ai/src/providers/azure-openai-responses.ts
// from Pi at revision f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
package providers

import (
	"github.com/minifish-org/pith/packages/ai"
	"github.com/minifish-org/pith/packages/ai/api"
	"github.com/minifish-org/pith/packages/ai/catalog"
	"github.com/minifish-org/pith/packages/ai/types"
)

// AzureOpenAIResponsesProvider builds the Azure OpenAI provider.
func AzureOpenAIResponsesProvider() ai.Provider {
	return ai.CreateProvider(ai.CreateProviderOptions{
		ID:     types.ProviderAzureOpenAIResponses,
		Name:   "Azure OpenAI",
		Auth:   envAuth("Azure OpenAI API key", "AZURE_OPENAI_API_KEY"),
		Models: catalogModelList(catalog.AZURE_OPENAI_RESPONSES_MODELS),
		API:    api.AzureOpenAIResponsesApi(),
	})
}
