// This file is a Go port of packages/ai/src/providers/minimax.ts from Pi at
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

// MinimaxProvider builds the MiniMax provider.
func MinimaxProvider() ai.Provider {
	return ai.CreateProvider(ai.CreateProviderOptions{
		ID:      types.ProviderMinimax,
		Name:    "MiniMax",
		BaseURL: "https://api.minimax.io/anthropic",
		Auth:    envAuth("MiniMax API key", "MINIMAX_API_KEY"),
		Models:  catalogModelList(catalog.MINIMAX_MODELS),
		API:     api.AnthropicMessagesApi(),
	})
}
