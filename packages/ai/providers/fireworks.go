// This file is a Go port of packages/ai/src/providers/fireworks.ts from Pi at
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

// FireworksProvider builds the Fireworks provider. It serves both the
// Anthropic-messages and OpenAI-completions APIs, dispatched per model.
func FireworksProvider() ai.Provider {
	return ai.CreateProvider(ai.CreateProviderOptions{
		ID:      types.ProviderFireworks,
		Name:    "Fireworks",
		BaseURL: "https://api.fireworks.ai/inference",
		Auth:    envAuth("Fireworks API key", "FIREWORKS_API_KEY"),
		Models:  catalogModelList(catalog.FIREWORKS_MODELS),
		APIs: map[types.Api]types.ProviderStreams{
			types.ApiAnthropicMessages: api.AnthropicMessagesApi(),
			types.ApiOpenAICompletions: api.OpenAICompletionsApi(),
		},
	})
}
