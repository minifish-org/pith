// This file is a Go port of packages/ai/src/providers/openai.ts from Pi at
// revision f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
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

// OpenAIProvider builds the OpenAI provider.
func OpenAIProvider() ai.Provider {
	return ai.CreateProvider(ai.CreateProviderOptions{
		ID:      types.ProviderOpenAI,
		Name:    "OpenAI",
		BaseURL: "https://api.openai.com/v1",
		Auth:    envAuth("OpenAI API key", "OPENAI_API_KEY"),
		Models:  catalogModelList(catalog.OPENAI_MODELS),
		API:     api.OpenAIResponsesApi(),
	})
}
