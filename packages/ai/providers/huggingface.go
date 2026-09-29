// This file is a Go port of packages/ai/src/providers/huggingface.ts from Pi at
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

// HuggingfaceProvider builds the Hugging Face provider.
func HuggingfaceProvider() ai.Provider {
	return ai.CreateProvider(ai.CreateProviderOptions{
		ID:      types.ProviderHuggingFace,
		Name:    "Hugging Face",
		BaseURL: "https://router.huggingface.co/v1",
		Auth:    envAuth("Hugging Face token", "HF_TOKEN"),
		Models:  catalogModelList(catalog.HUGGINGFACE_MODELS),
		API:     api.OpenAICompletionsApi(),
	})
}
