// This file is a Go port of packages/ai/src/providers/github-copilot.ts from Pi
// at revision f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
package providers

import (
	"encoding/json"

	"github.com/minifish-org/pith/packages/ai"
	"github.com/minifish-org/pith/packages/ai/api"
	"github.com/minifish-org/pith/packages/ai/auth"
	"github.com/minifish-org/pith/packages/ai/auth/oauth"
	authtypes "github.com/minifish-org/pith/packages/ai/auth/types"
	"github.com/minifish-org/pith/packages/ai/catalog"
	"github.com/minifish-org/pith/packages/ai/types"
)

// filterGitHubCopilotModels keeps only the models an OAuth credential reports
// as available. A credential without a valid availableModelIds list keeps the
// full catalog.
func filterGitHubCopilotModels(models []types.Model, credential authtypes.Credential) []types.Model {
	oauthCredential, ok := credential.(*authtypes.OAuthCredential)
	if !ok || oauthCredential == nil {
		return models
	}
	raw, ok := oauthCredential.Extra["availableModelIds"]
	if !ok {
		return models
	}
	var availableModelIDs []string
	if err := json.Unmarshal(raw, &availableModelIDs); err != nil {
		return models
	}
	available := make(map[string]bool, len(availableModelIDs))
	for _, id := range availableModelIDs {
		available[id] = true
	}
	filtered := make([]types.Model, 0, len(models))
	for _, model := range models {
		if available[model.Id] {
			filtered = append(filtered, model)
		}
	}
	return filtered
}

// GitHubCopilotProvider builds the GitHub Copilot provider.
func GitHubCopilotProvider() ai.Provider {
	apiKey := auth.EnvApiKeyAuth("GitHub Copilot token", []string{"COPILOT_GITHUB_TOKEN"})
	copilotOAuth := auth.LazyOAuth(auth.LazyOAuthInput{
		Name:           "GitHub Copilot",
		IsSubscription: true,
		Load:           oauth.LoadGitHubCopilotOAuth,
	})
	return ai.CreateProvider(ai.CreateProviderOptions{
		ID:           types.ProviderGitHubCopilot,
		Name:         "GitHub Copilot",
		BaseURL:      "https://api.individual.githubcopilot.com",
		Auth:         authtypes.ProviderAuth{APIKey: &apiKey, OAuth: &copilotOAuth},
		Models:       catalogModelList(catalog.GITHUB_COPILOT_MODELS),
		FilterModels: filterGitHubCopilotModels,
		APIs: map[types.Api]types.ProviderStreams{
			types.ApiAnthropicMessages: api.AnthropicMessagesApi(),
			types.ApiOpenAICompletions: api.OpenAICompletionsApi(),
			types.ApiOpenAIResponses:   api.OpenAIResponsesApi(),
		},
	})
}
