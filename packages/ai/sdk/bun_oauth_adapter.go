// This file is a Go port of packages/ai/src/bun-oauth.ts from Pi at revision
// f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// bun-oauth.ts statically embeds the OAuth flows into the standalone Bun binary
// so the bundler-opaque dynamic loaders never run there. Go links the flows
// directly already, so this registers the same concrete flows through the
// bundled-loader seam used by standalone builds.
package sdk

import (
	"github.com/minifish-org/pith/packages/ai/auth/oauth"
	authtypes "github.com/minifish-org/pith/packages/ai/auth/types"
)

// RegisterBunOAuthFlows registers the OAuth flows statically embedded in the
// standalone binary.
//
// Ports `registerBunOAuthFlows` from packages/ai/src/bun-oauth.ts.
func RegisterBunOAuthFlows() {
	oauth.RegisterBundledOAuthFlowLoaders(oauth.OAuthFlowLoaders{
		Anthropic: func() (*authtypes.OAuthAuth, error) {
			return oauth.AnthropicOAuth, nil
		},
		OpenAICodex: func() (*authtypes.OAuthAuth, error) {
			return oauth.OpenAICodexOAuth, nil
		},
		GitHubCopilot: func() (*authtypes.OAuthAuth, error) {
			return oauth.GitHubCopilotOAuth, nil
		},
		OpenRouter: func() (*authtypes.OAuthAuth, error) {
			return oauth.OpenRouterOAuth, nil
		},
		KimiCoding: func() (*authtypes.OAuthAuth, error) {
			return oauth.KimiCodingOAuth, nil
		},
		Meta: func() (*authtypes.OAuthAuth, error) {
			return oauth.MetaOAuth, nil
		},
		Xai: func() (*authtypes.OAuthAuth, error) {
			return oauth.XaiOAuth, nil
		},
		Radius: func(options oauth.RadiusOAuthOptions) (*authtypes.OAuthAuth, error) {
			return oauth.CreateRadiusOAuth(options), nil
		},
	})
}
