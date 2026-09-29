// This file is a Go port of packages/ai/src/providers/opencode.ts from Pi at
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

// OpencodeProvider builds the OpenCode Zen provider.
func OpencodeProvider() ai.Provider {
	return ai.CreateProvider(ai.CreateProviderOptions{
		ID:     types.ProviderOpencode,
		Name:   "OpenCode Zen",
		Auth:   envAuth("OpenCode API key", "OPENCODE_API_KEY"),
		Models: catalogModelList(catalog.OPENCODE_MODELS),
		APIs: map[types.Api]types.ProviderStreams{
			types.ApiAnthropicMessages:  WithOpenCodeSessionHeader(api.AnthropicMessagesApi()),
			types.ApiGoogleGenerativeAI: WithOpenCodeSessionHeader(api.GoogleGenerativeAIApi()),
			types.ApiOpenAICompletions:  WithOpenCodeSessionHeader(api.OpenAICompletionsApi()),
			types.ApiOpenAIResponses:    WithOpenCodeSessionHeader(api.OpenAIResponsesApi()),
		},
	})
}
