// This file is a Go port of packages/ai/src/providers/groq.ts from Pi at
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

// GroqProvider builds the Groq provider.
func GroqProvider() ai.Provider {
	return ai.CreateProvider(ai.CreateProviderOptions{
		ID:      types.ProviderGroq,
		Name:    "Groq",
		BaseURL: "https://api.groq.com/openai/v1",
		Auth:    envAuth("Groq API key", "GROQ_API_KEY"),
		Models:  catalogModelList(catalog.GROQ_MODELS),
		API:     api.OpenAICompletionsApi(),
	})
}
