// This file is a Go port of packages/ai/src/auth/oauth/load.ts from Pi at
// revision f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// Upstream loads OAuth flow modules through a bundler-opaque dynamic import so
// Node-only flow code stays out of browser bundles. Go links the concrete
// flows directly; RegisterBundledOAuthFlowLoaders remains available for
// standalone builds that want to override the bundled implementations.
package oauth

import (
	"sync"

	authtypes "github.com/minifish-org/pith/packages/ai/auth/types"
)

// OAuthFlowLoaders is the set of statically bundled OAuth flows.
type OAuthFlowLoaders struct {
	Anthropic     func() (*authtypes.OAuthAuth, error)
	OpenAICodex   func() (*authtypes.OAuthAuth, error)
	GitHubCopilot func() (*authtypes.OAuthAuth, error)
	OpenRouter    func() (*authtypes.OAuthAuth, error)
	KimiCoding    func() (*authtypes.OAuthAuth, error)
	Meta          func() (*authtypes.OAuthAuth, error)
	Xai           func() (*authtypes.OAuthAuth, error)
	Radius        func(options RadiusOAuthOptions) (*authtypes.OAuthAuth, error)
}

var (
	bundledLoadersMu sync.RWMutex
	bundledLoaders   *OAuthFlowLoaders
)

// RegisterBundledOAuthFlowLoaders registers statically bundled OAuth flows for
// standalone binaries.
//
// Ports `registerBundledOAuthFlowLoaders` from
// packages/ai/src/auth/oauth/load.ts.
func RegisterBundledOAuthFlowLoaders(loaders OAuthFlowLoaders) {
	bundledLoadersMu.Lock()
	defer bundledLoadersMu.Unlock()
	copy := loaders
	bundledLoaders = &copy
}

func bundled() *OAuthFlowLoaders {
	bundledLoadersMu.RLock()
	defer bundledLoadersMu.RUnlock()
	return bundledLoaders
}

// LoadAnthropicOAuth returns the Anthropic OAuth flow.
func LoadAnthropicOAuth() (*authtypes.OAuthAuth, error) {
	if loaders := bundled(); loaders != nil && loaders.Anthropic != nil {
		return loaders.Anthropic()
	}
	return AnthropicOAuth, nil
}

// LoadOpenAICodexOAuth returns the OpenAI Codex OAuth flow.
func LoadOpenAICodexOAuth() (*authtypes.OAuthAuth, error) {
	if loaders := bundled(); loaders != nil && loaders.OpenAICodex != nil {
		return loaders.OpenAICodex()
	}
	return OpenAICodexOAuth, nil
}

// LoadGitHubCopilotOAuth returns the GitHub Copilot OAuth flow.
func LoadGitHubCopilotOAuth() (*authtypes.OAuthAuth, error) {
	if loaders := bundled(); loaders != nil && loaders.GitHubCopilot != nil {
		return loaders.GitHubCopilot()
	}
	return GitHubCopilotOAuth, nil
}

// LoadOpenRouterOAuth returns the OpenRouter OAuth flow.
func LoadOpenRouterOAuth() (*authtypes.OAuthAuth, error) {
	if loaders := bundled(); loaders != nil && loaders.OpenRouter != nil {
		return loaders.OpenRouter()
	}
	return OpenRouterOAuth, nil
}

// LoadKimiCodingOAuth returns the Kimi Code OAuth flow.
func LoadKimiCodingOAuth() (*authtypes.OAuthAuth, error) {
	if loaders := bundled(); loaders != nil && loaders.KimiCoding != nil {
		return loaders.KimiCoding()
	}
	return KimiCodingOAuth, nil
}

// LoadMetaOAuth returns the Meta OAuth flow.
func LoadMetaOAuth() (*authtypes.OAuthAuth, error) {
	if loaders := bundled(); loaders != nil && loaders.Meta != nil {
		return loaders.Meta()
	}
	return MetaOAuth, nil
}

// LoadXaiOAuth returns the xAI OAuth flow.
func LoadXaiOAuth() (*authtypes.OAuthAuth, error) {
	if loaders := bundled(); loaders != nil && loaders.Xai != nil {
		return loaders.Xai()
	}
	return XaiOAuth, nil
}

// LoadRadiusOAuth builds the Radius gateway OAuth flow.
func LoadRadiusOAuth(options RadiusOAuthOptions) (*authtypes.OAuthAuth, error) {
	if loaders := bundled(); loaders != nil && loaders.Radius != nil {
		return loaders.Radius(options)
	}
	return CreateRadiusOAuth(options), nil
}
